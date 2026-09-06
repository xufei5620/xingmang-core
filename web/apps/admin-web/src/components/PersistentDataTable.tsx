import {
  CUSTOM_VIEW_NAME,
  DataTableV2,
  matchesView,
  reconcileSavedViewState,
  toSavedViewStateV1,
  type DataTableV2Props,
  type SavedView,
  type TableColumnCapability,
  type TableViewState,
} from "@xingmang/ui-admin";
import { useSearchParams } from "react-router";
import { useSavedViews } from "../hooks/useSavedViews";
import {
  parseSavedViewSearchParams,
  serializeSavedViewSearchParams,
} from "../lib/savedViewSearchParams";

export const SAVED_VIEW_TABLE_KEYS = {
  auditEvents: "global.audit.events",
  sub2apiChannels: "platform.sub2api.channels",
  newapiChannels: "platform.newapi.channels",
  sub2apiUpstreams: "platform.sub2api.upstreams",
  newapiUpstreams: "platform.newapi.upstreams",
  sub2apiAlerts: "platform.sub2api.alerts",
  newapiAlerts: "platform.newapi.alerts",
} as const;

export type SavedViewTableKey = (typeof SAVED_VIEW_TABLE_KEYS)[keyof typeof SAVED_VIEW_TABLE_KEYS];

const CHANNEL_KEYS: Readonly<Record<"sub2api" | "newapi", SavedViewTableKey>> = {
  sub2api: SAVED_VIEW_TABLE_KEYS.sub2apiChannels,
  newapi: SAVED_VIEW_TABLE_KEYS.newapiChannels,
};

const UPSTREAM_KEYS: Readonly<Record<"sub2api" | "newapi", SavedViewTableKey>> = {
  sub2api: SAVED_VIEW_TABLE_KEYS.sub2apiUpstreams,
  newapi: SAVED_VIEW_TABLE_KEYS.newapiUpstreams,
};

const ALERT_KEYS: Readonly<Record<"sub2api" | "newapi", SavedViewTableKey>> = {
  sub2api: SAVED_VIEW_TABLE_KEYS.sub2apiAlerts,
  newapi: SAVED_VIEW_TABLE_KEYS.newapiAlerts,
};

export function platformSavedViewTableKey(
  platform: "sub2api" | "newapi",
  kind: "channels" | "upstreams" | "alerts",
): SavedViewTableKey {
  if (kind === "channels") return CHANNEL_KEYS[platform];
  if (kind === "upstreams") return UPSTREAM_KEYS[platform];
  return ALERT_KEYS[platform];
}

export interface PersistentDataTableProps<T>
  extends Omit<
    DataTableV2Props<T>,
    "persistence" | "viewState" | "onViewStateChange" | "defaultDensity"
  > {
  tableKey: SavedViewTableKey;
  defaultDensity?: "compact" | "standard" | "comfortable";
  schemaReady?: boolean;
  columnCapabilities?: readonly TableColumnCapability[];
}

function defaultCapabilities<T>(props: PersistentDataTableProps<T>): TableColumnCapability[] {
  return props.columns.map((column) => ({
    id: column.id,
    sortable: column.value !== undefined || column.sortAs !== undefined,
    primary: column.primary === true,
    defaultHidden: column.defaultHidden === true,
  }));
}

function defaultViewState(
  capabilities: readonly TableColumnCapability[],
  density: "compact" | "standard" | "comfortable",
): TableViewState {
  return {
    query: "",
    filters: {},
    sort: null,
    visibleColumns: capabilities
      .filter((column) => column.primary || !column.defaultHidden)
      .map((column) => column.id),
    density,
  };
}

export function PersistentDataTable<T>(props: PersistentDataTableProps<T>) {
  const {
    tableKey,
    defaultDensity = "compact",
    schemaReady = true,
    columnCapabilities: suppliedCapabilities,
    views = [],
    filters = [],
    ...tableProps
  } = props;
  const [searchParams, setSearchParams] = useSearchParams();
  const saved = useSavedViews(tableKey);
  // Query is expected to be table-key scoped server-side. Keep the boundary defensive at the
  // adapter as well so a stale cache or malformed response can never make one table's private
  // views appear in another table's selector.
  const scopedItems = saved.items.filter((item) => item.table_key === tableKey);
  const capabilities = suppliedCapabilities ?? defaultCapabilities(props);
  const fallback = defaultViewState(capabilities, defaultDensity);
  const builtIns: readonly SavedView[] = views.some((view) => view.name === "全部")
    ? views
    : [{ name: "全部", state: fallback }, ...views];
  const reconcileOptions = {
    schemaReady,
    columnCapabilities: capabilities,
    filterOptions: Object.fromEntries(filters.map((filter) => [filter.columnId, filter.options])),
    defaultDensity,
  };

  const requestedRef = searchParams.get("dt_view");
  const requestedPersonal = requestedRef
    ? scopedItems.find((view) => view.id === requestedRef)
    : undefined;
  const requestedBuiltIn = requestedRef
    ? builtIns.find((view) => view.name === requestedRef)
    : undefined;
  const personalReconciled = requestedPersonal
    ? reconcileSavedViewState(
        { ...requestedPersonal.state, schema_version: requestedPersonal.state_version } as typeof requestedPersonal.state,
        reconcileOptions,
      )
    : undefined;
  const base = personalReconciled?.state ?? requestedBuiltIn?.state ?? fallback;
  const parsed = parseSavedViewSearchParams(
    searchParams,
    {
      query: base.query,
      filters: base.filters,
      sort: base.sort,
      visibleColumns: base.visibleColumns,
      density: base.density,
    },
    reconcileOptions,
    [
      ...builtIns.map((view) => ({ ref: view.name, name: view.name })),
      ...scopedItems.map((view) => ({ ref: view.id, name: view.name })),
    ],
  );
  const warnings = [...(personalReconciled?.warnings ?? []), ...parsed.warnings];

  const referenceForState = (state: TableViewState): string => {
    for (const item of scopedItems) {
      const reconciled = reconcileSavedViewState(
        { ...item.state, schema_version: item.state_version } as typeof item.state,
        reconcileOptions,
      );
      if (reconciled.state && matchesView(state, { name: item.name, state: reconciled.state })) {
        return item.id;
      }
    }
    return builtIns.find((view) => matchesView(state, view))?.name ?? CUSTOM_VIEW_NAME;
  };

  return (
    <div className="flex flex-col gap-2">
      {warnings.length > 0 ? (
        <p role="status" className="rounded-md border border-warning bg-warning/10 px-3 py-2 text-xs text-fg">
          {warnings.join("；")}
        </p>
      ) : null}
      <DataTableV2
        {...tableProps}
        filters={filters}
        views={builtIns}
        defaultDensity={defaultDensity}
        viewState={parsed.state}
        onViewStateChange={(next) => {
          const wire = toSavedViewStateV1(next, capabilities);
          setSearchParams(
            serializeSavedViewSearchParams(searchParams, wire, referenceForState(next)),
            { replace: true },
          );
        }}
        persistence={{
          tableKey,
          items: scopedItems,
          schemaReady,
          columnCapabilities: capabilities,
          status: saved.status,
          ...(saved.message ? { message: saved.message } : {}),
          onRetry: saved.retry,
          onSave: saved.save,
          onRemove: saved.remove,
        }}
      />
    </div>
  );
}
