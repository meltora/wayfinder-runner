package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStdioServerToolsList(t *testing.T) {
	s := &Server{Caller: func(ctx context.Context, tool string, args json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	}}
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("error: %+v", resp.Error)
	}
	result := resp.Result.(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != len(Tools) {
		t.Fatalf("tools = %d, want %d", len(tools), len(Tools))
	}
}

func TestUnixBridgeRejectsTCP(t *testing.T) {
	srv := &UnixServer{SocketPath: "tcp://127.0.0.1:0", Token: "t", Handler: func(context.Context, string, json.RawMessage) (any, error) { return nil, nil }}
	if err := srv.Start(context.Background()); err == nil {
		t.Fatal("TCP MCP server unexpectedly started")
	}
	client := &UnixClient{SocketPath: "tcp://127.0.0.1:1", Token: "t"}
	if _, err := client.Call(context.Background(), "platform_get_job_context", json.RawMessage(`{}`)); err == nil {
		t.Fatal("TCP MCP client unexpectedly connected")
	}
}

func TestUnixBridgeRejectsOversizedRequestBeforeHandler(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "mcp-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "job.sock")
	var calls atomic.Int32
	srv := &UnixServer{SocketPath: sock, Token: "t", Handler: func(context.Context, string, json.RawMessage) (any, error) {
		calls.Add(1)
		return nil, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	request := `{"id":1,"token":"t","tool":"platform_report_progress","args":{"value":"` + strings.Repeat("x", maxSocketRequestBytes) + `"}}}`
	_, _ = conn.Write([]byte(request + "\n"))
	_ = conn.Close()
	if calls.Load() != 0 {
		t.Fatal("oversized request reached handler")
	}
}

func TestStdioServerUnknownTool(t *testing.T) {
	s := &Server{Caller: func(ctx context.Context, tool string, args json.RawMessage) (any, error) {
		return nil, nil
	}}
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_delete_everything","arguments":{}}}` + "\n"
	var out bytes.Buffer
	_ = s.Serve(context.Background(), strings.NewReader(in), &out)
	var resp Response
	_ = json.Unmarshal(out.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != ErrParams {
		t.Fatalf("expected params error, got %+v", resp)
	}
}

func TestStdioServerAcceptsReservedToolCallMetadataOnly(t *testing.T) {
	var called atomic.Bool
	server := &Server{Caller: func(_ context.Context, tool string, args json.RawMessage) (any, error) {
		called.Store(true)
		if tool != "platform_get_job_context" || string(args) != "{}" {
			t.Fatalf("tool=%q args=%s", tool, args)
		}
		return map[string]any{"ok": true}, nil
	}}
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_get_job_context","arguments":{},"_meta":{"progressToken":"token-1"}}}` + "\n"
	var out bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Error != nil || !called.Load() {
		t.Fatalf("response=%+v called=%v decode=%v", response, called.Load(), err)
	}

	called.Store(false)
	out.Reset()
	input = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"platform_get_job_context","arguments":{},"unexpected":true}}` + "\n"
	if err := server.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Error == nil || response.Error.Code != ErrParams || called.Load() {
		t.Fatalf("response=%+v called=%v decode=%v", response, called.Load(), err)
	}
}

func TestUnixBridgeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "job.sock")
	srv := &UnixServer{
		SocketPath: sock,
		Token:      "tok-1",
		Handler: func(ctx context.Context, tool string, args json.RawMessage) (any, error) {
			return map[string]any{"tool": tool}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	client := &UnixClient{SocketPath: sock, Token: "tok-1"}
	res, err := client.Call(ctx, "platform_get_job_context", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.(map[string]any)["tool"] != "platform_get_job_context" {
		t.Fatalf("result = %v", res)
	}

	bad := &UnixClient{SocketPath: sock, Token: "wrong"}
	if _, err := bad.Call(ctx, "platform_get_job_context", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected token error")
	}
}

func TestUnixSocketPermissions(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "job.sock")
	srv := &UnixServer{SocketPath: sock, Token: "t", Handler: func(ctx context.Context, tool string, args json.RawMessage) (any, error) { return nil, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	fi, err := statSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestUnixServerRefusesToReplaceRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.sock")
	if err := os.WriteFile(path, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := &UnixServer{SocketPath: path, Token: "token", Handler: func(context.Context, string, json.RawMessage) (any, error) { return nil, nil }}
	if err := srv.Start(context.Background()); err == nil {
		t.Fatal("regular file was replaced by MCP socket")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "do not delete" {
		t.Fatalf("regular file changed: %q %v", data, err)
	}
}

func TestStdioServerProtocolBranches(t *testing.T) {
	cases := []struct {
		name  string
		input string
		check func(Response) bool
	}{
		{"initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, func(r Response) bool { return r.Error == nil && r.Result != nil }},
		{"parse", `{`, func(r Response) bool { return r.Error != nil && r.Error.Code == ErrParse }},
		{"invalid", `{"jsonrpc":"1.0","id":1,"method":"initialize"}`, func(r Response) bool { return r.Error != nil && r.Error.Code == ErrInvalid }},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"future"}`, func(r Response) bool { return r.Error != nil && r.Error.Code == ErrNoMethod }},
		{"missing caller", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_get_job_context","arguments":{}}}`, func(r Response) bool { return r.Error != nil && r.Error.Code == ErrInternal }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := (&Server{}).Serve(context.Background(), strings.NewReader("\n"+tc.input+"\n"), &out); err != nil {
				t.Fatal(err)
			}
			var response Response
			if err := json.Unmarshal(out.Bytes(), &response); err != nil || !tc.check(response) {
				t.Fatalf("response=%+v decode=%v raw=%s", response, err, out.Bytes())
			}
		})
	}

	var out bytes.Buffer
	server := &Server{Caller: func(context.Context, string, json.RawMessage) (any, error) {
		return nil, errors.New(strings.Repeat("x", 2000))
	}}
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_get_job_context","arguments":{}}}` + "\n"
	if err := server.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Error == nil || len(response.Error.Message) > 1003 {
		t.Fatalf("response=%+v err=%v", response, err)
	}

	var notification bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"), &notification); err != nil {
		t.Fatal(err)
	}
	if notification.Len() != 0 {
		t.Fatalf("notification produced response %q", notification.String())
	}
}

func TestUnixBridgeConfigurationAndWireErrors(t *testing.T) {
	handler := func(context.Context, string, json.RawMessage) (any, error) { return nil, nil }
	for _, server := range []*UnixServer{
		{SocketPath: "relative.sock", Token: "t", Handler: handler},
		{SocketPath: filepath.Join(t.TempDir(), "job.sock"), Handler: handler},
		{SocketPath: filepath.Join(t.TempDir(), "job.sock"), Token: "t"},
	} {
		if err := server.Start(context.Background()); err == nil {
			t.Fatalf("invalid server accepted: %+v", server)
		}
	}
	client := &UnixClient{SocketPath: "relative.sock", Token: "t"}
	if _, err := client.Call(context.Background(), "platform_get_job_context", json.RawMessage(`{}`)); err == nil {
		t.Fatal("relative client socket accepted")
	}
	if _, err := client.Call(context.Background(), "delete_everything", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown socket tool accepted")
	}
	if truncateError("short") != "short" || len(truncateError(strings.Repeat("x", 2000))) > 1003 {
		t.Fatal("error truncation failed")
	}
	if _, err := scanJSONLine(strings.NewReader("\n"), 1024); err == nil {
		t.Fatal("empty JSON line accepted")
	}
}
