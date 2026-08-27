import { PageState, StatTile } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";

/** 渠道保障(原型孤儿页 `V["s2/model"]`,ADMIN-IA v3 §8.1 裁定 #1 恢复为页签)。
 *
 *  **这是 UI 蓝图态**：布局与文案照原型逐字，但**表里一行数据都没有**。
 *
 *  为什么不把原型的样例行搬过来：交接文档 §9.7 明写「UI 可以先实现，真实探针和
 *  高级统计检测**不能提前冒充已上线**」。原型里那几行「Claude 官方 API /
 *  一致 / probe-8842」看着就是真的检测结果——搬过来之后，没有任何东西能告诉
 *  看的人这是编的。所以留下的是**列结构**（它才是蓝图的内容）与一句说明。
 *
 *  原型自己的 warnbar 也说了同一件事，逐字保留在下面。 */

/** 原型 warnbar 逐字。 */
const ASSURANCE_WARNBAR =
  "这是目标布局。当前 read contract v1 没有渠道模型与检测历史，落地需要契约 v2、后台调度和只读快照。";

function Warnbar() {
  return (
    <p
      role="status"
      className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
    >
      {ASSURANCE_WARNBAR}
    </p>
  );
}

/** 只有表头的蓝图表。
 *
 *  画出列结构而不是一句「敬请期待」：蓝图的内容就是「将来这里有哪几列」,
 *  而空态说清楚为什么现在没有行。两样都要——只有空态就看不出结构,
 *  只有假数据就分不清真假。 */
function BlueprintTable({
  caption,
  columns,
  emptyTitle,
  emptyDescription,
}: {
  caption: string;
  columns: string[];
  emptyTitle: string;
  emptyDescription: string;
}) {
  return (
    <div className="flex flex-col overflow-hidden rounded-lg border border-edge bg-surface shadow-sm">
      <div className="max-w-full overflow-x-auto">
        <table className="w-full border-collapse text-sm">
          <caption className="sr-only">{caption}</caption>
          <thead className="border-b border-edge bg-surface-muted">
            <tr>
              {columns.map((c) => (
                <th
                  key={c}
                  scope="col"
                  className="px-3 py-2 text-left text-xs font-medium text-fg-muted whitespace-nowrap"
                >
                  {c}
                </th>
              ))}
            </tr>
          </thead>
        </table>
      </div>
      <PageState kind="unavailable" title={emptyTitle} description={emptyDescription} compact />
    </div>
  );
}

const NEEDS = "需要契约 v2、后台调度与只读快照（M1.5）；真实探针未上线前不显示任何检测结果。";

/** 保障概览。 */
function AssuranceOverview() {
  return (
    <div className="flex flex-col gap-3">
      <Warnbar />
      {/* 四格照原型，但一个数都不给：显示 5 / 11 / 38 / 2 就是冒充已上线 */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {["受保障渠道", "模型映射", "24h 检测", "需复核"].map((label) => (
          <StatTile
            key={label}
            label={label}
            value="—"
            unavailable
            note="随渠道保障探针（M1.5）上线"
            status={<Badge tone="neutral">未接入</Badge>}
          />
        ))}
      </div>
      <BlueprintTable
        caption="渠道保障状态：从渠道进入可查看对应模型与完整历史"
        columns={["渠道", "接入方式", "模型数", "最近检测", "结论", "下次策略"]}
        emptyTitle="还没有渠道保障数据"
        emptyDescription={NEEDS}
      />
    </div>
  );
}

/** 检测任务。 */
function AssuranceProbes() {
  return (
    <div className="flex flex-col gap-3">
      <Warnbar />
      <p className="text-xs text-fg-muted">支持不定时抽检，也保证最低检测频率。</p>
      <BlueprintTable
        caption="检测任务"
        columns={["任务", "渠道", "目标模型", "策略", "最近一次", "结果"]}
        emptyTitle="还没有检测任务"
        emptyDescription={NEEDS}
      />
    </div>
  );
}

/** 历史记录。 */
function AssuranceHistory() {
  return (
    <div className="flex flex-col gap-3">
      <Warnbar />
      <p className="text-xs text-fg-muted">所有检测结果按渠道和模型留痕。</p>
      <BlueprintTable
        caption="保障历史"
        columns={["时间", "渠道", "模型", "检测项", "结果", "证据"]}
        emptyTitle="还没有保障历史"
        emptyDescription={NEEDS}
      />
    </div>
  );
}

/** 按子页签 id 取内容。认不出的返回 undefined，由调用方回落到通用占位。 */
export function assuranceSubTab(subId: string): ReactNode | undefined {
  switch (subId) {
    case "overview":
      return <AssuranceOverview />;
    case "probes":
      return <AssuranceProbes />;
    case "history":
      return <AssuranceHistory />;
    default:
      return undefined;
  }
}
