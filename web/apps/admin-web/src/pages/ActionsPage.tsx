import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  formatLocalTimestamp,
  navItemByPath,
  navLabel,
  PageHeader,
  PageState,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge, Button, Input, Select, Tabs, type BadgeTone } from "@xingmang/ui-primitives";
import { useState, type ReactNode } from "react";
import { useSearchParams } from "react-router";
import {
  ACTION_RUN_PAGE_SIZE,
  getActionRun,
  listActionDefinitions,
  listActionRuns,
  type ActionDefinitionItem,
  type ActionRunDetail,
  type ActionRunItem,
  type ActionRunStatusFilter,
} from "../api/actions";
import { ApiStateView } from "../components/ApiStateView";
import { ApprovalQueue } from "../components/ApprovalQueue";

const ACTIONS_SUB_TABS = (navItemByPath("/actions")?.item.subTabs ?? []).map(
  (tab) => [tab.id, tab.label] as const,
);
const DEFAULT_SUB_TAB = "catalog";

const RISK_LEVEL_TONE: Readonly<Record<string, BadgeTone>> = {
  L0: "neutral",
  L1: "info",
  L2: "warning",
  L3: "warning",
  L4: "danger",
};

function riskTone(level: string): BadgeTone {
  return RISK_LEVEL_TONE[level] ?? "neutral";
}

/** 操作与审批页（ADMIN-IA §2.2 `g/actions`：操作目录 / 待审批 / 执行记录 /
 *  风险与启用条件）。
 *
 *  结构照 pages/AuditPage.tsx、pages/IdentityPage.tsx 的子页签模式：外层只判
 *  「认不认识这个 ?sub=」，认识就交给 Tabs，每个子页自己决定渲染什么。
 *
 *  与那两页的一处不同：这里多了一条**页面级**门禁横幅（AdvancedControlsGate），
 *  挂在 Tabs 外面——ADMIN-IA §七要求「F-B 未完成前，操作与审批页必须显示
 *  门禁」，这是整页的事实，不是某一个子页签的局部状态，所以不能只塞进
 *  「待审批」一格里，否则默认落在「操作目录」的人根本看不到它。 */
export function ActionsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const activeSub = rawSub === null || rawSub.trim() === "" ? DEFAULT_SUB_TAB : rawSub;
  const known = ACTIONS_SUB_TABS.some(([value]) => value === activeSub);

  if (!known) {
    return (
      <section>
        <PageHeader
          title={navLabel("/actions")}
          description="操作目录、待审批、执行记录与风险条件分开呈现；未知子页不会回落到操作目录。"
        />
        <PageState
          kind="unavailable"
          title={`「${rawSub}」子页尚未接入`}
          description="请从已定义的子页中选择；系统不会把未知地址误当成操作目录。"
        />
      </section>
    );
  }

  const selectSub = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("sub", value);
    // 换子页签用 replace：连点几格不该在浏览器历史里堆好几条
    setSearchParams(next, { replace: true });
  };

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={navLabel("/actions")}
        description="所有产生状态变化的写操作的唯一入口（宪法 2、3 条）。本页只读：查看已注册的 Action、它们的风险等级与启用条件，以及跨 Action 的执行记录；本页自身不执行任何写操作。"
      />
      <AdvancedControlsGate />
      <Tabs
        value={activeSub}
        onValueChange={selectSub}
        items={ACTIONS_SUB_TABS.map(([value, label]) => ({
          value,
          label,
          content: renderActionsSubTab(value),
        }))}
      />
    </section>
  );
}

function renderActionsSubTab(value: string): ReactNode {
  switch (value) {
    case "catalog":
      return <ActionCatalogTab />;
    case "pending":
      return <ActionPendingApprovalTab />;
    case "runs":
      return <ActionRunsTab />;
    case "risk":
      return <ActionRiskConditionsTab />;
    default:
      return null;
  }
}

/** 页面级门禁说明（ADMIN-IA §七 / 交接文档 §2.5 同一条纪律的操作与审批版本）。
 *
 *  这句话要在页面上**看得见**，不能只写在文档里。它随审批链的进度改过两次：
 *
 *  1. XM-0030b-ui：从「尚未上线」改成「尚未启用」——后端已实装但没注入，
 *     两者的下一步不同。
 *  2. XM-0030-ENABLE（本次）：**那句话现在是错的，而且错了两处。**
 *     一是审批服务已经在 platform-api / platform-worker 两端注入，「尚未
 *     启用」不再是编译期就能断言的事实——某个环境挂没挂，只有那个环境的
 *     `/api/v1/approvals` 通不通才知道，那由「待审批」子页签自己如实报
 *     （api/approvals.ts 的 APPROVALS_NOT_MOUNTED_DESCRIPTION）。
 *     二是「本页不提供任何执行入口」也不成立了：「待审批」里一张**已批准**
 *     的单上就有「执行」按钮（ApprovalQueue 的 canExecute 分支）。
 *
 *  所以这条横幅收回到它唯一始终为真的那件事：**目录页不是执行入口**。
 *  L2 及以上从目录里点不动，要先成单、被人批准，执行按钮只长在那张单上。
 *  一条会随环境变真变假的断言不该写死在组件里——写死就一定会像刚才那样，
 *  在环境变了之后继续理直气壮地说错话。 */
function AdvancedControlsGate() {
  return (
    <p role="status" className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg">
      门禁：操作目录只用来看，不是执行入口。写操作只走 Action，L2 及以上一律先由
      内核受理成审批单、由人批准，执行按钮只出现在「待审批」里那张已批准的单上，
      并且只按单上冻结的参数跑。此处永远不会出现「假装执行成功」的按钮。
    </p>
  );
}

// ---------------------------------------------------------------------------
// 操作目录
// ---------------------------------------------------------------------------

function ActionCatalogTab() {
  const query = useQuery({
    queryKey: ["action-definitions"],
    queryFn: ({ signal }) => listActionDefinitions({ signal }),
  });
  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title="操作目录"
        description="已注册的 Action 声明：ID、版本、风险等级、所需权限、允许的 Environment 与 Principal 类型。「可执行」为否的动作会被内核在执行时拒绝（ADVANCED_CONTROLS_REQUIRED）——隐藏或灰显不构成安全控制，服务端仍会拒绝。"
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
      />
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <DataTableV2
          caption="操作目录：ID、版本、风险等级、所需权限、允许环境、允许身份类型与可执行状态"
          columns={CATALOG_COLUMNS}
          rows={query.data ?? []}
          rowKey={(row) => `${row.id}@${row.version}`}
          searchable
          filters={[{ columnId: "risk", label: "风险等级", options: ["L0", "L1", "L2", "L3", "L4"] }]}
          emptyState={
            <PageState
              kind="empty"
              title="还没有注册任何 Action"
              description="Action 在各模块初始化时反向注册到内核（ADR-003：Action 是唯一写入口）。"
            />
          }
        />
      </ApiStateView>
    </section>
  );
}

const CATALOG_COLUMNS: DataTableColumn<ActionDefinitionItem>[] = [
  {
    id: "id",
    header: "Action ID",
    primary: true,
    value: (row) => row.id,
    cell: (row) => (
      <span className="min-w-0">
        <span className="font-mono text-xs [overflow-wrap:anywhere]">{row.id}</span>
        <p className="text-xs text-fg-muted">版本 {row.version}</p>
      </span>
    ),
  },
  {
    id: "risk",
    header: "风险等级",
    value: (row) => row.risk_level,
    cell: (row) => <Badge tone={riskTone(row.risk_level)}>{row.risk_level}</Badge>,
  },
  {
    id: "permission",
    header: "所需权限",
    value: (row) => row.permission,
    cell: (row) => <span className="font-mono text-xs [overflow-wrap:anywhere]">{row.permission}</span>,
  },
  {
    id: "environments",
    header: "允许环境",
    cell: (row) => (row.environments.length > 0 ? row.environments.join(" / ") : "—"),
  },
  {
    id: "principalTypes",
    header: "允许身份类型",
    cell: (row) => (row.principal_types.length > 0 ? row.principal_types.join(" / ") : "—"),
  },
  {
    id: "executable",
    header: "可执行",
    value: (row) => (row.executable ? "是" : "否"),
    cell: (row) =>
      row.executable ? (
        <Badge tone="success">是</Badge>
      ) : (
        <span className="flex flex-col gap-0.5">
          <Badge tone="warning">否</Badge>
          {row.blocked_reason ? <span className="text-xs text-fg-muted">{row.blocked_reason}</span> : null}
        </span>
      ),
  },
];

// ---------------------------------------------------------------------------
// 待审批
// ---------------------------------------------------------------------------

function ActionPendingApprovalTab() {
  return <ApprovalQueue />;
}

// ---------------------------------------------------------------------------
// 执行记录
// ---------------------------------------------------------------------------

const RUN_STATUS_ALL = "all";
const RUN_STATUS_OPTIONS = [
  { value: RUN_STATUS_ALL, label: "全部状态" },
  { value: "succeeded", label: "仅成功" },
  { value: "failed", label: "仅失败" },
];

/** 跨 Action 执行记录（操作与审批页「执行记录」子页签）。
 *
 *  结构照 components/RequestsPanel.tsx：筛选条件进 URL Search Params（可分享
 *  可恢复），游标是页内状态、不进 URL（它是不透明字符串，贴给同事也复现不出
 *  同一页——数据一直在写入，「第几页」本来就不是一个值得分享的东西）。 */
function ActionRunsTab() {
  const [searchParams, setSearchParams] = useSearchParams();
  const [cursor, setCursor] = useState("");
  const [cursorStack, setCursorStack] = useState<string[]>([]);

  const actionId = searchParams.get("action_id") ?? "";
  const status = (searchParams.get("status") ?? "") as ActionRunStatusFilter;
  const principalId = searchParams.get("principal") ?? "";

  const query = useQuery({
    queryKey: ["action-runs", { actionId, status, principalId, cursor }],
    queryFn: ({ signal }) =>
      listActionRuns({
        actionId,
        status,
        principal: principalId,
        cursor,
        limit: ACTION_RUN_PAGE_SIZE,
        signal,
      }),
  });

  /** 改筛选条件时回到第一页：留在第 3 页的游标对一组新条件毫无意义。 */
  const setFilter = (key: string, value: string) => {
    const next = new URLSearchParams(searchParams);
    if (value.trim() === "") next.delete(key);
    else next.set(key, value);
    setSearchParams(next, { replace: true });
    setCursor("");
    setCursorStack([]);
  };

  const hasFilters = [actionId, status, principalId].some((v) => v !== "");
  const clearFilters = () => {
    const next = new URLSearchParams(searchParams);
    for (const key of ["action_id", "status", "principal"]) next.delete(key);
    setSearchParams(next, { replace: true });
    setCursor("");
    setCursorStack([]);
  };

  const page = query.data;

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title="执行记录"
        description="跨 Action 的执行记录：谁在什么时候执行了哪个 Action、结果如何。只读；不提供任何重放或重试入口。"
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
      />
      <RunFilters
        actionId={actionId}
        status={status}
        principalId={principalId}
        hasFilters={hasFilters}
        onChange={setFilter}
        onClear={clearFilters}
      />
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        {page === undefined ? null : (
          <DataTableV2
            caption="执行记录：时间、Action、身份、环境、风险等级、状态、耗时、request_id 与详情"
            columns={RUN_COLUMNS}
            rows={page.items}
            rowKey={(row) => row.id}
            // 服务端已经在按游标分页，不再叠一层客户端 pageSize；表内搜索同理
            // 关闭——筛选在服务端，两个搜索框会打架
            renderExpanded={(row) => <RunDetailExpansion runId={row.id} />}
            emptyState={
              <PageState
                kind="empty"
                title={hasFilters ? "没有符合条件的执行记录" : "这个环境还没有执行记录"}
                description={
                  hasFilters
                    ? "换个条件，或清除筛选看全部。"
                    : "L0/L1 动作执行成功或被拒后都会出现在这里——包括被拒绝的尝试。"
                }
                action={
                  hasFilters ? (
                    <Button variant="secondary" size="sm" onClick={clearFilters}>
                      清除筛选
                    </Button>
                  ) : undefined
                }
              />
            }
            footerExtra={
              <Pager
                hasPrev={cursorStack.length > 0}
                hasNext={page.nextCursor !== ""}
                onPrev={() => {
                  const stack = [...cursorStack];
                  const prev = stack.pop() ?? "";
                  setCursorStack(stack);
                  setCursor(prev);
                }}
                onNext={() => {
                  setCursorStack([...cursorStack, cursor]);
                  setCursor(page.nextCursor);
                }}
              />
            }
          />
        )}
      </ApiStateView>
    </section>
  );
}

function RunFilters({
  actionId,
  status,
  principalId,
  hasFilters,
  onChange,
  onClear,
}: {
  actionId: string;
  status: ActionRunStatusFilter;
  principalId: string;
  hasFilters: boolean;
  onChange: (key: string, value: string) => void;
  onClear: () => void;
}) {
  return (
    <div className="flex flex-wrap items-end gap-3 rounded-lg border border-edge bg-surface p-3">
      <FilterField label="Action ID" hint="精确匹配，不是模糊搜索">
        <Input
          value={actionId}
          placeholder="如 registry.service.create"
          onChange={(e) => onChange("action_id", e.target.value)}
          className="w-56"
        />
      </FilterField>
      <FilterField label="状态">
        <Select
          aria-label="按状态筛选"
          options={RUN_STATUS_OPTIONS}
          value={status === "" ? RUN_STATUS_ALL : status}
          onValueChange={(v) => onChange("status", v === RUN_STATUS_ALL ? "" : v)}
          className="w-36"
        />
      </FilterField>
      <FilterField label="Principal" hint="精确匹配">
        <Input
          value={principalId}
          placeholder="如 staff_alice"
          onChange={(e) => onChange("principal", e.target.value)}
          className="w-44"
        />
      </FilterField>
      {hasFilters ? (
        <Button variant="secondary" size="sm" onClick={onClear}>
          清除筛选
        </Button>
      ) : null}
    </div>
  );
}

function FilterField({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-xs font-medium text-fg-muted" title={hint}>
        {label}
      </span>
      {children}
    </label>
  );
}

const RUN_COLUMNS: DataTableColumn<ActionRunItem>[] = [
  {
    id: "startedAt",
    header: "时间",
    value: (row) => row.started_at,
    cell: (row) => (
      <span className="tabular-nums whitespace-nowrap">{formatLocalTimestamp(row.started_at)}</span>
    ),
  },
  {
    id: "action",
    header: "Action",
    primary: true,
    value: (row) => `${row.action_id}@${row.action_version}`,
    cell: (row) => (
      <span className="min-w-0">
        <span className="font-mono text-xs [overflow-wrap:anywhere]">{row.action_id}</span>
        <p className="text-xs text-fg-muted">版本 {row.action_version}</p>
      </span>
    ),
  },
  {
    id: "principal",
    header: "身份",
    value: (row) => `${row.principal_id} ${row.principal_type}`,
    cell: (row) => (
      <span>
        <span className="font-medium">{row.principal_id}</span>
        <p className="text-xs text-fg-muted">{row.principal_type}</p>
      </span>
    ),
  },
  {
    id: "environment",
    header: "环境",
    value: (row) => row.environment,
    cell: (row) => row.environment,
  },
  {
    id: "risk",
    header: "风险等级",
    value: (row) => row.risk_level,
    cell: (row) => <Badge tone={riskTone(row.risk_level)}>{row.risk_level}</Badge>,
  },
  {
    id: "status",
    header: "状态",
    value: (row) => row.status,
    // 按状态本身排，不按中文文案：「成功」「失败」按笔画排没有意义
    sortAs: (row) => row.status,
    cell: (row) => (
      <span className="flex flex-col gap-0.5">
        <Badge tone={row.status === "succeeded" ? "success" : "danger"}>
          {row.status === "succeeded" ? "成功" : "失败"}
        </Badge>
        {row.error_code ? <span className="font-mono text-xs text-danger">{row.error_code}</span> : null}
      </span>
    ),
  },
  {
    id: "duration",
    header: "耗时",
    numeric: true,
    value: (row) => row.duration_ms,
    cell: (row) => `${row.duration_ms.toLocaleString("zh-CN")} ms`,
  },
  {
    id: "requestId",
    header: "request_id",
    value: (row) => row.request_id,
    cell: (row) => <span className="font-mono text-xs [overflow-wrap:anywhere]">{row.request_id}</span>,
  },
];

/** 游标翻页，只有上一页/下一页，没有页码（同 RequestsPanel.tsx 的 Pager：
 *  游标分页给不出总数，编一个页码等于编一个总页数）。 */
function Pager({
  hasPrev,
  hasNext,
  onPrev,
  onNext,
}: {
  hasPrev: boolean;
  hasNext: boolean;
  onPrev: () => void;
  onNext: () => void;
}) {
  if (!hasPrev && !hasNext) return null;
  return (
    <div className="flex items-center justify-end gap-2">
      <Button variant="secondary" size="sm" disabled={!hasPrev} onClick={onPrev}>
        上一页
      </Button>
      <Button variant="secondary" size="sm" disabled={!hasNext} onClick={onNext}>
        下一页
      </Button>
    </div>
  );
}

/** 单条执行记录的展开详情：懒加载——只有这一行被展开时才请求
 *  GET /actions/runs/{id}（DataTableV2 只为 expanded 的行调用 renderExpanded，
 *  见 ui-admin/DataTableV2.tsx），不会因为一页有 N 行就发 N 次请求。 */
function RunDetailExpansion({ runId }: { runId: string }) {
  const query = useQuery({
    queryKey: ["action-run", runId],
    queryFn: ({ signal }) => getActionRun(runId, { signal }),
  });
  return (
    <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()} compact>
      {query.data ? <RunDetailPanel detail={query.data} /> : null}
    </ApiStateView>
  );
}

function RunDetailPanel({ detail }: { detail: ActionRunDetail }) {
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
      <div className="min-w-0 md:col-span-2">
        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs sm:grid-cols-3">
          <DetailField label="run_id" value={detail.run.id} mono />
          <DetailField label="request_id" value={detail.run.request_id} mono />
          <DetailField label="耗时" value={`${detail.run.duration_ms} ms`} />
        </dl>
      </div>
      {detail.audit ? (
        <>
          <SummaryBlock title="变更前 before_summary" summary={detail.audit.before_summary} />
          <SummaryBlock title="变更后 after_summary" summary={detail.audit.after_summary} />
          <p className="text-xs text-fg-muted md:col-span-2">
            资源：{detail.audit.resource_type || "—"} {detail.audit.resource_id}
            {detail.audit.reason ? ` · 理由：${detail.audit.reason}` : ""}
          </p>
        </>
      ) : (
        <p className="text-xs text-fg-muted md:col-span-2">
          没有找到关联的审计事件——审计写入失败不回滚业务结果，这种缺口必须如实显示，不能假装恒有。
        </p>
      )}
    </div>
  );
}

function SummaryBlock({ title, summary }: { title: string; summary: Record<string, unknown> | null }) {
  return (
    <div className="min-w-0">
      <p className="mb-1 text-xs font-medium text-fg">{title}</p>
      <pre className="overflow-x-auto rounded-md border border-edge bg-surface p-2 font-mono text-xs text-fg">
        {summary === null ? "（无镜像）" : JSON.stringify(summary, null, 2)}
      </pre>
    </div>
  );
}

function DetailField({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className="text-fg-muted">{label}</dt>
      <dd className={mono ? "font-mono break-all text-fg" : "text-fg"}>{value || "—"}</dd>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 风险与启用条件
// ---------------------------------------------------------------------------

interface RiskLevelInfo {
  level: string;
  examples: string;
  controls: string;
  /** 该等级能不能被**直接**执行（L0/L1 能；L2 及以上不能，见
   *  action.RiskLevel.RequiresAdvancedControls）。纯展示用的静态治理事实，
   *  不是查询结果——变更需要走 ADR，不随部署环境变化。
   *
   *  这个字段以前叫 foundationAReady，措辞是「（全部待 Foundation-B）」。
   *  审批中心启用（XM-0030-ENABLE）之后那句话是错的：L2 及以上不再是「等下一个
   *  阶段」，而是**现在就能提，只是先落审批单**。「不能直接执行」是这一栏唯一
   *  始终为真的意思，所以字段跟着改名——一个会过期的名字迟早会把过期的话
   *  再说一遍。至于某个环境接没接上审批中心，只有那个环境知道，由后端逐条给
   *  blocked_reason（httpapi.ListActionsHandler 的 approvalsWired 分支），
   *  显示在「操作目录」的「可执行」列里，本页不猜。 */
  directlyExecutable: boolean;
}

/** ADR-003（docs/adr/ADR-003-Action唯一写入口.md）的风险等级表，逐字对齐。 */
const RISK_LEVELS: readonly RiskLevelInfo[] = [
  { level: "L0", examples: "保存个人视图、低影响偏好", controls: "权限 + 基础审计", directlyExecutable: true },
  {
    level: "L1",
    examples: "修改低风险平台配置、确认普通告警",
    controls: "权限 + 审计；按需幂等",
    directlyExecutable: true,
  },
  {
    level: "L2",
    examples: "批量配置、启停低风险资源",
    controls: "预览 + 幂等 + 写后确认 + 完整审计",
    directlyExecutable: false,
  },
  {
    level: "L3",
    examples: "服务切换、账号批量导入、敏感配置",
    controls: "人工批准 + MFA + 冷却 + 补偿",
    directlyExecutable: false,
  },
  {
    level: "L4",
    examples: "退款、生产基础设施高影响动作、开票关键动作",
    controls: "双人审批目标；单人阶段 Break-glass",
    directlyExecutable: false,
  },
];

/** 风险与启用条件：ADR-003 的静态治理表 + 操作目录的实时统计。
 *
 *  「已注册」「可执行」两列复用操作目录同一个 queryKey（["action-definitions"]），
 *  与「操作目录」子页签共用 react-query 缓存——两个子页签同时存在数据时
 *  不会因为切换 Tab 就重新拉一次。 */
function ActionRiskConditionsTab() {
  const query = useQuery({
    queryKey: ["action-definitions"],
    queryFn: ({ signal }) => listActionDefinitions({ signal }),
  });
  const items = query.data ?? [];
  const countByLevel = (level: string) => items.filter((it) => it.risk_level === level).length;
  const executableByLevel = (level: string) =>
    items.filter((it) => it.risk_level === level && it.executable).length;

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title="风险与启用条件"
        description="ADR-003 定义的风险等级与基础控制。「已注册」「可执行」两列是当前 Foundation 阶段的真实数据，不是文档复述。"
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
      />
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <div className="overflow-x-auto rounded-lg border border-edge">
          <table className="w-full min-w-[640px] text-left text-xs">
            <caption className="sr-only">风险等级、例子、基础控制、已注册数量与可执行数量</caption>
            <thead className="bg-surface-muted text-fg-muted">
              <tr>
                <th className="px-3 py-2 font-medium">等级</th>
                <th className="px-3 py-2 font-medium">例子</th>
                <th className="px-3 py-2 font-medium">基础控制</th>
                <th className="px-3 py-2 text-right font-medium">已注册</th>
                <th className="px-3 py-2 text-right font-medium">可执行</th>
              </tr>
            </thead>
            <tbody>
              {RISK_LEVELS.map((r) => (
                <tr key={r.level} className="border-t border-edge">
                  <td className="px-3 py-2">
                    <Badge tone={riskTone(r.level)}>{r.level}</Badge>
                  </td>
                  <td className="px-3 py-2 text-fg-muted">{r.examples}</td>
                  <td className="px-3 py-2 text-fg-muted">{r.controls}</td>
                  <td className="px-3 py-2 text-right tabular-nums text-fg">{countByLevel(r.level)}</td>
                  <td className="px-3 py-2 text-right tabular-nums text-fg">
                    {executableByLevel(r.level)}
                    {!r.directlyExecutable && countByLevel(r.level) > 0 ? (
                      <span
                        className="ml-1 text-fg-muted"
                        title="L2 及以上不能直接执行：内核受理成审批单，批准后在「待审批」里由人触发"
                      >
                        （全部先落审批单）
                      </span>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="text-xs text-fg-muted">
          依据 docs/adr/ADR-003-Action唯一写入口.md。「可执行」由内核按风险等级实时判定
          （L2/L3/L4 恒为否——它们<strong>不能被直接执行</strong>，而是先由内核受理成审批单，
          批准后在「待审批」子页签由人触发），不是本页写死的规则；隐藏或灰显同样不构成安全控制。
          某个环境接没接上审批中心，逐条写在「操作目录」的「可执行」列里，由后端给出。
        </p>
      </ApiStateView>
    </section>
  );
}
