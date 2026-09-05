import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { costRowsToCsv, SMSCostPanel, totalsByCurrency } from "./SMSCostPanel";

vi.mock("../api/sms", async () => {
  const actual = await vi.importActual<typeof import("../api/sms")>("../api/sms");
  return { ...actual, listSMSCosts: vi.fn() };
});

import { listSMSCosts, type SMSCostRow } from "../api/sms";

const ROWS: SMSCostRow[] = [
  { day: "2026-09-06", provider: "hero_sms", currency: "", service: "go", amount: "0.700000", count: 2, unknown_count: 0 },
  { day: "2026-09-06", provider: "hero_sms", currency: "840", service: "gmail", amount: "1.000000", count: 1, unknown_count: 0 },
  { day: "2026-09-05", provider: "sms62", currency: "USD", service: "google", amount: "0.000000", count: 1, unknown_count: 1 },
];

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <SMSCostPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => vi.clearAllMocks());

// **分币种不折算**：三种币种就是三个合计，不能加成一个数。
it("合计按币种分开，不折算", async () => {
  vi.mocked(listSMSCosts).mockResolvedValue({ items: ROWS, from: "2026-09-05", to: "2026-09-06" });
  renderPanel();

  expect(await screen.findByText(/未知币种：0\.7/)).toBeTruthy();
  expect(screen.getByText(/840：1/)).toBeTruthy();
  expect(screen.getByText(/USD：0/)).toBeTruthy();
});

// 有金额未知的笔数时，必须说明合计是**下限**——否则那个数字会被当成实际花费。
it("有未知金额时说明合计是下限", async () => {
  vi.mocked(listSMSCosts).mockResolvedValue({ items: ROWS, from: "2026-09-05", to: "2026-09-06" });
  renderPanel();

  // 合计条上也有「其中 N 笔金额未知」，所以按那句独有的话找这段提醒。
  const note = await screen.findByText(/合计是下限/);
  expect(note.closest("p")?.textContent).toContain("1 笔金额未知");
});

it("没有未知金额时不显示那句提醒", async () => {
  vi.mocked(listSMSCosts).mockResolvedValue({
    items: [ROWS[0]!],
    from: "2026-09-06",
    to: "2026-09-06",
  });
  renderPanel();

  await screen.findByText(/未知币种：0\.7/);
  expect(screen.queryByText(/下限/)).toBeNull();
});

it("没有数据时导出按钮禁用", async () => {
  vi.mocked(listSMSCosts).mockResolvedValue({ items: [], from: "2026-09-05", to: "2026-09-06" });
  renderPanel();

  await waitFor(() =>
    expect((screen.getByRole("button", { name: "导出 CSV" }) as HTMLButtonElement).disabled).toBe(true),
  );
});

// 导出的那份数字是要拿去对账的，格式错了比不导更糟。
it("CSV 有表头、按行写、结尾有换行", () => {
  const csv = costRowsToCsv(ROWS);
  const lines = csv.split("\n");
  expect(lines[0]).toBe("day,provider,currency,service,amount,count,unknown_count");
  expect(lines[1]).toBe("2026-09-06,hero_sms,,go,0.700000,2,0");
  expect(lines[3]).toBe("2026-09-05,sms62,USD,google,0.000000,1,1");
  // 末尾留一个换行：很多工具把没有结尾换行的最后一行当成半截数据。
  expect(csv.endsWith("\n")).toBe(true);
});

// 服务代号是上游给的，我们保证不了它不含逗号或引号。
it("CSV 转义逗号与引号", () => {
  const csv = costRowsToCsv([
    { day: "2026-09-06", provider: "hero_sms", currency: "", service: 'go,"x"', amount: "1", count: 1, unknown_count: 0 },
  ]);
  expect(csv.split("\n")[1]).toBe('2026-09-06,hero_sms,,"go,""x""",1,1,0');
});

it("空币种单独成一组", () => {
  const totals = totalsByCurrency(ROWS);
  expect(totals.map((t) => t.currency)).toEqual(["", "840", "USD"]);
  expect(totals[0]!.count).toBe(2);
});
