import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { BlueprintTabView } from "./BlueprintView";
import { blueprintForPath, blueprintForPlatform } from "./index";
import type { BlueprintTab } from "./types";

/** 蓝图渲染的两条硬要求（UI 第 6 片）。
 *
 *  1. **结构要在**：列头、卡片字段名、页签文案照原型渲染出来——这是本片的产出；
 *  2. **数字不能在**：屏幕上不许出现任何看起来像读数的东西。
 *
 *  第 2 条在 blueprints.test.ts 里已经扫过**规格数据**，这里扫的是**渲染结果**——
 *  两者会漏掉不同的东西：规格里干净，但渲染层如果哪天给 StatTile 传了个默认值
 *  「0」，规格扫描是看不见的。 */

function tabOf(path: string, id: string): BlueprintTab {
  const tab = blueprintForPath(path)?.tabs.find((entry) => entry.id === id);
  if (!tab) throw new Error(`${path} 没有 ${id}`);
  return tab;
}

describe("蓝图渲染：结构在，数字不在", () => {
  it("表格渲染真实列头，表体是未接入而不是假数据", () => {
    render(<BlueprintTabView tab={tabOf("/identity", "accounts")} />);

    // 列头逐字来自原型
    for (const header of ["主体", "类型", "账号来源", "环境", "登录验证方式", "最后活动"]) {
      expect(screen.getByRole("columnheader", { name: header })).not.toBeNull();
    }
    // 表体没有数据行，只有一句「未接入」
    expect(screen.getByText("未接入")).not.toBeNull();
    expect(screen.queryAllByRole("row").length).toBe(1); // 只有表头那一行
  });

  it("统计格的主数位是「—」，不是数字", () => {
    const page = blueprintForPlatform("server");
    const overview = page!.tabs.find((tab) => tab.id === "overview")!;
    render(<BlueprintTabView tab={overview} />);

    // 标签与口径说明在
    expect(screen.getByText("折算月成本")).not.toBeNull();
    expect(screen.getByText("跨周期统一折算，保留汇率快照")).not.toBeNull();

    // 每个统计格的主数位都是「—」。原型这里是 `¥5,130.00`——抄过来的话，
    // 这一屏的任何一张截图都会被读成「平台已经在算钱了」
    const dashes = screen.getAllByText("—");
    expect(dashes.length).toBeGreaterThanOrEqual(overview.tiles!.length);
  });

  it("整页渲染后，屏幕上没有任何货币金额或百分比", () => {
    // 逐页渲染每一格，扫一遍可见文本。这一条是「不冒充读数」在渲染层的兜底
    const pages = [
      ...["/identity", "/finance", "/ops", "/changes", "/design"].map(
        (path) => [path, blueprintForPath(path)!] as const,
      ),
      ["platform:server", blueprintForPlatform("server")!] as const,
    ];
    for (const [name, page] of pages) {
      for (const tab of page.tabs) {
        const { container, unmount } = render(<BlueprintTabView tab={tab} />);
        const text = container.textContent ?? "";
        // 货币金额与百分比：原型样例数字的两种主要形态
        expect(/[¥$]\s?\d/.test(text), `${name}/${tab.id} 出现了货币金额`).toBe(false);
        expect(/\d+(\.\d+)?\s?%/.test(text), `${name}/${tab.id} 出现了百分比`).toBe(false);
        unmount();
      }
    }
  });

  it("筛选条只展示不实装，且说明了这一点", () => {
    render(<BlueprintTabView tab={tabOf("/finance", "reconciliation")} />);
    const bar = screen.getByLabelText("筛选条（尚未启用）");
    // 控件文案照原型
    expect(within(bar).getByText("平台：全部")).not.toBeNull();
    // 但必须说清它还不能用——一个能点却筛不出东西的下拉比没有筛选更让人困惑
    expect(within(bar).getByText("筛选条随数据源一并启用。")).not.toBeNull();
    // 真的没有可交互的表单控件
    expect(within(bar).queryAllByRole("combobox").length).toBe(0);
    expect(within(bar).queryAllByRole("button").length).toBe(0);
  });

  it("每一格都印出自己在等什么", () => {
    const tab = tabOf("/ops", "migration");
    render(<BlueprintTabView tab={tab} />);
    // 落款：这一格将来由什么填。没有它，一屏「未接入」看不出是在等采集、
    // 等后端，还是等产品拍板
    expect(screen.getByText(tab.source)).not.toBeNull();
  });

  it("键值卡列字段名，值位是「—」", () => {
    render(<BlueprintTabView tab={tabOf("/finance", "invoicing")} />);
    expect(screen.getByText("文件引用原则")).not.toBeNull();
    // 字段名是设计信息，照抄；值全是样例，一个不抄
    expect(screen.getByText("document_hash")).not.toBeNull();
    expect(screen.queryByText(/sha256:/)).toBeNull();
  });
});

/** 页面级：蓝图真的挂到了那几个路由上。
 *
 *  上面几条测的是渲染组件本身。这一条测**接线**——PlaceholderPage 有没有按
 *  路径查到蓝图、子页签有没有按 id 对上。接线断了的话，页面会安静地退回
 *  那句通用的「尚未实现」，而所有组件级测试仍然全绿。 */
describe("页面接线", () => {
  it("治理页按 ?sub= 渲染对应的蓝图内容", async () => {
    const { PlaceholderPage } = await import("../pages/PlaceholderPage");
    const { MemoryRouter, Route, Routes } = await import("react-router");
    render(
      <MemoryRouter initialEntries={["/ops?sub=migration"]}>
        <Routes>
          <Route path="/ops" element={<PlaceholderPage />} />
        </Routes>
      </MemoryRouter>,
    );

    // 页头用的是导航里的名字（不是原型的「未来功能 · 」前缀，见 types.ts）
    expect(screen.getByRole("heading", { name: "运行保障", level: 2 })).not.toBeNull();
    // 选中的子页签是 URL 指定的那一格，不是第一格
    expect(screen.getByRole("tab", { name: "迁移与数据对比", selected: true })).not.toBeNull();
    // 而且渲染的是蓝图内容，不是那句通用的「尚未实现」
    expect(screen.getByRole("columnheader", { name: "数据同步位置（Watermark）" })).not.toBeNull();
    expect(screen.queryByText(/尚未实现/)).toBeNull();
  });

  it("没有蓝图的页仍然是原来那句诚实占位", async () => {
    const { PlaceholderPage } = await import("../pages/PlaceholderPage");
    const { MemoryRouter, Route, Routes } = await import("react-router");
    render(
      <MemoryRouter initialEntries={["/jobs"]}>
        <Routes>
          <Route path="/jobs" element={<PlaceholderPage />} />
        </Routes>
      </MemoryRouter>,
    );
    // /jobs 不在蓝图表里：行为必须与本片之前完全一致
    expect(screen.getByText(/「运行中」尚未实现/)).not.toBeNull();
  });
});
