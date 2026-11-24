package transport

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const transportPreviewSecret = "sk_test_transport_preview_secret_value"

func previewSecretsEnvelope(messageType v1.MessageType) string {
	return `{"version":1,"type":"` + string(messageType) + `","id":"message-1","sent_at":"2030-01-01T00:00:00Z",` +
		`"in_reply_to":"event-1","payload":{},"preview_secrets":{"STRIPE_SECRET_KEY":"` + transportPreviewSecret + `"}}`
}

func TestReadLoopRejectsPreviewSecretsOnNonCommandMessages(t *testing.T) {
	for _, messageType := range []v1.MessageType{v1.TypeEventAck, v1.TypeHeartbeatAck, v1.TypeError, v1.TypePause} {
		t.Run(string(messageType), func(t *testing.T) {
			handler, err := readLoopAgainst(t, previewSecretsEnvelope(messageType))
			if err == nil || !strings.Contains(err.Error(), "preview secrets") {
				t.Fatalf("readLoop error = %v", err)
			}
			if strings.Contains(err.Error(), transportPreviewSecret) {
				t.Fatalf("error echoes the secret: %v", err)
			}
			if len(handler.commands) != 0 {
				t.Fatal("non-command envelope reached the command handler")
			}
		})
	}
}

func TestReadLoopDeliversCommandPreviewSecretsInMemory(t *testing.T) {
	command := strings.Replace(previewSecretsEnvelope(v1.TypeCommand), `"in_reply_to":"event-1",`, "", 1)
	handler, _ := readLoopAgainst(t, command)
	if len(handler.commands) != 1 || handler.commands[0].PreviewSecrets["STRIPE_SECRET_KEY"] != transportPreviewSecret {
		t.Fatal("preview secrets were not decoded beside the payload")
	}
}

func TestOutboundEnvelopesCannotCarryPreviewSecrets(t *testing.T) {
	client := &Client{runnerID: "runner-1"}
	event := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeEvent, ID: "event-1", SentAt: time.Now(),
		RunnerID: "runner-1", Payload: map[string]any{"kind": "progress"},
		PreviewSecrets: v1.PreviewSecrets{"STRIPE_SECRET_KEY": transportPreviewSecret},
	}
	err := client.SendEvent(context.Background(), event)
	if err == nil || strings.Contains(err.Error(), "not connected") {
		t.Fatalf("event carrying preview secrets reached transport: %v", err)
	}
}
