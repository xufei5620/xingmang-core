import { useQuery } from "@tanstack/react-query";
import { DataTableV2, type DataTableColumn } from "@xingmang/ui-admin";
import { Button, FormField, Input } from "@xingmang/ui-primitives";
import { useId, useMemo, useState } from "react";
import { listSMSCosts, type SMSCostRow } from "../api/sms";
import { ActionErrorNote } from "./ActionErrorNote";
import { ApiStateView } from "./ApiStateView";

/** 把聚合行写成 CSV。
 *
 *  纯函数，单独拿出来测：导出的那份数字是要拿去对账的，格式错了比不导更糟。
 *  字段里出现逗号、引号或换行时按 RFC 4180 用双引号包起来并把引号翻倍——
 *  服务代号是上游给的，我们保证不了它不含逗号。 */
export function costRowsToCsv(rows: readonly SMSCostRow[]): string {
  const header = ["day", "provider", "currency", "service", "amount", "count", "unknown_count"];
  const lines = [header.join(",")];
  for (const row of rows) {
    lines.push(
      [
        row.day,
        row.provider,
        row.currency ?? "",
        row.service ?? "",
        row.amount,
        String(row.count),
        String(row.unknown_count),
      ]
        .map(csvField)
        .join(","),
    );
  }
  // 末尾留一个换行：很多工具把没有结尾换行的最后一行当成半截数据。
  return lines.join("\n") + "\n";
}

function csvField(value: string): string {
  if (/[",\n\r]/.test(value)) {
    return `"${value.replaceAll('"', '""')}"`;
  }
  return value;
}

/** 按币种的合计。**不折算**：把两种币种加到一起得到的数字看起来像总成本，
 *  其实什么都不是。金额未知的笔数单独报——一份「这个月花了 X」的报表，背后
 *  如果有二十笔金额不明，那个 X 是下限不是花费。 */
export function totalsByCurrency(rows: readonly SMSCostRow[]): {
  currency: string;
  amount: string;
  count: number;
  unknownCount: number;
}[] {
  const acc = new Map<string, { amount: number; count: number; unknownCount: number }>();
  for (const row of rows) {
    const key = row.currency ?? "";
    const prev = acc.get(key) ?? { amount: 0, count: 0, unknownCount: 0 };
    // 合计只用于**页面展示**：精确到六位小数的十进制和在服务端算，这里做的是
    // 「一眼看个大概」。导出的 CSV 用的是服务端的原值，不是这个数。
    prev.amount += Number(row.amount) || 0;
    prev.count += row.count;
    prev.unknownCount += row.unknown_count;
    acc.set(key, prev);
  }
  return [...acc.entries()]
    .map(([currency, v]) => ({
      currency,
      amount: v.amount.toFixed(6).replace(/0+$/, "").replace(/\.$/, ""),
      count: v.count,
      unknownCount: v.unknownCount,
    }))
    .sort((a, b) => a.currency.localeCompare(b.currency));
}

function isoDay(offsetDays: number): string {
  const d = new Date(Date.now() + offsetDays * 86_400_000);
  return d.toISOString().slice(0, 10);
}

/** 成本统计（XM-SMS3 #3）。
 *
 *  聚合在服务端做（按供应商 × 币种 × 服务 × 天），页面只画表与合计。这张事实
 *  表的形状就是跨平台财务的输入。 */
export function SMSCostPanel() {
  const [from, setFrom] = useState(() => isoDay(-30));
  const [to, setTo] = useState(() => isoDay(0));
  const formId = useId();

  const query = useQuery({
    queryKey: ["sms-costs", from, to],
    queryFn: ({ signal }) => listSMSCosts(from, to, { signal }),
    staleTime: 60_000,
  });
  const rows = query.data?.items ?? [];
  const totals = useMemo(() => totalsByCurrency(rows), [rows]);
  const unknownTotal = totals.reduce((sum, t) => sum + t.unknownCount, 0);

  const columns: DataTableColumn<SMSCostRow>[] = [
    { id: "day", header: "日期", primary: true, cell: (r) => r.day, value: (r) => r.day },
    { id: "provider", header: "供应商", cell: (r) => r.provider, value: (r) => r.provider },
    { id: "currency", header: "币种", cell: (r) => r.currency || "未知", value: (r) => r.currency ?? "" },
    { id: "service", header: "服务", cell: (r) => r.service || "—", value: (r) => r.service ?? "" },
    { id: "amount", header: "金额", numeric: true, cell: (r) => r.amount, value: (r) => Number(r.amount) || 0 },
    { id: "count", header: "笔数", numeric: true, cell: (r) => String(r.count), value: (r) => r.count },
    {
      id: "unknown",
      header: "金额未知",
      numeric: true,
      cell: (r) => (r.unknown_count > 0 ? String(r.unknown_count) : "—"),
      value: (r) => r.unknown_count,
    },
  ];

  function download() {
    const blob = new Blob([costRowsToCsv(rows)], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `sms-costs-${from}_${to}.csv`;
    link.click();
    URL.revokeObjectURL(url);
  }

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <section className="border-edge flex min-w-0 flex-col gap-3 rounded-md border p-3">
        <div>
          <h3 className="text-sm font-semibold">成本统计</h3>
          <p className="text-fg-muted text-xs">
            按供应商 × 币种 × 服务 × 天聚合，服务端整表算。<strong>分币种不折算</strong>
            ——两种币种加到一起得到的数字看起来像总成本，其实什么都不是。
          </p>
        </div>
        <div className="grid gap-3 md:grid-cols-3">
          <FormField label="起（含）" htmlFor={`${formId}-from`} hint="YYYY-MM-DD，UTC。">
            <Input id={`${formId}-from`} aria-label="统计起始日" value={from} onChange={(e) => setFrom(e.target.value)} />
          </FormField>
          <FormField label="止（含）" htmlFor={`${formId}-to`} hint="一次最多 366 天。">
            <Input id={`${formId}-to`} aria-label="统计结束日" value={to} onChange={(e) => setTo(e.target.value)} />
          </FormField>
          <span className="flex items-end">
            <Button variant="secondary" size="sm" disabled={rows.length === 0} onClick={download}>
              导出 CSV
            </Button>
          </span>
        </div>
        {query.isError ? <ActionErrorNote error={query.error} /> : null}
        <div className="flex flex-wrap gap-3">
          {totals.map((t) => (
            <span key={t.currency} className="border-edge rounded-md border px-3 py-2 text-sm">
              {`${t.currency || "未知币种"}：${t.amount} · ${t.count} 笔`}
              {t.unknownCount > 0 ? `（其中 ${t.unknownCount} 笔金额未知）` : ""}
            </span>
          ))}
        </div>
        {unknownTotal > 0 ? (
          <p className="text-fg-muted text-xs">
            {`有 ${unknownTotal} 笔金额未知（62 买号只回订单号、Hero 延长不回价格），`}
            <strong>合计是下限而不是实际花费</strong>。
          </p>
        ) : null}
      </section>

      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <DataTableV2
          caption="接码成本（按天聚合）"
          columns={columns}
          rows={rows}
          rowKey={(r) => `${r.day}|${r.provider}|${r.currency ?? ""}|${r.service ?? ""}`}
          emptyState={<p className="text-fg-muted text-sm">这段时间没有成本事件。</p>}
        />
      </ApiStateView>
    </div>
  );
}
