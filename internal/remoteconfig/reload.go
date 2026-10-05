package remoteconfig

import (
	"context"
	"errors"
	"sync"
)

type ConditionalFetchFunc func(ctx context.Context, runningDigest string) ([]byte, error)

type ReloadOutcome int

const (
	ReloadUnchanged ReloadOutcome = iota
	ReloadRestartRequired
	ReloadApply
)

type Reload struct {
	Outcome     ReloadOutcome
	Delivery    Delivery
	Merged      Merged
	Raw         []byte
	RestartKeys []string
}

type Reloader struct {
	stateDir string
	runnerID string
	local    Local
	fetch    ConditionalFetchFunc

	mu          sync.Mutex
	running     Delivery
	conditional bool
}

func NewReloader(stateDir, runnerID string, local Local, running Delivery, fetch ConditionalFetchFunc) *Reloader {
	return &Reloader{stateDir: stateDir, runnerID: runnerID, local: local, running: running, fetch: fetch, conditional: true}
}

func (r *Reloader) RunningDigest() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running.Digest
}

func (r *Reloader) Check(ctx context.Context) (Reload, error) {
	r.mu.Lock()
	running := r.running
	sentDigest := ""
	if r.conditional {
		sentDigest = running.Digest
	}
	r.mu.Unlock()
	raw, err := r.fetch(ctx, sentDigest)
	if errors.Is(err, ErrNotModified) {
		return Reload{Outcome: ReloadUnchanged}, nil
	}
	if err != nil {
		return Reload{Outcome: ReloadUnchanged}, err
	}
	delivery, err := ParseDelivery(raw, r.runnerID)
	if err != nil {
		r.setConditional(false)
		return Reload{Outcome: ReloadUnchanged}, err
	}
	if delivery.Digest == running.Digest {
		r.setConditional(true)
		return Reload{Outcome: ReloadUnchanged}, nil
	}
	if keys := RestartRequiredChanges(running.Remote, delivery.Remote); len(keys) > 0 {
		return Reload{Outcome: ReloadRestartRequired, Delivery: delivery, RestartKeys: keys}, nil
	}
	merged, err := Merge(r.local, delivery)
	if err != nil {
		r.setConditional(false)
		return Reload{Outcome: ReloadUnchanged}, err
	}
	return Reload{Outcome: ReloadApply, Delivery: delivery, Merged: merged, Raw: raw}, nil
}

func (r *Reloader) Commit(reload Reload) error {
	if reload.Outcome != ReloadApply {
		return errors.New("only an applied runner configuration can be committed")
	}
	r.mu.Lock()
	r.running = reload.Delivery
	r.conditional = true
	r.mu.Unlock()
	return SaveCachedDelivery(r.stateDir, reload.Raw)
}

func (r *Reloader) Refuse(Reload) {
	r.setConditional(false)
}

func (r *Reloader) setConditional(conditional bool) {
	r.mu.Lock()
	r.conditional = conditional
	r.mu.Unlock()
}

func RestartRequiredChanges(running, next Remote) []string {
	var keys []string
	for _, field := range []struct {
		name    string
		changed bool
	}{
		{"isolation", running.Isolation != next.Isolation},
		{"allow_unsafe_direct", running.AllowUnsafeDirect != next.AllowUnsafeDirect},
		{"provider", running.Provider != next.Provider},
		{"job_image", running.JobImage != next.JobImage},
		{"job_network", running.JobNetwork != next.JobNetwork},
		{"job_memory_mb", running.JobMemoryMB != next.JobMemoryMB},
		{"job_cpus", running.JobCPUs != next.JobCPUs},
		{"job_pids", running.JobPIDs != next.JobPIDs},
	} {
		if field.changed {
			keys = append(keys, field.name)
		}
	}
	return keys
}
