import type { BadgeTone } from "@xingmang/ui-primitives";
import type { MetricHistoryItem } from "../api/platform";
import { readConnectorHealthValue } from "./ops";

/** XM-CREDS-TAB-PROBE：「连接与凭据」页签的连接器探测。
 *
 *  数据来自 `ConnectorProbeWorker` 每次探测写下的观测
 *  （`internal/platform/jobs/connector_probe.go`），value 形状是
 *  {version, supported, healthy, kind, latency_ms, checked_at}。
 *
 *  这里只做纯派生，不碰 React：一行探测该显示什么，是可以脱离渲染回答的问题。 */

/** 连接器探测写入的指标键。平台标识不认识时返回空串——调用方据此不发查询，
 *  而不是拼一个后端一定查不到的键然后显示"没有数据"。 */
export function connectorHealthMetricKey(platform: string): string {
  if (platform === "sub2api") return "sub2api.connector.health";
  if (platform === "newapi") return "newapi.connector.health";
  return "";
}

export interface UpstreamSupport {
  label: string;
  tone: BadgeTone;
  /** 一句话解释这个判定意味着什么；不确定时说"不确定"，不猜。 */
  detail: string;
}

/** 把 supported 翻成人话。
 *
 *  这不是一个装饰性徽章：`supported` 是**采集链路的判据**，连接器判假时
 *  这个上游的采集就停。所以判假要说清原因——矩阵没声明支持这个版本，
 *  与"上游坏了"是两件完全不同的事，处理方式也不同（前者改代码里的矩阵，
 *  后者查上游）。 */
export function describeUpstreamSupport(supported: boolean | null, version: string): UpstreamSupport {
  if (supported === true) {
    return {
      label: "矩阵已声明支持",
      tone: "success",
      detail: "连接器的兼容矩阵覆盖了这个版本，采集按已核对过的字段形状进行。",
    };
  }
  if (supported === false) {
    return {
      label: "矩阵未声明支持",
      tone: "warning",
      detail: version
        ? `连接器的兼容矩阵没有覆盖 ${version}。这不等于上游坏了：多半是上游升级后矩阵还没跟上（需要先逐字段核对源码，再加矩阵条目）。在此期间采集判据为假。`
        : "连接器的兼容矩阵没有覆盖这次探测到的版本。在此期间采集判据为假。",
    };
  }
  return {
    label: "未知",
    tone: "neutral",
    detail: "这次探测没有给出兼容矩阵判定（旧样本或探测未完成）。不知道不等于不支持。",
  };
}

/** 探测历史表的一行。纯数据，展示留给组件。 */
export interface ConnectorProbeRow {
  id: string;
  /** 探测执行时刻，优先用 value.checked_at，退回观测/落库时刻。 */
  checkedAt: string;
  /** checkedAt 取自哪个字段，显示成小字——两者相差很大时人得看得见。 */
  checkedAtSource: "探测时刻" | "观测时刻" | "落库时刻";
  version: string;
  supported: boolean | null;
  healthy: boolean | null;
  /** 连接器的错误类别（HealthResult.ErrorKind），健康时为空串。 */
  kind: string;
  latencyMs: number | null;
  /** 观测本身的状态（ok / failed …），与 healthy 不是一回事：
   *  前者是"这次采集有没有跑成功"，后者是"上游健不健康"。 */
  status: string;
  lastErrorCode: string;
  source: string;
}

/** 把 `/metrics/history` 的样本翻成探测行，**最近的在前**。
 *
 *  后端按 observed_at 升序返回；表格里人先看最近一次，所以这里倒过来。 */
export function buildProbeRows(items: MetricHistoryItem[]): ConnectorProbeRow[] {
  return items
    .map((item, index) => {
      const value = readConnectorHealthValue(item.value ?? {});
      const checkedAt = value.checkedAt || item.observed_at || item.synced_at;
      const checkedAtSource: ConnectorProbeRow["checkedAtSource"] = value.checkedAt
        ? "探测时刻"
        : item.observed_at
          ? "观测时刻"
          : "落库时刻";
      return {
        id: `${item.synced_at}#${index}`,
        checkedAt,
        checkedAtSource,
        version: value.version,
        supported: value.supported,
        healthy: value.healthy,
        kind: value.kind,
        latencyMs: value.latencyMs,
        status: item.status,
        lastErrorCode: item.last_error_code,
        source: item.source,
      };
    })
    .reverse();
}

/** 最近一次探测；没有样本时返回 null（调用方据此显示"还没探测过"，
 *  而不是显示一行全是破折号的假数据）。 */
export function latestProbe(rows: ConnectorProbeRow[]): ConnectorProbeRow | null {
  return rows[0] ?? null;
}

/** 延迟的显示值。0 是合法值（本地 fake 连接器就是 0），所以不能用真值判断。 */
export function formatLatency(latencyMs: number | null): string {
  return latencyMs === null ? "—" : `${latencyMs} ms`;
}

/** 健康列的显示值与色调。null 是"这次样本没给"，不染成红色。 */
export function describeProbeHealth(healthy: boolean | null, kind: string): {
  label: string;
  tone: BadgeTone;
} {
  if (healthy === true) return { label: "健康", tone: "success" };
  if (healthy === false) return { label: kind ? `不健康 · ${kind}` : "不健康", tone: "danger" };
  return { label: "未知", tone: "neutral" };
}
