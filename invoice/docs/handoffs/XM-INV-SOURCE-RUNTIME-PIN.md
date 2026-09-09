# XM-INV-SOURCE-RUNTIME-PIN — the source runtime pin, the cutover runtime, and how a source upgrade is applied

- **status:** implemented (RC100); the pin bump for the live Sub2API (0.1.179 → 0.2.1) is applied after RC100 rolls forward, by the procedure below.
- **owner question answered:** "Sub2API was upgraded in its own backend; why does our side still say 0.1.179, and will it follow next time?" — because the value is a declared, audited pin, not a probe, and no: it never follows. This handoff makes the pin movable without touching the immutable cutover manifest.

## What happened on 2026-09-06

The live Sub2API (`sub2api-mig` on the production host) had been upgraded in place by its own updater to **0.2.1** (binary `Sub2API 0.2.1`, commit `578785ee7`, 281 applied migrations) while every pin on our side still read `0.1.179`. A source-upgrade CAS was attempted per the runbook: `source_instances.runtime_version` moved to `0.2.1` through `bootstrap-sources` and `SUB2API_RUNTIME_VERSION` was set in the release env. The four economic agents then refused to start with `cutover manifest source or runtime mismatch`: the encrypted cutover manifest each agent sealed at cutover carries `source_runtime`, and `LoadCutoverManifest` compared it with `SOURCE_RUNTIME_VERSION`. The runbook's procedure covered two of the three places the runtime lives and not the third, and no agent command could rewrite it — nor should one: every ingested record carries the manifest's hash (`cutover_manifest_hash`), and the API keeps the registered manifest immutable, so the manifest cannot change without invalidating history. Readiness went 503 until the pin was rolled back.

## Three places, two meanings

| where | meaning | moves? |
| --- | --- | --- |
| `source_instances.runtime_version` (API) | the **approved pin**: every batch must declare exactly this (`source_sync.go`: `ErrVersionConflict` otherwise) | yes — `bootstrap-sources` CAS with `expected_previous_runtime_version`, `runtime_version_revision` +1 |
| `SOURCE_RUNTIME_VERSION` (agent env, from `SUB2API_RUNTIME_VERSION` / `NEWAPI_RUNTIME_VERSION`) | what the agent declares in every batch header | yes — release env, agents recreated |
| `source_runtime` inside the sealed cutover manifest (agent state) | the runtime the source ran **at capture time**; part of the manifest hash that every record references | **never** for a state generation |

Before RC100 the agent used one value for all three. RC100 gives the third its own name.

## The change (RC100)

- Agent (`agents/cmd/source-agent-prod`): `runConfig.CutoverRuntime` from `SOURCE_CUTOVER_RUNTIME_VERSION`, falling back to `SOURCE_RUNTIME_VERSION` when unset (`cutoverRuntimeFromEnv`). Every manifest check (`LoadCutoverManifest`, `LoadAndCheckCutover`, the live DB contract check, `buildDBConnector`) compares against the cutover runtime; batches keep declaring the pin. `cutover-init` refuses to seal under a cutover runtime other than the pin (`cutoverInitRuntimeGuard`) — a manifest records the runtime it was captured under, never a later bump.
- Compose (`deploy/docker-compose.sources.yml`): every source-agent service passes `SOURCE_CUTOVER_RUNTIME_VERSION: ${SUB2API_CUTOVER_RUNTIME_VERSION:-}` / `${NEWAPI_CUTOVER_RUNTIME_VERSION:-}`; empty keeps the pre-RC100 behaviour. `.env.production.example` and `CONFIGURATION.md` document both.
- API/console: the sync status carries `cutover_runtime_version` (from `source_cutover_manifests`) beside `approved_runtime_version` and `observed_runtime_version`; the console's 同步状态 shows 代理声明 / 契约审计 / 切换时 so a pin bump is visible as two values, not a rewrite.
- Runbook section 7: the source-upgrade CAS now names the three places and the order.

## Applying a source upgrade (the procedure the runbook carries)

1. Re-audit the bridge contract against the new upstream schema (the tables the bridge functions read; for 0.1.179 → 0.2.1: `usage_logs` and `users` gained columns only). Record it.
2. Confirm the release env carries `SUB2API_CUTOVER_RUNTIME_VERSION` equal to the manifest's `source_runtime_version` (`SELECT source_type,source_runtime_version FROM source_cutover_manifests`), and that the running agents are on a release with this handoff.
3. Drain and stop the source's five agents; no `pending.enc`, no lock.
4. `source-instances.json`: set `runtime_version` to the new value and `expected_previous_runtime_version` to the old one; run `bootstrap-sources`; verify `runtime_version_revision` moved.
5. Release env: `SUB2API_RUNTIME_VERSION=<new>`; leave `SUB2API_CUTOVER_RUNTIME_VERSION` untouched.
6. `docker compose ... up -d` the five agents; verify batches are accepted (no `ErrVersionConflict` rejections), cycles publish, readiness 200; drop `expected_previous_runtime_version` from the config afterwards.

Rollback is the same procedure with the values swapped.

## Not in this slice

- Observing the real upstream version. The invoice system never asks Sub2API what it runs; the platform's Sub2API connector is the right place for that (an observation of `admin/system/version` and an alert on change), tracked on the platform side.
- A schema fingerprint self-check inside the bridge functions. The bridge already fails closed when a referenced column disappears (SQL error → blocked stream); a fingerprint would only turn that into a nicer reason. Deferred.
