package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanCheckoutVerificationLogsOneCauseClassPerFailure(t *testing.T) {
	const mismatch1 = "plan repository checkout mismatch for repository-1"
	for name, test := range map[string]struct {
		change     func(t *testing.T, jr *jobRunner, dir string)
		err        string
		repository string
		cause      string
	}{
		"command set": {func(t *testing.T, jr *jobRunner, dir string) {
			jr.verifiedPlanRepositories = nil
		}, "plan repository authority is not the verified command set", "", "identity"},
		"checkout identity": {func(t *testing.T, jr *jobRunner, dir string) {
			jr.repositories[0].Branch = "agent-branch"
		}, "plan repository checkout identity does not match command authority", "repository-1", "identity"},
		"unreadable head": {func(t *testing.T, jr *jobRunner, dir string) {
			if err := os.Remove(filepath.Join(dir, ".git", "HEAD")); err != nil {
				t.Fatal(err)
			}
		}, mismatch1, "repository-1", "head_unreadable"},
		"moved head": {func(t *testing.T, jr *jobRunner, dir string) {
			planCheckoutGit(t, dir, "commit", "--allow-empty", "-m", "moved")
		}, mismatch1, "repository-1", "head_mismatch"},
		"missing baseline": {func(t *testing.T, jr *jobRunner, dir string) {
			jr.repositories[0].PlanBaseline = nil
		}, mismatch1, "repository-1", "baseline_missing"},
		"unreadable baseline": {func(t *testing.T, jr *jobRunner, dir string) {
			closed := filepath.Join(dir, "closed")
			if err := os.Mkdir(closed, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })
		}, mismatch1, "repository-1", "baseline_unreadable"},
		"changed baseline": {func(t *testing.T, jr *jobRunner, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("rewritten\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, mismatch1, "repository-1", "baseline_mismatch"},
		"changed secondary baseline": {func(t *testing.T, jr *jobRunner, dir string) {
			jr.repositories[1].PlanBaseline = newPlanCheckoutEvidence(planCheckoutDigests{Strict: "other", Identity: "other"})
		}, "plan repository checkout mismatch for repository-2", "repository-2", "baseline_mismatch"},
	} {
		t.Run(name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			configurePlanPublicationRepositories(t, jr)
			var logs bytes.Buffer
			jr.logger = slog.New(slog.NewTextHandler(&logs, nil))
			dir := jr.repositories[0].WorkDir
			digest := jr.repositories[0].PlanBaseline.current().Strict
			test.change(t, jr, dir)

			_, err := jr.verifyPlanRepositoryAuthority(context.Background())

			if err == nil || err.Error() != test.err {
				t.Fatalf("err = %v, want %q", err, test.err)
			}
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("log lines = %q", lines)
			}
			line := lines[0]
			want := "cause=" + test.cause
			if test.repository != "" {
				want = "repository=" + test.repository + " " + want
			} else if strings.Contains(line, "repository=") {
				t.Fatalf("command-set log names a repository: %s", line)
			}
			if !strings.Contains(line, `msg="plan repository checkout verification failed" `+want) {
				t.Fatalf("log = %s, want %s", line, want)
			}
			for _, secret := range []string{dir, digest, "README", ".git", "closed", "/tmp"} {
				if strings.Contains(line, secret) {
					t.Fatalf("log discloses %q: %s", secret, line)
				}
			}
		})
	}
}

func TestPlanCheckoutVerificationLogsNothingOnSuccess(t *testing.T) {
	jr, _ := testJobRunner(t)
	configurePlanPublicationRepositories(t, jr)
	var logs bytes.Buffer
	jr.logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := jr.verifyPlanRepositoryAuthority(context.Background()); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("log = %s", logs.String())
	}
}

func TestPlanCheckoutEvidenceRefusesAnIncompleteCarryForward(t *testing.T) {
	var missing *planCheckoutEvidence
	if missing.carryForward(planCheckoutDigests{Strict: "s", Identity: "i"}) || missing.current() != (planCheckoutDigests{}) {
		t.Fatal("nil evidence accepted a carry-forward")
	}
	evidence := newPlanCheckoutEvidence(planCheckoutDigests{Strict: "s", Identity: "i"})
	for _, observed := range []planCheckoutDigests{{Strict: "", Identity: "i"}, {Strict: "t", Identity: ""}, {Strict: "t", Identity: "j"}} {
		if evidence.carryForward(observed) {
			t.Fatalf("carry-forward accepted %+v", observed)
		}
	}
	if evidence.current() != (planCheckoutDigests{Strict: "s", Identity: "i"}) {
		t.Fatalf("evidence = %+v", evidence.current())
	}
	if !evidence.carryForward(planCheckoutDigests{Strict: "t", Identity: "i"}) || evidence.current().Strict != "t" {
		t.Fatal("carry-forward refused an unchanged identity")
	}
	empty := newPlanCheckoutEvidence(planCheckoutDigests{Strict: "s"})
	if empty.carryForward(planCheckoutDigests{Strict: "t", Identity: "i"}) {
		t.Fatal("carry-forward accepted a baseline without identity")
	}
}
