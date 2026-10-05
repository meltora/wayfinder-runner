package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/collector"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/identity"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/lease"
)

func swapRaceDaemon(t *testing.T, sink func(context.Context, any) error) *Daemon {
	t.Helper()
	stateDir := t.TempDir()
	cfg := reloadTestConfig(stateDir)
	cfg.Isolation = "direct"
	cfg.AllowUnsafeDirect = true
	cfg.JobImage = ""
	d := &Daemon{
		cfg:        cfg,
		store:      identity.NewStore(stateDir),
		runnerID:   "runner-1",
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		leases:     lease.NewManager(),
		collectors: collector.NewStore(stateDir),
		eventSink:  sink,
	}
	j, err := journal.Open(filepath.Join(stateDir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	d.journal = j
	return d
}

func swapRaceJob(index int) v1.JobSpec {
	return withTestAuthority(v1.JobSpec{
		JobID: fmt.Sprintf("job-swap-%d", index), AttemptID: fmt.Sprintf("attempt-swap-%d", index), Kind: v1.JobKindBuild,
		FencingToken: uint64(index + 1), LeaseExpiresAt: time.Now().Add(time.Hour),
		ApprovedPlanRevision: 1,
		Repositories:         []v1.JobRepository{{RepositoryID: "repo-unregistered", Primary: true}},
		Ticket:               v1.TicketContext{Key: "T-1", Summary: "summary", Description: "description"},
	})
}

func waitForNoActiveJob(t *testing.T, d *Daemon) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		d.jobMu.Lock()
		active := d.activeJob
		d.jobMu.Unlock()
		if active == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the started job did not retire")
}

func TestJobsStartedDuringHotSwapsKeepOneWholeConfiguration(t *testing.T) {
	var holding atomic.Bool
	entered := make(chan struct{}, 1)
	d := swapRaceDaemon(t, func(ctx context.Context, _ any) error {
		if !holding.Load() {
			return nil
		}
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	base := d.cfg
	next := hotReload(base, strings.Repeat("e", 64), nil).Merged.Config
	documents := []struct {
		cfg    config.Config
		digest string
	}{{base, strings.Repeat("d", 64)}, {next, strings.Repeat("e", 64)}}
	var wholes []hotConfiguration
	for _, document := range documents {
		if err := d.applyHotConfiguration(document.cfg, document.digest); err != nil {
			t.Fatal(err)
		}
		wholes = append(wholes, d.hotConfiguration())
	}
	if reflect.DeepEqual(wholes[0], wholes[1]) {
		t.Fatal("the two documents are indistinguishable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var swaps atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for index := 0; ctx.Err() == nil; index++ {
			document := documents[index%2]
			if err := d.applyHotConfiguration(document.cfg, document.digest); err != nil {
				t.Error(err)
				return
			}
			swaps.Add(1)
		}
	}()
	seen := map[string]int{}
	const jobs = 40
	for index := range jobs {
		holding.Store(true)
		if err := d.startJobWithReviewFeedbackContinuation(context.Background(), swapRaceJob(index), nil, nil); err != nil {
			cancel()
			wg.Wait()
			t.Fatalf("job %d: %v", index, err)
		}
		<-entered
		d.jobMu.Lock()
		jr := d.activeJob
		d.jobMu.Unlock()
		started := *jr.configuration
		if !reflect.DeepEqual(started, wholes[0]) && !reflect.DeepEqual(started, wholes[1]) {
			cancel()
			wg.Wait()
			t.Fatalf("job %d started with a torn configuration: %+v", index, started)
		}
		seen[started.digest]++
		holding.Store(false)
		jr.cancel()
		waitForNoActiveJob(t, d)
	}
	cancel()
	wg.Wait()
	if swaps.Load() < jobs {
		t.Fatalf("only %d swaps ran during %d job starts", swaps.Load(), jobs)
	}
	t.Logf("jobs per digest: %v, swaps: %d", seen, swaps.Load())
}
