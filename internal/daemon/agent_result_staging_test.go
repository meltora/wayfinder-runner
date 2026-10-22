package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStagingFailureIsATypedActionableRefusal(t *testing.T) {
	f := newAgentResultTest(t)
	writeAgentFile(t, f.checkout, ".gitattributes", "* working-tree-encoding=NOPE-ENC\n")
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")

	_, err := f.jr.commitAgentChanges(context.Background(), f.repository())

	var resultErr *agentResultError
	if !errors.As(err, &resultErr) {
		t.Fatalf("err = %v, want a typed refusal", err)
	}
	for _, want := range []string{"the checkout repositories/repo-1 could not be staged", "failed to encode", "from NOPE-ENC"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
	}
	outcome := buildFinalizationFailure(fmt.Errorf("commit repo-1: %w", err))
	if outcome.FailureMessage != err.Error() {
		t.Fatalf("failure message = %q", outcome.FailureMessage)
	}
	assertNoHostPath(t, err.Error(), f.jr.d.cfg.StateDir, f.jr.workDir)
}

func TestUnknownStartCommitIsATypedRefusal(t *testing.T) {
	f := newAgentResultTest(t)
	repository := f.repository()
	repository.StartSHA = strings.Repeat("b", 40)

	_, err := f.jr.resolveAgentResult(context.Background(), repository, "build result")

	var resultErr *agentResultError
	if !errors.As(err, &resultErr) || !strings.Contains(err.Error(), "could not be staged") {
		t.Fatalf("err = %v, want a typed staging refusal", err)
	}
	assertNoHostPath(t, err.Error(), f.jr.d.cfg.StateDir, f.jr.workDir)
}

func TestTrustedStepRefusalMapsFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "job-results", "job-1", "repo-1")
	trusted := &trustedResultRepository{root: root}
	label := "worktree repositories/repo-1/wt"
	background := context.Background()

	invalid := trustedGitError([]string{"add", "-A"}, &fakeExitError{}, "error: invalid path 'sub/.git /config'\nerror: unable to add 'sub/.git /config' to index\nfatal: adding files failed")
	got := trusted.stepRefusal(background, "repo-1", label, invalid)
	if !strings.Contains(got.Error(), `has a path Git refuses to store: "sub/.git /config"; rename or remove it`) {
		t.Fatalf("invalid path refusal = %v", got)
	}

	leaky := trustedGitError([]string{"write-tree"}, &fakeExitError{}, "fatal: Unable to create '"+root+"/repo.git/index.lock': File exists")
	got = trusted.stepRefusal(background, "repo-1", label, leaky)
	var resultErr *agentResultError
	if !errors.As(got, &resultErr) || strings.Contains(got.Error(), root) || !strings.Contains(got.Error(), "could not be staged (git write-tree: fatal: Unable to create") {
		t.Fatalf("generic refusal = %v", got)
	}

	plain := fmt.Errorf("mkdir %s/files: permission denied", root)
	got = trusted.stepRefusal(background, "repo-1", label, plain)
	if !errors.As(got, &resultErr) || strings.Contains(got.Error(), root) {
		t.Fatalf("non-Git refusal = %v", got)
	}

	typed := agentResultRefusal("repo-1", "already typed")
	if got := trusted.stepRefusal(background, "repo-1", label, typed); got != typed {
		t.Fatalf("typed refusal was rewrapped: %v", got)
	}

	cancelled, cancel := context.WithCancel(background)
	cancel()
	if got := trusted.stepRefusal(cancelled, "repo-1", label, plain); errors.As(got, &resultErr) {
		t.Fatalf("cancellation became a refusal: %v", got)
	}
	if got := trusted.stepRefusal(background, "repo-1", label, nil); got != nil {
		t.Fatalf("nil error = %v", got)
	}

	trusted.resolveTimeout = time.Hour
	expired, cancelExpired := context.WithDeadline(background, time.Now().Add(-time.Second))
	defer cancelExpired()
	callerBound, cancelCallerBound := trusted.resolutionContext(expired)
	defer cancelCallerBound()
	if got := trusted.stepRefusal(callerBound, "repo-1", label, context.DeadlineExceeded); errors.As(got, &resultErr) {
		t.Fatalf("caller deadline became a resolution timeout refusal: %v", got)
	}

	trusted.resolveTimeout = time.Nanosecond
	ownBound, cancelOwnBound := trusted.resolutionContext(background)
	defer cancelOwnBound()
	<-ownBound.Done()
	if got := trusted.stepRefusal(ownBound, "repo-1", label, context.DeadlineExceeded); !errors.As(got, &resultErr) || !strings.Contains(got.Error(), "took longer than 1ns") {
		t.Fatalf("resolution timeout refusal = %v", got)
	}
}

func TestSnapshotFilesystemFailureIsATypedRefusalWithoutHostPaths(t *testing.T) {
	f := newAgentResultTest(t)
	trusted := f.repository().Trusted
	writeAgentFile(t, trusted.filesDir, "stale/leftover.txt", "stale\n")
	if err := os.Chmod(trusted.filesDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(trusted.filesDir, 0o700) })
	writeAgentFile(t, f.checkout, "feature.txt", "real\n")

	_, err := f.jr.resolveAgentResult(context.Background(), f.repository(), "build result")

	var resultErr *agentResultError
	if !errors.As(err, &resultErr) || !strings.Contains(err.Error(), "could not be copied by the runner") {
		t.Fatalf("err = %v, want a typed copy refusal", err)
	}
	if !strings.Contains(err.Error(), trustedResultPlaceholder) {
		t.Fatalf("err = %v, want the host path replaced", err)
	}
	assertNoHostPath(t, err.Error(), f.jr.d.cfg.StateDir, trusted.root)
}

func TestTrustedGitFailureMessages(t *testing.T) {
	exitErr := exec.Command("false").Run()
	if got := trustedGitError([]string{"add"}, exitErr, " fatal: boom \n").Error(); got != "trusted Git add failed: fatal: boom" {
		t.Fatalf("exit message = %q", got)
	}
	if got := trustedGitError([]string{"add"}, exitErr, "").Error(); got != "trusted Git add failed: exit status 1" {
		t.Fatalf("silent exit message = %q", got)
	}
	failure := trustedGitError([]string{"-q", "write-tree"}, context.DeadlineExceeded, "ignored")
	if got := failure.Error(); got != "trusted Git write-tree failed: context deadline exceeded" || !errors.Is(failure, context.DeadlineExceeded) {
		t.Fatalf("start failure = %q", got)
	}
}

type fakeExitError struct{}

func (*fakeExitError) Error() string { return "exit status 128" }

func TestInvalidStagingPathParsing(t *testing.T) {
	for stderr, want := range map[string]string{
		"error: invalid path 'a/b'\nfatal: adding files failed": "a/b",
		"error: invalid path 'it''s'":                           "it''s",
		"fatal: something else":                                 "",
		"error: invalid path 'unterminated":                     "",
	} {
		if got, _ := invalidStagingPath(stderr); got != want {
			t.Errorf("invalidStagingPath(%q) = %q, want %q", stderr, got, want)
		}
	}
}
