import { afterAll, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import type { UserEligibilitySummary } from "./types";

// The repository has no jsdom / RTL (adding either is a separate decision, see
// the handoff's risks 12), so the panel is rendered to static markup with
// react-dom/server. That is enough for what is being pinned: whether a piece
// of text is in the rendered document or not.
//
// App.tsx and AuthProvider.tsx read window.location.href once at module scope,
// so a minimal window is stubbed before the module is imported. Nothing else
// on the import chain touches the browser at load time.
vi.stubGlobal("window", { location: { href: "http://127.0.0.1/" } });
const { EligibilitySummaryPanel } = await import("./App");
afterAll(() => {
  vi.unstubAllGlobals();
});

//
// Why this test exists: the summary's `eligibilityDegraded` was computed and
// unit-tested through two review rounds while the panel never rendered it, so
// "the user sees a degraded badge instead of a red banner" was written down as
// a fact with no code behind it. Both directions are asserted, and the absent
// direction was mutation-checked (badge made unconditional -> red).

const DEGRADED_BADGE = "账本状态待确认";

function summary(
  overrides: Partial<UserEligibilitySummary> = {},
): UserEligibilitySummary {
  return {
    source: "sub2api",
    sourceInstanceId: "10000000-0000-4000-8000-000000000001",
    sourceLabel: "SoloV API",
    bindingStatus: "verified",
    status: "active",
    currency: "CNY",
    availableMinor: 12_300,
    consumedMinor: 12_300,
    unconsumedMinor: 0,
    reservedMinor: 0,
    issuedMinor: 0,
    legacyNoninvoiceable: { serviceUnits: "0", unitCode: "SUB2_BALANCE_1E8" },
    noncash: { serviceUnits: "0", unitCode: "SUB2_BALANCE_1E8" },
    reasons: ["READY"],
    eligibilityDegraded: false,
    ...overrides,
  };
}

function render(items: UserEligibilitySummary[]) {
  return renderToStaticMarkup(
    <EligibilitySummaryPanel items={items} loading={false} />,
  );
}

describe("EligibilitySummaryPanel: the degraded badge", () => {
  it("renders the badge when the summary is degraded", () => {
    const html = render([summary({ eligibilityDegraded: true })]);
    expect(html).toContain(DEGRADED_BADGE);
    // It is a badge next to the status badge, not free text somewhere.
    expect(html).toMatch(
      new RegExp(`<span class="badge badge-amber">${DEGRADED_BADGE}</span>`),
    );
  });

  it("renders the badge for a degraded row even when its status is a known one", () => {
    // The unknown-envelope-key case: status and reasons are perfectly normal,
    // the only signal that this bundle is behind the backend is the flag.
    const html = render([
      summary({ status: "active", reasons: ["READY"], eligibilityDegraded: true }),
    ]);
    expect(html).toContain(DEGRADED_BADGE);
  });

  it("does not render the badge when the summary is not degraded", () => {
    const html = render([
      summary({ eligibilityDegraded: false }),
      summary({
        sourceInstanceId: "10000000-0000-4000-8000-000000000002",
        eligibilityDegraded: undefined,
      }),
    ]);
    // Guard against a trivially-true absence: the cards did render.
    expect(html).toContain("SoloV API");
    expect(html).toContain("当前可开");
    expect(html).not.toContain(DEGRADED_BADGE);
  });

  it("renders the badge only on the degraded rows of a mixed list", () => {
    const html = render([
      summary({ eligibilityDegraded: false }),
      summary({
        sourceInstanceId: "10000000-0000-4000-8000-000000000002",
        source: "newapi",
        sourceLabel: "SoloV 模型平台",
        eligibilityDegraded: true,
      }),
    ]);
    expect(html.split(DEGRADED_BADGE)).toHaveLength(2);
    const [first, second] = html.split("</article>");
    expect(first).not.toContain(DEGRADED_BADGE);
    expect(second).toContain(DEGRADED_BADGE);
  });
});
