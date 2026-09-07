/** 通知投递的汇总口径（XM-ALERTS-GAPS，规格 §9.4）。
 *
 *  「通知」子页回答的是一个问题：**有人被通知到了吗**。它的数据源不是一份
 *  独立的通知流水——平台今天没有按渠道逐条记账的地方——而是告警自己带的
 *  三个字段（httpapi/alerts.go 的 `notify_status` / `notify_error` /
 *  `notified_at`）。这一层只做归类与计数，页面负责把这个来源说清楚。 */
import type { AlertItem } from "../api/alerts";

export interface NotifyRollup {
  delivered: number;
  pending: number;
  failed: number;
  /** 后端加了新投递状态而前端还没跟上时落这里，不并进上面三个。
   *
   *  单独一格而不是并进 pending：把一个不认识的状态算成「未投递」是在替
   *  服务端下结论，而这一页恰恰是用来发现「结论下错了」的。 */
  unknown: number;
  total: number;
  /** 还没解决、却没有投递出去的那些。
   *
   *  这是本模块最危险的组合，也是这一页存在的唯一理由：运维以为告警会找
   *  上门，实际上没有任何人收到（规格 §9.4 的闭环在那时是断的）。 */
  unreachedActive: AlertItem[];
}

/** 一条告警是否「已经把人通知到了」。
 *
 *  只有 delivered 算数。pending 不算——一个渠道都没配的部署会让告警永远停
 *  在 pending，而那正是最需要被看见的情形。 */
export function isDelivered(alert: AlertItem): boolean {
  return alert.notify_status === "delivered";
}

/** 按投递状态归类整批告警。
 *
 *  未解决用 `status !== "RESOLVED"` 判定，与 countBySeverity 同一条口径：
 *  静默的**算**未解决——它只是被捂住了嘴，窗口过期还会回来。 */
export function rollupNotifyDelivery(alerts: readonly AlertItem[]): NotifyRollup {
  const rollup: NotifyRollup = {
    delivered: 0,
    pending: 0,
    failed: 0,
    unknown: 0,
    total: alerts.length,
    unreachedActive: [],
  };
  for (const alert of alerts) {
    switch (alert.notify_status) {
      case "delivered":
        rollup.delivered += 1;
        break;
      case "pending":
        rollup.pending += 1;
        break;
      case "failed":
        rollup.failed += 1;
        break;
      default:
        rollup.unknown += 1;
    }
    if (alert.status !== "RESOLVED" && !isDelivered(alert)) {
      rollup.unreachedActive.push(alert);
    }
  }
  return rollup;
}
