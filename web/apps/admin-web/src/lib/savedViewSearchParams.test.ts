import { describe, expect, it } from "vitest";
import type { TableColumnCapability, TableViewState } from "@xingmang/ui-admin";
import {
  parseSavedViewSearchParams,
  serializeSavedViewSearchParams,
} from "./savedViewSearchParams";

const columns: TableColumnCapability[] = [
  { id: "name", sortable: true, primary: true, defaultHidden: false },
  { id: "status", sortable: true, primary: false, defaultHidden: false },
  { id: "grossProfit", sortable: true, primary: false, defaultHidden: false },
  { id: "internal", sortable: false, primary: false, defaultHidden: true },
];

const base: TableViewState = {
  query: "from-view",
  filters: { status: "正常" },
  sort: { columnId: "name", direction: "asc" },
  visibleColumns: ["name", "status"],
  density: "compact",
};

const options = {
  schemaReady: true,
  columnCapabilities: columns,
  filterOptions: { status: ["正常", "需关注"] },
  defaultDensity: "compact" as const,
};

describe("SavedView Router Search Params", () => {
  it("序列化完整可分享条件，同时保留平台 tab/sub 等非表格参数", () => {
    const params = new URLSearchParams("tab=upstream&sub=profit&dt_f.old=remove-me");
    const next = serializeSavedViewSearchParams(
      params,
      {
        schema_version: 1,
        query: "open ai",
        filters: { status: "需关注" },
        sort: { column_id: "grossProfit", direction: "desc" },
        columns: { known: ["name", "status", "grossProfit"], visible: ["name", "grossProfit"] },
        density: "standard",
      },
      "view-123",
    );
    expect(next.get("tab")).toBe("upstream");
    expect(next.get("sub")).toBe("profit");
    expect(next.get("dt_q")).toBe("open ai");
    expect(next.get("dt_f.status")).toBe("需关注");
    expect(next.has("dt_f.old")).toBe(false);
    expect(next.get("dt_sort")).toBe("grossProfit:desc");
    expect(next.get("dt_known")).toBe("name,status,grossProfit");
    expect(next.get("dt_visible")).toBe("name,grossProfit");
    expect(next.get("dt_density")).toBe("standard");
    expect(next.get("dt_view")).toBe("view-123");
  });

  it("URL 有效字段逐项覆盖所选视图，缺席字段继续沿用视图", () => {
    const parsed = parseSavedViewSearchParams(
      new URLSearchParams("dt_q=url-query&dt_f.status=需关注&dt_sort=grossProfit:desc&dt_density=comfortable&dt_view=view-123"),
      base,
      options,
      [{ ref: "view-123", name: "高风险" }],
    );
    expect(parsed.state).toEqual({
      query: "url-query",
      filters: { status: "需关注" },
      sort: { columnId: "grossProfit", direction: "desc" },
      visibleColumns: ["name", "status"],
      density: "comfortable",
    });
    expect(parsed.activeViewName).toBe("高风险");
  });

  it("无效值不覆盖可信基准并产生可见警告", () => {
    const parsed = parseSavedViewSearchParams(
      new URLSearchParams(
        "dt_f.unknown=x&dt_f.status=不存在&dt_sort=internal:desc&dt_density=dense&dt_known=name,bad%20id&dt_visible=name",
      ),
      base,
      options,
      [],
    );
    expect(parsed.state).toEqual(base);
    expect(parsed.warnings.length).toBeGreaterThan(0);
  });

  it("收件人拿不到私有 dt_view 时仍按物化条件恢复，并显示为自定义", () => {
    const parsed = parseSavedViewSearchParams(
      new URLSearchParams(
        "dt_q=shared&dt_sort=grossProfit:desc&dt_known=name,status,grossProfit&dt_visible=name,grossProfit&dt_density=standard&dt_view=private-id",
      ),
      { ...base, query: "", sort: null, filters: {} },
      options,
      [],
    );
    expect(parsed.state.query).toBe("shared");
    expect(parsed.state.sort).toEqual({ columnId: "grossProfit", direction: "desc" });
    expect(parsed.state.visibleColumns).toEqual(["name", "grossProfit"]);
    expect(parsed.activeViewName).toBe("自定义");
    expect(parsed.viewRef).toBe("private-id");
  });
});
