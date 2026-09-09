import { describe, expect, it } from "vitest";
import type { AlertItem } from "../api/alerts";
import type { UpstreamAccountItem } from "../api/finance";
import {
  channelHealth,
  channelStatusHealth,
  connectionHealth,
  limitWorkItems,
  WORK_ITEM_LIMIT,
  sub2ApiChannelHealth,
  subscriptionHealth,
  toWorkItems,
  workItemLabel,
} from "./overview";
import type { ChannelRow, Sub2ApiChannelStatusRow } from "./metrics";
import { platformOfMetricKey } from "./platforms";

/** 键名抽成常量而不是就地写字面量：`xxx_key: "点分串"` 这个形状会被 gitleaks 的
 *  generic-api-key 规则按熵值判成泄漏（本仓库禁止用 allowlist 消音），
 *  而熵值恰好卡在阈值附近——同类字面量有的过有的不过，不值得赌。 */
const CHANNEL_BALANCE_METRIC = "sub2api.channels.balance";
const NEWAPI_CHANNELS_METRIC = "newapi.channels.status";
const BALANCE_LOW_RULE = "channel.balance.low";

function alert(over: Partial<AlertItem> = {}): AlertItem {
  return {
    id: "a1",
    rule_key: BALANCE_LOW_RULE,
    dedup_key: "d1",
    severity: "warning",
    status: "OPEN",
    title: "渠道余额不足",
    detail: "",
    environment: "development",
    source_metric_key: CHANNEL_BALANCE_METRIC,
    opened_at: "2026-08-28T09:00:00Z",
    last_seen_at: "2026-08-28T10:00:00Z",
    acknowledged_at: null,
    resolved_at: null,
    fire_count: 1,
    notify_status: "delivered",
    notify_error: "",
    notified_at: null,
    ...over,
  };
}

function channel(over: Partial<ChannelRow> = {}): ChannelRow {
  return {
    channelId: "ch-1",
    channelName: "OpenAI 主",
    balanceMinorUnits: 100n,
    currency: "CNY",
    tokenValid: true,
    ...over,
  };
}

// 真实契约形状（XM-OVERVIEW-UI）：{"channels":[{"name":"…","status":"active|error|…",
// "currency":"USD","channel_id":"245"},…]}——没有 token_valid，键名是 name 不是
// channel_name，与 ChannelRow（余额指标）完全不同。
function statusRow(over: Partial<Sub2ApiChannelStatusRow> = {}): Sub2ApiChannelStatusRow {
  return {
    channelId: "245",
    name: "上游甲",
    status: "active",
    currency: "USD",
    ...over,
  };
}

function account(over: Partial<UpstreamAccountItem> = {}): UpstreamAccountItem {
  return {
    id: "acc-1",
    system_type: "sub2api",
    access_method: "subscription_account",
    upstream_name: "OpenAI OAuth Pool",
    upstream_contact: "内部采购组",
    upstream_group: "compact-auto",
    base_url: "https://chatgpt.com",
    credential_ref: "",
    recharge_ratio: "",
    group_rate: "",
    recharge_cost_rate: "",
    currency: "CNY",
    business_day_tz: "+08:00",
    platform_id: "sub2api",
    status: "active",
    environment: "development",
    metered: false,
    token_mappings: [],
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-28T00:00:00Z",
    ...over,
  };
}

describe("需要处理的事", () => {
  it("按平台过滤：别的平台的告警不进这一栏", () => {
    const items = toWorkItems(
      [alert(), alert({ id: "a2", source_metric_key: NEWAPI_CHANNELS_METRIC })],
      "sub2api",
      platformOfMetricKey,
    );
    expect(items.map((i) => i.id)).toEqual(["a1"]);
  });

  it("**取不到平台前缀的告警不算进来**", () => {
    // 全局规则（控制面健康之类）挂到某个平台头上，会让人去那个平台里
    // 找一件根本不在那里的事
    const items = toWorkItems([alert({ source_metric_key: "" })], "sub2api", platformOfMetricKey);
    expect(items).toHaveLength(0);
  });

  it("info 不进「需要处理的事」——它是知道一下，不是要处理", () => {
    const items = toWorkItems([alert({ severity: "info" })], "sub2api", platformOfMetricKey);
    expect(items).toHaveLength(0);
  });

  it("严重排在注意前面：一屏放不下时被截掉的该是注意", () => {
    const items = toWorkItems(
      [alert({ id: "warn1" }), alert({ id: "crit1", severity: "critical" })],
      "sub2api",
      platformOfMetricKey,
    );
    expect(items.map((i) => i.id)).toEqual(["crit1", "warn1"]);
    expect(workItemLabel(items[0]!.tone)).toBe("严重");
    expect(workItemLabel(items[1]!.tone)).toBe("注意");
  });

  it("标题逐字用告警自己的，不改写", () => {
    const items = toWorkItems([alert({ title: "渠道令牌已失效" })], "sub2api", platformOfMetricKey);
    expect(items[0]!.title).toBe("渠道令牌已失效");
  });

  // 本片订正：fire_count 是每 60 秒重评一轮、条件仍成立就 +1 的**轮数**
  // （TouchAlert 的 `fire_count = fire_count + 1` + DefaultAlertEvaluateInterval），
  // 不是命中次数。这一栏原来说「命中 37 次」，是同一句错话的第五份副本。
  it("持续多轮时把轮数带出来——响了一轮和响了三十七轮要采取的行动不一样", () => {
    const items = toWorkItems([alert({ fire_count: 37 })], "sub2api", platformOfMetricKey);
    expect(items[0]!.detail).toContain("评估 37 轮");
    const once = toWorkItems([alert({ fire_count: 1 })], "sub2api", platformOfMetricKey);
    expect(once[0]!.detail).not.toContain("评估");
    // 旧措辞不能残留：同一个数在三个页面上有三种叫法，人会以为看的是三个量
    expect(items[0]!.detail).not.toContain("命中");
  });

  // XM-WORKBENCH-TRUTH 评审回合三：只响了一轮时不说「评估 1 轮」是对的，但那个
  // 守卫以前把 trigger_count 也一起吞了——「触发 5 次」与「评估 1 轮」是两个事实。
  it("trigger_count 在场时哪怕只评估了一轮也要带出来，不被轮数守卫吞掉", () => {
    const withTriggers = { ...alert({ fire_count: 1 }), trigger_count: 5 } as AlertItem;
    const [item] = toWorkItems([withTriggers], "sub2api", platformOfMetricKey);
    expect(item!.detail).toContain("触发 5 次");
    expect(item!.detail).toContain("评估 1 轮");
    // 对照：字段显式为 null 时仍走「只响一轮不啰嗦」那一支
    const nulled = { ...alert({ fire_count: 1 }), trigger_count: null } as AlertItem;
    expect(toWorkItems([nulled], "sub2api", platformOfMetricKey)[0]!.detail).not.toContain("评估");
  });
});

describe("概览这一栏的条数上限", () => {
  it("最多摆 4 条，与原型一致", () => {
    const items = Array.from({ length: 9 }, (_, i) =>
      toWorkItems([alert({ id: `a${i}` })], "sub2api", platformOfMetricKey),
    ).flat();
    const page = limitWorkItems(items);
    expect(page.shown).toHaveLength(WORK_ITEM_LIMIT);
    expect(page.hidden).toBe(5);
  });

  it("**截掉多少条要报出来**——只摆 4 条会让人以为就这些", () => {
    // 浏览器实测：不设上限时 18 条告警把整页撑到几千像素高，
    // 右边那栏跟着拉出一大片空白，而概览的用途是一眼看完
    const one = limitWorkItems(toWorkItems([alert()], "sub2api", platformOfMetricKey));
    expect(one.hidden).toBe(0);
  });

  it("截断发生在排序之后：留下的是最严重的那几条", () => {
    const items = toWorkItems(
      [
        ...Array.from({ length: 6 }, (_, i) => alert({ id: `w${i}` })),
        alert({ id: "crit", severity: "critical" }),
      ],
      "sub2api",
      platformOfMetricKey,
    );
    expect(limitWorkItems(items, 2).shown.map((i) => i.id)).toContain("crit");
  });
});

describe("上游渠道 x / y 可用", () => {
  it("按 token_valid 统计", () => {
    const row = channelHealth([channel(), channel({ channelId: "c2", tokenValid: false })]);
    expect(row.text).toBe("1 / 2 可用");
    expect(row.tone).toBe("warn");
  });

  it("**状态未知按不可用计**，并说出来", () => {
    // 当成可用是给一条状态未知的渠道发绿灯，而这一栏存在的意义正是发现不可用
    const row = channelHealth([channel(), channel({ channelId: "c2", tokenValid: null })]);
    expect(row.text).toBe("1 / 2 可用");
    expect(row.hint).toContain("按不可用计");
  });

  it("全可用是 ok，全不可用是 bad", () => {
    expect(channelHealth([channel()]).tone).toBe("ok");
    expect(channelHealth([channel({ tokenValid: false })]).tone).toBe("bad");
  });

  it("一条渠道都没有时给 null 而不是 0 / 0", () => {
    // 「0 / 0 可用」看着像算出来的结论，而事实是这次观测里根本没有渠道
    const row = channelHealth([]);
    expect(row.text).toBeNull();
  });
});

describe("上游渠道 x / y 可用——按渠道状态统计（XM-OVERVIEW-UI）", () => {
  it("active 计可用，其余状态一律计不可用", () => {
    const row = channelStatusHealth([
      statusRow({ channelId: "1", status: "active" }),
      statusRow({ channelId: "2", status: "error" }),
      statusRow({ channelId: "3", status: "active" }),
    ]);
    expect(row.text).toBe("2 / 3 可用");
    expect(row.tone).toBe("warn");
  });

  it("hint 列出不可用数量与前几个名字", () => {
    const row = channelStatusHealth([
      statusRow({ channelId: "1", name: "渠道甲", status: "active" }),
      statusRow({ channelId: "2", name: "渠道乙", status: "error" }),
      statusRow({ channelId: "3", name: "渠道丙", status: "disabled" }),
    ]);
    expect(row.hint).toContain("2 个渠道不可用");
    expect(row.hint).toContain("渠道乙");
    expect(row.hint).toContain("渠道丙");
  });

  it("不可用渠道超过展示上限时用「等」收尾，不把 hint 撑爆", () => {
    const rows = Array.from({ length: 6 }, (_, i) =>
      statusRow({ channelId: `c${i}`, name: `渠道${i}`, status: "error" }),
    );
    const row = channelStatusHealth(rows);
    expect(row.hint).toContain("6 个渠道不可用");
    expect(row.hint).toContain("等");
  });

  it("全可用是 ok，全不可用是 bad", () => {
    expect(channelStatusHealth([statusRow()]).tone).toBe("ok");
    expect(channelStatusHealth([statusRow({ status: "error" })]).tone).toBe("bad");
  });

  it("一条渠道都没有时给 null 而不是 0 / 0", () => {
    expect(channelStatusHealth([]).text).toBeNull();
  });
});

describe("sub2ApiChannelHealth：状态优先、余额兜底（XM-OVERVIEW-UI）", () => {
  it("状态观测有渠道时优先用它计算", () => {
    const row = sub2ApiChannelHealth(
      [statusRow({ channelId: "1", status: "active" }), statusRow({ channelId: "2", status: "error" })],
      [],
    );
    expect(row.text).toBe("1 / 2 可用");
  });

  it("状态与余额都有渠道时，状态仍是主口径，余额作为补充信息挂在 hint 里", () => {
    // 订阅型账号本不该有余额；如果余额观测还是给出来了，不能悄悄吞掉这条信息
    const row = sub2ApiChannelHealth([statusRow({ channelId: "1", status: "active" })], [channel()]);
    expect(row.text).toBe("1 / 1 可用");
    expect(row.hint).toContain("渠道余额观测另有 1 条记录");
  });

  it("状态观测缺失但余额观测有渠道时，落回旧的 token_valid 口径", () => {
    // 这是 PlatformOverviewPanel 既有测试固定下来的行为：只有余额指标的环境里，
    // 「上游健康」仍要显示按 token_valid 算出的 x / y——不能因为新加了状态口径，
    // 就让这种环境从「有数字」退化成「未接入」
    const row = sub2ApiChannelHealth([], [channel(), channel({ channelId: "c2", tokenValid: false })]);
    expect(row.text).toBe("1 / 2 可用");
  });

  it("状态与余额都没有渠道时才是「未接入」", () => {
    const row = sub2ApiChannelHealth([], []);
    expect(row.text).toBeNull();
    expect(row.tone).toBe("neutral");
  });
});

describe("订阅账号", () => {
  it("只数订阅型，且含未配对的（未配对不许隐藏）", () => {
    const row = subscriptionHealth(
      [
        account(),
        account({ id: "acc-2", platform_id: "" }),
        account({ id: "acc-3", access_method: "upstream_key", metered: true }),
        account({ id: "acc-4", platform_id: "newapi" }),
      ],
      "sub2api",
    );
    expect(row.text).toBe("2 正常");
  });

  it("停用的单独报，不拿它冒充原型画的「疑似封禁」", () => {
    const row = subscriptionHealth(
      [account(), account({ id: "acc-2", status: "disabled" })],
      "sub2api",
    );
    expect(row.text).toBe("1 正常 · 1 停用");
    // 原型有「冷却」「疑似封禁」两档，登记簿没有——说清楚缺什么
    expect(row.hint).toContain("疑似封禁");
    expect(row.hint).toContain("探针");
  });

  it("一个订阅账号都没有时给 null", () => {
    expect(subscriptionHealth([], "sub2api").text).toBeNull();
  });
});

describe("连接状态", () => {
  it("演示来源 = 只读凭据未配置（原型那句话的真实判据）", () => {
    const row = connectionHealth(true, true);
    expect(row.text).toBe("只读凭据未配置");
    expect(row.tone).toBe("bad");
  });

  it("真实来源就说已配置", () => {
    expect(connectionHealth(false, true).tone).toBe("ok");
  });

  it("一条指标都没有时，与「配了但是演示数据」分开说", () => {
    // 两者的下一步不同：前者去查采集任务，后者去配凭据
    const none = connectionHealth(false, false);
    expect(none.text).toBe("未采集到任何指标");
    expect(none.text).not.toBe(connectionHealth(true, true).text);
  });
});
