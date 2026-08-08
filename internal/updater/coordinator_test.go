package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/example/app-runner/internal/updateipc"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeCatalog struct {
	release Release
	err     error
}
type countingCatalog struct {
	calls int
	err   error
}

func (c *countingCatalog) Latest(context.Context, CatalogQuery) (Release, error) {
	c.calls++
	return Release{}, c.err
}

func (f fakeCatalog) Latest(context.Context, CatalogQuery) (Release, error) { return f.release, f.err }

type fakeArtifacts struct {
	body []byte
	err  error
}

func (f fakeArtifacts) Download(_ context.Context, _ string, size int64, digest string, dst io.Writer) error {
	if f.err != nil {
		return f.err
	}
	if int64(len(f.body)) != size {
		return errors.New("size mismatch")
	}
	sum := sha256.Sum256(f.body)
	if hex.EncodeToString(sum[:]) != digest {
		return errors.New("digest mismatch")
	}
	_, err := dst.Write(f.body)
	return err
}

type fakeService struct {
	mu       sync.Mutex
	calls    []string
	startErr error
	stopErr  error
}

func (s *fakeService) Stop(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "stop")
	return s.stopErr
}
func (s *fakeService) Start(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "start")
	return s.startErr
}
func (s *fakeService) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}
func (s *fakeService) MainPID(context.Context, string) (int, error) { return 42, nil }

type fakePreflight struct{ err error }

func (f fakePreflight) Check(context.Context, string) error { return f.err }

type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time { return f.now }

type fakeDisk uint64

func (f fakeDisk) FreeBytes(string) (uint64, error) { return uint64(f), nil }

func TestPrepareActivateHealthAndSequencePersistence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("runner"), "our-runner-updater": []byte("updater")})
	release := releaseFor(archive, 8, "1.2.3")
	service := &fakeService{}
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	c := newTestCoordinator(t, dir, fakeCatalog{release: release}, fakeArtifacts{body: archive}, service, clock)

	prepared, err := c.Prepare(context.Background(), release.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Phase != PhasePrepared || prepared.UpdateID != release.ReleaseID {
		t.Fatalf("prepared = %+v", prepared)
	}
	for _, name := range requiredBinaries {
		if info, err := os.Stat(filepath.Join(dir, "versions", fmt.Sprintf("%020d-%s", release.Sequence, release.Version), name)); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "versions", fmt.Sprintf("%020d-%s", release.Sequence, release.Version))); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("slot mode=%v err=%v", info, err)
	}

	activated, err := c.Activate(context.Background(), release.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	if activated.Phase != PhaseAwaitingHealth {
		t.Fatalf("activated = %+v", activated)
	}
	if got := mustReadlink(t, filepath.Join(dir, "current")); got != filepath.Join(dir, "versions", fmt.Sprintf("%020d-%s", release.Sequence, release.Version)) {
		t.Fatalf("current = %q", got)
	}
	if err := c.Health(context.Background(), release.ReleaseID, release.Version); err != nil {
		t.Fatal(err)
	}
	status, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != PhaseIdle || status.Sequence != 8 || status.Version != "1.2.3" {
		t.Fatalf("status = %+v", status)
	}
	if got := mustReadlink(t, filepath.Join(dir, "updater-current")); got != filepath.Join(dir, "versions", fmt.Sprintf("%020d-%s", release.Sequence, release.Version), "our-runner-updater") {
		t.Fatalf("updater current=%q", got)
	}
	calls := service.Calls()
	if len(calls) != 2 || calls[0] != "stop" || calls[1] != "start" {
		t.Fatalf("service calls = %v", calls)
	}

	c2 := newTestCoordinator(t, dir, fakeCatalog{release: release}, fakeArtifacts{body: archive}, service, clock)
	if _, err := c2.Prepare(context.Background(), release.ReleaseID); !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("replay error = %v", err)
	}
}

func TestNewRejectsUnsafeConfigurationAndMissingDependencies(t *testing.T) {
	t.Parallel()
	base := Config{Channel: "stable", VersionsDir: "/tmp/versions", StateDir: "/tmp/state", CurrentLink: "/tmp/current", PreviousLink: "/tmp/previous", UpdaterCurrentLink: "/tmp/updater", RunnerService: "runner.service"}
	deps := Dependencies{Catalog: fakeCatalog{}, Artifacts: fakeArtifacts{}, Service: &fakeService{}, Preflight: fakePreflight{}}
	cases := []Config{func() Config { c := base; c.Channel = "bad/channel"; return c }(), func() Config { c := base; c.VersionsDir = "relative"; return c }(), func() Config { c := base; c.RunnerService = "bad/service"; return c }(), func() Config { c := base; c.RunnerVersion = "v1.0.0"; return c }()}
	for _, cfg := range cases {
		if _, err := New(cfg, deps); err == nil {
			t.Fatalf("unsafe config accepted: %+v", cfg)
		}
	}
	if _, err := New(base, Dependencies{}); err == nil {
		t.Fatal("missing dependencies accepted")
	}
}

func TestPreparePropagatesDownloadAndPreflightFailures(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		a ArtifactFetcher
		p Preflighter
	}{"download": {fakeArtifacts{err: errors.New("network")}, fakePreflight{}}, "preflight": {fakeArtifacts{}, fakePreflight{err: errors.New("bad candidate")}}}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
			rel := releaseFor(archive, 1, "1.0.0")
			if name == "preflight" {
				tc.a = fakeArtifacts{body: archive}
			}
			cfg := Config{Channel: "stable", OS: "linux", Arch: "amd64", VersionsDir: filepath.Join(dir, "versions"), StateDir: filepath.Join(dir, "state"), CurrentLink: filepath.Join(dir, "current"), PreviousLink: filepath.Join(dir, "previous"), UpdaterCurrentLink: filepath.Join(dir, "updater"), RunnerService: "runner.service", MaxArchiveBytes: 1 << 20, MaxFileBytes: 1 << 18}
			c, err := New(cfg, Dependencies{Catalog: fakeCatalog{release: rel}, Artifacts: tc.a, Service: &fakeService{}, Preflight: tc.p, Clock: &fakeClock{now: time.Now()}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Prepare(context.Background(), rel.ReleaseID); err == nil {
				t.Fatal("failure ignored")
			}
			state, err := c.loadState()
			if err != nil || state.Phase != PhaseIdle || state.ConsecutiveFailures != 1 {
				t.Fatalf("state=%+v err=%v", state, err)
			}
		})
	}
}

func TestPrepareRejectsUnsafeArchivesAndEquivocation(t *testing.T) {
	t.Parallel()
	for name, files := range map[string]map[string][]byte{
		"missing updater": {"our-runner": []byte("x")},
		"extra file":      {"our-runner": []byte("x"), "our-runner-updater": []byte("y"), "oops": []byte("z")},
		"traversal":       {"../our-runner": []byte("x"), "our-runner-updater": []byte("y")},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			archive := makeArchive(t, files)
			rel := releaseFor(archive, 1, "2.0.0")
			c := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, &fakeClock{now: time.Now()})
			if _, err := c.Prepare(context.Background(), rel.ReleaseID); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}

	dir := t.TempDir()
	first := makeArchive(t, map[string][]byte{"our-runner": []byte("a"), "our-runner-updater": []byte("b")})
	rel := releaseFor(first, 9, "9.0.0")
	clock := &fakeClock{now: time.Now()}
	c := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: first}, &fakeService{}, clock)
	if _, err := c.Prepare(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	state, _ := c.loadState()
	state.Phase = PhaseIdle
	state.clearPending()
	state.Sequence = 9
	state.PayloadDigest = rel.PayloadDigest
	state.ReleaseID = rel.ReleaseID
	if err := c.saveState(state); err != nil {
		t.Fatal(err)
	}
	second := makeArchive(t, map[string][]byte{"our-runner": []byte("c"), "our-runner-updater": []byte("d")})
	evil := releaseFor(second, 9, "9.0.1")
	c.catalog = fakeCatalog{release: evil}
	c.artifacts = fakeArtifacts{body: second}
	clock.now = clock.now.Add(2 * time.Minute)
	if _, err := c.Prepare(context.Background(), evil.ReleaseID); !errors.Is(err, ErrEquivocation) {
		t.Fatalf("equivocation error = %v", err)
	}
}

func TestActivationFailureAndExpiredHealthRollback(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("/tmp", "updater-rollback-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	old := filepath.Join(dir, "versions", "1.0.0")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredBinaries {
		if err := os.WriteFile(filepath.Join(old, name), []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(old, filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("new"), "our-runner-updater": []byte("new")})
	rel := releaseFor(archive, 2, "2.0.0")
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	service := &fakeService{}
	c := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, service, clock)
	if _, err := c.Prepare(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	_, err = c.Activate(context.Background(), rel.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if err := c.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := mustReadlink(t, filepath.Join(dir, "current")); got != old {
		t.Fatalf("rollback current = %q", got)
	}
	if err := c.Health(context.Background(), rel.ReleaseID, rel.Version); err == nil {
		t.Fatal("late health accepted")
	}
	socket := filepath.Join(dir, "control.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Coordinator: c, Peers: fixedPeer{peer: Peer{UID: 1, PID: 2}}, Authorizer: &allowPeer{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, ln) }()
	client := updateipc.NewClient(socket)
	rolledBack, err := client.Status(ctx, "rollback-status", rel.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	if !rolledBack.OK || rolledBack.Phase != "rolled_back" {
		t.Fatalf("rollback status=%+v", rolledBack)
	}
	cancel()
	<-done
	if _, err := c.Prepare(context.Background(), rel.ReleaseID); !errors.Is(err, ErrSuppressed) {
		t.Fatalf("rolled back release not suppressed: %v", err)
	}
	newArchive := makeArchive(t, map[string][]byte{"our-runner": []byte("newer"), "our-runner-updater": []byte("newer")})
	newRelease := releaseFor(newArchive, 3, "3.0.0")
	c.catalog = fakeCatalog{release: newRelease}
	c.artifacts = fakeArtifacts{body: newArchive}
	if _, err := c.Prepare(context.Background(), newRelease.ReleaseID); err != nil {
		t.Fatalf("newer release suppressed: %v", err)
	}
}

func TestHealthRequiresExactTransactionAndVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
	rel := releaseFor(archive, 3, "3.0.0")
	c := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, &fakeClock{now: time.Now()})
	if _, err := c.Prepare(context.Background(), rel.ReleaseID); err != nil {
		t.Fatal(err)
	}
	a, err := c.Activate(context.Background(), rel.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	_ = a
	for _, tc := range []struct{ id, version string }{{"wrong", rel.Version}, {rel.ReleaseID, "0.0.0"}} {
		if err := c.Health(context.Background(), tc.id, tc.version); err == nil {
			t.Fatalf("accepted bad health %+v", tc)
		}
	}
}

func TestActivationFailpointsRestorePreviousService(t *testing.T) {
	for _, point := range []string{"after_stop", "after_switch"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			old := filepath.Join(dir, "versions", "00000000000000000000-0.1.0")
			if err := os.MkdirAll(old, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range requiredBinaries {
				if err := os.WriteFile(filepath.Join(old, name), []byte("old"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(old, filepath.Join(dir, "current")); err != nil {
				t.Fatal(err)
			}
			archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
			rel := releaseFor(archive, 4, "4.0.0")
			svc := &fakeService{}
			c := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, svc, &fakeClock{now: time.Now()})
			if _, err := c.Prepare(context.Background(), rel.ReleaseID); err != nil {
				t.Fatal(err)
			}
			c.failpoint = func(got string) error {
				if got == point {
					return errors.New("crash")
				}
				return nil
			}
			if _, err := c.Activate(context.Background(), rel.ReleaseID); err == nil {
				t.Fatal("failpoint ignored")
			}
			if got := mustReadlink(t, filepath.Join(dir, "current")); got != old {
				t.Fatalf("current=%q", got)
			}
			calls := svc.Calls()
			if len(calls) < 2 || calls[len(calls)-1] != "start" {
				t.Fatalf("service not restarted: %v", calls)
			}
		})
	}
}

func TestPrepareFailsBeforeStagingOnLowDiskAndBoundsTotalExtraction(t *testing.T) {
	t.Parallel()
	for name, configure := range map[string]func(*Coordinator){"low disk": func(c *Coordinator) { c.disk = fakeDisk(1) }, "extracted total": func(c *Coordinator) { c.cfg.MaxExtractedBytes = 1 }} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
			rel := releaseFor(archive, 1, "1.0.0")
			c := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, &fakeClock{now: time.Now()})
			before := mustReadlink(t, filepath.Join(dir, "current"))
			configure(c)
			if _, err := c.Prepare(context.Background(), rel.ReleaseID); err == nil {
				t.Fatal("resource policy failure ignored")
			}
			if got := mustReadlink(t, filepath.Join(dir, "current")); got != before {
				t.Fatalf("current changed: %q", got)
			}
		})
	}
}

func TestPrepareFailureBackoffIsDurableAndBounded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clock := &fakeClock{now: time.Now()}
	catalog := &countingCatalog{err: errors.New("offline")}
	c := newTestCoordinator(t, dir, catalog, fakeArtifacts{}, &fakeService{}, clock)
	if _, err := c.Prepare(context.Background(), "release-1"); err == nil {
		t.Fatal("catalog failure ignored")
	}
	if _, err := c.Prepare(context.Background(), "release-1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second prepare=%v", err)
	}
	if catalog.calls != 1 {
		t.Fatalf("network calls=%d", catalog.calls)
	}
	clock.now = clock.now.Add(3 * time.Minute)
	if _, err := c.Prepare(context.Background(), "release-1"); err == nil {
		t.Fatal("catalog failure ignored after backoff")
	}
	if catalog.calls != 2 {
		t.Fatalf("network calls=%d", catalog.calls)
	}
	state, err := c.loadState()
	if err != nil {
		t.Fatal(err)
	}
	state.ConsecutiveFailures = 32
	incrementFailures(&state)
	if state.ConsecutiveFailures != 32 {
		t.Fatalf("counter overflowed to %d", state.ConsecutiveFailures)
	}
}

func TestUpdaterLinkCommitsOnlyAfterDurableRunnerHealth(t *testing.T) {
	for _, point := range []string{"after_health_commit", "after_updater_switch"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			archive := makeArchive(t, map[string][]byte{"our-runner": []byte("r"), "our-runner-updater": []byte("u")})
			rel := releaseFor(archive, 11, "11.0.0")
			c := newTestCoordinator(t, dir, fakeCatalog{release: rel}, fakeArtifacts{body: archive}, &fakeService{}, &fakeClock{now: time.Now()})
			oldUpdater := mustReadlink(t, filepath.Join(dir, "updater-current"))
			if _, err := c.Prepare(context.Background(), rel.ReleaseID); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Activate(context.Background(), rel.ReleaseID); err != nil {
				t.Fatal(err)
			}
			c.failpoint = func(got string) error {
				if got == point {
					return errors.New("power loss")
				}
				return nil
			}
			if err := c.Health(context.Background(), rel.ReleaseID, rel.Version); err == nil {
				t.Fatal("failpoint ignored")
			}
			state, err := c.loadState()
			if err != nil {
				t.Fatal(err)
			}
			if state.Phase != PhaseCommittingUpdater {
				t.Fatalf("phase=%s", state.Phase)
			}
			if point == "after_health_commit" && mustReadlink(t, filepath.Join(dir, "updater-current")) != oldUpdater {
				t.Fatal("updater switched before durable commit")
			}
			c.failpoint = nil
			if err := c.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			state, err = c.loadState()
			if err != nil || state.Phase != PhaseIdle || state.ReleaseID != rel.ReleaseID {
				t.Fatalf("recovered state=%+v err=%v", state, err)
			}
			want := filepath.Join(dir, "versions", fmt.Sprintf("%020d-%s", rel.Sequence, rel.Version), "our-runner-updater")
			if got := mustReadlink(t, filepath.Join(dir, "updater-current")); got != want {
				t.Fatalf("updater=%q want %q", got, want)
			}
		})
	}
}

func newTestCoordinator(t *testing.T, dir string, cat Catalog, artifacts ArtifactFetcher, service ServiceController, clock Clock) *Coordinator {
	t.Helper()
	cfg := Config{Channel: "stable", OS: "linux", Arch: "amd64", VersionsDir: filepath.Join(dir, "versions"), StateDir: filepath.Join(dir, "state"), CurrentLink: filepath.Join(dir, "current"), PreviousLink: filepath.Join(dir, "previous"), UpdaterCurrentLink: filepath.Join(dir, "updater-current"), RunnerService: "runner.service", HealthTimeout: time.Minute, MaxArchiveBytes: 1 << 20, MaxFileBytes: 1 << 18, RetainVersions: 3}
	c, err := New(cfg, Dependencies{Catalog: cat, Artifacts: artifacts, Service: service, Preflight: fakePreflight{}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(cfg.CurrentLink); os.IsNotExist(err) {
		initial := filepath.Join(cfg.VersionsDir, "00000000000000000000-0.1.0")
		if err := os.MkdirAll(initial, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range requiredBinaries {
			if err := os.WriteFile(filepath.Join(initial, name), []byte("initial"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(initial, cfg.CurrentLink); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(initial, "our-runner-updater"), cfg.UpdaterCurrentLink); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func releaseFor(body []byte, sequence uint64, version string) Release {
	sum := sha256.Sum256(body)
	ps := sha256.Sum256([]byte(version))
	return Release{ReleaseID: "rel-" + version, Version: version, Sequence: sequence, Channel: "stable", OS: "linux", Arch: "amd64", URL: "https://releases.test/archive.tgz", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), PayloadDigest: hex.EncodeToString(ps[:])}
}

func makeArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func mustReadlink(t *testing.T, p string) string {
	t.Helper()
	v, err := os.Readlink(p)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
