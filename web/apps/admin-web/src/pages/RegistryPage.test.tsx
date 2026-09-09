import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RegistryPage } from "./RegistryPage";

// 资源目录页。本文件钉三件事：
//
//  1. **六个子页签真的渲染出来，而且 ?sub= 决定看到哪一格**。这一条是回归
//     测试：信息架构给 /registry 声明了六个子页签，而这一页此前根本不渲染
//     Tabs——`?sub=connectors` 静默显示服务表。地址栏说你在看连接器、屏幕上
//     给的是服务，比缺功能更糟。
//  2. 连接器与连接两格取的是各自的端点，外键 UUID 换成人认得出的名字；
//  3. 三处空值语义（从未核验 / 无写通道 / 未声明兼容版本）不被显示成一个
//     看起来正常的值。

function fakeResponse(body: unknown, status = 200): Response {
  return {
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(typeof body === "string" ? body : JSON.stringify(body)),
  } as unknown as Response;
}

const SERVICE_ID = "33333333-3333-4333-8333-333333333333";
const CONNECTOR_ID = "11111111-1111-4111-8111-111111111111";

/** 只出现在**服务**表里的列头。用它做「这一格是不是服务表」的判据——
 *  实例名之类的值会被连接表借去做名字映射，分不清是哪一格渲染的。 */
const SERVICES_ONLY = "接入地址";
/** 只出现在**连接器**表里的列头。 */
const CONNECTORS_ONLY = "契约版本";
/** 只出现在**连接**表里的列头。 */
const CONNECTIONS_ONLY = "凭据引用";

function serviceItem() {
  return {
    id: SERVICE_ID,
    service_type: "sub2api",
    instance_id: "sub2api-prod",
    environment: "development",
    endpoint: "https://api.example.test",
    owner: "staff_alice",
    status: "active",
    source_watermark: "w-1",
    observed_at: "2026-09-08T03:04:05Z",
    stale_seconds: 60,
  };
}

function connectorItem(overrides: Record<string, unknown> = {}) {
  return {
    id: CONNECTOR_ID,
    key: "sub2api",
    version: "0.1.0",
    contract_version: "v1",
    connection_schema_path: "contracts/connectors/sub2api.v1.json",
    target_allowlist: ["api.example.test"],
    read_capabilities: ["sub2api.user.list"],
    write_capabilities: ["sub2api.user.update"],
    supported_upstream_versions: ["0.1.179"],
    compatibility_test_path: "",
    created_at: "2026-09-01T03:04:05Z",
    updated_at: "2026-09-02T03:04:05Z",
    ...overrides,
  };
}

function connectionItem(overrides: Record<string, unknown> = {}) {
  return {
    id: "22222222-2222-4222-8222-222222222222",
    connector_id: CONNECTOR_ID,
    service_id: SERVICE_ID,
    environment: "development",
    credential_ref: "secret://sub2api/dev-token",
    target_allowlist: ["api.example.test"],
    granted_capabilities: ["sub2api.user.list"],
    kill_switch: "",
    status: "enabled",
    detected_upstream_version: "0.1.179",
    version_fingerprint: "sha256:abc",
    last_verified_at: null,
    created_at: "2026-09-01T03:04:05Z",
    updated_at: "2026-09-02T03:04:05Z",
    ...overrides,
  };
}

interface Fixture {
  services?: unknown[];
  connectors?: unknown[];
  connections?: unknown[];
}

/** 按 URL 路由的 fetch 桩。这一页同时打四个端点，一个恒定返回的桩会让
 *  「哪一格取的是哪个端点」测不出来。 */
function stubFetch(fixture: Fixture = {}) {
  const fetchImpl = vi.fn((input: string) => {
    if (input.includes("/api/v1/connectors")) {
      return Promise.resolve(fakeResponse({ items: fixture.connectors ?? [connectorItem()] }));
    }
    if (input.includes("/api/v1/connections")) {
      return Promise.resolve(fakeResponse({ items: fixture.connections ?? [connectionItem()] }));
    }
    if (input.includes("/api/v1/services")) {
      return Promise.resolve(fakeResponse({ items: fixture.services ?? [serviceItem()] }));
    }
    if (input.includes("/api/v1/metrics")) {
      return Promise.resolve(fakeResponse({ items: [] }));
    }
    throw new Error(`测试没有为这个地址准备响应：${input}`);
  });
  vi.stubGlobal("fetch", fetchImpl);
  return fetchImpl;
}

function renderRegistry(initialEntry = "/registry") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <RegistryPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function urls(fetchImpl: ReturnType<typeof stubFetch>): string {
  return JSON.stringify(fetchImpl.mock.calls);
}

afterEach(() => vi.unstubAllGlobals());

describe("资源目录页 · 子页签", () => {
  it("六个子页签都在，默认落在服务", async () => {
    stubFetch();
    renderRegistry();

    for (const label of ["服务", "连接器", "连接", "支持能力", "应用与模块", "环境"]) {
      expect(screen.getByRole("tab", { name: label })).toBeTruthy();
    }
    expect(screen.getByRole("tab", { name: "服务", selected: true })).toBeTruthy();
    expect(await screen.findByText(SERVICES_ONLY)).toBeTruthy();
  });

  // 这一条是本片修的那个 bug 的回归测试。
  //
  // 修之前：这一页不渲染 Tabs，`?sub=connectors` 与 `?sub=` 渲染的是同一屏
  // ——服务表。所以判据必须是**两个方向都断言**：连接器表在、服务表不在。
  // 只断言前者的话，一个「六格都渲染服务表」的实现照样绿。
  it("?sub=connectors 显示的是连接器表，不是服务表", async () => {
    stubFetch();
    renderRegistry("/registry?sub=connectors");

    // 正向锚点先立。
    expect(await screen.findByText(CONNECTORS_ONLY)).toBeTruthy();
    expect(screen.getByRole("tab", { name: "连接器", selected: true })).toBeTruthy();
    // 再同步断言服务表**不在**。不套 waitFor——套在缺席断言外面几乎必然恒真。
    expect(screen.queryByText(SERVICES_ONLY)).toBeNull();
    expect(screen.queryByText(CONNECTIONS_ONLY)).toBeNull();
  });

  it("?sub=connections 显示的是连接表，不是服务表", async () => {
    stubFetch();
    renderRegistry("/registry?sub=connections");

    expect(await screen.findByText(CONNECTIONS_ONLY)).toBeTruthy();
    expect(screen.getByRole("tab", { name: "连接", selected: true })).toBeTruthy();
    expect(screen.queryByText(SERVICES_ONLY)).toBeNull();
    expect(screen.queryByText(CONNECTORS_ONLY)).toBeNull();
  });

  // 上面两条缺席断言的**对照组**：同一个判据字符串，在服务那一格必须找得到。
  // 没有它的话，`queryByText("接入地址")` 有可能只是永远匹配不上而恒为 null。
  it("?sub=services 显示的是服务表——上面两条缺席断言的判据自检", async () => {
    stubFetch();
    renderRegistry("/registry?sub=services");

    expect(await screen.findByText(SERVICES_ONLY)).toBeTruthy();
    expect(screen.queryByText(CONNECTORS_ONLY)).toBeNull();
    expect(screen.queryByText(CONNECTIONS_ONLY)).toBeNull();
  });

  it("未知子页不静默回落到服务，并且一个请求都不发", async () => {
    const fetchImpl = stubFetch();
    renderRegistry("/registry?sub=not-a-real-tab");

    expect(await screen.findByText("「not-a-real-tab」子页尚未接入")).toBeTruthy();
    expect(screen.getByRole("link", { name: "返回服务" }).getAttribute("href")).toBe(
      "/registry?sub=services",
    );
    expect(screen.queryByText(SERVICES_ONLY)).toBeNull();
    // 拼错的地址不该白打四次请求（useQuery 的 enabled: known）。
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("三个没有后端的格逐格说清在等什么，而不是一句「尚未接入」", async () => {
    stubFetch();
    const cases: ReadonlyArray<readonly [string, RegExp]> = [
      ["capabilities", /按能力反查「哪些连接器支持它、哪条连接被授了它」/],
      ["apps", /没有应用登记表，路由表里也没有对应端点/],
      ["environments", /被 CHECK 约束钉死成 development \/ staging \/ production/],
    ];
    for (const [sub, copy] of cases) {
      const { unmount } = renderRegistry(`/registry?sub=${sub}`);
      expect(await screen.findByText(copy)).toBeTruthy();
      // 占位格里不该冒出任何一张真表。
      expect(screen.queryByText(SERVICES_ONLY)).toBeNull();
      expect(screen.queryByText(CONNECTORS_ONLY)).toBeNull();
      unmount();
    }
  });
});

describe("资源目录页 · 三张表", () => {
  it("三张表各自打各自的端点", async () => {
    const fetchImpl = stubFetch();
    renderRegistry();

    expect(await screen.findByText(SERVICES_ONLY)).toBeTruthy();
    const called = urls(fetchImpl);
    expect(called).toContain("/api/v1/connectors");
    expect(called).toContain("/api/v1/connections");
    expect(called).toContain("/api/v1/services");
  });

  it("不再声称两张表尚未建，改成说清缺的只是只读端点", async () => {
    stubFetch();
    renderRegistry();

    // 正向锚点先立：新的口径句渲染出来了。
    expect(await screen.findByText(/服务、连接器、连接三张表/)).toBeTruthy();
    expect(screen.getByText(/只读端点/)).toBeTruthy();
    // 再同步断言旧那句假话不在。表在迁移 000001 就建好了，说「尚未建」
    // 会让人以为要从建表开始。
    expect(screen.queryByText(/连接器与连接两张表尚未建/)).toBeNull();
    expect(
      screen.getByText(/后两者要过审批中心，所以这一页只有服务那一格带写入按钮/),
    ).toBeTruthy();
  });

  it("连接表把 connector_id / service_id 换成人认得出的名字", async () => {
    stubFetch();
    renderRegistry("/registry?sub=connections");

    expect(await screen.findByText("secret://sub2api/dev-token")).toBeTruthy();
    // 这两个名字都不在 /connections 的响应里（那边只有 UUID），
    // 所以这条断言同时证明了三张表的数据被关联起来了。
    expect(screen.getByText("sub2api v0.1.0")).toBeTruthy();
    expect(screen.getByText("sub2api-prod")).toBeTruthy();
    // UUID 仍然留在下面一行：换了名字不等于把原始标识藏起来。
    expect(screen.getAllByText(CONNECTOR_ID).length).toBeGreaterThan(0);
  });

  it("连接指向不在清单里的对象时如实说出来，而不是留空", async () => {
    stubFetch({
      connections: [connectionItem({ connector_id: "99999999-9999-4999-8999-999999999999" })],
    });
    renderRegistry("/registry?sub=connections");

    expect(await screen.findByText("secret://sub2api/dev-token")).toBeTruthy();
    expect(screen.getByText("不在本页清单里")).toBeTruthy();
    expect(screen.getByText("99999999-9999-4999-8999-999999999999")).toBeTruthy();
  });

  it("从未核验的连接说「从未核验」，不显示成一个时间", async () => {
    stubFetch();
    renderRegistry("/registry?sub=connections");

    expect(await screen.findByText("从未核验")).toBeTruthy();
  });

  it("核验过的连接显示那个时刻——与上一条互为对照", async () => {
    stubFetch({ connections: [connectionItem({ last_verified_at: "2026-09-03T06:07:08Z" })] });
    renderRegistry("/registry?sub=connections");

    expect(await screen.findByText("secret://sub2api/dev-token")).toBeTruthy();
    expect(screen.queryByText("从未核验")).toBeNull();
  });

  it("有写能力的连接器标出「有写通道」，纯读的说「无（只读）」", async () => {
    stubFetch({
      connectors: [
        connectorItem(),
        connectorItem({
          id: "44444444-4444-4444-8444-444444444444",
          key: "cpa",
          write_capabilities: [],
        }),
      ],
    });
    renderRegistry("/registry?sub=connectors");

    expect(await screen.findByText("有写通道")).toBeTruthy();
    expect(screen.getByText("无（只读）")).toBeTruthy();
  });

  it("没声明兼容上游版本时说「未声明」，不显示成空白（会被读成「都兼容」）", async () => {
    stubFetch({ connectors: [connectorItem({ supported_upstream_versions: [] })] });
    renderRegistry("/registry?sub=connectors");

    expect(await screen.findByText("未声明兼容版本")).toBeTruthy();
  });

  it("拉闸的连接用「已拉闸」标出来，并显示闸名", async () => {
    stubFetch({
      connections: [connectionItem({ status: "killed", kill_switch: "sub2api.kill" })],
    });
    renderRegistry("/registry?sub=connections");

    expect(await screen.findByText("已拉闸")).toBeTruthy();
    expect(screen.getByText("闸 sub2api.kill")).toBeTruthy();
    expect(screen.queryByText("已启用")).toBeNull();
  });

  it("两张新表各自的空状态说清写入走哪个 Action、为什么这里没有按钮", async () => {
    stubFetch({ connectors: [], connections: [] });

    const first = renderRegistry("/registry?sub=connectors");
    expect(await screen.findByText("暂无连接器")).toBeTruthy();
    expect(
      screen.getByText(/registry.connector.create（L2），要过审批中心，本页不提供入口/),
    ).toBeTruthy();
    first.unmount();

    renderRegistry("/registry?sub=connections");
    expect(await screen.findByText("暂无连接")).toBeTruthy();
    expect(
      screen.getByText(/registry.connection.create（L3），要过审批中心，本页不提供入口/),
    ).toBeTruthy();
  });

  it("连接器端点 403 时只有那一格是错误态，连接格照常显示", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) => {
        if (input.includes("/api/v1/connectors")) {
          return Promise.resolve(
            fakeResponse(
              { error: { code: "PERMISSION_DENIED", message: "缺少权限 registry.read" } },
              403,
            ),
          );
        }
        if (input.includes("/api/v1/connections")) {
          return Promise.resolve(fakeResponse({ items: [connectionItem()] }));
        }
        if (input.includes("/api/v1/services")) {
          return Promise.resolve(fakeResponse({ items: [serviceItem()] }));
        }
        return Promise.resolve(fakeResponse({ items: [] }));
      }),
    );

    // 三个 query 分开的理由就在这里：一格坏了不该把另外两格拖成错误态。
    const first = renderRegistry("/registry?sub=connectors");
    // 先 await 错误文案本身（异步），再确认它落在当前那一格里——
    // 反过来写的话，`within(panel).getByText` 会在查询还没落地时就同步失败。
    const denied = await screen.findByText(/registry\.read/);
    expect(within(screen.getByRole("tabpanel")).getByText(/registry\.read/)).toBe(denied);
    // 连接器取不到时，连接表照样渲染得出来（只是连接器名字换不成）。
    expect(screen.queryByText(CONNECTORS_ONLY)).toBeNull();
    first.unmount();

    renderRegistry("/registry?sub=connections");
    expect(await screen.findByText("secret://sub2api/dev-token")).toBeTruthy();
  });
});
