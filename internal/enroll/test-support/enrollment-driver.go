// This test-only driver is invoked by the app's isolated enrollment interoperability test.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/capability"
	"github.com/example/app-runner/internal/enroll"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/transport"
)

type input struct {
	ControlURL string `json:"controlUrl"`
	GatewayURL string `json:"gatewayUrl"`
	ControlCA  string `json:"controlCa"`
	Code       string `json:"code"`
	Directory  string `json:"directory"`
}

type handler struct {
	connected    chan struct{}
	acknowledged chan struct{}
	failure      chan error
	capabilityID string
}

func (h *handler) OnCommand(context.Context, v1.Envelope) error {
	return errors.New("unexpected operational command")
}
func (h *handler) OnConnected(context.Context) {
	select {
	case h.connected <- struct{}{}:
	default:
	}
}
func (h *handler) OnDisconnected(error) {
	select {
	case h.failure <- errors.New("gateway disconnected"):
	default:
	}
}
func (h *handler) OnEventAck(_ context.Context, id string) {
	if id == h.capabilityID {
		select {
		case h.acknowledged <- struct{}{}:
		default:
		}
	}
}

func run() error {
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20))
	decoder.DisallowUnknownFields()
	var in input
	if decoder.Decode(&in) != nil {
		return errors.New("invalid isolated driver configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := identity.NewStore(in.Directory)
	key, err := store.EnsureKey()
	if err != nil {
		return errors.New("generate isolated identity")
	}
	firstCSR, err := identity.NewCSR(key, "lost-response-host")
	if err != nil {
		return errors.New("generate initial CSR")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(in.ControlCA)) {
		return errors.New("invalid control trust")
	}
	client := enroll.Client{BaseURL: in.ControlURL, HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}, Timeout: 10 * time.Second}}
	if _, err = client.Enroll(ctx, enroll.Request{EnrollmentCode: in.Code, CSR: string(firstCSR), RunnerVersion: "1.0.0"}); err == nil {
		return errors.New("expected committed response loss")
	}
	reloadedKey, err := identity.NewStore(in.Directory).EnsureKey()
	if err != nil {
		return errors.New("reload enrollment key")
	}
	secondCSR, err := identity.NewCSR(reloadedKey, "retry-host")
	if err != nil || string(firstCSR) == string(secondCSR) {
		return errors.New("regenerate CSR")
	}
	result, err := client.Enroll(ctx, enroll.Request{EnrollmentCode: in.Code, CSR: string(secondCSR), RunnerVersion: "1.0.0"})
	if err != nil {
		return errors.New("recover committed enrollment")
	}
	if identity.ValidateIssued([]byte(result.Certificate), []byte(result.CACert), reloadedKey, result.RunnerID, time.Now()) != nil {
		return errors.New("validate issued identity")
	}
	thirdCSR, err := identity.NewCSR(reloadedKey, "another-retry-host")
	if err != nil {
		return errors.New("regenerate replay CSR")
	}
	replay, err := client.Enroll(ctx, enroll.Request{EnrollmentCode: in.Code, CSR: string(thirdCSR), RunnerVersion: "1.0.0"})
	if err != nil || *result != *replay {
		return errors.New("exact certificate replay changed")
	}
	if store.SaveIdentity(reloadedKey, []byte(result.Certificate), []byte(result.CACert)) != nil {
		return errors.New("persist isolated identity")
	}
	restored := identity.NewStore(in.Directory)
	if restored.ValidateIdentity(result.RunnerID, time.Now()) != nil {
		return errors.New("validate reloaded identity")
	}
	cert, ca, err := restored.LoadCert()
	if err != nil {
		return errors.New("read persisted certificate")
	}
	key, err = restored.LoadKey()
	if err != nil {
		return errors.New("read persisted key")
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return errors.New("encode persisted key")
	}
	tlsConfig, err := transport.TLSConfigFromPEM(cert, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), ca)
	if err != nil || tlsConfig.MinVersion != tls.VersionTLS13 {
		return errors.New("create strict TLS transport")
	}
	h := &handler{connected: make(chan struct{}, 1), acknowledged: make(chan struct{}, 1), failure: make(chan error, 1), capabilityID: "enrollment-interop-capability"}
	stream := transport.NewClient(in.GatewayURL, result.RunnerID, "1.0.0", tlsConfig, h)
	stream.HeartbeatPayload = func(context.Context) v1.HeartbeatPayload {
		return v1.HeartbeatPayload{State: "idle", ActiveJobs: 0, UnackedCount: 0}
	}
	finished := make(chan struct{})
	go func() { defer close(finished); _ = stream.Run(ctx) }()
	defer func() { cancel(); <-finished }()
	select {
	case <-h.connected:
	case <-h.failure:
		return errors.New("strict gateway hello failed")
	case <-ctx.Done():
		return errors.New("gateway hello timed out")
	}
	detector := capability.New(result.RunnerID, "1.0.0", map[string]string{}, map[string]string{})
	// This test does not inspect user provider credentials or execute any provider binary.
	detector.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	detector.AuthReady = func(string) bool { return false }
	manifest := detector.Collect(ctx)
	if detector.Sign(&manifest, key) != nil {
		return errors.New("sign capability manifest")
	}
	if stream.SendEvent(ctx, v1.Envelope{Version: v1.ProtocolVersion, Type: v1.TypeEvent, ID: h.capabilityID, SentAt: time.Now(), RunnerID: result.RunnerID, Payload: map[string]any{"kind": "capability_manifest", "manifest": manifest}}) != nil {
		return errors.New("publish capability manifest")
	}
	select {
	case <-h.acknowledged:
	case <-h.failure:
		return errors.New("gateway capability failed")
	case <-ctx.Done():
		return errors.New("capability acknowledgement timed out")
	}
	block, _ := pem.Decode(cert)
	if block == nil {
		return errors.New("decode enrolled certificate")
	}
	fingerprint := sha256.Sum256(block.Bytes)
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return errors.New("encode enrolled public key")
	}
	spkiHash := sha256.Sum256(spki)
	if json.NewEncoder(os.Stdout).Encode(map[string]any{"phase": "ready", "runnerId": result.RunnerID, "fingerprintSha256": hex.EncodeToString(fingerprint[:]), "spkiSha256": hex.EncodeToString(spkiHash[:]), "capabilityDeliveryId": h.capabilityID, "regeneratedCsr": true, "identityReloaded": true, "tls13Required": true}) != nil {
		return errors.New("report isolated result")
	}
	var stop struct {
		Stop bool `json:"stop"`
	}
	if decoder.Decode(&stop) != nil || !stop.Stop {
		return errors.New("missing isolated completion")
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
