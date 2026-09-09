# Report delivery notes

- Canonical `artifact.json`: validated successfully by
  `mcp__dataAnalyticsWidgets__validate_artifact`.
- MCP render call: accepted the same payload.
- Portable HTML builder: content/package validation passed, but browser QA
  consistently failed `horizontal_overflow` at 1440px because the shared
  sticky page header uses viewport width while the long report has a vertical
  scrollbar. Several content/layout reductions did not change the overflow.
- No HTML artifact was published because the required browser verification did
  not pass. Temporary HTML and failure screenshots were deleted.
- Reader-facing fallback: `CONSUMPTION-ELIGIBILITY-ADJUSTMENT.md`.
