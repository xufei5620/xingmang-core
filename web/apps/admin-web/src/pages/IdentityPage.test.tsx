import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { IdentityPage } from "./IdentityPage";

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

/** 这一片改的四格都不发请求；桩只是兜底，免得「账号与身份」那格
 *  （StaffAccountsPanel）在某次改动后被默认渲染时炸掉整个文件。 */
function stubFetch() {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(fakeResponse({ items: [] }))));
}

function renderIdentity(initialEntry: string) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <IdentityPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function columnHeaders(): string[] {
  return screen.getAllByRole("columnheader").map((th) => th.textContent ?? "");
}

afterEach(() => vi.unstubAllGlobals());

describe("人员与权限：密钥引用格接上凭据管理", () => {
  it("不再说「尚未实现」，给出凭据管理的真实入口", () => {
    stubFetch();
    renderIdentity("/identity?sub=credentials");

    // 缺席型断言：先钉住这一格确实渲染出了新内容（下面两行），
    // 「尚未实现」不在才有意义——整格渲染失败时它同样不在
    expect(screen.getByRole("heading", { name: "凭据管理已经可用（在「设置」底下）" })).toBeTruthy();
    const link = screen.getByRole("link", { name: "打开凭据管理 →" });
    expect(link.getAttribute("href")).toBe("/settings?sub=credentials");
    expect(screen.queryByText("「密钥引用」尚未实现")).toBeNull();
    expect(screen.queryByText("阶段 F-A：随后续切片实现。")).toBeNull();
  });

  it("说清那一页覆盖了哪几列、这一格还差哪几列——不含糊说「已接入」", () => {
    stubFetch();
    renderIdentity("/identity?sub=credentials");

    expect(screen.getByText(/值只在 Action 请求期间存在，保存后不会回读到页面/)).toBeTruthy();
    expect(screen.getByText(/「密钥引用（CredentialRef）」「用途」「状态」/)).toBeTruthy();
    expect(screen.getByText(/「供应方」「消费者」「环境」「最近使用」「轮换 \/ 到期」/)).toBeTruthy();
    // 这一格只做交叉跳转，不是第二份凭据页——写在屏幕上，不只写在注释里
    expect(screen.getByText(/把凭据从设置迁到这里是一次信息架构/)).toBeTruthy();
    // 蓝图给这张表的落款原文是「凭据登记随 F-A 上线」——凭据登记已经能用了，
    // 那句话必须换掉（正向锚是上一条测试里逐字对过的列头）
    expect(screen.queryByText(/凭据登记随 F-A 上线/)).toBeNull();
  });

  it("蓝图列头照旧渲染（这一格自己的表还没有数据源）", () => {
    stubFetch();
    renderIdentity("/identity?sub=credentials");

    expect(columnHeaders()).toEqual([
      "密钥引用（CredentialRef）",
      "供应方",
      "消费者",
      "环境",
      "用途",
      "最近使用",
      "轮换 / 到期",
      "状态",
    ]);
  });
});

describe("人员与权限：其余三格从「尚未实现」换成蓝图列头 + 如实归因", () => {
  it("权限规则：列头逐字，落款说清缺的是「可编辑的授权策略」这个对象", () => {
    stubFetch();
    renderIdentity("/identity?sub=rules");

    expect(columnHeaders()).toEqual([
      "权限规则",
      "效果",
      "资源范围",
      "环境",
      "风险门槛",
      "优先级",
      "状态",
    ]);
    expect(screen.getByText(/平台今天没有「授权策略」这个可编辑对象/)).toBeTruthy();
    expect(screen.getByText(/DefaultRoleScopeMap/)).toBeTruthy();
    // 蓝图那句已经过期的落款（五格都写「随 F-A 上线」）不能再出现在屏幕上
    expect(screen.queryByText(/随 F-A 上线/)).toBeNull();
  });

  it("权限规则：蓝图里那条恒定 DENY 照常显示，并说明它今天在哪里生效", () => {
    stubFetch();
    renderIdentity("/identity?sub=rules");

    expect(screen.getByRole("heading", { name: "AI 不作为 L3/L4 第二审批人" })).toBeTruthy();
    expect(screen.getByText(/生效点在审批内核里/)).toBeTruthy();
  });

  it("权限范围：列头逐字，落款把它与「设置 → 身份与权限（只读）」区分开", () => {
    stubFetch();
    renderIdentity("/identity?sub=scopes");

    expect(columnHeaders()).toEqual([
      "角色集合",
      "平台 / 资源",
      "环境",
      "读取",
      "受控操作",
      "审批",
      "审计",
    ]);
    expect(screen.getByText(/矩阵的数据今天存在，但取不出来/)).toBeTruthy();
    expect(screen.getByText(/不是服务端认可的权限/)).toBeTruthy();
    expect(screen.queryByText(/随 F-A 上线/)).toBeNull();
  });

  it("会话：列头逐字，落款说清库表已有、端点还没有", () => {
    stubFetch();
    renderIdentity("/identity?sub=sessions");

    expect(columnHeaders()).toEqual([
      "主体",
      "会话 / Client",
      "登录验证方式",
      "来源 IP",
      "环境",
      "最后活动",
      "状态",
    ]);
    expect(screen.getByText(/core\.staff_session（迁移 000021，XM-LOGIN）/)).toBeTruthy();
    expect(screen.getByText(/oidc 模式下的会话在 Keycloak 里/)).toBeTruthy();
    expect(screen.queryByText(/随 F-A 上线/)).toBeNull();
  });
});

describe("人员与权限：整页的两条纪律", () => {
  it("不渲染蓝图页顶横幅——这一页有一格真的会写数据，那句「不执行任何真实操作」是假的", () => {
    stubFetch();
    renderIdentity("/identity?sub=rules");

    // 正向锚：这一格确实渲染出来了
    expect(screen.getByRole("heading", { name: "权限规则", level: 2 })).toBeTruthy();
    expect(screen.queryByText(/本页不执行任何真实操作/)).toBeNull();
    // 页级统计格同理不渲染：四个「—」摆在真实账号表上面会让整页看起来没建
    expect(screen.queryByText("异常密钥引用")).toBeNull();
  });

  it("五个子页签都在，未知 ?sub= 仍然不回落到账号列表", () => {
    stubFetch();
    renderIdentity("/identity?sub=notatab");

    expect(screen.getByText("「notatab」子页尚未接入")).toBeTruthy();
    expect(screen.getByRole("link", { name: "返回账号与身份" }).getAttribute("href")).toBe(
      "/identity?sub=accounts",
    );
    expect(screen.queryByRole("tab")).toBeNull();
  });

  it("每一格都能从页签切过去（五格齐全）", () => {
    stubFetch();
    renderIdentity("/identity?sub=rules");

    for (const label of ["账号与身份", "权限规则", "权限范围", "密钥引用", "会话"]) {
      expect(screen.getByRole("tab", { name: label })).toBeTruthy();
    }
    expect(screen.getByRole("tab", { name: "权限规则", selected: true })).toBeTruthy();
  });
});
