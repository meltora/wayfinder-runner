package egresspolicy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEffectivePolicyResponseRequiresFreshLiveDeniedReachabilityProof(t *testing.T) {
	now := time.Date(2030, 8, 25, 12, 0, 0, 0, time.UTC)
	client := &Client{now: func() time.Time { return now }}
	req := request{
		SchemaVersion: 1, Nonce: strings.Repeat("a", 64), NetworkID: strings.Repeat("b", 64),
		PolicyID: "policy-v1", LeaseDurationMS: 5000, RequestedAt: now,
	}
	valid := response{
		SchemaVersion: 1, Nonce: req.Nonce, NetworkID: req.NetworkID, PolicyID: req.PolicyID,
		EnforcementID: "enforcer-1", RulesDigest: strings.Repeat("c", 64), LeaseID: "lease-1", Effective: true,
		UnauthorizedReachabilityBlocked: true, ObservedAt: now, LeaseExpiresAt: now.Add(5 * time.Second),
	}
	binding, err := client.validateResponse(req, valid)
	if err != nil || binding.EnforcementID != "enforcer-1" {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	for name, mutate := range map[string]func(*response){
		"wrong policy":           func(value *response) { value.PolicyID = "other-policy" },
		"wrong network":          func(value *response) { value.NetworkID = strings.Repeat("d", 64) },
		"not effective":          func(value *response) { value.Effective = false },
		"wrong-policy reachable": func(value *response) { value.UnauthorizedReachabilityBlocked = false },
		"cached observation":     func(value *response) { value.ObservedAt = now.Add(-time.Second) },
		"lease too short":        func(value *response) { value.LeaseExpiresAt = now.Add(time.Second) },
		"long lived":             func(value *response) { value.LeaseExpiresAt = now.Add(time.Minute) },
		"bad rules":              func(value *response) { value.RulesDigest = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if _, err := client.validateResponse(req, candidate); err == nil {
				t.Fatal("unsafe effective-policy attestation accepted")
			}
		})
	}
}

func TestClientDefaultsToRootAuthenticatedVerifier(t *testing.T) {
	if client := NewClient(DefaultSocketPath); client.ExpectedServerUID != 0 {
		t.Fatalf("expected verifier UID = %d, want root", client.ExpectedServerUID)
	}
}

func TestClientCurrentTimeFallsBackToSystemClock(t *testing.T) {
	before := time.Now()
	observed := (&Client{}).currentTime()
	if observed.Before(before) || observed.After(time.Now()) {
		t.Fatalf("unexpected current time %s", observed)
	}
}

func TestAcquireRejectsInvalidAuthorityDurationAndSocketPaths(t *testing.T) {
	validNetworkID := strings.Repeat("b", 64)
	for name, testCase := range map[string]struct {
		client    *Client
		networkID string
		duration  time.Duration
	}{
		"invalid authority": {NewClient(DefaultSocketPath), "bad", 5 * time.Second},
		"invalid duration":  {NewClient(DefaultSocketPath), validNetworkID, time.Millisecond},
		"relative socket":   {NewClient("relative.sock"), validNetworkID, 5 * time.Second},
		"missing socket":    {NewClient(filepath.Join(t.TempDir(), "missing.sock")), validNetworkID, 5 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := testCase.client.Acquire(
				context.Background(), testCase.networkID, "runner-egress", "policy-v1", testCase.duration,
			); err == nil {
				t.Fatal("invalid policy lease request accepted")
			}
		})
	}
	regularPath := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(regularPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(regularPath).Acquire(
		context.Background(), validNetworkID, "runner-egress", "policy-v1", 5*time.Second,
	); err == nil {
		t.Fatal("regular file accepted as policy verifier socket")
	}
}

func TestClientRequiresFreshNonceBoundEffectivePolicyFromVerifier(t *testing.T) {
	temporary, err := os.MkdirTemp("/tmp", "egress-policy-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	socketPath := filepath.Join(temporary, "policy.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer connection.Close()
		var req request
		if err := decodeMessage(bufio.NewReaderSize(connection, MaxMessageBytes+2), &req); err != nil {
			done <- err
			return
		}
		now := time.Now().UTC()
		if err := encodeMessage(connection, response{
			SchemaVersion: SchemaVersion, Nonce: req.Nonce, NetworkID: req.NetworkID, PolicyID: req.PolicyID,
			EnforcementID: "enforcer-1", RulesDigest: strings.Repeat("c", 64), LeaseID: "lease-1", Effective: true,
			UnauthorizedReachabilityBlocked: true, ObservedAt: now,
			LeaseExpiresAt: now.Add(time.Duration(req.LeaseDurationMS) * time.Millisecond),
		}); err != nil {
			done <- err
			return
		}
		var probe [1]byte
		_, err = connection.Read(probe[:])
		if err != io.EOF {
			done <- err
			return
		}
		done <- nil
	}()
	client := NewClient(socketPath)
	client.ExpectedServerUID = -1
	binding, err := client.Attest(context.Background(), strings.Repeat("b", 64), "runner-egress", "policy-v1")
	if err != nil || binding.EnforcementID != "enforcer-1" {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEffectivePolicyIPCRejectsDuplicateOrUnknownFields(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate":  "{\"schema_version\":1,\"schema_version\":1}\n",
		"unknown":    "{\"unknown\":true}\n",
		"malformed":  "{\n",
		"not object": "[]\n",
		"trailing":   "{}{}\n",
	} {
		t.Run(name, func(t *testing.T) {
			var destination response
			if err := decodeMessage(bufio.NewReaderSize(bytes.NewBufferString(raw), MaxMessageBytes+2), &destination); err == nil {
				t.Fatal("hostile IPC response accepted")
			}
		})
	}
}

func TestHeldPolicyLeaseFailsWhenVerifierConnectionEnds(t *testing.T) {
	temporary, err := os.MkdirTemp("/tmp", "egress-policy-lease-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	socketPath := filepath.Join(temporary, "policy.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		var req request
		if decodeMessage(bufio.NewReaderSize(connection, MaxMessageBytes+2), &req) != nil {
			return
		}
		now := time.Now().UTC()
		_ = encodeMessage(connection, response{
			SchemaVersion: SchemaVersion, Nonce: req.Nonce, NetworkID: req.NetworkID, PolicyID: req.PolicyID,
			EnforcementID: "enforcer-1", RulesDigest: strings.Repeat("c", 64), LeaseID: "lease-1", Effective: true,
			UnauthorizedReachabilityBlocked: true, ObservedAt: now,
			LeaseExpiresAt: now.Add(time.Duration(req.LeaseDurationMS) * time.Millisecond),
		})
	}()
	client := NewClient(socketPath)
	client.ExpectedServerUID = -1
	lease, err := client.Acquire(
		context.Background(), strings.Repeat("b", 64), "runner-egress", "policy-v1", 5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-lease.Failures():
		if err == nil {
			t.Fatal("lease ended without a failure")
		}
	case <-time.After(time.Second):
		t.Fatal("verifier disconnect did not revoke policy lease")
	}
}

func TestPolicyLeaseRequiresHeldThroughReleaseAcknowledgement(t *testing.T) {
	temporary, err := os.MkdirTemp("/tmp", "egress-policy-release-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	socketPath := filepath.Join(temporary, "policy.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		defer connection.Close()
		reader := bufio.NewReaderSize(connection, MaxMessageBytes+2)
		var req request
		if err := decodeMessage(reader, &req); err != nil {
			done <- err
			return
		}
		now := time.Now().UTC()
		if err := encodeMessage(connection, response{
			SchemaVersion: SchemaVersion, Nonce: req.Nonce, NetworkID: req.NetworkID, PolicyID: req.PolicyID,
			EnforcementID: "enforcer-1", RulesDigest: strings.Repeat("c", 64), LeaseID: "lease-1", Effective: true,
			UnauthorizedReachabilityBlocked: true, ObservedAt: now,
			LeaseExpiresAt: now.Add(time.Duration(req.LeaseDurationMS) * time.Millisecond),
		}); err != nil {
			done <- err
			return
		}
		var release releaseRequest
		if err := decodeMessage(reader, &release); err != nil {
			done <- err
			return
		}
		done <- encodeMessage(connection, releaseResponse{
			SchemaVersion: SchemaVersion, Kind: "held_through_release", Nonce: release.Nonce,
			LeaseID: release.LeaseID, ReleasedAt: time.Now().UTC(),
		})
	}()
	client := NewClient(socketPath)
	client.ExpectedServerUID = -1
	lease, err := client.Acquire(
		context.Background(), strings.Repeat("b", 64), "runner-egress", "policy-v1", 5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	completeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lease.Complete(completeCtx); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPolicyLeaseRejectsInvalidReleaseAcknowledgement(t *testing.T) {
	temporary, err := os.MkdirTemp("/tmp", "egress-policy-bad-release-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	socketPath := filepath.Join(temporary, "policy.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReaderSize(connection, MaxMessageBytes+2)
		var req request
		if decodeMessage(reader, &req) != nil {
			return
		}
		now := time.Now().UTC()
		if encodeMessage(connection, response{
			SchemaVersion: SchemaVersion, Nonce: req.Nonce, NetworkID: req.NetworkID, PolicyID: req.PolicyID,
			EnforcementID: "enforcer-1", RulesDigest: strings.Repeat("c", 64), LeaseID: "lease-1", Effective: true,
			UnauthorizedReachabilityBlocked: true, ObservedAt: now,
			LeaseExpiresAt: now.Add(time.Duration(req.LeaseDurationMS) * time.Millisecond),
		}) != nil {
			return
		}
		var release releaseRequest
		if decodeMessage(reader, &release) != nil {
			return
		}
		_ = encodeMessage(connection, releaseResponse{
			SchemaVersion: SchemaVersion, Kind: "not-held", Nonce: release.Nonce,
			LeaseID: release.LeaseID, ReleasedAt: time.Now().UTC(),
		})
	}()
	client := NewClient(socketPath)
	client.ExpectedServerUID = -1
	lease, err := client.Acquire(
		context.Background(), strings.Repeat("b", 64), "runner-egress", "policy-v1", 5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	completeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lease.Complete(completeCtx); err == nil {
		t.Fatal("invalid held-through-release acknowledgement accepted")
	}
	_ = lease.Close()
}

func TestAcquireRejectsUnknownVerifierResponseFields(t *testing.T) {
	temporary, err := os.MkdirTemp("/tmp", "egress-policy-hostile-response-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	socketPath := filepath.Join(temporary, "policy.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		var req request
		if decodeMessage(bufio.NewReaderSize(connection, MaxMessageBytes+2), &req) != nil {
			return
		}
		_, _ = connection.Write([]byte("{\"unknown\":true}\n"))
	}()
	client := NewClient(socketPath)
	client.ExpectedServerUID = -1
	if _, err := client.Acquire(
		context.Background(), strings.Repeat("b", 64), "runner-egress", "policy-v1", 5*time.Second,
	); err == nil {
		t.Fatal("unknown verifier response field accepted")
	}
}
