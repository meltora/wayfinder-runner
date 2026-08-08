package updateipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

const DefaultSocketPath = "/run/our-runner-updater/control.sock"

type Client struct {
	SocketPath     string
	Timeout        time.Duration
	PrepareTimeout time.Duration
	// ExpectedServerUID authenticates the updater through kernel peer
	// credentials. A negative value is reserved for in-process tests.
	ExpectedServerUID int
}

func NewClient(socketPath string) *Client {
	return &Client{SocketPath: socketPath, Timeout: 5 * time.Second, PrepareTimeout: 20 * time.Minute, ExpectedServerUID: -1}
}

func (c *Client) Prepare(ctx context.Context, requestID, updateID string) (Response, error) {
	return c.call(ctx, Request{SchemaVersion: SchemaVersion, RequestID: requestID, Action: ActionPrepare, UpdateID: updateID})
}

func (c *Client) Activate(ctx context.Context, requestID, updateID string) (Response, error) {
	return c.call(ctx, Request{SchemaVersion: SchemaVersion, RequestID: requestID, Action: ActionActivate, UpdateID: updateID})
}

func (c *Client) Health(ctx context.Context, requestID, updateID, runnerVersion string) (Response, error) {
	return c.call(ctx, Request{SchemaVersion: SchemaVersion, RequestID: requestID, Action: ActionHealth, UpdateID: updateID, RunnerVersion: runnerVersion})
}

func (c *Client) Status(ctx context.Context, requestID, updateID string) (Response, error) {
	return c.call(ctx, Request{SchemaVersion: SchemaVersion, RequestID: requestID, Action: ActionStatus, UpdateID: updateID})
}

func (c *Client) call(ctx context.Context, request Request) (Response, error) {
	if err := request.Validate(); err != nil {
		return Response{}, err
	}
	if !filepath.IsAbs(c.SocketPath) || len(c.SocketPath) > 100 {
		return Response{}, fmt.Errorf("updater socket path must be a bounded absolute path")
	}
	if info, err := os.Lstat(c.SocketPath); err != nil {
		return Response{}, fmt.Errorf("inspect updater socket: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return Response{}, fmt.Errorf("updater socket path is not a Unix socket")
	}
	timeout := c.Timeout
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	operationTimeout := timeout
	if request.Action == ActionPrepare {
		operationTimeout = c.PrepareTimeout
		if operationTimeout <= 0 || operationTimeout > 20*time.Minute {
			operationTimeout = 20 * time.Minute
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(callCtx, "unix", c.SocketPath)
	if err != nil {
		return Response{}, fmt.Errorf("connect updater: %w", err)
	}
	defer conn.Close()
	if c.ExpectedServerUID >= 0 {
		if int64(c.ExpectedServerUID) > int64(^uint32(0)) {
			return Response{}, fmt.Errorf("authenticate updater peer: configured uid is out of range")
		}
		uid, err := unixPeerUID(conn)
		if err != nil {
			return Response{}, fmt.Errorf("authenticate updater peer: %w", err)
		}
		// #nosec G115 -- ExpectedServerUID is non-negative and bounded above.
		if uid != uint32(c.ExpectedServerUID) {
			return Response{}, fmt.Errorf("authenticate updater peer: unexpected server uid")
		}
	}
	writeDeadline := time.Now().Add(timeout)
	if contextDeadline, ok := callCtx.Deadline(); ok && contextDeadline.Before(writeDeadline) {
		writeDeadline = contextDeadline
	}
	if err := conn.SetDeadline(writeDeadline); err != nil {
		return Response{}, fmt.Errorf("set updater IPC deadline: %w", err)
	}
	if err := EncodeRequest(conn, request); err != nil {
		return Response{}, err
	}
	if unixConn, ok := conn.(*net.UnixConn); ok {
		if err := unixConn.CloseWrite(); err != nil {
			return Response{}, fmt.Errorf("finish updater IPC request: %w", err)
		}
	}
	readDeadline := time.Now().Add(operationTimeout)
	if contextDeadline, ok := callCtx.Deadline(); ok && contextDeadline.Before(readDeadline) {
		readDeadline = contextDeadline
	}
	if err := conn.SetReadDeadline(readDeadline); err != nil {
		return Response{}, fmt.Errorf("set updater IPC response deadline: %w", err)
	}
	response, err := DecodeResponse(conn)
	if err != nil {
		return Response{}, err
	}
	if response.UpdateID != "" && response.UpdateID != request.UpdateID {
		return Response{}, fmt.Errorf("updater response update_id mismatch")
	}
	return response, nil
}
