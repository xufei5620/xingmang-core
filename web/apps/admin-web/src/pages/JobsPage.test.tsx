import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { JobsPage } from "./JobsPage";

function renderPage(entry = "/jobs") {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <JobsPage />
    </MemoryRouter>,
  );
}

describe("后台任务只读 UI shell", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    // 页面尚未接入 Worker/River Query；即使宿主提供 fetch，也不能被这张只读蓝图
    // 悄悄当成数据入口。
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("展示五个导航同源页签与明确的未接入边界", () => {
    renderPage();

    expect(screen.getByRole("heading", { name: "后台任务", level: 2 })).not.toBeNull();
    for (const label of ["运行中", "定时任务", "同步批次", "失败与重试", "多次失败任务"]) {
      expect(screen.getByRole("tab", { name: label })).not.toBeNull();
    }
    expect(screen.getByRole("tab", { name: "运行中", selected: true })).not.toBeNull();

    const boundary = screen.getByRole("region", { name: "后台任务接入边界" });
    expect(within(boundary).getByText("未接入")).not.toBeNull();
    expect(within(boundary).getByText("CLI-only")).not.toBeNull();
    expect(within(boundary).getByText(/Watermark/)).not.toBeNull();
    expect(within(boundary).getByText(/负责人/)).not.toBeNull();
    expect(within(boundary).getAllByText(/重试/).length).toBeGreaterThan(0);

    // 没有查询端点，也没有把重试、暂停、取消等写操作伪装成按钮。
    expect(fetchMock).not.toHaveBeenCalled();
    for (const label of ["重试", "重新入队", "暂停任务", "取消任务", "执行任务"]) {
      expect(screen.queryByRole("button", { name: label })).toBeNull();
    }
  });

  it("按 ?sub 深链选择对应页签，并对未知值保持 Not Found 语义", () => {
    renderPage("/jobs?sub=failures");
    expect(screen.getByRole("tab", { name: "失败与重试", selected: true })).not.toBeNull();
    expect(screen.getByRole("heading", { name: "失败与重试", level: 3 })).not.toBeNull();

    renderPage("/jobs?sub=not-a-job-tab");
    expect(screen.getByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
    expect(screen.getByText(/没有名为 not-a-job-tab 的子页签/)).not.toBeNull();
  });
});
