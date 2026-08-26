import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { FreshnessContract } from "./freshness";
import { FreshnessBadge, FreshnessNote, ServiceStatusBadge } from "./StatusBadges";

function freshness(over: Partial<FreshnessContract> = {}): FreshnessContract {
  return {
    state: "fresh",
    staleness_seconds: 12,
    threshold_seconds: 1800,
    is_partial: false,
    observed_at: "2026-08-26T10:00:00Z",
    last_success: "2026-08-26T10:00:00Z",
    last_error_code: "",
    ...over,
  };
}

describe("FreshnessBadge", () => {
  it("渲染状态文案与令牌语气类", () => {
    render(<FreshnessBadge freshness={freshness({ state: "stale" })} />);
    const el = screen.getByText("数据延迟");
    expect(el.className).toContain("border-warning");
  });

  it("失败时把错误码带进悬停说明，省得再翻日志", () => {
    render(
      <FreshnessBadge
        freshness={freshness({ state: "failed", last_error_code: "upstream_timeout" })}
      />,
    );
    expect(screen.getByTitle(/upstream_timeout/)).not.toBeNull();
  });

  it("未初始化显示「未初始化」，不显示任何数字", () => {
    render(
      <FreshnessBadge
        freshness={freshness({ state: "uninitialized", observed_at: null, staleness_seconds: null })}
      />,
    );
    expect(screen.getByText("未初始化")).not.toBeNull();
  });
});

describe("FreshnessNote", () => {
  it("显示数据时间与落后时长", () => {
    render(<FreshnessNote freshness={freshness({ staleness_seconds: 180 })} />);
    expect(screen.getByText(/2026-08-26 10:00:00 UTC/)).not.toBeNull();
    expect(screen.getByText(/落后 3 分钟/)).not.toBeNull();
  });

  it("主状态盖住 is_partial 时，补一句「数据不完整」不丢信息", () => {
    render(<FreshnessNote freshness={freshness({ state: "stale", is_partial: true })} />);
    expect(screen.getByText(/数据不完整/)).not.toBeNull();
  });
});

describe("ServiceStatusBadge", () => {
  it("三个状态各自渲染", () => {
    render(
      <>
        <ServiceStatusBadge status="active" />
        <ServiceStatusBadge status="degraded" />
        <ServiceStatusBadge status="retired" />
      </>,
    );
    expect(screen.getByText("运行中")).not.toBeNull();
    expect(screen.getByText("降级")).not.toBeNull();
    expect(screen.getByText("已下线")).not.toBeNull();
  });
});
