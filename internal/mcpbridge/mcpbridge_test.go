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
	if len(tools) != len(ToolDefinitions()) {
		t.Fatalf("tools = %d, want %d", len(tools), len(ToolDefinitions()))
	}
}

func TestStdioServerUsesAttemptScopedToolsForDiscoveryAndCalls(t *testing.T) {
	var calls atomic.Int32
	server := &Server{
		Caller: func(_ context.Context, tool string, _ json.RawMessage) (any, error) {
			calls.Add(1)
			return map[string]any{"tool": tool}, nil
		},
		ListTools: func(context.Context) ([]ToolDef, error) {
			return ToolDefinitionsForNames([]string{"platform_get_job_context"})
		},
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"platform_publish_preview","arguments":{"idempotency_key":"preview-1"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	responses := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(responses) != 2 {
		t.Fatalf("responses=%q", output.String())
	}
	var listed struct {
		Result struct {
			Tools []ToolDef `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(responses[0]), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Result.Tools) != 1 || listed.Result.Tools[0].Name != "platform_get_job_context" {
		t.Fatalf("scoped tools=%+v", listed.Result.Tools)
	}
	var rejected Response
	if err := json.Unmarshal([]byte(responses[1]), &rejected); err != nil {
		t.Fatal(err)
	}
	if rejected.Error == nil || rejected.Error.Code != ErrParams || calls.Load() != 0 {
		t.Fatalf("rejected=%+v calls=%d", rejected, calls.Load())
	}
}

func TestStrictJSONRejectsDuplicateKeysAtEveryDepth(t *testing.T) {
	for _, input := range []string{
		`{"jsonrpc":"2.0","jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_get_job_context","arguments":{"project_id":"one","project_id":"two"}}}`,
	} {
		var output bytes.Buffer
		if err := (&Server{}).Serve(context.Background(), strings.NewReader(input+"\n"), &output); err != nil {
			t.Fatal(err)
		}
		var response Response
		if err := json.Unmarshal(output.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Error == nil || response.Error.Code != ErrParse {
			t.Fatalf("duplicate input accepted: %s => %+v", input, response)
		}
	}
}

func TestStdioServerRejectsInvalidRequestIDsAndDoesNotExecuteToolNotifications(t *testing.T) {
	var calls atomic.Int32
	server := &Server{Caller: func(context.Context, string, json.RawMessage) (any, error) {
		calls.Add(1)
		return map[string]any{"ok": true}, nil
	}}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":{"nested":true},"method":"initialize"}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"platform_get_job_context","arguments":{}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	responses := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(responses) != 1 {
		t.Fatalf("unexpected notification response: %q", output.String())
	}
	var response Response
	if err := json.Unmarshal([]byte(responses[0]), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != ErrInvalid || calls.Load() != 0 {
		t.Fatalf("response=%+v calls=%d", response, calls.Load())
	}
}

func TestStdioServerBoundsEncodedToolResponses(t *testing.T) {
	server := &Server{Caller: func(context.Context, string, json.RawMessage) (any, error) {
		return map[string]any{"content": strings.Repeat("x", maxSocketResponseBytes)}, nil
	}}
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_get_job_context","arguments":{}}}` + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() > maxSocketResponseBytes+1 {
		t.Fatalf("stdio response exceeded limit: %d", output.Len())
	}
	var response Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != ErrInternal {
		t.Fatalf("oversized response was not replaced safely: %+v", response)
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
	dir := privateTempDir(t)
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
	dir := privateTempDir(t)
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
	dir := privateTempDir(t)
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

func TestUnixServerRefusesExistingSocketAndInsecureParent(t *testing.T) {
	privateDir := privateTempDir(t)
	path := filepath.Join(privateDir, "job.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := &UnixServer{SocketPath: path, Token: "token", Handler: func(context.Context, string, json.RawMessage) (any, error) { return nil, nil }}
	if err := server.Start(context.Background()); err == nil {
		t.Fatal("existing socket was replaced")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("existing socket was removed: %v", err)
	}

	insecureDir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(insecureDir, 0o755); err != nil {
		t.Fatal(err)
	}
	insecure := &UnixServer{SocketPath: filepath.Join(insecureDir, "job.sock"), Token: "token", Handler: server.Handler}
	if err := insecure.Start(context.Background()); err == nil {
		t.Fatal("socket started in group/world-accessible directory")
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
		{SocketPath: filepath.Join(privateTempDir(t), "job.sock"), Handler: handler},
		{SocketPath: filepath.Join(privateTempDir(t), "job.sock"), Token: "t"},
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

func privateTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "mcp-private-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}
