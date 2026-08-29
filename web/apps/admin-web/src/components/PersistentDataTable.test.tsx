import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DataTableColumn } from "@xingmang/ui-admin";

const hook = vi.hoisted(() => ({ useSavedViews: vi.fn() }));
vi.mock("../hooks/useSavedViews", () => ({
  useSavedViews: hook.useSavedViews,
}));

import { PersistentDataTable } from "./PersistentDataTable";

interface Row { id: string; name: string; state: string }
const rows: Row[] = [
  { id: "1", name: "OpenAI 主渠道", state: "正常" },
  { id: "2", name: "Claude 备用", state: "需关注" },
];
const columns: DataTableColumn<Row>[] = [
  { id: "name", header: "渠道", primary: true, value: (row) => row.name, cell: (row) => row.name },
  { id: "state", header: "状态", value: (row) => row.state, cell: (row) => row.state },
];

function hookValue(over: Record<string, unknown> = {}) {
  return {
    items: [], status: "ready", message: undefined, saving: false, removing: false,
    mutationError: undefined, lastRunId: null,
    save: vi.fn().mockResolvedValue({ runId: "run-save" }),
    remove: vi.fn().mockResolvedValue({ runId: "run-remove" }), retry: vi.fn(),
    ...over,
  };
}

function LocationProbe() {
  const location = useLocation();
  return <output aria-label="当前地址">{location.search}</output>;
}

function setup(initialEntry = "/platforms/sub2api?tab=upstream") {
  hook.useSavedViews.mockReturnValue(hookValue());
  render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <PersistentDataTable
        tableKey="platform.sub2api.channels"
        caption="渠道列表"
        columns={columns}
        rows={rows}
        rowKey={(row) => row.id}
        searchable
        filters={[{ columnId: "state", label: "状态", options: ["正常", "需关注"] }]}
        emptyState={<p>没有渠道</p>}
      />
      <LocationProbe />
    </MemoryRouter>,
  );
}

describe("PersistentDataTable Router adapter", () => {
  afterEach(() => vi.clearAllMocks());

  it("使用稳定 table_key 读取个人视图", () => {
    setup();
    expect(hook.useSavedViews).toHaveBeenCalledWith("platform.sub2api.channels");
  });

  it("URL 物化条件优先恢复；拿不到私有 dt_view 时显示自定义", () => {
    setup("/platforms/sub2api?tab=upstream&dt_q=OpenAI&dt_known=name,state&dt_visible=name,state&dt_density=compact&dt_view=foreign-private-id");
    expect((screen.getByRole("searchbox", { name: "搜索当前表格" }) as HTMLInputElement).value).toBe("OpenAI");
    expect(within(screen.getByRole("table")).getAllByRole("row")).toHaveLength(2);
    expect((screen.getByRole("combobox", { name: /视图/ }) as HTMLSelectElement).value).toBe("自定义");
  });

  it("修改表格条件会把完整 dt_* 状态写回 URL，且保留 tab", () => {
    setup();
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索当前表格" }), {
      target: { value: "Claude" },
    });
    const search = screen.getByLabelText("当前地址").textContent ?? "";
    expect(search).toContain("tab=upstream");
    expect(search).toContain("dt_q=Claude");
    expect(search).toContain("dt_known=name%2Cstate");
    expect(search).toContain("dt_visible=name%2Cstate");
    expect(search).toContain("dt_view=%E8%87%AA%E5%AE%9A%E4%B9%89");
  });

  it("SavedView Query 失败时底层表和内置全部仍可用", () => {
    hook.useSavedViews.mockReturnValue(hookValue({ status: "error", message: "个人视图读取失败" }));
    render(
      <MemoryRouter>
        <PersistentDataTable
          tableKey="platform.sub2api.channels"
          caption="渠道列表"
          columns={columns}
          rows={rows}
          rowKey={(row) => row.id}
          emptyState={<p>没有渠道</p>}
        />
      </MemoryRouter>,
    );
    expect(screen.getByRole("table", { name: "渠道列表" })).toBeTruthy();
    expect(screen.getByRole("option", { name: "全部" })).toBeTruthy();
    expect(screen.getByText("个人视图读取失败")).toBeTruthy();
  });

  it("不会把其他 table_key 的个人视图混入当前表", () => {
    const foreign = {
      id: "foreign-view",
      table_key: "platform.newapi.channels",
      name: "NewAPI 视图",
      state_version: 1 as const,
      state: {
        schema_version: 1 as const,
        query: "",
        filters: {},
        sort: null,
        columns: { known: ["name", "state"], visible: ["name", "state"] },
        density: "compact" as const,
      },
      created_at: "2026-08-29T00:00:00Z",
      updated_at: "2026-08-29T00:00:00Z",
    };
    hook.useSavedViews.mockReturnValue(hookValue({ items: [foreign] }));
    render(
      <MemoryRouter>
        <PersistentDataTable
          tableKey="platform.sub2api.channels"
          caption="渠道列表"
          columns={columns}
          rows={rows}
          rowKey={(row) => row.id}
          emptyState={<p>没有渠道</p>}
        />
      </MemoryRouter>,
    );
    expect(screen.queryByRole("option", { name: "NewAPI 视图" })).toBeNull();
  });
});
