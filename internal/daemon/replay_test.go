package daemon_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/daemon"
	"github.com/example/app-runner/internal/journal"
	"github.com/example/app-runner/internal/mockcp"
)

func TestUnackedEventsReplayedOnConnect(t *testing.T) {
	cp, err := mockcp.NewServer()
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := cp.Start(context.Background()); err != nil {
		t.Fatalf("cp.Start: %v", err)
	}
	defer cp.Close()

	stateDir := t.TempDir()
	enrollRunner(t, cp, stateDir)

	j, err := journal.Open(stateDir + "/journal.db")
	if err != nil {
		t.Fatalf("journal.Open: %v", err)
	}
	env := v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeEvent, ID: "evt-offline-1",
		SentAt: time.Now(), Payload: map[string]any{"kind": "offline_test"},
	}
	raw, _ := json.Marshal(env)
	if err := j.Append(context.Background(), journal.Record{
		ID: env.ID, Kind: journal.KindEvent, Payload: raw, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("journal.Append: %v", err)
	}
	_ = j.Close()

	cfg := directRunnerConfig(cp.WSURL(), stateDir)
	d, err := daemon.New(cfg, nil)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	waitFor(t, "offline event replayed", 10*time.Second, func() bool {
		for _, e := range cp.Events() {
			if e.ID == "evt-offline-1" {
				return true
			}
		}
		return false
	})
	cancel()
}
