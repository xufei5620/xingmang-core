import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PlatformCredentialsPanel } from "./PlatformCredentialsPanel";

function response(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function service(overrides: Record<string, unknown> = {}) {
  return {
    id: "service-newapi-1",
    service_type: "newapi",
    instance_id: "newapi-prod-a",
    environment: "production",
    endpoint: "https://newapi.example.invalid",
    owner: "平台运营组",
    status: "active",
    source_watermark: "wm-20260829-01",
    observed_at: "2026-08-29T08:30:00Z",
    stale_seconds: 90,
    ...overrides,
  };
}

function renderPanel(platform = "newapi") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <PlatformCredentialsPanel platform={platform} />
    </QueryClientProvider>,
  );
}

describe("平台连接与凭据", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("只展示 service_type 精确匹配的平台实例及最近一次真实观测", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: unknown) => {
        // 探测历史另有一张表（XM-CREDS-TAB-PROBE），这条测试只关心实例表。
        if (String(input).includes("/metrics/history")) {
          return Promise.resolve(response({ items: [] }));
        }
        return Promise.resolve(
          response({
            items: [
              service(),
              service({
                id: "service-sub2api-1",
                service_type: "sub2api",
                instance_id: "sub2api-prod-a",
                endpoint: "https://sub2api.example.invalid",
              }),
            ],
          }),
        );
      }),
    );

    renderPanel();

    expect(await screen.findByText("newapi-prod-a")).toBeTruthy();
    expect(screen.queryByText("sub2api-prod-a")).toBeNull();
    const table = within(
      screen.getByRole("table", { name: /已登记连接实例/ }),
    );
    expect(table.getByText("https://newapi.example.invalid")).toBeTruthy();
    expect(table.getByText("平台运营组")).toBeTruthy();
    expect(table.getByText("数据新鲜")).toBeTruthy();
    expect(table.getByText(/2026-08-29 08:30:00 UTC/)).toBeTruthy();
  });

  it("凭据引用列出本平台声明需要的引用及其配置状态与指纹（XM-CREDS-TAB-REFS）", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: unknown) => {
        const url = String(input);
        if (url.includes("/credentials/expected")) {
          return Promise.resolve(
            response({
              items: [
                {
                  credential_ref: "secret://newapi/readonly-token",
                  platform: "newapi",
                  purpose: "NewAPI 管理员 access token",
                  configured: true,
                },
                {
                  credential_ref: "secret://newapi/revenue-db",
                  platform: "newapi",
                  purpose: "NewAPI 收入库只读口令",
                  configured: false,
                },
                {
                  credential_ref: "secret://sub2api-prod/read-token",
                  platform: "sub2api",
                  purpose: "Sub2API admin x-api-key",
                  configured: true,
                },
              ],
            }),
          );
        }
        if (url.includes("/api/v1/credentials")) {
          return Promise.resolve(
            response({
              items: [
                {
                  credential_ref: "secret://newapi/readonly-token",
                  scope: "newapi",
                  updated_at: "2026-09-01T02:03:04Z",
                  fingerprint: "sha256:abcdef0123456789",
                  version: 3,
                  available: true,
                  revoked: false,
                },
              ],
            }),
          );
        }
        return Promise.resolve(response({ items: [service()] }));
      }),
    );

    renderPanel();

    await screen.findByText("newapi-prod-a");
    const credential = (await screen.findByRole("heading", { name: "凭据引用", level: 3 })).closest(
      "section",
    ) as HTMLElement;
    // 只列本平台的引用：sub2api 那条不在这一页。
    expect(await within(credential).findByText("secret://newapi/readonly-token")).toBeTruthy();
    expect(within(credential).getByText("secret://newapi/revenue-db")).toBeTruthy();
    expect(within(credential).queryByText("secret://sub2api-prod/read-token")).toBeNull();
    expect(within(credential).getByText("已配置")).toBeTruthy();
    expect(within(credential).getByText("未配置")).toBeTruthy();
    // 指纹只显示前缀，永远不是值本身。
    expect(within(credential).getByText("sha256:abcdef01…")).toBeTruthy();
    expect(within(credential).getByText("3")).toBeTruthy();
    // 统计卡说的是「已配置 / 本平台声明需要的总数」。
    expect(screen.getByText("1 / 2")).toBeTruthy();
    expect(screen.getByText("有未配置")).toBeTruthy();
  });

  it("没有 credential.manage 权限时仍显示配置状态，只是没有指纹与版本", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: unknown) => {
        const url = String(input);
        if (url.includes("/credentials/expected")) {
          return Promise.resolve(
            response({
              items: [
                {
                  credential_ref: "secret://newapi/revenue-db",
                  platform: "newapi",
                  purpose: "NewAPI 收入库只读口令",
                  configured: false,
                },
              ],
            }),
          );
        }
        if (url.includes("/api/v1/credentials")) {
          return Promise.resolve(response({ message: "缺少权限 credential.manage" }, 403));
        }
        return Promise.resolve(response({ items: [service()] }));
      }),
    );

    renderPanel();

    const credential = (await screen.findByRole("heading", { name: "凭据引用", level: 3 })).closest(
      "section",
    ) as HTMLElement;
    expect(await within(credential).findByText("secret://newapi/revenue-db")).toBeTruthy();
    expect(within(credential).getByText("未配置")).toBeTruthy();
    expect(await within(credential).findByText(/credential\.manage/)).toBeTruthy();
  });

  // XM-CREDS-TAB-PROBE：这一格以前写着"独立探测历史 Query 尚未提供"，
  // 而 /metrics/history 一直都在。占位比缺功能更糟——它让人以为没做。
  function probeSample(value: Record<string, unknown>, overrides: Record<string, unknown> = {}) {
    return {
      observed_at: "2026-09-06T01:00:00Z",
      synced_at: "2026-09-06T01:00:05Z",
      source: "connector",
      status: "ok",
      is_partial: false,
      watermark: "fp-1",
      last_error_code: "",
      value,
      ...overrides,
    };
  }

  function stubWithProbes(items: unknown[], probeStatus = 200) {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: unknown) => {
        const url = String(input);
        if (url.includes("/metrics/history")) {
          return Promise.resolve(
            probeStatus === 200
              ? response({ items })
              : response(
                  // 统一错误体形状（规格 §18.4）；写成 {message} 拿不到
                  // missingScope，测出来的就只是"HTTP 403"而不是"缺哪个权限"。
                  { error: { code: "forbidden", message: "缺少权限 ops.read", request_id: "req-1" } },
                  probeStatus,
                ),
          );
        }
        return Promise.resolve(response({ items: [service()] }));
      }),
    );
  }

  it("探测历史显示上游版本、兼容矩阵判定、结果与延迟，最近一次在前", async () => {
    stubWithProbes([
      probeSample({ version: "1.0.0", supported: true, healthy: true, kind: "", latency_ms: 20 }),
      probeSample(
        { version: "1.1.0", supported: true, healthy: false, kind: "auth", latency_ms: 35 },
        { synced_at: "2026-09-06T02:00:05Z", status: "failed", last_error_code: "auth" },
      ),
    ]);

    renderPanel();

    const probes = (await screen.findByRole("heading", { name: "健康探测历史", level: 3 })).closest(
      "article",
    ) as HTMLElement;
    // 标题在 ApiStateView 外面：等到标题不等于查询已落地，要等表里的值。
    await within(probes).findByText("1.1.0");
    const rows = within(probes).getAllByRole("row");
    // 表头一行 + 两条样本；最近一次（1.1.0）排在第一条数据行。
    expect(rows.length).toBe(3);
    expect(within(rows[1] as HTMLElement).getByText("1.1.0")).toBeTruthy();
    expect(within(rows[1] as HTMLElement).getByText("不健康 · auth")).toBeTruthy();
    expect(within(rows[1] as HTMLElement).getByText("35 ms")).toBeTruthy();
    expect(within(rows[2] as HTMLElement).getByText("1.0.0")).toBeTruthy();
    // 上游版本这个事实本身出现在统计卡上——以前整个控制台只有"变化时告警"，
    // 没告警时无从确认现在是什么版本。统计卡与表格各有一个徽章，取统计卡那个。
    // 统计卡的标签是 h3（StatTile），表头的"上游版本"不是——按 role 取才唯一。
    const tile = screen
      .getByRole("heading", { name: "上游版本", level: 3 })
      .closest("article") as HTMLElement;
    expect(within(tile).getByText("1.1.0")).toBeTruthy();
    expect(within(tile).getByText("矩阵已声明支持")).toBeTruthy();
  });

  it("矩阵未声明支持时说清这是矩阵没跟上，不是上游坏了", async () => {
    stubWithProbes([
      probeSample({ version: "2.0.0", supported: false, healthy: true, kind: "", latency_ms: 8 }),
    ]);

    renderPanel();

    expect((await screen.findAllByText("矩阵未声明支持")).length).toBeGreaterThan(0);
    // 统计卡的说明要讲清这是矩阵没跟上，不是上游坏了。
    expect(screen.getByText(/不等于上游坏了/)).toBeTruthy();
    // 探测到的版本号要出现在统计卡（值 + 说明里点名它）与探测表里。
    const tile = screen
      .getByRole("heading", { name: "上游版本", level: 3 })
      .closest("article") as HTMLElement;
    expect(within(tile).getByText("2.0.0")).toBeTruthy();
    expect(within(tile).getByText(/没有覆盖 2\.0\.0/)).toBeTruthy();
    // 上游本身是健康的，不能因为矩阵陈旧就说它不健康。
    const probes = screen.getByRole("heading", { name: "健康探测历史", level: 3 }).closest(
      "article",
    ) as HTMLElement;
    expect(within(probes).getByText("健康")).toBeTruthy();
    expect(within(probes).getByText("2.0.0")).toBeTruthy();
  });

  it("没有探测记录时说明探测只在真实对接下运行，不编一个版本号出来", async () => {
    stubWithProbes([]);

    renderPanel();

    const probes = (await screen.findByRole("heading", { name: "健康探测历史", level: 3 })).closest(
      "article",
    ) as HTMLElement;
    expect(await within(probes).findByText(/近 24 小时没有 NewAPI 的探测记录/)).toBeTruthy();
    expect(within(probes).getByText(/这不是故障/)).toBeTruthy();
    // 统计卡上不能出现任何矩阵判定徽章——包括"未知"。一次都没探测过
    // 与"探测了但没给判定"是两件事，把前者显示成后者就是在编事实。
    const tile = screen
      .getByRole("heading", { name: "上游版本", level: 3 })
      .closest("article") as HTMLElement;
    expect(within(tile).queryByText("矩阵已声明支持")).toBeNull();
    expect(within(tile).queryByText("矩阵未声明支持")).toBeNull();
    expect(within(tile).queryByText("未知")).toBeNull();
    expect(within(tile).getByText(/没有探测记录/)).toBeTruthy();
  });

  it("没有 ops.read 权限时只有这一格降级，实例表照常显示", async () => {
    stubWithProbes([], 403);

    renderPanel();

    // 整页没有跟着塌掉：实例表还在。
    expect(await screen.findByText("newapi-prod-a")).toBeTruthy();
    const probes = screen.getByRole("heading", { name: "健康探测历史", level: 3 }).closest(
      "article",
    ) as HTMLElement;
    expect(await within(probes).findByText(/ops\.read/)).toBeTruthy();
  });

  it("平台没有登记实例时显示有行动指向的空态，不渲染空表", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: unknown) =>
        Promise.resolve(
          String(input).includes("/metrics/history")
            ? response({ items: [] })
            : response({ items: [service({ service_type: "sub2api" })] }),
        ),
      ),
    );

    renderPanel();

    expect(await screen.findByText("NewAPI 还没有登记实例")).toBeTruthy();
    expect(screen.getByText(/平台治理.*服务注册表/)).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });
});
