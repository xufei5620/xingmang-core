import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { FreshnessContract } from "./freshness";
import { MetricCard } from "./MetricCard";

// 指标键抽成常量而不是就地写字面量：`metricKey="……"` 这个形状会被 gitleaks 的
// generic-api-key 规则当成泄露的密钥（同一条误报见 admin-web 的 OverviewPage.test.tsx）
const USERS_METRIC = "sub2api.users.total";

const fresh: FreshnessContract = {
  state: "fresh",
  staleness_seconds: 42,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: "2026-08-26T10:00:00Z",
  last_success: "2026-08-26T10:00:00Z",
  last_error_code: "",
};

describe("MetricCard", () => {
  it("数值旁边一定有新鲜度徽章与数据时间（规格 §9.1）", () => {
    render(
      <MetricCard
        label="Sub2API 用户数"
        metricKey={USERS_METRIC}
        value="1,204"
        freshness={fresh}
        source="connector"
      />,
    );
    expect(screen.getByText("1,204")).not.toBeNull();
    expect(screen.getByText("数据新鲜")).not.toBeNull();
    expect(screen.getByText(/数据时间 2026-08-26 10:00:00 UTC/)).not.toBeNull();
  });

  it("没有可信数值时弱化主数位，不让它看着像个正常读数", () => {
    const { rerender } = render(
      <MetricCard label="日收入" value="未初始化" unavailable freshness={fresh} />,
    );
    expect(screen.getByText("未初始化").className).toContain("text-fg-muted");

    rerender(<MetricCard label="日收入" value="¥12.00" freshness={fresh} />);
    expect(screen.getByText("¥12.00").className).toContain("text-fg");
  });

  it("来源缺失时显示占位而不是空白：空白读起来像「没有来源这回事」", () => {
    render(<MetricCard label="日收入" value="¥12.00" freshness={fresh} />);
    expect(screen.getByText(/来源 —/)).not.toBeNull();
  });

  it("水位有才显示", () => {
    const { rerender } = render(
      <MetricCard label="日收入" value="¥12.00" freshness={fresh} source="connector" />,
    );
    expect(screen.queryByText(/水位/)).toBeNull();

    rerender(
      <MetricCard
        label="日收入"
        value="¥12.00"
        freshness={fresh}
        source="connector"
        watermark="2026-08-26"
      />,
    );
    expect(screen.getByText(/水位 2026-08-26/)).not.toBeNull();
  });

  it("趋势与平台入口是插槽：不传就不占位，卡片自己不去拉数据", () => {
    const { rerender } = render(<MetricCard label="日收入" value="¥12.00" freshness={fresh} />);
    expect(screen.queryByText("趋势")).toBeNull();
    expect(screen.queryByRole("link")).toBeNull();

    rerender(
      <MetricCard
        label="日收入"
        value="¥12.00"
        freshness={fresh}
        trend={<span>趋势</span>}
        link={<a href="/platforms/sub2api">查看平台</a>}
      />,
    );
    expect(screen.getByText("趋势")).not.toBeNull();
    expect(screen.getByRole("link", { name: "查看平台" })).not.toBeNull();
  });
});
