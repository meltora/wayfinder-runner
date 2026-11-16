package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/egresspolicy"
	"github.com/example/app-runner/internal/isolation"
)

const jobEgressRulesNotHeldMessage = "host egress rules were not held through the run"

var errJobEgressRulesNotHeld = errors.New(jobEgressRulesNotHeldMessage)

type jobEgressRulesLease interface {
	Failures() <-chan error
	Complete(context.Context) error
	Close() error
}

type jobEgressRulesAcquirer func(context.Context, egresspolicy.JobRulesRequest) (jobEgressRulesLease, egresspolicy.JobRules, error)

type jobEgressRulesState struct {
	lease   jobEgressRulesLease
	rules   *egresspolicy.JobRules
	failure error
	done    chan struct{}
}

func newJobEgressRulesAcquirer(cfg config.Config, attestation isolation.DockerNetworkAttestation) jobEgressRulesAcquirer {
	client := egresspolicy.NewClient(cfg.EgressPolicyVerifierSocket)
	return func(ctx context.Context, request egresspolicy.JobRulesRequest) (jobEgressRulesLease, egresspolicy.JobRules, error) {
		if err := isolation.VerifyDockerNetworkAttestation(ctx, attestation); err != nil {
			return nil, egresspolicy.JobRules{}, err
		}
		lease, rules, err := client.AcquireJobRules(ctx, request)
		if err != nil {
			return nil, egresspolicy.JobRules{}, err
		}
		return lease, rules, nil
	}
}

func (jr *jobRunner) acquireJobEgressRules(ctx context.Context, approvedHosts []string) {
	d := jr.d
	if d.cfg.Isolation != "docker" || d.cfg.JobNetwork != "restricted" || d.jobNetworkAttestation.ID == "" ||
		d.acquireJobEgressRules == nil {
		return
	}
	hosts := append([]string{}, approvedHosts...)
	acquireCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lease, rules, err := d.acquireJobEgressRules(acquireCtx, egresspolicy.JobRulesRequest{
		NetworkID: d.jobNetworkAttestation.ID, NetworkName: d.jobNetworkAttestation.Name,
		PolicyID: d.jobNetworkAttestation.PolicyID, ContainerName: isolation.ContainerName(d.runnerID, jr.job.AttemptID),
		ApprovedHosts: hosts,
	})
	if err != nil {
		jr.logger.Warn("job egress rules are not verified; approved hosts stay denied at host egress",
			"approved_hosts", len(hosts), "err", err)
		return
	}
	state := &jobEgressRulesState{lease: lease, rules: &rules, done: make(chan struct{})}
	jr.procMu.Lock()
	jr.egressRules = state
	jr.procMu.Unlock()
	jr.logger.Info("job egress rules held", "enforcement_id", rules.EnforcementID, "rules_digest", rules.RulesDigest,
		"approved_hosts", len(rules.ApprovedHosts))
	go jr.watchJobEgressRules(state)
}

func (jr *jobRunner) watchJobEgressRules(state *jobEgressRulesState) {
	select {
	case err := <-state.lease.Failures():
		jr.procMu.Lock()
		if state.failure == nil {
			state.failure = fmt.Errorf("%w: %v", errJobEgressRulesNotHeld, err)
		}
		proc := jr.proc
		jr.procMu.Unlock()
		jr.logger.Error("job egress rules lease ended; stopping provider", "err", err)
		if proc != nil {
			_ = proc.Kill()
		}
	case <-state.done:
	}
}

func (jr *jobRunner) jobEgressRulesFailedLocked() bool {
	return jr.egressRules != nil && jr.egressRules.failure != nil
}

func (jr *jobRunner) releaseJobEgressRules(ctx context.Context) error {
	jr.procMu.Lock()
	state := jr.egressRules
	jr.procMu.Unlock()
	if state == nil {
		return nil
	}
	releaseCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	completeErr := state.lease.Complete(releaseCtx)
	jr.procMu.Lock()
	if state.failure == nil && completeErr != nil {
		state.failure = fmt.Errorf("%w: %v", errJobEgressRulesNotHeld, completeErr)
	}
	failure := state.failure
	jr.procMu.Unlock()
	jr.closeJobEgressRules()
	return failure
}

func (jr *jobRunner) closeJobEgressRules() {
	jr.procMu.Lock()
	state := jr.egressRules
	jr.procMu.Unlock()
	if state == nil {
		return
	}
	select {
	case <-state.done:
		return
	default:
		close(state.done)
	}
	_ = state.lease.Close()
}

func (jr *jobRunner) reportedEgressRules() *v1.RunEgressRules {
	if !jr.job.RunGuardrails.ReportsEgressRules() {
		return nil
	}
	jr.procMu.Lock()
	defer jr.procMu.Unlock()
	if jr.egressRules == nil || jr.egressRules.rules == nil || jr.egressRules.failure != nil {
		return nil
	}
	rules := jr.egressRules.rules
	return &v1.RunEgressRules{
		EnforcementID: rules.EnforcementID, RulesDigest: rules.RulesDigest,
		ApprovedHosts: append([]string{}, rules.ApprovedHosts...), ApprovedHostsDigest: rules.ApprovedHostsDigest,
	}
}
