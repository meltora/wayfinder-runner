package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/claudelogin"
	"github.com/example/app-runner/internal/isolation"
)

func TestProviderJobReportsAJobRotationWithoutWritingItBackOnEveryEnd(t *testing.T) {
	for name, test := range map[string]struct {
		configure   func(*ownershipRun)
		state, code string
	}{
		"clean exit": {configure: func(*ownershipRun) {}, state: "failed", code: string(v1.JobFailurePlanNotPublished)},
		"provider failure": {
			configure: func(run *ownershipRun) { run.sup.waitErr = errors.New("exit status 3") },
			state:     "failed", code: string(v1.JobFailureAgentExit),
		},
		"cancelled while running": {
			configure: func(run *ownershipRun) {
				run.sup.waitErr = errors.New("killed")
				previous := run.sup.duringRun
				run.sup.duringRun = func() {
					previous()
					run.jr.cancelled = true
				}
			},
			state: "cancelled", code: string(v1.JobFailureCancelled),
		},
	} {
		t.Run(name, func(t *testing.T) {
			run := newOwnershipRun(t, testRootlessMapping)
			hostDir := filepath.Join(os.Getenv("HOME"), ".claude")
			writeTestFile(t, filepath.Join(hostDir, ".credentials.json"), `{"mcpOAuth":{},"claudeAiOauth":`+fakeOAuth("r0", 100, "")+`}`)
			var logs bytes.Buffer
			exchanges := 0
			run.jr.d.claudeLogins = newClaudeLoginSync(func(context.Context, string, string, claudelogin.ExchangeRequest) (claudelogin.ExchangeResult, error) {
				exchanges++
				return claudelogin.ExchangeResult{}, nil
			}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
			run.jr.logger = slog.New(slog.NewTextHandler(&logs, nil))
			run.sup.duringRun = func() {
				home := run.ownership.handOffs[0].Private[0]
				writeTestFile(t, filepath.Join(home, ".claude", ".credentials.json"), fakeCredentials(fakeOAuth("r1", 200, "")))
			}
			test.configure(run)
			run.execute(t)
			run.expectFinished(t, test.state, test.code)
			if got := refreshOf(t, hostDir); got != "r0" {
				t.Fatalf("host login = %s after the job ended, want r0 untouched", got)
			}
			if !strings.Contains(logs.String(), "event=job_login_rotated") {
				t.Fatalf("the job rotation was not reported: %s", logs.String())
			}
			if len(run.jr.d.claudeLogins.jobs) != 0 {
				t.Fatal("the finished job is still registered")
			}
			if exchanges != 0 {
				t.Fatalf("exchanges = %d during a job without ticks", exchanges)
			}
			if strings.Contains(logs.String(), "fake-") {
				t.Fatalf("a token reached the log: %s", logs.String())
			}
		})
	}
}

func TestProviderJobStopsLoginExchangesBeforeReclaim(t *testing.T) {
	run := newOwnershipRun(t, testRootlessMapping)
	writeTestFile(t, filepath.Join(os.Getenv("HOME"), ".claude", ".credentials.json"), fakeCredentials(fakeOAuth("r0", 100, "")))
	run.jr.d.claudeLogins = newClaudeLoginSync(func(context.Context, string, string, claudelogin.ExchangeRequest) (claudelogin.ExchangeResult, error) {
		run.log.add("exchange")
		return claudelogin.ExchangeResult{}, nil
	}, nil, nil)
	var registered *claudeLoginJob
	run.sup.duringRun = func() {
		registered = run.jr.d.claudeLogins.jobs["a1"]
		if registered == nil {
			t.Error("the job login was not registered after the hand-off")
			return
		}
		registered.lastExchange = registered.lastExchange.Add(-claudeLoginExchangeInterval)
		run.jr.d.claudeLogins.tick(context.Background())
	}
	stoppedAtReclaim := false
	run.ownership.onReclaim = func(isolation.OwnershipRequest) {
		if registered != nil {
			registered.mu.Lock()
			stoppedAtReclaim = registered.stopped
			registered.mu.Unlock()
		}
	}
	run.execute(t)
	run.expectCalls(t, "handoff:job start exchange remove:a1 reclaim:job")
	if !stoppedAtReclaim {
		t.Fatal("login exchanges were still running when the job home was reclaimed")
	}
}
