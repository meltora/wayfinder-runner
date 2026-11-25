package daemon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/previewtunnel"
)

type fakePreviewTunnel struct {
	mu         sync.Mutex
	running    int
	maxRunning int
	starts     int
	resets     []string
	tlsConfigs []*tls.Config
	started    chan struct{}
}

func newFakePreviewTunnel() *fakePreviewTunnel {
	return &fakePreviewTunnel{started: make(chan struct{}, 16)}
}

func (f *fakePreviewTunnel) Run(ctx context.Context) error {
	f.mu.Lock()
	f.starts++
	f.running++
	f.maxRunning = max(f.maxRunning, f.running)
	f.mu.Unlock()
	f.started <- struct{}{}
	<-ctx.Done()
	time.Sleep(10 * time.Millisecond)
	f.mu.Lock()
	f.running--
	f.mu.Unlock()
	return ctx.Err()
}

func (f *fakePreviewTunnel) UpdateTLSConfig(cfg *tls.Config) {
	f.mu.Lock()
	f.tlsConfigs = append(f.tlsConfigs, cfg)
	f.mu.Unlock()
}

func (f *fakePreviewTunnel) ResetPreview(previewID string) {
	f.mu.Lock()
	f.resets = append(f.resets, previewID)
	f.mu.Unlock()
}

func (f *fakePreviewTunnel) state() (running, maxRunning, starts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running, f.maxRunning, f.starts
}

func (f *fakePreviewTunnel) resetIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.resets)
}

func (f *fakePreviewTunnel) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("preview tunnel did not start")
	}
}

type fakeTunnelHosts struct {
	host  journal.PreviewHost
	found bool
	err   error
}

func (hosts fakeTunnelHosts) PreviewHostByPreviewID(_ context.Context, previewID string) (journal.PreviewHost, bool, error) {
	if hosts.err != nil || !hosts.found || hosts.host.PreviewID != previewID {
		return journal.PreviewHost{}, false, hosts.err
	}
	return hosts.host, true, nil
}

type recordingIngress struct {
	mu      sync.Mutex
	address string
	err     error
	calls   []string
}

func (ingress *recordingIngress) IngressAddress(_ context.Context, previewID string) (string, error) {
	ingress.mu.Lock()
	defer ingress.mu.Unlock()
	ingress.calls = append(ingress.calls, previewID)
	return ingress.address, ingress.err
}

func TestPreviewTunnelResolverAdmitsOnlyDeployedLiveOwnedHosts(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	key := strings.Repeat("k", 26)
	deployed := journal.PreviewHost{PreviewID: "preview-a", PreviewKey: key, State: journal.PreviewHostDeployed, ExpiresAt: now.Add(time.Minute)}
	with := func(change func(*journal.PreviewHost)) journal.PreviewHost {
		host := deployed
		change(&host)
		return host
	}
	request := previewtunnel.OpenRequest{PreviewID: "preview-a", PreviewKey: key}
	cases := map[string]struct {
		hosts      fakeTunnelHosts
		request    previewtunnel.OpenRequest
		ingressErr error
		ok         bool
	}{
		"unknown preview":   {fakeTunnelHosts{}, request, nil, false},
		"journal error":     {fakeTunnelHosts{host: deployed, found: true, err: errors.New("journal down")}, request, nil, false},
		"wrong key":         {fakeTunnelHosts{host: deployed, found: true}, previewtunnel.OpenRequest{PreviewID: "preview-a", PreviewKey: strings.Repeat("x", 26)}, nil, false},
		"key prefix":        {fakeTunnelHosts{host: deployed, found: true}, previewtunnel.OpenRequest{PreviewID: "preview-a", PreviewKey: key[:25]}, nil, false},
		"other preview":     {fakeTunnelHosts{host: deployed, found: true}, previewtunnel.OpenRequest{PreviewID: "preview-b", PreviewKey: key}, nil, false},
		"deploying":         {fakeTunnelHosts{host: with(func(h *journal.PreviewHost) { h.State = journal.PreviewHostDeploying }), found: true}, request, nil, false},
		"destroying":        {fakeTunnelHosts{host: with(func(h *journal.PreviewHost) { h.State = journal.PreviewHostDestroying }), found: true}, request, nil, false},
		"destroyed":         {fakeTunnelHosts{host: with(func(h *journal.PreviewHost) { h.State = journal.PreviewHostDestroyed }), found: true}, request, nil, false},
		"uncertain":         {fakeTunnelHosts{host: with(func(h *journal.PreviewHost) { h.State = journal.PreviewHostUncertain }), found: true}, request, nil, false},
		"expired":           {fakeTunnelHosts{host: with(func(h *journal.PreviewHost) { h.ExpiresAt = now.Add(-time.Second) }), found: true}, request, nil, false},
		"expires right now": {fakeTunnelHosts{host: with(func(h *journal.PreviewHost) { h.ExpiresAt = now }), found: true}, request, nil, false},
		"ingress unknown":   {fakeTunnelHosts{host: deployed, found: true}, request, errors.New("no gateway"), false},
		"deployed and live": {fakeTunnelHosts{host: deployed, found: true}, request, nil, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ingress := &recordingIngress{address: "127.0.0.1:41234", err: tc.ingressErr}
			resolver := previewTunnelResolver{hosts: tc.hosts, ingress: ingress, now: func() time.Time { return now }}
			address, ok := resolver.Resolve(context.Background(), tc.request)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (address %q)", ok, tc.ok, address)
			}
			if !ok {
				if address != "" {
					t.Fatalf("address = %q on a refusal", address)
				}
				if tc.ingressErr == nil && len(ingress.calls) != 0 {
					t.Fatalf("ingress looked up for a refused open: %v", ingress.calls)
				}
				return
			}
			if address != "127.0.0.1:41234" || !slices.Equal(ingress.calls, []string{"preview-a"}) {
				t.Fatalf("address = %q, ingress calls = %v", address, ingress.calls)
			}
		})
	}
}

func newPreviewTunnelDaemon(t *testing.T, controller *fakePreviewHostController, tunnel previewTunnel) (*Daemon, *capturedEvents) {
	t.Helper()
	stateDir := t.TempDir()
	events := &capturedEvents{}
	d := &Daemon{
		cfg:       config.Normalize(config.Config{StateDir: stateDir, ControlPlaneHTTPURL: "https://control.example.test"}),
		store:     identity.NewStore(stateDir),
		runnerID:  "runner-1",
		logger:    slog.Default(),
		eventSink: events.sink,
	}
	if controller != nil {
		d.previewHostController = controller
	}
	d.previewTunnel = tunnel
	if _, err := d.store.EnsureKey(); err != nil {
		t.Fatal(err)
	}
	return d, events
}

func TestPublishCapabilityAdvertisesPreviewTunnel(t *testing.T) {
	manifest := publishPreviewHostingManifestWith(t, &fakePreviewHostController{}, newFakePreviewTunnel())
	if manifest.PreviewTunnelTransportVersion != v1.PreviewTunnelTransportVersion || manifest.PreviewHosting == nil {
		t.Fatalf("manifest = %#v", manifest)
	}
	unsigned := manifest
	unsigned.Signature = ""
	encoded, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"max_extension_ms":604800000},"preview_tunnel_transport_version":1`) {
		t.Fatalf("encoded = %s", encoded)
	}
}

func TestPublishCapabilityOmitsPreviewTunnelWithoutPreviewHostingOrTunnel(t *testing.T) {
	invalid := v1.PreviewHostingCapability{ProtocolVersion: 2}
	cases := map[string]v1.CapabilityManifest{
		"hosting disabled":           publishPreviewHostingManifestWith(t, nil, newFakePreviewTunnel()),
		"hosting capability invalid": publishPreviewHostingManifestWith(t, &fakePreviewHostController{capability: &invalid}, newFakePreviewTunnel()),
		"tunnel disabled":            publishPreviewHostingManifestWith(t, &fakePreviewHostController{}, nil),
	}
	for name, manifest := range cases {
		encoded, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if manifest.PreviewTunnelTransportVersion != 0 || strings.Contains(string(encoded), "preview_tunnel") {
			t.Fatalf("%s: encoded = %s", name, encoded)
		}
	}
}

func TestTunnelRunsOnlyWhileControlConnected(t *testing.T) {
	tunnel := newFakePreviewTunnel()
	d, _ := newPreviewTunnelDaemon(t, &fakePreviewHostController{}, tunnel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.OnConnected(ctx)
	tunnel.waitStarted(t)
	if running, _, _ := tunnel.state(); running != 1 {
		t.Fatalf("running after connect = %d", running)
	}
	d.OnConnected(ctx)
	tunnel.waitStarted(t)
	if running, maxRunning, starts := tunnel.state(); running != 1 || maxRunning != 1 || starts != 2 {
		t.Fatalf("after a second connect running=%d max=%d starts=%d", running, maxRunning, starts)
	}

	d.OnDisconnected(errors.New("control stream dropped"))
	if running, _, _ := tunnel.state(); running != 0 {
		t.Fatalf("running after disconnect = %d", running)
	}
	time.Sleep(50 * time.Millisecond)
	if running, _, starts := tunnel.state(); running != 0 || starts != 2 {
		t.Fatalf("tunnel re-dialed while disconnected: running=%d starts=%d", running, starts)
	}

	d.OnConnected(ctx)
	tunnel.waitStarted(t)
	cancel()
	d.stopPreviewTunnel()
	if running, maxRunning, starts := tunnel.state(); running != 0 || maxRunning != 1 || starts != 3 {
		t.Fatalf("after shutdown running=%d max=%d starts=%d", running, maxRunning, starts)
	}
	d.OnConnected(ctx)
	if _, _, starts := tunnel.state(); starts != 3 {
		t.Fatalf("tunnel started on a cancelled daemon context: starts=%d", starts)
	}
}

func TestTunnelDoesNotRunWhenItIsNotAdvertised(t *testing.T) {
	tunnel := newFakePreviewTunnel()
	d, _ := newPreviewTunnelDaemon(t, nil, tunnel)
	d.OnConnected(context.Background())
	d.OnDisconnected(nil)
	if _, _, starts := tunnel.state(); starts != 0 {
		t.Fatalf("tunnel started without preview hosting: starts=%d", starts)
	}
}

func TestNewPreviewTunnelNeedsAnIngressLocator(t *testing.T) {
	d, _ := newPreviewTunnelDaemon(t, &fakePreviewHostController{}, nil)
	d.cfg.ControlPlaneURL = "wss://control.example.test/v1/runners/stream"
	if tunnel := d.newPreviewTunnel(&tls.Config{MinVersion: tls.VersionTLS12}); tunnel != nil {
		t.Fatalf("tunnel = %#v for a controller without an ingress locator", tunnel)
	}
	d.previewHostController = ingressPreviewHostController{fakePreviewHostController: &fakePreviewHostController{}, address: "127.0.0.1:1"}
	if tunnel := d.newPreviewTunnel(&tls.Config{MinVersion: tls.VersionTLS12}); tunnel == nil {
		t.Fatal("no tunnel for a controller with an ingress locator")
	}
	d.cfg.ControlPlaneURL = "https://control.example.test/v1/runners/stream"
	if tunnel := d.newPreviewTunnel(&tls.Config{MinVersion: tls.VersionTLS12}); tunnel != nil {
		t.Fatal("tunnel built from a control url that is not a websocket url")
	}
}

type ingressPreviewHostController struct {
	*fakePreviewHostController
	address string
}

func (controller ingressPreviewHostController) IngressAddress(context.Context, string) (string, error) {
	return controller.address, nil
}

func TestDestroyTransitionResetsTunnelStreams(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		d := jr.d
		tunnel := newFakePreviewTunnel()
		d.previewTunnel = tunnel
		d.previewHostController = &fakePreviewHostController{}
		capturePreviewHostingEvents(d)
		insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDeployed, time.Now().Add(-time.Second), "")
		insertPreviewHostRow(t, d, "preview-b", "b", journal.PreviewHostDeployed, time.Now().Add(time.Hour), "")
		if err := d.expirePreviewHosts(context.Background(), v1.PreviewHostedDestroyExpired); err != nil {
			t.Fatal(err)
		}
		if ids := tunnel.resetIDs(); !slices.Equal(ids, []string{"preview-a"}) {
			t.Fatalf("resets = %v", ids)
		}
	})
	t.Run("emergency stop", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		d := jr.d
		tunnel := newFakePreviewTunnel()
		d.previewTunnel = tunnel
		d.previewHostController = &fakePreviewHostController{}
		capturePreviewHostingEvents(d)
		insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDeployed, time.Now().Add(time.Hour), "")
		insertPreviewHostRow(t, d, "preview-b", "b", journal.PreviewHostDeployed, time.Now().Add(time.Hour), "")
		if err := d.emergencyStopPreviewHosts(context.Background()); err != nil {
			t.Fatal(err)
		}
		ids := tunnel.resetIDs()
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"preview-a", "preview-b"}) {
			t.Fatalf("resets = %v", ids)
		}
	})
	t.Run("deploy finishing after emergency stop", func(t *testing.T) {
		jr, _ := testJobRunner(t)
		d := jr.d
		d.cfg.PreviewHosting.MemoryBudgetMB = 1 << 20
		allowPreviewHostingPause(t, d)
		tunnel := newFakePreviewTunnel()
		d.previewTunnel = tunnel
		controller := &fakePreviewHostController{
			deployOutcome: deployedPreviewOutcome(), started: make(chan struct{}, 1), release: make(chan struct{}),
		}
		d.previewHostController = controller
		capturePreviewHostingEvents(d)
		command := validPreviewDeployCommand()
		trustPreviewDeployCommits(t, d, *command.PreviewDeploy)
		done := make(chan error, 1)
		go func() {
			done <- d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("1")}, command)
		}()
		<-controller.started
		if err := d.executeCommand(context.Background(), v1.Envelope{}, v1.CommandPayload{Command: v1.CmdEmergencyStop}); err != nil {
			t.Fatal(err)
		}
		close(controller.release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if ids := tunnel.resetIDs(); !slices.Contains(ids, command.PreviewDeploy.PreviewID) {
			t.Fatalf("resets = %v", ids)
		}
	})
	t.Run("hosted lifecycle destroy", func(t *testing.T) {
		deploy := *validPreviewDeployCommand().PreviewDeploy
		jr, _ := testJobRunner(t)
		d := jr.d
		tunnel := newFakePreviewTunnel()
		d.previewTunnel = tunnel
		d.previewHostController = &fakePreviewHostController{lifecycleOutcome: v1.PreviewLifecycleResultOutcome{
			Kind: v1.PreviewLifecycleSucceeded,
			ResourceState: &v1.PreviewLifecycleResourceState{
				Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: v1.PreviewLifecycleDestroyed,
			},
		}}
		insertTestPreviewHost(t, d, deploy, journal.PreviewHostDeployed, []byte(`{}`))
		capturePreviewHostingEvents(d)
		destroy := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleDestroy, deploy)
		if err := d.executeCommand(context.Background(), v1.Envelope{ID: hostedLifecycleCommandID("6")}, destroy); err != nil {
			t.Fatal(err)
		}
		if ids := tunnel.resetIDs(); len(ids) == 0 || !slices.Equal(slices.Compact(ids), []string{deploy.PreviewID}) {
			t.Fatalf("resets = %v", ids)
		}
	})
	t.Run("hosted lifecycle extend", func(t *testing.T) {
		deploy := *validPreviewDeployCommand().PreviewDeploy
		jr, _ := testJobRunner(t)
		d := jr.d
		tunnel := newFakePreviewTunnel()
		d.previewTunnel = tunnel
		d.previewHostController = &fakePreviewHostController{}
		insertTestPreviewHost(t, d, deploy, journal.PreviewHostDeployed, []byte(`{}`))
		capturePreviewHostingEvents(d)
		extend := validHostedPreviewLifecycleCommand(v1.PreviewLifecycleExtend, deploy)
		_ = d.executeCommand(context.Background(), v1.Envelope{ID: hostedLifecycleCommandID("7")}, extend)
		if ids := tunnel.resetIDs(); len(ids) != 0 {
			t.Fatalf("extend reset streams: %v", ids)
		}
	})
}

type tunnelGatewayServer struct {
	server *httptest.Server
	conns  chan *websocket.Conn
}

func newTunnelGatewayServer(t *testing.T) *tunnelGatewayServer {
	t.Helper()
	gateway := &tunnelGatewayServer{conns: make(chan *websocket.Conn, 4)}
	release := make(chan struct{})
	gateway.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != previewtunnel.TunnelPath {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		gateway.conns <- conn
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		gateway.server.Close()
	})
	return gateway
}

func (gateway *tunnelGatewayServer) clientTLS() *tls.Config {
	roots := x509.NewCertPool()
	roots.AddCert(gateway.server.Certificate())
	return &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}

func (gateway *tunnelGatewayServer) accept(t *testing.T) *websocket.Conn {
	t.Helper()
	select {
	case conn := <-gateway.conns:
		t.Cleanup(func() { _ = conn.CloseNow() })
		return conn
	case <-time.After(10 * time.Second):
		t.Fatal("the runner did not open the preview tunnel")
		return nil
	}
}

func writeTunnelFrame(t *testing.T, conn *websocket.Conn, frame previewtunnel.Frame) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, previewtunnel.EncodeFrame(frame)); err != nil {
		t.Fatal(err)
	}
}

func awaitTunnelReset(t *testing.T, conn *websocket.Conn, streamID uint32) previewtunnel.ResetCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, message, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for RESET on stream %d: %v", streamID, err)
		}
		frame, err := previewtunnel.DecodeFrame(message)
		if err != nil {
			t.Fatal(err)
		}
		if frame.StreamID != streamID {
			continue
		}
		if code, _, ok := previewtunnel.ResetCodeOf(frame); ok {
			return code
		}
		if frame.Type == previewtunnel.FrameEnd {
			t.Fatalf("stream %d ended instead of being reset", streamID)
		}
	}
}

func openTunnelPayload(t *testing.T, previewID, previewKey string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"preview_id": previewID, "preview_key": previewKey})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestExpiredPreviewStreamsAreResetNotFoundOverTheTunnel(t *testing.T) {
	jr, _ := testJobRunner(t)
	d := jr.d
	var clockMu sync.Mutex
	clock := time.Now()
	d.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	d.previewHostController = &fakePreviewHostController{}
	capturePreviewHostingEvents(d)
	host := insertPreviewHostRow(t, d, "preview-a", "a", journal.PreviewHostDeployed, clock.Add(time.Hour), "")

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			conn, err := upstream.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()

	gateway := newTunnelGatewayServer(t)
	url, err := previewtunnel.TunnelURL("wss://" + gateway.server.Listener.Addr().String() + "/v1/runners/stream")
	if err != nil {
		t.Fatal(err)
	}
	ingress := &recordingIngress{address: upstream.Addr().String()}
	resolver := previewTunnelResolver{hosts: d.journal, ingress: ingress, now: d.previewHostNow}
	d.previewTunnel = previewtunnel.NewClient(url, gateway.clientTLS(), resolver, nil)
	d.previewTunnelAdvertised.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		d.stopPreviewTunnel()
	})
	d.startPreviewTunnel(ctx)
	conn := gateway.accept(t)

	writeTunnelFrame(t, conn, previewtunnel.Frame{Type: previewtunnel.FrameOpen, StreamID: 1, Payload: openTunnelPayload(t, host.PreviewID, host.PreviewKey)})
	writeTunnelFrame(t, conn, previewtunnel.Frame{Type: previewtunnel.FrameData, StreamID: 1, Payload: []byte("hello")})
	var upstreamConn net.Conn
	select {
	case upstreamConn = <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("the runner did not dial the preview ingress")
	}
	defer func() { _ = upstreamConn.Close() }()
	if err := upstreamConn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, 5)
	if _, err := io.ReadFull(upstreamConn, received); err != nil || string(received) != "hello" {
		t.Fatalf("upstream received %q, %v", received, err)
	}

	clockMu.Lock()
	clock = clock.Add(2 * time.Hour)
	clockMu.Unlock()
	if err := d.expirePreviewHosts(context.Background(), v1.PreviewHostedDestroyExpired); err != nil {
		t.Fatal(err)
	}
	if code := awaitTunnelReset(t, conn, 1); code != previewtunnel.ResetNotFound {
		t.Fatalf("reset code = %d, want not_found", code)
	}
	if _, err := upstreamConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("upstream connection stayed open after the preview expired")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("upstream connection was not closed after the preview expired")
	}

	writeTunnelFrame(t, conn, previewtunnel.Frame{Type: previewtunnel.FrameOpen, StreamID: 3, Payload: openTunnelPayload(t, host.PreviewID, host.PreviewKey)})
	if code := awaitTunnelReset(t, conn, 3); code != previewtunnel.ResetNotFound {
		t.Fatalf("open after expiry reset code = %d, want not_found", code)
	}
	if calls := len(ingress.calls); calls != 1 {
		t.Fatalf("ingress looked up %d times, want once", calls)
	}
}
