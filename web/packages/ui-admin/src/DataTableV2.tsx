import { useEffect, useId, useMemo, useState, type ReactNode } from "react";
import { cx } from "@xingmang/ui-primitives";
import {
  ariaSort,
  describeCriteria,
  filterRows,
  matchesView,
  nextSort,
  normalizeViewName,
  pageSelection,
  paginate,
  sortHint,
  sortRows,
  toggleKeys,
  reconcileSavedViewState,
  toSavedViewStateV1,
  CUSTOM_VIEW_NAME,
  DENSITY_LABELS,
  type CellValue,
  type Density,
  type SavedView,
  type PersistedSavedView,
  type SavedViewStateV1,
  type TableColumnCapability,
  type TableFilters,
  type TableRow,
  type TableSort,
  type TableViewState,
} from "./dataTable";

export interface DataTableColumn<T> {
  id: string;
  header: string;
  /** 单元格内容。可以是任意节点（徽章、链接、两行文字）。 */
  cell: (row: T) => ReactNode;
  /** 该列的**纯值**：排序、搜索与筛选都用它。
   *  不给 = 这一列不可排序、也不进搜索（例如「操作」那种只有按钮的列）。 */
  value?: (row: T) => CellValue;
  /** 排序专用取值，覆盖 `value`。显示的词与该排的序不是一回事时用它
   *  （严重度显示「严重/警告/提示」，但要按危急程度排，不按中文笔画）。 */
  sortAs?: (row: T) => CellValue;
  /** 数字列：右对齐 + 等宽数字。金额、计数、耗时都该置上。 */
  numeric?: boolean;
  /** 主标识列。不可隐藏——把「是哪一行」这一列藏掉，剩下的表没法读。 */
  primary?: boolean;
  defaultHidden?: boolean;
  headerTitle?: string;
}

export interface DataTableFilterSpec {
  /** 按哪一列筛。该列必须有 `value`。 */
  columnId: string;
  label: string;
  options: readonly string[];
}

export type TableViewChangeReason =
  | "search"
  | "filter"
  | "sort"
  | "columns"
  | "density"
  | "reset"
  | "apply-view";

export interface DataTableViewPersistence {
  tableKey: string;
  items: readonly PersistedSavedView[];
  schemaReady: boolean;
  columnCapabilities: readonly TableColumnCapability[];
  status: "loading" | "ready" | "denied" | "error";
  message?: string;
  onRetry?: () => void;
  onSave: (name: string, state: SavedViewStateV1) => Promise<{ runId: string }>;
  onRemove: (id: string) => Promise<{ runId: string }>;
}

export interface DataTableV2Props<T> {
  /** 表格用途的一句话。渲染成 `<caption class="sr-only">`——
   *  读屏用户进到一张表时，第一句要听到的是「这是什么表」。 */
  caption: string;
  columns: readonly DataTableColumn<T>[];
  rows: readonly T[];
  rowKey: (row: T) => string;
  /** 客户端分页的每页条数。**不传 = 不分页**：数据源自己在翻页（游标）时
   *  再叠一层客户端分页，人要点两种「下一页」，而它们翻的不是同一批东西。 */
  pageSize?: number;
  defaultDensity?: Density;
  /** 工具条上的表内搜索。数据源自带服务端筛选时关掉它（两个搜索框会打架）。 */
  searchable?: boolean;
  filters?: readonly DataTableFilterSpec[];
  /** 预置视图。**只活在内存里**，见 dataTable.ts 的 SavedView。 */
  views?: readonly SavedView[];
  /** 服务端个人视图；ui-admin 只管交互，不认识 Query/Action 传输。 */
  persistence?: DataTableViewPersistence;
  /** 可选受控状态，供 admin-web 把条件放进 Router Search Params。 */
  viewState?: TableViewState;
  onViewStateChange?: (state: TableViewState, reason: TableViewChangeReason) => void;
  selectable?: boolean;
  /** 选中若干行之后出现的操作条。本片只有告警场景传它。 */
  bulkActions?: (selectedKeys: readonly string[]) => ReactNode;
  /** 行展开区（审计的证据、请求的详情）。 */
  renderExpanded?: (row: T) => ReactNode;
  /** 数据源本来就空时显示什么（≠ 筛选之后为空）。 */
  emptyState: ReactNode;
  /** 工具条右侧追加（服务端筛选控件等）。 */
  toolbarExtra?: ReactNode;
  /** 页脚追加（游标翻页、「加载更多」）。 */
  footerExtra?: ReactNode;
  stickyFirstColumn?: boolean;
}

const CELL_PAD: Record<Density, string> = {
  compact: "px-3 py-2",
  standard: "px-3 py-3",
  comfortable: "px-3 py-4",
};

const controlClass =
  "rounded-md border border-edge-strong bg-surface px-2 py-1 text-xs text-fg hover:border-accent focus:outline-2 focus:outline-accent";

/** DataTableV2：排序 / 搜索 / 筛选 / 列显隐 / 密度 / 分页 / 行选择 / 视图。
 *
 *  能力清单照 UI 交接文档 §12.1 与原型的 `dataTableV2`。纯逻辑在 dataTable.ts,
 *  这里只做渲染与事件——排序规则、分页边界、半选态那些**说不清对错的地方**
 *  都在那边被直接断言过。
 *
 *  横向滚动只发生在表格容器内部（§11.2）：页面 body 永远不横滚。
 *  一张 12 列的表把整页撑宽之后，连左侧导航都得横着找。 */
export function DataTableV2<T>({
  caption,
  columns,
  rows,
  rowKey,
  pageSize,
  defaultDensity = "compact",
  searchable = false,
  filters = [],
  views = [],
  persistence,
  viewState,
  onViewStateChange,
  selectable = false,
  bulkActions,
  renderExpanded,
  emptyState,
  toolbarExtra,
  footerExtra,
  stickyFirstColumn = false,
}: DataTableV2Props<T>) {
  const domId = useId();
  const defaultVisible = useMemo(
    () => columns.filter((c) => c.primary || !c.defaultHidden).map((c) => c.id),
    [columns],
  );

  const [query, setQuery] = useState("");
  const [filterValues, setFilterValues] = useState<TableFilters>({});
  const [sort, setSort] = useState<TableSort | null>(null);
  const [visible, setVisible] = useState<readonly string[]>(defaultVisible);
  const [density, setDensity] = useState<Density>(defaultDensity);
  const [page, setPage] = useState(1);
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const [sessionViews, setSessionViews] = useState<readonly SavedView[]>([]);
  const [viewDraft, setViewDraft] = useState("");
  const [selectedPersistentId, setSelectedPersistentId] = useState<string | null>(null);
  const [viewNotice, setViewNotice] = useState<string | null>(null);
  const [viewError, setViewError] = useState<string | null>(null);
  const [savingView, setSavingView] = useState(false);
  const [removingView, setRemovingView] = useState(false);
  const [productivityEpoch, setProductivityEpoch] = useState(0);

  // Controlled mode is used by the Router adapter. We still mirror it locally so legacy call
  // sites and the render pipeline stay identical; incoming URL changes remain authoritative.
  useEffect(() => {
    if (!viewState) return;
    setQuery(viewState.query);
    setFilterValues(viewState.filters);
    setSort(viewState.sort);
    setVisible(viewState.visibleColumns);
    setDensity(viewState.density);
  }, [viewState]);

  // 业务对象 → 逻辑层认识的行。列的 `value` 决定了什么能被搜到、被排序
  const tableRows: TableRow[] = useMemo(
    () =>
      rows.map((row) => {
        const values: Record<string, CellValue> = {};
        const sortValues: Record<string, CellValue> = {};
        for (const column of columns) {
          if (column.value) values[column.id] = column.value(row);
          if (column.sortAs) sortValues[column.id] = column.sortAs(row);
        }
        return { key: rowKey(row), values, sortValues };
      }),
    [rows, columns, rowKey],
  );
  const byKey = useMemo(() => {
    const map = new Map<string, T>();
    for (const row of rows) map.set(rowKey(row), row);
    return map;
  }, [rows, rowKey]);

  const filtered = useMemo(
    () => sortRows(filterRows(tableRows, query, filterValues), sort),
    [tableRows, query, filterValues, sort],
  );
  const slice = paginate(filtered, page, pageSize);
  const pageKeys = slice.rows.map((r) => r.key);
  const selection = pageSelection(slice.rows, selected);
  const shownColumns = columns.filter((c) => visible.includes(c.id));
  const allViews = [...views, ...sessionViews];

  const state: TableViewState = {
    query,
    filters: filterValues,
    sort,
    visibleColumns: visible,
    density,
  };
  const filterOptions = Object.fromEntries(filters.map((filter) => [filter.columnId, filter.options]));
  const persistentViews = (persistence?.items ?? []).map((item) => ({
    item,
    reconciled: reconcileSavedViewState(
      { ...item.state, schema_version: item.state_version } as SavedViewStateV1,
      {
        schemaReady: persistence?.schemaReady ?? false,
        columnCapabilities: persistence?.columnCapabilities ?? [],
        filterOptions,
        defaultDensity,
      },
    ),
  }));
  const matchedPersistent = persistentViews.find(
    ({ reconciled }) => reconciled.state !== null && matchesView(state, { name: "", state: reconciled.state }),
  );
  const matchedBuiltIn = allViews.find((view) => matchesView(state, view));
  const selectedPersistent = selectedPersistentId
    ? persistentViews.find(({ item }) => item.id === selectedPersistentId)
    : undefined;
  const activePersistent = selectedPersistent ?? matchedPersistent;
  const activeView = activePersistent?.item.name ?? matchedBuiltIn?.name ?? CUSTOM_VIEW_NAME;
  const activeViewValue = persistence
    ? activePersistent
      ? `personal:${activePersistent.item.id}`
      : matchedBuiltIn
        ? `builtin:${matchedBuiltIn.name}`
        : CUSTOM_VIEW_NAME
    : activeView;
  const filterLabels = Object.fromEntries(filters.map((f) => [f.columnId, f.label]));
  // 没有配任何视图时不带视图前缀：「视图：自定义」会让人以为有一套视图机制
  const summary = describeCriteria(
    query,
    filterValues,
    filterLabels,
    allViews.length > 0 || persistence ? activeView : undefined,
  );
  const hasCriteria = query.trim() !== "" || Object.values(filterValues).some((v) => v !== "");

  const commitViewState = (
    next: TableViewState,
    reason: TableViewChangeReason,
    clearTransient = false,
  ) => {
    setQuery(next.query);
    setFilterValues(next.filters);
    setSort(next.sort);
    setVisible(next.visibleColumns);
    setDensity(next.density);
    setPage(1);
    if (clearTransient) {
      setSelected(new Set());
      setExpanded(new Set());
      setProductivityEpoch((value) => value + 1);
    }
    if (reason !== "apply-view") setSelectedPersistentId(null);
    onViewStateChange?.(next, reason);
  };

  const reset = () => {
    commitViewState({ ...state, query: "", filters: {} }, "reset");
  };

  const applyView = (value: string) => {
    setViewError(null);
    setViewNotice(null);
    if (persistence && value.startsWith("personal:")) {
      const id = value.slice("personal:".length);
      const personal = persistentViews.find(({ item }) => item.id === id);
      if (!personal) return;
      setSelectedPersistentId(id);
      if (personal.reconciled.deferred) {
        setViewNotice("列结构仍在加载，个人视图已排队，暂不改动当前表格。请稍后重试。");
        return;
      }
      if (!personal.reconciled.state) {
        setViewError(personal.reconciled.warnings.join("；"));
        return;
      }
      commitViewState(personal.reconciled.state, "apply-view", true);
      if (personal.reconciled.warnings.length > 0) {
        setViewNotice(personal.reconciled.warnings.join("；"));
      }
      return;
    }
    const name = persistence && value.startsWith("builtin:")
      ? value.slice("builtin:".length)
      : value;
    const view = allViews.find((candidate) => candidate.name === name);
    if (!view) return;
    setSelectedPersistentId(null);
    commitViewState(
      {
        query: view.state.query,
        filters: view.state.filters,
        sort: view.state.sort,
        visibleColumns: view.state.visibleColumns,
        density: view.state.density,
      },
      "apply-view",
      true,
    );
  };

  const saveView = async () => {
    const name = normalizeViewName(viewDraft);
    // 空名的视图在下拉里没法选中，直接不收
    if (!name) return;
    setViewError(null);
    setViewNotice(null);
    if (persistence) {
      if (name === CUSTOM_VIEW_NAME || name === "全部" || views.some((view) => view.name === name)) {
        setViewError("该名称已由内置视图占用，请换一个名称。");
        return;
      }
      if (persistence.status !== "ready") {
        setViewError(persistence.message ?? "个人视图暂不可用，当前条件没有被保存。");
        return;
      }
      setSavingView(true);
      try {
        const result = await persistence.onSave(
          name,
          toSavedViewStateV1(state, persistence.columnCapabilities),
        );
        setViewDraft("");
        setViewNotice(`个人视图已保存，run_id=${result.runId}`);
      } catch (error) {
        setViewError(error instanceof Error ? error.message : "保存失败，当前条件没有被保存。");
      } finally {
        setSavingView(false);
      }
      return;
    }
    setSessionViews((prev) => [...prev.filter((v) => v.name !== name), { name, state }]);
    setViewDraft("");
  };

  const removeActiveView = async () => {
    if (!persistence || !activePersistent) return;
    setViewError(null);
    setViewNotice(null);
    setRemovingView(true);
    try {
      const result = await persistence.onRemove(activePersistent.item.id);
      setSelectedPersistentId(null);
      setViewNotice(`个人视图已删除，run_id=${result.runId}`);
    } catch (error) {
      setViewError(error instanceof Error ? error.message : "删除失败，个人视图仍然保留。");
    } finally {
      setRemovingView(false);
    }
  };

  const changeSelection = (keys: readonly string[], checked: boolean) => {
    setSelected((prev) => toggleKeys(prev, keys, checked));
  };

  const columnCount = shownColumns.length + (selectable ? 1 : 0) + (renderExpanded ? 1 : 0);

  // 数据源本来就空：这时候工具条、表头、分页全都没有意义
  if (rows.length === 0) return <>{emptyState}</>;

  return (
    <section
      data-density={density}
      // relative 是必须的，不是装饰：sr-only 是 position:absolute，没有一个
      // 定位祖先时它会一路上溯到文档，跑到表格的横向滚动容器**外面**去,
      // 于是整个页面被一个 1px 的隐藏元素撑出横向滚动条（§11.2 明令禁止）。
      // 浏览器里实测过：1024px 视口下 documentElement 因此宽出 38px
      className="relative flex flex-col overflow-hidden rounded-lg border border-edge bg-surface shadow-sm"
    >
      <div className="flex flex-wrap items-end gap-2 border-b border-edge bg-surface-muted px-3 py-2">
        {filters.map((filter) => (
          <label key={filter.columnId} className="flex flex-col gap-1 text-xs text-fg-muted">
            <span>{filter.label}</span>
            <select
              className={controlClass}
              value={filterValues[filter.columnId] ?? ""}
              onChange={(event) => {
                commitViewState(
                  {
                    ...state,
                    filters: { ...state.filters, [filter.columnId]: event.target.value },
                  },
                  "filter",
                );
              }}
            >
              <option value="">全部</option>
              {filter.options.map((option) => (
                <option key={option} value={option}>
                  {option}
                </option>
              ))}
            </select>
          </label>
        ))}

        {hasCriteria ? (
          <button type="button" onClick={reset} className={controlClass}>
            清除条件
          </button>
        ) : null}

        {/* 「当前看的是什么」。一张筛过的表和一张没筛过的表长得一样,
            而人会照着它做判断 */}
        <span className="text-xs text-fg-muted">{summary}</span>

        <div className="ml-auto flex flex-wrap items-end gap-2">
          {toolbarExtra}

          {searchable ? (
            <label className="flex flex-col gap-1 text-xs text-fg-muted">
              <span className="sr-only">搜索当前表格</span>
              <input
                type="search"
                aria-label="搜索当前表格"
                placeholder="搜索当前列表"
                value={query}
              onChange={(event) => {
                  commitViewState({ ...state, query: event.target.value }, "search");
                }}
                className={cx(controlClass, "w-40")}
              />
            </label>
          ) : null}

          {allViews.length > 0 || persistence ? (
            <label className="flex flex-col gap-1 text-xs text-fg-muted">
              <span>视图</span>
              <select
                className={controlClass}
                value={activeViewValue}
                onChange={(event) => applyView(event.target.value)}
              >
                {persistence ? (
                  <>
                    <optgroup label="内置视图">
                      {allViews.map((view) => (
                        <option key={view.name} value={`builtin:${view.name}`}>
                          {view.name}
                        </option>
                      ))}
                    </optgroup>
                    <optgroup label="个人视图">
                      {persistentViews.map(({ item }) => (
                        <option key={item.id} value={`personal:${item.id}`}>
                          {item.name}
                        </option>
                      ))}
                    </optgroup>
                  </>
                ) : (
                  allViews.map((view) => (
                    <option key={view.name} value={view.name}>
                      {view.name}
                    </option>
                  ))
                )}
                {/* 当前条件不匹配任何视图时下拉停在「自定义」——
                    否则它会一直显示上一个视图名，等于在撒谎 */}
                <option value={CUSTOM_VIEW_NAME}>{CUSTOM_VIEW_NAME}</option>
              </select>
            </label>
          ) : null}

          {allViews.length > 0 || persistence ? (
            <details key={`save-${productivityEpoch}`} className="relative">
              <summary className={cx(controlClass, "cursor-pointer list-none")}>保存视图</summary>
              <div className="absolute right-0 z-10 mt-1 flex w-56 flex-col gap-2 rounded-md border border-edge bg-surface p-3 shadow-md">
                <label className="flex flex-col gap-1 text-xs text-fg-muted">
                  <span>视图名称</span>
                  <input
                    type="text"
                    maxLength={24}
                    value={viewDraft}
                    onChange={(event) => setViewDraft(event.target.value)}
                    placeholder="例如：高风险复核"
                    className={controlClass}
                  />
                </label>
                <button
                  type="button"
                  onClick={() => void saveView()}
                  disabled={savingView || (persistence !== undefined && persistence.status !== "ready")}
                  className={cx(controlClass, "disabled:cursor-not-allowed disabled:opacity-45")}
                >
                  {persistence ? (savingView ? "保存中…" : "保存到个人视图") : "保存到本次会话"}
                </button>
                <p className="text-xs text-fg-muted">
                  {persistence
                    ? "保存到当前身份与当前环境；不会写入浏览器存储。"
                    : "不会同步或写入浏览器存储。"}
                </p>
              </div>
            </details>
          ) : null}

          {persistence && activePersistent ? (
            <button
              type="button"
              onClick={() => void removeActiveView()}
              disabled={removingView || persistence.status !== "ready"}
              className={cx(controlClass, "disabled:cursor-not-allowed disabled:opacity-45")}
            >
              {removingView ? "删除中…" : "删除个人视图"}
            </button>
          ) : null}

          <details key={`columns-${productivityEpoch}`} className="relative">
            <summary className={cx(controlClass, "cursor-pointer list-none")}>列管理</summary>
            <div className="absolute right-0 z-10 mt-1 flex w-56 flex-col gap-1 rounded-md border border-edge bg-surface p-3 shadow-md">
              {columns.map((column) => (
                <label key={column.id} className="flex items-center gap-2 text-xs text-fg">
                  <input
                    type="checkbox"
                    checked={visible.includes(column.id)}
                    // 主标识列不给关：把「是哪一行」藏掉，剩下的表没法读
                    disabled={column.primary}
                    onChange={(event) =>
                      commitViewState(
                        {
                          ...state,
                          visibleColumns: event.target.checked
                            ? [...visible, column.id]
                            : visible.filter((id) => id !== column.id),
                        },
                        "columns",
                      )
                    }
                  />
                  <span>
                    {column.header}
                    {column.primary ? " · 主标识" : null}
                  </span>
                </label>
              ))}
              <button
                type="button"
                onClick={() => commitViewState({ ...state, visibleColumns: defaultVisible }, "columns")}
                className={cx(controlClass, "mt-1")}
              >
                恢复默认列
              </button>
            </div>
          </details>

          <label className="flex flex-col gap-1 text-xs text-fg-muted">
            <span>密度</span>
            <select
              aria-label="表格密度"
              className={controlClass}
              value={density}
              onChange={(event) =>
                commitViewState({ ...state, density: event.target.value as Density }, "density")
              }
            >
              {(Object.keys(DENSITY_LABELS) as Density[]).map((value) => (
                <option key={value} value={value}>
                  {DENSITY_LABELS[value]}
                </option>
              ))}
            </select>
          </label>
        </div>
      </div>

      {persistence && persistence.status !== "ready" ? (
        <div className="flex flex-wrap items-center gap-2 border-b border-edge bg-surface px-3 py-2 text-xs text-fg-muted">
          <span>
            {persistence.status === "loading"
              ? "个人视图加载中；表格与内置视图仍可使用。"
              : persistence.message ?? "个人视图暂不可用；当前条件不会写入浏览器。"}
          </span>
          {persistence.status === "error" && persistence.onRetry ? (
            <button type="button" onClick={persistence.onRetry} className="font-medium text-accent hover:underline">
              重试
            </button>
          ) : null}
        </div>
      ) : null}
      {viewError ? (
        <p role="alert" className="border-b border-edge bg-danger/10 px-3 py-2 text-xs text-danger">
          {viewError}
        </p>
      ) : null}
      {viewNotice ? (
        <p role="status" className="border-b border-edge bg-success/10 px-3 py-2 text-xs text-success">
          {viewNotice}
        </p>
      ) : null}

      {selectable && selected.size > 0 ? (
        <div className="flex flex-wrap items-center gap-3 border-b border-edge bg-accent-soft px-3 py-2 text-xs">
          <b aria-live="polite" className="text-fg">
            已选择 {selected.size} 条
          </b>
          {bulkActions?.([...selected])}
          <button type="button" onClick={() => setSelected(new Set())} className={controlClass}>
            清除选择
          </button>
        </div>
      ) : null}

      {/* 横向滚动关在这一层里：页面 body 永不横滚（§11.2） */}
      <div className="max-w-full overflow-x-auto">
        <table className="w-full border-collapse text-sm">
          <caption className="sr-only">{caption}</caption>
          <thead className="border-b border-edge bg-surface-muted">
            <tr>
              {selectable ? (
                <th scope="col" className={cx(CELL_PAD[density], "w-10 text-left")}>
                  <input
                    type="checkbox"
                    aria-label="选择本页全部"
                    checked={selection.checked}
                    ref={(node) => {
                      // 半选态只能用 DOM 属性设：5 行里选了 2 行时,
                      // 一个未勾选的框会让人以为一行都没选
                      if (node) node.indeterminate = selection.indeterminate;
                    }}
                    onChange={(event) => changeSelection(pageKeys, event.target.checked)}
                  />
                </th>
              ) : null}
              {shownColumns.map((column) => (
                <th
                  key={column.id}
                  scope="col"
                  aria-sort={
                    column.value !== undefined || column.sortAs !== undefined
                      ? ariaSort(sort, column.id)
                      : undefined
                  }
                  title={column.headerTitle}
                  className={cx(
                    "text-xs font-medium text-fg-muted",
                    column.numeric ? "text-right" : "text-left",
                    column.value !== undefined || column.sortAs !== undefined
                      ? "p-0"
                      : CELL_PAD[density],
                  )}
                >
                  {column.value !== undefined || column.sortAs !== undefined ? (
                    <button
                      type="button"
                      onClick={() => {
                        commitViewState(
                          { ...state, sort: nextSort(sort, column.id) },
                          "sort",
                        );
                      }}
                      title={sortHint(sort, column.id, column.header)}
                      className={cx(
                        CELL_PAD[density],
                        "flex w-full items-center gap-1 font-medium whitespace-nowrap hover:text-accent",
                        column.numeric ? "justify-end" : "justify-start",
                      )}
                    >
                      {column.header}
                      <span aria-hidden="true">
                        {ariaSort(sort, column.id) === "ascending"
                          ? "↑"
                          : ariaSort(sort, column.id) === "descending"
                            ? "↓"
                            : ""}
                      </span>
                    </button>
                  ) : (
                    column.header
                  )}
                </th>
              ))}
              {renderExpanded ? (
                // 用 aria-label 而不是 sr-only 子元素：这一列没有可见表头,
                // 而一个隐藏的定位元素在滚动容器里只会带来麻烦
                <th
                  scope="col"
                  aria-label="详情"
                  className={cx(CELL_PAD[density], "w-16 text-left")}
                />
              ) : null}
            </tr>
          </thead>
          <tbody>
            {slice.rows.map((tableRow, index) => {
              const row = byKey.get(tableRow.key);
              if (!row) return null;
              const open = expanded.has(tableRow.key);
              return (
                <tr
                  key={tableRow.key}
                  // 斑马纹用专门的令牌：surface-muted 是工具条与悬停用的,
                  // 拿它做隔行底色会让整张表看起来一半是选中态
                  className={cx(
                    "border-b border-edge last:border-b-0",
                    index % 2 === 1 ? "bg-table-stripe" : null,
                  )}
                >
                  {selectable ? (
                    <td className={CELL_PAD[density]}>
                      <input
                        type="checkbox"
                        aria-label={`选择 ${tableRow.key}`}
                        checked={selected.has(tableRow.key)}
                        onChange={(event) => changeSelection([tableRow.key], event.target.checked)}
                      />
                    </td>
                  ) : null}
                  {shownColumns.map((column) => (
                    <td
                      key={column.id}
                      className={cx(
                        CELL_PAD[density],
                        "align-top",
                        column.numeric ? "text-right tabular-nums" : null,
                        stickyFirstColumn && column.id === shownColumns[0]?.id
                          ? "sticky left-0 bg-surface"
                          : null,
                      )}
                    >
                      {column.cell(row)}
                    </td>
                  ))}
                  {renderExpanded ? (
                    <td className={CELL_PAD[density]}>
                      <button
                        type="button"
                        aria-expanded={open}
                        aria-controls={`${domId}-detail-${tableRow.key}`}
                        onClick={() =>
                          setExpanded((prev) => {
                            const next = new Set(prev);
                            if (!next.delete(tableRow.key)) next.add(tableRow.key);
                            return next;
                          })
                        }
                        className="text-xs font-medium text-accent hover:underline"
                      >
                        {open ? "收起" : "详情"}
                      </button>
                    </td>
                  ) : null}
                </tr>
              );
            })}
            {/* 展开区必须是**独立的一行**并跨满整表：塞进原来那一行的某个单元格里,
                列一隐藏它就跟着消失了 */}
            {renderExpanded
              ? slice.rows
                  .filter((tableRow) => expanded.has(tableRow.key))
                  .map((tableRow) => {
                    const row = byKey.get(tableRow.key);
                    if (!row) return null;
                    return (
                      <tr key={`${tableRow.key}-detail`} className="border-b border-edge">
                        <td
                          id={`${domId}-detail-${tableRow.key}`}
                          colSpan={columnCount}
                          className="bg-surface-muted px-3 py-3"
                        >
                          {renderExpanded(row)}
                        </td>
                      </tr>
                    );
                  })
              : null}
          </tbody>
        </table>
      </div>

      {slice.total === 0 ? (
        <div className="flex flex-col items-center gap-2 px-3 py-7 text-center">
          <b className="text-sm text-fg">没有匹配的数据</b>
          <p className="text-xs text-fg-muted">调整搜索或筛选条件后重试。</p>
          <button type="button" onClick={reset} className={controlClass}>
            清除筛选
          </button>
        </div>
      ) : null}

      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-edge px-3 py-2 text-xs text-fg-muted">
        <span>共 {slice.total} 条</span>
        <span aria-live="polite" aria-atomic="true" className="sr-only">
          第 {slice.page} 页，共 {slice.pages} 页
        </span>
        <div className="flex items-center gap-3">
          {footerExtra}
          {pageSize !== undefined && slice.pages > 1 ? (
            <div className="flex items-center gap-2" aria-label="分页">
              <button
                type="button"
                disabled={slice.page <= 1}
                onClick={() => setPage(slice.page - 1)}
                className={cx(controlClass, "disabled:cursor-not-allowed disabled:opacity-45")}
              >
                上一页
              </button>
              <span className="min-w-14 text-center font-mono">
                {slice.page} / {slice.pages}
              </span>
              <button
                type="button"
                disabled={slice.page >= slice.pages}
                onClick={() => setPage(slice.page + 1)}
                className={cx(controlClass, "disabled:cursor-not-allowed disabled:opacity-45")}
              >
                下一页
              </button>
            </div>
          ) : null}
        </div>
      </div>
    </section>
  );
}
