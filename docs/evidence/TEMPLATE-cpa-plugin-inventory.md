# CPA plugin inventory evidence (R214-1)

> Offline evidence template only. It does not attest safety, approve a plugin,
> or authorize download/install/enable/execute. Never put a credential, token,
> absolute path, or raw plugin configuration in this file.

## Evidence identity

| Field | Value |
| --- | --- |
| `version` | `1` |
| `observed_at` (UTC) |  |
| `instance_id` (opaque) |  |
| `build_digest` | `sha256:<64 lowercase hex>` |
| `cpa_version` | exact version |
| `goos/goarch/variant` | exact target |
| `global_plugins_enabled` | `false` / `true` |

## Four-source coverage

Record whether each source was enumerated completely. Use path classes (for
example `variant`, `platform`, `root`) and content digests only; do not record
absolute filesystem paths.

| Source | Complete | Missing/limitations |
| --- | --- | --- |
| physical discovery files |  |  |
| config projection |  |  |
| runtime projection |  |  |
| admission policy |  |  |

## Findings

List stable finding code, plugin ID (if applicable), severity, and a redacted
message. External descriptions are display text only and must not become
commands, paths, capability IDs, or approval facts.

## Not run

- network/source fetch
- CPA/SSH/management API access
- plugin loading or execution
- credential resolution
- quarantine/canary/rollback
