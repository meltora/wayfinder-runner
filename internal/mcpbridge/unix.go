package mcpbridge

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxSocketRequestBytes  = 1 << 20
	maxSocketResponseBytes = 2 << 20
	maxSocketConnections   = 16
	requestReadTimeout     = 10 * time.Second
	toolCallTimeout        = 31 * time.Minute
	responseWriteTimeout   = 10 * time.Second
)

type socketRequest struct {
	ID    uint64          `json:"id"`
	Token string          `json:"token"`
	Tool  string          `json:"tool"`
	Args  json.RawMessage `json:"args"`
}

type socketResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type UnixClient struct {
	SocketPath string
	Token      string
	counter    atomic.Uint64
}

func (c *UnixClient) dial(ctx context.Context) (net.Conn, error) {
	if strings.HasPrefix(c.SocketPath, "tcp://") {
		return nil, fmt.Errorf("TCP MCP bridges are disabled")
	}
	if !filepath.IsAbs(c.SocketPath) {
		return nil, fmt.Errorf("MCP socket path must be absolute")
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", c.SocketPath)
}

func (c *UnixClient) Call(ctx context.Context, tool string, args json.RawMessage) (any, error) {
	if err := ValidateSocketTool(tool); err != nil {
		return nil, err
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp socket dial: %w", err)
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(toolCallTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	id := c.counter.Add(1)
	req := socketRequest{ID: id, Token: c.Token, Tool: tool, Args: args}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	line, err := scanJSONLine(conn, maxSocketResponseBytes)
	if err != nil {
		return nil, err
	}
	var resp socketResponse
	if err := decodeStrict(line, &resp); err != nil {
		return nil, err
	}
	if resp.ID != id {
		return nil, fmt.Errorf("MCP response ID mismatch")
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	var out any
	if len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type JobHandler func(ctx context.Context, tool string, args json.RawMessage) (any, error)

type UnixServer struct {
	SocketPath string
	Token      string
	Handler    JobHandler
	ln         net.Listener
	sem        chan struct{}
	startOnce  sync.Once
}

func (s *UnixServer) Start(ctx context.Context) error {
	if strings.HasPrefix(s.SocketPath, "tcp://") {
		return fmt.Errorf("TCP MCP bridges are disabled")
	}
	if !filepath.IsAbs(s.SocketPath) {
		return fmt.Errorf("MCP socket path must be absolute")
	}
	if s.Token == "" || s.Handler == nil {
		return fmt.Errorf("MCP token and handler are required")
	}
	if info, err := os.Lstat(s.SocketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to replace non-socket MCP path")
		}
		if err := os.Remove(s.SocketPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	ln, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.SocketPath, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	s.ln = ln
	s.startOnce.Do(func() { s.sem = make(chan struct{}, maxSocketConnections) })
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go s.acceptLoop(ctx)
	return nil
}

func (s *UnixServer) acceptLoop(ctx context.Context) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		select {
		case s.sem <- struct{}{}:
			go func() {
				defer func() { <-s.sem }()
				s.serveConn(ctx, conn)
			}()
		default:
			_ = conn.Close()
		}
	}
}

func (s *UnixServer) serveConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(requestReadTimeout))
	enc := json.NewEncoder(conn)
	line, err := scanJSONLine(conn, maxSocketRequestBytes)
	if err != nil {
		return
	}
	var req socketRequest
	if err := decodeStrict(line, &req); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	var resp socketResponse
	resp.ID = req.ID
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.Token)) != 1 {
		resp.Error = "invalid job token"
	} else if err := ValidateSocketTool(req.Tool); err != nil {
		resp.Error = err.Error()
	} else {
		callCtx, cancel := context.WithTimeout(ctx, toolCallTimeout)
		result, err := s.Handler(callCtx, req.Tool, req.Args)
		cancel()
		if err != nil {
			resp.Error = truncateError(err.Error())
		} else {
			raw, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				resp.Error = "tool result could not be encoded"
			} else {
				resp.Result = raw
			}
		}
	}
	_ = conn.SetWriteDeadline(time.Now().Add(responseWriteTimeout))
	_ = enc.Encode(resp)
}

func scanJSONLine(reader io.Reader, limit int) ([]byte, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, min(limit, 64<<10)), limit)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	line := append([]byte(nil), scanner.Bytes()...)
	if len(line) == 0 {
		return nil, fmt.Errorf("empty JSON message")
	}
	return line, nil
}

func truncateError(message string) string {
	const max = 1000
	if len(message) <= max {
		return message
	}
	return message[:max] + "…"
}
