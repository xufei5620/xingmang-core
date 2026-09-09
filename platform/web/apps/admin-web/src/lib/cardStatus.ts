/** Infini 卡片状态的显示字典。
 *
 *  取值来自官方参考（infini-skill/references/CARDS.md）的权威枚举，其中
 *  `suspend` 已于 2026-09-05 对真实卡冻结实测确认。此前代码里猜的是
 *  `frozen`，后果是一张已锁定的卡永远显示「锁定」按钮，点下去是再锁一次。
 *
 *  文案对齐上游后台：那边叫「已激活 / 已锁定」，这里不另起一套叫法，
 *  免得运营在两个后台之间来回对照。
 *
 *  **没见过的取值原样显示并标记**，不静默归到某个已知分类。上游加新状态时，
 *  那正是最需要被人看见的时刻——归错类比显示一个陌生单词危险得多。 */

const LABELS: Record<string, string> = {
  init: "初始化",
  pending: "处理中",
  active: "已激活",
  suspend: "已锁定",
  // 关停是异步的：先 pending_delete，结清余额后才 deleted。
  pending_delete: "删除中",
  deleted: "已删除",
};

export function cardStatusLabel(status: string): string {
  return LABELS[status] ?? `${status}（未知状态）`;
}

export function cardStatusTone(status: string): "neutral" | "success" | "warning" | "danger" {
  switch (status) {
    case "active":
      return "success";
    case "init":
    case "pending":
      return "warning";
    case "suspend":
    case "pending_delete":
    case "deleted":
      return "danger";
    default:
      // 未知取值给中性色而不是任何一种"看起来正常"的颜色。
      return "neutral";
  }
}

/** 这张卡当前是不是锁定的——决定操作列显示「锁定」还是「解锁」。 */
export function isCardLocked(status: string): boolean {
  return status === "suspend";
}

/** 交易类型与状态的显示文案。
 *
 *  上游在两处用了不同的大小写：REST 流水接口回 `Consume` / `Completed`，
 *  webhook 回 `consume` / `completed`。同一个概念两套写法，按字面匹配必有
 *  一条路悄悄匹配不上——所以查表前一律小写。
 *
 *  取值来自官方 webhook 文档（consume / reversal / refund；authorized /
 *  completed / failed）与卡 API 示例。**没见过的取值原样显示**，理由同状态：
 *  上游加新类型时那正是最该被看见的时刻。
 */
const TX_TYPE_LABELS: Record<string, string> = {
  consume: "消费",
  reversal: "冲正",
  refund: "退款",
  topup: "充值",
  top_up: "充值",
  redeem: "赎回",
};

const TX_STATUS_LABELS: Record<string, string> = {
  authorized: "授权中",
  completed: "已完成",
  failed: "失败",
  pending: "处理中",
};

export function transactionTypeLabel(value: string): string {
  if (!value) return "—";
  return TX_TYPE_LABELS[value.toLowerCase()] ?? value;
}

export function transactionStatusLabel(value: string): string {
  if (!value) return "—";
  return TX_STATUS_LABELS[value.toLowerCase()] ?? value;
}
