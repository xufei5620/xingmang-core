import { afterAll, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import type { SourceAccount } from "./types";
import {
  loadUserInvoiceData,
  type UserDataRequestKey,
  type UserDataSetters,
  type UserDataSource,
} from "./lib/user-data-load";

// No jsdom / RTL in this repository (handoff risks 12); the panel is rendered
// to static markup with react-dom/server, which is enough to pin whether a
// piece of text is in the document. App.tsx and AuthProvider.tsx read
// window.location.href once at module scope, hence the stub before import.
vi.stubGlobal("window", { location: { href: "http://127.0.0.1/" } });
const { DataContext, SourceAccountStatus } = await import("./App");
const { AuthProvider } = await import("./AuthProvider");
afterAll(() => {
  vi.unstubAllGlobals();
});

// XM-INV-LOT-REASON-CONTRACT R10 / R11. What is pinned here, and why by
// rendering:
//
//  - R11: a refresh that fails as a whole (applyUserDataResults throwing) must
//    put the panel in its "本次读取失败" state, not the binding wizard. The
//    decision is unit-tested in sourceAccountPanelMode; what only a render can
//    check is that SourceAccountStatus feeds it the context's failedRequests
//    and that the "unavailable" branch really does not render the three steps.
//    The failedRequests value is produced by the REAL loadUserInvoiceData with
//    a throwing setter, so deleting the failure registration in its catch is
//    what turns this red (not a hand-written `failed: [...]`).
//  - R10: a row on a platform this bundle does not know still renders, with a
//    「未识别的平台」 badge, and does not turn the panel into the wizard.
//
// The absence assertions ("no wizard") are guarded by the positive control at
// the bottom, which renders the wizard on purpose using the same strings.

const WIZARD_HEADING = "关联平台账号";
const WIZARD_STEP = "打开原平台并使用原账号登录";
const UNAVAILABLE_HEADING = "已关联账号暂时无法读取";
const UNAVAILABLE_BODY = "这只是本次读取失败";
const CONNECTED_HEADING = "已关联的平台账号";
const UNKNOWN_PLATFORM_BADGE = "未识别的平台";

const ACCOUNTS: SourceAccount[] = [
  {
    id: "a1",
    source: "sub2api",
    sourceInstanceId: "10000000-0000-4000-8000-000000000001",
    sourceLabel: "SoloV API",
    externalUserIdMasked: "11**7",
    status: "verified",
  },
];

type PanelState = {
  sourceAccounts: SourceAccount[];
  failed: UserDataRequestKey[];
  loadError: string | null;
};

function makeSetters(): { state: PanelState; setters: UserDataSetters } {
  const state: PanelState = { sourceAccounts: [], failed: [], loadError: null };
  const setters: UserDataSetters = {
    setOrders: () => undefined,
    setProfiles: () => undefined,
    setSourceAccounts: (value) => (state.sourceAccounts = value),
    setEligibilitySummaries: () => undefined,
    setRequests: () => undefined,
    setSummary: () => undefined,
    setLoadError: (value) => (state.loadError = value),
    setFailed: (keys) => (state.failed = keys),
  };
  return { state, setters };
}

const api: UserDataSource = {
  getOrders: async () => [],
  getProfiles: async () => [],
  getSourceAccounts: async () => ACCOUNTS,
  getUserEligibilitySummary: async () => [],
  getUserRequestPage: async () => ({ items: [], nextCursor: undefined }),
};

function renderPanel(state: PanelState) {
  return renderToStaticMarkup(
    <AuthProvider>
      <DataContext.Provider
        value={{
          loading: false,
          orders: [],
          profiles: [],
          sourceAccounts: state.sourceAccounts,
          eligibilitySummaries: [],
          requests: [],
          summary: null,
          loadError: state.loadError,
          failedRequests: state.failed,
          refresh: async () => undefined,
          loadingMoreRequests: false,
          loadMoreRequests: async () => undefined,
        }}
      >
        <SourceAccountStatus />
      </DataContext.Provider>
    </AuthProvider>,
  );
}

describe("SourceAccountStatus after a refresh that failed as a whole (R11)", () => {
  it("shows the unavailable notice and not the binding wizard on a first load", async () => {
    const { state, setters } = makeSetters();
    // The five reads settle fine; the apply step throws. This is the path the
    // component's old catch handled with a banner and nothing else.
    await loadUserInvoiceData(api, {
      ...setters,
      setSourceAccounts: () => {
        throw new Error("boom");
      },
    });
    expect(state.sourceAccounts).toEqual([]);

    const html = renderPanel(state);
    expect(html).toContain(UNAVAILABLE_HEADING);
    expect(html).toContain(UNAVAILABLE_BODY);
    expect(html).toContain(CONNECTED_HEADING);
    expect(html).not.toContain(WIZARD_STEP);
    expect(html).not.toContain(`<h2>${WIZARD_HEADING}</h2>`);
  });

  it("keeps rendering last-good accounts, not a notice, when a later refresh fails as a whole", async () => {
    const { state, setters } = makeSetters();
    await loadUserInvoiceData(api, setters);
    expect(state.sourceAccounts).toEqual(ACCOUNTS);
    await loadUserInvoiceData(api, {
      ...setters,
      setOrders: () => {
        throw new Error("boom");
      },
    });
    const html = renderPanel(state);
    expect(html).toContain("SoloV API");
    expect(html).toContain("11**7");
    expect(html).not.toContain(UNAVAILABLE_HEADING);
    expect(html).not.toContain(WIZARD_STEP);
  });
});

describe("SourceAccountStatus with a row on a platform this bundle does not know (R10)", () => {
  it("lists the row with the unknown-platform badge instead of the wizard", () => {
    const html = renderPanel({
      sourceAccounts: [
        {
          id: "u1",
          source: "thirdapi",
          sourceInstanceId: "10000000-0000-4000-8000-000000000009",
          sourceLabel: UNKNOWN_PLATFORM_BADGE,
          externalUserIdMasked: "99**1",
          status: "verified",
        },
      ],
      failed: [],
      loadError: null,
    });
    expect(html).toContain(CONNECTED_HEADING);
    expect(html).toContain("99**1");
    expect(html).toMatch(
      new RegExp(`<span class="badge badge-neutral">${UNKNOWN_PLATFORM_BADGE}</span>`),
    );
    expect(html).not.toContain(WIZARD_STEP);
    expect(html).not.toContain(UNAVAILABLE_HEADING);
  });

  it("still labels the known platforms by name", () => {
    const html = renderPanel({ sourceAccounts: ACCOUNTS, failed: [], loadError: null });
    expect(html).toContain('<span class="badge badge-cyan">SoloV API</span>');
    expect(html).not.toContain(UNKNOWN_PLATFORM_BADGE);
  });
});

describe("positive control: the wizard strings above really are what the wizard renders", () => {
  it("renders the wizard when the accounts request succeeded with nothing", () => {
    const html = renderPanel({ sourceAccounts: [], failed: [], loadError: null });
    expect(html).toContain(`<h2>${WIZARD_HEADING}</h2>`);
    expect(html).toContain(WIZARD_STEP);
    expect(html).not.toContain(UNAVAILABLE_HEADING);
  });
});
