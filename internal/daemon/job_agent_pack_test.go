package daemon

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/config"
)

func authenticatedArchiveFixture(t *testing.T) ([]byte, v1.AgentPackRef) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# workflow"), 0o600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if _, err := agentpack.PackDir(source, "2.1.0", "codex", &archive, key); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive.Bytes())
	return archive.Bytes(), v1.AgentPackRef{
		Version: "2.1.0", Digest: hex.EncodeToString(sum[:]), KeyID: "pack-key-1",
	}
}

func TestMaterializeAgentPackUsesVerifiedDigestCacheAndReadOnlyJobViews(t *testing.T) {
	archive, reference := authenticatedArchiveFixture(t)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	reference.URL = server.URL + "/pack.tgz"
	stateDir := t.TempDir()
	daemon := &Daemon{
		cfg: config.Config{
			StateDir: stateDir, Provider: "codex", ControlPlaneHTTPURL: server.URL,
			AllowedArtifactOrigins: []string{server.URL},
		},
		controlHTTP: server.Client(),
	}
	runner := &jobRunner{d: daemon, ctx: context.Background()}
	for _, name := range []string{"first", "second"} {
		destination := filepath.Join(t.TempDir(), name)
		writableCleanupJobView(t, destination)
		if err := runner.materializeAgentPack(&reference, destination); err != nil {
			t.Fatalf("materialize %s: %v", name, err)
		}
		contents, err := os.ReadFile(filepath.Join(destination, "SKILL.md"))
		if err != nil || string(contents) != "# workflow" {
			t.Fatalf("job view contents=%q err=%v", contents, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want one cached fetch", requests.Load())
	}
}

func TestMaterializeAgentPackRejectsRedirectEncodingAndDigestMismatch(t *testing.T) {
	archive, reference := authenticatedArchiveFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/redirect":
			http.Redirect(writer, request, "/pack", http.StatusFound)
		case "/encoded":
			writer.Header().Set("Content-Encoding", "gzip")
			_, _ = writer.Write(archive)
		default:
			_, _ = writer.Write(archive)
		}
	}))
	defer server.Close()
	runner := &jobRunner{
		d: &Daemon{
			cfg:         config.Config{StateDir: t.TempDir(), Provider: "codex", ControlPlaneHTTPURL: server.URL, AllowedArtifactOrigins: []string{server.URL}},
			controlHTTP: server.Client(),
		},
		ctx: context.Background(),
	}
	for name, mutate := range map[string]func(*v1.AgentPackRef){
		"redirect":         func(ref *v1.AgentPackRef) { ref.URL = server.URL + "/redirect" },
		"content encoding": func(ref *v1.AgentPackRef) { ref.URL = server.URL + "/encoded" },
		"digest mismatch": func(ref *v1.AgentPackRef) {
			ref.URL = server.URL + "/pack"
			prefix := "a"
			if ref.Digest[0] == 'a' {
				prefix = "b"
			}
			ref.Digest = prefix + ref.Digest[1:]
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := reference
			mutate(&candidate)
			destination := filepath.Join(t.TempDir(), "job-view")
			writableCleanupJobView(t, destination)
			if err := runner.materializeAgentPack(&candidate, destination); err == nil {
				t.Fatal("unsafe artifact accepted")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("failed materialization created job view: %v", err)
			}
		})
	}
}

func writableCleanupJobView(t *testing.T, destination string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.Walk(destination, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
}
