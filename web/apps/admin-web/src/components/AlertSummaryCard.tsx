import { Badge } from "@xingmang/ui-primitives";
import { Link } from "react-router";
import type { AlertItem } from "../api/alerts";
import { countBySeverity, describeSeverity } from "../lib/alerts";

export interface AlertSummaryCardProps {
  alerts: AlertItem[];
}

/** 运营总览上的「告警」卡（规格 §9.2：总览面板的固定一项）。
 *
 *  与 MetricCard 并排，所以外形照抄它（同样的边框、圆角、标题层级），
 *  但内部不同：它没有「新鲜度」——告警不是采集来的数字，而是对数字的判读，
 *  给它套一个 freshness 徽章会是编造出来的语义。
 *
 *  零告警显示「无活动告警」而不是一个大大的 0：0 和「还没接上」在一个
 *  数字上长得一模一样，而这两件事在运营上完全相反。 */
export function AlertSummaryCard({ alerts }: AlertSummaryCardProps) {
  const counts = countBySeverity(alerts);
  const critical = describeSeverity("critical");
  const warning = describeSeverity("warning");

  return (
    <article className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <header className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="truncate text-sm font-medium text-fg">告警</h3>
          <p className="truncate font-mono text-xs text-fg-muted">alerts.active</p>
        </div>
        {/* 名字**不能**也叫「告警中心」：侧栏导航里已经有一个同名链接，
            同一页出现两个同名同目的地的链接，读屏用户听到的是两遍一模一样的
            提示，分不清自己在哪一个上面。这里说清楚它是「这张卡的全部内容」。 */}
        <Link
          to="/alerts"
          className="shrink-0 text-xs font-medium text-accent hover:underline"
        >
          查看全部告警
        </Link>
      </header>

      {counts.total === 0 ? (
        <>
          <p className="text-2xl font-semibold text-fg-muted">无活动告警</p>
          <p className="text-xs text-fg-muted">
            该环境下当前没有活跃告警
          </p>
        </>
      ) : (
        <>
          <p className="text-2xl font-semibold text-fg tabular-nums">{counts.total}</p>
          <div className="flex flex-wrap items-center gap-2">
            {counts.critical > 0 ? (
              <Badge tone={critical.tone} title={critical.hint}>
                严重 {counts.critical}
              </Badge>
            ) : null}
            {counts.warning > 0 ? (
              <Badge tone={warning.tone} title={warning.hint}>
                警告 {counts.warning}
              </Badge>
            ) : null}
            {counts.info > 0 ? <Badge tone="info">提示 {counts.info}</Badge> : null}
          </div>
        </>
      )}

      <div className="mt-auto border-t border-edge pt-2">
        {/* 「已静默的也算」必须说出来：静默期间总览显示零告警，正是最容易
            出事的时候。这行字让人知道计数没有替他把问题藏起来。 */}
        <p className="text-xs text-fg-muted">
          统计未解决的告警（含已确认与已静默）
        </p>
      </div>
    </article>
  );
}
