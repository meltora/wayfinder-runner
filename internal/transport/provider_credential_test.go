package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const transportProviderToken = "ghs_transport00000000000000000000000000000"

type recordingCommandHandler struct {
	commands []v1.Envelope
}

func (h *recordingCommandHandler) OnCommand(_ context.Context, env v1.Envelope) error {
	h.commands = append(h.commands, env)
	return nil
}

func (h *recordingCommandHandler) OnEventAck(context.Context, string) {}

func (h *recordingCommandHandler) OnConnected(context.Context) {}

func (h *recordingCommandHandler) OnDisconnected(error) {}

func credentialEnvelope(messageType v1.MessageType) string {
	return `{"version":1,"type":"` + string(messageType) + `","id":"message-1","sent_at":"2030-01-01T00:00:00Z",` +
		`"in_reply_to":"event-1","payload":{},"provider_credential":{"kind":"github_installation_token","token":"` +
		transportProviderToken + `","expires_at":"2030-01-01T01:00:00Z","repository_ids":["1"]}}`
}

func readLoopAgainst(t *testing.T, frames ...string) (*recordingCommandHandler, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		for _, frame := range frames {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(frame)); err != nil {
				return
			}
		}
		_ = conn.Close(websocket.StatusNormalClosure, "done")
	}))
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	handler := &recordingCommandHandler{}
	client := &Client{runnerID: "runner-1", handler: handler, conn: conn}
	return handler, client.readLoop(ctx)
}

func TestReadLoopDeliversCommandProviderCredentialInMemory(t *testing.T) {
	command := strings.Replace(credentialEnvelope(v1.TypeCommand), `"in_reply_to":"event-1",`, "", 1)
	handler, _ := readLoopAgainst(t, command)
	if len(handler.commands) != 1 {
		t.Fatalf("commands = %d", len(handler.commands))
	}
	credential := handler.commands[0].ProviderCredential
	if credential == nil || credential.Token != transportProviderToken || credential.RepositoryIDs[0] != "1" {
		t.Fatal("provider credential was not decoded beside the payload")
	}
}

func TestReadLoopRejectsProviderCredentialOnNonCommandMessages(t *testing.T) {
	for _, messageType := range []v1.MessageType{v1.TypeEventAck, v1.TypeHeartbeatAck, v1.TypeError, v1.TypePause} {
		t.Run(string(messageType), func(t *testing.T) {
			handler, err := readLoopAgainst(t, credentialEnvelope(messageType))
			if err == nil || !strings.Contains(err.Error(), "provider credential") {
				t.Fatalf("readLoop error = %v", err)
			}
			if strings.Contains(err.Error(), transportProviderToken) {
				t.Fatalf("error echoes the token: %v", err)
			}
			if len(handler.commands) != 0 {
				t.Fatal("non-command envelope reached the command handler")
			}
		})
	}
}

func TestReadLoopRejectsMalformedProviderCredentialWithoutEchoingIt(t *testing.T) {
	malformed := strings.Replace(
		strings.Replace(credentialEnvelope(v1.TypeCommand), `"in_reply_to":"event-1",`, "", 1),
		`"repository_ids":["1"]`, `"repository_ids":["1"],"scope":"admin"`, 1,
	)
	handler, err := readLoopAgainst(t, malformed)
	if err == nil {
		t.Fatal("malformed provider credential accepted")
	}
	if strings.Contains(err.Error(), transportProviderToken) {
		t.Fatalf("error echoes the token: %v", err)
	}
	if len(handler.commands) != 0 {
		t.Fatal("malformed credential reached the command handler")
	}
}

func TestOutboundEnvelopesCannotCarryProviderCredential(t *testing.T) {
	client := &Client{runnerID: "runner-1"}
	credential := v1.ProviderCredential{
		Kind: v1.ProviderCredentialGitHubInstallationToken, Token: transportProviderToken,
		ExpiresAt: time.Now().Add(time.Hour), RepositoryIDs: []string{"1"},
	}
	event := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeEvent, ID: "event-1", SentAt: time.Now(),
		RunnerID: "runner-1", Payload: map[string]any{"kind": "progress"}, ProviderCredential: &credential,
	}
	err := client.SendEvent(context.Background(), event)
	if err == nil || strings.Contains(err.Error(), "not connected") {
		t.Fatalf("event carrying a provider credential reached transport: %v", err)
	}
}
