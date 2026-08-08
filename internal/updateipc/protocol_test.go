package updateipc

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequestStrictBoundary(t *testing.T) {
	valid := `{"schema_version":1,"request_id":"request-1","action":"prepare","update_id":"update-1"}` + "\n"
	req, err := DecodeRequest(strings.NewReader(valid))
	if err != nil {
		t.Fatalf("valid request: %v", err)
	}
	if req.Action != ActionPrepare || req.UpdateID != "update-1" {
		t.Fatalf("request=%+v", req)
	}
	invalid := []string{
		`{"schema_version":1,"request_id":"request-1","action":"prepare","update_id":"update-1","url":"https://attacker.invalid"}` + "\n",
		`{"schema_version":1,"request_id":"request-1","action":"prepare","update_id":"bad/id"}` + "\n",
		`{"schema_version":2,"request_id":"request-1","action":"prepare","update_id":"update-1"}` + "\n",
		`{"schema_version":1,"request_id":"request-1","action":"prepare","action":"activate","update_id":"update-1"}` + "\n",
		valid + valid,
		strings.Repeat("x", MaxMessageBytes+1),
	}
	for _, raw := range invalid {
		if _, err := DecodeRequest(strings.NewReader(raw)); err == nil {
			t.Fatalf("invalid request accepted: %.100q", raw)
		}
	}
}

func TestClientUsesFixedBoundedProtocol(t *testing.T) {
	socket := shortSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan Request, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		line, readErr := bufio.NewReader(conn).ReadBytes('\n')
		if readErr != nil {
			return
		}
		var request Request
		if json.Unmarshal(line, &request) != nil {
			return
		}
		received <- request
		_, _ = conn.Write([]byte(`{"schema_version":1,"ok":true,"phase":"prepared","update_id":"update-1"}` + "\n"))
	}()

	client := NewClient(socket)
	client.Timeout = time.Second
	response, err := client.Prepare(context.Background(), "request-1", "update-1")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !response.OK || response.Phase != "prepared" {
		t.Fatalf("response=%+v", response)
	}
	select {
	case request := <-received:
		if request.SchemaVersion != SchemaVersion || request.Action != ActionPrepare || request.UpdateID != "update-1" || request.RunnerVersion != "" {
			t.Fatalf("request=%+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not receive request")
	}
}

func TestResponseRejectsUnknownOversizedAndMismatchedUpdate(t *testing.T) {
	cases := []string{
		`{"schema_version":1,"ok":true,"phase":"prepared","update_id":"other"}` + "\n",
		`{"schema_version":1,"ok":true,"phase":"prepared","update_id":"update-1","path":"/usr/bin/evil"}` + "\n",
		strings.Repeat("x", MaxMessageBytes+1),
	}
	for _, response := range cases {
		socket := shortSocketPath(t)
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			conn, acceptErr := listener.Accept()
			if acceptErr == nil {
				defer conn.Close()
				_, _ = bufio.NewReader(conn).ReadBytes('\n')
				_, _ = conn.Write([]byte(response))
			}
		}()
		client := NewClient(socket)
		client.Timeout = time.Second
		if _, err := client.Prepare(context.Background(), "request-1", "update-1"); err == nil {
			t.Fatalf("invalid response accepted: %.100q", response)
		}
		_ = listener.Close()
	}
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "uipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "u.sock")
}
