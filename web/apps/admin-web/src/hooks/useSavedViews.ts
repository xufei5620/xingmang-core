import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { PersistedSavedView, SavedViewStateV1 } from "@xingmang/ui-admin";
import { ApiError } from "../api/client";
import {
  listSavedViews,
  removeSavedView,
  setSavedView,
} from "../api/savedViews";

export type SavedViewPersistenceStatus = "loading" | "ready" | "denied" | "error";

export function savedViewQueryKey(tableKey: string) {
  return ["ui", "savedViews", tableKey] as const;
}

function queryStatus(error: unknown, pending: boolean): SavedViewPersistenceStatus {
  if (pending) return "loading";
  if (error instanceof ApiError && error.isAuthFailure) return "denied";
  if (error) return "error";
  return "ready";
}

function errorMessage(error: unknown): string | undefined {
  if (error instanceof ApiError) {
    if (error.missingScope) return `缺少 ${error.missingScope}，个人视图不会保存到浏览器。`;
    return error.message;
  }
  return error instanceof Error ? error.message : error ? "个人视图暂不可用" : undefined;
}

export interface UseSavedViewsResult {
  items: readonly PersistedSavedView[];
  status: SavedViewPersistenceStatus;
  message: string | undefined;
  saving: boolean;
  removing: boolean;
  mutationError: string | undefined;
  lastRunId: string | null;
  save: (name: string, state: SavedViewStateV1) => Promise<{ runId: string }>;
  remove: (id: string) => Promise<{ runId: string }>;
  retry: () => void;
}

export function useSavedViews(tableKey: string): UseSavedViewsResult {
  const queryClient = useQueryClient();
  const [lastRunId, setLastRunId] = useState<string | null>(null);
  const query = useQuery({
    queryKey: savedViewQueryKey(tableKey),
    queryFn: ({ signal }) => listSavedViews(tableKey, { signal }),
    enabled: tableKey !== "",
  });
  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: savedViewQueryKey(tableKey), exact: true });

  const saveMutation = useMutation({
    mutationFn: ({ name, state }: { name: string; state: SavedViewStateV1 }) =>
      setSavedView({ tableKey, name, state }),
    onSuccess: async (run) => {
      setLastRunId(run.runId);
      await invalidate();
    },
  });
  const removeMutation = useMutation({
    mutationFn: (id: string) => removeSavedView(id),
    onSuccess: async (run) => {
      setLastRunId(run.runId);
      await invalidate();
    },
  });

  const mutationFailure = saveMutation.error ?? removeMutation.error;
  return {
    items: query.data ?? [],
    status: queryStatus(query.error, query.isPending),
    message: errorMessage(query.error),
    saving: saveMutation.isPending,
    removing: removeMutation.isPending,
    mutationError: errorMessage(mutationFailure),
    lastRunId,
    save: async (name, state) => {
      const run = await saveMutation.mutateAsync({ name, state });
      return { runId: run.runId };
    },
    remove: async (id) => {
      const run = await removeMutation.mutateAsync(id);
      return { runId: run.runId };
    },
    retry: () => void query.refetch(),
  };
}
