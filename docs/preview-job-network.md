# Preview reach from build jobs

A build job that is offered `platform_deploy_preview` can reach its own deployed preview at `http://our-preview:8080/` over a per-job internal Docker network. This page records how the runner opens that reach for each agent CLI and what it reports.

## When reach opens

The runner decides once, at job start, before the agent starts. Reach is open only when all of these hold:

- the job's tool list includes `platform_deploy_preview` (preview hosting, the tunnel and a build job);
- isolation is `docker` with the `restricted` job network;
- the provider is not `codex`.

The decision does not change during the job. If the tunnel comes up after the job started, `platform_deploy_preview` answers `refused` with `runner_capability_missing`; when reach can never open for the job (codex, direct isolation, `network: none`), it answers `job_network_unsupported`.

## Per agent CLI

- **Claude Code.** The inner sandbox keeps `strictAllowlist: true`. Exactly `our-preview` is added to `allowedDomains`, next to any applied `network_access` grants. No wildcard is added. Sandboxed commands reach the network only through the sandbox proxy in `HTTP_PROXY`. The runner sets `NODE_USE_ENV_PROXY=1` for every hardened Claude job, so a plain node `fetch` uses that proxy instead of failing name resolution with `EAI_AGAIN`. A host off the allowlist is refused by the proxy: a client that sends a plain proxy request gets `403`, and `fetch`, which tunnels with `CONNECT`, fails with an error.
- **Codex.** Reach never opens. The permission profile keeps `[permissions.runner-job.network] enabled = false`, and the tool answers `job_network_unsupported`. See the finding below.
- **opencode.** It has no nested sandbox, so the per-job internal network is the only boundary. Nothing in its configuration changes.

## Evidence

`run_network_policy.agent_sandbox_network` describes the baseline of the agent CLI sandbox only:

- `preview_only`: a Claude job under Docker with reach open. The baseline allows only `our-preview`.
- `deny_all`: a Claude job under Docker without reach. It is never reported while reach is open.
- `not_recorded`: every non-Claude provider, including opencode with reach open, and any job under direct isolation.

Hosts added by `network_access` grants are not part of this value. `elevated_capability_outcomes` for the same manifest records which grants were applied, by `request_id`; it carries no host names, and the control plane knows each grant's host from its own request. The hosts themselves appear in `run_network_policy.egress_rules.approved_hosts` only under run guardrails schema version 2, with Docker isolation and the restricted job network, and only when the host egress rules were held. See [elevated-capabilities.md](elevated-capabilities.md).

## Codex finding (codex 0.146.0, the version pinned in `packaging/job-image/package.json`)

Read from the `rust-v0.146.0` tag of github.com/openai/codex:

- `codex-rs/core/config.schema.json`: the profile network table accepts `domains`, `mode`, `allow_local_binding` and `enabled`.
- `codex-rs/core/src/config/permissions.rs` (`network_proxy_config_from_profile_network`): profile `network.enabled` controls sandbox network access; profile proxy settings do not start the managed proxy on their own.
- `codex-rs/features/src/lib.rs`: the `network_proxy` feature that enforces the domain list is `Stage::Experimental` and off by default. A local `codex features list` (0.153.4) reports `network_proxy experimental false`.
- `codex-rs/network-proxy/src/runtime.rs` (`host_blocked`): hostnames that resolve to private addresses are blocked even when allowlisted unless `allow_local_binding` is true. `our-preview` resolves to a private Docker address.

Setting `enabled = true` with `domains` and without the experimental feature would give the agent full network access. The runner therefore fails closed for Codex and does not use the experimental feature.
