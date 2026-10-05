package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/remoteconfig"
	"golang.org/x/sys/unix"
)

type countingTransport struct{ requests atomic.Int32 }

func (transport *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.requests.Add(1)
	return nil, errors.New("no network in this test")
}

func cacheTestArchive(t *testing.T, stateDir string, body []byte) (string, string) {
	t.Helper()
	cache := agentpack.ArchiveCache{Root: filepath.Join(stateDir, "agent-pack-cache", "archives"), MaxArchiveBytes: agentpack.DefaultMaxArchiveBytes}
	if body == nil {
		body, _ = authenticatedArchiveFixture(t)
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	path, err := cache.Acquire(context.Background(), digest, func(_ context.Context, writer io.Writer) error {
		_, err := writer.Write(body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return digest, path
}

func assertEvicted(t *testing.T, archivePath string) {
	t.Helper()
	for _, path := range []string{archivePath, strings.TrimSuffix(archivePath, ".tgz") + ".lock"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s remains after eviction: %v", filepath.Base(path), err)
		}
	}
}

func TestStartJobRefusesARevokedAgentPackBeforeDownloading(t *testing.T) {
	job, trustedKey := signedTestPackJob(t)
	transport := &countingTransport{}
	newDaemon := func(revoked []string) *Daemon {
		stateDir := t.TempDir()
		return &Daemon{
			cfg: config.Config{
				StateDir: stateDir, Provider: "codex", ControlPlaneHTTPURL: "https://packs.example.test",
				AgentPackPublicKeys:     map[string]string{"pack-key-1": trustedKey},
				AllowedArtifactOrigins:  []string{"https://packs.example.test"},
				RevokedAgentPackDigests: revoked,
			},
			controlHTTP: &http.Client{Transport: transport},
			leases:      lease.NewManager(),
			logger:      slog.Default(),
		}
	}
	unrevoked := newDaemon([]string{strings.Repeat("0", 64)})
	if reason := commandRejectionReason(unrevoked.startJob(context.Background(), job)); reason == v1.CommandAckReasonAgentPackAuthorityInvalid {
		t.Fatalf("an unrevoked digest was refused as %q", reason)
	}

	revoked := newDaemon([]string{strings.Repeat("0", 64), job.AgentPack.Digest})
	err := revoked.startJob(context.Background(), job)
	if reason := commandRejectionReason(err); reason != v1.CommandAckReasonAgentPackAuthorityInvalid {
		t.Fatalf("rejection reason = %q (%v), want %q", reason, err, v1.CommandAckReasonAgentPackAuthorityInvalid)
	}
	if transport.requests.Load() != 0 {
		t.Fatalf("a revoked Agent Pack was downloaded: %d requests", transport.requests.Load())
	}
	if _, err := os.Lstat(filepath.Join(revoked.cfg.StateDir, "agent-pack-cache")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the Agent Pack cache was touched: %v", err)
	}
	if revoked.activeJob != nil {
		t.Fatal("a job with a revoked Agent Pack became active")
	}

	revoked.cfg.AgentPackPublicKeys = nil
	if reason := commandRejectionReason(revoked.verifyJobAgentPackAuthority(job)); reason != v1.CommandAckReasonAgentPackAuthorityInvalid {
		t.Fatalf("revocation did not take precedence over the key check: %q", reason)
	}
}

func TestReloadEvictsARevokedAgentPackArchiveWithoutARestart(t *testing.T) {
	d, _, logs := reloadTestDaemon(t)
	digest, archivePath := cacheTestArchive(t, d.cfg.StateDir, nil)
	keptDigest, keptPath := cacheTestArchive(t, d.cfg.StateDir, []byte("kept archive"))
	newDigest := strings.Repeat("e", 64)
	reloader := &fakeConfigurationReloader{reloads: []remoteconfig.Reload{hotReload(d.cfg, newDigest, func(next *config.Config) {
		next.RevokedAgentPackDigests = []string{digest}
	})}, errs: []error{nil}}
	d.configurationReloader = reloader
	if !d.reloadConfiguration(context.Background()) {
		t.Fatal("reload reported a failure")
	}
	if reloader.commits() != 1 || len(reloader.refusals()) != 0 {
		t.Fatalf("commits=%d refusals=%v", reloader.commits(), reloader.refusals())
	}
	if hot := d.hotConfiguration(); !reflect.DeepEqual(hot.revokedAgentPackDigests, []string{digest}) || hot.digest != newDigest {
		t.Fatalf("hot = %+v", hot)
	}
	assertEvicted(t, archivePath)
	if _, err := os.Lstat(keptPath); err != nil {
		t.Fatalf("unrevoked archive %s removed: %v", keptDigest, err)
	}
	if output := logs.String(); !strings.Contains(output, digest) || !strings.Contains(output, "evicted=1") || strings.Contains(output, "restart our-runner") {
		t.Fatalf("logs = %s", output)
	}
	job := v1.JobSpec{AgentPack: &v1.AgentPackRef{KeyID: "key-2", Digest: digest}, AgentPackAuthority: &v1.SignedAgentPackAuthority{}}
	if reason := commandRejectionReason(d.verifyJobAgentPackAuthority(job)); reason != v1.CommandAckReasonAgentPackAuthorityInvalid {
		t.Fatalf("a hot-revoked digest was not refused: %q", reason)
	}
}

func TestStartupEvictsRevokedAgentPackArchives(t *testing.T) {
	d, _, _ := reloadTestDaemon(t)
	digest, archivePath := cacheTestArchive(t, d.cfg.StateDir, nil)
	d.cfg.RevokedAgentPackDigests = []string{digest}
	d.applyAgentPackRevocations()
	assertEvicted(t, archivePath)
}

func TestAnEvictionSkippedForAHeldLockIsRetriedOnTheNextConfigurationCheck(t *testing.T) {
	d, _, _ := reloadTestDaemon(t)
	digest, archivePath := cacheTestArchive(t, d.cfg.StateDir, nil)
	lockPath := strings.TrimSuffix(archivePath, ".tgz") + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	d.cfg.RevokedAgentPackDigests = []string{digest}
	d.applyAgentPackRevocations()
	if _, err := os.Lstat(archivePath); err != nil {
		t.Fatalf("an archive whose lock is held was removed: %v", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	d.configurationReloader = &fakeConfigurationReloader{reloads: []remoteconfig.Reload{{Outcome: remoteconfig.ReloadUnchanged}}, errs: []error{nil}}
	if !d.reloadConfiguration(context.Background()) {
		t.Fatal("reload reported a failure")
	}
	assertEvicted(t, archivePath)
}

func TestARunningJobKeepsItsAgentPackViewWhenItsDigestIsEvicted(t *testing.T) {
	archive, reference := authenticatedArchiveFixture(t)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	reference.URL = server.URL + "/pack.tgz"
	newRunner := func(revoked []string) *jobRunner {
		daemon := &Daemon{
			cfg: config.Config{
				StateDir: t.TempDir(), Provider: "codex", ControlPlaneHTTPURL: server.URL,
				AllowedArtifactOrigins: []string{server.URL}, RevokedAgentPackDigests: revoked,
			},
			controlHTTP: server.Client(),
			logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		return &jobRunner{d: daemon, ctx: context.Background()}
	}
	archivePath := func(runner *jobRunner) string {
		return filepath.Join(runner.d.cfg.StateDir, "agent-pack-cache", "archives", reference.Digest+".tgz")
	}

	running := newRunner(nil)
	destination := filepath.Join(t.TempDir(), "pack")
	writableCleanupJobView(t, destination)
	if err := running.materializeAgentPack(&reference, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(archivePath(running)); err != nil {
		t.Fatalf("archive not cached: %v", err)
	}
	next := running.d.cfg
	next.RevokedAgentPackDigests = []string{reference.Digest}
	if err := running.d.applyHotConfiguration(next, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	running.d.applyAgentPackRevocations()
	assertEvicted(t, archivePath(running))
	if err := verifyAgentPackView(destination, running.packFiles); err != nil {
		t.Fatalf("the running job's pack view changed: %v", err)
	}

	alreadyLeased := newRunner([]string{reference.Digest})
	leasedDestination := filepath.Join(t.TempDir(), "pack")
	writableCleanupJobView(t, leasedDestination)
	if err := alreadyLeased.materializeAgentPack(&reference, leasedDestination); err != nil {
		t.Fatalf("a job leased before the revocation could not materialize its pack: %v", err)
	}
	if err := verifyAgentPackView(leasedDestination, alreadyLeased.packFiles); err != nil {
		t.Fatal(err)
	}
	assertEvicted(t, archivePath(alreadyLeased))
	if requests.Load() != 2 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestARunningJobKeepsUsingItsAgentPackWhenItsKeyLeavesTheTrustedSet(t *testing.T) {
	archive, reference := authenticatedArchiveFixture(t)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	reference.URL = server.URL + "/pack.tgz"
	daemon := &Daemon{
		cfg: config.Config{
			StateDir: t.TempDir(), Provider: "codex", ControlPlaneHTTPURL: server.URL,
			AllowedArtifactOrigins: []string{server.URL},
			AgentPackPublicKeys:    map[string]string{reference.KeyID: "trusted-at-start"},
		},
		controlHTTP: server.Client(),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	configuration := daemon.hotConfiguration()
	running := &jobRunner{d: daemon, ctx: context.Background(), configuration: &configuration}
	destination := filepath.Join(t.TempDir(), "pack")
	writableCleanupJobView(t, destination)
	if err := running.materializeAgentPack(&reference, destination); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(daemon.cfg.StateDir, "agent-pack-cache", "archives", reference.Digest+".tgz")

	next := daemon.cfg
	next.AgentPackPublicKeys = map[string]string{}
	if err := daemon.applyHotConfiguration(next, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	daemon.applyAgentPackRevocations()
	if _, ok := daemon.hotConfiguration().agentPackPublicKeys[reference.KeyID]; ok {
		t.Fatal("the hot configuration still trusts the dropped key")
	}
	if _, err := os.Lstat(archivePath); err != nil {
		t.Fatalf("the archive of an in-flight attempt was evicted when its key left the trusted set: %v", err)
	}
	if err := verifyAgentPackView(destination, running.packFiles); err != nil {
		t.Fatalf("the running job's pack view changed: %v", err)
	}
	if _, ok := running.jobConfiguration().agentPackPublicKeys[reference.KeyID]; !ok {
		t.Fatal("the running job lost the key it started with")
	}

	retried := filepath.Join(t.TempDir(), "pack")
	writableCleanupJobView(t, retried)
	if err := running.materializeAgentPack(&reference, retried); err != nil {
		t.Fatalf("an in-flight attempt could not use its pack after its key left the trusted set: %v", err)
	}
	if err := verifyAgentPackView(retried, running.packFiles); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want the cached archive to be reused", requests.Load())
	}
}
