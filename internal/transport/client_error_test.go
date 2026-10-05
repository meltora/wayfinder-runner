package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type rejectionCall struct {
	eventID string
	code    string
}

type rejectionRecordingHandler struct {
	mu                sync.Mutex
	eventRejections   []rejectionCall
	sessionRejections []string
	eventAcks         []string
	commands          int
	failWrites        bool
}

func (h *rejectionRecordingHandler) OnCommand(context.Context, v1.Envelope) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commands++
	return nil
}

func (h *rejectionRecordingHandler) OnEventAck(_ context.Context, eventID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.eventAcks = append(h.eventAcks, eventID)
}

func (h *rejectionRecordingHandler) OnConnected(context.Context) {}

func (h *rejectionRecordingHandler) OnDisconnected(error) {}

func (h *rejectionRecordingHandler) OnEventRejected(_ context.Context, eventID, code string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.eventRejections = append(h.eventRejections, rejectionCall{eventID: eventID, code: code})
	if h.failWrites {
		return errors.New("journal is closed")
	}
	return nil
}

func (h *rejectionRecordingHandler) OnSessionRejected(_ context.Context, code string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessionRejections = append(h.sessionRejections, code)
	if h.failWrites {
		return errors.New("journal is closed")
	}
	return nil
}

func runReadLoop(t *testing.T, handler Handler, frames ...string) error {
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
	client := &Client{runnerID: "runner-1", handler: handler, conn: conn}
	return client.readLoop(ctx)
}

func errorFrame(inReplyTo, payload string) string {
	frame := `{"version":1,"type":"error","id":"srv-1","sent_at":"2030-01-01T00:00:00Z",`
	if inReplyTo != "" {
		frame += `"in_reply_to":"` + inReplyTo + `",`
	}
	return frame + `"payload":` + payload + `}`
}

const eventAckFrame = `{"version":1,"type":"event_ack","id":"srv-2","sent_at":"2030-01-01T00:00:00Z","in_reply_to":"evt-2"}`

func requireNormalClosure(t *testing.T, err error) {
	t.Helper()
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("read loop ended with %v, want the server's normal closure", err)
	}
}

func TestReadLoopRoutesEventRejectionsByInReplyTo(t *testing.T) {
	handler := &rejectionRecordingHandler{}
	err := runReadLoop(t, handler,
		errorFrame("evt-1", `{"code":"runner_event_rejected","message":"Runner event rejected"}`),
		errorFrame("", `{"code":"invalid_envelope","message":"Protocol message rejected"}`),
		eventAckFrame,
	)
	requireNormalClosure(t, err)
	if len(handler.eventRejections) != 1 || handler.eventRejections[0] != (rejectionCall{"evt-1", "runner_event_rejected"}) {
		t.Fatalf("event rejections = %+v", handler.eventRejections)
	}
	if len(handler.sessionRejections) != 1 || handler.sessionRejections[0] != "invalid_envelope" {
		t.Fatalf("session rejections = %+v", handler.sessionRejections)
	}
	if len(handler.eventAcks) != 1 || handler.eventAcks[0] != "evt-2" {
		t.Fatalf("event acks = %+v", handler.eventAcks)
	}
}

func TestReadLoopContinuesWhenRejectionWriteFails(t *testing.T) {
	handler := &rejectionRecordingHandler{failWrites: true}
	err := runReadLoop(t, handler,
		errorFrame("evt-1", `{"code":"runner_event_rejected","message":"m"}`),
		errorFrame("", `{"code":"malformed_json","message":"m"}`),
		eventAckFrame,
	)
	requireNormalClosure(t, err)
	if len(handler.eventRejections) != 1 || len(handler.sessionRejections) != 1 {
		t.Fatalf("rejections = %+v / %+v", handler.eventRejections, handler.sessionRejections)
	}
	if len(handler.eventAcks) != 1 || handler.eventAcks[0] != "evt-2" {
		t.Fatalf("event_ack after a failed rejection write was not processed: %+v", handler.eventAcks)
	}
}

func TestReadLoopIgnoresUndecodableErrorPayloads(t *testing.T) {
	handler := &rejectionRecordingHandler{}
	err := runReadLoop(t, handler,
		errorFrame("evt-1", `{"code":"runner_event_rejected","message":"m","extra":true}`),
		errorFrame("evt-1", `{"code":7,"message":"m"}`),
		errorFrame("", `"not an object"`),
		errorFrame("", `{"message":"missing code"}`),
		`{"version":1,"type":"error","id":"srv-9","sent_at":"2030-01-01T00:00:00Z"}`,
		eventAckFrame,
	)
	requireNormalClosure(t, err)
	if len(handler.eventRejections) != 0 || len(handler.sessionRejections) != 0 {
		t.Fatalf("undecodable payloads reached the handler: %+v / %+v", handler.eventRejections, handler.sessionRejections)
	}
	if len(handler.eventAcks) != 1 {
		t.Fatalf("event acks = %+v", handler.eventAcks)
	}
}

func TestReadLoopErrorFramesWithoutRejectionHandlerAreIgnored(t *testing.T) {
	handler := &recordingCommandHandler{}
	err := runReadLoop(t, handler,
		errorFrame("evt-1", `{"code":"runner_event_rejected","message":"m"}`),
		errorFrame("", `{"code":"invalid_envelope","message":"m"}`),
		eventAckFrame,
	)
	requireNormalClosure(t, err)
}
