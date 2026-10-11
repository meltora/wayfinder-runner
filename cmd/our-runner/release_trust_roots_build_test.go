package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/agentpack/trustroot"
)

func versionTestRoot(t *testing.T) (string, string) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), base64.StdEncoding.EncodeToString(der)
}

func TestReleaseBuildsReportTheirCompiledTrustRoots(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the runner once per release channel")
	}
	directory := t.TempDir()
	for _, channel := range trustroot.Channels() {
		embedded, err := trustroot.Embedded(channel)
		if err != nil {
			t.Fatal(err)
		}
		want := "platform trust roots: none"
		if ids := embedded.KeyIDs(); len(ids) != 0 {
			want = "platform trust roots: " + strings.Join(ids, ", ")
		}
		stamp := channel
		if channel == trustroot.ChannelDevelopment {
			stamp = ""
		}
		binary := filepath.Join(directory, "our-runner-"+channel)
		build := exec.Command("go", "build", "-o", binary, "-ldflags", "-X github.com/example/app-runner/internal/daemon.Channel="+stamp, ".")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", channel, err, output)
		}
		version := exec.Command(binary, "version")
		version.Env = append(os.Environ(), trustroot.OverrideEnvironment+"=")
		output, err := version.CombinedOutput()
		if err != nil {
			t.Fatalf("version %s: %v\n%s", channel, err, output)
		}
		if !strings.Contains(string(output), want+"\n") {
			t.Fatalf("%s build reports:\n%s\nwant %q", channel, output, want)
		}
	}
}
