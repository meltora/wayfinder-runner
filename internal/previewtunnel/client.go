package previewtunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/example/app-runner/internal/transport"
)

const (
	TunnelPath           = "/v1/runners/preview-tunnel"
	PingInterval         = 20 * time.Second
	PingTimeout          = 60 * time.Second
	CloseTimeout         = 2 * time.Second
	CloseNormal          = 1000
	CloseUnsupportedData = 1003
	minReconnectBackoff  = time.Second
	maxReconnectBackoff  = 60 * time.Second
	backoffResetAfter    = time.Minute
	dialTimeout          = 10 * time.Second
	textMessageReason    = "binary messages only"
	rotatedReason        = "certificate rotated"
	goodbyeReason        = "bye"
)

var ErrTextMessage = errors.New("preview tunnel received a text message")

type Client struct {
	url          string
	resolver     Resolver
	logger       *slog.Logger
	pingInterval time.Duration
	pingTimeout  time.Duration
	closeTimeout time.Duration
	backoff      func(time.Duration) time.Duration
	now          func() time.Time
	dial         DialFunc

	mu      sync.Mutex
	tlsCfg  *tls.Config
	conn    *wsConn
	session *Session
}

func NewClient(url string, tlsCfg *tls.Config, resolver Resolver, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	return &Client{
		url:          url,
		tlsCfg:       tlsCfg,
		resolver:     resolver,
		logger:       logger,
		pingInterval: PingInterval,
		pingTimeout:  PingTimeout,
		closeTimeout: CloseTimeout,
		backoff:      transport.JitterBackoff,
		now:          time.Now,
		dial: func(ctx context.Context, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", address)
		},
	}
}

func TunnelURL(controlURL string) (string, error) {
	parsed, err := url.Parse(controlURL)
	if err != nil {
		return "", fmt.Errorf("parse control url: %w", err)
	}
	if parsed.Scheme != "wss" && parsed.Scheme != "ws" {
		return "", fmt.Errorf("control url scheme %q is not wss or ws", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("control url has no host")
	}
	parsed.User = nil
	parsed.Path = TunnelPath
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String(), nil
}

func (c *Client) UpdateTLSConfig(cfg *tls.Config) {
	c.mu.Lock()
	c.tlsCfg = cfg
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close(CloseNormal, rotatedReason)
	}
}

func (c *Client) ResetPreview(previewID string) {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session != nil {
		session.ResetPreview(previewID)
	}
}

func (c *Client) Run(ctx context.Context) error {
	backoff := minReconnectBackoff
	for {
		connectedAt, err := c.connectOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrProtocol) || errors.Is(err, ErrOutboxOverflow) {
			c.logger.Warn("preview tunnel disconnected", "error", err)
		} else {
			c.logger.Debug("preview tunnel disconnected", "error", err)
		}
		if !connectedAt.IsZero() && c.now().Sub(connectedAt) >= backoffResetAfter {
			backoff = minReconnectBackoff
		}
		timer := time.NewTimer(c.backoff(backoff))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, maxReconnectBackoff)
	}
}

func (c *Client) connectOnce(ctx context.Context) (time.Time, error) {
	c.mu.Lock()
	tlsCfg := c.tlsCfg
	c.mu.Unlock()
	httpTransport := &http.Transport{
		TLSClientConfig:     tlsCfg.Clone(),
		DialContext:         (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: dialTimeout,
	}
	defer httpTransport.CloseIdleConnections()
	conn, resp, err := websocket.Dial(ctx, c.url, &websocket.DialOptions{HTTPClient: &http.Client{Transport: httpTransport}})
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			return time.Time{}, fmt.Errorf("dial: upgrade rejected with HTTP %d: %w", resp.StatusCode, err)
		}
		return time.Time{}, fmt.Errorf("dial: %w", err)
	}
	connectedAt := c.now()
	ws := newWSConn(conn, c.closeTimeout)
	defer ws.abort()
	session := NewSession(ws, c.resolver, c.dial, c.logger, nil)
	c.mu.Lock()
	if c.tlsCfg != tlsCfg {
		c.mu.Unlock()
		_ = conn.CloseNow()
		return connectedAt, errors.New("tls config changed while dialing")
	}
	c.conn = ws
	c.session = session
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.session = nil
		c.mu.Unlock()
	}()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var helpers sync.WaitGroup
	helpers.Add(2)
	go func() {
		defer helpers.Done()
		<-runCtx.Done()
		_ = ws.Close(CloseNormal, goodbyeReason)
	}()
	go func() {
		defer helpers.Done()
		c.keepAlive(runCtx, ws)
	}()
	err = session.Run(ctx)
	_ = ws.Close(CloseNormal, goodbyeReason)
	ws.wait()
	cancel()
	helpers.Wait()
	return connectedAt, err
}

func (c *Client) keepAlive(ctx context.Context, ws *wsConn) {
	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pingCtx, cancel := context.WithTimeout(ws.ctx, c.pingTimeout)
		err := ws.conn.Ping(pingCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				c.logger.Debug("preview tunnel ping failed", "error", err)
			}
			ws.abort()
			return
		}
	}
}

type wsConn struct {
	conn         *websocket.Conn
	ctx          context.Context
	abort        context.CancelFunc
	closeTimeout time.Duration
	closeOnce    sync.Once
	closed       chan struct{}
}

func newWSConn(conn *websocket.Conn, closeTimeout time.Duration) *wsConn {
	conn.SetReadLimit(MaxMessage + 1)
	ctx, abort := context.WithCancel(context.Background())
	return &wsConn{conn: conn, ctx: ctx, abort: abort, closeTimeout: closeTimeout, closed: make(chan struct{})}
}

func (w *wsConn) ReadMessage(context.Context) ([]byte, error) {
	messageType, reader, err := w.conn.Reader(w.ctx)
	if err != nil {
		return nil, err
	}
	if messageType != websocket.MessageBinary {
		_ = w.Close(CloseUnsupportedData, textMessageReason)
		return nil, ErrTextMessage
	}
	message, err := io.ReadAll(io.LimitReader(reader, MaxMessage+1))
	if err != nil {
		return nil, err
	}
	if len(message) > MaxMessage {
		return nil, protocolError("message exceeds %d bytes", MaxMessage)
	}
	return message, nil
}

func (w *wsConn) WriteMessage(_ context.Context, message []byte) error {
	err := w.conn.Write(w.ctx, websocket.MessageBinary, message)
	if err != nil {
		w.abort()
	}
	return err
}

func (w *wsConn) Close(code int, reason string) error {
	w.closeOnce.Do(func() {
		go func() {
			defer close(w.closed)
			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = w.conn.Close(websocket.StatusCode(code), reason)
			}()
			timer := time.NewTimer(w.closeTimeout)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				w.abort()
			}
		}()
	})
	return nil
}

func (w *wsConn) wait() {
	<-w.closed
}
