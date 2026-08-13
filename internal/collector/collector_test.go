package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestSaveArtifact(t *testing.T) {
	s := NewStore(t.TempDir())
	ref, path, err := s.SaveArtifact("j1", "a1", 7, "plan.md", "text/markdown", strings.NewReader("# Plan"))
	if err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}
	if ref.SizeBytes != 6 || ref.FencingToken != 7 {
		t.Fatalf("ref = %+v", ref)
	}
	if len(ref.DigestSHA256) != 64 {
		t.Fatalf("digest = %q", ref.DigestSHA256)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("artifact missing: %v", err)
	}
}

func TestSaveArtifactRejectsTraversal(t *testing.T) {
	t.Parallel()
	s := NewStore(t.TempDir())

	for _, tc := range []struct {
		jobID   string
		attempt string
		name    string
	}{
		{jobID: "../escape", attempt: "a1", name: "plan.md"},
		{jobID: "j1", attempt: "../../escape", name: "plan.md"},
		{jobID: "j1", attempt: "a1", name: "../../../runner-key.pem"},
		{jobID: "j1", attempt: "a1", name: "nested/plan.md"},
	} {
		if _, _, err := s.SaveArtifact(tc.jobID, tc.attempt, 1, tc.name, "text/plain", strings.NewReader("x")); err == nil {
			t.Fatalf("SaveArtifact(%q, %q, %q) unexpectedly succeeded", tc.jobID, tc.attempt, tc.name)
		}
	}
}

func TestSaveArtifactUsesInternalStorageName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := NewStore(root)
	ref, path, err := s.SaveArtifact("j1", "a1", 1, "plan.md", "text/markdown", strings.NewReader("plan"))
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != "plan.md" {
		t.Fatalf("display name = %q", ref.Name)
	}
	if filepath.Base(path) == ref.Name {
		t.Fatal("artifact storage name must not be controlled by the agent")
	}
}

func TestSaveArtifactEnforcesSpoolQuota(t *testing.T) {
	s := NewStore(t.TempDir())
	s.SetMaxSpoolBytes(5)
	if _, _, err := s.SaveArtifact("j1", "a1", 1, "first", "text/plain", strings.NewReader("12345")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveArtifact("j1", "a1", 1, "second", "text/plain", strings.NewReader("x")); err == nil {
		t.Fatal("artifact exceeding spool quota was accepted")
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	job := v1.JobSpec{JobID: "j1", AttemptID: "a1", FencingToken: 1}
	cp1 := collectorCheckpoint(job, "step-1")
	if err := s.SaveCheckpoint(cp1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	cp2 := collectorCheckpoint(job, "step-2")
	if err := s.SaveCheckpoint(cp2); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestCheckpoint("j1")
	if err != nil {
		t.Fatal(err)
	}
	if latest.PlanStep != "step-2" {
		t.Fatalf("latest = %+v", latest)
	}
}

func TestCheckpointForAttemptRequiresExactFence(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	job := v1.JobSpec{JobID: "job-1", AttemptID: "attempt-1", FencingToken: 9}
	checkpoint := collectorCheckpoint(job, "step")
	if err := store.SaveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	got, err := store.CheckpointForAttempt(job.JobID, job.AttemptID, job.FencingToken)
	if err != nil {
		t.Fatal(err)
	}
	if got.AttemptID != job.AttemptID || got.FencingToken != job.FencingToken {
		t.Fatalf("checkpoint = %#v", got)
	}
	if _, err := store.CheckpointForAttempt(job.JobID, job.AttemptID, job.FencingToken+1); err == nil {
		t.Fatal("checkpoint was accepted under a different fence")
	}
	for name, read := range map[string]func() error{
		"invalid job": func() error {
			_, err := store.CheckpointForAttempt("bad/id", job.AttemptID, job.FencingToken)
			return err
		},
		"invalid attempt": func() error {
			_, err := store.CheckpointForAttempt(job.JobID, "bad/id", job.FencingToken)
			return err
		},
		"zero fence": func() error {
			_, err := store.CheckpointForAttempt(job.JobID, job.AttemptID, 0)
			return err
		},
		"missing checkpoint": func() error {
			_, err := store.CheckpointForAttempt(job.JobID, "missing", job.FencingToken)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := read(); err == nil {
				t.Fatal("invalid checkpoint lookup succeeded")
			}
		})
	}
}

func collectorCheckpoint(job v1.JobSpec, step string) v1.Checkpoint {
	return v1.Checkpoint{
		JobID: job.JobID, AttemptID: job.AttemptID, FencingToken: job.FencingToken,
		PlanStep: step, CreatedAt: time.Now().UTC(),
	}
}

func TestCheckpointValidationAndMalformedCandidates(t *testing.T) {
	store := NewStore(t.TempDir())
	base := v1.Checkpoint{
		JobID: "job-1", AttemptID: "attempt-1", FencingToken: 1,
		PlanStep: "step", CreatedAt: time.Now(),
	}
	mutations := map[string]func(*v1.Checkpoint){
		"job":     func(c *v1.Checkpoint) { c.JobID = "bad/id" },
		"attempt": func(c *v1.Checkpoint) { c.AttemptID = "bad id" },
		"fence":   func(c *v1.Checkpoint) { c.FencingToken = 0 },
		"created": func(c *v1.Checkpoint) { c.CreatedAt = time.Time{} },
		"step":    func(c *v1.Checkpoint) { c.PlanStep = "bad\x00step" },
		"branch":  func(c *v1.Checkpoint) { c.BranchState = strings.Repeat("x", (64<<10)+1) },
		"session": func(c *v1.Checkpoint) { c.ProviderSessionID = "bad session" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			checkpoint := base
			mutate(&checkpoint)
			if err := store.SaveCheckpoint(checkpoint); err == nil {
				t.Fatal("invalid checkpoint accepted")
			}
		})
	}
	if _, err := store.LatestCheckpoint("bad/id"); err == nil {
		t.Fatal("unsafe checkpoint lookup accepted")
	}
	if _, err := store.LatestCheckpoint("missing"); err == nil {
		t.Fatal("missing checkpoint unexpectedly found")
	}

	dir := filepath.Join(store.Root, "checkpoints", "job-1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"bad id.json":    `{}`,
		"wrong-job.json": `{"job_id":"other","attempt_id":"wrong-job"}`,
		"invalid.json":   `{`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.LatestCheckpoint("job-1"); err == nil {
		t.Fatal("malformed checkpoints accepted")
	}
}

func TestArtifactSpoolRejectsSymlinksAndRemainingQuotaOverflow(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(artifactRoot, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveArtifact("job-1", "attempt-1", 1, "x", "text/plain", strings.NewReader("x")); err == nil {
		t.Fatal("artifact spool symlink accepted")
	}
	if err := os.Remove(filepath.Join(artifactRoot, "escape")); err != nil {
		t.Fatal(err)
	}
	store.SetMaxSpoolBytes(3)
	if _, _, err := store.SaveArtifact("job-1", "attempt-1", 1, "x", "text/plain", strings.NewReader("1234")); err == nil {
		t.Fatal("artifact exceeding remaining quota accepted")
	}
}

func TestNewCheckpointCopiesAuthoritativeIdentity(t *testing.T) {
	job := v1.JobSpec{JobID: "job-1", AttemptID: "attempt-1", FencingToken: 7}
	cp := NewCheckpoint(job, "step", "branch", "next")
	if cp.JobID != job.JobID || cp.AttemptID != job.AttemptID || cp.FencingToken != 7 || cp.CreatedAt.IsZero() {
		t.Fatalf("checkpoint=%+v", cp)
	}
}
