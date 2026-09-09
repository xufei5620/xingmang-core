# XM-INV-PROJECTION-HEALTH-UI: surface the eligibility projection queue on the admin health screen

- **status:** implemented on the release branch and gated locally (web
  typecheck, 160 tests across 11 files, production build). Not yet released;
  rides the next RC.
- **branch:** `ai/claude/XM-INV-AUTOLOGIN` (the release line).

## Gap

`XM-INV-PROJECTION-FAILURE-GRADING` (RC80) added the eligibility-projection
worker's own queue grading to `GET /api/v1/admin/source-health` as an
`eligibility_projection` block, alongside the ten per-stream rows. Nothing
consumed it: the web client's `BackendSourceHealth` type did not declare the
field, `mapSourceHealth` did not read it, and `SourceHealthReport` had no
place to put it. The counters that decide whether an operator needs to run
`invoice-eligibility-repair --kind=projection-requeue-dead` were reaching the
browser and being discarded there.

## Change

- `SourceHealthReport` gains an optional `eligibilityProjection`
  (`EligibilityProjectionHealth`: queued, processing, retrying, dead,
  proofPending, and the two optional "oldest" timestamps).
- `mapEligibilityProjectionHealth` validates the block as strictly as the
  per-stream rows are validated -- non-negative safe integers, parseable
  timestamps -- and throws the same `INVALID_SOURCE_HEALTH_RESPONSE` on a
  malformed one. An **absent** block is not an error: a server predating the
  field sends none, and it maps to `undefined`.
- The admin 来源同步状态 screen renders a second card, 资格投影队列, below the
  stream table. It states in its own subtitle that the queue is global
  (one queue serves every account on both platforms), because the stream
  table above it is platform-filtered for the embedded admin and the counters
  are not. Its badge is driven by `dead`, the one counter that is never
  normal: a job stops retrying after eight graded failures and only an
  operator repair moves it.
- Undefined maps to an honest empty state ("服务未返回资格投影明细"), not to
  zeros -- zeros on this screen read as an idle, healthy queue, which is the
  opposite of what an unreported block means during an incident.
- The mock API returns a small non-zero queue so the mock mode exercises the
  populated path rather than the empty state.

No backend change: the endpoint already returned this block, and the top-level
`ready` field keeps its existing meaning (the five-stream source readiness),
exactly as the RC80 handler comment requires.

## Tests

`web/src/lib/http-api.eligibility-projection.test.ts` (5 cases, fetch and
timer stubs following `http-api.source-instance-filter.test.ts`):

- every counter and both timestamps map through;
- omitted timestamps stay undefined and a zeroed block still maps;
- an absent block yields `undefined` with the ten stream rows and `ready`
  untouched;
- a negative counter is rejected with `INVALID_SOURCE_HEALTH_RESPONSE`;
- an unparseable timestamp is rejected the same way.

## Gates

`npm run typecheck` exit 0; `npm test -- --run` 11 files / 160 tests passed;
`npm run build` exit 0. Prettier is not part of this project's gate and
already reports the repository's CRLF files as unformatted, including files
this change does not touch.
