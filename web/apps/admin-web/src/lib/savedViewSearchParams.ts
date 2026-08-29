import {
  CUSTOM_VIEW_NAME,
  reconcileSavedViewState,
  toSavedViewStateV1,
  type SavedViewReconcileOptions,
  type SavedViewStateV1,
  type TableViewState,
} from "@xingmang/ui-admin";

const PREFIX = "dt_";
const FILTER_PREFIX = "dt_f.";
const ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

export interface SavedViewReference {
  ref: string;
  name: string;
}

export interface ParsedSavedViewSearchParams {
  state: TableViewState;
  viewRef: string | null;
  activeViewName: string | null;
  warnings: readonly string[];
  hasExplicitCriteria: boolean;
}

function clearTableParams(params: URLSearchParams): void {
  for (const key of [...params.keys()]) {
    if (key.startsWith(PREFIX)) params.delete(key);
  }
}

/** 应用视图时把完整可分享条件物化到 URL。tab/sub 等页面上下文原样保留。 */
export function serializeSavedViewSearchParams(
  current: URLSearchParams,
  state: SavedViewStateV1,
  viewRef: string,
): URLSearchParams {
  const next = new URLSearchParams(current);
  clearTableParams(next);
  next.set("dt_q", state.query);
  for (const [id, value] of Object.entries(state.filters)) {
    if (value !== "") next.set(`${FILTER_PREFIX}${id}`, value);
  }
  if (state.sort) next.set("dt_sort", `${state.sort.column_id}:${state.sort.direction}`);
  next.set("dt_known", state.columns.known.join(","));
  next.set("dt_visible", state.columns.visible.join(","));
  next.set("dt_density", state.density);
  next.set("dt_view", viewRef);
  return next;
}

function validText(value: string, maxRunes: number): boolean {
  return Array.from(value).length <= maxRunes;
}

function parseIDs(value: string | null): string[] | null {
  if (value === null) return null;
  const ids = value.split(",").filter((id) => id !== "");
  if (ids.length === 0 || ids.length > 64 || new Set(ids).size !== ids.length) return null;
  return ids.every((id) => ID_PATTERN.test(id)) ? ids : null;
}

/** URL 只覆盖它明确携带且校验通过的字段。无效值不会把可信视图状态清空。 */
export function parseSavedViewSearchParams(
  params: URLSearchParams,
  base: TableViewState,
  options: SavedViewReconcileOptions,
  available: readonly SavedViewReference[],
): ParsedSavedViewSearchParams {
  const warnings: string[] = [];
  let state: TableViewState = {
    query: base.query,
    filters: { ...base.filters },
    sort: base.sort ? { ...base.sort } : null,
    visibleColumns: [...base.visibleColumns],
    density: base.density,
  };
  let hasExplicitCriteria = false;

  if (params.has("dt_q")) {
    hasExplicitCriteria = true;
    const value = params.get("dt_q") ?? "";
    if (validText(value, 256)) state = { ...state, query: value.replace(/\s+/g, " ").trim() };
    else warnings.push("URL 搜索词超过 256 个字符，已忽略");
  }

  const filters = { ...state.filters };
  for (const [key, value] of params) {
    if (!key.startsWith(FILTER_PREFIX)) continue;
    hasExplicitCriteria = true;
    const id = key.slice(FILTER_PREFIX.length);
    const choices = options.filterOptions[id];
    if (!choices || !choices.includes(value)) {
      warnings.push(`URL 筛选 ${id || "（空）"} 已失效，已忽略`);
      continue;
    }
    filters[id] = value;
  }
  state = { ...state, filters };

  if (params.has("dt_sort")) {
    hasExplicitCriteria = true;
    const raw = params.get("dt_sort") ?? "";
    const match = raw.match(/^([A-Za-z0-9][A-Za-z0-9._-]{0,127}):(asc|desc)$/);
    const capability = match
      ? options.columnCapabilities.find((column) => column.id === match[1])
      : undefined;
    if (match && capability?.sortable) {
      state = { ...state, sort: { columnId: match[1] as string, direction: match[2] as "asc" | "desc" } };
    } else {
      warnings.push("URL 排序已失效，已忽略");
    }
  }

  if (params.has("dt_density")) {
    hasExplicitCriteria = true;
    const density = params.get("dt_density");
    if (density === "compact" || density === "standard" || density === "comfortable") {
      state = { ...state, density };
    } else {
      warnings.push("URL 表格密度已失效，已忽略");
    }
  }

  const hasKnown = params.has("dt_known");
  const hasVisible = params.has("dt_visible");
  if (hasKnown || hasVisible) {
    hasExplicitCriteria = true;
    const known = parseIDs(params.get("dt_known"));
    const visible = parseIDs(params.get("dt_visible"));
    if (!hasKnown || !hasVisible || known === null || visible === null) {
      warnings.push("URL 列集合不完整或格式无效，已忽略");
    } else {
      const wire: SavedViewStateV1 = {
        ...toSavedViewStateV1(state, options.columnCapabilities),
        columns: { known, visible },
      };
      const reconciled = reconcileSavedViewState(wire, options);
      warnings.push(...reconciled.warnings);
      if (reconciled.state) state = reconciled.state;
    }
  }

  const viewRef = params.get("dt_view");
  const availableView = viewRef === null ? undefined : available.find((view) => view.ref === viewRef);
  const activeViewName = availableView?.name ??
    (hasExplicitCriteria ? CUSTOM_VIEW_NAME : null);
  return { state, viewRef, activeViewName, warnings, hasExplicitCriteria };
}
