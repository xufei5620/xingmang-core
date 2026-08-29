import { navItemByPath, PageHeader, PageState, type NavSubTab } from "@xingmang/ui-admin";
import { Badge, Tabs } from "@xingmang/ui-primitives";
import { useSearchParams } from "react-router";
import { NotFoundView } from "./NotFoundPage";

/**
 * 后台任务页是 Foundation-A 的只读壳。
 *
 * 这里刻意没有 query、fetch 或 River client：任务读模型尚未登记，先把信息架构、
 * 数据新鲜度和责任边界固定下来，避免用「0 条任务」冒充「任务系统正常」。等
 * Worker Query 契约落地后，再由应用层把每格的 PageState 替换成真实快照。
 */

interface JobField {
  label: string;
  value: string;
  note: string;
}

interface JobTabCopy {
  status: "未接入" | "CLI-only";
  tone: "neutral" | "info";
  summary: string;
  source: string;
  owner: string;
  freshness: string;
  retryBoundary: string;
  emptyDescription: string;
  fields: readonly JobField[];
}

/**
 * 每个页签都写清「在等什么」。状态不是数据：它描述接入阶段，不能被误读成
 * 任务数量或执行结果。`CLI-only` 表示当前只能由人工运行命令行检查，平台页
 * 不会读取或触发那条命令。
 */
const JOB_TAB_COPY: Readonly<Record<string, JobTabCopy>> = {
  running: {
    status: "未接入",
    tone: "neutral",
    summary: "运行中的任务需要 Worker Query 快照；当前没有平台读模型。",
    source: "Worker Query（未接入）",
    owner: "平台任务团队（待指定）",
    freshness: "未初始化：没有 observed_at / synced_at",
    retryBoundary: "仅展示后端声明的重试边界；本页不暂停、取消或重试任务。",
    emptyDescription: "运行中任务列表尚未接入。没有数据不代表队列为空。",
    fields: [
      { label: "任务标识", value: "—", note: "待 Job Query 契约" },
      { label: "当前状态", value: "未接入", note: "不显示假定的运行数量" },
      { label: "数据水位（Watermark）", value: "未初始化", note: "采集器尚未回写" },
      { label: "负责人", value: "平台任务团队（待指定）", note: "待责任登记" },
      { label: "数据新鲜度", value: "未初始化", note: "没有可验证的观测时间" },
    ],
  },
  scheduled: {
    status: "CLI-only",
    tone: "info",
    summary: "定时定义目前只存在于部署与命令行配置，尚无可查询的计划目录。",
    source: "部署配置 / CLI（只读人工检查）",
    owner: "平台任务团队（待指定）",
    freshness: "CLI-only：平台没有同步水位",
    retryBoundary: "计划变更属于 Action；在审批与契约上线前不提供编辑、启停或立即执行。",
    emptyDescription: "定时任务目录为 CLI-only。页面仅说明边界，不运行命令、不读取本机配置。",
    fields: [
      { label: "计划标识", value: "—", note: "待计划 Query 契约" },
      { label: "调度来源", value: "CLI-only", note: "平台尚未接入" },
      { label: "数据水位（Watermark）", value: "未初始化", note: "没有同步快照" },
      { label: "负责人", value: "平台任务团队（待指定）", note: "待责任登记" },
      { label: "重试边界", value: "仅声明，不执行", note: "计划变更须经 Action" },
    ],
  },
  batches: {
    status: "未接入",
    tone: "neutral",
    summary: "同步批次要绑定来源、游标和完整性水位；当前没有批次快照。",
    source: "同步采集器（未接入）",
    owner: "数据同步负责人（待指定）",
    freshness: "未初始化：无批次 observed_at",
    retryBoundary: "批次重跑必须先确认幂等与补偿模式；本页不触发重跑或回滚。",
    emptyDescription: "同步批次列表尚未接入。Watermark 会随采集器契约一并提供。",
    fields: [
      { label: "批次标识", value: "—", note: "待同步 Query 契约" },
      { label: "来源与范围", value: "—", note: "不猜测数据覆盖范围" },
      { label: "数据水位（Watermark）", value: "未初始化", note: "不伪造游标" },
      { label: "负责人", value: "数据同步负责人（待指定）", note: "待责任登记" },
      { label: "数据新鲜度", value: "未初始化", note: "没有批次观测时间" },
    ],
  },
  failures: {
    status: "未接入",
    tone: "neutral",
    summary: "失败记录与安全重试条件尚未进入平台审计和查询模型。",
    source: "Worker Query / 审计记录（未接入）",
    owner: "平台任务团队（待指定）",
    freshness: "未初始化：没有失败快照水位",
    retryBoundary: "只对可安全重试的错误重试；最大次数、退避和死信由后端契约决定，页面不执行。",
    emptyDescription: "失败与重试记录尚未接入。页面不会把缺少记录显示成「没有失败」。",
    fields: [
      { label: "失败任务", value: "—", note: "待失败 Query 契约" },
      { label: "错误分类", value: "—", note: "不推断可重试性" },
      { label: "数据水位（Watermark）", value: "未初始化", note: "没有失败快照" },
      { label: "负责人", value: "平台任务团队（待指定）", note: "待责任登记" },
      { label: "重试边界", value: "仅声明，不执行", note: "需 Action + 审计" },
    ],
  },
  repeated: {
    status: "CLI-only",
    tone: "info",
    summary: "多次失败与死信筛选目前只能由人工 CLI 检查，尚未形成平台告警视图。",
    source: "CLI / 死信报告（只读人工检查）",
    owner: "平台任务团队（待指定）",
    freshness: "CLI-only：没有平台 Watermark",
    retryBoundary: "多次失败必须先人工确认影响范围与补偿方式；本页不重放死信、不清除记录。",
    emptyDescription: "多次失败任务为 CLI-only。平台不会读取或执行 CLI，也不提供死信重放入口。",
    fields: [
      { label: "任务标识", value: "—", note: "待死信 Query 契约" },
      { label: "失败次数", value: "—", note: "不显示假定计数" },
      { label: "数据水位（Watermark）", value: "未初始化", note: "CLI 结果未同步" },
      { label: "负责人", value: "平台任务团队（待指定）", note: "待责任登记" },
      { label: "重试边界", value: "人工确认后由后端执行", note: "本页保持只读" },
    ],
  },
};

const FALLBACK_COPY: JobTabCopy = {
  status: "未接入",
  tone: "neutral",
  summary: "该页签尚未登记后台任务读模型。",
  source: "Worker Query（未接入）",
  owner: "平台任务团队（待指定）",
  freshness: "未初始化",
  retryBoundary: "仅展示契约，不执行任何任务操作。",
  emptyDescription: "该页签尚未接入，暂无可验证的任务数据。",
  fields: [
    { label: "数据水位（Watermark）", value: "未初始化", note: "待数据源契约" },
    { label: "负责人", value: "平台任务团队（待指定）", note: "待责任登记" },
    { label: "重试边界", value: "仅声明，不执行", note: "待后端契约" },
  ],
};

function copyFor(tabId: string): JobTabCopy {
  return JOB_TAB_COPY[tabId] ?? FALLBACK_COPY;
}

/** 后台任务页（ADMIN-IA §2.2 / §9.1）。 */
export function JobsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const hit = navItemByPath("/jobs");

  // 导航是本页签集合的唯一来源；若导航被改坏，显示 Not Found 比悄悄画一套
  // 漂移的页签更诚实。
  if (!hit) return <NotFoundView pathname="/jobs" detail="后台任务没有登记在导航中" />;

  const tabs = hit.item.subTabs;
  const rawSub = searchParams.get("sub");
  const active = rawSub === null || rawSub === "" ? tabs[0]?.id ?? "running" : rawSub;
  const selected = tabs.find((tab) => tab.id === active);

  if (!selected) {
    return (
      <NotFoundView
        pathname={`/jobs?sub=${rawSub ?? ""}`}
        detail={`「${hit.item.label}」没有名为 ${rawSub} 的子页签`}
      />
    );
  }

  const selectSub = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("sub", value);
    // 子页签是可分享的地址；replace 避免连点五格后浏览器历史堆满中间态。
    setSearchParams(next, { replace: true });
  };

  return (
    <section>
      <PageHeader
        title={hit.item.label}
        status={<Badge tone="warning">未接入·F-A</Badge>}
        description="后台任务只读蓝图：当前不读取 River 或 Worker 数据，不把空列表解释成队列正常。接入后每个快照必须携带 Watermark、观测时间、来源与新鲜度。"
      />

      <div className="flex flex-col gap-4">
        <BoundaryCard />
        <Tabs
          value={active}
          onValueChange={selectSub}
          className="gap-4"
          items={tabs.map((tab) => ({
            value: tab.id,
            label: tab.label,
            content: <JobTabPanel tab={tab} />,
          }))}
        />
      </div>
    </section>
  );
}

function BoundaryCard() {
  return (
    <section
      role="region"
      aria-labelledby="jobs-boundary-title"
      className="rounded-lg border border-edge bg-surface p-4"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 id="jobs-boundary-title" className="text-sm font-semibold text-fg">
            后台任务接入边界
          </h3>
          <p className="mt-1 text-xs text-fg-muted">
            本页只固定字段与责任边界；没有实时任务读数，也没有任何执行入口。
          </p>
        </div>
        <div className="flex shrink-0 gap-2">
          <Badge tone="neutral">未接入</Badge>
          <Badge tone="info">CLI-only</Badge>
        </div>
      </div>

      <dl className="mt-4 grid grid-cols-1 gap-3 text-xs sm:grid-cols-2 xl:grid-cols-5">
        <BoundaryItem label="数据来源" value="River / Worker Query：未接入" />
        <BoundaryItem label="数据水位（Watermark）" value="未初始化" />
        <BoundaryItem label="负责人" value="平台任务团队（待指定）" />
        <BoundaryItem label="数据新鲜度" value="未初始化；没有 observed_at" />
        <BoundaryItem
          label="重试边界"
          value="仅声明可重试条件；本页不触发重试"
        />
      </dl>
    </section>
  );
}

function BoundaryItem({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-md border border-edge bg-surface-muted px-3 py-2">
      <dt className="text-fg-muted">{label}</dt>
      <dd className="mt-1 break-words font-medium text-fg">{value}</dd>
    </div>
  );
}

function JobTabPanel({ tab }: { tab: NavSubTab }) {
  const copy = copyFor(tab.id);
  const titleId = `jobs-tab-${tab.id}-title`;

  return (
    <div className="flex flex-col gap-3">
      <section className="rounded-lg border border-edge bg-surface shadow-sm">
        <header className="flex flex-wrap items-start justify-between gap-3 border-b border-edge px-4 py-3">
          <div className="min-w-0">
            <h3 id={titleId} className="text-sm font-semibold text-fg">
              {tab.label}
            </h3>
            <p className="mt-1 text-xs text-fg-muted">{copy.summary}</p>
          </div>
          <div className="flex shrink-0 gap-2">
            <Badge tone={copy.tone}>{copy.status}</Badge>
            {copy.status === "CLI-only" ? <Badge tone="info">仅命令行</Badge> : null}
          </div>
        </header>

        <div className="p-4">
          <PageState
            kind="unavailable"
            title={`${tab.label}数据未接入`}
            description={copy.emptyDescription}
            compact
            footnote={`来源：${copy.source} · Watermark：未初始化`}
          />
        </div>
      </section>

      <section
        aria-labelledby={`${titleId}-contract`}
        className="rounded-lg border border-edge bg-surface shadow-sm"
      >
        <header className="border-b border-edge px-4 py-3">
          <h4 id={`${titleId}-contract`} className="text-sm font-semibold text-fg">
            只读字段契约
          </h4>
          <p className="mt-1 text-xs text-fg-muted">
            字段先固定，值在数据源接入后才出现；空位使用「—」而不是样例数字。
          </p>
        </header>
        <div className="overflow-x-auto p-4">
          <table className="w-full min-w-160 text-sm">
            <caption className="sr-only">{tab.label}的只读字段与接入边界</caption>
            <thead>
              <tr className="border-b border-edge text-left text-xs text-fg-muted">
                <th scope="col" className="pb-2 font-medium">
                  字段
                </th>
                <th scope="col" className="pb-2 font-medium">
                  当前值
                </th>
                <th scope="col" className="pb-2 font-medium">
                  说明
                </th>
              </tr>
            </thead>
            <tbody>
              {copy.fields.map((field) => (
                <tr key={field.label} className="border-b border-edge last:border-0">
                  <th scope="row" className="py-2 text-left font-medium text-fg">
                    {field.label}
                  </th>
                  <td className="py-2 text-fg">{field.value}</td>
                  <td className="py-2 text-xs text-fg-muted">{field.note}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="border-t border-edge px-4 py-3">
          <dl className="grid grid-cols-1 gap-2 text-xs sm:grid-cols-3">
            <BoundaryItem label="数据来源" value={copy.source} />
            <BoundaryItem label="负责人" value={copy.owner} />
            <BoundaryItem label="数据新鲜度" value={copy.freshness} />
          </dl>
          <p className="mt-3 rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg">
            <span className="font-medium">重试边界：</span>
            {copy.retryBoundary}
          </p>
        </div>
      </section>
    </div>
  );
}
