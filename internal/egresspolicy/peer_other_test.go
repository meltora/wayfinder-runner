//go:build !linux

package egresspolicy

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultRootVerifierAuthenticationFailsClosedOffLinux(t *testing.T) {
	temporary, err := os.MkdirTemp("/tmp", "egress-peer-test-")
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
	accepted := make(chan struct{})
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = connection.Close()
		}
		close(accepted)
	}()
	client := NewClient(socketPath)
	if _, err := client.Attest(context.Background(), strings.Repeat("b", 64), "runner-egress", "policy-v1"); err == nil {
		t.Fatal("non-Linux verifier peer was accepted without kernel credentials")
	}
	<-accepted
}
