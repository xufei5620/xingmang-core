import { describe, expect, it } from "vitest";
import { describeFreshness } from "@xingmang/ui-admin";
import { FIRE_COUNT_HEADER, FIRE_COUNT_MEANING, type AlertItem } from "../api/alerts";
import type { ApprovalItem } from "../api/approvals";
import type { JobRunItem } from "../api/jobs";
import type { MetricItem, ServiceItem } from "../api/platform";
import {
  acknowledgedGroupHeading,
  acknowledgedWorkItems,
  approvalsDueSoonCount,
  focusRows,
  platformMatrixRows,
  recentlyRecoveredCount,
  urgentCount,
  truncationNote,
  workItemsFromAlerts,
  workItemsFromApprovals,
  workItemsFromJobRuns,
  FRESHNESS_PRIORITY,
  ACTIVE_ALERTS_LIMIT,
  APPROVAL_DUE_WINDOW_HOURS,
  WORK_APPROVALS_LIMIT,
  WORK_JOBS_LIMIT,
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
const NEWAPI_SUBSCRIPTION_METRIC = "newapi.subscription.daily";
const CHANNEL_BALANCE_METRIC = "sub2api.channels.balance";
const INVOICE_ORDERS_METRIC = "invoice.orders.daily";

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

function metric(
  key: string,
  state: string,
  observedAt: string | null,
  over: Partial<MetricItem["freshness"]> = {},
  watermark = "",
): MetricItem {
  return {
    metric_key: key,
    source: "s",
    environment: "development",
    watermark,
    value: {},
    freshness: {
      state,
      staleness_seconds: null,
      threshold_seconds: 1800,
      is_partial: false,
      observed_at: observedAt,
      last_success: observedAt,
      last_error_code: "",
      ...over,
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

  it("已静默的严重告警仍然算紧急", () => {
    // 静默不是解决：静默期间「紧急」掉到 0，正是最容易出事的时候
    const alerts = [alert({ id: "2", severity: "critical", status: "SILENCED" })];
    expect(urgentCount(alerts)).toBe(1);
  });

  // 这条以前写的是「已确认与已静默的严重告警仍然算紧急」。负责人按了确认之后
  // 看到「紧急」纹丝不动，得出的结论是「确认没生效」——一个永远降不下来的数
  // 回答不了「要不要放下手里的事」。已确认的有人在管；静默的没人管，仍算。
  it("已确认的严重告警不算紧急：有人认领了，不再催人放下手里的事", () => {
    const alerts = [
      alert({ id: "1", severity: "critical", status: "ACKNOWLEDGED", acknowledged_at: "2026-08-28T11:10:00Z" }),
      alert({ id: "2", severity: "critical", status: "OPEN" }),
    ];
    // 对照组在场：只有一条 ACKNOWLEDGED 时断言 0 太容易恒真（空数组也是 0）
    expect(urgentCount(alerts)).toBe(1);
    expect(urgentCount([alerts[0] as AlertItem])).toBe(0);
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

  // XM-UPSTREAM-DETAIL-COPY：这一格以前叫「今日到期」，数审批 + 重试 + 轮换到期
  // 三样，副行写着「随 Foundation-B 与后台任务页上线」。两样今天都在了，那句话
  // 是错的；而三样合成一个数同样错——轮换到期一个都算不出来，合计恒偏低。
  describe("「审批到期」只数一件有真正截止时刻的事", () => {
    function due(over: Partial<ApprovalItem> = {}): ApprovalItem {
      return {
        id: "ap-due", action_id: "registry.connector.create", action_version: "1",
        risk_level: "L2", params: {}, params_hash: "sha256:abc", requester_id: "staff_bob",
        requester_type: "HUMAN", reason: "上线新连接器", status: "PENDING",
        created_at: "2026-08-28T08:00:00Z", expires_at: "2026-08-28T15:00:00Z",
        decisions: [], votes_required: 2, votes_cast: 0,
        privileged_vote_required: false, privileged_vote_cast: false, ...over,
      };
    }

    it("窗口是滚动 24 小时，边界之内算、之外不算", () => {
      // NOW = 2026-08-28T12:00:00Z
      expect(APPROVAL_DUE_WINDOW_HOURS).toBe(24);
      const inside = due({ id: "a", expires_at: "2026-08-29T11:59:00Z" });
      const onEdge = due({ id: "b", expires_at: "2026-08-29T12:00:00Z" });
      const outside = due({ id: "c", expires_at: "2026-08-29T12:01:00Z" });
      expect(approvalsDueSoonCount([inside, onEdge, outside], NOW)).toBe(2);
    });

    it("已过期的不算，哪怕库里仍记着 PENDING——那张单谁也批不动了", () => {
      // ExpirePending 定时任务会滞后，服务端在执行那一刻才按 expires_at 判；
      // 照抄 status 会让这一格把一批动不了的事算成「快到期了，去看一眼」
      const expired = due({ id: "a", expires_at: "2026-08-28T11:00:00Z" });
      expect(approvalsDueSoonCount([expired], NOW)).toBe(0);
    });

    it("非 PENDING 的不算：已批准/已执行的单不是待办", () => {
      const approved = due({ id: "a", status: "APPROVED" });
      const executed = due({ id: "b", status: "EXECUTED" });
      expect(approvalsDueSoonCount([approved, executed], NOW)).toBe(0);
    });

    it("expires_at 解析不出来的不算——不替它断言「快到期了」", () => {
      const broken = due({ id: "a", expires_at: "不是一个时间" });
      expect(approvalsDueSoonCount([broken], NOW)).toBe(0);
    });
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

  it("「故障」「待审批」与「失败任务」有数据源，其余三类都写明被什么挡着", () => {
    // 摆一个永远空的分类而不说为什么，人会以为「这一类现在没有问题」
    const withSource = WORK_CATEGORIES.filter((c) => c.source);
    expect(withSource.map((c) => c.id)).toEqual(["incidents", "approvals", "jobs"]);
    for (const category of WORK_CATEGORIES) {
      expect(Boolean(category.source) !== Boolean(category.blockedBy)).toBe(true);
      if (category.blockedBy) expect(category.blockedBy.length).toBeGreaterThan(0);
    }
  });

  // XM-WORKBENCH-APPROVALS：接上数据源之后 blockedBy 必须撤掉。这不只是文案
  // 问题——OverviewPage 是先看 blockedBy 再决定要不要渲染列表的，留着它，真
  // 数据一行也显示不出来，而界面还理直气壮地说「还没有数据源」。
  it("「待审批」接上数据源之后不再挂 blockedBy", () => {
    const approvals = WORK_CATEGORIES.find((c) => c.id === "approvals");
    expect(approvals?.source).toBeTruthy();
    expect(approvals?.blockedBy).toBeUndefined();
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

  // 生产事实（2026-09-09）：upstream.version.changed 那条 ACKNOWLEDGED 于 15:45Z，
  // 此后 fire_count 每分钟 +1、last_seen_at 每轮更新，而工作台把它与未处理的
  // 混排、标「警告」、只写「已持续 13 小时」。负责人原话：「这个没有带时间，
  // 我感觉好像我确认之后还在告警」。下面这一组把三件事分开钉住：不混排、不催、
  // 给绝对时刻。
  describe("已确认的告警：不混排、不催、给绝对时刻", () => {
    const acked = alert({
      id: "acked",
      severity: "warning",
      status: "ACKNOWLEDGED",
      title: "上游版本变化",
      opened_at: "2026-08-27T13:31:00Z",
      acknowledged_at: "2026-08-27T15:45:00Z",
      last_seen_at: "2026-08-28T11:59:00Z",
      fire_count: 821,
    });

    it("已确认的不进未处理列表，而进已确认组——两边都用整个 id 数组相等，缺席不恒真", () => {
      const input = [alert({ id: "open" }), acked, alert({ id: "gone", status: "RESOLVED" })];
      // 「acked 不在这里」靠的是整个数组相等：把过滤删掉它就会多出来
      expect(workItemsFromAlerts(input, NOW).map((i) => i.id)).toEqual(["open"]);
      expect(acknowledgedWorkItems(input, NOW).map((i) => i.id)).toEqual(["acked"]);
    });

    it("已解决的不进已确认组，哪怕它带着当年的确认时刻", () => {
      const resolved = alert({
        id: "resolved",
        status: "RESOLVED",
        acknowledged_at: "2026-08-27T15:45:00Z",
        resolved_at: "2026-08-28T00:00:00Z",
      });
      expect(acknowledgedWorkItems([resolved, acked], NOW).map((i) => i.id)).toEqual(["acked"]);
    });

    it("已确认组的行不再挂「警告 / 严重」，徽章是状态口径的「已确认」", () => {
      const [item] = acknowledgedWorkItems([acked], NOW);
      expect(item?.categoryLabel).toBe("已确认");
      expect(item?.tone).toBe("info");
      // 严重度没有被丢掉，只是退到 meta 里不再催
      expect(item?.meta).toContain("警告");
    });

    it("已确认组的行写「已确认 <时刻>」与「仍在成立：最近评估 <时刻>」，一律带 UTC 后缀", () => {
      const [item] = acknowledgedWorkItems([acked], NOW);
      expect(item?.timeline).toBe(
        "打开 2026-08-27 13:31:00 UTC · 已确认 2026-08-27 15:45:00 UTC · 仍在成立：最近评估 2026-08-28 11:59:00 UTC",
      );
    });

    it("确认时刻缺失时明说缺失，不拿「—」或别的时刻冒充", () => {
      const [item] = acknowledgedWorkItems([alert({ ...acked, acknowledged_at: null })], NOW);
      expect(item?.timeline).toContain("已确认（后端没有记录确认时刻）");
      expect(item?.timeline).not.toContain("已确认 —");
    });

    it("未处理的行也给打开与最近评估两个绝对时刻——「已持续 13 小时」核对不了任何事", () => {
      const [item] = workItemsFromAlerts(
        [alert({ opened_at: "2026-08-28T11:00:00Z", last_seen_at: "2026-08-28T11:30:00Z" })],
        NOW,
      );
      expect(item?.timeline).toBe("打开 2026-08-28 11:00:00 UTC · 最近评估 2026-08-28 11:30:00 UTC");
      // 未处理的行没有「已确认」这一段——没人确认过就不能写出来
      expect(item?.timeline).not.toContain("已确认");
    });

    it("组头写条数", () => {
      expect(acknowledgedGroupHeading(3)).toBe("已确认，等待自愈（3 条）");
    });
  });

  it("每条待办都给出持续时长与评估轮数", () => {
    // 「抖了一下」和「响了两小时」在一个标题上分不出来。
    //
    // 这条断言原来写的是「触发 7 次」——那是错的：fire_count 数的是每 60 秒
    // 重评一轮、条件仍成立就 +1 的轮数（TouchAlert 的 fire_count + 1 与
    // DefaultAlertEvaluateInterval），生产上那条 669 正好是 668 分钟 + 1。
    const [item] = workItemsFromAlerts([alert({ fire_count: 7 })], NOW);
    expect(item?.meta).toContain("已持续 1 小时");
    expect(item?.due).toBe("评估 7 轮");
    expect(item?.to).toBe("/alerts");
  });

  // XM-WORKBENCH-TRUTH：两个后端字段今天都不存在，**三态都要测**。只测缺席
  // 那一支，字段到位那天才会发现在场分支从来没被走过（预留接口 ≠ 现在就接）。
  describe("评估轮数与「已持续」：字段缺席 / 在场 / 在场但为 null", () => {
    it("缺席：只说评估轮数，「已持续」退回 opened_at 并挂上会重新计时的说明", () => {
      const [item] = workItemsFromAlerts([alert({ fire_count: 669 })], NOW);
      expect(item?.due).toBe("评估 669 轮");
      // opened_at 11:00 → NOW 12:00
      expect(item?.meta).toContain("已持续 1 小时");
      expect(item?.hint).toContain("恢复后重新触发会新开一条");
    });

    it("在场：两个数一起显示，「已持续」改用 first_opened_at", () => {
      // first_opened_at 04:00 与工厂默认的 opened_at 11:00 刻意差 7 小时：
      // 不这么设，「优先用 first_opened_at」这条根本没法证伪。
      const withFields = {
        ...alert({ fire_count: 669 }),
        trigger_count: 3,
        first_opened_at: "2026-08-28T04:00:00Z",
      } as AlertItem;
      const [item] = workItemsFromAlerts([withFields], NOW);
      expect(item?.due).toBe("触发 3 次 · 评估 669 轮");
      expect(item?.meta).toContain("已持续 8 小时");
      // 用了真正的首次打开时刻，就没有「计时归零」这回事可说了
      expect(item?.hint).not.toContain("恢复后重新触发会新开一条");
    });

    it("在场但为 null：干净退回，不显示「触发 null 次」", () => {
      const nulled = {
        ...alert({ fire_count: 669 }),
        trigger_count: null,
        first_opened_at: null,
      } as AlertItem;
      const [item] = workItemsFromAlerts([nulled], NOW);
      expect(item?.due).toBe("评估 669 轮");
      expect(item?.meta).toContain("已持续 1 小时");
    });

    // 共用一份的证据（同「安全」行与「即将到期」分类那条的写法）：这句话曾有
    // 四份措辞不同的副本，于是同一个数在三个页面上有三种叫法。
    it("待办行的说明与告警页那一列的悬停说明是同一个字符串", () => {
      const [item] = workItemsFromAlerts([alert()], NOW);
      expect(item?.hint?.startsWith(FIRE_COUNT_MEANING)).toBe(true);
      // 防止上一条在两处都是空串时恒真
      expect(FIRE_COUNT_MEANING).toContain("60");
      expect(FIRE_COUNT_MEANING).toContain("轮");
      expect(FIRE_COUNT_HEADER).toBe("评估轮次");
    });
  });
});

// XM-WORKBENCH-JOBS：「失败任务」原来写着"后台任务页与 River 查询端点尚未建"，
// 而那两样在 XM-JOBS0 就交付了。占位让失败的后台任务在首屏彻底不可见。
describe("失败任务：只收已放弃的后台任务", () => {
  function run(overrides: Partial<JobRunItem> = {}): JobRunItem {
    return {
      id: 41,
      kind: "sub2api_sync",
      queue: "default",
      state: "discarded",
      attempt: 3,
      max_attempts: 3,
      created_at: "2026-08-28T08:00:00Z",
      scheduled_at: "2026-08-28T08:00:00Z",
      attempted_at: "2026-08-28T09:00:00Z",
      finalized_at: "2026-08-28T11:00:00Z",
      duration_ms: 120,
      error_count: 3,
      last_error: null,
      args: {},
      ...overrides,
    };
  }

  it("重试中的任务不进待办：它不需要人动手，列进来会让清单抖动", () => {
    const items = workItemsFromJobRuns(
      [run({ id: 1 }), run({ id: 2, state: "retryable" }), run({ id: 3, state: "cancelled" })],
      NOW,
    );
    expect(items.map((i) => i.id)).toEqual(["job-1"]);
  });

  it("完成与运行中的记录同样不进待办", () => {
    const items = workItemsFromJobRuns(
      [run({ id: 4, state: "completed" }), run({ id: 5, state: "running" })],
      NOW,
    );
    expect(items).toEqual([]);
  });

  it("标题说清是重试用尽后放弃的，并直达后台任务页的对应页签", () => {
    const item = workItemsFromJobRuns([run()], NOW)[0];
    expect(item?.title).toBe("Sub2API 同步 重试 3 次后放弃");
    expect(item?.categoryLabel).toBe("已放弃");
    expect(item?.tone).toBe("danger");
    expect(item?.due).toBe("需人工处理");
    // 落到 state=discarded 那个页签，而不是后台任务页首屏
    expect(item?.to).toBe("/jobs?sub=repeated");
    expect(item?.categoryId).toBe("jobs");
  });

  it("时刻优先用 finalized_at，缺了退回最近一次尝试，再退回创建时刻", () => {
    // NOW = 2026-08-28T12:00:00Z；夹具里终结/尝试/创建分别是 11:00 / 09:00 /
    // 08:00，所以三级回退各自给出不同的数字，谁都替代不了谁。
    expect(workItemsFromJobRuns([run()], NOW)[0]?.meta).toContain("1 小时前");
    expect(workItemsFromJobRuns([run({ finalized_at: null })], NOW)[0]?.meta).toContain(
      "3 小时前",
    );
    expect(
      workItemsFromJobRuns([run({ finalized_at: null, attempted_at: null })], NOW)[0]?.meta,
    ).toContain("4 小时前");
  });

  it("未知的任务类型显示原始 kind，不隐藏也不报错", () => {
    const item = workItemsFromJobRuns([run({ kind: "brand_new_job" })], NOW)[0];
    expect(item?.title).toContain("brand_new_job");
  });

  // XM-WORKBENCH-TRUTH：生产上 288 条 card_sync 把这一格全占满，真正需要人
  // 处理的东西被挤出首屏（PLATFORM-ALERT-STORM-2026-09-08 附录点名本函数）。
  describe("同类型的已放弃作业合并成一行", () => {
    const many = Array.from({ length: 288 }, (_, i) =>
      run({ id: 1000 + i, kind: "card_sync", finalized_at: "2026-08-28T11:59:00Z" }),
    );
    // 最早的那一条另设一个时刻，好证明「最近 / 最早」两个数不是同一个
    const oldest = run({ id: 999, kind: "card_sync", finalized_at: "2026-08-28T08:00:00Z" });
    // **别的 kind 排在数组最前面**，这是刻意的：/jobs/runs 按放弃时刻倒序
    // 返回，只要有一条别的 kind 在那 288 条 card_sync 之后被放弃，取回的
    // 20 条里它就排第一。若夹具照着期望顺序摆，「条数多的排最前」这条规则
    // 会靠分组的插入顺序恰好成立——把排序整段删掉测试照样绿（条件恰好为真
    // ≠ 条件正确）。
    const mixed = [run({ id: 7, kind: "sub2api_sync" }), ...many, oldest];

    it("一个 kind 一行，不是一条一行", () => {
      const items = workItemsFromJobRuns(mixed, NOW);
      expect(items).toHaveLength(2);
      // 条数多的排最前：288 条那一类必须第一眼看得见
      expect(items[0]?.title).toBe("卡片数据同步 已放弃 ×289");
      expect(items[0]?.id).toBe("job-kind-card_sync");
    });

    it("合并行给出最近与最早两个时刻，光有计数说不清它是不是还在发生", () => {
      const merged = workItemsFromJobRuns(mixed, NOW)[0];
      expect(merged?.meta).toContain("最近 1 分钟前");
      expect(merged?.meta).toContain("最早 4 小时前");
      expect(merged?.due).toBe("需人工处理");
      expect(merged?.to).toBe("/jobs?sub=repeated");
    });

    it("不同 kind 不会被并到一起——按 kind 分组，不是按队列", () => {
      // 夹具里两种 kind 同属 default 队列：按队列合并会把它们并成一行
      const items = workItemsFromJobRuns(mixed, NOW);
      expect(items.map((i) => i.id)).toEqual(["job-kind-card_sync", "job-7"]);
    });

    // 「条数多的排最前」是本片要修的那个症状本身（大堆把真正要处理的东西挤
    // 出首屏）。它必须自己为自己负责：两个取数方向都钉，否则删掉排序也绿。
    it("条数多的排最前，与取数顺序无关", () => {
      const other = run({ id: 7, kind: "sub2api_sync" });
      expect(workItemsFromJobRuns([other, ...many], NOW).map((i) => i.id)).toEqual([
        "job-kind-card_sync",
        "job-7",
      ]);
      expect(workItemsFromJobRuns([...many, other], NOW).map((i) => i.id)).toEqual([
        "job-kind-card_sync",
        "job-7",
      ]);
    });

    it("条数相同时保持取数顺序，不自己再排一遍", () => {
      // 服务端已按放弃时刻倒序给了，这里不该把它打乱：同样两条，谁先被放弃
      // 谁在上面。这一条钉的是 sort 里那个 `|| a.index - b.index`。
      const cards = [
        run({ id: 1, kind: "card_sync" }),
        run({ id: 2, kind: "card_sync" }),
      ];
      const syncs = [
        run({ id: 3, kind: "sub2api_sync" }),
        run({ id: 4, kind: "sub2api_sync" }),
      ];
      expect(workItemsFromJobRuns([...syncs, ...cards], NOW).map((i) => i.id)).toEqual([
        "job-kind-sub2api_sync",
        "job-kind-card_sync",
      ]);
      expect(workItemsFromJobRuns([...cards, ...syncs], NOW).map((i) => i.id)).toEqual([
        "job-kind-card_sync",
        "job-kind-sub2api_sync",
      ]);
    });

    it("只有一条时逐字保持原样，不出现「×1」这种噪声", () => {
      const single = workItemsFromJobRuns([run({ id: 7 })], NOW)[0];
      expect(single?.title).toBe("Sub2API 同步 重试 3 次后放弃");
      expect(single?.children).toBeUndefined();
    });

    it("明细挂在合并行上，且第一条是最近的那一次", () => {
      // 「展开可看明细」如果没有明细可展开，那句话就是空话
      const merged = workItemsFromJobRuns(mixed, NOW)[0];
      expect(merged?.children).toHaveLength(289);
      expect(merged?.children?.[0]?.meta).toContain("1 分钟前");
      expect(merged?.children?.at(-1)?.id).toBe("job-999");
    });

    it("合并之后仍然只收已放弃：重试中的不会被算进计数", () => {
      const withRetryable = [...many, run({ id: 9, kind: "card_sync", state: "retryable" })];
      const merged = workItemsFromJobRuns(withRetryable, NOW)[0];
      expect(merged?.title).toBe("卡片数据同步 已放弃 ×288");
    });

    // 取数上限是 20 条，而生产上真实是 288。合并后若显示「×20」却不说它可能
    // 不全，界面会从「288 行刷屏」退化成「一个看起来权威的错数字」——更危险。
    it("取数被截断时计数后面带「+」，不冒充这就是全部", () => {
      const twenty = many.slice(0, 20);
      expect(workItemsFromJobRuns(twenty, NOW, { truncated: true })[0]?.title).toBe(
        "卡片数据同步 已放弃 ×20+",
      );
      expect(workItemsFromJobRuns(twenty, NOW)[0]?.title).toBe("卡片数据同步 已放弃 ×20");
    });
  });
});

// XM-WORKBENCH-APPROVALS：审批中心（XM-0030）已启用，这一类原来只有一句
// 「后端在、前端没接」的占位。占位比缺功能更糟：等着人投票的 L3/L4 动作在
// 首屏彻底不可见。
describe("待审批：只收此刻真的还等着人投票的单", () => {
  function approval(over: Partial<ApprovalItem> = {}): ApprovalItem {
    return {
      id: "ap-1",
      action_id: "registry.connection.set_status",
      action_version: "1",
      risk_level: "L3",
      params: {},
      params_hash: "sha256:abc",
      requester_id: "staff_bob",
      requester_type: "HUMAN",
      reason: "上游换域名，需要重新登记",
      status: "PENDING",
      created_at: "2026-08-28T08:00:00Z",
      expires_at: "2026-08-28T15:00:00Z",
      decisions: [],
      votes_required: 2,
      votes_cast: 0,
      privileged_vote_required: false,
      privileged_vote_cast: false,
      ...over,
    };
  }

  it("一条待办给出等级、提交人、还差几票与到期时间，并直达「待审批」子页签", () => {
    const item = workItemsFromApprovals([approval({ votes_cast: 1 })], NOW)[0];
    // 前缀不能省：告警的 id 直接就是 alert.id，审批单的 id 也是一串 UUID,
    // 三类合并成一张清单之后撞上一次，就是 React 静默丢掉一行
    expect(item?.id).toBe("approval-ap-1");
    expect(item?.categoryId).toBe("approvals");
    // 徽章上放风险等级：这一屏唯一要当场判断的是「先看哪一张」
    expect(item?.categoryLabel).toBe("L3");
    expect(item?.tone).toBe("warning");
    expect(item?.title).toBe("registry.connection.set_status@1");
    expect(item?.meta).toBe("staff_bob 提交 · 还差 1 票（已 1/2）");
    // NOW = 2026-08-28T12:00:00Z，夹具的到期时刻是当天 15:00
    expect(item?.due).toBe("3 小时后到期");
    expect(item?.to).toBe("/actions?sub=pending");
  });

  it("L4 排在 L3 前面，同一档里先到期的在前——与审批队列页同一条排序规则", () => {
    const items = workItemsFromApprovals(
      [
        approval({ id: "l3-late", risk_level: "L3", expires_at: "2026-08-28T20:00:00Z" }),
        approval({ id: "l4", risk_level: "L4" }),
        approval({ id: "l3-soon", risk_level: "L3", expires_at: "2026-08-28T13:00:00Z" }),
      ],
      NOW,
    );
    expect(items.map((i) => i.id)).toEqual([
      "approval-l4",
      "approval-l3-soon",
      "approval-l3-late",
    ]);
  });

  it("L4 是危险色；不认识的等级照原样显示成中性徽章，不静默丢掉", () => {
    expect(workItemsFromApprovals([approval({ risk_level: "L4" })], NOW)[0]?.tone).toBe("danger");
    const unknown = workItemsFromApprovals([approval({ risk_level: "L9" })], NOW)[0];
    expect(unknown?.categoryLabel).toBe("L9");
    expect(unknown?.tone).toBe("neutral");
  });

  // ExpirePending 是定时任务，库里的 status 会滞后，而服务端在执行那一刻才按
  // expires_at 判。照抄 status 会让首屏混进一批谁也批不动的单。
  it("库里仍记着 PENDING 但已过期的单不进待办：它已经批不动了", () => {
    const items = workItemsFromApprovals(
      [
        approval({ id: "alive", expires_at: "2026-08-28T15:00:00Z" }),
        approval({ id: "dead", expires_at: "2026-08-28T11:00:00Z" }),
      ],
      NOW,
    );
    expect(items.map((i) => i.id)).toEqual(["approval-alive"]);
  });

  it("非 PENDING 的单一律不进待办", () => {
    const items = workItemsFromApprovals(
      [
        approval({ id: "ok" }),
        approval({ id: "approved", status: "APPROVED" }),
        approval({ id: "rejected", status: "REJECTED" }),
        approval({ id: "executed", status: "EXECUTED" }),
        approval({ id: "cancelled", status: "CANCELLED" }),
      ],
      NOW,
    );
    expect(items.map((i) => i.id)).toEqual(["approval-ok"]);
  });

  it("票数满了但还缺特权票时，那句话原样落在这一行上", () => {
    // 只说「已 2/2 票」会让人以为该批了，而它还挂着——那看起来像故障。
    // 措辞只有 voteProgress 一份，这里钉住它确实被用上了
    const item = workItemsFromApprovals(
      [approval({ votes_cast: 2, privileged_vote_required: true })],
      NOW,
    )[0];
    expect(item?.meta).toBe("staff_bob 提交 · 票数已满 2/2，但还缺一张 approval.l4 特权票");
  });

  it("到期时刻解析不出来时说「到期时间未知」，不拼出「—后到期」", () => {
    const item = workItemsFromApprovals([approval({ expires_at: "" })], NOW)[0];
    expect(item?.due).toBe("到期时间未知");
  });
});

// XM-WORKBENCH-TRUNCATION：取满上限时要说出来。
//
// 一张"没有待处理事项"的清单如果其实被截断了，人会据此收工——这比少一条
// 信息严重得多。
describe("这一屏是不是全部", () => {
  const none = {
    activeCategoryId: "",
    alertsTruncated: false,
    jobsTruncated: false,
    approvalsTruncated: false,
  };

  it("没到上限时不说话——显示一句「没有截断」是噪声", () => {
    expect(truncationNote(none)).toBeNull();
  });

  it("告警被截断时说出来，并且只说「可能」", () => {
    const note = truncationNote({ ...none, alertsTruncated: true });
    expect(note).not.toBeNull();
    expect(note).toContain("可能不是全部");
    expect(note).toContain(String(ACTIVE_ALERTS_LIMIT));
    expect(note).not.toContain("后台任务");
    expect(note).not.toContain("待审批");
  });

  it("任务被截断时说出来", () => {
    const note = truncationNote({ ...none, jobsTruncated: true });
    expect(note).toContain("后台任务");
    expect(note).toContain(String(WORK_JOBS_LIMIT));
    expect(note).not.toContain("活跃告警");
  });

  it("待审批被截断时说出来", () => {
    const note = truncationNote({ ...none, approvalsTruncated: true });
    expect(note).toContain("待审批的单");
    expect(note).toContain(String(WORK_APPROVALS_LIMIT));
    expect(note).not.toContain("活跃告警");
    expect(note).not.toContain("后台任务");
  });

  it("三边都被截断就都说", () => {
    const note = truncationNote({
      activeCategoryId: "",
      alertsTruncated: true,
      jobsTruncated: true,
      approvalsTruncated: true,
    });
    expect(note).toContain("活跃告警");
    expect(note).toContain("待审批的单");
    expect(note).toContain("后台任务");
  });

  // 在「待审批」下面提"告警取了 200 条"是噪声：那一格根本不显示告警。
  //
  // 这里逐字比整句而不是 `not.toContain`：断言"某一句不在"太容易恒真
  // （拼错一个字、少一个格子，它照样"不在"）。整句相等既钉住了该说的，
  // 也钉住了不该说的。
  it("只说当前这一格可能被截断的那一条", () => {
    const all = { alertsTruncated: true, jobsTruncated: true, approvalsTruncated: true };
    expect(truncationNote({ ...all, activeCategoryId: "incidents" })).toBe(
      "这一屏可能不是全部：活跃告警只取了 200 条。完整清单在各自的页面里。",
    );
    expect(truncationNote({ ...all, activeCategoryId: "approvals" })).toBe(
      "这一屏可能不是全部：待审批的单只取了 20 条。完整清单在各自的页面里。",
    );
    expect(truncationNote({ ...all, activeCategoryId: "jobs" })).toBe(
      "这一屏可能不是全部：已放弃的后台任务只取了 20 条。完整清单在各自的页面里。",
    );
    // 没有数据源的分类底下一条都不说
    expect(truncationNote({ ...all, activeCategoryId: "finance" })).toBeNull();
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

  // XM-UPSTREAM-DETAIL-COPY：这一行以前写着「凭据到期与权限异常随『人员与权限』
  // 页上线」。/identity 早就建成了——一句指错方向的话比没有话更坏，它让人去等
  // 一个已经到货的东西。同一句话的另一份副本（WORK_CATEGORIES.expiring）上一次
  // 已经订正过，这一份被漏掉了，所以现在两处共用同一段文案。
  it("「安全」说的是真正的缺口，不是某个早就建成的页面", () => {
    const security = focusRows([])[2];
    expect(security?.domain).toBe("安全");
    // 凭据到期缺的是数据模型：CredentialRef 只有 scope/name
    expect(security?.detail).toContain("凭据模型里还没有到期时间");
    expect(security?.detail).toContain("CredentialRef");
    // 权限异常缺的是判定规则，不是页面
    expect(security?.detail).toContain("权限异常则还没有判定规则");
    // 缺席断言，已做变异验证（把旧那句写回 focusRows 后本行转红）。
    // 上面三条正向断言已经落在同一个字符串上，这一行不会因为「还没渲染」假绿。
    expect(security?.detail).not.toContain("人员与权限");
  });

  // 两处共用一份的证据：文案漂开过一次，就是这一格被漏掉的原因
  it("「安全」行与「即将到期」分类说的是同一个凭据缺口，不各写一份", () => {
    const security = focusRows([])[2];
    const expiring = WORK_CATEGORIES.find((c) => c.id === "expiring");
    const shared = "凭据模型里还没有到期时间：CredentialRef 只登记 secret://<scope>/<name>，不记录签发与轮换到期。";
    expect(security?.detail.startsWith(shared)).toBe(true);
    expect(expiring?.blockedBy?.startsWith(shared)).toBe(true);
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

  // XM-WORKBENCH-TRUTH：这一行原来是三个写死的字面量，不查任何数据。它今天
  // 恰好说对了，但将来通道接上了、或接上后挂了，它还是这三个词。
  describe("开票系统那一行由数据算出来，不是写死的字面量", () => {
    const invoice = (
      input: Partial<{
        services: ServiceItem[];
        metrics: MetricItem[];
        alerts: AlertItem[];
      }> = {},
    ) =>
      platformMatrixRows({ ...base, ...input }).find((r) => r.key === "invoice");

    it("今天什么都没有，算出来仍然是「未接入 / 未初始化」——诚实不等于换结论", () => {
      const row = invoice();
      expect(row?.statusLabel).toBe("未接入");
      expect(row?.freshness.state).toBe("uninitialized");
      expect(row?.events).toBe(0);
      expect(row?.observedAt).toBeNull();
      expect(row?.stage).toBe("契约前置");
    });

    it("登记簿里有实例时走 Registry 状态，与资源目录页同一套口径", () => {
      expect(invoice({ services: [service("invoice", "active")] })?.statusLabel).toBe("运行中");
      expect(invoice({ services: [service("invoice", "degraded")] })?.statusTone).toBe("warning");
    });

    it("没有实例但已经有 invoice.* 指标在采时说「未登记」，不含糊成「未接入」", () => {
      const row = invoice({
        metrics: [metric(INVOICE_ORDERS_METRIC, "fresh", "2026-08-28T11:00:00Z")],
      });
      expect(row?.statusLabel).toBe("未登记");
    });

    it("指标与告警都按 invoice 前缀归到这一行", () => {
      const row = invoice({
        metrics: [
          metric(INVOICE_ORDERS_METRIC, "failed", null, { last_error_code: "UNAVAILABLE" }),
        ],
        alerts: [alert({ id: "i1", source_metric_key: INVOICE_ORDERS_METRIC })],
      });
      expect(row?.freshness.state).toBe("failed");
      expect(row?.freshness.last_error_code).toBe("UNAVAILABLE");
      expect(row?.events).toBe(1);
    });

    it("最近观测时刻也真算，不再恒为 null", () => {
      const row = invoice({
        metrics: [metric(INVOICE_ORDERS_METRIC, "stale", "2026-08-28T09:00:00Z")],
      });
      expect(row?.observedAt).toBe("2026-08-28T09:00:00Z");
      expect(row?.freshness.state).toBe("stale");
    });

    // 整句相等而不是拼几个 toContain：在这个位置断言「某句不在」太容易恒真。
    const SCOPE_NOTE =
      "这一行说的是只读数据对接（invoice.* 指标与登记簿实例）；" +
      "点链接进去的是嵌到平台里的开票管理端，那条线已经能用——两者不是一回事。";

    it("格子里说清楚这一行说的是只读对接，与点进去的嵌入管理端不是一回事", () => {
      expect(invoice()?.scopeNote).toBe(SCOPE_NOTE);
    });

    it("登记簿里有一行之后这句话仍在——它最容易在那时候骗人", () => {
      // 「登记簿里有一行」不等于「只读数据通道通了」
      expect(invoice({ services: [service("invoice", "active")] })?.scopeNote).toBe(SCOPE_NOTE);
    });

    // 评审回合三：「一条指标都没有」那句话以前写死主语「这个平台」，开票行也
    // 显示这一句——同一格的 scopeNote 正说着它是只读数据对接，这里却叫它平台。
    it("「一条指标都没有」的那句话不把开票叫成平台", () => {
      const note = invoice()?.freshnessNote;
      expect(note).toBe("开票的只读数据通道还没有任何指标在采。");
      expect(note).not.toContain("平台");
      // 对照：平台行仍说「这个平台」——主语是参数，不是把整句换掉
      expect(platformMatrixRows(base).find((r) => r.key === "server")?.freshnessNote).toContain(
        "这个平台",
      );
    });
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

  // XM-WORKBENCH-TRUTH：前端在汇总时把「未初始化」排在「同步失败」之前，与
  // 后端 ops.Observation.Freshness 正好相反（后端为同一件事专门修过，XM-0031）。
  // 后果：Sub2API 有一条结构上永远采不到数的指标，于是那一格恒为中性灰，
  // **哪怕其余指标全部同步失败也不会变红**——对该平台的后续故障失明。
  describe("取最差：同步失败压过未初始化", () => {
    const failed = metric(REVENUE_METRIC, "failed", null, { last_error_code: "UNAVAILABLE" });
    const uninit = metric(CHANNEL_BALANCE_METRIC, "uninitialized", null);
    const cell = (metrics: MetricItem[]) =>
      platformMatrixRows({ ...base, metrics }).find((r) => r.key === "sub2api");

    it("同一平台同时有 failed 与 uninitialized 时，这一格是 failed", () => {
      expect(cell([uninit, failed])?.freshness.state).toBe("failed");
    });

    it("换个数组顺序结论不变——不是「恰好挑了最后一条」", () => {
      expect(cell([failed, uninit])?.freshness.state).toBe("failed");
    });

    it("返回的是那条 failed 记录本身，错误码要能传到徽章的悬停里", () => {
      expect(cell([uninit, failed])?.freshness.last_error_code).toBe("UNAVAILABLE");
    });

    it("这一格真的会变红，而不只是内部字符串变了", () => {
      // 旧实现下这里是 neutral（未初始化），也就是「正在发生的故障被显示成
      // 中性的尚未接入」——后端注释里那句原话
      expect(describeFreshness(cell([uninit, failed])!.freshness.state).tone).toBe("danger");
    });

    it("改排序没有把「一条指标都没有」也说成失败", () => {
      expect(platformMatrixRows(base).find((r) => r.key === "server")?.freshness.state).toBe(
        "uninitialized",
      );
    });
  });

  // 修完顺序之后的新陷阱：兜底 `?? 0` 今天恰好等于「最差」，只因为 0 是
  // uninitialized 的档位；把 0 让给 failed 之后它就变成「与失败并列」，而
  // `rank < worstRank` 的严格小于会让先到的 failed 把未知状态吃掉。
  // 这一组防的是**修复过程中的回归**，它在旧实现下是偶然绿的。
  describe("前端不认识的新鲜度状态不被 failed 盖掉", () => {
    const failed = metric(REVENUE_METRIC, "failed", null, { last_error_code: "X" });
    const unknown = metric(CHANNEL_BALANCE_METRIC, "not_applicable", "2026-08-28T11:59:00Z");

    it("未知状态排在比 failed 还差的位置", () => {
      const row = platformMatrixRows({ ...base, metrics: [failed, unknown] }).find(
        (r) => r.key === "sub2api",
      );
      expect(row?.freshness.state).toBe("not_applicable");
    });

    it("未知状态显示成「未知状态（…）」且是 warning，不静默降级也不冒充 danger", () => {
      const shown = describeFreshness("not_applicable");
      expect(shown.tone).toBe("warning");
      expect(shown.label).toContain("not_applicable");
    });

    it("它排到最差靠的是兜底档，不是靠混进优先级清单", () => {
      // 正向锚点：少了它，下面那条「不含」是恒真的
      expect(FRESHNESS_PRIORITY[0]).toBe("failed");
      expect(FRESHNESS_PRIORITY).not.toContain("not_applicable");
    });
  });

  // 这一格黄灯亮了两周，界面上一个字的说明都没有。原因从数据里发现（哪几条
  // is_partial），不是把「NewAPI 上游没有订阅订单接口」这条后端事实抄一份。
  describe("数据不完整要说清楚是哪几条、为什么", () => {
    const partial = (over = {}) =>
      metric(
        NEWAPI_SUBSCRIPTION_METRIC,
        "partial",
        "2026-08-28T11:59:00Z",
        { is_partial: true, ...over },
        "day:2026-08-27;subscription:unavailable_over_http",
      );
    const newapi = (metrics: MetricItem[]) =>
      platformMatrixRows({ ...base, metrics }).find((r) => r.key === "newapi");

    it("点名不完整的那条指标，并说清这不是故障、也不会自己好转", () => {
      const row = newapi([partial()]);
      expect(row?.freshnessNote).toBe(
        "NewAPI 日订阅这一轮采集成功了，但其中一部分数据上游给不出来，" +
          "显示的数值可能偏小；这不是故障，也不会自己好转。",
      );
    });

    it("水位线原样放进悬停当工程证据，只展示不解析", () => {
      // watermark 是连接器私有的自由文本，解析它等于在前端复刻一份后端事实
      expect(newapi([partial()])?.freshnessEvidence).toBe(
        "采集水位线（连接器原文）：NewAPI 日订阅：day:2026-08-27;subscription:unavailable_over_http",
      );
    });

    it("加了解释不改判定：徽章仍是「数据不完整」", () => {
      const row = newapi([partial()]);
      expect(row?.freshness.state).toBe("partial");
      expect(describeFreshness(row!.freshness.state).label).toBe("数据不完整");
    });

    it("没有要解释的东西就不说话——噪声也是一种谎", () => {
      const fresh = metric(NEWAPI_CHANNELS_METRIC, "fresh", "2026-08-28T11:59:00Z");
      expect(newapi([fresh])?.freshnessNote).toBeUndefined();
      expect(newapi([fresh])?.freshnessEvidence).toBeUndefined();
    });
  });

  // 「这个平台一条指标都没有」与「有指标在采、其中一条从未采到值」是两回事，
  // 而今天两者显示成同一个中性灰徽章。这个区分不需要任何新后端字段。
  describe("「未初始化」分两种，说得出是哪一种", () => {
    it("一条指标都没有时就这么说", () => {
      expect(platformMatrixRows(base).find((r) => r.key === "server")?.freshnessNote).toBe(
        "这个平台还没有任何指标在采。",
      );
    });

    it("有指标在采但某一条从未采到值时点名是哪一条", () => {
      const row = platformMatrixRows({
        ...base,
        metrics: [
          metric(REVENUE_METRIC, "fresh", "2026-08-28T11:59:00Z"),
          metric(CHANNEL_BALANCE_METRIC, "uninitialized", null),
        ],
      }).find((r) => r.key === "sub2api");
      expect(row?.freshnessNote).toContain("有指标在采，但「Sub2API 渠道余额」从未采到值");
      // 措辞止步于事实：前端没有任何字段能断言「结构上不适用」，
      // 一条刚上线还没采到值的新指标与它长得一模一样
      expect(row?.freshnessNote).not.toContain("不适用");
    });
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
