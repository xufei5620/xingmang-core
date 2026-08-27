import { describe, expect, it } from "vitest";
import type { AlertItem } from "../api/alerts";
import type { MetricItem, ServiceItem } from "../api/platform";
import {
  focusRows,
  platformMatrixRows,
  recentlyRecoveredCount,
  urgentCount,
  workItemsFromAlerts,
  RECOVERED_WINDOW_HOURS,
  WORK_CATEGORIES,
} from "./workbench";

const NOW = new Date("2026-08-28T12:00:00Z");

// 指标键抽成常量而不是就地写字面量：`xxx_key: "……"` 这个形状会被 gitleaks 的
// generic-api-key 规则当成泄露的密钥（同一条误报见 router.test.tsx 与
// api/platform.test.ts）。本仓禁止加 gitleaks allowlist（会顺手掩盖真报，
// 见 scripts/check-governance.sh），所以换个写法比放宽扫描器划算。
const REVENUE_METRIC = "sub2api.revenue.daily";
const BALANCE_METRIC = "sub2api.users.balance";
const NEWAPI_CHANNELS_METRIC = "newapi.channels.status";

function alert(over: Partial<AlertItem> = {}): AlertItem {
  return {
    id: over.id ?? "a-1",
    rule_key: "metric.sync.failed",
    dedup_key: "d-1",
    severity: "warning",
    status: "OPEN",
    title: "示例告警",
    detail: "",
    environment: "development",
    source_metric_key: REVENUE_METRIC,
    opened_at: "2026-08-28T11:00:00Z",
    last_seen_at: "2026-08-28T11:30:00Z",
    acknowledged_at: null,
    resolved_at: null,
    fire_count: 3,
    notify_status: "pending",
    notify_error: "",
    notified_at: null,
    ...over,
  };
}

function metric(key: string, state: string, observedAt: string | null): MetricItem {
  return {
    metric_key: key,
    source: "s",
    environment: "development",
    watermark: "",
    value: {},
    freshness: {
      state,
      staleness_seconds: null,
      threshold_seconds: 1800,
      is_partial: false,
      observed_at: observedAt,
      last_success: observedAt,
      last_error_code: "",
    },
  };
}

function service(serviceType: string, status: string): ServiceItem {
  return {
    id: `id-${serviceType}`,
    service_type: serviceType,
    instance_id: `${serviceType}-1`,
    environment: "development",
    endpoint: "https://example.com",
    owner: "平台组",
    status,
    source_watermark: "",
    observed_at: null,
    stale_seconds: null,
  };
}

describe("顶部四格的计数口径", () => {
  it("「紧急」只数未解决的严重告警", () => {
    // 把 warning 也算进「紧急」的话这个数永远不会小，
    // 于是它不再能回答「现在要不要放下手里的事」
    const alerts = [
      alert({ id: "1", severity: "critical" }),
      alert({ id: "2", severity: "warning" }),
      alert({ id: "3", severity: "critical", status: "RESOLVED" }),
    ];
    expect(urgentCount(alerts)).toBe(1);
  });

  it("已确认与已静默的严重告警仍然算紧急", () => {
    // 静默不是解决：静默期间「紧急」掉到 0，正是最容易出事的时候
    const alerts = [
      alert({ id: "1", severity: "critical", status: "ACKNOWLEDGED" }),
      alert({ id: "2", severity: "critical", status: "SILENCED" }),
    ];
    expect(urgentCount(alerts)).toBe(2);
  });

  it("「最近恢复」只数窗口内已解决的", () => {
    const inside = alert({ id: "1", status: "RESOLVED", resolved_at: "2026-08-28T11:00:00Z" });
    const outside = alert({ id: "2", status: "RESOLVED", resolved_at: "2026-08-26T11:00:00Z" });
    const stillOpen = alert({ id: "3", status: "OPEN" });
    expect(recentlyRecoveredCount([inside, outside, stillOpen], NOW)).toBe(1);
    expect(RECOVERED_WINDOW_HOURS).toBe(24);
  });

  it("RESOLVED 但没有 resolved_at 的不算——不拿缺失字段当「刚刚恢复」", () => {
    const noTimestamp = alert({ id: "1", status: "RESOLVED", resolved_at: null });
    expect(recentlyRecoveredCount([noTimestamp], NOW)).toBe(0);
  });
});

describe("我的待处理", () => {
  it("分类逐字按原型的筛选条", () => {
    expect(WORK_CATEGORIES.map((c) => c.label)).toEqual([
      "故障",
      "待审批",
      "失败任务",
      "财务异常",
      "即将到期",
      "待评审变更",
    ]);
  });

  it("只有「故障」一类有数据源，其余五类都写明被什么挡着", () => {
    // 摆一个永远空的分类而不说为什么，人会以为「这一类现在没有问题」
    const withSource = WORK_CATEGORIES.filter((c) => c.source);
    expect(withSource.map((c) => c.id)).toEqual(["incidents"]);
    for (const category of WORK_CATEGORIES) {
      expect(Boolean(category.source) !== Boolean(category.blockedBy)).toBe(true);
      if (category.blockedBy) expect(category.blockedBy.length).toBeGreaterThan(0);
    }
  });

  it("已解决的告警不进待办", () => {
    const items = workItemsFromAlerts(
      [alert({ id: "1" }), alert({ id: "2", status: "RESOLVED" })],
      NOW,
    );
    expect(items.map((i) => i.id)).toEqual(["1"]);
  });

  it("严重的排在前面，与告警页同一条排序规则", () => {
    const items = workItemsFromAlerts(
      [alert({ id: "warn", severity: "warning" }), alert({ id: "crit", severity: "critical" })],
      NOW,
    );
    expect(items.map((i) => i.id)).toEqual(["crit", "warn"]);
  });

  it("每条待办都给出持续时长与触发次数", () => {
    // 「抖了一下」和「响了两小时」在一个标题上分不出来
    const [item] = workItemsFromAlerts([alert({ fire_count: 7 })], NOW);
    expect(item?.meta).toContain("已持续 1 小时");
    expect(item?.due).toBe("触发 7 次");
    expect(item?.to).toBe("/alerts");
  });
});

describe("运营焦点：三个领域分开，绝不合成总健康分", () => {
  it("永远是三行，不多一行「总分」", () => {
    // 交接文档 §9.1 明令禁止合成分数：一个 87 分的看板没法回答
    // 「我现在该去修哪个」，而任何一条恶化都会被另外两条稀释掉
    const rows = focusRows([]);
    expect(rows.map((r) => r.domain)).toEqual(["可靠性", "财务", "安全"]);
  });

  it("可靠性跟着告警走：有严重的说「严重」，只有警告说「需关注」，都没有说「正常」", () => {
    expect(focusRows([alert({ severity: "critical" })])[0]?.state).toBe("严重");
    expect(focusRows([alert({ severity: "warning" })])[0]?.state).toBe("需关注");
    expect(focusRows([])[0]?.state).toBe("正常");
  });

  it("财务与安全没有数据源，计数是 undefined 而不是 0", () => {
    // 0 会被读成「这一类没有问题」，而事实是我们还没接这条线
    const rows = focusRows([alert({ severity: "critical" })]);
    expect(rows[1]?.count).toBeUndefined();
    expect(rows[2]?.count).toBeUndefined();
    expect(rows[1]?.state).toBe("未接入");
    expect(rows[2]?.state).toBe("未接入");
  });
});

describe("平台状态矩阵", () => {
  const base = { services: [], metrics: [], alerts: [] };

  it("四个平台加一行开票系统（原型矩阵逐行）", () => {
    expect(platformMatrixRows(base).map((r) => r.label)).toEqual([
      "Sub2API",
      "NewAPI",
      "CPA",
      "服务器",
      "开票系统",
    ]);
  });

  it("开票系统指向治理段的开票集成，而不是一个平台页", () => {
    // 开票不是平台（ADMIN-IA §5.2）。矩阵里有这一行不等于把它变回平台，
    // 但它的入口必须落在归位之后的地方
    const invoice = platformMatrixRows(base).find((r) => r.key === "invoice");
    expect(invoice?.to).toBe("/finance?sub=invoicing");
    expect(invoice?.statusLabel).toBe("未接入");
  });

  it("已登记的平台用 Registry 状态，没登记的说「未登记 / 未接入·Mx」", () => {
    const rows = platformMatrixRows({ ...base, services: [service("sub2api", "degraded")] });
    expect(rows.find((r) => r.key === "sub2api")?.statusLabel).toBe("降级");
    expect(rows.find((r) => r.key === "cpa")?.statusLabel).toBe("未接入·M4");
    // 没有实例可谈的时候，「正常」是编出来的
    expect(rows.find((r) => r.key === "newapi")?.statusLabel).toBe("未登记");
  });

  it("新鲜度取最差的那一条，不取最新", () => {
    // 五条指标里有一条两小时没更新时，这一格必须显示「延迟」——
    // 取最新的会把它盖掉，而那正是人来这一屏要找的东西
    const rows = platformMatrixRows({
      ...base,
      metrics: [
        metric("sub2api.a", "fresh", "2026-08-28T11:59:00Z"),
        metric("sub2api.b", "stale", "2026-08-28T09:00:00Z"),
      ],
    });
    expect(rows.find((r) => r.key === "sub2api")?.freshness.state).toBe("stale");
  });

  it("最近观测取最新的那一条", () => {
    const rows = platformMatrixRows({
      ...base,
      metrics: [
        metric("sub2api.a", "fresh", "2026-08-28T11:59:00Z"),
        metric("sub2api.b", "stale", "2026-08-28T09:00:00Z"),
      ],
    });
    expect(rows.find((r) => r.key === "sub2api")?.observedAt).toBe("2026-08-28T11:59:00Z");
  });

  it("没有指标的平台是「未初始化」，不是「新鲜」", () => {
    const rows = platformMatrixRows(base);
    expect(rows.find((r) => r.key === "server")?.freshness.state).toBe("uninitialized");
    expect(rows.find((r) => r.key === "server")?.observedAt).toBeNull();
  });

  it("活动事件按指标键前缀归到对应平台，已解决的不算", () => {
    const rows = platformMatrixRows({
      ...base,
      alerts: [
        alert({ id: "1", source_metric_key: REVENUE_METRIC }),
        alert({ id: "2", source_metric_key: NEWAPI_CHANNELS_METRIC }),
        alert({ id: "3", source_metric_key: BALANCE_METRIC, status: "RESOLVED" }),
      ],
    });
    expect(rows.find((r) => r.key === "sub2api")?.events).toBe(1);
    expect(rows.find((r) => r.key === "newapi")?.events).toBe(1);
    expect(rows.find((r) => r.key === "cpa")?.events).toBe(0);
  });
});
