//go:build rotationinterop

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/enroll"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/transport"
)

type result struct {
	Mode                   string `json:"mode"`
	RunnerID               string `json:"runner_id"`
	PreviousSPKI           string `json:"previous_spki"`
	CurrentSPKI            string `json:"current_spki"`
	CurrentFingerprint     string `json:"current_fingerprint"`
	PreviousRemoved        bool   `json:"previous_removed"`
	PendingRemoved         bool   `json:"pending_removed"`
	ReplayedFingerprint    string `json:"replayed_fingerprint,omitempty"`
	OldUpgradeUnauthorized bool   `json:"old_upgrade_unauthorized"`
	OldRotateStatus        int    `json:"old_rotate_status"`
}

type material struct {
	certPEM []byte
	keyPEM  []byte
	caPEM   []byte
	spki    string
}

func spkiOf(key *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

func fingerprintOf(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", errors.New("invalid certificate PEM")
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

func current(store *identity.Store) (material, error) {
	certPEM, caPEM, err := store.LoadCert()
	if err != nil {
		return material{}, err
	}
	key, err := store.LoadKey()
	if err != nil {
		return material{}, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return material{}, err
	}
	spki, err := spkiOf(key)
	if err != nil {
		return material{}, err
	}
	return material{certPEM: certPEM, keyPEM: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), caPEM: caPEM, spki: spki}, nil
}

func rotationClient(base string, m material) (*enroll.Client, error) {
	tlsCfg, err := transport.TLSConfigFromPEM(m.certPEM, m.keyPEM, m.caPEM)
	if err != nil {
		return nil, err
	}
	return &enroll.Client{BaseURL: base, HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}}}, nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

type staleHandler struct {
	mu      sync.Mutex
	lastErr error
	cancel  context.CancelFunc
}

func (h *staleHandler) OnCommand(context.Context, v1.Envelope) error { return nil }
func (h *staleHandler) OnEventAck(context.Context, string)           {}
func (h *staleHandler) OnConnected(context.Context)                  {}
func (h *staleHandler) OnDisconnected(err error) {
	h.mu.Lock()
	h.lastErr = err
	h.mu.Unlock()
	h.cancel()
}

func oldUpgradeUnauthorized(ctx context.Context, wss, runnerID string, old material) (bool, error) {
	tlsCfg, err := transport.TLSConfigFromPEM(old.certPEM, old.keyPEM, old.caPEM)
	if err != nil {
		return false, err
	}
	staleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	h := &staleHandler{cancel: cancel}
	_ = transport.NewClient(wss, runnerID, "rotation-interop", tlsCfg, h).Run(staleCtx)
	h.mu.Lock()
	defer h.mu.Unlock()
	return transport.IsUpgradeUnauthorized(h.lastErr), nil
}

func oldRotateStatus(ctx context.Context, base, runnerID string, old material) (int, error) {
	client, err := rotationClient(base, old)
	if err != nil {
		return 0, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return 0, err
	}
	csr, err := identity.NewCSR(key, runnerID)
	if err != nil {
		return 0, err
	}
	_, err = client.Rotate(ctx, enroll.RotateRequest{CSR: string(csr), RunnerVersion: daemon.Version})
	var rejected *enroll.RotateRejectedError
	if !errors.As(err, &rejected) {
		return 0, fmt.Errorf("rotation with the superseded certificate was not rejected: %v", err)
	}
	return rejected.Status, nil
}

func loseRotationResponse(ctx context.Context, store *identity.Store, base, runnerID string, old material) (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	if err := store.SavePendingKey(key); err != nil {
		return "", err
	}
	client, err := rotationClient(base, old)
	if err != nil {
		return "", err
	}
	csr, err := identity.NewCSR(key, runnerID)
	if err != nil {
		return "", err
	}
	resp, err := client.Rotate(ctx, enroll.RotateRequest{CSR: string(csr), RunnerVersion: daemon.Version})
	if err != nil {
		return "", err
	}
	return fingerprintOf([]byte(resp.Certificate))
}

func run() error {
	stateDir := flag.String("state-dir", "", "")
	gateway := flag.String("gateway", "", "")
	mode := flag.String("mode", "", "")
	clockOffset := flag.Duration("clock-offset", 0, "")
	flag.Parse()
	if *stateDir == "" || !strings.HasPrefix(*gateway, "wss://") {
		return errors.New("state directory and wss gateway URL are required")
	}
	if *mode != "renewal" && *mode != "operator" && *mode != "lost-response" {
		return fmt.Errorf("unknown mode %q", *mode)
	}
	base := "https://" + strings.TrimPrefix(*gateway, "wss://")
	if i := strings.Index(base, "/v1/"); i >= 0 {
		base = base[:i]
	}
	runnerID, err := daemon.ReadRunnerID(*stateDir)
	if err != nil {
		return err
	}
	store := identity.NewStore(*stateDir)
	old, err := current(store)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res := result{Mode: *mode, RunnerID: runnerID, PreviousSPKI: old.spki}
	if *mode == "lost-response" {
		if res.ReplayedFingerprint, err = loseRotationResponse(ctx, store, base, runnerID, old); err != nil {
			return err
		}
	}
	d, err := daemon.New(config.Config{
		ControlPlaneURL:   *gateway,
		StateDir:          *stateDir,
		Transport:         "websocket",
		Isolation:         "direct",
		AllowUnsafeDirect: true,
		Supervisor:        "direct",
		Provider:          "codex",
	}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		return err
	}
	if *clockOffset != 0 {
		d.SetInteropClock(func() time.Time { return time.Now().Add(*clockOffset) })
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- d.Run(runCtx) }()
	deadline := time.Now().Add(60 * time.Second)
	for {
		now, loadErr := current(store)
		if loadErr == nil && now.spki != old.spki && !exists(filepath.Join(*stateDir, "previous-identity.json")) {
			break
		}
		select {
		case runErr := <-done:
			return fmt.Errorf("daemon stopped before the switch was confirmed: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			stop()
			<-done
			return errors.New("timed out waiting for the renewed certificate to be accepted")
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	if runErr := <-done; runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("daemon run: %w", runErr)
	}
	now, err := current(store)
	if err != nil {
		return err
	}
	res.CurrentSPKI = now.spki
	if res.CurrentFingerprint, err = fingerprintOf(now.certPEM); err != nil {
		return err
	}
	res.PreviousRemoved = !exists(filepath.Join(*stateDir, "previous-identity.json"))
	res.PendingRemoved = !exists(filepath.Join(*stateDir, "pending-identity.json"))
	if res.OldUpgradeUnauthorized, err = oldUpgradeUnauthorized(ctx, *gateway, runnerID, old); err != nil {
		return err
	}
	if res.OldRotateStatus, err = oldRotateStatus(ctx, base, runnerID, old); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(res)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
