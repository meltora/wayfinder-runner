# Job-requested preview deploy v1

This is the frozen contract between the runner and Meltora for P5f. It covers how a build job's agent deploys a preview of its own commits, and how the runner publishes the final preview evidence. It builds on the P5d hosted deploy (`preview_deploy` command, `preview_deploy_result` event), on P5d-L (hosted lifecycle), and on the P5e tunnel (`preview-tunnel-v1.md`). Agreed with the app side on 2026-11-25 (R1: request, then command).

## J1 Tool

- **Name and availability.** The agent tool is `platform_deploy_preview`. It is served only in build jobs; a plan job gets the normal unknown-tool refusal. Its input is an empty object: the agent cannot choose a URL, a commit, a blueprint or a target.
- **On call, the runner does the following in order:**
  1. Resolves the job's current commit set with the same rules as `platform_publish_preview`.
  2. Records the commit set as trusted.
  3. Sends `preview_deploy_requested` (J2).
  4. Waits for the result (J3).
- **One open request per job.** A second call while a request is open fails locally with `request_in_flight`, without sending an event.
- **Success reply:** `{"status":"deployed","url":"<internal url>","expires_at":"<RFC3339 ms>","commit_set":{…}}`.
  - The internal URL is reachable only from this job's container (J5) and is never the edge URL.
- **Failure reply:** `{"status":"refused"|"failed"|"timeout","code":"<code>"}`.
  - `refused` carries a J3 rejection code.
  - `failed` carries the `preview_deploy_result` failure code.
  - `timeout` means no command arrived in time. The deploy may still finish later.

## J2 Event `preview_deploy_requested`

The event is manifest-bound like the other job events.

| Field | Value |
|---|---|
| `schema_version` | `1` |
| `job_id`, `lease_id`, `fencing_token` | The job's current lease. |
| `execution_manifest_id`, `execution_manifest_digest` | The job's execution manifest. |
| `request_id` | `"preview-deploy-request-" + hex(sha256(job_id + "\0" + lease_id + "\0" + decimal(sequence)))`. The sequence is per job and lease, starts at 1 and increases by 1 per sent request. |
| `commit_set` | P5d rules: 1–16 entries, ascending `repository_id`, each value a 40-character lowercase hex sha. |
| `commit_set_digest` | The P5d commit set digest of `commit_set`. |
| `requested_at` | RFC3339 UTC with milliseconds. |

- Event id: `"preview-deploy-requested-" + hex(sha256(request_id))`. Resending the same request reuses the same event id.
- The runner sends at most 6 requests per job. The 7th call fails locally with `deploy_limit_reached`. The app enforces the same limit.

## J3 Correlation and outcome

- **Acceptance.** The app answers an accepted request by sending the normal P5d `preview_deploy` command to the same runner. Its `idempotency_key` is `"preview-deploy-" + hex(sha256("job-request" + "\0" + request_id))`.
  - The P5d command wire format and digests are unchanged.
  - The runner matches the command to the open request by that key. It treats the command as the request's answer only when `workflow_run_id`, `execution_manifest_id` and `commit_set_digest` also equal the job's.
  - A command that matches the key but not those fields still runs through the normal P5d path, but it does not answer the tool.
- **Rejection.** The app rejects a request through the existing event rejection path, with one of these permanent codes:
  - `not_build_job`
  - `no_approved_blueprint`
  - `commit_set_mismatch`
  - `request_in_flight`
  - `deploy_limit_reached`
  - `runner_capability_missing`
  - `emergency_stop_active`

  The standard job event codes can also arrive: `lease_not_found`, `lease_not_active`, `lease_expired`, `delivery_conflict` and `decision_expired`. For these, and for the seven codes above, the runner ends the wait at once and returns `refused` with the code passed through verbatim. Any other final rejection code is returned as `refused` with code `rejected`.
- **Retryable rejection.** `timestamp_out_of_window` is not final. The runner handles it the same way as for the other job events and keeps waiting. The 2-minute timeout below still applies.
- **Result.** The tool returns when the runner's own `preview_deploy_result` for the matched command is final:
  - succeeded: `deployed`
  - any failure: `failed`
- **Timeouts.**
  - If neither a command nor a rejection arrives within 2 minutes of the event being sent, the tool returns `timeout` and the request is closed. A command that arrives later still runs through the normal P5d path.
  - The whole wait is also bounded by the job lease and by the deploy itself; the P5d deploy timeout applies.
  - Job cancellation ends the wait.
- **Expiry.** The app sets the job preview's `expires_at` to min(blueprint max lifetime, job lease expiry + 1 h). The runner applies the command's value unchanged.

## J4 Revise rounds and destroy

- The runner does not destroy earlier job previews on its own.
- When a newer deploy from the same job succeeds, the app sends the P5d-L hosted `destroy` for the previous one. No new runner destroy reason is added.
- Expiry, emergency stop and the existing destroy paths behave as in P5d. Destroying a preview resets its tunnel streams (P5e T4).

## J5 Agent reach (REQ-F3)

- The internal URL is `http://our-preview:8080/`. It reaches the preview's gateway ingress, the same ingress that the tunnel serves.
- To provide it, the runner attaches the job container and the preview gateway to a per-job Docker network with `internal: true`.
  - The gateway answers on that network under the alias `our-preview`, on port 8080 only.
  - The gateway's egress proxy accepts connections only from the preview's own network, never from the job network.
  - The job container gains no other route.
- When the job ends, or a newer job preview replaces the old one, the runner removes the job network.
- None of this is visible on the wire.

## J6 Final evidence

- **When.** At the end of a build job (`finalizeBuild`), after the final commit set is recorded and before review-ready, the runner publishes preview evidence only if this job has deployed at least one preview.
  - If the latest deployed preview from this job does not have the final commit set's digest, the runner first requests a deploy of the final commit set (J2, counted toward the limit of 6) and waits for it (J3).
  - If that deploy does not succeed, no evidence is published. The build itself still finishes, and the failure is logged.
- **What.** The evidence is the existing `preview` event:
  - `url`: `https://<edge_host>`, taken from the deploy command, never from the agent.
  - `commit_set`: the final commit set in the `preview` event's existing object form, `{"<repository_id>": "<40-hex sha>"}` with 1–16 entries. This is not the ordered array form used by J2 and the P5d command. The app canonicalises it by sorting on `repository_id` before it compares the digest with the review-ready commit set.
  - `expires_at`: from the deploy command.
  - `health`: `"healthy"`. It is derived from the runner's own succeeded `preview_deploy_result`, never from the agent, because no evidence is sent without one.
  - `known_limitations`: `[]`.
- **App checks.** The app accepts the event only when it names a deployed, unexpired preview from this job whose `commit_set_digest` equals the review-ready commit set. The app ignores `health` for readiness. `create_preview` then closes through the built-in observation (REQ-F4). An agent-claimed health never satisfies readiness.

## J7 Capability

No new capability field. The tool is offered only when the runner advertises `preview_hosting` and `preview_tunnel_transport_version: 1`. Otherwise the tool is not listed, and a call fails with `runner_capability_missing` without sending an event.
