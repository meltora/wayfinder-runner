//go:build linux

package egresspolicy

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientAuthenticatesVerifierWithKernelPeerCredentials(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "policy.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go serveOnePolicyLease(t, listener)
	client := NewClient(socketPath)
	client.ExpectedServerUID = os.Getuid()
	if _, err := client.Attest(context.Background(), strings.Repeat("b", 64), "runner-egress", "policy-v1"); err != nil {
		t.Fatalf("kernel-authenticated verifier rejected: %v", err)
	}

	secondSocket := filepath.Join(t.TempDir(), "policy.sock")
	secondListener, err := net.Listen("unix", secondSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer secondListener.Close()
	go serveOnePolicyLease(t, secondListener)
	client = NewClient(secondSocket)
	client.ExpectedServerUID = os.Getuid() + 1
	if _, err := client.Attest(context.Background(), strings.Repeat("b", 64), "runner-egress", "policy-v1"); err == nil {
		t.Fatal("verifier with wrong kernel peer UID was accepted")
	}
}

func serveOnePolicyLease(t *testing.T, listener net.Listener) {
	t.Helper()
	connection, err := listener.Accept()
	if err != nil {
		return
	}
	defer connection.Close()
	var req request
	if err := decodeMessage(bufio.NewReaderSize(connection, MaxMessageBytes+2), &req); err != nil {
		return
	}
	now := time.Now().UTC()
	_ = encodeMessage(connection, response{
		SchemaVersion: SchemaVersion, Nonce: req.Nonce, NetworkID: req.NetworkID, PolicyID: req.PolicyID,
		EnforcementID: "enforcer-1", RulesDigest: strings.Repeat("c", 64), LeaseID: "lease-1", Effective: true,
		UnauthorizedReachabilityBlocked: true, ObservedAt: now,
		LeaseExpiresAt: now.Add(time.Duration(req.LeaseDurationMS) * time.Millisecond),
	})
}
