# XM-INV-EMBED-HEIGHT: keep the embedded frame tall enough as content grows

- **status:** implemented on the release branch, gated locally (web typecheck,
  174 tests across 12 files, production build). Not yet released.
- **branch:** `ai/claude/XM-INV-AUTOLOGIN` (the release line).
- **reported by the product owner**, 2026-09-04: the embedded admin console
  showed its own scrollbar with the console page half empty below it -- the
  frame was shorter than the ledger table inside it.

## Cause

The invoice side posts its height to the console, which clamps it to
[480, 4000] and sizes the frame (`EmbeddedConsoleFrame`, platform repo). Both
halves work. The measurement was the problem.

`useEmbeddedAdminHeightSync` read `document.documentElement.scrollHeight` and
observed that same element. The embedded layout pins
`.portal-embedded-admin` to `min-height: 100vh`, so as soon as the console has
sized the frame once, the root element's own box **is** the viewport: it stops
growing, the ResizeObserver stops firing, and the reported height freezes at
whatever the page measured before its data arrived. Everything that renders
afterwards -- a ledger table filling in, a queue loading rows -- overflows into
the frame's own scrollbar instead of making the frame taller.

## Fix

`measureEmbeddedAdminHeight` (new, pure, in `embedded-admin-scope.ts`) takes
the larger of `documentElement.scrollHeight` and `body.scrollHeight`. The body
box does grow with the content, so the maximum keeps tracking. It returns 0
when neither box is usable, and the caller treats 0 as "nothing to report"
rather than posting a height that would clamp to the console's floor.

The hook now also observes `document.body`, posts only when the value actually
changed, and keeps a one-second re-measurement as a safety net for growth
neither observer reports (a late image decode, a font swap, a panel expanding
inside an already-sized box). A stable page costs one measurement per second
and sends no messages at all.

## Tests

`measureEmbeddedAdminHeight` gains four cases in
`web/src/lib/embedded-admin-scope.test.ts`: the body box wins when it is
taller, the root box still wins when it is, a missing or zero box is ignored
rather than reported, and an entirely unusable pair reports 0.
