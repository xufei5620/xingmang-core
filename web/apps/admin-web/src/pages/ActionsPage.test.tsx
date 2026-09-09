import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ActionsPage } from "./ActionsPage";

/** 一条 L3 的 Action 声明（提现在 XM-RISK-RESTORE 之后就是这个形状）。
 *
 *  blocked_reason 逐字照抄后端接上审批中心那一支的措辞
 *  （httpapi.ListActionsHandler 的 approvalsWired 分支）。 */
const withdrawDefinition = {
  id: "cards.withdraw.execute",
  version: "1",
  risk_level: "L3",
  permission: "fund.withdraw",
  environments: ["prod"],
  principal_types: ["HUMAN"],
  executable: false,
  blocked_reason: "需要审批：提交后落一张审批单，批准后由人触发执行",
};

function renderRiskTab() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      ({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ items: [withdrawDefinition] }),
      }) as unknown as Response,
    ),
  );
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={["/actions?sub=risk"]}>
        <ActionsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => vi.unstubAllGlobals());

/** 「风险与启用条件」子页签在审批中心启用之后必须改口径（XM-ACTION-REASON）。
 *
 *  这一格以前说 L2/L3/L4「（全部待 Foundation-B）」，脚注说它们「在
 *  Foundation-A 阶段恒为不可执行」。审批中心已经启用，这两句都成了假话：
 *  这些动作现在**提得了**，只是先落审批单。 */
describe("风险与启用条件：不再说「待 Foundation-B」", () => {
  it("L2 及以上标注的是「先落审批单」，并说清批准后在哪触发", async () => {
    renderRiskTab();
    const mark = await screen.findByText("（全部先落审批单）");
    expect(mark.getAttribute("title")).toBe(
      "L2 及以上不能直接执行：内核受理成审批单，批准后在「待审批」里由人触发",
    );
  });

  // 缺席型断言，配套的变异验证记在交接文档里：把上面那句改回
  // 「（全部待 Foundation-B）」时这两条必红。单看它们容易恒真——页面上本来
  // 也可能一个字都没有——所以两条正向断言（上一条 it）必须同时在。
  it("页面上不再出现 Foundation-B / Foundation-A 阶段那两句旧话", async () => {
    renderRiskTab();
    // 先等这一格真的渲染出来，再断言「没有」：不等就是在对一个空页面提问。
    await screen.findByText("（全部先落审批单）");
    expect(screen.queryByText(/全部待 Foundation-B/)).toBeNull();
    expect(screen.queryByText(/Foundation-A 阶段恒为不可执行/)).toBeNull();
    expect(screen.queryByText(/在 Foundation-A 阶段恒为不可执行/)).toBeNull();
  });

  it("脚注把「不能直接执行」和「先落审批单」一起说出来", async () => {
    renderRiskTab();
    expect(await screen.findByText(/不能被直接执行/)).toBeTruthy();
    expect(screen.getByText(/先由内核受理成审批单/)).toBeTruthy();
  });
});
