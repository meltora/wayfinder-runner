package daemon

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type memoryListener struct {
	conns  chan net.Conn
	done   chan struct{}
	closed sync.Once
}

type memoryAddr struct{}

func (memoryAddr) Network() string { return "memory" }
func (memoryAddr) String() string  { return "upload.invalid" }

func (l *memoryListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *memoryListener) Close() error {
	l.closed.Do(func() { close(l.done) })
	return nil
}

func (l *memoryListener) Addr() net.Addr { return memoryAddr{} }

func (l *memoryListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		_ = client.Close()
		_ = server.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		return nil, ctx.Err()
	}
}

func newInMemoryArtifactUploader(t *testing.T, handler http.Handler) *artifactUploader {
	t.Helper()
	listener := &memoryListener{conns: make(chan net.Conn), done: make(chan struct{})}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	uploader := newArtifactUploader("http://" + listener.Addr().String())
	uploader.SetHTTPClient(&http.Client{
		Timeout:   time.Minute,
		Transport: &http.Transport{DialContext: listener.dial, DisableKeepAlives: true},
	})
	return uploader
}

func TestInMemoryUploadServerReachesOnlyItsUploader(t *testing.T) {
	var requests atomic.Int32
	uploader := newInMemoryArtifactUploader(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeArtifactReceipt(w, r)
	}))
	parsed, err := url.Parse(uploader.baseURL)
	if err != nil {
		t.Fatal(err)
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil {
		t.Fatalf("in-memory upload server exposes an IP address: %s", uploader.baseURL)
	}
	for _, address := range []string{parsed.Host, net.JoinHostPort("127.0.0.1", parsed.Port()), net.JoinHostPort("localhost", parsed.Port())} {
		dialer := net.Dialer{Timeout: time.Second}
		if conn, err := dialer.Dial("tcp", address); err == nil {
			_ = conn.Close()
			t.Fatalf("in-memory upload server is reachable over TCP at %s", address)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("a probe reached the handler: %d requests", requests.Load())
	}
	pending := pendingArtifactUpload{Artifact: v1.ArtifactRef{JobID: "job-1", AttemptID: "attempt-1", Name: "report.txt"}}
	if err := uploader.Upload(context.Background(), pending, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("uploader requests=%d", requests.Load())
	}
}
