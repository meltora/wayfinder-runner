package egresspolicy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

const (
	SchemaVersion     = 1
	MaxMessageBytes   = 16 << 10
	DefaultSocketPath = "/run/our-runner-egress/policy.sock"

	defaultAttestationLease = 5 * time.Second
	maximumPolicyLease      = 40 * time.Second
)

var (
	idPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	digestPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	networkIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Binding is the stable, externally issued identity of the currently
// effective host egress rules. It is safe to bind into signed capabilities.
type Binding struct {
	NetworkID     string `json:"network_id"`
	PolicyID      string `json:"policy_id"`
	EnforcementID string `json:"enforcement_id"`
	RulesDigest   string `json:"rules_digest"`
}

type request struct {
	SchemaVersion   int       `json:"schema_version"`
	Nonce           string    `json:"nonce"`
	NetworkID       string    `json:"network_id"`
	NetworkName     string    `json:"network_name"`
	PolicyID        string    `json:"policy_id"`
	LeaseDurationMS int64     `json:"lease_duration_ms"`
	RequestedAt     time.Time `json:"-"`
}

type response struct {
	SchemaVersion                   int       `json:"schema_version"`
	Nonce                           string    `json:"nonce"`
	NetworkID                       string    `json:"network_id"`
	PolicyID                        string    `json:"policy_id"`
	EnforcementID                   string    `json:"enforcement_id"`
	RulesDigest                     string    `json:"rules_digest"`
	LeaseID                         string    `json:"lease_id"`
	Effective                       bool      `json:"effective"`
	UnauthorizedReachabilityBlocked bool      `json:"unauthorized_reachability_blocked"`
	ObservedAt                      time.Time `json:"observed_at"`
	LeaseExpiresAt                  time.Time `json:"lease_expires_at"`
}

type releaseRequest struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Nonce         string `json:"nonce"`
	LeaseID       string `json:"lease_id"`
}

type releaseResponse struct {
	SchemaVersion int       `json:"schema_version"`
	Kind          string    `json:"kind"`
	Nonce         string    `json:"nonce"`
	LeaseID       string    `json:"lease_id"`
	ReleasedAt    time.Time `json:"released_at"`
}

// Lease is a root-verifier-held policy lease. The verifier contract guarantees
// that the exact enforcement/rules binding cannot change while this
// authenticated Unix connection remains open. A verifier crash, revocation,
// unexpected payload, or lease expiry is reported through Failures.
type Lease struct {
	Binding   Binding
	ID        string
	ExpiresAt time.Time

	conn             net.Conn
	releaseNonce     string
	observedAt       time.Time
	now              func() time.Time
	failures         chan error
	completion       chan error
	releaseRequested chan struct{}
	closed           chan struct{}
	closeOnce        sync.Once
	completeOnce     sync.Once
	completeErr      error
}

func (lease *Lease) Failures() <-chan error { return lease.failures }

func (lease *Lease) Complete(ctx context.Context) error {
	lease.completeOnce.Do(func() {
		releaseNonce := make([]byte, 32)
		if _, err := rand.Read(releaseNonce); err != nil {
			lease.completeErr = fmt.Errorf("create egress policy release nonce: %w", err)
			return
		}
		lease.releaseNonce = hex.EncodeToString(releaseNonce)
		close(lease.releaseRequested)
		deadline := time.Now().Add(2 * time.Second)
		if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
			deadline = requested
		}
		if err := lease.conn.SetWriteDeadline(deadline); err != nil {
			lease.completeErr = err
			return
		}
		if err := encodeMessage(lease.conn, releaseRequest{
			SchemaVersion: SchemaVersion, Kind: "release", Nonce: lease.releaseNonce, LeaseID: lease.ID,
		}); err != nil {
			lease.completeErr = err
			return
		}
		select {
		case lease.completeErr = <-lease.completion:
		case <-ctx.Done():
			lease.completeErr = ctx.Err()
		}
	})
	return lease.completeErr
}

func (lease *Lease) Close() error {
	var err error
	lease.closeOnce.Do(func() {
		close(lease.closed)
		err = lease.conn.Close()
	})
	return err
}

type Client struct {
	SocketPath        string
	ExpectedServerUID int
	Timeout           time.Duration
	now               func() time.Time
}

func NewClient(socketPath string) *Client {
	return &Client{SocketPath: socketPath, ExpectedServerUID: 0, Timeout: 2 * time.Second, now: time.Now}
}

func (client *Client) Attest(ctx context.Context, networkID, networkName, policyID string) (Binding, error) {
	lease, err := client.Acquire(ctx, networkID, networkName, policyID, defaultAttestationLease)
	if err != nil {
		return Binding{}, err
	}
	binding := lease.Binding
	if err := lease.Close(); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

func (client *Client) Acquire(
	ctx context.Context,
	networkID string,
	networkName string,
	policyID string,
	duration time.Duration,
) (*Lease, error) {
	if !networkIDPattern.MatchString(networkID) || !idPattern.MatchString(networkName) || !idPattern.MatchString(policyID) {
		return nil, fmt.Errorf("effective egress attestation authority is invalid")
	}
	if duration < time.Second || duration > maximumPolicyLease || duration%time.Millisecond != 0 {
		return nil, fmt.Errorf("effective egress policy lease duration is invalid")
	}
	if !filepath.IsAbs(client.SocketPath) || filepath.Clean(client.SocketPath) != client.SocketPath || len(client.SocketPath) > 100 {
		return nil, fmt.Errorf("egress policy verifier socket must be a clean bounded absolute path")
	}
	info, err := os.Lstat(client.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("inspect egress policy verifier socket: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("egress policy verifier path is not a Unix socket")
	}
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("create egress policy attestation nonce: %w", err)
	}
	req := request{
		SchemaVersion: SchemaVersion, Nonce: hex.EncodeToString(nonceBytes),
		NetworkID: networkID, NetworkName: networkName, PolicyID: policyID,
		LeaseDurationMS: duration.Milliseconds(), RequestedAt: client.currentTime().UTC(),
	}
	conn, reader, err := client.exchange(ctx, req)
	if err != nil {
		return nil, err
	}
	var res response
	if err := decodeMessage(reader, &res); err != nil {
		_ = conn.Close()
		return nil, err
	}
	binding, err := client.validateResponse(req, res)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.SetReadDeadline(res.LeaseExpiresAt); err != nil {
		_ = conn.Close()
		return nil, err
	}
	lease := &Lease{
		Binding: binding, ID: res.LeaseID, ExpiresAt: res.LeaseExpiresAt,
		conn: conn, observedAt: res.ObservedAt, now: client.currentTime,
		failures: make(chan error, 1), completion: make(chan error, 1),
		releaseRequested: make(chan struct{}), closed: make(chan struct{}),
	}
	go lease.monitor(reader)
	return lease, nil
}

func (client *Client) exchange(ctx context.Context, req request) (net.Conn, *bufio.Reader, error) {
	timeout := client.Timeout
	if timeout <= 0 || timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(callCtx, "unix", client.SocketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("connect egress policy verifier: %w", err)
	}
	if client.ExpectedServerUID >= 0 {
		if int64(client.ExpectedServerUID) > int64(^uint32(0)) {
			_ = conn.Close()
			return nil, nil, fmt.Errorf("authenticate egress policy verifier peer")
		}
		uid, peerErr := unixPeerUID(conn)
		if peerErr != nil || uid != uint32(client.ExpectedServerUID) {
			_ = conn.Close()
			return nil, nil, fmt.Errorf("authenticate egress policy verifier peer")
		}
	}
	deadline := client.currentTime().Add(timeout)
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := encodeMessage(conn, req); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, bufio.NewReaderSize(conn, MaxMessageBytes+2), nil
}

func (client *Client) validateResponse(req request, res response) (Binding, error) {
	now := client.currentTime().UTC()
	requested := time.Duration(req.LeaseDurationMS) * time.Millisecond
	leaseLifetime := res.LeaseExpiresAt.Sub(res.ObservedAt)
	if res.SchemaVersion != SchemaVersion || res.Nonce != req.Nonce || res.NetworkID != req.NetworkID || res.PolicyID != req.PolicyID ||
		!idPattern.MatchString(res.EnforcementID) || !digestPattern.MatchString(res.RulesDigest) || !idPattern.MatchString(res.LeaseID) ||
		!res.Effective || !res.UnauthorizedReachabilityBlocked || res.ObservedAt.IsZero() || res.LeaseExpiresAt.IsZero() ||
		res.ObservedAt.Before(req.RequestedAt) || res.ObservedAt.After(now.Add(2*time.Second)) ||
		!res.LeaseExpiresAt.After(now) || leaseLifetime < requested || leaseLifetime > requested+5*time.Second {
		return Binding{}, fmt.Errorf("egress policy verifier did not acquire a fresh effective restricted policy lease")
	}
	return Binding{NetworkID: res.NetworkID, PolicyID: res.PolicyID, EnforcementID: res.EnforcementID, RulesDigest: res.RulesDigest}, nil
}

func (client *Client) currentTime() time.Time {
	if client.now != nil {
		return client.now()
	}
	return time.Now()
}

func (lease *Lease) monitor(reader *bufio.Reader) {
	var released releaseResponse
	err := decodeMessage(reader, &released)
	select {
	case <-lease.closed:
		return
	default:
	}
	if err != nil {
		err = fmt.Errorf("effective egress policy lease ended: %w", err)
		lease.reportFailure(err)
		return
	}
	select {
	case <-lease.releaseRequested:
	default:
		lease.reportFailure(fmt.Errorf("egress policy verifier released the policy lease before request"))
		return
	}
	if released.SchemaVersion != SchemaVersion || released.Kind != "held_through_release" ||
		released.Nonce != lease.releaseNonce || released.LeaseID != lease.ID || released.ReleasedAt.IsZero() ||
		released.ReleasedAt.Before(lease.observedAt) || released.ReleasedAt.After(lease.ExpiresAt) ||
		released.ReleasedAt.After(lease.now().UTC().Add(2*time.Second)) {
		lease.reportFailure(fmt.Errorf("egress policy verifier returned an invalid policy lease release"))
		return
	}
	lease.completion <- nil
}

func (lease *Lease) reportFailure(err error) {
	lease.failures <- err
	lease.completion <- err
}

func encodeMessage(dst io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 || len(raw) > MaxMessageBytes {
		return fmt.Errorf("encode egress policy IPC message")
	}
	raw = append(raw, '\n')
	_, err = dst.Write(raw)
	return err
}

func decodeMessage(reader *bufio.Reader, destination any) error {
	raw, err := reader.ReadSlice('\n')
	if err != nil || len(raw) <= 1 || len(raw) > MaxMessageBytes+1 {
		return fmt.Errorf("read egress policy IPC message")
	}
	raw = bytes.TrimSuffix(raw, []byte{'\n'})
	if err := rejectDuplicateFields(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode egress policy IPC message: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("decode egress policy IPC message: trailing data")
	}
	return nil
}

func rejectDuplicateFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode egress policy IPC message: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return fmt.Errorf("egress policy IPC message must be an object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := field.(string)
		if !ok || seen[name] {
			return fmt.Errorf("egress policy IPC message contains a duplicate field")
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	return nil
}
