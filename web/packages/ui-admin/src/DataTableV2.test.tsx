import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { DataTableV2, type DataTableColumn } from "./DataTableV2";

interface Channel {
  id: string;
  name: string;
  state: string;
  balance: bigint;
}

const rows: Channel[] = [
  { id: "c", name: "自建 Ollama", state: "停用", balance: 300n },
  { id: "a", name: "OpenAI 中转", state: "启用", balance: 100n },
  { id: "b", name: "Gemini 备用", state: "启用", balance: 200n },
];

const columns: DataTableColumn<Channel>[] = [
  { id: "name", header: "渠道", primary: true, value: (r) => r.name, cell: (r) => r.name },
  { id: "state", header: "状态", value: (r) => r.state, cell: (r) => r.state },
  {
    id: "balance",
    header: "余额",
    numeric: true,
    value: (r) => r.balance,
    cell: (r) => String(r.balance),
  },
  { id: "op", header: "操作", cell: () => <button type="button">确认</button> },
];

const empty = <p>这个环境还没有渠道</p>;

function setup(props: Partial<React.ComponentProps<typeof DataTableV2<Channel>>> = {}) {
  return render(
    <DataTableV2<Channel>
      caption="渠道列表"
      columns={columns}
      rows={rows}
      rowKey={(r) => r.id}
      emptyState={empty}
      {...props}
    />,
  );
}

const bodyRows = () => within(screen.getByRole("table")).getAllByRole("row").slice(1);
const firstCells = () => bodyRows().map((r) => within(r).getAllByRole("cell")[0]?.textContent);

describe("DataTableV2 基本渲染", () => {
  it("给表格一个只读屏能听见的用途说明", () => {
    // 读屏用户进到一张表时，第一句要听到的是「这是什么表」
    setup();
    expect(screen.getByRole("table").querySelector("caption")?.textContent).toBe("渠道列表");
  });

  it("数据源本来就空时只显示调用方的空态，不摆工具条与表头", () => {
    // 摆一张有表头、有分页、一行都没有的表，等于让人以为「筛掉了」
    setup({ rows: [] });
    expect(screen.getByText("这个环境还没有渠道")).not.toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByLabelText("表格密度")).toBeNull();
  });

  it("默认不排序，按入参顺序渲染", () => {
    // 调用方给的顺序往往有意义（告警按严重度、审计按序号倒序）
    setup();
    expect(firstCells()).toEqual(["自建 Ollama", "OpenAI 中转", "Gemini 备用"]);
  });
});

describe("横向溢出只发生在表格容器内部（§11.2）", () => {
  it("外壳是定位祖先——sr-only 是 absolute，没有它就会跑出滚动容器把整页撑宽", () => {
    // 浏览器里实测过：少了 relative，1024px 视口下 documentElement 会因为
    // 一个 1px 的隐藏元素宽出 38px，于是整页出现横向滚动条。
    // jsdom 不做布局，测不出宽度，能钉住的是这个结构前提
    const { container } = setup();
    const section = container.querySelector("[data-density]") as HTMLElement;
    expect(section.className).toContain("relative");
    expect(section.className).toContain("overflow-hidden");
  });

  it("表格包在 overflow-x-auto 的容器里", () => {
    setup();
    const wrap = screen.getByRole("table").parentElement as HTMLElement;
    expect(wrap.className).toContain("overflow-x-auto");
  });

  it("展开列的表头用 aria-label，不塞 sr-only 子元素", () => {
    setup({ renderExpanded: () => <p>x</p> });
    const header = screen.getByRole("columnheader", { name: "详情" });
    expect(header.querySelector(".sr-only")).toBeNull();
  });
});

describe("排序", () => {
  it("点列头在 升 → 降 → 取消 之间循环，aria-sort 跟着变", () => {
    setup();
    const header = screen.getByRole("columnheader", { name: /渠道/ });
    const button = within(header).getByRole("button");

    fireEvent.click(button);
    expect(header.getAttribute("aria-sort")).toBe("ascending");
    const ascending = firstCells();
    // 具体次序由 zh-CN 的排序规则决定（拼音/笔画由 ICU 定，不该在这里写死）,
    // 能断言的是：它确实重排了，而且升降序互为逆序
    expect(ascending).not.toEqual(["自建 Ollama", "OpenAI 中转", "Gemini 备用"]);

    fireEvent.click(button);
    expect(header.getAttribute("aria-sort")).toBe("descending");
    expect(firstCells()).toEqual([...ascending].reverse());

    fireEvent.click(button);
    expect(header.getAttribute("aria-sort")).toBe("none");
    // 取消之后回到入参顺序，而不是停在最后一次排序上
    expect(firstCells()).toEqual(["自建 Ollama", "OpenAI 中转", "Gemini 备用"]);
  });

  it("金额按数值排，不按字符串", () => {
    setup();
    fireEvent.click(within(screen.getByRole("columnheader", { name: /余额/ })).getByRole("button"));
    expect(firstCells()).toEqual(["OpenAI 中转", "Gemini 备用", "自建 Ollama"]);
  });

  it("没有 value 的列不给排序按钮——只有按钮的那一列排序没有意义", () => {
    setup();
    const header = screen.getByRole("columnheader", { name: "操作" });
    expect(within(header).queryByRole("button")).toBeNull();
    expect(header.getAttribute("aria-sort")).toBeNull();
  });
});

describe("搜索与筛选", () => {
  it("搜索跨列匹配，并把条件写在工具条上", () => {
    setup({ searchable: true });
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索当前表格" }), {
      target: { value: "openai" },
    });
    expect(bodyRows()).toHaveLength(1);
    // 一张筛过的表和一张没筛过的表长得一样，而人会照着它做判断
    expect(screen.getByText(/搜索：openai/)).not.toBeNull();
  });

  it("筛选下拉按列筛，「全部」等于不筛", () => {
    setup({ filters: [{ columnId: "state", label: "状态", options: ["启用", "停用"] }] });
    const select = screen.getByRole("combobox", { name: /状态/ });
    fireEvent.change(select, { target: { value: "停用" } });
    expect(bodyRows()).toHaveLength(1);
    expect(screen.getByText(/状态：停用/)).not.toBeNull();
    fireEvent.change(select, { target: { value: "" } });
    expect(bodyRows()).toHaveLength(3);
  });

  it("筛完为空时说的是「没有匹配的数据」，不是调用方那句「还没有渠道」", () => {
    // 「筛没了」和「本来就没有」是两回事：前者的下一步是清筛选，后者是去造数据
    setup({ searchable: true });
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索当前表格" }), {
      target: { value: "查无此渠道" },
    });
    expect(screen.getByText("没有匹配的数据")).not.toBeNull();
    expect(screen.queryByText("这个环境还没有渠道")).toBeNull();
  });

  it("「清除筛选」把条件清干净", () => {
    setup({ searchable: true });
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索当前表格" }), {
      target: { value: "查无此渠道" },
    });
    fireEvent.click(screen.getByRole("button", { name: "清除筛选" }));
    expect(bodyRows()).toHaveLength(3);
    expect(screen.getByText("全部数据")).not.toBeNull();
  });

  it("没有条件时不显示「清除条件」——一个永远点不出效果的按钮只是噪音", () => {
    setup({ searchable: true });
    expect(screen.queryByRole("button", { name: "清除条件" })).toBeNull();
  });
});

describe("列管理", () => {
  it("取消勾选就把那一列藏掉，「恢复默认列」放回来", () => {
    setup();
    expect(screen.getByRole("columnheader", { name: /状态/ })).not.toBeNull();
    fireEvent.click(screen.getByRole("checkbox", { name: /^状态$/ }));
    expect(screen.queryByRole("columnheader", { name: /状态/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "恢复默认列" }));
    expect(screen.getByRole("columnheader", { name: /状态/ })).not.toBeNull();
  });

  it("主标识列关不掉——把「是哪一行」藏掉，剩下的表没法读", () => {
    setup();
    const primary = screen.getByRole("checkbox", { name: /渠道 · 主标识/ });
    expect((primary as HTMLInputElement).disabled).toBe(true);
  });

  it("defaultHidden 的列默认不显示，但在列管理里能打开", () => {
    setup({
      columns: [...columns, { id: "extra", header: "备注", defaultHidden: true, cell: () => "x" }],
    });
    expect(screen.queryByRole("columnheader", { name: "备注" })).toBeNull();
    fireEvent.click(screen.getByRole("checkbox", { name: "备注" }));
    expect(screen.getByRole("columnheader", { name: "备注" })).not.toBeNull();
  });
});

describe("密度", () => {
  it("三档可选，切换写在容器的 data-density 上", () => {
    const { container } = setup();
    const section = container.querySelector("[data-density]");
    expect(section?.getAttribute("data-density")).toBe("compact");
    fireEvent.change(screen.getByRole("combobox", { name: "表格密度" }), {
      target: { value: "comfortable" },
    });
    expect(section?.getAttribute("data-density")).toBe("comfortable");
  });

  it("默认紧凑——管理后台一屏多看十行是这类表的主要价值", () => {
    const { container } = setup();
    expect(container.querySelector("[data-density]")?.getAttribute("data-density")).toBe("compact");
  });
});

describe("分页", () => {
  it("传了 pageSize 才分页，页码到边界时按钮禁用", () => {
    setup({ pageSize: 2 });
    expect(bodyRows()).toHaveLength(2);
    const prev = screen.getByRole("button", { name: "上一页" });
    const next = screen.getByRole("button", { name: "下一页" });
    expect((prev as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(next);
    expect(bodyRows()).toHaveLength(1);
    expect((next as HTMLButtonElement).disabled).toBe(true);
  });

  it("不传 pageSize 就不分页：数据源自己在翻页时不叠第二层", () => {
    setup();
    expect(screen.queryByRole("button", { name: "下一页" })).toBeNull();
    expect(bodyRows()).toHaveLength(3);
  });

  it("页脚给出总条数，并有一条给读屏的页码播报", () => {
    setup({ pageSize: 2 });
    expect(screen.getByText("共 3 条")).not.toBeNull();
    expect(screen.getByText(/第 1 页，共 2 页/)).not.toBeNull();
  });

  it("筛选之后回到第 1 页——留在第 3 页上会看到一屏空白", () => {
    setup({ pageSize: 2, searchable: true });
    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索当前表格" }), {
      target: { value: "启用" },
    });
    expect(bodyRows().length).toBeGreaterThan(0);
  });
});

describe("行选择与批量条", () => {
  it("选中之后出现批量条，「清除选择」收回去", () => {
    setup({ selectable: true, bulkActions: (keys) => <span>已选 {keys.length}</span> });
    expect(screen.queryByText(/已选择/)).toBeNull();
    fireEvent.click(screen.getByRole("checkbox", { name: "选择 a" }));
    expect(screen.getByText("已选择 1 条")).not.toBeNull();
    expect(screen.getByText("已选 1")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "清除选择" }));
    expect(screen.queryByText(/已选择/)).toBeNull();
  });

  it("页内全选与半选态", () => {
    setup({ selectable: true });
    const all = screen.getByRole("checkbox", { name: "选择本页全部" }) as HTMLInputElement;
    fireEvent.click(screen.getByRole("checkbox", { name: "选择 a" }));
    // 3 行里选了 1 行时，一个未勾选的框会让人以为一行都没选
    expect(all.indeterminate).toBe(true);
    fireEvent.click(all);
    expect(all.checked).toBe(true);
    expect(screen.getByText("已选择 3 条")).not.toBeNull();
  });

  it("不开 selectable 就没有勾选框", () => {
    setup();
    expect(screen.queryByRole("checkbox", { name: "选择本页全部" })).toBeNull();
  });
});

describe("行展开", () => {
  it("展开区是独立的一行并跨满整表", () => {
    setup({ renderExpanded: (row) => <p>详情 {row.name}</p> });
    fireEvent.click(within(bodyRows()[0] as HTMLElement).getByRole("button", { name: "详情" }));
    const detail = screen.getByText("详情 自建 Ollama").closest("td");
    // 塞进原来那一行的某个单元格里，列一隐藏它就跟着消失了
    expect(Number(detail?.getAttribute("colspan"))).toBeGreaterThan(1);
  });

  it("展开按钮标出 aria-expanded，并指向展开区", () => {
    setup({ renderExpanded: (row) => <p>详情 {row.name}</p> });
    const button = within(bodyRows()[0] as HTMLElement).getByRole("button", { name: "详情" });
    expect(button.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(button);
    expect(screen.getByRole("button", { name: "收起" }).getAttribute("aria-expanded")).toBe("true");
  });
});

describe("视图（只活在内存里）", () => {
  const views = [
    { name: "全部", state: { query: "", filters: {}, sort: null, visibleColumns: ["name", "state", "balance", "op"], density: "compact" as const } },
  ];

  it("条件与任何预置视图都不符时，下拉停在「自定义」", () => {
    // 一直显示上一个视图名等于在撒谎
    setup({ views, searchable: true });
    const select = screen.getByRole("combobox", { name: /视图/ }) as HTMLSelectElement;
    expect(select.value).toBe("全部");
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索当前表格" }), {
      target: { value: "openai" },
    });
    expect(select.value).toBe("自定义");
  });

  it("保存视图之后能选回来，且面板上写明不会被持久化", () => {
    setup({ views, searchable: true });
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索当前表格" }), {
      target: { value: "启用" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: /视图名称/ }), {
      target: { value: "在用的" },
    });
    fireEvent.click(screen.getByRole("button", { name: "保存到本次会话" }));
    // 一个「保存了、刷新就丢」却不说明的按钮，比没有这个按钮更糟
    expect(screen.getByText("不会同步或写入浏览器存储。")).not.toBeNull();

    const select = screen.getByRole("combobox", { name: /视图/ }) as HTMLSelectElement;
    expect(select.value).toBe("在用的");
    fireEvent.change(select, { target: { value: "全部" } });
    expect(bodyRows()).toHaveLength(3);
  });

  it("空名的视图不收——它在下拉里没法选中", () => {
    setup({ views });
    fireEvent.change(screen.getByRole("textbox", { name: /视图名称/ }), {
      target: { value: "   " },
    });
    fireEvent.click(screen.getByRole("button", { name: "保存到本次会话" }));
    const select = screen.getByRole("combobox", { name: /视图/ }) as HTMLSelectElement;
    expect(within(select).getAllByRole("option").map((o) => o.textContent)).toEqual([
      "全部",
      "自定义",
    ]);
  });

  it("不传 views 就没有视图与保存视图两个控件", () => {
    setup();
    expect(screen.queryByRole("combobox", { name: /视图/ })).toBeNull();
    expect(screen.queryByText("保存视图")).toBeNull();
  });
});

describe("插槽", () => {
  it("工具条与页脚的追加内容照渲染", () => {
    setup({
      toolbarExtra: <span>服务端筛选</span>,
      footerExtra: <button type="button">加载更多</button>,
    });
    expect(screen.getByText("服务端筛选")).not.toBeNull();
    expect(screen.getByRole("button", { name: "加载更多" })).not.toBeNull();
  });

  it("单元格里的交互元素照常可点", () => {
    const onClick = vi.fn();
    setup({
      columns: [
        columns[0] as DataTableColumn<Channel>,
        {
          id: "op",
          header: "操作",
          cell: (row) => (
            <button type="button" onClick={() => onClick(row.id)}>
              确认
            </button>
          ),
        },
      ],
    });
    fireEvent.click(within(bodyRows()[0] as HTMLElement).getByRole("button", { name: "确认" }));
    expect(onClick).toHaveBeenCalledWith("c");
  });
});
