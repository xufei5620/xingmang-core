# XM-C001 — 支付与财务 · 资金概览交接

## Status

`DONE_WITH_CONCERNS` — the Sub2API overview is implemented and all runnable
TypeScript/UI gates passed. One environment-wide responsive-shell follow-up
remains below.

Branch: `ai/codex/XM-C001-finance-overview`
Base verified: `d965760cdf2cfea580a67e2562d4c37f76f7ece9` (`origin/release/v0.1-launch`)

## Delivered

- Replaced only Sub2API `finance&sub=overview` with the required date-only
  day/week/month control, exact ordered eight-card grid, reconciliation panel,
  profit bridge, and recent-events table.
- The reusable `PeriodRangeControl` and `periodRangeFor` use UTC date fields;
  week starts Monday and the React Query key carries `from` and `to`.
- Added integer-string aggregation shared with the pre-existing finance cards.
  It filters `sub2api`, fails closed for missing/invalid/mixed money, preserves
  scale, uses `formatScaledMinorUnits`, shows coverage/source/freshness, and
  takes the oldest contributor observation.
- Payment-only figures deliberately render `—` plus `未接入` and explicit
  `支付 Connector（M3）未接入` evidence. No payment events, counts, amounts,
  order IDs, or prototype statuses were copied into production.
- NewAPI continues to use its pre-existing finance-overview component; no
  NewAPI tab, API, navigation, contract, Go, migration, dependency, or Action
  behavior changed.

## TDD evidence

Before production modules existed, ran:

```text
pnpm --config.verify-deps-before-run=false --filter admin-web exec vitest run src/components/FinanceOverview.test.tsx
FAIL: Failed to resolve import ../lib/financeOverview (and the page module did not exist)
```

That was the expected RED: the test named the absent date-range/aggregation and
rendered-page behavior. After the minimum implementation and the shared-policy
refactor:

```text
FinanceOverview.test.tsx + FinanceSummaryCards.test.tsx: 24 passed / 24
```

The focused tests cover exact card order, six unavailable payment cards, literal
scale-6 integer fixtures, missing/invalid/mixed money, Sub2API filtering,
oldest observation and coverage, day/week/month values, API range arguments,
reconciliation/event structure, and the profit bridge.

## UI/UX review

- `finance dashboard unavailable data --domain ux`: returned unrelated bulk-
  action/video results; rejected as not applicable.
- `responsive metric card grid --stack react`: no match; retried with
  `responsive dashboard card layout --stack react`, also no match. Used the
  verified fallback guidance only: mobile-first grid, visible focus state,
  semantic token classes, native date input/buttons, and table-local overflow.
- Applied the prototype design system: existing `MetricCard`, `StatTile`,
  `Badge`, `FreshnessBadge`, semantic token classes, compact borders, and the
  two-column information panels. No colors/radii/shadows were hardcoded.

## Browser verification

Vite route: `/platforms/sub2api?tab=finance&sub=overview`, with mocked
`/api/v1/finance/channels/summary` and `/api/v1/services` responses.

| Viewport | Result |
|---|---|
| 1440×900 | All cards/panels/table visible; mock aggregate and evidence render. |
| 1024×900 | Responsive grid/panels remain contained. |
| 800×900 | Responsive grid/panels remain contained. |
| 390×844 | Finance controls and grid wrap; existing global sidebar creates document-level horizontal overflow. |

Clicked `周`: control showed `2026-08-24 至 2026-08-30` and the mock Query
continued rendering. After mocking shell reads, browser console reported `0`
errors and `0` warnings. At 390px, the unmodified shell measured
`document.scrollWidth=720` while `main.scrollWidth=398`; its persistent sidebar
left the finance panel only 87px wide. For isolation evidence only, hiding that
existing sidebar in the browser yielded `finance.scrollWidth=327` and
`finance.clientWidth=327`: this slice's controls/grid/table contained without
their own horizontal overflow. The remaining document overflow therefore
belongs to the untouched global shell/sidebar and is reported rather than fixed
outside this slice.

## Verification gates

Passed:

```text
pnpm --config.verify-deps-before-run=false --filter admin-web run typecheck
pnpm --config.verify-deps-before-run=false --filter admin-web run test
  36 files, 672 tests passed
pnpm --config.verify-deps-before-run=false -r run typecheck
  all 5 workspace projects passed
pnpm --config.verify-deps-before-run=false -r run test
  admin-web 36 files/672 tests; ui-admin 15/214; ui-primitives 7/16; tokens 1/10
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
  completed successfully (pre-existing >500 kB chunk-size warning only)
git diff --check
  passed
```

`D:\Git\bin\bash.exe scripts/check-governance.sh` passed with exit 0 and no
output. The initial WSL Bash attempt was not accepted as evidence because it
could not resolve this Windows worktree's Git path; Git for Windows Bash is the
authoritative local result recorded here.

## Self-review and follow-ups

- Reviewed the diff against the brief: only finance UI/helper/tests/report;
  no write transport, `number`/`parseFloat` money conversion, sample payment
  values, NewAPI expansion, or right drawer.
- Follow up: make the shared admin shell responsive at 390px so the global
  sidebar does not produce document overflow.
- Push/PR: not performed.
