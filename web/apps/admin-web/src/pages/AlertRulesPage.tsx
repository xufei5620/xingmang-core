import { PageHeader, navLabel } from "@xingmang/ui-admin";
import { Link } from "react-router";
import { RunwayThresholdRulePanel } from "../components/RunwayThresholdRulePanel";

/** 告警规则子页：与活跃告警并列，但不把规则编辑塞进右侧抽屉。 */
export function AlertRulesPage() {
  return (
    <section className="min-w-0">
      <div className="mb-3 flex flex-wrap items-center gap-2 text-sm">
        <Link to="/alerts?sub=alerts" className="text-accent hover:underline">活跃告警</Link>
        <span aria-hidden="true" className="text-fg-muted">/</span>
        <span className="font-medium text-fg">告警与故障规则</span>
      </div>
      <PageHeader
        title="告警与故障"
        description="规则说明、可用天数阈值、影响预览与历史。所有值按当前环境的服务端快照展示。"
      />
      <RunwayThresholdRulePanel />
      <p className="mt-4 text-xs text-fg-muted">所在位置：{navLabel("/alerts")} · 规则子页</p>
    </section>
  );
}
