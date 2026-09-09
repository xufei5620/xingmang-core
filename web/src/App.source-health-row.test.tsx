import { afterAll, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import type { SourceStreamHealth } from "./types";

// No jsdom / RTL in this repository; the row is rendered to static markup with
// react-dom/server, which is enough to pin whether a piece of text reaches the
// document. App.tsx reads window.location.href once at module scope, hence the
// stub before the import.
vi.stubGlobal("window", { location: { href: "http://127.0.0.1/" } });
const { SourceStreamHealthRow } = await import("./App");
afterAll(() => {
  vi.unstubAllGlobals();
});

// XM-INV-DEAD-CONTAINMENT. Two visible changes shipped with no coverage at
// all, and the second one is the kind that a test can very easily pretend to
// cover:
//
//   - the contained-dead count next to the dead count;
//   - the diagnosis line, which used to render only when the stream was NOT
//     ready. A contained dead event leaves the stream READY, which is the
//     entire point of the slice -- so under the old condition the
//     EVENTS_DEAD_CONTAINED label existed in the label map and could never
//     appear on screen. A test that only checked "the label map has an entry"
//     would have passed against a screen that never showed it.
//
// So the third case below asserts the copy in rendered markup for a stream
// with ready=true, which is the one combination that separates the new
// condition from the old one.

const CONTAINED_LABEL =
  "存在死信事件（已由账号冻结兜住，不影响同来源其他客户）";
const UNCONTAINED_LABEL = "存在死信事件";

function row(overrides: Partial<SourceStreamHealth> = {}): SourceStreamHealth {
  return {
    sourceInstanceId: "10000000-0000-4000-8000-000000000001",
    sourceType: "sub2api",
    sourceName: "SoloV API",
    sourceEnabled: true,
    streamId: "usage",
    sequence: 12,
    approvedRuntimeVersion: "0.3.0",
    cutoverRuntimeVersion: "0.3.0",
    observedRuntimeVersion: "0.3.0",
    observedAgentVersion: "0.3.0",
    projectionStatus: "healthy",
    lastAcceptedAt: "2026-09-09T05:00:00Z",
    lastNonemptyBatchAt: "2026-09-09T05:00:00Z",
    economicWatermarkAt: "2026-09-09T05:00:00Z",
    maximumAgeSeconds: 900,
    economicWatermarkMaximumAgeSeconds: 900,
    pendingEvents: 0,
    deadEvents: 0,
    containedDeadEvents: 0,
    waitingDependencies: 0,
    ready: true,
    reasons: [],
    ...overrides,
  };
}

function render(item: SourceStreamHealth): string {
  return renderToStaticMarkup(
    <table>
      <tbody>
        <SourceStreamHealthRow item={item} />
      </tbody>
    </table>,
  );
}

describe("SourceStreamHealthRow", () => {
  it("shows how many of the dead events are already contained", () => {
    const markup = render(row({ deadEvents: 3, containedDeadEvents: 2 }));
    expect(markup).toContain("死信 3");
    expect(markup).toContain("已兜住 2");
  });

  it("says nothing about containment when no dead event is contained", () => {
    // The positive control for the assertion above: the same row with the
    // count at zero must render the dead total and NOT the contained phrase,
    // so "已兜住" appearing is a fact about the data and not about the
    // component always printing it.
    const markup = render(row({ deadEvents: 3, containedDeadEvents: 0, ready: false }));
    expect(markup).toContain("死信 3");
    expect(markup).not.toContain("已兜住");
  });

  it("shows the contained-dead diagnosis on a stream that is still ready", () => {
    // The load-bearing case. ready=true is what the slice buys, and it is
    // also what the previous render condition used to hide the diagnosis
    // behind -- so this is the one arm that fails if the condition goes back
    // to `!item.ready`.
    const markup = render(
      row({
        ready: true,
        deadEvents: 1,
        containedDeadEvents: 1,
        reasons: ["EVENTS_DEAD_CONTAINED"],
      }),
    );
    expect(markup).toContain(CONTAINED_LABEL);
  });

  it("still shows a blocked stream's reasons", () => {
    const markup = render(
      row({ ready: false, deadEvents: 1, containedDeadEvents: 0, reasons: ["EVENTS_DEAD"] }),
    );
    expect(markup).toContain(UNCONTAINED_LABEL);
  });

  it("renders an unknown readiness reason without inventing a label for it", () => {
    const markup = render(
      row({ ready: false, reasons: ["SOMETHING_THIS_BUNDLE_HAS_NEVER_HEARD_OF"] }),
    );
    expect(markup).toContain("未识别的安全阻断原因");
  });
});
