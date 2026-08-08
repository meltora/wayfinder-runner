package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/supervisor"
)

func TestStartJobRejectsUnsafeIdentifiersBeforeCreatingWorkspace(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	d := &Daemon{cfg: config.Config{StateDir: stateDir}, leases: lease.NewManager(), logger: slog.Default()}

	jobs := []v1.JobSpec{
		{JobID: "../../escape", AttemptID: "a1", Kind: v1.JobKindPlan, FencingToken: 1},
		{JobID: "j1", AttemptID: "../escape", Kind: v1.JobKindPlan, FencingToken: 1},
		{JobID: "j1", AttemptID: "a1", Kind: "unknown", FencingToken: 1},
		{JobID: "j1", AttemptID: "a1", Kind: v1.JobKindPlan, FencingToken: 0},
	}
	for _, job := range jobs {
		if err := d.startJob(context.Background(), job); err == nil {
			t.Fatalf("startJob(%+v) unexpectedly succeeded", job)
		}
	}
}

func TestPauseAndResumePersistState(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: config.Config{StateDir: stateDir}, logger: slog.Default()}
	if err := d.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !d.Paused() {
		t.Fatal("runner did not enter paused state")
	}
	info, err := os.Stat(filepath.Join(stateDir, "paused"))
	if err != nil {
		t.Fatalf("pause file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("pause file mode = %o", info.Mode().Perm())
	}
	if err := d.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if d.Paused() {
		t.Fatal("runner remained paused")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "paused")); !os.IsNotExist(err) {
		t.Fatalf("pause file still exists: %v", err)
	}
}

func TestStartJobRequiresControlPlaneLeaseInProductionMode(t *testing.T) {
	t.Parallel()
	d := &Daemon{
		cfg:    config.Config{StateDir: t.TempDir(), AllowUnsafeDirect: false},
		leases: lease.NewManager(),
		logger: slog.Default(),
	}
	err := d.startJob(context.Background(), v1.JobSpec{
		JobID: "job-1", AttemptID: "attempt-1", Kind: v1.JobKindPlan, FencingToken: 1,
	})
	if err == nil {
		t.Fatal("job without an authoritative lease unexpectedly started")
	}
}

type leaseTestProcess struct{ killed bool }

func (p *leaseTestProcess) PID() int         { return 123 }
func (p *leaseTestProcess) Wait() error      { return nil }
func (p *leaseTestProcess) Interrupt() error { return nil }
func (p *leaseTestProcess) Kill() error      { p.killed = true; return nil }

var _ supervisor.Process = (*leaseTestProcess)(nil)

func TestLeaseMonitorQuarantinesAndKillsProcess(t *testing.T) {
	proc := &leaseTestProcess{}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	jr := &jobRunner{
		d:      &Daemon{leases: lease.NewManager()},
		job:    v1.JobSpec{JobID: "job-1", AttemptID: "attempt-1", FencingToken: 1},
		proc:   proc,
		logger: slog.Default(),
		cancel: cancel,
	}
	done := make(chan struct{})
	go func() {
		jr.monitorLease(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("lease monitor did not detect missing lease")
	}
	jr.procMu.Lock()
	lost, cancelled := jr.leaseLost, jr.cancelled
	jr.procMu.Unlock()
	if !lost || !cancelled || !proc.killed {
		t.Fatalf("lost=%v cancelled=%v killed=%v", lost, cancelled, proc.killed)
	}
}

func TestCleanupOrphanedJobStateUsesStrictPrefixes(t *testing.T) {
	stateDir := t.TempDir()
	paths := map[string][]string{
		"jobs":       {"job-old", "keep"},
		"job-homes":  {"home-old", "keep"},
		"sockets":    {"job-old", "keep"},
		"tmux":       {"job-old", "keep"},
		"supervisor": {".herdr-exit-old", "keep"},
	}
	for dir, names := range paths {
		for _, name := range names {
			if err := os.MkdirAll(filepath.Join(stateDir, dir, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := cleanupOrphanedJobState(stateDir); err != nil {
		t.Fatal(err)
	}
	for dir, names := range paths {
		if _, err := os.Stat(filepath.Join(stateDir, dir, names[0])); !os.IsNotExist(err) {
			t.Fatalf("orphan %s/%s remains: %v", dir, names[0], err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, dir, names[1])); err != nil {
			t.Fatalf("unrelated %s/%s removed: %v", dir, names[1], err)
		}
	}
}

func workspaceValidationRunner(t *testing.T, repositories []v1.JobRepository) *jobRunner {
	t.Helper()
	stateDir := t.TempDir()
	d := &Daemon{
		cfg:        config.Config{StateDir: stateDir, Repositories: map[string]config.RepositoryConfig{}},
		collectors: collector.NewStore(stateDir),
		logger:     slog.Default(),
	}
	return &jobRunner{
		d: d, job: v1.JobSpec{JobID: "job-1", AttemptID: "attempt-1", Repositories: repositories},
		workDir: t.TempDir(), logger: slog.Default(),
	}
}

func TestPrepareWorkspaceRejectsInvalidRepositoryTopologyBeforeGit(t *testing.T) {
	cases := map[string][]v1.JobRepository{
		"none":             nil,
		"too many":         make([]v1.JobRepository, 17),
		"no primary":       {{RepositoryID: "repo-1"}},
		"multiple primary": {{RepositoryID: "repo-1", Primary: true}, {RepositoryID: "repo-2", Primary: true}},
		"invalid id":       {{RepositoryID: "bad/id"}},
		"duplicate":        {{RepositoryID: "repo-1"}, {RepositoryID: "repo-1"}},
		"unregistered":     {{RepositoryID: "repo-1"}},
	}
	for name, repositories := range cases {
		t.Run(name, func(t *testing.T) {
			jr := workspaceValidationRunner(t, repositories)
			if _, _, err := jr.prepareWorkspace(context.Background()); err == nil {
				t.Fatal("invalid repository topology accepted")
			}
		})
	}
}

func TestAgentPackReferenceAndArtifactOriginValidation(t *testing.T) {
	jr := workspaceValidationRunner(t, []v1.JobRepository{{RepositoryID: "repo-1"}})
	jr.ctx = context.Background()
	if err := jr.fetchAndMountPack(&v1.AgentPackRef{}, filepath.Join(t.TempDir(), "pack")); err == nil {
		t.Fatal("pack without key ID accepted")
	}
	if err := jr.fetchAndMountPack(&v1.AgentPackRef{KeyID: "unknown"}, filepath.Join(t.TempDir(), "pack")); err == nil {
		t.Fatal("untrusted pack key accepted")
	}
	jr.d.cfg.AgentPackPublicKeys = map[string]string{"key-1": "invalid"}
	if err := jr.fetchAndMountPack(&v1.AgentPackRef{KeyID: "key-1", URL: "file:///etc/passwd"}, filepath.Join(t.TempDir(), "pack")); err == nil {
		t.Fatal("unsafe pack URL accepted")
	}

	jr.d.cfg.ControlPlaneURL = "wss://control.example.test/runner"
	if err := jr.d.validateArtifactURL("https://control.example.test/artifacts/a"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"not a URL", "http://control.example.test/a", "https://other.example.test/a"} {
		if err := jr.d.validateArtifactURL(raw); err == nil {
			t.Fatalf("artifact URL %q accepted", raw)
		}
	}
	jr.d.cfg.AllowUnsafeDirect = true
	jr.d.cfg.AllowedArtifactOrigins = []string{"http://local.example.test"}
	if err := jr.d.validateArtifactURL("http://local.example.test/a"); err != nil {
		t.Fatal(err)
	}
}

func TestStartJobRefusesBusyExpiredAndOversizedLeases(t *testing.T) {
	newDaemon := func(t *testing.T) *Daemon {
		t.Helper()
		stateDir := t.TempDir()
		j, err := journal.Open(filepath.Join(stateDir, "journal.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = j.Close() })
		return &Daemon{
			cfg: config.Config{StateDir: stateDir, AllowUnsafeDirect: true}, journal: j,
			leases: lease.NewManager(), logger: slog.Default(),
		}
	}
	job := v1.JobSpec{
		JobID: "job-1", AttemptID: "attempt-1", Kind: v1.JobKindPlan, FencingToken: 1,
		Repositories: []v1.JobRepository{{RepositoryID: "repo-1", Primary: true}},
		Ticket:       v1.TicketContext{Key: "PROJ-1", Summary: "Feature"},
	}

	d := newDaemon(t)
	d.activeJob = &jobRunner{job: job}
	if err := d.startJob(context.Background(), job); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("busy start=%v", err)
	}

	d = newDaemon(t)
	expired := job
	expired.LeaseExpiresAt = time.Now().Add(-time.Minute)
	if err := d.startJob(context.Background(), expired); err == nil {
		t.Fatal("expired lease accepted")
	}

	d = newDaemon(t)
	tooLong := job
	tooLong.LeaseExpiresAt = time.Now().Add(25 * time.Hour)
	if err := d.startJob(context.Background(), tooLong); err == nil {
		t.Fatal("lease longer than 24 hours accepted")
	}

	d = newDaemon(t)
	if err := os.WriteFile(filepath.Join(d.cfg.StateDir, "jobs"), []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.startJob(context.Background(), job); err == nil || !strings.Contains(err.Error(), "jobs root") {
		t.Fatalf("blocked jobs root=%v", err)
	}
}
