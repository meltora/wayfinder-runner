package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/example/app-runner/internal/safeio"
)

type herdrRequest struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type herdrResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *herdrError     `json:"error,omitempty"`
}

type herdrError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *herdrError) Error() string { return e.Code + ": " + e.Message }

type herdrConn interface {
	call(method string, params any) (json.RawMessage, error)
	close() error
}

type herdrSocketConn struct {
	socketPath string
	counter    atomic.Uint64
}

func defaultSocketPath() string {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "herdr", "herdr.sock")
}

func dialHerdr(socketPath string) (*herdrSocketConn, error) {
	return &herdrSocketConn{socketPath: socketPath}, nil
}

func (c *herdrSocketConn) call(method string, params any) (json.RawMessage, error) {
	id := fmt.Sprintf("req-%d", c.counter.Add(1))
	conn, err := net.DialTimeout("unix", c.socketPath, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return nil, err
	}
	req := herdrRequest{ID: id, Method: method, Params: params}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bufio.NewReader(io.LimitReader(conn, 1<<20)))
	for {
		var resp herdrResponse
		if err := dec.Decode(&resp); err != nil {
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (c *herdrSocketConn) close() error { return nil }

type HerdrSupervisor struct {
	SocketPath string
	dial       func(path string) (herdrConn, error)
	LookPath   func(string) (string, error)
}

func NewHerdr(socketPath string) *HerdrSupervisor {
	if socketPath == "" {
		socketPath = defaultSocketPath()
	}
	return &HerdrSupervisor{
		SocketPath: socketPath,
		dial: func(path string) (herdrConn, error) {
			return dialHerdr(path)
		},
	}
}

func (h *HerdrSupervisor) Name() string { return "herdr" }

func (h *HerdrSupervisor) Available() bool {
	if h.SocketPath == "" {
		return false
	}
	info, err := os.Lstat(h.SocketPath)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

type herdrProc struct {
	conn       herdrConn
	paneID     string
	workspace  string
	exitFile   string
	launchFile string
	sessionRef string
	ctx        context.Context
	done       chan struct{}
	stopOnce   sync.Once
}

func (p *herdrProc) SessionRef() (string, error) {
	if p.sessionRef != "" {
		return p.sessionRef, nil
	}
	result, err := p.conn.call("pane.get", map[string]any{"pane_id": p.paneID})
	if err != nil {
		return "", err
	}
	var parsed struct {
		Pane struct {
			AgentSession struct {
				Value string `json:"value"`
			} `json:"agent_session"`
		} `json:"pane"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		return "", err
	}
	p.sessionRef = parsed.Pane.AgentSession.Value
	return p.sessionRef, nil
}

func (h *HerdrSupervisor) Start(ctx context.Context, spec Spec) (Process, error) {
	conn, err := h.dial(h.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("herdr connect: %w", err)
	}
	label := sanitizeSession(spec.Name)
	wsResult, err := conn.call("workspace.create", map[string]any{
		"cwd": spec.Dir, "label": label, "focus": false,
	})
	if err != nil {
		_ = conn.close()
		return nil, fmt.Errorf("workspace.create: %w", err)
	}
	workspaceID := parseStringField(wsResult, "workspace", "workspace_id")
	if workspaceID == "" {
		workspaceID = parseStringField(wsResult, "", "workspace_id")
	}
	paneID, err := h.findPane(conn, workspaceID)
	if err != nil {
		_, _ = conn.call("workspace.close", map[string]any{"workspace_id": workspaceID})
		_ = conn.close()
		return nil, err
	}

	if spec.RuntimeDir == "" {
		_, _ = conn.call("workspace.close", map[string]any{"workspace_id": workspaceID})
		_ = conn.close()
		return nil, fmt.Errorf("herdr requires a runner-owned runtime directory")
	}
	if err := os.MkdirAll(spec.RuntimeDir, 0o700); err != nil {
		_, _ = conn.call("workspace.close", map[string]any{"workspace_id": workspaceID})
		_ = conn.close()
		return nil, err
	}
	exitHandle, err := os.CreateTemp(spec.RuntimeDir, ".herdr-exit-*")
	if err != nil {
		_, _ = conn.call("workspace.close", map[string]any{"workspace_id": workspaceID})
		_ = conn.close()
		return nil, err
	}
	exitFile := exitHandle.Name()
	_ = exitHandle.Close()
	_ = os.Remove(exitFile)

	launchFile, err := writeHerdrLaunchScript(spec, exitFile)
	if err != nil {
		_, _ = conn.call("workspace.close", map[string]any{"workspace_id": workspaceID})
		_ = conn.close()
		return nil, err
	}

	if _, err := conn.call("pane.send_input", map[string]any{
		"pane_id": paneID, "text": shellQuote(launchFile), "keys": []string{"enter"},
	}); err != nil {
		_ = os.Remove(launchFile)
		_, _ = conn.call("workspace.close", map[string]any{"workspace_id": workspaceID})
		_ = conn.close()
		return nil, fmt.Errorf("pane.send_input: %w", err)
	}
	return &herdrProc{
		conn: conn, paneID: paneID, workspace: workspaceID, exitFile: exitFile, launchFile: launchFile,
		ctx: ctx, done: make(chan struct{}),
	}, nil
}

func writeHerdrLaunchScript(spec Spec, exitFile string) (string, error) {
	file, err := os.CreateTemp(spec.RuntimeDir, ".herdr-launch-*")
	if err != nil {
		return "", fmt.Errorf("create Herdr launch script: %w", err)
	}
	path := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o700); err != nil {
		return "", fmt.Errorf("secure Herdr launch script: %w", err)
	}

	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	for _, entry := range spec.Env {
		if forwardSessionEnvironment(entry) {
			fmt.Fprintf(&script, "export %s\n", shellQuote(entry))
		}
	}
	fmt.Fprintf(&script, "cd %s\n", shellQuote(spec.Dir))
	script.WriteString("status=$?\n")
	script.WriteString("if [ \"$status\" -eq 0 ]; then\n  ")
	script.WriteString(shellQuote(spec.Command))
	for _, arg := range spec.Args {
		script.WriteString(" " + shellQuote(arg))
	}
	script.WriteString("\n  status=$?\nfi\n")
	fmt.Fprintf(&script, "printf '%%s\\n' \"$status\" > %s\n", shellQuote(exitFile))
	script.WriteString("exit \"$status\"\n")

	if _, err := file.WriteString(script.String()); err != nil {
		return "", fmt.Errorf("write Herdr launch script: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync Herdr launch script: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close Herdr launch script: %w", err)
	}
	keep = true
	return path, nil
}

func (h *HerdrSupervisor) findPane(conn herdrConn, workspaceID string) (string, error) {
	result, err := conn.call("pane.list", map[string]any{"workspace_id": workspaceID})
	if err != nil {
		return "", fmt.Errorf("pane.list: %w", err)
	}
	var parsed struct {
		Panes []struct {
			PaneID string `json:"pane_id"`
		} `json:"panes"`
	}
	if err := json.Unmarshal(result, &parsed); err == nil && len(parsed.Panes) > 0 {
		return parsed.Panes[0].PaneID, nil
	}
	if id := parseStringField(result, "pane", "pane_id"); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("no pane found in workspace %s", workspaceID)
}

func parseStringField(raw json.RawMessage, object, field string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	target := m
	if object != "" {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(m[object], &nested); err != nil {
			return ""
		}
		target = nested
	}
	var s string
	if err := json.Unmarshal(target[field], &s); err != nil {
		return ""
	}
	return s
}

func (p *herdrProc) PID() int { return 0 }

func (p *herdrProc) Wait() error {
	defer func() {
		_, _ = p.conn.call("workspace.close", map[string]any{"workspace_id": p.workspace})
		_ = p.conn.close()
		_ = os.Remove(p.exitFile)
		_ = os.Remove(p.launchFile)
	}()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return p.ctx.Err()
		case <-p.done:
			return context.Canceled
		case <-ticker.C:
			data, err := readExitCode(p.exitFile)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			code := strings.TrimSpace(string(data))
			_, _ = p.SessionRef()
			if code != "0" {
				return fmt.Errorf("exit status %s", code)
			}
			return nil
		}
	}
}

func (p *herdrProc) Interrupt() error {
	_, err := p.conn.call("pane.send_keys", map[string]any{"pane_id": p.paneID, "keys": []string{"ctrl+c"}})
	return err
}

func (p *herdrProc) Kill() error {
	p.stopOnce.Do(func() { close(p.done) })
	_ = os.Remove(p.exitFile)
	_ = os.Remove(p.launchFile)
	defer func() { _ = p.conn.close() }()
	_, _ = p.conn.call("pane.close", map[string]any{"pane_id": p.paneID})
	_, err := p.conn.call("workspace.close", map[string]any{"workspace_id": p.workspace})
	return err
}

func readExitCode(path string) ([]byte, error) {
	return safeio.ReadRegularFile(path, 32, 0o022)
}

func (p *herdrProc) ReportAgentSession(agent, sessionID string) error {
	_, err := p.conn.call("pane.report_agent_session", map[string]any{
		"pane_id": p.paneID, "source": "herdr:" + agent,
		"agent": agent, "agent_session_id": sessionID,
	})
	return err
}
