package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/config"
)

func authenticatedArchiveFixture(t *testing.T) ([]byte, v1.AgentPackRef) {
	t.Helper()
	var archive bytes.Buffer
	content := agentpack.ArchiveContent{
		PackKey: "workflow-pack", Provider: "codex", Version: "2.1.0",
		Files: map[string][]byte{"SKILL.md": []byte("# workflow")},
	}
	if _, err := agentpack.WriteCanonicalArchive(&archive, content); err != nil {
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

func TestMaterializeAgentPackClassifiesControlledFailureEvidence(t *testing.T) {
	archive, reference := authenticatedArchiveFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/redirect":
			http.Redirect(writer, request, "/pack", http.StatusFound)
		case "/encoded":
			writer.Header().Set("Content-Encoding", "gzip")
			_, _ = writer.Write(archive)
		case "/unavailable":
			http.Error(writer, "unavailable", http.StatusServiceUnavailable)
		case "/invalid":
			_, _ = writer.Write([]byte("not a gzip archive"))
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
	for name, test := range map[string]struct {
		mutate    func(*v1.AgentPackRef)
		category  v1.AgentPackMaterializationFailureCategory
		retryable bool
	}{
		"redirect": {
			mutate:   func(ref *v1.AgentPackRef) { ref.URL = server.URL + "/redirect" },
			category: v1.AgentPackFailureArchiveInvalid,
		},
		"content encoding": {
			mutate:   func(ref *v1.AgentPackRef) { ref.URL = server.URL + "/encoded" },
			category: v1.AgentPackFailureArchiveInvalid,
		},
		"download unavailable": {
			mutate:   func(ref *v1.AgentPackRef) { ref.URL = server.URL + "/unavailable" },
			category: v1.AgentPackFailureDownloadUnavailable, retryable: true,
		},
		"digest mismatch": {
			mutate:   func(ref *v1.AgentPackRef) { ref.URL = server.URL + "/invalid" },
			category: v1.AgentPackFailureDigestMismatch,
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := reference
			test.mutate(&candidate)
			destination := filepath.Join(t.TempDir(), "job-view")
			writableCleanupJobView(t, destination)
			err := runner.materializeAgentPack(&candidate, destination)
			if err == nil {
				t.Fatal("unsafe artifact accepted")
			}
			category, retryable, ok := agentPackMaterializationEvidence(err)
			if !ok || category != test.category || retryable != test.retryable {
				t.Fatalf("classification = %q %v %v, want %q %v", category, retryable, ok, test.category, test.retryable)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("failed materialization created job view: %v", err)
			}
		})
	}
}

func TestMaterializeAgentPackRefusesFormatOneArchiveBeforeCreatingJobView(t *testing.T) {
	for _, name := range []string{"unsupported_format_v1_layout.tgz", "unsupported_format_v1_signature.tgz"} {
		t.Run(name, func(t *testing.T) {
			archive, err := os.ReadFile(filepath.Join("..", "agentpack", "testdata", "conformance", name))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write(archive)
			}))
			defer server.Close()
			sum := sha256.Sum256(archive)
			reference := v1.AgentPackRef{Version: "1.0.0", Digest: hex.EncodeToString(sum[:]), KeyID: "pack-key-1", URL: server.URL + "/pack.tgz"}
			runner := &jobRunner{
				d: &Daemon{
					cfg:         config.Config{StateDir: t.TempDir(), Provider: "codex", ControlPlaneHTTPURL: server.URL, AllowedArtifactOrigins: []string{server.URL}},
					controlHTTP: server.Client(),
				},
				ctx: context.Background(),
			}
			destination := filepath.Join(t.TempDir(), "job-view")
			writableCleanupJobView(t, destination)
			err = runner.materializeAgentPack(&reference, destination)
			category, retryable, ok := agentPackMaterializationEvidence(err)
			if !ok || category != v1.AgentPackFailureArchiveInvalid || retryable {
				t.Fatalf("classification = %q %v %v: %v", category, retryable, ok, err)
			}
			if reason, ok := agentpack.ArchiveRejectionReasonOf(err); !ok || reason != agentpack.RejectUnsupportedFormat {
				t.Fatalf("reason = %q %v: %v", reason, ok, err)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("format 1 archive created a job view: %v", err)
			}
		})
	}
}

func TestMaterializeAgentPackRefusesCachedArchiveThatDiffersFromTheSignedDigest(t *testing.T) {
	archive, reference := authenticatedArchiveFixture(t)
	var substitute bytes.Buffer
	if _, err := agentpack.WriteCanonicalArchive(&substitute, agentpack.ArchiveContent{
		PackKey: "workflow-pack", Provider: "codex", Version: reference.Version,
		Files: map[string][]byte{"SKILL.md": []byte("# substituted")},
	}); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	cacheRoot := filepath.Join(stateDir, "agent-pack-cache", "archives")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(archive)
		writer.(http.Flusher).Flush()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			temporaries, _ := filepath.Glob(filepath.Join(cacheRoot, "."+reference.Digest+"-*.tmp"))
			if len(temporaries) == 1 {
				if info, err := os.Stat(temporaries[0]); err == nil && info.Size() == int64(len(archive)) {
					if err := os.WriteFile(temporaries[0], substitute.Bytes(), 0o600); err != nil {
						t.Errorf("substitute cached archive: %v", err)
					}
					return
				}
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("cache temporary never received the archive")
	}))
	defer server.Close()
	reference.URL = server.URL + "/pack.tgz"
	runner := &jobRunner{
		d: &Daemon{
			cfg:         config.Config{StateDir: stateDir, Provider: "codex", ControlPlaneHTTPURL: server.URL, AllowedArtifactOrigins: []string{server.URL}},
			controlHTTP: server.Client(),
		},
		ctx: context.Background(),
	}
	destination := filepath.Join(t.TempDir(), "job-view")
	writableCleanupJobView(t, destination)
	err := runner.materializeAgentPack(&reference, destination)
	cached, readErr := os.ReadFile(filepath.Join(cacheRoot, reference.Digest+".tgz"))
	if readErr != nil || !bytes.Equal(cached, substitute.Bytes()) {
		t.Fatalf("cache does not hold the substituted archive: %v", readErr)
	}
	category, retryable, ok := agentPackMaterializationEvidence(err)
	if !ok || category != v1.AgentPackFailureDigestMismatch || retryable {
		t.Fatalf("classification = %q %v %v: %v", category, retryable, ok, err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("archive that differs from the signed digest created a job view: %v", err)
	}
}

func TestMaterializeAgentPackClassifiesLocalCacheFailureAsRetryableStorage(t *testing.T) {
	archive, reference := authenticatedArchiveFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	reference.URL = server.URL + "/pack"
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "agent-pack-cache"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &jobRunner{
		d: &Daemon{
			cfg:         config.Config{StateDir: stateDir, Provider: "codex", ControlPlaneHTTPURL: server.URL, AllowedArtifactOrigins: []string{server.URL}},
			controlHTTP: server.Client(),
		},
		ctx: context.Background(),
	}
	err := runner.materializeAgentPack(&reference, filepath.Join(t.TempDir(), "job-view"))
	category, retryable, ok := agentPackMaterializationEvidence(err)
	if !ok || category != v1.AgentPackFailureStorageUnavailable || !retryable {
		t.Fatalf("classification = %q %v %v: %v", category, retryable, ok, err)
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
