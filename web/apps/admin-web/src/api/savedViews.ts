import type { PersistedSavedView, SavedViewStateV1 } from "@xingmang/ui-admin";
import { apiClient, type ApiClient } from "./client";
import {
  executeAction,
  type ActionRun,
  type ListOptions,
} from "./platform";

export const SAVED_VIEW_MANAGE_PERMISSION = "ui.saved_view.manage";

interface SavedViewListResponse {
  items: PersistedSavedView[] | null;
}

export async function listSavedViews(
  tableKey: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<PersistedSavedView[]> {
  const body = await client.get<SavedViewListResponse>("/api/v1/ui/saved-views", {
    searchParams: { table_key: tableKey },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return body.items ?? [];
}

export interface SetSavedViewInput {
  tableKey: string;
  name: string;
  state: SavedViewStateV1;
}

function canonicalFilters(filters: Readonly<Record<string, string>>): string {
  return JSON.stringify(
    Object.fromEntries(Object.entries(filters).sort(([left], [right]) => left.localeCompare(right))),
  );
}

export function setSavedView(
  input: SetSavedViewInput,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  const sort = input.state.sort;
  return executeAction(
    {
      actionId: "ui.saved_view.set",
      version: "1",
      params: {
        table_key: input.tableKey,
        name: input.name,
        schema_version: input.state.schema_version,
        query: input.state.query,
        filters_json: canonicalFilters(input.state.filters),
        sort_column: sort?.column_id ?? "",
        sort_direction: sort?.direction ?? "",
        known_columns: [...input.state.columns.known],
        visible_columns: [...input.state.columns.visible],
        density: input.state.density,
      },
    },
    options,
    client,
  );
}

export function removeSavedView(
  id: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    {
      actionId: "ui.saved_view.remove",
      version: "1",
      params: { saved_view_id: id },
    },
    options,
    client,
  );
}
