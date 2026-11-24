package previewtunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
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
	CloseUnsupportedData = 1003
	minReconnectBackoff  = time.Second
	maxReconnectBackoff  = 60 * time.Second
	backoffResetAfter    = time.Minute
	dialTimeout          = 10 * time.Second
	textMessageReason    = "binary messages only"
	rotatedReason        = "certificate rotated"
)

var ErrTextMessage = errors.New("preview tunnel received a text message")

type Client struct {
	url          string
	resolver     Resolver
	logger       *slog.Logger
	pingInterval time.Duration
	pingTimeout  time.Duration
	backoff      func(time.Duration) time.Duration
	now          func() time.Time
	dial         DialFunc

	mu      sync.Mutex
	tlsCfg  *tls.Config
	conn    *websocket.Conn
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
	switch parsed.Scheme {
	case "wss", "ws", "https", "http":
	default:
		return "", fmt.Errorf("control url scheme %q is not a websocket or http scheme", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("control url has no host")
	}
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
		_ = conn.Close(websocket.StatusNormalClosure, rotatedReason)
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
		c.logger.Debug("preview tunnel disconnected", "error", err)
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
	conn.SetReadLimit(MaxMessage)
	session := NewSession(&wsConn{conn: conn}, c.resolver, c.dial, c.logger, nil)
	c.mu.Lock()
	rotated := c.tlsCfg != tlsCfg
	c.conn = conn
	c.session = session
	c.mu.Unlock()
	connectedAt := c.now()
	defer func() {
		_ = conn.CloseNow()
		c.mu.Lock()
		c.conn = nil
		c.session = nil
		c.mu.Unlock()
	}()
	if rotated {
		_ = conn.Close(websocket.StatusNormalClosure, rotatedReason)
		return connectedAt, errors.New("tls config changed while dialing")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	keepAliveDone := make(chan struct{})
	go func() {
		defer close(keepAliveDone)
		c.keepAlive(runCtx, conn)
	}()
	err = session.Run(runCtx)
	cancel()
	<-keepAliveDone
	return connectedAt, err
}

func (c *Client) keepAlive(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pingCtx, cancel := context.WithTimeout(ctx, c.pingTimeout)
		err := conn.Ping(pingCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				c.logger.Debug("preview tunnel ping failed", "error", err)
			}
			_ = conn.CloseNow()
			return
		}
	}
}

type wsConn struct {
	conn *websocket.Conn
}

func (w *wsConn) ReadMessage(ctx context.Context) ([]byte, error) {
	messageType, message, err := w.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if messageType != websocket.MessageBinary {
		_ = w.conn.Close(CloseUnsupportedData, textMessageReason)
		return nil, ErrTextMessage
	}
	return message, nil
}

func (w *wsConn) WriteMessage(ctx context.Context, message []byte) error {
	return w.conn.Write(ctx, websocket.MessageBinary, message)
}

func (w *wsConn) Close(code int, reason string) error {
	return w.conn.Close(websocket.StatusCode(code), reason)
}
