/** DataTableV2 的纯逻辑：排序、搜索、筛选、分页、选择、列显隐、视图。
 *
 *  与组件分开是为了能直接断言规则本身。一张把「未确认」排在「已解决」后面的表,
 *  在截图上和正确实现长得一模一样——只有测试看得出来。ui-admin 里
 *  sparklineGeometry / freshness / navigation 都是这个路子。
 *
 *  能力清单照 UI 交接文档 §12.1 与原型的 `dataTableV2`。 */

/** 行密度。原型三档：紧凑 40px / 标准 48px / 舒适 56px。
 *  管理后台默认紧凑——一屏能多看十行，是这类表的主要价值。 */
export type Density = "compact" | "standard" | "comfortable";

export const DENSITY_LABELS: Readonly<Record<Density, string>> = {
  compact: "紧凑",
  standard: "标准",
  comfortable: "舒适",
};

export type SortDirection = "asc" | "desc";

export interface TableSort {
  columnId: string;
  direction: SortDirection;
}

/** 一次筛选：某一列取某个值。空串表示「全部」。 */
export type TableFilters = Readonly<Record<string, string>>;

export interface TableViewState {
  query: string;
  filters: TableFilters;
  sort: TableSort | null;
  /** 可见列的 id；顺序不参与，可见性才参与。 */
  visibleColumns: readonly string[];
  density: Density;
}

/** 服务端持久化的 SavedView v1 wire contract。字段名逐字对齐 XM-B003。 */
export interface SavedViewStateV1 {
  schema_version: 1;
  query: string;
  filters: Readonly<Record<string, string>>;
  sort: { column_id: string; direction: SortDirection } | null;
  columns: {
    known: readonly string[];
    visible: readonly string[];
  };
  density: Density;
}

/** Query 返回的个人视图。state_version 故意保留 number：未来版本必须仍可列出与删除，
 *  但当前客户端不会猜着应用。 */
export interface PersistedSavedView {
  id: string;
  table_key: string;
  name: string;
  state_version: number;
  state: SavedViewStateV1;
  created_at: string;
  updated_at: string;
}

/** 与当前行数据无关的静态列能力。异步摘要尚未返回时也必须完整。 */
export interface TableColumnCapability {
  id: string;
  sortable: boolean;
  primary: boolean;
  defaultHidden: boolean;
}

export interface SavedViewReconcileOptions {
  schemaReady: boolean;
  columnCapabilities: readonly TableColumnCapability[];
  filterOptions: Readonly<Record<string, readonly string[]>>;
  defaultDensity: Density;
}

export interface ReconciledSavedView {
  state: TableViewState | null;
  warnings: readonly string[];
  deferred: boolean;
  unsupported: boolean;
}

function normalizedViewQuery(raw: string): string {
  return raw.replace(/\s+/g, " ").trim();
}

/** 持久状态对当前静态列/筛选契约的兼容折算。
 *
 * schemaReady=false 时一处都不折算：这挡住异步表格在首屏“列暂时不存在”时把
 * 保存的 margin 排序永久清空。 */
export function reconcileSavedViewState(
  saved: SavedViewStateV1,
  options: SavedViewReconcileOptions,
): ReconciledSavedView {
  if (!options.schemaReady) {
    return { state: null, warnings: [], deferred: true, unsupported: false };
  }
  if (saved.schema_version !== 1) {
    return {
      state: null,
      warnings: [`SavedView 版本 ${String(saved.schema_version)} 暂不支持，已回到「全部」`],
      deferred: false,
      unsupported: true,
    };
  }

  const warnings: string[] = [];
  const capabilities = options.columnCapabilities;
  const current = new Map(capabilities.map((capability) => [capability.id, capability]));
  const savedKnown = new Set(saved.columns.known);
  const savedVisible = new Set(saved.columns.visible);
  const removed = saved.columns.known.filter((id) => !current.has(id));
  if (removed.length > 0) warnings.push(`已移除不存在的列：${removed.join("、")}`);

  let visibleColumns = capabilities
    .filter((capability) => {
      if (capability.primary) return true;
      if (savedKnown.has(capability.id)) return savedVisible.has(capability.id);
      return !capability.defaultHidden;
    })
    .map((capability) => capability.id);
  if (visibleColumns.length === 0) {
    visibleColumns = capabilities
      .filter((capability) => capability.primary || !capability.defaultHidden)
      .map((capability) => capability.id);
    warnings.push("保存的列集合已失效，已恢复当前默认列");
  }

  const filters: Record<string, string> = {};
  for (const [id, value] of Object.entries(saved.filters)) {
    const choices = options.filterOptions[id];
    if (!choices || !choices.includes(value)) {
      warnings.push(`已忽略失效筛选：${id}`);
      continue;
    }
    if (value !== "") filters[id] = value;
  }

  let sort: TableSort | null = null;
  if (saved.sort) {
    const capability = current.get(saved.sort.column_id);
    if (capability?.sortable) {
      sort = { columnId: saved.sort.column_id, direction: saved.sort.direction };
    } else {
      warnings.push(`已忽略失效排序：${saved.sort.column_id}`);
    }
  }

  const validDensity = Object.prototype.hasOwnProperty.call(DENSITY_LABELS, saved.density);
  if (!validDensity) warnings.push("保存的密度已失效，已恢复当前默认密度");
  return {
    state: {
      query: normalizedViewQuery(saved.query),
      filters,
      sort,
      visibleColumns,
      density: validDensity ? saved.density : options.defaultDensity,
    },
    warnings,
    deferred: false,
    unsupported: false,
  };
}

/** 当前表格状态 → v1 持久 wire。瞬态（页码/选择/展开/请求状态）在类型上不可表示。 */
export function toSavedViewStateV1(
  state: TableViewState,
  capabilities: readonly TableColumnCapability[],
): SavedViewStateV1 {
  return {
    schema_version: 1,
    query: normalizedViewQuery(state.query),
    filters: Object.fromEntries(Object.entries(state.filters).filter(([, value]) => value !== "")),
    sort: state.sort
      ? { column_id: state.sort.columnId, direction: state.sort.direction }
      : null,
    columns: {
      known: capabilities.map((capability) => capability.id),
      visible: [...state.visibleColumns],
    },
    density: state.density,
  };
}

/** 排序取值。
 *
 *  规则照原型的 `xmSortValue`：先认日期时间、再认时分秒、再认带货币符号的数字,
 *  都不是就按中文 locale 比较字符串。
 *
 *  为什么不直接 `String.localeCompare`：表里全是 `2026-08-28 10:32`、`¥1,234.56`、
 *  `12 分钟` 这种东西，按字符串排会把 `¥1,234.56` 排在 `¥900.00` 前面——
 *  一张排错的金额表比没有排序更危险，因为它看起来是对的。 */
export function sortValue(raw: CellValue | undefined): string | number | bigint {
  if (raw === null || raw === undefined) return "";
  // bigint 原样通过：金额在本仓一律是最小单位的整数（宪法：金额禁止 float）,
  // 转成 number 排序会在 2^53 之上悄悄丢精度——而那正是「总额」这类字段的量级
  if (typeof raw === "bigint") return raw;
  if (typeof raw === "number") return Number.isFinite(raw) ? raw : "";
  const text = raw.replace(/\s+/g, " ").trim();
  if (!text) return "";

  const dateMatch = text.match(/^(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2}):(\d{2})(?::(\d{2}))?)?/);
  if (dateMatch) {
    const [, y, m, d, hh, mm, ss] = dateMatch;
    return Number(`${y}${m}${d}${hh ?? "00"}${mm ?? "00"}`) + Number(ss ?? 0) / 100;
  }
  const clockMatch = text.match(/^(\d{1,2}):(\d{2})(?::(\d{2}))?$/);
  if (clockMatch) {
    return Number(clockMatch[1]) * 3600 + Number(clockMatch[2]) * 60 + Number(clockMatch[3] ?? 0);
  }
  const numberMatch = text.match(/^[¥$€£#]?\s*(-?\d[\d,]*(?:\.\d+)?)/);
  if (numberMatch) return Number((numberMatch[1] as string).replace(/,/g, ""));
  return text.toLocaleLowerCase("zh-CN");
}

export function compareValues(
  a: string | number | bigint,
  b: string | number | bigint,
): number {
  // bigint 之间直接比大小，不经过 Number：精度必须是精确的
  if (typeof a === "bigint" && typeof b === "bigint") return a < b ? -1 : a > b ? 1 : 0;
  if (typeof a !== "string" && typeof b !== "string") return Number(a) - Number(b);
  return String(a).localeCompare(String(b), "zh-CN");
}

/** 一个单元格的可比较取值。
 *
 *  收 bigint 是因为金额在本仓一律是最小单位的整数（宪法：金额禁止 float）,
 *  而金额列必须能排序。 */
export type CellValue = string | number | bigint | null;

/** 一行在表里的可比较形态。组件把业务对象映射成它，逻辑层不认识业务类型。 */
export interface TableRow {
  key: string;
  /** 列 id → 该单元格的可比较取值（搜索、筛选与排序都用它）。 */
  values: Readonly<Record<string, CellValue>>;
  /** 排序专用的覆盖值。
   *
   *  有些列**显示的词**和**该排的序**不是一回事：严重度列显示「严重/警告/提示」,
   *  筛选也要按这三个词筛，但按中文比较排出来是「严重 < 提示 < 警告」——
   *  一个毫无意义的顺序。这时把序号放这里，文字仍留在 values 里。 */
  sortValues?: Readonly<Record<string, CellValue>>;
}

export function normalizeText(text: string): string {
  return text.replace(/\s+/g, " ").trim().toLocaleLowerCase("zh-CN");
}

/** 整行的可搜索文本。搜索是**跨列**的：人记得的往往是「那条 openai 的」,
 *  而不是「渠道名这一列里含 openai 的」。 */
export function rowText(row: TableRow): string {
  return normalizeText(Object.values(row.values).map((v) => (v === null ? "" : String(v))).join(" "));
}

/** 搜索 + 筛选。
 *
 *  筛选按**列**匹配(选了「结果=失败」就只看结果那一列)，搜索按整行匹配。
 *  两者都是包含匹配，不是相等：审计的结果列里是「失败」，但请求列表的状态列里
 *  是「失败 · 502」——按相等匹配的话后者永远筛不出来。 */
export function filterRows(
  rows: readonly TableRow[],
  query: string,
  filters: TableFilters,
): TableRow[] {
  const q = normalizeText(query);
  const active = Object.entries(filters).filter(([, value]) => value !== "");
  return rows.filter((row) => {
    if (q && !rowText(row).includes(q)) return false;
    return active.every(([columnId, value]) =>
      normalizeText(String(row.values[columnId] ?? "")).includes(normalizeText(value)),
    );
  });
}

/** 排序。**稳定**：同值的行保持原顺序。
 *
 *  不稳定的排序会让「按状态排」把同状态的行洗乱，而那些行原本是按时间来的——
 *  人会以为数据变了。 */
export function sortRows(rows: readonly TableRow[], sort: TableSort | null): TableRow[] {
  if (!sort) return [...rows];
  const withIndex = rows.map((row, index) => ({ row, index }));
  withIndex.sort((a, b) => {
    const av = sortValue(a.row.sortValues?.[sort.columnId] ?? a.row.values[sort.columnId]);
    const bv = sortValue(b.row.sortValues?.[sort.columnId] ?? b.row.values[sort.columnId]);
    const cmp = compareValues(av, bv);
    if (cmp !== 0) return sort.direction === "asc" ? cmp : -cmp;
    return a.index - b.index;
  });
  return withIndex.map((x) => x.row);
}

export interface PageSlice {
  rows: TableRow[];
  page: number;
  pages: number;
  total: number;
}

/** 分页。`pageSize` 为 undefined 表示**不分页**——数据源自己在翻页
 *  （审计与请求详情走游标）时再叠一层客户端分页，人要点两种「下一页」,
 *  而它们翻的还不是同一批东西。 */
export function paginate(
  rows: readonly TableRow[],
  page: number,
  pageSize: number | undefined,
): PageSlice {
  const total = rows.length;
  if (pageSize === undefined) return { rows: [...rows], page: 1, pages: 1, total };
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const current = Math.min(Math.max(1, page), pages);
  const start = (current - 1) * pageSize;
  return { rows: rows.slice(start, start + pageSize), page: current, pages, total };
}

/** 下一个排序状态：同列点击在 升→降→取消 之间循环。
 *
 *  留出「取消」这一档是因为原始顺序往往有意义（审计按序号倒序、告警按严重度）,
 *  排过之后回不去，人只能刷新页面。 */
export function nextSort(current: TableSort | null, columnId: string): TableSort | null {
  if (!current || current.columnId !== columnId) return { columnId, direction: "asc" };
  if (current.direction === "asc") return { columnId, direction: "desc" };
  return null;
}

export function ariaSort(sort: TableSort | null, columnId: string): "ascending" | "descending" | "none" {
  if (!sort || sort.columnId !== columnId) return "none";
  return sort.direction === "asc" ? "ascending" : "descending";
}

/** 表头当前状态的可读说明，给读屏用。列头上的 ↑↓ 是纯视觉的。 */
export function sortHint(sort: TableSort | null, columnId: string, header: string): string {
  const state = ariaSort(sort, columnId);
  if (state === "none") return `${header}：未排序，按下按升序排列`;
  if (state === "ascending") return `${header}：升序，按下改为降序`;
  return `${header}：降序，按下取消排序`;
}

export interface SelectionState {
  /** 当前页全选框的状态。 */
  checked: boolean;
  indeterminate: boolean;
  selectedOnPage: number;
  pageCount: number;
}

/** 全选框只对**当前页**负责。
 *
 *  跨页全选是另一件事（它会选中人没看见的行），原型也只做页内全选。
 *  半选态必须有：5 行里选了 2 行时，一个未勾选的框会让人以为一行都没选。 */
export function pageSelection(
  pageRows: readonly TableRow[],
  selected: ReadonlySet<string>,
): SelectionState {
  const selectedOnPage = pageRows.filter((row) => selected.has(row.key)).length;
  return {
    checked: pageRows.length > 0 && selectedOnPage === pageRows.length,
    indeterminate: selectedOnPage > 0 && selectedOnPage < pageRows.length,
    selectedOnPage,
    pageCount: pageRows.length,
  };
}

export function toggleKeys(
  selected: ReadonlySet<string>,
  keys: readonly string[],
  checked: boolean,
): Set<string> {
  const next = new Set(selected);
  for (const key of keys) {
    if (checked) next.add(key);
    else next.delete(key);
  }
  return next;
}

/** 工具条上那句「当前看的是什么」。
 *
 *  必须有：一张筛过的表和一张没筛过的表长得一样，而人会照着它做判断。
 *  「共 12 条」在页脚回答的是「筛完剩多少」，这一句回答的是「凭什么筛的」。 */
export function describeCriteria(
  query: string,
  filters: TableFilters,
  filterLabels: Readonly<Record<string, string>>,
  viewName?: string,
): string {
  const parts: string[] = [];
  for (const [columnId, value] of Object.entries(filters)) {
    if (!value) continue;
    parts.push(`${filterLabels[columnId] ?? columnId}：${value}`);
  }
  if (query.trim()) parts.push(`搜索：${query.trim()}`);
  const criteria = parts.length > 0 ? `当前条件：${parts.join(" · ")}` : "全部数据";
  return viewName ? `视图：${viewName} · ${criteria}` : criteria;
}

/** 一个已保存的视图。**只活在内存里**——刷新就没了。
 *
 *  这不是偷懒：交接文档把 SavedView 的持久化排在后面的阶段，而一个「保存了、
 *  刷新就丢」却不说明的按钮比没有这个按钮更糟。所以界面上必须写明
 *  「不会同步或写入浏览器存储」（原型的原话）。 */
export interface SavedView {
  name: string;
  state: Pick<TableViewState, "query" | "filters" | "sort" | "visibleColumns" | "density">;
}

export const CUSTOM_VIEW_NAME = "自定义";

/** 当前状态与某个视图是否一致。用来判断视图下拉该不该跳到「自定义」。 */
export function matchesView(state: TableViewState, view: SavedView): boolean {
  const a = state;
  const b = view.state;
  if (normalizeText(a.query) !== normalizeText(b.query)) return false;
  if (a.density !== b.density) return false;
  if (JSON.stringify(a.sort) !== JSON.stringify(b.sort)) return false;
  const aFilters = Object.entries(a.filters).filter(([, v]) => v !== "").sort();
  const bFilters = Object.entries(b.filters).filter(([, v]) => v !== "").sort();
  if (JSON.stringify(aFilters) !== JSON.stringify(bFilters)) return false;
  return JSON.stringify([...a.visibleColumns].sort()) === JSON.stringify([...b.visibleColumns].sort());
}

/** 视图名限长，与原型的 `maxlength=24` 一致。空名不接受——
 *  一个叫「」的视图在下拉里没法选中。 */
export function normalizeViewName(raw: string): string {
  return raw.replace(/\s+/g, " ").trim().slice(0, 24);
}
