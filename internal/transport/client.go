package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type Handler interface {
	OnCommand(ctx context.Context, env v1.Envelope) error
	OnEventAck(ctx context.Context, eventID string)
	OnConnected(ctx context.Context)
	OnDisconnected(err error)
}

type Client struct {
	url      string
	tlsCfg   *tls.Config
	handler  Handler
	runnerID string
	version  string

	mu          sync.Mutex
	writeMu     sync.Mutex
	conn        *websocket.Conn
	connected   bool
	heartbeatIv time.Duration
	connectedAt time.Time

	ReplayUnacked    func(ctx context.Context, send func(v1.Envelope) error) error
	HeartbeatPayload func(context.Context) v1.HeartbeatPayload
}

func NewClient(url, runnerID, version string, tlsCfg *tls.Config, h Handler) *Client {
	return &Client{url: url, tlsCfg: tlsCfg, handler: h, runnerID: runnerID, version: version, heartbeatIv: 30 * time.Second}
}

func TLSConfigFromPEM(certPEM, keyPEM, caPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load keypair: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("invalid CA PEM")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func (c *Client) UpdateTLSConfig(cfg *tls.Config) {
	c.mu.Lock()
	c.tlsCfg = cfg
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "certificate rotated")
	}
}

func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		err := c.connectOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.handler.OnDisconnected(err)
		c.mu.Lock()
		connectedAt := c.connectedAt
		c.connectedAt = time.Time{}
		c.mu.Unlock()
		if !connectedAt.IsZero() && time.Since(connectedAt) >= time.Minute {
			backoff = time.Second
		}
		wait := jitterBackoff(backoff)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, 60*time.Second)
	}
}

func jitterBackoff(backoff time.Duration) time.Duration {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return backoff
	}
	percent := 80 + binary.LittleEndian.Uint64(random[:])%41
	// #nosec G115 -- percent is mathematically constrained to [80, 120].
	return backoff * time.Duration(percent) / 100
}

func (c *Client) connectOnce(ctx context.Context) error {
	c.mu.Lock()
	tlsCfg := c.tlsCfg
	c.mu.Unlock()
	dialer := &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{
			TLSClientConfig:     tlsCfg.Clone(),
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		}},
	}
	conn, _, err := websocket.Dial(ctx, c.url, dialer)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	conn.SetReadLimit(2 << 20)
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		_ = conn.Close(websocket.StatusNormalClosure, "bye")
		c.mu.Lock()
		c.conn = nil
		c.connected = false
		c.mu.Unlock()
	}()

	if err := c.send(ctx, v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeHello, ID: newID(),
		SentAt: time.Now(), RunnerID: c.runnerID,
		Payload: v1.HelloPayload{ProtocolVersion: v1.ProtocolVersion, RunnerVersion: c.version},
	}); err != nil {
		return err
	}

	var ack v1.Envelope
	helloCtx, helloCancel := context.WithTimeout(ctx, 15*time.Second)
	defer helloCancel()
	if err := c.readInto(helloCtx, &ack); err != nil {
		return err
	}
	if ack.Type != v1.TypeHelloAck {
		return fmt.Errorf("expected hello_ack, got %s", ack.Type)
	}
	ackPayload, err := decodePayload[v1.HelloAckPayload](ack)
	if err != nil {
		return err
	}
	if !ackPayload.Accepted {
		return fmt.Errorf("connection rejected: %s", ackPayload.RejectReason)
	}
	if ackPayload.HeartbeatInterval != 0 && (ackPayload.HeartbeatInterval < 1 || ackPayload.HeartbeatInterval > 300) {
		return fmt.Errorf("hello_ack heartbeat interval must be between 1 and 300 seconds")
	}
	if ackPayload.HeartbeatInterval > 0 {
		c.heartbeatIv = time.Duration(ackPayload.HeartbeatInterval) * time.Second
	}
	c.mu.Lock()
	c.connected = true
	c.connectedAt = time.Now()
	c.mu.Unlock()

	c.handler.OnConnected(ctx)
	if c.ReplayUnacked != nil {
		if err := c.ReplayUnacked(ctx, func(env v1.Envelope) error { return c.send(ctx, env) }); err != nil {
			return fmt.Errorf("replay: %w", err)
		}
	}

	errCh := make(chan error, 1)
	go func() { errCh <- c.readLoop(ctx) }()
	ticker := time.NewTicker(c.heartbeatIv)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		case <-ticker.C:
			payload := v1.HeartbeatPayload{State: "idle"}
			if c.HeartbeatPayload != nil {
				payload = c.HeartbeatPayload(ctx)
			}
			if err := c.send(ctx, v1.Envelope{
				Version: v1.ProtocolVersion, Type: v1.TypeHeartbeat, ID: newID(),
				SentAt: time.Now(), RunnerID: c.runnerID,
				Payload: payload,
			}); err != nil {
				return err
			}
		}
	}
}

func (c *Client) readLoop(ctx context.Context) error {
	for {
		var env v1.Envelope
		if err := c.readInto(ctx, &env); err != nil {
			return err
		}
		switch env.Type {
		case v1.TypeCommand:
			if err := c.handler.OnCommand(ctx, env); err != nil {
				return err
			}
		case v1.TypeEventAck:
			if env.InReplyTo == "" {
				return fmt.Errorf("event_ack is missing in_reply_to")
			}
			c.handler.OnEventAck(ctx, env.InReplyTo)
		case v1.TypeHeartbeatAck:
		default:
		}
	}
}

func (c *Client) SendEvent(ctx context.Context, envelope v1.Envelope) error {
	if envelope.Version != v1.ProtocolVersion || envelope.Type != v1.TypeEvent ||
		envelope.ID == "" || envelope.SentAt.IsZero() || envelope.RunnerID != c.runnerID ||
		envelope.InReplyTo != "" {
		return fmt.Errorf("invalid outbound event envelope")
	}
	return c.send(ctx, envelope)
}

func (c *Client) SendCommandAck(ctx context.Context, commandID string, payload v1.CommandAckPayload) error {
	return c.send(ctx, v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommandAck, ID: newID(),
		SentAt: time.Now(), RunnerID: c.runnerID, InReplyTo: commandID, Payload: payload,
	})
}

func (c *Client) send(ctx context.Context, env v1.Envelope) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.Write(ctx, websocket.MessageText, data)
}

func (c *Client) readInto(ctx context.Context, env *v1.Envelope) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(env); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("envelope contains trailing JSON")
	}
	return nil
}

func decodePayload[T any](env v1.Envelope) (T, error) {
	var out T
	data, err := json.Marshal(env.Payload)
	if err != nil {
		return out, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return out, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return out, fmt.Errorf("payload contains trailing JSON")
	}
	return out, nil
}
