import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { OpsOverview } from "../api/ops";
import { ChangesPage } from "./ChangesPage";

function fakeResponse(body: unknown, status = 200): Response {
  return {
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(typeof body === "string" ? body : JSON.stringify(body)),
  } as unknown as Response;
}

/** chi 对没有挂载的路由回**纯文本** 404，解析不出 `error.code`——
 *  api/client.ts 的 looksLikeUnmountedRoute 靠这层结构差异判「整组端点不存在」。
 *  逐字模拟它，否则测的就不是真实的「这套后台没接审批中心」形态。 */
function bareNotFound(): Response {
  return {
    ok: false,
    status: 404,
    json: () => Promise.reject(new Error("not json")),
    text: () => Promise.resolve("404 page not found"),
  } as unknown as Response;
}

const FRESHNESS = {
  state: "fresh" as const,
  staleness_seconds: 5,
  threshold_seconds: 120,
  is_partial: false,
  observed_at: "2026-09-07T03:04:05Z",
  last_success: "2026-09-07T03:04:05Z",
  last_error_code: "",
};

/** build 三行按生产真实形状造：BUILD_VERSION 被 deploy-local.sh 写成**环境名**
 *  （`export BUILD_VERSION="$expected_environment"`），不是语义化版本号。
 *  fixture 若写成 "1.2.3"，「别把它当版本号」这条断言就测不到真实场景。 */
function opsOverview(build?: Partial<OpsOverview["build"]>): OpsOverview {
  return {
    build: { version: "production", commit: "9f3c1ab", environment: "production", ...build },
    worker_heartbeat: {
      metric_key: "platform.heartbeat",
      source: "platform-worker",
      value: {},
      freshness: FRESHNESS,
    },
    sync_pipelines: [],
    connector_health: [],
    alert_delivery: { telegram_configured: true, webhook_configured: false },
    retention: {
      metric_key: "platform.retention.last_run",
      source: "platform-worker",
      value: {},
      freshness: FRESHNESS,
    },
    database: { connected: true },
  };
}

function approvalListBody() {
  return { items: [], limit: 1, truncated: false };
}

/** 按 URL 路由的 fetch 桩：这一页不同子页签打不同端点，一个恒定返回的桩会让
 *  「哪一格发了哪个请求」测不出来。 */
function stubFetch(handler: (url: string) => Response) {
  const fetchImpl = vi.fn((input: string) => Promise.resolve(handler(input)));
  vi.stubGlobal("fetch", fetchImpl);
  return fetchImpl;
}

function defaultHandler(url: string): Response {
  if (url.includes("/api/v1/approvals")) return fakeResponse(approvalListBody());
  if (url.includes("/api/v1/ops/overview")) return fakeResponse(opsOverview());
  throw new Error(`测试没有为这个地址准备响应：${url}`);
}

function renderChanges(initialEntry = "/changes") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <ChangesPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function urls(fetchImpl: ReturnType<typeof stubFetch>): string {
  return JSON.stringify(fetchImpl.mock.calls);
}

afterEach(() => vi.unstubAllGlobals());

describe("版本与发布页", () => {
  it("五格页签都在，默认落在变更单", async () => {
    stubFetch(defaultHandler);
    renderChanges();

    for (const label of [
      "变更单",
      "发布与回滚",
      "自动测试与质量",
      "发布包与安全检查",
      "数据库变更",
    ]) {
      expect(screen.getByRole("tab", { name: label })).toBeTruthy();
    }
    expect(screen.getByRole("tab", { name: "变更单", selected: true })).toBeTruthy();
    expect(await screen.findByText(/审批中心已在本环境启用/)).toBeTruthy();
  });

  it("变更单格只探审批中心通不通：打的是 approvals、不打 ops，也不显示队列条数", async () => {
    const fetchImpl = stubFetch(defaultHandler);
    renderChanges();

    await screen.findByText(/审批中心已在本环境启用/);
    // 只发一次请求，且是审批中心的探测（limit=1）。用「只发了一次」这个正向
    // 断言代替「没打 ops/overview」的缺席断言：后者在页面整体渲染失败时也会绿。
    await waitFor(() => expect(fetchImpl).toHaveBeenCalledTimes(1));
    expect(urls(fetchImpl)).toContain("/api/v1/approvals?limit=1");
    expect(
      screen.getByText(/这一页因此不复制队列，也不显示待审批条数/),
    ).toBeTruthy();
    expect(screen.getByRole("link", { name: /待审批/ }).getAttribute("href")).toBe(
      "/actions?sub=pending",
    );
  });

  it("变更单格说清审批中心管的是 Action 执行审批，不是这一格的变更单台账", async () => {
    stubFetch(defaultHandler);
    renderChanges("/changes?sub=requests");

    expect(await screen.findByText(/XM-0030 · 2026-09-07 在本后台启用/)).toBeTruthy();
    expect(screen.getByText(/它回答的是「这一次写操作要不要放行」/)).toBeTruthy();
    expect(
      screen.getByText(/所以审批中心接上并不等于这一格接上了——它覆盖的是审批链，不覆盖变更单台账/),
    ).toBeTruthy();
    // 落款说清「在等什么」：等的是产品负责人裁定，不是排期。
    expect(screen.getByText(/待产品负责人裁定——这一格等的是那个裁定，不是排期/)).toBeTruthy();
  });

  it("审批中心没挂载时说的是「这个部署还停在接上之前」，不说已启用", async () => {
    stubFetch((url) => {
      if (url.includes("/api/v1/approvals")) return bareNotFound();
      return defaultHandler(url);
    });
    renderChanges("/changes?sub=requests");

    expect(await screen.findByText("这套后台还没接审批中心")).toBeTruthy();
    expect(screen.getByText(/更可能是这个部署还停在接上之前的提交/)).toBeTruthy();
    // 缺席断言，已做变异验证：把「已启用」那一行改成无条件渲染之后，
    // 红的正是这一条（而不是上面两条正向断言）。
    expect(screen.queryByText(/审批中心已在本环境启用/)).toBeNull();
  });

  it("发布与回滚接的是当前部署，且把构建标签与版本号分清", async () => {
    const fetchImpl = stubFetch(defaultHandler);
    renderChanges("/changes?sub=releases");

    expect(await screen.findByText("9f3c1ab")).toBeTruthy();
    expect(urls(fetchImpl)).toContain("/api/v1/ops/overview");
    // 主数位来自 build.version，但它今天是环境名——标签叫「构建标签」并就地说明。
    expect(screen.getByText(/部署脚本今天把它写成环境名，不是语义化版本号/)).toBeTruthy();
    expect(screen.getByText(/这是「现在跑的是什么」，不是发布历史/)).toBeTruthy();
    expect(screen.getByText(/与「运行保障 → 控制平面健康」读的是同一个端点/)).toBeTruthy();
    expect(screen.getByRole("link", { name: /控制平面健康/ }).getAttribute("href")).toBe(
      "/ops?sub=health",
    );
  });

  it("构建信息取不到时说清是取不到，不说成没发布过", async () => {
    stubFetch((url) => {
      if (url.includes("/api/v1/ops/overview")) {
        return fakeResponse(opsOverview({ commit: "", version: "", environment: "" }));
      }
      return defaultHandler(url);
    });
    renderChanges("/changes?sub=releases");

    expect(await screen.findByText(/这次部署没有注入 BUILD_COMMIT/)).toBeTruthy();
    expect(screen.getByText(/空值说明取不到构建信息，不代表没有发布过/)).toBeTruthy();
  });

  it("自动测试与质量说清 Actions 停摆与复审期限，且不发请求", async () => {
    const fetchImpl = stubFetch(defaultHandler);
    renderChanges("/changes?sub=tests");

    expect(await screen.findByText(/有效期至 2026-09-12，产品负责人须在该日前复审/)).toBeTruthy();
    expect(screen.getByText(/它等的是一次复审，不是排期/)).toBeTruthy();
    // 后台任务不是 CI：这句话防的是拿 /jobs 的运行记录顶这一格。
    expect(screen.getByText(/后台任务页的运行记录不是 CI/)).toBeTruthy();
    // 「不发请求」同样做过变异验证：给 BlueprintOnlyTab 加一个 useQuery 之后，
    // 这三格的这条断言都会红——它不是一条恒真的断言。
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("发布包与安全检查说清整条供应链环节都不存在，且不发请求", async () => {
    const fetchImpl = stubFetch(defaultHandler);
    renderChanges("/changes?sub=packages");

    expect(await screen.findByText(/全仓找不到 cosign \/ syft \/ trivy \/ grype 的调用/)).toBeTruthy();
    expect(screen.getByText(/开票线是另一条独立流水线/)).toBeTruthy();
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("数据库变更说清等的是一条只读 Query，而不是 Foundation-B", async () => {
    const fetchImpl = stubFetch(defaultHandler);
    renderChanges("/changes?sub=database");

    expect(await screen.findByText(/这一格等的是一条只读 Query，不是 Foundation-B/)).toBeTruthy();
    expect(screen.getByText(/public.schema_migrations 也确实记着已应用到哪一版/)).toBeTruthy();
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("四张统计格主数位是「—」，并逐格说清今天没有来源", async () => {
    stubFetch(defaultHandler);
    renderChanges();

    for (const label of ["待评审变更", "等待生产批准", "自动测试失败", "供应链异常"]) {
      const tile = screen.getByTitle(label).closest("article");
      expect(tile).toBeTruthy();
      expect(within(tile as HTMLElement).getByText("—")).toBeTruthy();
      expect(within(tile as HTMLElement).getByText(/今天没有来源/)).toBeTruthy();
    }
    // 「等待生产批准」不能拿审批中心顶：这一格要的是发布批准。
    expect(
      screen.getByText(/审批中心里的单是 Action 执行审批，不是发布批准，不能拿来充数/),
    ).toBeTruthy();
  });

  it("五格的落款不再把阻塞一律归给 Foundation-B", async () => {
    stubFetch(defaultHandler);
    for (const sub of ["requests", "releases", "tests", "packages", "database"]) {
      const { unmount } = renderChanges(`/changes?sub=${sub}`);
      // 缺席断言，已做变异验证：蓝图数据文件里那五句「随 Foundation-B
      // （XM-0030）上线」是错归因（审批中心 2026-09-07 已启用，五格没有一格
      // 在等它）。把 honestBlueprintTab 改成直接返回蓝图原样之后这条会红。
      await waitFor(() => expect(screen.queryByText(/随 Foundation-B/)).toBeNull());
      unmount();
    }
  });

  it("未知子页不静默回落到变更单", async () => {
    const fetchImpl = stubFetch(defaultHandler);
    renderChanges("/changes?sub=not-a-real-tab");

    expect(await screen.findByText("「not-a-real-tab」子页尚未接入")).toBeTruthy();
    expect(screen.getByRole("link", { name: "返回变更单" }).getAttribute("href")).toBe(
      "/changes?sub=requests",
    );
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});
