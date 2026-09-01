/** XM-CHAN-FIELDS0（chanfields）已经交付了渠道目录契约扩展——这些字段今天
 *  是真的，null 不再是"字段还不存在"，是这一行、这个平台在这个具体字段上的
 *  真实空值，各有各的原因。原因抄自 XM-CHAN-FIELDS0 的两份契约文档
 *  （`contracts/connectors/{sub2api,newapi}.channel-catalog.v3.md`）与其
 *  handoff 的"字段→上游来源速查"表，不是猜的。两个平台的原因经常不同：
 *  Sub2API 这一侧多数字段是"这一次没采集到"（预算限制/账号没配置这个能力）,
 *  NewAPI 这一侧不少字段是"这个平台从产品设计上就没有这个概念"（恒为 null,
 *  不会随下一次读取变化）。
 *
 *  行列（`ManagedChannelTable.tsx`）与详情页（`ChannelDetailPage.tsx`）共用
 *  这同一份原因，避免两处各写各的、日后改一处漏改另一处。 */
const FIELD_NULL_REASONS = {
  capacity: {
    sub2api: "并发数据还没采集到（账号响应里缺 current_concurrency 或 concurrency/load_factor 任一项）",
    newapi: "NewAPI 没有渠道级并发上限这个概念——限流按用户组配置，不是按渠道",
  },
  scheduling: {
    sub2api: "调度开关 / 优先级还没采集到",
    newapi: "调度开关 / 优先级还没采集到",
  },
  today: {
    sub2api: "超出本次读取的预算（40 个账号 / 次）没有采集到，或这个账号本身还没有今日数据",
    newapi: "超出本次读取的预算（40 个渠道 / 次）没有采集到，或这个渠道本身还没有今日数据",
  },
  usageWindow: {
    sub2api: "这个订阅账号没有配置费用上限（window_cost_limit）",
    newapi: "NewAPI 没有存储任何用量窗口 / 配额重置的字段",
  },
  proxy: {
    sub2api: "这个账号没有配置代理",
    newapi: "这个渠道没有配置代理，或代理地址解析失败",
  },
  lastUsed: {
    sub2api: "登记簿没有这个时间戳",
    newapi: "NewAPI 没有渠道最近服务请求时间的字段（连通性测试时间不能当服务时间用）",
  },
  createdAt: {
    sub2api: "登记簿没有这个时间戳",
    newapi: "登记簿没有这个时间戳",
  },
  expiresAt: {
    sub2api: "登记簿没有这个时间戳",
    newapi: "NewAPI 的到期概念（Codex OAuth 令牌过期）编码在读不到的凭据字段里",
  },
} as const;

export type ChannelFieldWithNullReason = keyof typeof FIELD_NULL_REASONS;

export function channelFieldNullReason(field: ChannelFieldWithNullReason, platform: "sub2api" | "newapi"): string {
  return FIELD_NULL_REASONS[field][platform];
}

/** 调度写操作固定文案——字段本身即使到位，开关/优先级仍然只读，等 XM-SCHED0。 */
export const SCHEDULING_WRITE_HINT = "调度开关待 XM-SCHED0 Action";

/** 用量窗口的 Sub2API 专属说明：真实值是费用上限占用率，不是 Anthropic 原生
 *  的 5 小时用量百分比，两者容易混淆，展示真值时也要点明是哪一个。 */
export const USAGE_WINDOW_SUB2API_HINT =
  "费用上限占用率（window_cost_limit），不是 Anthropic 原生 5 小时用量百分比；可能超过 100%，超过就是真的超支了，不是算错";
