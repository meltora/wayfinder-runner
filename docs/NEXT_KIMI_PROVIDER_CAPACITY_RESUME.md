# Next Slice: Provider-Capacity Resume Flow

## Goal

Close the gap between:

- the runner detecting provider quota or credit exhaustion, and
- the system safely resuming the same feature attempt after the provider resets.

The runner already detects provider-capacity failures and emits a `provider_capacity_exhausted` event. The next slice is to make that state resumable, inspectable, and safe to continue without duplicating work or losing the provider session context.

## Why This Is Next

This is the highest-leverage unfinished path in the runner:

- the provider adapters already support resume session IDs,
- the job contract already includes `resume_session_id`, `continuation_count`, and `provider_capacity_resume_policy`,
- the daemon already normalizes quota failures and preserves partial build output,
- but the end-to-end continuation lifecycle is not yet hardened enough to treat quota exhaustion as a first-class paused state.

Without this, Kimi, Claude, or Codex can stop in the middle of a valid feature implementation and the operator still has to reconstruct how to continue.

## Product Rules

- The runner finishing a job does not mark the feature done.
- A merge does not mark the feature done.
- Only an authorized user action in the control plane marks the feature done.
- The runner must never autonomously change workflow state outside its authenticated contract with the control plane.
- Automatic resume is a policy choice, not a default. `manual` remains the safe default.

## Architectural Direction

Treat provider-capacity exhaustion as a durable continuation point, not as an ordinary failure.

That means:

- preserve enough state for a safe resume,
- expose that state locally for diagnosis,
- make the continuation contract explicit,
- and keep the control plane authoritative for whether and when the next attempt starts.

## Runner Scope

### 1. Persist a durable provider-capacity wait record

Add a runner-owned durable record for the latest capacity-stop state. It should survive restart and be cleared only when the job is explicitly resumed, cancelled, superseded, or fully completed.

Suggested fields:

- `job_id`
- `attempt_id`
- `fencing_token`
- `provider`
- `adapter`
- `model`
- `reason`
- `message`
- `resume_policy`
- `retry_at`
- `provider_session_id`
- `continuation_count`
- `checkpoint_attempt_id`
- `recorded_at`

Requirements:

- one active record per current waiting attempt
- reject malformed or stale state on load
- never trust host-global provider state instead of the recorded attempt state

### 2. Expose waiting state in local status surfaces

Update `our-runner status` and any existing daemon status payloads so an operator can clearly see:

- the runner is idle vs paused vs busy vs waiting for provider capacity
- which job and attempt are blocked
- whether resume policy is `manual` or `automatic`
- whether a `retry_at` hint exists
- whether a provider session ID was captured

This should make recovery observable without opening the SQLite journal directly.

### 3. Harden continuation semantics

A resumed attempt must be treated as a continuation of prior work, not a fresh unrelated run.

Requirements:

- when a continuation command arrives without `resume_session_id`, the daemon should keep using the latest checkpoint session backfill behavior
- if both a durable capacity-wait record and a continuation command exist, they must agree on job identity
- stale or mismatched continuation requests must fail closed
- continuation must increment `continuation_count`
- continuation must not re-run a completed attempt under the same durable identity

### 4. Publish richer capacity-failure facts

The control plane needs enough information to schedule or approve the resume.

Make sure the authoritative event path exposes:

- `resume_policy`
- `retry_at`
- `provider_session_id`
- `continuation_count`

If the separate `provider_capacity_exhausted` event is the only authoritative place for these fields, document that clearly and keep it stable. If the terminal `job_finished` event is also expected to drive automation, include the same facts there.

### 5. Define the resume contract explicitly

Document and test the exact continuation contract expected from the control plane.

At minimum:

- control plane requeues with a new `attempt_id`
- carries forward `job_id`
- increments `continuation_count`
- passes `resume_session_id` when available
- keeps the approved plan revision and repository set stable unless a human explicitly changes them

The runner should not silently guess across incompatible job shapes.

## Control-Plane Dependency

Automatic resume should be controlled by the control plane, not by a local runner timer.

Reason:

- workflow policy belongs in the control plane
- operators need visibility and override controls
- the control plane is where `manual` vs `automatic` policy should be enforced consistently across companies and teams

So for this slice, the runner should emit and persist the facts required for resume, but the control plane should decide whether to:

- wait for `retry_at` and enqueue the continuation automatically, or
- require a human to approve resume

## Files Kimi Should Likely Touch

- `internal/daemon/job.go`
- `internal/daemon/daemon.go`
- `internal/daemon/provider_output_test.go`
- `internal/daemon/build_integration_test.go`
- `cmd/our-runner/main.go`
- `cmd/our-runner/main_test.go`
- `api/runner/v1/jobs.go`
- `api/runner/v1/provider_capacity.go`
- `internal/journal/journal.go` or a new focused durable-state file if journal reuse is wrong

If the new state shape gets non-trivial, prefer a small dedicated file over expanding unrelated files further.

## Tests Required

### Unit

- durable wait record save, load, clear, reject malformed
- status output reflects waiting-for-capacity state
- continuation request mismatch fails closed
- default resume policy remains `manual`

### Integration

- provider-capacity exhaustion records waiting state and preserves partial work
- restart after capacity exhaustion keeps the waiting state visible
- continuation with resume session starts the provider in resume mode
- continuation without explicit resume session backfills from latest checkpoint when safe
- stale continuation request is rejected
- successful continuation clears the waiting record

### Regression

- ordinary failures do not create waiting-for-capacity state
- generic rate limiting is not misclassified as billing-reset exhaustion
- duplicate capacity events do not create duplicate waiting records

## Non-Goals For This Slice

- no local autonomous runner-side scheduler that starts jobs on its own timer
- no new workflow builder UI
- no feature-done state changes
- no broad refactor of the entire command protocol

## Acceptance Criteria

- a quota-exhausted build attempt becomes a durable, inspectable waiting state
- the runner restart path preserves that state
- the control plane has enough stable data to decide resume behavior
- a continuation attempt safely resumes prior provider context when allowed
- mismatched or stale continuation inputs fail closed
- tests cover the full quota-stop to continuation path

## Kimi Prompt

```text
Implement the next runner slice for provider-capacity continuation in /Users/omaririskic/Code/Automated Development/app-runner.

Objective:
Treat provider quota exhaustion as a durable waiting state that can be safely resumed later by the control plane.

Constraints:
- Follow AGENTS.md and CLAUDE.md if present.
- Prefer many small focused files over growing existing files.
- Do not mix unrelated types and logic into one large file.
- Security-first: fail closed on mismatched continuation state.
- Do not add a local autonomous scheduler that starts jobs by itself.
- Manual remains the default resume policy.
- Control plane remains authoritative for workflow progression.

Required outcomes:
1. Persist a durable waiting-for-provider-capacity record with the current attempt identity, resume policy, retry hint, provider session ID, and continuation context.
2. Surface that waiting state through local status output.
3. Harden continuation semantics so resumed attempts are validated against the stored waiting state and stale or mismatched resume requests are rejected.
4. Ensure the authoritative event path exposes enough data for the control plane to decide manual vs automatic resume.
5. Add focused unit and integration tests for quota-stop, restart visibility, and safe continuation.

Likely files:
- internal/daemon/job.go
- internal/daemon/daemon.go
- cmd/our-runner/main.go
- cmd/our-runner/main_test.go
- api/runner/v1/jobs.go
- api/runner/v1/provider_capacity.go
- internal/journal/journal.go or a new narrow durable-state file

Testing bar:
- add unit tests and integration coverage
- no regressions to existing provider-capacity classification
- no duplicate waiting records on duplicate events

Deliverable:
- code
- tests
- short summary of the continuation contract you implemented
```
