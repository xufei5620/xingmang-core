import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  formatDuration,
  formatUtcTimestamp,
  navItemByPath,
  PageHeader,
  PageState,
  StatTile,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge, Button, Tabs, type BadgeTone } from "@xingmang/ui-primitives";
import { useSearchParams } from "react-router";
import {
  JOB_RUNS_PAGE_SIZE,
  SYNC_JOB_KINDS,
  getJobsOverview,
  jobKindLabel,
  jobStateLabel,
  listJobRuns,
  type JobQueueBacklogRow,
  type JobRunItem,
  type JobRunState,
  type JobScheduleStatus,
  type JobsOverview,
} from "../api/jobs";
import { ApiStateView } from "../components/ApiStateView";
import { OVERVIEW_POLL_INTERVAL_MS, useAutoRefresh } from "../lib/autoRefresh";
import { NotFoundView } from "./NotFoundPage";

/** react-query 缓存键：概览快照页面级共用一份（顶部四格与「定时任务」页签
 *  各自 useQuery 同一个 key，Radix 只挂载当前页签内容，实际请求只发一次）。 */
export const JOBS_OVERVIEW_QUERY_KEY = ["jobs-overview"];

/** 后台任务页（ADMIN-IA §2.2 `g/jobs`，XM-JOBS0）。
 *
 *  数据来自 river_job（周期任务清单 + 队列积压 + worker 心跳）与其分页运行
 *  记录；两个只读端点都需要 ops.read。**这批数据是平台级、跨环境共享的**：
 *  六个已注册的周期任务（心跳/两个数据连接器同步/成本采集/告警评估/保留期
 *  清理）目前都不按 environment 拆分，staging 与 production 读到的是同一个
 *  river_job 表（后端 jobs.QueryStore 的文件头注释）。这不是缺陷，是如实反映
 *  ——伪造一条环境隔离会比不隔离更危险。 */
export function JobsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const hit = navItemByPath("/jobs");

  // 导航是本页签集合的唯一来源；若导航被改坏，显示 Not Found 比悄悄画一套
  // 漂移的页签更诚实。
  if (!hit) return <NotFoundView pathname="/jobs" detail="后台任务没有登记在导航中" />;

  const tabs = hit.item.subTabs;
  const rawSub = searchParams.get("sub");
  const active = rawSub === null || rawSub === "" ? (tabs[0]?.id ?? "running") : rawSub;
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

  const overviewQuery = useQuery({
    queryKey: JOBS_OVERVIEW_QUERY_KEY,
    queryFn: ({ signal }) => getJobsOverview({ signal }),
  });
  const refreshOverview = () => void overviewQuery.refetch();
  useAutoRefresh(refreshOverview);

  return (
    <section>
      <PageHeader
        title={hit.item.label}
        description={`周期任务目录、队列积压与 worker 心跳来自 river_job；平台级共享，不按环境拆分（见下方说明）。写操作（暂停、取消、立即执行、重放死信）不在本页——River 任务的执行仍由 Platform Worker 独立完成。每 ${OVERVIEW_POLL_INTERVAL_MS / 1000} 秒自动刷新一次（页面不可见时暂停）。`}
        onRefresh={refreshOverview}
        refreshing={overviewQuery.isFetching}
        lastRefreshedAt={overviewQuery.dataUpdatedAt || undefined}
      />

      <ApiStateView isPending={overviewQuery.isPending} error={overviewQuery.error} onRetry={refreshOverview} compact>
        {overviewQuery.data ? <JobsTiles overview={overviewQuery.data} /> : null}
      </ApiStateView>

      <div className="mt-4">
        <Tabs
          value={active}
          onValueChange={selectSub}
          className="gap-4"
          items={tabs.map((tab) => ({
            value: tab.id,
            label: tab.label,
            content: <JobsTabPanel tabId={tab.id} label={tab.label} />,
          }))}
        />
      </div>
    </section>
  );
}

// --- 顶部四格 -------------------------------------------------------------

function sumBacklog(rows: JobQueueBacklogRow[], field: keyof Omit<JobQueueBacklogRow, "queue">): number {
  return rows.reduce((total, row) => total + row[field], 0);
}

function JobsTiles({ overview }: { overview: JobsOverview }) {
  const running = sumBacklog(overview.queue_backlog, "running");
  const retryable = sumBacklog(overview.queue_backlog, "retryable");
  const discarded = sumBacklog(overview.queue_backlog, "discarded");
  const heartbeat = overview.worker_heartbeat;

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          label="运行中任务"
          value={String(running)}
          note="全部队列里状态为「运行中」的任务数"
        />
        <StatTile
          label="Worker 心跳"
          value={heartbeat.known && heartbeat.seconds_ago !== null ? `${formatDuration(heartbeat.seconds_ago)}前` : "—"}
          unavailable={!heartbeat.known}
          note={
            heartbeat.known
              ? `最近一次 ${formatUtcTimestamp(heartbeat.last_seen_at)}`
              : "river_job 里还没有出现过心跳记录"
          }
        />
        <StatTile
          label="失败待重试"
          value={String(retryable)}
          note="状态为「等待重试」，River 会按退避策略自动重试"
        />
        <StatTile
          label="多次失败任务"
          value={String(discarded)}
          note={discarded > 0 ? "已达最大重试次数并放弃，需要人工检查" : "已达最大重试次数并放弃的任务"}
        />
      </div>
      <QueueBacklogTable rows={overview.queue_backlog} />
    </div>
  );
}

function QueueBacklogTable({ rows }: { rows: JobQueueBacklogRow[] }) {
  return (
    <section className="rounded-lg border border-edge bg-surface shadow-sm">
      <header className="border-b border-edge px-4 py-3">
        <h3 className="text-sm font-semibold text-fg">队列积压</h3>
        <p className="mt-1 text-xs text-fg-muted">
          按队列汇总各状态计数；「已完成」只统计最近 24 小时——River 自带的清理任务会删掉更早的记录，统计更早的窗口只会读到一个被清理策略截断的数字。
        </p>
      </header>
      <div className="overflow-x-auto p-4">
        <table className="w-full min-w-160 text-sm">
          <caption className="sr-only">各队列在各状态下的任务数</caption>
          <thead>
            <tr className="border-b border-edge text-left text-xs text-fg-muted">
              <th scope="col" className="pb-2 font-medium">队列</th>
              <th scope="col" className="pb-2 font-medium">待执行</th>
              <th scope="col" className="pb-2 font-medium">运行中</th>
              <th scope="col" className="pb-2 font-medium">等待重试</th>
              <th scope="col" className="pb-2 font-medium">计划中</th>
              <th scope="col" className="pb-2 font-medium">已完成（24h）</th>
              <th scope="col" className="pb-2 font-medium">多次失败</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.queue} className="border-b border-edge last:border-0">
                <th scope="row" className="py-2 text-left font-mono font-medium text-fg">{row.queue}</th>
                <td className="py-2 tabular-nums text-fg">{row.available}</td>
                <td className="py-2 tabular-nums text-fg">{row.running}</td>
                <td className="py-2 tabular-nums text-fg">{row.retryable}</td>
                <td className="py-2 tabular-nums text-fg">{row.scheduled}</td>
                <td className="py-2 tabular-nums text-fg">{row.completed_24h}</td>
                <td className="py-2 tabular-nums text-fg">{row.discarded}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

// --- 子页签内容 -------------------------------------------------------------

/** 五个子页签（ADMIN-IA §2.2 逐字：运行中/定时任务/同步批次/失败与重试/
 *  多次失败任务）里，只有「定时任务」直接读概览快照；其余四个都是
 *  /jobs/runs 的不同筛选视图，共用同一套表格与「加载更多」交互。 */
function JobsTabPanel({ tabId, label }: { tabId: string; label: string }) {
  switch (tabId) {
    case "scheduled":
      return <ScheduledTab label={label} />;
    case "batches":
      return (
        <RunsTab
          label={label}
          description="Sub2API / NewAPI 数据连接器同步与成本采集三类任务的最近记录。"
          kinds={SYNC_JOB_KINDS}
          emptyTitle="暂无同步批次记录"
          emptyDescription="没有数据不代表同步已停止——请到「定时任务」页签确认这三个任务最近有没有活跃观测。"
        />
      );
    case "failures":
      return (
        <RunsTab
          label={label}
          description="状态为「等待重试」的任务：River 会按退避策略自动重试，本页不提供手工重试入口。"
          state="retryable"
          emptyTitle="当前没有等待重试的任务"
          emptyDescription="这是好消息，但请确认采集与告警评估任务在跑——一个卡住的 worker 同样显示为零条失败。"
        />
      );
    case "repeated":
      return (
        <RunsTab
          label={label}
          description="状态为「多次失败（已放弃）」的任务：已达最大重试次数，River 不会再自动重试，需要人工检查原因。"
          state="discarded"
          emptyTitle="当前没有多次失败的任务"
          emptyDescription="没有数据不代表一切正常——请结合上方「Worker 心跳」与「失败待重试」一起看。"
        />
      );
    case "running":
    default:
      return (
        <RunsTab
          label={label}
          description="全部队列最近的运行记录，按 id 倒序（最新在前）；可用下方「类型」「状态」筛选当前已加载的记录。"
          emptyTitle="暂无最近运行记录"
          emptyDescription="没有数据不代表任务系统正常——请确认 Platform Worker 进程在跑（参考上方「Worker 心跳」）。"
        />
      );
  }
}

function ScheduledTab({ label }: { label: string }) {
  const query = useQuery({
    queryKey: JOBS_OVERVIEW_QUERY_KEY,
    queryFn: ({ signal }) => getJobsOverview({ signal }),
  });
  const refresh = () => void query.refetch();

  return (
    <section>
      <PageHeader
        title={label}
        description="平台注册的周期任务目录；「观测周期」「预计下次」「活跃度」是从 river_job 里最近的运行记录推算出来的观测值，不是配置值。「部署状态」是另一件事：Platform API 进程按与 Platform Worker 相同的规则解析同一份部署环境变量（并对 Sub2API/NewAPI 两个同步任务读取 core.connector_config 的实际模式）得到的部署声明——不是对 Worker 进程正在跑什么的实时确认，两者在正常部署下应当一致，但这是两件不同的事。"
      />
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={refresh} compact>
        <ScheduledTable schedules={query.data?.schedules ?? []} />
      </ApiStateView>
    </section>
  );
}

const ACTIVITY_DISPLAY: Record<JobScheduleStatus["activity"], { label: string; tone: BadgeTone }> = {
  activity_observed: { label: "近期活跃", tone: "success" },
  no_recent_activity: { label: "长时间无活动", tone: "warning" },
  never_observed: { label: "从未观测到", tone: "neutral" },
};

function scheduledColumns(): DataTableColumn<JobScheduleStatus>[] {
  return [
    {
      id: "kind",
      header: "任务",
      primary: true,
      value: (s) => `${jobKindLabel(s.kind)} ${s.kind}`,
      cell: (s) => (
        <>
          <span className="font-medium">{jobKindLabel(s.kind)}</span>
          <p className="mt-0.5 font-mono text-xs text-fg-muted">{s.kind}</p>
        </>
      ),
    },
    { id: "queue", header: "队列", value: (s) => s.queue, cell: (s) => <span className="font-mono text-xs">{s.queue}</span> },
    {
      id: "scheduleConfig",
      header: "周期配置",
      value: (s) => s.schedule_config_env,
      cell: (s) => <span className="font-mono text-xs text-fg-muted">{s.schedule_config_env}</span>,
    },
    {
      id: "lastRun",
      header: "最近一次",
      value: (s) => s.last_run?.created_at ?? "",
      cell: (s) =>
        s.last_run ? (
          <>
            <RunStateBadge state={s.last_run.state} />
            <p className="mt-1 text-xs text-fg-muted">{formatUtcTimestamp(s.last_run.created_at)}</p>
          </>
        ) : (
          <span className="text-fg-muted">—</span>
        ),
    },
    {
      id: "interval",
      header: "观测周期",
      value: (s) => s.observed_interval_seconds ?? -1,
      cell: (s) =>
        s.observed_interval_seconds !== null ? (
          <span>{formatDuration(s.observed_interval_seconds)}</span>
        ) : (
          <span className="text-fg-muted" title="观测不足两次，暂时算不出周期">—</span>
        ),
    },
    {
      id: "nextRun",
      header: "预计下次",
      value: (s) => s.next_run_estimated_at ?? "",
      cell: (s) => <span>{s.next_run_estimated_at ? formatUtcTimestamp(s.next_run_estimated_at) : "—"}</span>,
    },
    {
      id: "activity",
      header: "活跃度",
      value: (s) => ACTIVITY_DISPLAY[s.activity].label,
      cell: (s) => {
        const shown = ACTIVITY_DISPLAY[s.activity];
        return <Badge tone={shown.tone}>{shown.label}</Badge>;
      },
    },
    {
      id: "configured",
      header: "部署状态",
      value: (s) => `${s.configured_enabled ?? ""} ${s.configured_mode ?? ""}`,
      cell: (s) => <ConfiguredCell schedule={s} />,
    },
    {
      id: "lastError",
      header: "最近错误",
      value: (s) => s.last_run?.last_error?.message ?? "",
      cell: (s) =>
        s.last_run?.last_error ? (
          <span className="break-all text-xs text-danger">{s.last_run.last_error.message}</span>
        ) : (
          <span className="text-fg-muted">—</span>
        ),
    },
  ];
}

const CONFIGURED_MODE_DISPLAY: Record<"real" | "fake", { label: string; tone: BadgeTone }> = {
  real: { label: "真实接入", tone: "success" },
  fake: { label: "演示数据", tone: "neutral" },
};

/** 「部署状态」列：configured_enabled/configured_mode 两件事分两行摆——
 *  「启用/停用」对全部任务有意义，「真实接入/演示数据」只对 Sub2API/NewAPI
 *  两个同步任务有意义，混进同一句话容易让人以为其它任务也有真实/演示之分。
 *  两者都明确标着来源（环境变量名 / database·default·unavailable），
 *  不让"部署声明"读起来像"Worker 已确认"。 */
function ConfiguredCell({ schedule }: { schedule: JobScheduleStatus }) {
  // typeof 判断而不是 === null：真实后端响应里这个字段恒为显式 boolean
  // 或 null（Go 侧没有加 omitempty），但顺带也接住字段整个缺失
  // （undefined）的情形，不因为一次边界写法差异就崩成白屏。
  if (typeof schedule.configured_enabled !== "boolean") {
    return <span className="text-fg-muted" title="本次装配没有接部署配置读数">—</span>;
  }
  const modeShown = schedule.configured_mode ? CONFIGURED_MODE_DISPLAY[schedule.configured_mode] : null;
  return (
    <div className="flex flex-col items-start gap-1">
      <Badge tone={schedule.configured_enabled ? "success" : "neutral"}>
        {schedule.configured_enabled ? "已配置启用" : "已配置停用"}
      </Badge>
      {modeShown ? (
        <Badge tone={modeShown.tone}>{modeShown.label}</Badge>
      ) : schedule.configured_mode_source === "unavailable" ? (
        <span className="text-xs text-danger">connector_config 读取失败</span>
      ) : null}
      <span className="text-xs text-fg-muted" title="决定「已配置启用/停用」的环境变量">
        {schedule.configured_source}
      </span>
    </div>
  );
}

function ScheduledTable({ schedules }: { schedules: JobScheduleStatus[] }) {
  return (
    <DataTableV2
      caption="周期任务目录：队列、最近一次运行、观测周期与活跃度"
      columns={scheduledColumns()}
      rows={schedules}
      rowKey={(s) => s.id}
      searchable
      emptyState={
        <PageState
          kind="empty"
          title="没有已注册的周期任务"
          description="这与正常情况不符——平台至少注册了心跳任务，若为空请检查后端 jobs.RegisteredPeriodicJobSpecs 是否正常返回。"
        />
      }
    />
  );
}

// --- 运行记录（运行中 / 同步批次 / 失败与重试 / 多次失败任务共用） ----------

const RUN_STATE_DISPLAY: Record<JobRunState, BadgeTone> = {
  available: "info",
  running: "info",
  retryable: "warning",
  scheduled: "neutral",
  completed: "success",
  discarded: "danger",
  cancelled: "neutral",
};

function RunStateBadge({ state }: { state: JobRunState }) {
  return <Badge tone={RUN_STATE_DISPLAY[state] ?? "warning"}>{jobStateLabel(state)}</Badge>;
}

function formatDurationMs(ms: number | null): string {
  if (ms === null) return "—";
  if (ms < 1000) return `${ms} ms`;
  return formatDuration(Math.round(ms / 1000));
}

/** args 白名单里挑几个人认识的字段拼成一行小字；不认识的键原样带 key= 前缀，
 *  不猜测它的含义。 */
function formatArgs(args: Record<string, unknown>): string | null {
  const entries = Object.entries(args);
  if (entries.length === 0) return null;
  return entries.map(([key, value]) => `${key}=${String(value)}`).join(" · ");
}

function runColumns(): DataTableColumn<JobRunItem>[] {
  return [
    {
      id: "kind",
      header: "任务",
      primary: true,
      value: (r) => `${jobKindLabel(r.kind)} ${r.kind} ${r.id}`,
      cell: (r) => (
        <>
          <span className="font-medium">{jobKindLabel(r.kind)}</span>
          <p className="mt-0.5 font-mono text-xs text-fg-muted">
            #{r.id} · {r.queue}
          </p>
        </>
      ),
    },
    {
      id: "state",
      header: "状态",
      value: (r) => jobStateLabel(r.state),
      cell: (r) => (
        <>
          <RunStateBadge state={r.state} />
          <p className="mt-1 text-xs text-fg-muted">尝试 {r.attempt} / {r.max_attempts}</p>
        </>
      ),
    },
    {
      id: "created",
      header: "创建 / 开始",
      value: (r) => r.created_at,
      cell: (r) => (
        <>
          <span className="block text-xs text-fg-muted">创建 {formatUtcTimestamp(r.created_at)}</span>
          {r.attempted_at ? (
            <span className="block text-xs text-fg-muted">开始 {formatUtcTimestamp(r.attempted_at)}</span>
          ) : null}
        </>
      ),
    },
    {
      id: "finalized",
      header: "结束",
      value: (r) => r.finalized_at ?? "",
      cell: (r) => <span>{r.finalized_at ? formatUtcTimestamp(r.finalized_at) : "—"}</span>,
    },
    {
      id: "duration",
      header: "耗时",
      numeric: true,
      value: (r) => r.duration_ms ?? -1,
      cell: (r) => <span>{formatDurationMs(r.duration_ms)}</span>,
    },
    {
      id: "error",
      header: "错误",
      value: (r) => r.last_error?.message ?? "",
      cell: (r) =>
        r.last_error ? (
          <>
            <span className="break-all text-xs text-danger">{r.last_error.message}</span>
            {r.error_count > 1 ? (
              <p className="mt-0.5 text-xs text-fg-muted">共 {r.error_count} 次失败尝试</p>
            ) : null}
          </>
        ) : (
          <span className="text-fg-muted">—</span>
        ),
    },
    {
      id: "args",
      header: "参数",
      value: (r) => formatArgs(r.args) ?? "",
      cell: (r) => {
        const shown = formatArgs(r.args);
        return shown ? (
          <span className="break-all font-mono text-xs text-fg-muted">{shown}</span>
        ) : (
          <span className="text-fg-muted">—</span>
        );
      },
    },
  ];
}

const RUN_STATE_FILTER_OPTIONS = [
  "待执行",
  "运行中",
  "等待重试",
  "计划中",
  "已完成",
  "多次失败（已放弃）",
  "已取消",
];

interface RunsTabProps {
  label: string;
  description: string;
  /** 固定的服务端 kind 过滤（如「同步批次」的三个数据连接器同步任务）；
   *  不传 = 不筛类型（「运行中」页签展示全部）。用户不能在页面上改它——
   *  这是页签本身的定义，不是一个可调的筛选项。 */
  kinds?: readonly string[];
  /** 固定的服务端 state 过滤（如「失败与重试」= retryable）；不传 = 不筛状态。 */
  state?: JobRunState;
  emptyTitle: string;
  emptyDescription: string;
}

function runsQueryKey(kinds: readonly string[] | undefined, state: JobRunState | undefined) {
  return ["jobs-runs", (kinds ?? []).join(","), state ?? ""] as const;
}

function RunsTab({ label, description, kinds, state, emptyTitle, emptyDescription }: RunsTabProps) {
  const query = useInfiniteQuery({
    queryKey: runsQueryKey(kinds, state),
    queryFn: ({ signal, pageParam }) =>
      listJobRuns({
        signal,
        ...(kinds ? { kinds } : {}),
        ...(state ? { state } : {}),
        limit: JOB_RUNS_PAGE_SIZE,
        ...(pageParam === undefined ? {} : { before: pageParam }),
      }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => last.nextBefore ?? undefined,
  });
  const refresh = () => void query.refetch();
  const items = (query.data?.pages ?? []).flatMap((p) => p.items);

  return (
    <section>
      <PageHeader
        title={label}
        description={description}
        onRefresh={refresh}
        refreshing={query.isFetching && !query.isFetchingNextPage}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
      />
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={refresh} compact>
        <RunsTable
          items={items}
          emptyTitle={emptyTitle}
          emptyDescription={emptyDescription}
          hasMore={query.hasNextPage}
          loadingMore={query.isFetchingNextPage}
          onLoadMore={() => void query.fetchNextPage()}
        />
      </ApiStateView>
    </section>
  );
}

function RunsTable({
  items,
  emptyTitle,
  emptyDescription,
  hasMore,
  loadingMore,
  onLoadMore,
}: {
  items: JobRunItem[];
  emptyTitle: string;
  emptyDescription: string;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
}) {
  return (
    <DataTableV2
      caption="任务运行记录：类型、状态、时间、耗时与错误"
      columns={runColumns()}
      rows={items}
      rowKey={(r) => String(r.id)}
      // 不传 pageSize：这一页自己在按游标翻（「加载更多」），再叠一层客户端
      // 分页，人要点两种「下一页」而它们翻的不是同一批东西（同 AuditPage）。
      searchable
      filters={[
        { columnId: "state", label: "状态", options: RUN_STATE_FILTER_OPTIONS },
      ]}
      footerExtra={
        hasMore ? (
          <Button variant="secondary" size="sm" onClick={onLoadMore} loading={loadingMore}>
            加载更多
          </Button>
        ) : items.length > 0 ? (
          <span>已到最早一条</span>
        ) : null
      }
      emptyState={<PageState kind="empty" title={emptyTitle} description={emptyDescription} />}
    />
  );
}
