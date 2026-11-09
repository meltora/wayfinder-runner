# Runner check image digest follow-ups: classic image store reference and lowercase digests

## Control

| Field                  | Value |
| ---------------------- | ----- |
| ID                     | RDBC follow-ups FU-9 and the uppercase-hex pin (RCID) |
| Kind                   | bug (operator-facing: the reference Go check image prints a reference that does not resolve on a classic image store; an uppercase check image digest passes validation and fails only at start) |
| Status                 | review_ready |
| Risk                   | medium. The slice changes which check image references the runner accepts at load, what the reference build script prints, the start gate and doctor messages, and the case of the digest written into the provenance artifact. It does not change which image a check runs in, the container restrictions, the pull policy, the remote configuration document or the control-plane protocol. Every change is a tightening or a clearer message, and each failure mode stays closed. A defect could mislead an operator, or refuse a configuration that works today. |
| Complexity             | standard. Small changes across config validation, `isolation.InspectImage`, the gate, doctor, provenance, one shell script, a shell test with a fake `docker`, and one gated test on a second Docker daemon. The classic-store behaviour was measured by the planner (below), so the main uncertainty is removed. |
| Parent scope           | `runner-docker-build-checks.md` (RDBC, `done`): FU-9 (review decision I-9, "Follow-ups"), and the non-blocking point "Uppercase hex in a check image passes config validation but fails closed at the gate" (repair attempt 1 observations, I-4 note in the first review). RDBC REQ-05 (check image validation), REQ-07 (start gate and doctor), REQ-08 (provenance v2), REQ-09 (reference Go image), REQ-12 (docs). App: PRODUCT.md "Workspace composition" item 2 (a pinned project toolchain layer). |
| Product sources        | App `PRODUCT.md` "Workspace composition" item 2; isolation baseline. Owner decisions LC-1 and LC-2 (checks are defined only on the runner host), recorded in app `docs/development/runner-hermetic-check-workspace.md`. |
| Planner                | Opus 5.5 (`claude-opus-5-5`); develop-planner subagent, RCID planning session, 9 November 2026 |
| Implementer            | Opus 5.5 (`claude-opus-5-5`); develop-implementer subagent, RCID implementation session, 9 November 2026 |
| Reviewer               | Opus 5.5 (`claude-opus-5-5`), fresh context; not yet assigned |
| Repair attempt         | 0 |
| Updated                | 2026-11-09 |
| Baseline               | app-runner `main` at `67eeac7` ("chore(docs): record RDBC REQ-14 live Docker Build on KAN-25 and set the record done"), worktree `.claude/worktrees/rdbc-digest-followups`, branch `rdbc-digest-followups`. No pre-existing dirty changes. This record is the only change. App read at `main` `abf145dd`, read only; no app change is planned. |
| Blocker / resume state | None. Coordinator decisions on Q-1 to Q-3 are recorded under "Owner questions". |

## Outcome and current gap

**Outcome.** An operator on any supported Docker host (containerd image store or classic image store) who builds the reference Go check image gets either a pinned reference that is proven to resolve on that host, or a clear refusal that says what to do. A check image whose digest is not lowercase is refused when the configuration loads, with the pinning message, not at start with a Docker parse error. Every digest that the runner writes for the control plane is lowercase, and nothing that the app parses changes.

**Current code at `67eeac7` (evidence):**

- `packaging/check-image-go/build.sh` builds with `--iidfile` and prints `pinned reference: $IMAGE@$IMAGE_ID` (last line). It never checks that this reference resolves. `$IMAGE_ID` is the image ID: the config digest on the classic store, and the manifest (list) digest on the containerd store.
- `packaging/job-image/build.sh` already has a `--push` path: it pushes, picks the `RepoDigests` entry for `${IMAGE%:*}@sha256:` and prints `pinned:`. The check image script has no such path.
- `internal/config/config.go:525` `immutableImagePattern = ^[^\s@]+@sha256:[a-fA-F0-9]{64}$` is shared by `job_image` (`config.go:284`) and check images (`check_validation.go:15`). Uppercase hex is accepted for both. `internal/config/check_image_config_test.go:17` asserts that an uppercase check image is accepted.
- `internal/isolation/image_inspect.go`: `InspectImage` maps "No such image" to `ErrImageNotPresent`. Any other Docker error, including "invalid checksum digest format", becomes `inspect image: …`. `pinnedDigestPattern` accepts `[a-fA-F0-9]`, and `ImageDigest` returns the digest with its original case. `ImageDigest` feeds provenance `check_image` (`internal/daemon/check_install_provenance.go:81-91`, with fallback to `job_image` when a check has no image) and the doctor short digest (`internal/doctor/check_image_check.go:58`).
- `internal/daemon/check_image_gate.go` returns "check <id>: image <ref> is not present on the runner's Docker host; pull or build it before starting" for `ErrImageNotPresent`, and "check <id>: verify image <ref>: <err>" otherwise. Doctor (`check_image_check.go`) uses the same two cases.
- README "checks.<id>.image" section (around lines 1000-1050): the YAML example says `<image ID printed by build.sh>`, and the build instruction says to copy the printed pinned reference. Neither mentions the image store. `docs/TESTING.md` lines 136-161 describe `CHECK_IMAGE` as `<tag>@sha256:<image ID>`.

**Planner measurements, 9 November 2026 (read only, Docker 29.6.1 on this host):**

| Daemon | Store | Command | Result |
| ------ | ----- | ------- | ------ |
| rootless `unix:///run/user/1000/docker.sock` | containerd (`driver-type io.containerd.snapshotter.v1`) | `docker image ls --no-trunc` | `our-runner-check-go:rdbc` ID `sha256:dce6fc60…7bc67`, digest the same. The ID equals the manifest digest, so `name@<ID>` resolves (RDBC live run). |
| rootful `unix:///var/run/docker.sock` | classic (`overlay2` graph driver) | `docker image inspect -- esm-email@sha256:<its ID>` (locally built, `RepoDigests` empty) | `No such image: esm-email@sha256:ebc0ad52…` |
| rootful, classic | | `docker image inspect -- esm-email:latest@sha256:<its ID>` | `No such image` |
| rootful, classic | | `docker image inspect -- alpine@sha256:be265606…` (its repo digest; its ID is `sha256:b9284176…`) | resolves to `sha256:b9284176…` |
| rootful, classic | | `docker image inspect -- alpine@sha256:BE265606…` (uppercase) | `invalid checksum digest format` |

So FU-9 is confirmed. On the classic store, `build.sh` prints a reference that does not resolve. The start gate then says "not present on the runner's Docker host; pull or build it", which is wrong: the image is present, and rebuilding it does not help. Only a repository digest, which exists after a push or a pull, resolves by `name@digest` on the classic store. Uppercase hex is refused by Docker itself, so today it can only fail closed, at start, with a Docker parse error.

**App side (read only at `abf145dd`).**

- Provenance: `git grep` for `check_image`, `check-install.v` and `CheckImage` under `src` finds only test support. `src/server/planning-manifests/test-support/go-build-upload-evidence.go:24` expects `our-runner.check-install.v2` (commit `20b946d1`). Its `buildCheckProvenance` struct reads `schema`, `job_id`, `attempt_id`, `check_id` and `carried_directories`. It does not read or compare `check_image`. No production code parses the provenance document.
- Check images never reach the app; they are host-local (LC-1, LC-2). The app never sees a check image reference, only the digest inside the provenance artifact, which it stores as an opaque artifact.
- Job image: the app validates the remote document's `job_image` with `RUNNER_CONFIGURATION_JOB_IMAGE_PATTERN = /^[^\s@]+@sha256:[a-fA-F0-9]{64}$/u` (`src/shared/schemas/runner-configuration-profile.schema.ts:19-20`, used by `runner-remote-configuration-document.schema.ts:41` and `runner-enroll-choices.schema.ts`). This is the same permissive pattern as the runner's `immutableImagePattern`. The `defaultJobImage` patterns (`runner-configuration-profile.schema.ts:188`, `runner-enrollment.schema.ts:165`, `src/lib/env/runner-release-settings.ts:13`) are lowercase only.
- Conclusion: the app does not parse or compare check image digests, so this slice needs no app change, and no app worktree was created. The two repositories agree on `job_image` today because both accept uppercase. Tightening `job_image` must happen in both repositories together, so it is excluded here (FU-A, Q-2).

**What exists versus what is only described.** Exists: the check image field and its validation, the start gate, the doctor row, provenance v2 and the reference build script (containerd store only, in practice). Described only: a resolvable reference on the classic store; refusing non-canonical digests at load.

## Scope

**In scope (app-runner only):**

1. FU-9. `packaging/check-image-go/build.sh` prints a `pinned reference` only after it proves that the reference resolves on the build host to the built image. On the classic store it gains a `--push` path that prints a repository digest. Without `--push` on the classic store it refuses, with instructions.
2. FU-9. The start gate and doctor report the classic-store case accurately, instead of "not present … pull or build it".
3. Uppercase hex. Check image references must use a lowercase digest, refused at load with the pinning message. Every digest that the runner derives from a reference (provenance `check_image`, doctor short digest) is lowercase.
4. README and `docs/TESTING.md` updates; a shell test for the build script with a fake `docker`; a gated test on a classic-store daemon.

**Out of scope:**

- Tightening `job_image` validation to lowercase in the runner or the app (FU-A, Q-2). `immutableImagePattern` and the `job_image` rule stay byte-identical.
- Running a check by bare image ID (`sha256:<id>`), or resolving a tag to an ID at start. The runner keeps running checks by the configured `name@sha256` reference, with `--pull=never`.
- Any pull by the runner, any registry credential handling, or a registry for tests. `--push` runs only when the operator passes it to the build script.
- Pushing during this slice's verification to any real registry (the shell test uses a fake `docker`).
- The job image build script (`packaging/job-image/build.sh`), which already prints a pushed registry digest.
- Any app repository change, the remote configuration document, the dashboard and the protocol.
- RDBC FU-1 to FU-6, O-1 and O-2.

**Repositories touched:** app-runner only.

## Acceptance contract

| ID | Source | Requirement (observable) | Verification |
| -- | ------ | ------------------------ | ------------ |
| REQ-01 | FU-9, I-9 | **Classic-store behaviour confirmed by test.** A gated Go test (`RUN_DOCKER_CLASSIC_STORE_PROBE=1`, `CLASSIC_DOCKER_HOST=<socket>`) builds a throwaway image `FROM scratch` with one small file and no network, tagged `our-runner-classic-probe:<random>` with a unique label, on that daemon. It records the image ID and asserts: `name:tag@<ID>` does not resolve (`InspectImage` returns the REQ-05 sentinel), and `sha256:<ID>` alone does resolve to the same ID. It removes the image in `t.Cleanup` and fails if any image with the label remains. The same test, when `DOCKER_HOST` names a containerd-store daemon (`RUN_DOCKER_SANDBOX_PROBE=1`), asserts that `name:tag@<ID>` resolves to the ID. If the classic-store result differs from the planner measurement, the implementer stops and reports before REQ-02 to REQ-05. | Gated test output pasted in this record, for both daemons |
| REQ-02 | FU-9 | **The build script prints only a reference that resolves.** Usage becomes `build.sh IMAGE_TAG [--push]`; any other argument prints usage and exits 2. After the existing build, `go version` and setuid checks, the script runs `docker image inspect --format '{{.Id}}' -- "$IMAGE@$IMAGE_ID"`. When that succeeds and prints exactly `$IMAGE_ID`, and `--push` was not given, it prints `pinned reference: $IMAGE@$IMAGE_ID` as today and exits 0. When it does not resolve and `--push` was not given, it prints no `pinned reference` line, keeps the `local image ID` line, and fails (exit 1) with: "Go check image build failed: this Docker host's image store does not resolve a locally built image by digest (classic image store); rerun with --push to push IMAGE_TAG to its registry and print a repository digest, or use a Docker host with the containerd image store". The image stays built and tagged. | Shell test (REQ-04) |
| REQ-03 | FU-9 | **`--push` prints a resolvable repository digest.** With `--push`, after the checks above, the script runs `docker push "$IMAGE"` (a failure fails the script). It then takes the first `RepoDigests` entry that starts with `${IMAGE%:*}@sha256:`, requires it to match `[^[:space:]@]+@sha256:[a-f0-9]{64}`, and requires `docker image inspect --format '{{.Id}}' -- <entry>` to print exactly `$IMAGE_ID`. Only then does it print `pinned reference: <entry>`. A missing entry, a malformed entry or an ID mismatch fails with a message that names the cause and prints no `pinned reference` line. A repository with a registry port (`host:5000/name:tag`) selects the right entry. The script never pulls, and never pushes without `--push`. On a containerd-store host `--push` uses the same path and prints the repository digest. | Shell test (REQ-04) |
| REQ-04 | FU-9, AGENTS.md | **Shell test.** New `packaging/tests/check_image_go_build_test.sh` follows the existing `packaging/tests/*_test.sh` style (stub binaries in a temporary `PATH`, a call log, `fail()`, and the system tools linked in). It runs the real `build.sh` against a fake `docker` and covers these cases: (a) containerd, no push: prints the pinned reference and exits 0; (b) classic, no push: exit 1, the REQ-02 message, no `pinned reference` line, no `push` call; (c) classic, `--push`: prints the matching repository digest; (d) a `RepoDigests` list with an entry for another repository first and the matching one second: the matching one is printed; (e) a registry with a port; (f) push fails: exit 1; (g) no matching entry: exit 1; (h) an entry whose inspected ID differs: exit 1; (i) uppercase hex in the entry: exit 1; (j) an unknown second argument: exit 2. It asserts that the log has no `pull` call in any case. The test is added to both `sh -n` lists (README and `docs/TESTING.md`) and to the documented shell test commands. | `sh packaging/tests/check_image_go_build_test.sh` output |
| REQ-05 | FU-9, RDBC REQ-07 | **Accurate gate and doctor on the classic store.** `isolation.InspectImage(ctx, ref)`: when the pinned reference is not found and `ref` matches `name@sha256:<64 lowercase hex>`, it runs a second inspection of the bare `sha256:<hex>`. If that prints exactly the same `sha256:<hex>`, it returns a new sentinel `ErrImageResolvesOnlyByID`. Otherwise it returns `ErrImageNotPresent` as today. Any error from the second inspection other than "No such image" is returned as `inspect image: …`. The start gate then stops with exactly "check <id>: image <ref> does not resolve on the runner's Docker host, but a local image has that digest as its ID; on the classic image store use the repository digest printed by build.sh --push, or correct the image name", before connecting. The doctor `check-images` row is Fail with "check <ids>: image <ref> does not resolve by digest; a local image has that digest as its ID". Both inspections use `--`, the existing reference validation and no pull. A found reference makes one inspection, as today. The runner never starts a container by the bare ID. | `image_inspect_test.go` (found, not found then ID found, not found then not found, not found then a different ID, second-call error); `check_image_gate_test.go`; `check_image_check_test.go`; REQ-01 gated test |
| REQ-06 | Uppercase-hex point, RDBC REQ-05 | **Lowercase digest required for check images.** `config.ValidateCheck` refuses a non-empty `image` that does not match `^[^\s@]+@sha256:[a-f0-9]{64}$`, with exactly "check <id>: image must be pinned by digest (name@sha256:<64 lowercase hex>)". This holds in state-dir mode (`runner.local.yaml`) and config-file mode (`config.yaml`). The existing `check_image_config_test.go` "uppercase hex" case moves from accepted to refused, and a mixed-case case is added. The runner does not rewrite the operator's value (Q-1). `job_image` validation and `immutableImagePattern` are unchanged, and a test asserts that an uppercase-hex `job_image` still passes `Config.Validate`, so that a later change to the shared pattern is a visible decision (FU-A). | `config` unit tests; remoteconfig local-load test with an uppercase check image refused |
| REQ-07 | Uppercase-hex point, RDBC REQ-08 | **Canonical digest output.** `isolation.ImageDigest` returns the digest with lowercase hex for any reference that `pinnedDigestPattern` matches. Provenance `check_image` is therefore always `null` or matches `^sha256:[a-f0-9]{64}$`, including the job-image fallback when the job image is written in uppercase. The doctor short digest is lowercase. Every other provenance key, the key order and the schema `our-runner.check-install.v2` are unchanged, and no schema bump is made, because v2 already documents lowercase `sha256:<64 hex>` and the only reachable uppercase source (an uppercase `job_image`) stops start at the provider CLI gate before any check runs. | `image_inspect_test.go`; `check_install_provenance_test.go` exact JSON with an uppercase job image and no check image; doctor short-digest test |
| REQ-08 | Brief: app agreement | **App agreement re-checked.** The implementer re-runs, read only, in the app checkout at `main` (record the commit): `git grep -n -E "check_image\|check-install\.v\|CheckImage" -- src` and `git grep -n -E "sha256:\[" -- src`. The record states which hits are production code and which are test support. The slice proceeds only if no production code parses or compares `check_image`, and the test support does not compare `check_image`. Otherwise the implementer stops and reports. The implementer changes no app file. | Grep output pasted in this record |
| REQ-09 | AGENTS.md | **Docs.** README "checks.<id>.image" section: the YAML example says `<pinned reference printed by build.sh>`. It explains both image stores, the `--push` flag, that the reference must use lowercase hex, and the new gate and doctor message. The README provenance paragraph says `sha256:<64 lowercase hex>`. `docs/TESTING.md`: `CHECK_IMAGE=<pinned reference printed by build.sh>`, the new gated test with `RUN_DOCKER_CLASSIC_STORE_PROBE` and `CLASSIC_DOCKER_HOST`, and the new shell test. No code comments (owner rule). | Diff review |
| REQ-10 | AGENTS.md | **Checks.** `gofmt -l .` prints nothing; `go build ./...`, `go vet ./...` and `go test ./... -count=1` pass; `go test -race ./internal/daemon/ ./internal/config/ ./internal/doctor/ ./internal/isolation/ ./internal/remoteconfig/ ./cmd/our-runner/ -count=1` passes. Statement coverage of each new or changed Go file is at least 80 %, using the same `-coverpkg` profile as RDBC. `sh scripts/check.sh sec` and `sh scripts/check.sh vuln` pass, or each failure is shown to predate `67eeac7`. `staticcheck` is recorded as run or "not installed". Both `sh -n` lists pass, and every `packaging/tests/*_test.sh` passes. The REQ-01 gated test passes on both daemons. The existing RDBC gated tests `TestGoCheckImageRunsGoTestUnderTheProductionPolicy` (with `CHECK_IMAGE` set to a reference printed by the new `build.sh` on the rootless daemon) and `TestIterativeSnapshotSkipsHarnessStateDuringARootlessHandOff` still pass. | Command output pasted in this record |
| REQ-11 | Security review | **Security properties kept.** No path pulls an image. The runner never starts a container by bare image ID. A check runs in exactly the configured reference, with `--pull=never` and the RDBC REQ-06 restrictions; the argv for a valid reference is byte-identical to `67eeac7` (existing policy tests unchanged and passing). No remote field is added. The control plane, the ticket and the agent still cannot choose a check image. The extra inspection in REQ-05 runs only after a "No such image" result for a validated, lowercase pinned reference. Gate and doctor messages print only the operator's configured reference in local output. Provenance carries only the lowercase digest. | Tests above plus reviewer diff review |

**Inapplicable dimensions.** UI, prototype, browser and accessibility: no app or UI change. Persistence and migrations: none. Tenant permission: the runner is single-tenant, and the check image is a host fact (LC-1, LC-2). Cardinality: the gate and doctor already handle zero, one and several distinct images (RDBC REQ-07). REQ-05 adds one case per image, and the tests cover several checks that share one ID-only image (the doctor lists both check IDs) as well as two distinct images, one resolvable and one ID-only. `RepoDigests` with zero, one and several entries is covered in REQ-04. Live Build: not required. The RDBC live run already proved the containerd path end to end, and this slice does not change the check container argv for a valid reference (REQ-11).

## Design and decisions

### D-1 Prove the printed reference instead of guessing the store

- Options: (a) read `docker info` and branch on the containerd snapshotter `driver-type`; (b) after the build, inspect the candidate reference and print it only if it resolves to the built ID; (c) always require `--push`.
- Decision: (b), plus an optional `--push`. (b) does not depend on the format of `docker info` output, and it proves the exact property that the operator needs: this reference resolves to this image on this host. (c) would force a registry on containerd hosts, where today's flow works (RDBC live run).
- `--push` mirrors `packaging/job-image/build.sh`, so the two scripts behave alike. The ID check after the push makes sure the repository digest points at the image just built.

### D-2 Refuse uppercase, do not normalise (Q-1 default)

- Options: (a) refuse a check image whose hex is not lowercase, at load; (b) lowercase the digest silently and run the rewritten reference; (c) keep accepting it (fails at the gate).
- Decision: (a). The OCI image specification requires lowercase hex for the `sha256` encoded part, Docker refuses uppercase ("invalid checksum digest format", measured), and the app's own `defaultJobImage` patterns are lowercase only. Refusing keeps the configured value and the value the runner runs identical, so the gate, doctor and logs show what the operator wrote. (b) would run a reference that nobody typed. The config is local to the host and loaded at start, so the operator sees the refusal immediately.
- Output normalisation (REQ-07) is different: a digest the runner writes for the control plane is always canonical, as defence in depth.

### D-3 Keep `job_image` as is in this slice (Q-2 default)

- `job_image` comes from the app's remote document, and both repositories accept uppercase today. A runner-only tightening would make the runner refuse a document that the app saved as valid. The way a runner reacts to a refused document (stop, or keep the cached one) is an operational change that needs the app schema, its dashboard validation message (`jobImageInvalid` in every locale) and the runner to change together.
- An uppercase `job_image` already fails closed: the provider CLI start gate runs the job image under Docker, and Docker refuses the reference. So the gap is a bad message, not a safety hole. Recorded as FU-A.

### D-4 Diagnose the ID-only case at the gate, but never run by ID

- The second inspection only chooses the error message. The runner keeps one way to name a check image: the configured `name@sha256` reference. Running by bare ID would change `.Config.Image` in the live evidence (RDBC REQ-14 (e)), and would let a name typo go unnoticed.

## Owner questions

- **Q-1.** Should an uppercase-hex check image be refused at load (default, D-2), or normalised to lowercase? The implementer proceeds with refusal.
- **Q-2.** Should `job_image` also require lowercase hex? If yes, that is a cross-repository slice: the app schema and dashboard message, then the runner. The default is no change here (D-3, FU-A).
- **Q-3.** REQ-01 builds and removes one throwaway `FROM scratch` image on the rootful classic-store daemon (`unix:///var/run/docker.sock`), which also runs other local projects' containers. The test is opt-in, labels its image, touches no other image or container, and removes its image afterwards. The default is to go ahead. If the owner declines, REQ-01 runs only on the rootless daemon, the classic-store case rests on the planner measurement and the shell test, and the record says so.

**Coordinator decisions (9 November 2026, given to the implementer in the RCID handoff):**

- Q-1: refuse uppercase at load (the default, D-2). Implemented in REQ-06; the runner never rewrites the value.
- Q-2: no `job_image` change. FU-A stays a follow-up; `immutableImagePattern` and the `job_image` rule are byte-identical to `67eeac7`.
- Q-3: yes. The implementer may build and remove one labelled throwaway `FROM scratch` image on the rootful daemon, removing only that image. Done in REQ-01; no other image or container on the rootful daemon was touched. On the rootless daemon the slice added only the `our-runner-check-go:rcid` image (plan step 6) and the REQ-01 probe image, which the test removed. The probe builds leave small BuildKit cache entries on both daemons; they were not pruned, because pruning is daemon-wide.

## Implementation plan

Owned files (app-runner): `packaging/check-image-go/build.sh`; new `packaging/tests/check_image_go_build_test.sh`; `internal/config/check_validation.go` (and `config.go` only to add a separate lowercase check-image pattern, leaving `immutableImagePattern` unchanged), `internal/config/check_image_config_test.go`; `internal/remoteconfig/local_check_image_test.go`; `internal/isolation/image_inspect.go` and test; new gated `internal/isolation/classic_store_integration_test.go`; `internal/daemon/check_image_gate.go` and test; `internal/daemon/check_install_provenance_test.go`; `internal/doctor/check_image_check.go` and test; `README.md`; `docs/TESTING.md`; this record. Do not touch `bin/`, `~/.shortstack-runner`, `runner.local.yaml`, the running runner, the job image, any other image or container on either daemon, host Docker settings or any app file. Grep for a test helper name before adding it (helper names clash across files in the daemon package).

0. [x] REQ-01 gated test first, no production code. Run it on the classic daemon (subject to Q-3) and on the rootless daemon. Stop and report if the classic result differs from the planner measurement.
1. [x] REQ-06 tests first (uppercase and mixed case refused; uppercase `job_image` still valid), then the check-image pattern and message.
2. [x] REQ-07 tests first, then lowercase output in `ImageDigest`.
3. [x] REQ-05 tests first with the existing `runImageInspect` seam, then the second inspection, the sentinel, the gate message and the doctor message.
4. [x] REQ-04 shell test first, then REQ-02 and REQ-03 in `build.sh`.
5. [x] REQ-08 app grep (read only).
6. [x] Rebuild the reference image on the rootless daemon with the new `build.sh` (no `--push`), and record the printed reference. Run the REQ-10 gated tests with it.
7. [x] REQ-09 docs, REQ-10 checks, implementation report, status `review_ready`.

## Verification plan and results

| Command / scenario | Required for | Actual result / date | Evidence |
| ------------------ | ------------ | -------------------- | -------- |
| `CLASSIC_DOCKER_HOST=unix:///var/run/docker.sock RUN_DOCKER_CLASSIC_STORE_PROBE=1 go test ./internal/isolation/ -run TestImageStoreResolvesALocallyBuiltImageByDigest -v -count=1` | REQ-01, REQ-05 | PASS, 2026-11-09 (two runs) | Matches the planner measurement. See "REQ-01 gated output". |
| `DOCKER_HOST=unix:///run/user/1000/docker.sock RUN_DOCKER_SANDBOX_PROBE=1 go test ./internal/isolation/ -run TestImageStoreResolvesALocallyBuiltImageByDigest -v -count=1` (containerd case) | REQ-01 | PASS, 2026-11-09 | See "REQ-01 gated output". |
| `sh packaging/tests/check_image_go_build_test.sh` | REQ-02 to REQ-04 | PASS, 2026-11-09 | "check image go build tests passed". It failed first against the old `build.sh` ("the containerd reference was not proven"). |
| `go test ./internal/config/ ./internal/remoteconfig/ ./internal/isolation/ ./internal/daemon/ ./internal/doctor/ -count=1` | REQ-05 to REQ-07 | PASS, 2026-11-09 | 2301 tests passed in 5 packages |
| App read-only grep at app `main` | REQ-08 | done, 2026-11-09 | See "REQ-08 app grep". |
| `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./... -count=1`, race, per-file coverage, `sh scripts/check.sh sec`, `sh scripts/check.sh vuln`, `staticcheck`, both `sh -n` lists, every `packaging/tests/*_test.sh` | REQ-10 | PASS, 2026-11-09 (one pre-existing exception, noted) | See "REQ-10 checks". |
| `DOCKER_HOST=unix:///run/user/1000/docker.sock sh packaging/check-image-go/build.sh our-runner-check-go:rcid`, then the RDBC gated tests with `CHECK_IMAGE` set to the printed reference | REQ-02, REQ-10 | PASS, 2026-11-09 | It printed `pinned reference: our-runner-check-go:rcid@sha256:dfdbaa6bfd423b33c6ccb6f1cc7cd66bbb9ee46c099c23682b7039723f0fd995`. Both RDBC gated tests passed with it. |
| Reviewer diff review: argv for a valid reference unchanged, no pull, no bare-ID run, no remote field | REQ-11 | for the reviewer | The implementer's notes are under "REQ-11". |

### Implementation report (implementer, 9 November 2026)

Changed paths (app-runner only):

- `internal/isolation/image_inspect.go`: adds `ErrImageResolvesOnlyByID` and `inspectImageID`. The second inspection of a bare `sha256:<hex>` runs only after a "No such image" result for a reference that matches `lowercasePinnedPattern`. `ImageDigest` lowercases its result.
- `internal/isolation/image_inspect_test.go`: tests for the cases found, not found then the ID found, not found then not found, not found then a different ID, not found then unexpected output, a failure of the second call, and no second call for a tag, a name, an uppercase digest, a short digest or a bare ID. Lowercase `ImageDigest` cases.
- New `internal/isolation/classic_store_integration_test.go`: the REQ-01 gated test.
- `internal/config/config.go`: adds `checkImagePattern` (lowercase only). `immutableImagePattern` is unchanged. `internal/config/check_validation.go` uses it, with the new message.
- `internal/config/check_image_config_test.go`: uppercase, mixed-case and one-uppercase-character values are refused. An uppercase `job_image` still validates. An uppercase check image in `config.yaml` is refused by `Load`.
- `internal/remoteconfig/local_check_image_test.go`: tag-only, uppercase and mixed-case values from `runner.local.yaml` are refused.
- `internal/daemon/check_image_gate.go` and its test: the ID-only message, for several checks that share one image, for one resolvable and one ID-only image, and through `New` (refused before the daemon is returned, and the lock is released).
- `internal/daemon/check_install_provenance_test.go`: exact JSON with an uppercase job image and no check image gives a lowercase `check_image`.
- `internal/doctor/check_image_check.go` and its test: the ID-only Fail detail, with shared checks listed and mixed with a missing image. The short digest is lowercase.
- `packaging/check-image-go/build.sh`: `IMAGE_TAG [--push]`, the resolve proof, the classic-store refusal, and the `--push` repository digest path.
- New `packaging/tests/check_image_go_build_test.sh`.
- `README.md` and `docs/TESTING.md`: REQ-09.

Test-first evidence:

- REQ-01 first failed to compile (`undefined: ErrImageResolvesOnlyByID`).
- REQ-06 config and remoteconfig tests failed before the pattern change: 8 config failures and 4 remoteconfig failures.
- REQ-07 tests failed with `ToLower` temporarily removed: `TestImageDigestReturnsOnlyThePinnedDigest` and `TestDockerProvenanceIsHermeticAndNamesOnlyTheImageDigest`.
- REQ-05 gate and doctor tests failed before the gate and doctor branches: 4 daemon failures and the doctor table.
- The REQ-04 shell test failed against the old `build.sh`.

Decisions:

- **I-1 (REQ-02 message).** The refusal names the actual tag ("rerun with --push to push our-runner-check-go:v1 to its registry ..."), not the literal word `IMAGE_TAG`. Otherwise the text is exactly the contract's.
- **I-2 (REQ-02).** Without `--push`, two more failures fail closed and print no pinned reference: an inspection error that is not "No such image" ("could not inspect <ref>: <docker error>"), and a reference that resolves to a different ID.
- **I-3 (REQ-03).** An entry is selected when it starts with `<repository>@sha256:`, using a shell `case` prefix match. A substring match is not enough. The shell test puts `mirror.example/our-runner-check-go@...` first to prove this. Only the first matching entry is used. If it is malformed, the script fails and does not skip to a later entry.
- **I-4 (REQ-05).** In the second inspection, unexpected output, or a different ID, counts as `ErrImageNotPresent`. Only a Docker error other than "No such image" is returned as `inspect image: ...`. When the first inspection prints unexpected output, it still returns the same error as at `67eeac7`.

#### REQ-01 gated output

On the classic store, rootful `unix:///var/run/docker.sock` (overlay2):

```
classic_store_integration_test.go:95: our-runner-classic-probe:63253d998410f6e2@sha256:507daa8330bb90bc7a4d3bf3f7f2e16955c87d1de68ef8c53cf2cde9c9e2a351 does not resolve: image does not resolve by digest, but a local image has that digest as its ID
classic_store_integration_test.go:103: sha256:507daa8330bb90bc7a4d3bf3f7f2e16955c87d1de68ef8c53cf2cde9c9e2a351 resolves to sha256:507daa8330bb90bc7a4d3bf3f7f2e16955c87d1de68ef8c53cf2cde9c9e2a351
--- PASS: TestImageStoreResolvesALocallyBuiltImageByDigest/classic_store (0.94s)
```

The first run gave the same result with probe `87bc1bf98889d137`, ID `sha256:a00cd053…2b08`. After each run, `docker image ls --filter label=our-runner-classic-probe` was empty on the rootful daemon.

On the containerd store, rootless `unix:///run/user/1000/docker.sock` (`driver-type io.containerd.snapshotter.v1`):

```
classic_store_integration_test.go:123: our-runner-classic-probe:4750e2af81c279b6@sha256:b5840dde8ce92e56659b7b94b64e4a322b562459f45ddce5081633063cfe2b9b resolves to sha256:b5840dde8ce92e56659b7b94b64e4a322b562459f45ddce5081633063cfe2b9b
--- PASS: TestImageStoreResolvesALocallyBuiltImageByDigest/containerd_store (0.43s)
```

The label filter was empty afterwards.

#### REQ-08 app grep (app `main` at `abf145dd`, read only)

```
$ git grep -n -E "check_image|check-install\.v|CheckImage" main -- src
src/server/planning-manifests/test-support/go-build-upload-evidence.go:24:	buildCheckProvenanceSchema = "our-runner.check-install.v2"
src/server/planning-manifests/test-support/go-planning-driver_test.go:258:	document := `{"schema":"our-runner.check-install.v2",...,"check_image":null,...}`
src/server/planning-manifests/test-support/go-planning-driver_test.go:404:	seedQueuedArtifact(... `{"schema":"our-runner.check-install.v2",...}`)
src/server/planning-manifests/test-support/go-planning-driver_test.go:419:	if os.WriteFile(provenance, []byte(`{"schema":"our-runner.check-install.v2"}`), 0o600) != nil {
$ git grep -n -E "sha256:\[" main -- src
src/lib/env/runner-release-settings.ts:13:const JOB_IMAGE = /^[^\s@]+@sha256:[a-f0-9]{64}$/u;
src/shared/schemas/runner-configuration-profile.schema.ts:20:  /^[^\s@]+@sha256:[a-fA-F0-9]{64}$/u;
src/shared/schemas/runner-configuration-profile.schema.ts:188:    .regex(/^[^\s@]+@sha256:[a-f0-9]{64}$/u)
src/shared/schemas/runner-enrollment.schema.ts:165:      .regex(/^[^\s@]+@sha256:[a-f0-9]{64}$/)
```

- All hits in the first grep are test support under `src/server/planning-manifests/test-support/`. Line 258 is a fixture document that contains `"check_image":null`. `go-build-upload-evidence.go` matches only the schema string and never reads `check_image`.
- The second grep hits only production `job_image` and `defaultJobImage` patterns, which are unchanged (FU-A).
- No production code parses or compares `check_image`, so the slice proceeds. No app file was changed.

#### REQ-10 checks (worktree, 9 November 2026)

- `gofmt -l .`: empty. `go build ./...`, `go vet ./...`: exit 0.
- `go test ./... -count=1`: exit 0, with 42 packages `ok` and no failures.
- `go test -race ./internal/daemon/ ./internal/config/ ./internal/doctor/ ./internal/isolation/ ./internal/remoteconfig/ ./cmd/our-runner/ -count=1`: exit 0. The daemon package took 395.1 s.
- Coverage uses `-coverpkg` over daemon, config, doctor, isolation and cmd, aggregated per file with a scratchpad helper:

  | File | Coverage |
  | ---- | -------- |
  | `internal/config/check_validation.go` | 11/11, 100.0 % |
  | `internal/config/config.go` | 251/268, 93.7 % |
  | `internal/isolation/image_inspect.go` | 29/29, 100.0 % |
  | `internal/daemon/check_image_gate.go` | 25/25, 100.0 % |
  | `internal/doctor/check_image_check.go` | 35/36, 97.2 % |

- `sh scripts/check.sh sec` (gosec v2.29.0): exit 0, with no findings.
- `sh scripts/check.sh vuln` (govulncheck v1.8.0): exit 0, with "Your code is affected by 0 vulnerabilities". It also reports one vulnerability in a required module that the code does not call.
- `staticcheck`: not installed.
- Both `sh -n` lists, README and `docs/TESTING.md`, now include the new test. Run together: exit 0.
- Shell tests: these all pass when run directly: bootstrap, check_image_go_build, fresh_host_doctor, install, prepare_release, published_tag, release_artifacts_fixture and systemd.
  - `release_artifacts_test.sh` fails without arguments ("no archive matched"). It is documented only as `sh packaging/tests/release_artifacts_test.sh dist snapshot` after a snapshot release build, and it is not in either standalone list. That predates `67eeac7`, and this slice does not touch it.
- `shellcheck -s sh` on the new test is clean. On `build.sh` it reports only SC1007 for the `CDPATH= cd` line, which is the same at `67eeac7`.
- REQ-01 gated test: passes on both daemons (above).
- RDBC gated tests on the rootless daemon used `JOB_IMAGE=our-runner-job:rjic@sha256:a2c8cabef3dbe9d4044bf2bcb2f7031aadbeee24ac0bd0dce8b41b5e900e8382` and `CHECK_IMAGE=our-runner-check-go:rcid@sha256:dfdbaa6bfd423b33c6ccb6f1cc7cd66bbb9ee46c099c23682b7039723f0fd995`, the reference printed by the new `build.sh`. `TestIterativeSnapshotSkipsHarnessStateDuringARootlessHandOff` passed (22.70 s). `TestGoCheckImageRunsGoTestUnderTheProductionPolicy` passed (21.58 s), with all 4 subtests.

#### REQ-11 (implementer notes for the reviewer)

- No container argv code changed. `internal/isolation/container.go` and `isolation.go` are not in the diff, and the policy tests (`pull_never_test.go` and others) are unchanged and pass.
- The only new Docker commands are these:
  - `docker image inspect --format {{.Id}} -- sha256:<hex>`. It runs only after "No such image" for a lowercase pinned reference, and its result only chooses the error.
  - In `build.sh`, `docker image inspect`, plus `docker push` only with `--push`.
- No path pulls an image, and no remote field was added. Gate and doctor messages print only the configured reference.

#### Candidate coverage

- Several checks share one ID-only image: gate and doctor tests.
- One resolvable image and one ID-only image: gate test, and the doctor "one image resolves only by ID" case.
- `RepoDigests` with zero entries (empty), one entry, and several entries (with another repository first): shell test.

## Follow-ups

- **FU-A.** Require lowercase hex for `job_image` in both repositories together: the app's `RUNNER_CONFIGURATION_JOB_IMAGE_PATTERN` (`src/shared/schemas/runner-configuration-profile.schema.ts:19-20`) and its `jobImageInvalid` messages, then the runner's `immutableImagePattern` for `job_image` (Q-2).
- **FU-B.** An optional live proof of `build.sh --push` against a throwaway local registry on the classic daemon. It is not required, because the shell test covers the script logic and REQ-01 covers the store behaviour.

## Estimate

This slice: about 0.5 slice (range 0.5 to 1; the upper end if REQ-01 contradicts the planner measurement or Q-3 is declined).
