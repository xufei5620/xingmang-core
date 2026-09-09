import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DesignPage } from "./DesignPage";

function renderDesign(initialEntry = "/design") {
  // 这一页一条端点都不读。fetch 被换成一个会失败的桩：真有人在这一页里加了取数，
  // 下面「不发起任何请求」那条会红，而不是安静地多打一个请求
  const fetchImpl = vi.fn(() => Promise.reject(new Error("界面规范页不应发起任何请求")));
  vi.stubGlobal("fetch", fetchImpl);
  render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <DesignPage />
    </MemoryRouter>,
  );
  return fetchImpl;
}

/** 把 getComputedStyle 的自定义属性读取换成夹具，好把「读到了」那条路走一遍。
 *
 *  只覆盖 getPropertyValue、其余照原样返回：Radix 与 testing-library 自己也会调
 *  getComputedStyle，整个换掉会把它们一起弄坏。 */
function stubTokenValues(values: Readonly<Record<string, string>>) {
  const real = window.getComputedStyle.bind(window);
  vi.spyOn(window, "getComputedStyle").mockImplementation((element, pseudo) => {
    const style = real(element, pseudo ?? undefined);
    const original = style.getPropertyValue.bind(style);
    style.getPropertyValue = (property: string) => values[property] ?? original(property);
    return style;
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("DesignPage 宿主", () => {
  it("六个子页签都在，默认进「颜色与排版」，且不发起任何请求", async () => {
    const fetchImpl = renderDesign();

    for (const label of [
      "颜色与排版",
      "按钮与表单",
      "卡片与状态",
      "表格与详情",
      "页面状态",
      "复杂组件",
    ]) {
      expect(screen.getByRole("tab", { name: label })).toBeTruthy();
    }
    expect(screen.getByRole("tab", { name: "颜色与排版" }).getAttribute("aria-selected")).toBe(
      "true",
    );
    expect(screen.getByText("核心颜色")).toBeTruthy();
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("页面写明它没有取数上限、也没有「今天是 0」这种空态", () => {
    renderDesign();
    const note = screen.getByRole("status");
    expect(note.textContent).toContain("不读任何后端端点");
    expect(note.textContent).toContain("这一屏可能不是全部");
    expect(note.textContent).toContain("今天真的是 0");
    // Storybook 只说怎么在仓库里跑，不给一个点不开的外链（生产镜像不构建 Storybook）
    expect(note.textContent).toContain("pnpm --filter ui-storybook dev");
    expect(within(note).queryByRole("link")).toBeNull();
  });

  it("未知子页不静默回落到第一格", () => {
    renderDesign("/design?sub=not-a-real-tab");
    expect(screen.getByText("「not-a-real-tab」子页尚未接入")).toBeTruthy();
    // 回落的话这里会渲染色卡
    expect(screen.queryByText("核心颜色")).toBeNull();
    expect(screen.getByRole("link", { name: "返回颜色与排版" }).getAttribute("href")).toBe(
      "/design?sub=color",
    );
  });

  it("点子页签换格，切过去的是那一格的真内容", () => {
    renderDesign();
    // Radix 的 Tabs 在 mousedown 上换值，不是 click
    fireEvent.mouseDown(screen.getByRole("tab", { name: "页面状态" }));
    expect(screen.getByRole("tab", { name: "页面状态", selected: true })).toBeTruthy();
    expect(screen.getByText("五种组件态")).toBeTruthy();
    // 换格之后原来那一格的内容要真的走掉，而不是两格叠着
    expect(screen.queryByText("核心颜色")).toBeNull();
  });
});

describe("颜色与排版", () => {
  it("读不到令牌当前值时如实说读不到，而不是显示成空白或白色", () => {
    // jsdom 不加载样式表，getPropertyValue 返回空串——这正是「变量没定义」时
    // 浏览器给的东西，所以这条同时钉住了真实环境里令牌被删掉的那种情形
    renderDesign("/design?sub=color");
    const missing = screen.getAllByText(/当前值 读不到（这个变量在当前样式里没有定义，不是白色）/);
    // 浅色 + 深色两列，每列一整套 color.* 与 nav.*
    expect(missing.length).toBeGreaterThan(20);
  });

  // 「页面不写死色值」这件事由上面那条钉住：写死的实现永远说不出「读不到」。
  // 这一条只钉「读回来的值确实显示出来了、且前导空格被去掉」
  it("读到了就显示实读回来的那个值", () => {
    stubTokenValues({
      "--xm-color-accent": " #4353b8",
      "--xm-color-nav-surface": "#1c2430",
      "--xm-radius-sm": "0.375rem",
      "--xm-space-4": "1rem",
      "--xm-control-h-md": "2.25rem",
    });
    renderDesign("/design?sub=color");

    // 前导空格来自 CSS 声明本身，显示前要去掉
    expect(screen.getAllByText("当前值 #4353b8").length).toBeGreaterThan(0);
    expect(screen.getAllByText("当前值 #1c2430").length).toBeGreaterThan(0);
    expect(screen.getAllByText("0.375rem").length).toBeGreaterThan(0);
    expect(screen.getAllByText("1rem").length).toBeGreaterThan(0);
    expect(screen.getAllByText("2.25rem").length).toBeGreaterThan(0);
  });

  it("深色一列走容器级 data-theme，不去改 documentElement", () => {
    const { container } = render(
      <MemoryRouter initialEntries={["/design?sub=color"]}>
        <DesignPage />
      </MemoryRouter>,
    );
    const dark = container.querySelector('[data-theme="dark"]');
    expect(dark, "深色一列不在").not.toBeNull();
    // 两列各摆一整套令牌，所以同一个变量名会出现两次
    expect(within(dark as HTMLElement).getAllByText("--xm-color-canvas").length).toBe(1);
    expect(screen.getAllByText("--xm-color-canvas").length).toBe(2);
    // 改全局会把浅色那一列也翻掉
    expect(document.documentElement.getAttribute("data-theme")).toBeNull();
  });

  it("深色导航令牌与主题色分开展示，间距刻度按名字实读", () => {
    renderDesign("/design?sub=color");
    expect(screen.getAllByText("nav.surface").length).toBe(2);
    expect(screen.getAllByText("color.tableStripe").length).toBe(2);
    // 原型的六个称呼 → 令牌 的对照表：拿着原型的词也要查得到令牌
    expect(screen.getByText("交互主色")).toBeTruthy();
    expect(screen.getAllByText("color.accent").length).toBe(3); // 两列色卡 + 对照表一行
    for (const variable of ["--xm-space-1", "--xm-space-10", "--xm-control-h-md"]) {
      expect(screen.getByText(variable), `${variable} 没画出来`).toBeTruthy();
    }
    // 蓝图那档「行内详情圆角」在实现里没有独立取值，页面如实说明而不是编一个
    expect(screen.getByText(/行内详情是表内的一整行/)).toBeTruthy();
  });
});

describe("按钮与表单", () => {
  it("四种 variant × 三种 size 全是真按钮，禁用与加载中也在", () => {
    renderDesign("/design?sub=controls");
    for (const variant of ["primary", "secondary", "ghost", "danger"]) {
      for (const size of ["sm", "md", "lg"]) {
        expect(
          screen.getByRole("button", { name: `${variant} / ${size}` }),
          `${variant}/${size} 缺一格`,
        ).toBeTruthy();
      }
    }
    expect(screen.getAllByRole("button", { name: "禁用" }).length).toBe(4);
    const loading = screen.getAllByRole("button", { name: "加载中" });
    expect(loading.length).toBe(4);
    // loading 自动带 disabled 与 aria-busy——这是「防重复提交」在组件里的落点
    for (const button of loading) {
      expect(button.getAttribute("aria-busy")).toBe("true");
      expect((button as HTMLButtonElement).disabled).toBe(true);
    }
  });

  it("「边框按钮」这一档对不上，页面挑明而不是照着蓝图硬认", () => {
    renderDesign("/design?sub=controls");
    const text = screen.getByText(/实现里带描边的是/).textContent ?? "";
    expect(text).toContain("secondary");
    expect(text).toContain("ghost");
    expect(text).toContain("ADMIN-IA.md");
  });

  it("错误态用 role=alert 的说明，不是只画一圈红边", () => {
    renderDesign("/design?sub=controls");
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("示例校验信息");
    const invalid = screen.getByDisplayValue("不合法的示例取值");
    expect(invalid.getAttribute("aria-invalid")).toBe("true");
    expect(invalid.getAttribute("aria-describedby")).toBe(alert.id);
    // 只读 ≠ 禁用：只读仍可选中复制
    const readOnly = screen.getByDisplayValue("只读示例取值") as HTMLInputElement;
    expect(readOnly.readOnly).toBe(true);
    expect(readOnly.disabled).toBe(false);
  });
});

describe("卡片与状态", () => {
  it("新鲜度五档加未知兜底全部真渲染", () => {
    renderDesign("/design?sub=cards");
    for (const label of ["未初始化", "同步失败", "数据延迟", "数据不完整", "数据新鲜"]) {
      expect(screen.getByText(label), `${label} 没渲染`).toBeTruthy();
    }
    // 后端将来新增状态时，前端宁可显示「未知状态」让人来查
    expect(screen.getByText("未知状态（demo_unknown_state）")).toBeTruthy();
  });

  it("运行环境标签用 environmentLabel 的真实文案，三档加未指定", () => {
    renderDesign("/design?sub=cards");
    for (const environment of ["development", "staging", "production"]) {
      expect(screen.getByText(`环境 ${environment}`)).toBeTruthy();
    }
    expect(screen.getByText("环境 由服务端解析")).toBeTruthy();
  });

  it("风险等级标签不擅自二选一：既不画三档，也不冒充 L0–L4 分级表", () => {
    renderDesign("/design?sub=cards");
    const placeholder = screen.getByText("风险等级标签的最终措辞未定");
    expect(placeholder).toBeTruthy();
    // 三档是原型的说法，平台的真实分级是 ADR-003 的 L0–L4——两套没对过映射，
    // 所以这一页一套都不画（画哪一套都是替产品负责人拍板）
    for (const tier of ["低风险", "中风险", "高风险"]) {
      expect(screen.queryByText(tier), `${tier} 不该出现在这一页上`).toBeNull();
    }
    for (const level of ["L0", "L1", "L2", "L3", "L4"]) {
      expect(
        screen.queryByText(level, { exact: true }),
        `${level} 徽章不该在这一页上被复制一份`,
      ).toBeNull();
    }
    expect(
      screen.getByRole("link", { name: "去看真实的 L0–L4 分级表" }).getAttribute("href"),
    ).toBe("/actions?sub=risk");
  });

  it("骨架说清它不是一个组件，加载态由 PageState 承担", () => {
    renderDesign("/design?sub=cards");
    expect(screen.getByText(/仓库里没有独立的骨架组件/)).toBeTruthy();
  });
});

describe("表格与详情", () => {
  it("演示表挂着「这是合成数据」的横幅，且行标识一眼看得出是示例", () => {
    renderDesign("/design?sub=tables");
    const banner = screen.getByText(/本表是规范演示/);
    expect(banner.textContent).toContain("不是平台数据");
    expect(screen.getByText("示例行 A")).toBeTruthy();
    expect(screen.getByText("示例行 B")).toBeTruthy();
  });

  it("分页是活的：第三行在第二页", () => {
    renderDesign("/design?sub=tables");
    // 每页两条，所以 C 一开始不在
    expect(screen.queryByText("示例行 C")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    expect(screen.getByText("示例行 C")).toBeTruthy();
    expect(screen.queryByText("示例行 A")).toBeNull();
  });

  it("证据行内详情展开后四个必含字段都在", () => {
    renderDesign("/design?sub=tables");
    const rowB = screen.getByText("示例行 B").closest("tr") as HTMLElement;
    fireEvent.click(within(rowB).getByRole("button", { name: "详情" }));
    for (const field of ["哈希", "审计编号"]) {
      expect(screen.getByText(field), `${field} 没出现在展开区`).toBeTruthy();
    }
    // 观测时间与来源在列头上也各有一个，所以这两项按「展开区里也有」来断言
    for (const field of ["观测时间", "来源"]) {
      expect(screen.getAllByText(field).length, `${field} 没出现在展开区`).toBeGreaterThan(1);
    }
    expect(screen.getByText(`sha256:${"0".repeat(64)}`)).toBeTruthy();
    expect(screen.getByText("audit_demo_b")).toBeTruthy();
  });

  it("表内搜索真的能筛，筛空了显示「没有匹配的数据」而不是空表", () => {
    renderDesign("/design?sub=tables");
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索当前表格" }), {
      target: { value: "不存在的字串" },
    });
    expect(screen.getByText("没有匹配的数据")).toBeTruthy();
    expect(screen.queryByText("示例行 A")).toBeNull();
  });
});

describe("页面状态", () => {
  it("PageState 的五种 kind 全部真渲染", () => {
    renderDesign("/design?sub=states");
    expect(screen.getByText("加载中…")).toBeTruthy();
    expect(screen.getAllByText("还没有示例记录").length).toBeGreaterThan(0);
    expect(screen.getByText("加载失败")).toBeTruthy();
    expect(screen.getByText("无权访问")).toBeTruthy();
    expect(screen.getByText("「示例功能」尚未接入")).toBeTruthy();
  });

  it("另外三种状态说得出它在平台里的落点，而不是硬塞成 PageState 的 kind", () => {
    renderDesign("/design?sub=states");
    const noResults = screen.getByText(/= empty \+ 查询条件描述/);
    expect(noResults.textContent).toContain("DataTableV2");
    expect(screen.getByText(/= FreshnessBadge 的 stale/)).toBeTruthy();
    expect(screen.getByText(/= FreshnessBadge 的 partial/)).toBeTruthy();
    // 「（数据不完整）」这句是 FreshnessNote 真渲染出来的，不是页面抄的一段话
    expect(screen.getByText(/落后 30 秒（数据不完整）/)).toBeTruthy();
    // 八态是否要全做进 PageState 属于改公共组件，需要产品负责人一句话
    expect(screen.getByText(/需产品负责人一句话/)).toBeTruthy();
  });

  it("保留「部分数据与空不是一回事」这条宪法 12 条的落点", () => {
    renderDesign("/design?sub=states");
    expect(screen.getByText("「部分数据」与「空」不是一回事")).toBeTruthy();
    expect(screen.getByText(/把部分数据显示成完整数据，比显示成空更糟/)).toBeTruthy();
  });
});

describe("复杂组件", () => {
  it("四个组件保持诚实占位，每一个都说得出在等谁", () => {
    renderDesign("/design?sub=components");
    expect(screen.getByText("这四个组件仓库里一个都还没有")).toBeTruthy();
    for (const title of ["流程节点", "内容日历单元格", "桌面与手机预览框", "告警时间线"]) {
      expect(screen.getByText(title), `${title} 没列出来`).toBeTruthy();
    }
    expect(screen.getAllByText("尚未实现").length).toBe(4);
    expect(screen.getByText(/等审批链前端/)).toBeTruthy();
    expect(screen.getByText(/实施计划 §2.5/)).toBeTruthy();
  });
});

describe("整页不冒充读数", () => {
  const SUBS = ["color", "controls", "cards", "tables", "states", "components"] as const;

  it.each(SUBS)("?sub=%s 这一格不出现货币金额或百分比", (sub) => {
    // 这一页是全站唯一摆演示行的地方，所以它反而要把这条守得最紧：
    // 一张截图里的「¥5,130.00」不会有人去追问它是不是样例
    const { container } = render(
      <MemoryRouter initialEntries={[`/design?sub=${sub}`]}>
        <DesignPage />
      </MemoryRouter>,
    );
    const text = container.textContent ?? "";
    expect(/[¥$]\s?\d/.test(text), `${sub} 出现了货币金额`).toBe(false);
    expect(/\d+(\.\d+)?\s?%/.test(text), `${sub} 出现了百分比`).toBe(false);
    // 「带单位的计数」这条**不在**这里扫：表格页脚的「共 N 条」是 DataTableV2
    // 自己数出来的行数，不是伪造读数；演示行本身的那一条断言在
    // lib/designSpec.test.ts 里，扫的是数据而不是渲染结果
  });
});
