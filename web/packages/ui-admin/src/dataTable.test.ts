import { describe, expect, it } from "vitest";
import {
  ariaSort,
  describeCriteria,
  filterRows,
  matchesView,
  nextSort,
  normalizeViewName,
  pageSelection,
  paginate,
  rowText,
  sortHint,
  sortRows,
  sortValue,
  toggleKeys,
  type CellValue,
  type SavedView,
  type TableRow,
  type TableViewState,
} from "./dataTable";

const row = (key: string, values: Record<string, CellValue>): TableRow => ({ key, values });

describe("排序取值", () => {
  it("日期时间按时间先后排，不按字符串", () => {
    expect(sortValue("2026-08-28 10:32")).toBeLessThan(sortValue("2026-08-28 11:00") as number);
    expect(sortValue("2026-09-01")).toBeGreaterThan(sortValue("2026-08-31") as number);
    // ISO 的 T 分隔同样认
    expect(sortValue("2026-08-28T09:00:00Z")).toBeLessThan(sortValue("2026-08-28T10:00:00Z") as number);
  });

  it("时分秒按秒排", () => {
    expect(sortValue("9:30")).toBeLessThan(sortValue("10:00") as number);
    expect(sortValue("00:00:05")).toBe(5);
  });

  it("带货币符号与千分位的金额按数值排", () => {
    // 按字符串排会把 ¥1,234.56 排在 ¥900.00 前面——
    // 一张排错的金额表比没有排序更危险，因为它看起来是对的
    expect(sortValue("¥900.00")).toBeLessThan(sortValue("¥1,234.56") as number);
    expect(sortValue("¥1,234.56")).toBe(1234.56);
    expect(sortValue("2,340 ms")).toBe(2340);
    expect(sortValue("-12")).toBe(-12);
  });

  it("数字类型原样通过，非有限数按空处理", () => {
    expect(sortValue(42)).toBe(42);
    expect(sortValue(Number.NaN)).toBe("");
  });

  it("bigint 原样通过——金额是最小单位的整数，不许经过 number", () => {
    // 宪法：金额禁止 float。转成 number 排序会在 2^53 之上悄悄丢精度,
    // 而那正是「总额」这类字段的量级
    expect(sortValue(1234n)).toBe(1234n);
  });

  it("其余按中文 locale 比较，空与 null 归一到空串", () => {
    expect(sortValue("渠道甲")).toBe("渠道甲");
    expect(sortValue(null)).toBe("");
    expect(sortValue(undefined)).toBe("");
    expect(sortValue("   ")).toBe("");
  });
});

describe("排序", () => {
  const rows = [row("a", { n: 3 }), row("b", { n: 1 }), row("c", { n: 2 })];

  it("升序与降序", () => {
    expect(sortRows(rows, { columnId: "n", direction: "asc" }).map((r) => r.key)).toEqual([
      "b",
      "c",
      "a",
    ]);
    expect(sortRows(rows, { columnId: "n", direction: "desc" }).map((r) => r.key)).toEqual([
      "a",
      "c",
      "b",
    ]);
  });

  it("不排序时保持原顺序，且不改动入参", () => {
    expect(sortRows(rows, null).map((r) => r.key)).toEqual(["a", "b", "c"]);
    expect(rows.map((r) => r.key)).toEqual(["a", "b", "c"]);
  });

  it("同值稳定：不把原本按时间来的行洗乱", () => {
    const tied = [row("x", { s: "同" }), row("y", { s: "同" }), row("z", { s: "同" })];
    expect(sortRows(tied, { columnId: "s", direction: "asc" }).map((r) => r.key)).toEqual([
      "x",
      "y",
      "z",
    ]);
    expect(sortRows(tied, { columnId: "s", direction: "desc" }).map((r) => r.key)).toEqual([
      "x",
      "y",
      "z",
    ]);
  });

  it("bigint 金额精确排序：超出 2^53 的两个值不会被判成相等", () => {
    // Number(9007199254740993n) === Number(9007199254740992n)，
    // 走 number 的实现会把这两行判成同值，于是「按金额排」在大额上失效
    const big = [
      row("small", { amount: 9007199254740992n }),
      row("large", { amount: 9007199254740993n }),
    ];
    expect(sortRows(big, { columnId: "amount", direction: "desc" }).map((r) => r.key)).toEqual([
      "large",
      "small",
    ]);
  });

  it("sortValues 覆盖排序取值，但搜索与筛选仍看 values", () => {
    // 严重度显示「严重/警告/提示」，筛选要按这三个词筛，
    // 但按中文比较排出来是「严重 < 提示 < 警告」——一个毫无意义的顺序
    const graded: TableRow[] = [
      { key: "info", values: { level: "提示" }, sortValues: { level: 2 } },
      { key: "critical", values: { level: "严重" }, sortValues: { level: 0 } },
      { key: "warning", values: { level: "警告" }, sortValues: { level: 1 } },
    ];
    expect(sortRows(graded, { columnId: "level", direction: "asc" }).map((r) => r.key)).toEqual([
      "critical",
      "warning",
      "info",
    ]);
    expect(filterRows(graded, "", { level: "警告" }).map((r) => r.key)).toEqual(["warning"]);
  });

  it("按不存在的列排等于全部同值，顺序不变", () => {
    expect(sortRows(rows, { columnId: "不存在", direction: "asc" }).map((r) => r.key)).toEqual([
      "a",
      "b",
      "c",
    ]);
  });
});

describe("排序状态切换", () => {
  it("同一列在 升 → 降 → 取消 之间循环", () => {
    // 留出「取消」是因为原始顺序往往有意义（审计按序号倒序、告警按严重度），
    // 排过之后回不去，人只能刷新页面
    let sort = nextSort(null, "time");
    expect(sort).toEqual({ columnId: "time", direction: "asc" });
    sort = nextSort(sort, "time");
    expect(sort).toEqual({ columnId: "time", direction: "desc" });
    expect(nextSort(sort, "time")).toBeNull();
  });

  it("换一列从升序重新开始", () => {
    expect(nextSort({ columnId: "a", direction: "desc" }, "b")).toEqual({
      columnId: "b",
      direction: "asc",
    });
  });

  it("aria-sort 与提示语跟着状态走", () => {
    expect(ariaSort(null, "a")).toBe("none");
    expect(ariaSort({ columnId: "a", direction: "asc" }, "a")).toBe("ascending");
    expect(ariaSort({ columnId: "a", direction: "asc" }, "b")).toBe("none");
    expect(sortHint(null, "a", "时间")).toContain("未排序");
    expect(sortHint({ columnId: "a", direction: "desc" }, "a", "时间")).toContain("取消排序");
  });
});

describe("搜索与筛选", () => {
  const rows = [
    row("1", { name: "OpenAI 中转", state: "启用", n: 12 }),
    row("2", { name: "Gemini 备用", state: "停用", n: 3 }),
    row("3", { name: "自建 Ollama", state: "启用", n: 7 }),
  ];

  it("搜索跨列匹配整行", () => {
    // 人记得的往往是「那条 openai 的」，而不是「渠道名这一列里含 openai 的」
    expect(filterRows(rows, "openai", {}).map((r) => r.key)).toEqual(["1"]);
    expect(filterRows(rows, "停用", {}).map((r) => r.key)).toEqual(["2"]);
  });

  it("搜索大小写与首尾空格不敏感", () => {
    expect(filterRows(rows, "  OPENAI ", {}).map((r) => r.key)).toEqual(["1"]);
  });

  it("筛选只看指定的那一列", () => {
    expect(filterRows(rows, "", { state: "启用" }).map((r) => r.key)).toEqual(["1", "3"]);
    // 「启用」这两个字也出现在别的行的别的列里时，筛选不会被带偏
    expect(filterRows(rows, "", { name: "自建" }).map((r) => r.key)).toEqual(["3"]);
  });

  it("筛选是包含匹配而不是相等", () => {
    // 请求列表的状态列里是「失败 · 502」，按相等匹配永远筛不出来
    const withDetail = [row("a", { s: "失败 · 502" }), row("b", { s: "成功" })];
    expect(filterRows(withDetail, "", { s: "失败" }).map((r) => r.key)).toEqual(["a"]);
  });

  it("空筛选值等于不筛", () => {
    expect(filterRows(rows, "", { state: "" })).toHaveLength(3);
  });

  it("搜索与筛选同时生效（取交集）", () => {
    expect(filterRows(rows, "ollama", { state: "启用" }).map((r) => r.key)).toEqual(["3"]);
    expect(filterRows(rows, "gemini", { state: "启用" })).toHaveLength(0);
  });

  it("rowText 把 null 当空串，不产出 \"null\"；bigint 按十进制字符串进搜索", () => {
    expect(rowText(row("x", { a: null, b: "值" }))).toBe("值");
    expect(rowText(row("y", { a: 1234n }))).toBe("1234");
  });
});

describe("分页", () => {
  const rows = Array.from({ length: 25 }, (_, i) => row(String(i), { n: i }));

  it("按页切片", () => {
    const first = paginate(rows, 1, 10);
    expect(first.rows).toHaveLength(10);
    expect(first).toMatchObject({ page: 1, pages: 3, total: 25 });
    expect(paginate(rows, 3, 10).rows).toHaveLength(5);
  });

  it("页码越界时夹回有效范围，不给空白页", () => {
    expect(paginate(rows, 99, 10).page).toBe(3);
    expect(paginate(rows, 0, 10).page).toBe(1);
  });

  it("不传 pageSize 就不分页——数据源自己在翻页时不叠第二层", () => {
    const all = paginate(rows, 1, undefined);
    expect(all.rows).toHaveLength(25);
    expect(all).toMatchObject({ page: 1, pages: 1, total: 25 });
  });

  it("空数据也至少有一页，不会出现「第 1 页，共 0 页」", () => {
    expect(paginate([], 1, 10)).toMatchObject({ page: 1, pages: 1, total: 0 });
  });
});

describe("行选择", () => {
  const pageRows = [row("a", {}), row("b", {}), row("c", {})];

  it("一行没选 / 选了一部分 / 全选，三种状态分得开", () => {
    expect(pageSelection(pageRows, new Set())).toMatchObject({
      checked: false,
      indeterminate: false,
    });
    // 半选态必须有：3 行里选了 1 行时，一个未勾选的框会让人以为一行都没选
    expect(pageSelection(pageRows, new Set(["a"]))).toMatchObject({
      checked: false,
      indeterminate: true,
      selectedOnPage: 1,
    });
    expect(pageSelection(pageRows, new Set(["a", "b", "c"]))).toMatchObject({
      checked: true,
      indeterminate: false,
    });
  });

  it("空页不算全选", () => {
    expect(pageSelection([], new Set(["a"]))).toMatchObject({ checked: false, indeterminate: false });
  });

  it("只统计**本页**的行：别的页选中的不影响本页全选框", () => {
    expect(pageSelection(pageRows, new Set(["a", "b", "c", "别的页"]))).toMatchObject({
      checked: true,
    });
  });

  it("勾选与取消都不改动原集合", () => {
    const before = new Set(["a"]);
    expect([...toggleKeys(before, ["b", "c"], true)]).toEqual(["a", "b", "c"]);
    expect([...toggleKeys(before, ["a"], false)]).toEqual([]);
    expect([...before]).toEqual(["a"]);
  });
});

describe("当前条件说明", () => {
  it("什么都没筛时说「全部数据」", () => {
    expect(describeCriteria("", {}, {})).toBe("全部数据");
  });

  it("列出筛选与搜索，列名用标签而不是列 id", () => {
    expect(describeCriteria("gpt", { result: "失败" }, { result: "结果" })).toBe(
      "当前条件：结果：失败 · 搜索：gpt",
    );
  });

  it("带视图名时前缀出视图", () => {
    expect(describeCriteria("", {}, {}, "全部")).toBe("视图：全部 · 全部数据");
  });

  it("空筛选值不进说明", () => {
    expect(describeCriteria("", { result: "" }, { result: "结果" })).toBe("全部数据");
  });
});

describe("视图（只活在内存里）", () => {
  const base: TableViewState = {
    query: "",
    filters: {},
    sort: null,
    visibleColumns: ["a", "b"],
    density: "compact",
  };
  const view: SavedView = { name: "全部", state: base };

  it("状态与视图一致时认得出来", () => {
    expect(matchesView(base, view)).toBe(true);
  });

  it("任何一处不同就算自定义", () => {
    expect(matchesView({ ...base, query: "x" }, view)).toBe(false);
    expect(matchesView({ ...base, density: "standard" }, view)).toBe(false);
    expect(matchesView({ ...base, sort: { columnId: "a", direction: "asc" } }, view)).toBe(false);
    expect(matchesView({ ...base, visibleColumns: ["a"] }, view)).toBe(false);
    expect(matchesView({ ...base, filters: { a: "1" } }, view)).toBe(false);
  });

  it("列顺序与空筛选值不影响判定", () => {
    expect(matchesView({ ...base, visibleColumns: ["b", "a"] }, view)).toBe(true);
    expect(matchesView({ ...base, filters: { a: "" } }, view)).toBe(true);
  });

  it("视图名去空白、限长 24，空名不接受", () => {
    expect(normalizeViewName("  高风险  复核 ")).toBe("高风险 复核");
    expect(normalizeViewName("x".repeat(40))).toHaveLength(24);
    expect(normalizeViewName("   ")).toBe("");
  });
});
