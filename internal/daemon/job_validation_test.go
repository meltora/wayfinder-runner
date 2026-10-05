package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
	"github.com/example/app-runner/internal/supervisor"
)

func signedTestPackJob(t *testing.T) (v1.JobSpec, string) {
	t.Helper()
	ref := v1.AgentPackRef{
		Version: "1.0.0", Digest: strings.Repeat("a", 64),
		URL: "https://packs.example.test/pack-1.0.0.tgz", KeyID: "pack-key-1",
	}
	authority := v1.SignedAgentPackAuthority{
		SchemaVersion: 1, RegistryVersionID: "pack-version-1",
		Scope:   v1.AgentPackAuthorityScope{Kind: v1.AgentPackScopePlatform},
		PackKey: "default-pack", PackName: "Default Pack", Provider: "codex",
		ProtocolVersion: 1, MinimumRunnerVersion: "0.1.0", AgentPack: ref,
	}
	digest, err := agentpack.PublicationAttestationDigest(authority)
	if err != nil {
		t.Fatal(err)
	}
	authority.AttestationDigest = digest
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digestBytes, _ := hex.DecodeString(digest)
	authority.SignatureBase64 = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, digestBytes))
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	job := withTestAuthority(v1.JobSpec{
		JobID: "job-pack", AttemptID: "attempt-pack", ProjectID: "project-1", Kind: v1.JobKindPlan,
		FencingToken: 1, Repositories: []v1.JobRepository{{RepositoryID: "repo-1", Primary: true}},
		Ticket:    v1.TicketContext{Key: "PROJ-1", Summary: "Use signed pack"},
		AgentPack: &ref, AgentPackAuthority: &authority,
	})
	return job, base64.StdEncoding.EncodeToString(der)
}

func TestRunnerVerifiesSignedAgentPackAuthorityBeforeStartingJob(t *testing.T) {
	job, trustedKey := signedTestPackJob(t)
	d := &Daemon{cfg: config.Config{
		Provider: "codex", AgentPackPublicKeys: map[string]string{"pack-key-1": trustedKey},
		AllowedArtifactOrigins: []string{"https://packs.example.test"},
	}}
	if err := d.verifyJobAgentPackAuthority(job); err != nil {
		t.Fatalf("valid authority rejected: %v", err)
	}

	tests := map[string]struct {
		mutate func(*Daemon, *v1.JobSpec)
		reason v1.CommandAckReasonCode
	}{
		"untrusted key": {
			mutate: func(d *Daemon, _ *v1.JobSpec) { d.cfg.AgentPackPublicKeys = nil },
			reason: v1.CommandAckReasonAgentPackKeyUntrusted,
		},
		"wrong provider": {
			mutate: func(d *Daemon, _ *v1.JobSpec) { d.cfg.Provider = "opencode" },
			reason: v1.CommandAckReasonAgentPackIncompatible,
		},
		"tampered signature": {
			mutate: func(_ *Daemon, job *v1.JobSpec) {
				raw, _ := base64.StdEncoding.DecodeString(job.AgentPackAuthority.SignatureBase64)
				raw[0] ^= 0xff
				job.AgentPackAuthority.SignatureBase64 = base64.StdEncoding.EncodeToString(raw)
			},
			reason: v1.CommandAckReasonAgentPackAuthorityInvalid,
		},
		"origin unapproved": {
			mutate: func(d *Daemon, _ *v1.JobSpec) { d.cfg.AllowedArtifactOrigins = []string{"https://other.example.test"} },
			reason: v1.CommandAckReasonAgentPackOriginUnapproved,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := job
			authority := *job.AgentPackAuthority
			candidate.AgentPackAuthority = &authority
			candidateDaemon := &Daemon{cfg: config.Normalize(d.cfg)}
			test.mutate(candidateDaemon, &candidate)
			if err := candidateDaemon.verifyJobAgentPackAuthority(candidate); err == nil {
				t.Fatal("invalid authority accepted")
			} else if reason := commandRejectionReason(err); reason != test.reason {
				t.Fatalf("rejection reason = %q, want %q", reason, test.reason)
			}
		})
	}
}

func TestAgentPackMaterializationFailureHasControlledTerminalClassification(t *testing.T) {
	failure := &agentPackMaterializationError{
		category:  v1.AgentPackFailureDownloadUnavailable,
		retryable: true,
		cause:     context.Canceled,
	}
	outcome := classifyWorkspacePreparationFailure(failure)
	if outcome.FailureCode != v1.JobFailureAgentPackMaterialization ||
		outcome.FailureMessage != "Agent Pack materialization failed" ||
		outcome.FailureCategory != v1.AgentPackFailureDownloadUnavailable ||
		outcome.Retryable == nil || !*outcome.Retryable {
		t.Fatalf("classification = %#v", outcome)
	}
	outcome = classifyWorkspacePreparationFailure(context.Canceled)
	if outcome.FailureCode != v1.JobFailureWorkspacePreparation ||
		outcome.FailureMessage != "workspace preparation failed" ||
		outcome.FailureCategory != "" || outcome.Retryable != nil {
		t.Fatalf("ordinary classification = %#v", outcome)
	}
}

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

func TestPlanWorkspaceUsesObservedCommitAfterCommandBranchMoves(t *testing.T) {
	origin := t.TempDir()
	runGit := func(dir string, args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = dir
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	runGit(origin, "init", "-b", "main")
	runGit(origin, "config", "user.email", "runner@example.test")
	runGit(origin, "config", "user.name", "Runner Test")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("observed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(origin, "add", "README.md")
	runGit(origin, "commit", "-m", "observed")
	observedSHA := runGit(origin, "rev-parse", "HEAD")
	repositories := []v1.JobRepository{{
		RepositoryID: "repo-1", RepositoryHeadObservationID: "head-observation-1",
		BaseBranch: "main", BaseSHA: observedSHA, Primary: true,
	}}

	stateDir := t.TempDir()
	durableJournal, err := journal.Open(filepath.Join(stateDir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = durableJournal.Close() })
	jr := &jobRunner{
		d: &Daemon{
			cfg: config.Config{
				StateDir: stateDir, AllowUnsafeDirect: true,
				Repositories: map[string]config.RepositoryConfig{"repo-1": {URL: origin}},
			},
			journal: durableJournal, logger: slog.Default(),
		},
		job: v1.JobSpec{
			JobID: "job-1", AttemptID: "attempt-1", ProjectID: "project-1", Kind: v1.JobKindPlan,
			Repositories: repositories,
		},
		workDir: t.TempDir(), logger: slog.Default(),
	}
	if err := os.WriteFile(filepath.Join(origin, "moved.txt"), []byte("new branch head\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(origin, "add", "moved.txt")
	runGit(origin, "commit", "-m", "branch moved after command")

	primaryDir, cleanup, err := jr.prepareWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if got := runGit(primaryDir, "rev-parse", "HEAD"); got != observedSHA {
		t.Fatalf("planning checkout = %s, want observed %s", got, observedSHA)
	}
	if branch := runGit(primaryDir, "branch", "--show-current"); branch != "" {
		t.Fatalf("planning checkout attached to mutable branch %q", branch)
	}
	if !slices.Equal(jr.verifiedPlanRepositories, repositories) {
		t.Fatalf("verified repositories = %#v", jr.verifiedPlanRepositories)
	}

	unavailable := append([]v1.JobRepository(nil), repositories...)
	unavailable[0].BaseSHA = strings.Repeat("f", 40)
	rejected := &jobRunner{
		d: jr.d,
		job: v1.JobSpec{
			JobID: "job-2", AttemptID: "attempt-2", ProjectID: "project-1", Kind: v1.JobKindPlan,
			Repositories: unavailable,
		},
		workDir: t.TempDir(), logger: slog.Default(),
	}
	if _, _, err := rejected.prepareWorkspace(context.Background()); err == nil || !strings.Contains(err.Error(), "workspace repo-1") {
		t.Fatalf("unavailable observed commit error = %v", err)
	}
}

func TestAgentPackReferenceAndArtifactOriginValidation(t *testing.T) {
	jr := workspaceValidationRunner(t, []v1.JobRepository{{RepositoryID: "repo-1"}})
	jr.ctx = context.Background()
	jr.d.cfg.ControlPlaneURL = "wss://control.example.test/runner"
	if err := jr.d.validateArtifactURL("https://control.example.test/artifacts/a"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"not a URL", "http://control.example.test/a", "https://other.example.test/a",
		"https://user@control.example.test/a", "https://control.example.test/a#fragment",
		"https://CONTROL.example.test/a", "https://control.example.test.:443/a", "https://café.example.test/a",
	} {
		if err := jr.d.validateArtifactURL(raw); err == nil {
			t.Fatalf("artifact URL %q accepted", raw)
		}
	}
	jr.d.cfg.AllowUnsafeDirect = true
	jr.d.cfg.AllowedArtifactOrigins = []string{"https://local.example.test"}
	if err := jr.d.validateArtifactURL("http://local.example.test/a"); err == nil {
		t.Fatal("unsafe direct mode weakened signed artifact transport")
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
	job := withTestAuthority(v1.JobSpec{
		JobID: "job-1", AttemptID: "attempt-1", Kind: v1.JobKindPlan, FencingToken: 1,
		Repositories: []v1.JobRepository{{RepositoryID: "repo-1", Primary: true}},
		Ticket:       v1.TicketContext{Key: "PROJ-1", Summary: "Feature"},
	})

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
