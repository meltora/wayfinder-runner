package transport

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestConnectReportsUpgradeStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		client := NewClient("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/runners/stream", "runner-1", "1.0.0", &tls.Config{MinVersion: tls.VersionTLS13}, &rejectionRecordingHandler{})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := client.connectOnce(ctx)
		cancel()
		server.Close()
		var rejected *UpgradeRejectedError
		if !errors.As(err, &rejected) || rejected.StatusCode != status {
			t.Fatalf("status %d: connectOnce error = %v, want an UpgradeRejectedError", status, err)
		}
		if IsUpgradeUnauthorized(err) != (status == http.StatusUnauthorized) {
			t.Fatalf("status %d: IsUpgradeUnauthorized = %v", status, IsUpgradeUnauthorized(err))
		}
		if !strings.HasPrefix(err.Error(), "dial: ") {
			t.Fatalf("status %d: message %q changed", status, err.Error())
		}
	}
}

func TestConnectNetworkFailureIsNotAnUpgradeRejection(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	client := NewClient("ws://"+address+"/v1/runners/stream", "runner-1", "1.0.0", &tls.Config{MinVersion: tls.VersionTLS13}, &rejectionRecordingHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = client.connectOnce(ctx)
	if err == nil || IsUpgradeUnauthorized(err) {
		t.Fatalf("connectOnce = %v, want a plain dial failure", err)
	}
	var rejected *UpgradeRejectedError
	if errors.As(err, &rejected) {
		t.Fatalf("network failure reported as upgrade status %d", rejected.StatusCode)
	}
}

type tlsRecordingHandler struct {
	rejectionRecordingHandler
	client *Client
	seen   chan *tls.Config
}

func (h *tlsRecordingHandler) OnConnected(context.Context) { h.seen <- h.client.ConnectedTLSConfig() }

func TestConnectedTLSConfigIdentifiesTheAcceptedSession(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		if _, _, err := conn.Read(r.Context()); err != nil {
			return
		}
		ack, _ := json.Marshal(v1.Envelope{Version: v1.ProtocolVersion, Type: v1.TypeHelloAck, ID: "ack-1", SentAt: time.Now(), Payload: v1.HelloAckPayload{Accepted: true}})
		_ = conn.Write(r.Context(), websocket.MessageText, ack)
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	handler := &tlsRecordingHandler{seen: make(chan *tls.Config, 1)}
	client := NewClient("ws"+strings.TrimPrefix(server.URL, "http"), "runner-1", "1.0.0", cfg, handler)
	handler.client = client
	if client.ConnectedTLSConfig() != nil {
		t.Fatal("a disconnected client reports a session TLS config")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = client.connectOnce(ctx) }()
	select {
	case got := <-handler.seen:
		if got != cfg {
			t.Fatal("ConnectedTLSConfig does not identify the configuration of the accepted session")
		}
	case <-ctx.Done():
		t.Fatal("session was not accepted")
	}
}
