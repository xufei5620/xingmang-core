import { useQuery } from "@tanstack/react-query";
import {
  FreshnessBadge,
  PageHeader,
  StatTile,
  formatUtcTimestamp,
  navLabel,
} from "@xingmang/ui-admin";
import { Badge, EmptyState } from "@xingmang/ui-primitives";
import { useId, useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import {
  ALERT_STATUS_ALL,
  listAlerts,
  listAlertsPage,
  type AlertItem,
} from "../api/alerts";
import { listApprovals, type ApprovalItem } from "../api/approvals";
import { listJobRuns, type JobRunItem } from "../api/jobs";
import {
  listAuditEvents,
  listMetrics,
  listServices,
  type AuditEventItem,
  type MetricItem,
  type ServiceItem,
} from "../api/platform";
import { APPROVAL_QUEUE_PATH } from "../components/ActionResultNote";
import { ApiStateView } from "../components/ApiStateView";
import { OVERVIEW_POLL_INTERVAL_MS, useAutoRefresh } from "../lib/autoRefresh";
import {
  focusRows,
  platformMatrixRows,
  recentlyRecoveredCount,
  urgentCount,
  truncationNote,
  workItemsFromAlerts,
  workItemsFromApprovals,
  workItemsFromJobRuns,
  approvalsDueSoonCount,
  ACTIVE_ALERTS_LIMIT,
  APPROVAL_DUE_WINDOW_HOURS,
  RECOVERED_WINDOW_HOURS,
  WORK_APPROVALS_LIMIT,
  WORK_CATEGORIES,
  WORK_JOBS_LIMIT,
  type MatrixRow,
  type WorkItem,
} from "../lib/workbench";

/** 「最近告警（含已解决）」一次拉多少条。
 *
 *  与后端 `defaultAlertLimit` 对齐。写成常量并显式传参，是为了能判断
 *  「这一屏是不是被截断了」——恰好取满时，「最近恢复」的计数可能少算,
 *  那句话必须显示出来，而不是让人以为看到的是全部。 */
const RECENT_ALERTS_LIMIT = 200;

/** 最近活动取几条。工作台只做入口，完整清单在审计记录页。 */
const RECENT_ACTIVITY_LIMIT = 5;

// 两条数据源的取数上限都在 lib/workbench：判断"是不是被截断了"要用同一个数，
// 分两处写迟早会分叉（XM-WORKBENCH-TRUNCATION）。

/** 运营工作台(ADMIN-IA v3 §一 分组 1 第 1 页，原型 `#/g/overview`)。
 *
 *  版式照原型：顶部四格计数 → 我的待处理 → 运营焦点 + 最近活动 → 平台状态矩阵。
 *
 *  **不合成总健康分**（交接文档 §9.1 明令禁止，原型也把可靠性/财务/安全分成三行）。
 *  一个 87 分的看板没法回答「我现在该去修哪个」，而把三条互不相关的信号平均
 *  起来，任何一条恶化都会被另外两条稀释掉。
 *
 *  能接真数据的格子接真数据（告警、指标、注册表、审计），接不上的格子明确显示
 *  「未接入」并说清楚什么上线之后它才会有内容——摆一个永远是 0 的格子,
 *  等于告诉运营「这一类现在没有问题」。 */
export function OverviewPage() {
  const alertsQuery = useQuery({
    // 用带信封的那个：截断由服务端说（XM-ALERTS-LIST-TRUNCATED）。
    queryKey: ["alerts", "active", ACTIVE_ALERTS_LIMIT],
    queryFn: ({ signal }) => listAlertsPage({ signal, limit: ACTIVE_ALERTS_LIMIT }),
  });
  // 「最近恢复」要的是**已解决**的告警，活跃列表里没有它们，所以是第二条 query。
  // 不把两者合成一条：活跃告警是这一屏最要紧的东西，它不该因为「顺便多要了
  // 已解决的」而被限流挤掉
  const recentAlertsQuery = useQuery({
    queryKey: ["alerts", "recent"],
    queryFn: ({ signal }) =>
      listAlerts({ signal, status: ALERT_STATUS_ALL, limit: RECENT_ALERTS_LIMIT }),
  });
  const metricsQuery = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });
  const servicesQuery = useQuery({
    queryKey: ["services"],
    queryFn: ({ signal }) => listServices({ signal }),
  });
  const auditQuery = useQuery({
    queryKey: ["audit", "recent"],
    queryFn: ({ signal }) => listAuditEvents({ signal, limit: RECENT_ACTIVITY_LIMIT }),
  });
  // 已放弃的后台任务（XM-WORKBENCH-JOBS）。只查 discarded：重试中的任务不需要
  // 人动手，见 lib/workbench 的 workItemsFromJobRuns。
  const discardedJobsQuery = useQuery({
    queryKey: ["jobs", "runs", "discarded", WORK_JOBS_LIMIT],
    queryFn: ({ signal }) => listJobRuns({ signal, state: "discarded", limit: WORK_JOBS_LIMIT }),
    retry: false,
  });
  // 待审批的动作（XM-WORKBENCH-APPROVALS）。
  //
  // queryKey 不与「操作与审批」页的审批队列共用：那边按状态筛选、缓存的是整条
  // 队列，这边固定 PENDING 且另有取数上限，共用会让两屏互相改写对方的缓存。
  // 但第一段仍是 "approvals"，所以那边投完票 invalidate ["approvals"] 时这一格
  // 跟着刷——投了一票回到工作台还看见旧的票数，人会以为票没投上去。
  //
  // retry: false 与后台任务那条同理：整组端点没挂载不会因为再试三次就挂上，
  // 而重试期间这一格一直停在加载态，把「未接入」拖成「好像很慢」。
  const approvalsQuery = useQuery({
    queryKey: ["approvals", "workbench", WORK_APPROVALS_LIMIT],
    queryFn: ({ signal }) =>
      listApprovals({ signal, status: "PENDING", limit: WORK_APPROVALS_LIMIT }),
    retry: false,
  });

  // 每条 query 各自独立：任何一条挂掉，别的格子照常显示。合成一条的话,
  // 指标端点 500 会把告警一起换成错误态——而那正是最需要看见告警的时候
  const refreshAll = () => {
    void alertsQuery.refetch();
    void recentAlertsQuery.refetch();
    void metricsQuery.refetch();
    void servicesQuery.refetch();
    void auditQuery.refetch();
    void discardedJobsQuery.refetch();
    void approvalsQuery.refetch();
  };
  useAutoRefresh(refreshAll);

  const alerts = alertsQuery.data?.items ?? [];
  const recent = recentAlertsQuery.data ?? [];
  const now = new Date();
  const failedJobs = discardedJobsQuery.data?.items ?? [];
  const pendingApprovals = approvalsQuery.data?.items ?? [];
  // 任务那条的权威判据是游标：还有下一页就是还有没显示的。
  const jobsTruncated = discardedJobsQuery.data?.nextBefore != null;

  return (
    <section>
      <PageHeader
        title={navLabel("/dashboard")}
        description={`集中查看需要我处理的事项、跨平台运营焦点和最近活动。每 ${OVERVIEW_POLL_INTERVAL_MS / 1000} 秒自动刷新（页面不可见时暂停）。各领域分开呈现，不合成总健康分。`}
        onRefresh={refreshAll}
        refreshing={alertsQuery.isFetching || metricsQuery.isFetching}
        lastRefreshedAt={alertsQuery.dataUpdatedAt || undefined}
      />

      <div className="flex flex-col gap-4">
        <TileRow
          alerts={alerts}
          recent={recent}
          approvals={pendingApprovals}
          approvalsPending={approvalsQuery.isPending}
          approvalsError={approvalsQuery.error}
          approvalsTruncated={approvalsQuery.data?.truncated === true}
          now={now}
          truncated={recent.length >= RECENT_ALERTS_LIMIT}
          pending={alertsQuery.isPending}
          error={alertsQuery.error}
          onRetry={() => void alertsQuery.refetch()}
        />

        <WorkList
          alerts={alerts}
          jobs={failedJobs}
          approvals={pendingApprovals}
          alertsTruncated={alertsQuery.data?.truncated === true}
          jobsTruncated={jobsTruncated}
          approvalsTruncated={approvalsQuery.data?.truncated === true}
          now={now}
          pending={alertsQuery.isPending}
          error={alertsQuery.error}
          // 后台任务是独立的一格：它读不到时只有「失败任务」那一类降级，
          // 告警照常显示。合成一个 error 会让 ops.read 缺权限时整块变红。
          jobsPending={discardedJobsQuery.isPending}
          jobsError={discardedJobsQuery.error}
          onJobsRetry={() => void discardedJobsQuery.refetch()}
          // 「待审批」同理，而且它多一种降级形态：整组端点没挂载时
          // listApprovals 会把裸 404 翻成 FeatureNotMountedError，ApiStateView
          // 据此显示「未接入」加那句说明，而不是「加载失败」加一个重试按钮
          // ——与「操作与审批」页的审批队列同一条路径，两处不另立一套说法。
          approvalsPending={approvalsQuery.isPending}
          approvalsError={approvalsQuery.error}
          onApprovalsRetry={() => void approvalsQuery.refetch()}
          onRetry={() => void alertsQuery.refetch()}
        />

        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <FocusCard
            alerts={alerts}
            pending={alertsQuery.isPending}
            error={alertsQuery.error}
            onRetry={() => void alertsQuery.refetch()}
          />
          <ActivityCard
            events={auditQuery.data?.items ?? []}
            pending={auditQuery.isPending}
            error={auditQuery.error}
            onRetry={() => void auditQuery.refetch()}
          />
        </div>

        <MatrixCard
          services={servicesQuery.data ?? []}
          metrics={metricsQuery.data ?? []}
          alerts={alerts}
          pending={servicesQuery.isPending || metricsQuery.isPending}
          error={servicesQuery.error ?? metricsQuery.error}
          onRetry={() => {
            void servicesQuery.refetch();
            void metricsQuery.refetch();
          }}
        />
      </div>
    </section>
  );
}

/** 卡片外壳。原型的每一块都是「标题 + 一句话 + 内容」，统一在这里。 */
function Card({
  title,
  hint,
  action,
  children,
}: {
  title: string;
  hint?: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="flex flex-col rounded-lg border border-edge bg-surface shadow-sm">
      <header className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-edge px-4 py-3">
        <h3 className="text-sm font-semibold text-fg">{title}</h3>
        {hint ? <p className="text-xs text-fg-muted">{hint}</p> : null}
        {action ? <div className="ml-auto shrink-0">{action}</div> : null}
      </header>
      <div className="p-4">{children}</div>
    </section>
  );
}

function TileRow({
  alerts,
  recent,
  approvals,
  approvalsPending,
  approvalsError,
  approvalsTruncated,
  now,
  truncated,
  pending,
  error,
  onRetry,
}: {
  alerts: AlertItem[];
  recent: AlertItem[];
  approvals: ApprovalItem[];
  approvalsPending: boolean;
  approvalsError: unknown;
  approvalsTruncated: boolean;
  now: Date;
  truncated: boolean;
  pending: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  const urgent = urgentCount(alerts);
  const recovered = recentlyRecoveredCount(recent, now);
  // 「审批到期」跟着**它自己那条 query** 降级：审批端点没挂载或没权限时显示
  // 「—」而不是 0——0 会被读成「没有快到期的单」，而事实是我们没读到。
  // 与 WorkList 里「待审批」那一类同一条纪律，不因为它在格子里就省掉。
  const approvalsUnavailable = Boolean(approvalsError) || approvalsPending;
  const dueSoon = approvalsDueSoonCount(approvals, now);

  return (
    <ApiStateView isPending={pending} error={error} onRetry={onRetry}>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          label="紧急"
          value={String(urgent)}
          note={urgent === 0 ? "当前没有未解决的严重告警" : "未解决的严重（critical）告警"}
          link={
            <Link to="/alerts" className="text-xs font-medium text-accent hover:underline">
              查看全部告警 →
            </Link>
          }
        />
        {/* 这一格原型里叫「今日到期」，数的是审批 + 重试 + 轮换到期三样，副行
            写着「随 Foundation-B 与后台任务页上线」——那两样今天都在了（审批中心
            XM-0030 已启用，/jobs 与 /jobs/runs 更早），所以那句话是错的。但三样
            合成一个数同样是错的：重试不是一条截止线（工作台只查已放弃的任务,
            它们已经不会再试），轮换到期则一个数都算不出来（凭据模型没有到期
            字段）。合计里少一样，得到的是一个恒偏低、又与正确值长得一样的数。
            所以收窄成「审批到期」——数一件有真正截止时刻的事，并且对它是完整的；
            另外两样各自在同一屏的「失败任务」与「即将到期」两类里说清楚。
            口径见 lib/workbench 的 approvalsDueSoonCount */}
        <StatTile
          label="审批到期"
          value={approvalsUnavailable ? "—" : String(dueSoon)}
          {...(approvalsUnavailable
            ? {
                unavailable: true,
                status: (
                  <Badge tone="neutral">{approvalsError ? "读不到" : "加载中"}</Badge>
                ),
              }
            : {})}
          note={
            approvalsError
              ? "审批队列读不到（端点未挂载或缺 approval.read），这一格不拿 0 冒充「没有快到期的单」"
              : approvalsPending
                ? "正在读审批队列"
                : `${APPROVAL_DUE_WINDOW_HOURS} 小时内到期的待审批单（已过期的不算，那些已经批不动了）${
                    approvalsTruncated ? `；取满 ${WORK_APPROVALS_LIMIT} 条上限，可能少算` : ""
                  }`
          }
          link={
            <Link to={APPROVAL_QUEUE_PATH} className="text-xs font-medium text-accent hover:underline">
              查看审批队列 →
            </Link>
          }
        />
        <StatTile
          label="阻塞"
          value="—"
          unavailable
          note="上游异常与退款冻结；随支付接入（M3）上线"
          status={<Badge tone="neutral">未接入</Badge>}
        />
        <StatTile
          label="最近恢复"
          value={String(recovered)}
          note={
            truncated
              ? `最近 ${RECOVERED_WINDOW_HOURS} 小时自动恢复(仅统计最近 ${RECENT_ALERTS_LIMIT} 条告警，可能少算)`
              : `最近 ${RECOVERED_WINDOW_HOURS} 小时自动恢复的告警`
          }
        />
      </div>
    </ApiStateView>
  );
}

/** 某一类事项的加载态 / 错误态：它跟着**这一类自己**那条 query 走。 */
interface WorkListSource {
  pending: boolean;
  error: unknown;
  onRetry: () => void;
}

/** 各分类"读到了，里面没有"时说的话。
 *
 *  逐类分开写而不共用一句「暂无数据」：空的是什么、为什么空、下一步该去哪，
 *  这三件事每一类都不一样，而「暂无数据」一件也没说。 */
const EMPTY_DESCRIPTIONS: Readonly<Record<string, string>> = {
  jobs: "当前没有已放弃的后台任务。重试中的任务不计入这里——它会自己再试，试到用尽才会出现。",
  approvals:
    "当前没有等着投票的动作。L2 及以上的调用才会在这里排队；L0/L1 直接执行，不经过审批。已过期的单也不算——它已经批不动了。",
};

/** 「全部」以及没有专属文案的分类用这一句。**必须点名还有哪几类没接**：
 *  一个「没有待处理事项」如果被读成「都处理完了」，人就会据此收工。 */
const DEFAULT_EMPTY_DESCRIPTION =
  "当前没有未解决的告警、没有等着投票的审批单，也没有失败的后台任务。注意：财务异常、到期项与待评审变更还没有接入，这一屏并不代表全部待办。";

function WorkList({
  alerts,
  jobs,
  approvals,
  alertsTruncated,
  jobsTruncated,
  approvalsTruncated,
  now,
  pending,
  error,
  onRetry,
  jobsPending,
  jobsError,
  onJobsRetry,
  approvalsPending,
  approvalsError,
  onApprovalsRetry,
}: {
  alerts: AlertItem[];
  jobs: JobRunItem[];
  approvals: ApprovalItem[];
  alertsTruncated: boolean;
  jobsTruncated: boolean;
  approvalsTruncated: boolean;
  now: Date;
  pending: boolean;
  error: unknown;
  onRetry: () => void;
  jobsPending: boolean;
  jobsError: unknown;
  onJobsRetry: () => void;
  approvalsPending: boolean;
  approvalsError: unknown;
  onApprovalsRetry: () => void;
}) {
  // 筛选进 Search Params：一个筛过的工作台是可以贴给同事的地址（交接文档 §8）
  const [searchParams, setSearchParams] = useSearchParams();
  const activeId = searchParams.get("work") ?? "";
  const activeCategory = WORK_CATEGORIES.find((c) => c.id === activeId);

  const select = (id: string) => {
    const next = new URLSearchParams(searchParams);
    if (id) next.set("work", id);
    else next.delete("work");
    setSearchParams(next, { replace: true });
  };

  // 三类事项各自派生再合并。「全部」视图里的先后本身就是一份排序建议：告警在
  // 最前（严重告警比什么都急），审批其次（它挂着别人的动作，而且会到期作废），
  // 失败任务最后（已经放弃了，晚半小时看不会更糟）。
  const items = [
    ...workItemsFromAlerts(alerts, now),
    ...workItemsFromApprovals(approvals, now),
    // truncated 这一位必须传进去：合并行显示的是「×20」，而生产上真实是 288。
    // 不带这一位，一个看起来权威的错数字比 288 行刷屏更危险。
    ...workItemsFromJobRuns(jobs, now, { truncated: jobsTruncated }),
  ];
  const shown = activeId ? items.filter((item) => item.categoryId === activeId) : items;

  // 加载态与错误态跟着**当前这一格自己**那条 query 走。写成按分类查表，而不是
  // 一串 `a ? … : b ? … : …`：三选一还勉强读得懂，第四类进来就没人敢动了，而
  // 这张表加一行就够。
  //
  // 表里查不到（含「全部」）落到告警那一条：「全部」以告警为主，另外两条各自
  // 失败时只是少几行，不该把整块换成错误态——那正是最需要看见告警的时候。
  const sourceByCategory: Readonly<Record<string, WorkListSource>> = {
    jobs: { pending: jobsPending, error: jobsError, onRetry: onJobsRetry },
    approvals: { pending: approvalsPending, error: approvalsError, onRetry: onApprovalsRetry },
  };
  const source: WorkListSource = sourceByCategory[activeId] ?? { pending, error, onRetry };

  // 取满上限时说出来（XM-WORKBENCH-TRUNCATION）。只在**当前这一格**真的可能
  // 被截断时才说——在「财务异常」下面提"告警取了 200 条"是噪声。
  const truncation = truncationNote({
    activeCategoryId: activeId,
    alertsTruncated,
    jobsTruncated,
    approvalsTruncated,
  });

  return (
    <Card title="我的待处理" hint="审批、故障、任务、财务、到期与变更">
      <div role="group" aria-label="待处理事项筛选" className="mb-3 flex flex-wrap gap-2">
        <FilterChip label="全部" active={activeId === ""} onClick={() => select("")} />
        {WORK_CATEGORIES.map((category) => (
          <FilterChip
            key={category.id}
            label={category.label}
            active={activeId === category.id}
            onClick={() => select(category.id)}
          />
        ))}
      </div>

      {/* 这一句必须在：今天「故障」一类里躺的其实是活跃告警，故障事件（Incident）
          对象还没建。不说的话，人会以为这些已经是收敛过的故障单 */}
      <p className="mb-3 text-xs text-fg-muted">
        本阶段有数据源的是「故障」「待审批」与「失败任务」三类：「故障」的内容是活跃告警——故障事件（Incident）对象随治理段切片建立后再单列；
        「待审批」是审批中心里仍等着投票的单，最多 {WORK_APPROVALS_LIMIT} 条，已过期的不算——它已经批不动了；
        「失败任务」只收已放弃（重试用尽、不会再跑）的后台任务，最多 {WORK_JOBS_LIMIT} 条，
        同一类型的已放弃作业合并成一行显示计数（点标题展开明细），所以计数只统计这最多{" "}
        {WORK_JOBS_LIMIT} 条、不是该类型的全部；条数后带「+」就表示还有没取到的。
        重试中的任务不在这里，它不需要人动手，看过程请去后台任务页的「失败与重试」。
        其余各类的空是「还没接」，不是「没有问题」。
      </p>

      {/* 没有数据源的分类不进 ApiStateView：它的空与这次请求成不成功无关。
          套在里面的话，后端一挂就会显示「加载失败」——把一个「还没接」的事实
          说成一次网络故障，人会去点重试，而重试一万次也不会有内容 */}
      {activeCategory?.blockedBy ? (
        <EmptyState
          title={`「${activeCategory.label}」还没有数据源`}
          description={activeCategory.blockedBy}
        />
      ) : (
        <ApiStateView isPending={source.pending} error={source.error} onRetry={source.onRetry}>
          {truncation ? (
            <p role="status" className="mb-2 text-xs text-warning">
              {truncation}
            </p>
          ) : null}
          {shown.length === 0 ? (
            <EmptyState
              title="没有待处理事项"
              description={EMPTY_DESCRIPTIONS[activeId] ?? DEFAULT_EMPTY_DESCRIPTION}
            />
          ) : (
            <ul className="flex flex-col gap-2">
              {shown.map((item) => (
                <WorkRow key={item.id} item={item} />
              ))}
            </ul>
          )}
        </ApiStateView>
      )}
    </Card>
  );
}

function FilterChip({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      // aria-pressed 而不是把选中态只画成颜色：读屏用户要能听出哪一个是当前筛选
      aria-pressed={active}
      onClick={onClick}
      className={
        active
          ? "rounded-md border border-accent bg-accent-soft px-2 py-1 text-xs font-medium text-accent"
          : "rounded-md border border-edge px-2 py-1 text-xs text-fg-muted hover:bg-surface-muted"
      }
    >
      {label}
    </button>
  );
}

function WorkRow({ item }: { item: WorkItem }) {
  // 合并行与单条行是两种交互，分成两个组件而不是在一个里面判：hook 不能条件
  // 调用，而合并行需要自己的展开状态。
  if (item.children) return <MergedWorkRow item={item} />;
  return (
    <li>
      <Link
        to={item.to}
        title={item.hint}
        className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-edge px-3 py-2 hover:bg-surface-muted"
      >
        <Badge tone={item.tone}>{item.categoryLabel}</Badge>
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-fg">{item.title}</span>
        <span className="truncate text-xs text-fg-muted">{item.meta}</span>
        <span className="shrink-0 text-xs text-fg-muted tabular-nums">{item.due}</span>
      </Link>
    </li>
  );
}

/** 合并成一行的已放弃作业（同一个 job kind）。
 *
 *  **展开与跳转是两个可点区域**：整行套一个 `<Link>` 再往里塞一个 `<button>`
 *  是嵌套交互控件，读屏与键盘都会失灵。
 *
 *  明细用**条件渲染**，不用 `<details>`、也不用 CSS 隐藏：折叠时 `<details>`
 *  的内容仍然留在 DOM 里，testing-library 照样查得到，于是「折叠时看不到明细」
 *  那条断言会恒真——本仓在 Portal / 懒渲染上踩过同一个坑。 */
function MergedWorkRow({ item }: { item: WorkItem }) {
  const [open, setOpen] = useState(false);
  const detailId = useId();
  const children = item.children ?? [];
  return (
    <li>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-edge px-3 py-2">
        <Badge tone={item.tone}>{item.categoryLabel}</Badge>
        <button
          type="button"
          aria-expanded={open}
          aria-controls={detailId}
          onClick={() => setOpen((v) => !v)}
          className="min-w-0 flex-1 truncate text-left text-sm font-medium text-fg hover:underline"
        >
          {item.title}
          <span className="ml-2 text-xs font-normal text-fg-muted">
            {open ? "收起明细" : "展开明细"}
          </span>
        </button>
        <span className="truncate text-xs text-fg-muted">{item.meta}</span>
        <span className="shrink-0 text-xs text-fg-muted tabular-nums">{item.due}</span>
        <Link to={item.to} className="shrink-0 text-xs text-accent hover:underline">
          去后台任务页
        </Link>
      </div>
      {open ? (
        <ul id={detailId} className="mt-1 flex flex-col gap-1 pl-6">
          {children.map((child) => (
            <WorkRow key={child.id} item={child} />
          ))}
        </ul>
      ) : null}
    </li>
  );
}

function FocusCard({
  alerts,
  pending,
  error,
  onRetry,
}: {
  alerts: AlertItem[];
  pending: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  return (
    <Card title="运营焦点" hint="按领域分开呈现，不合成总健康分">
      <ApiStateView isPending={pending} error={error} onRetry={onRetry}>
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-edge text-left text-xs text-fg-muted">
              <th className="pb-2 font-medium">领域</th>
              <th className="pb-2 font-medium">状态</th>
              <th className="pb-2 text-right font-medium">待处理</th>
            </tr>
          </thead>
          <tbody>
            {focusRows(alerts).map((row) => (
              <tr key={row.domain} className="border-b border-edge last:border-0">
                <td className="py-2 text-fg">
                  {row.domain}
                  <span className="block text-xs text-fg-muted">{row.detail}</span>
                </td>
                <td className="py-2">
                  <Badge tone={row.tone}>{row.state}</Badge>
                </td>
                <td className="py-2 text-right tabular-nums text-fg">
                  {row.count === undefined ? <span className="text-fg-muted">—</span> : row.count}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </ApiStateView>
    </Card>
  );
}

function ActivityCard({
  events,
  pending,
  error,
  onRetry,
}: {
  events: AuditEventItem[];
  pending: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  return (
    <Card
      title="最近活动"
      hint="来自审计记录"
      action={
        <Link to="/audit" className="text-xs font-medium text-accent hover:underline">
          查看审计记录 →
        </Link>
      }
    >
      <ApiStateView isPending={pending} error={error} onRetry={onRetry}>
        {events.length === 0 ? (
          <EmptyState
            title="还没有审计事件"
            description="执行一次动作之后，这里会显示最近的几条"
          />
        ) : (
          <ul className="flex flex-col gap-2">
            {events.map((event) => (
              <li key={event.sequence} className="border-b border-edge pb-2 last:border-0 last:pb-0">
                <p className="truncate text-sm font-medium text-fg">
                  {event.action_id}@{event.action_version}
                </p>
                <p className="truncate text-xs text-fg-muted">
                  {event.resource_type}/{event.resource_id} · {event.principal_id} ·{" "}
                  {formatUtcTimestamp(event.occurred_at)}
                </p>
              </li>
            ))}
          </ul>
        )}
      </ApiStateView>
    </Card>
  );
}

function MatrixCard({
  services,
  metrics,
  alerts,
  pending,
  error,
  onRetry,
}: {
  services: ServiceItem[];
  metrics: MetricItem[];
  alerts: AlertItem[];
  pending: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  return (
    <Card title="平台状态矩阵" hint="点击进入对应平台或治理页面">
      <ApiStateView isPending={pending} error={error} onRetry={onRetry}>
        <div className="overflow-x-auto">
          <table className="w-full min-w-160 text-sm">
            <thead>
              <tr className="border-b border-edge text-left text-xs text-fg-muted">
                <th className="pb-2 font-medium">平台 / 系统</th>
                <th className="pb-2 font-medium">接入阶段</th>
                <th className="pb-2 font-medium">关键状态</th>
                <th className="pb-2 font-medium">数据新鲜度</th>
                <th className="pb-2 text-right font-medium">活动事件</th>
                <th className="pb-2 font-medium">最近观测</th>
              </tr>
            </thead>
            <tbody>
              {platformMatrixRows({ services, metrics, alerts }).map((row) => (
                <MatrixTableRow key={row.key} row={row} />
              ))}
            </tbody>
          </table>
        </div>
      </ApiStateView>
    </Card>
  );
}

function MatrixTableRow({ row }: { row: MatrixRow }) {
  return (
    <tr className="border-b border-edge last:border-0">
      <td className="py-2">
        <Link to={row.to} className="font-medium text-accent hover:underline">
          {row.label}
        </Link>
        {/* 这一行说的是什么范围。今天只有开票那一行挂：它写着「未接入」，而
            点进去是一个能用的嵌入管理端，两者不是一回事——这句话必须在格子
            里，不能只靠 hover。 */}
        {row.scopeNote ? (
          <span className="mt-1 block max-w-80 text-xs font-normal text-fg-muted">
            {row.scopeNote}
          </span>
        ) : null}
      </td>
      <td className="py-2 text-xs text-fg-muted">{row.stage}</td>
      <td className="py-2">
        <Badge tone={row.statusTone}>{row.statusLabel}</Badge>
      </td>
      <td className="py-2">
        <FreshnessBadge freshness={row.freshness} />
        {/* 「数据新鲜度必须可见」在这一屏上原来只剩四个字的徽章：NewAPI 那格
            的黄灯亮了两周，没人说得出为什么。原因由 workbench 从数据里得出。 */}
        {row.freshnessNote ? (
          <span
            className="mt-1 block max-w-80 text-xs text-fg-muted"
            title={row.freshnessEvidence}
          >
            {row.freshnessNote}
          </span>
        ) : null}
      </td>
      <td className="py-2 text-right tabular-nums text-fg">{row.events}</td>
      <td className="py-2 text-xs text-fg-muted tabular-nums">
        {formatUtcTimestamp(row.observedAt)}
      </td>
    </tr>
  );
}
