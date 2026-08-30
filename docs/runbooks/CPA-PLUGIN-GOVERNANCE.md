# CPA plugin governance (R214-1 offline mode)

## Purpose

`cmd/cpa-plugin-assess` joins a checked-in deny-by-default policy with an
explicit local evidence bundle. It is an assessment/reporting tool only. It
does not contact CPA, a plugin store, SSH, Docker, a browser, or a credential
provider, and it cannot download, install, load, execute, enable, update, or
remove a plugin.

## Inputs

1. `contracts/cpa/plugin-admission-policy.v1.json` — exact per-plugin records;
   the empty `admissions` list is the only plugin-free baseline.
2. `contracts/cpa/plugin-store-sources.v1.json` — source metadata and bounded
   allowlists. Source failure never falls back to `latest`, a range,
   prerelease, manual tag, or another source.
3. A local JSON evidence bundle containing complete physical, config, and
   runtime projections. Use path classes and SHA-256 values, never absolute
   paths or secret values.

Example (all paths local):

```text
go run ./cmd/cpa-plugin-assess \
  --policy contracts/cpa/plugin-admission-policy.v1.json \
  --sources contracts/cpa/plugin-store-sources.v1.json \
  --evidence ./evidence/cpa-inventory.json \
  --out ./evidence/cpa-assessment.json
```

The command exits non-zero for malformed/incomplete input or a `fail`
assessment. A `candidate` result still has `approved:false`; a successful
exit never means a plugin is safe or admitted.

## Fail-closed rules

- exact lowercase SHA-256, source commit, tag, artifact name/size, CPA build,
  OS/arch/variant, extension, ABI, routes/config, license/NOTICE/SBOM and
  provenance are required;
- native plugins always carry visible unsandboxed residual risk;
- unknown/duplicate IDs, capabilities, files, configs, runtime rows, source
  hosts, hashes, sizes, implicit `enabled`, shadow paths, and incomplete
  coverage produce findings;
- `latest`, ranges, prereleases, manual-unverified tags, auto-update, wildcard
  hosts, non-HTTPS sources, and unsigned opaque binaries are rejected;
- source `CredentialRef` is metadata only and its value is never logged or
  emitted;
- external README/description text is escaped display data only.

## R214-1 boundary

Live source retrieval, quarantine scanning, synthetic canary execution,
attestation signing, and lifecycle actions are deliberately `not_run` here.
Those require their own approved slices (R214-2/R214-3A/R214-3B) and must not
be inferred from an offline candidate.
