import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  DataTableV2,
  navItemByPath,
  navLabel,
  PageHeader,
  PageState,
  StatTile,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge, Tabs } from "@xingmang/ui-primitives";
import { useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import {
  listApiClients,
  listAutomationRules,
  setApiClient,
  setApiClientStatus,
  setAutomationRule,
  setAutomationRuleStatus,
  API_CLIENTS_QUERY,
  AUTOMATION_RULES_QUERY,
  CLIENT_STATUS_HINTS,
  CLIENT_STATUS_LABELS,
  RULE_STATUS_LABELS,
  TRIGGER_KIND_LABELS,
  type ApiClientItem,
  type ApiClientsResponse,
  type AutomationRuleItem,
  type AutomationRulesResponse,
  type CallerActivity,
} from "../api/integration";
import {
  BlueprintTabView,
  blueprintForPath,
  type BlueprintTab,
} from "../blueprints";
import { ApiStateView } from "../components/ApiStateView";

const INTEGRATION_PATH = "/ext/integration";

const SUB_TABS = (navItemByPath(INTEGRATION_PATH)?.item.subTabs ?? []).map(
  (tab) => [tab.id, tab.label] as const,
);

/** 默认落在「API调用方」：它是信息架构冻结的第一格，也恰好是接了真数据的
 *  那一格。两条理由指向同一格时不必再挑（对照 ChangesPage 那边两格各有读数、
 *  于是与侧栏顺序一致的处理）。 */
const DEFAULT_SUB = "clients";

/** 结构（页签、列头、统计格标签）取自冻结的蓝图规格。
 *
 *  只借结构、不借文案里的**归因**——蓝图那份写的是「后置能力：尚未排期，
 *  不因为这一页存在就提前建后端」，那句话在 2026-09-08 的裁定之后已经不对了
 *  （ADMIN-IA §5.4.1）。逐格的如实归因见 TAB_SOURCE。 */
const BLUEPRINT = blueprintForPath(INTEGRATION_PATH);

/** 每一格「今天真正在等什么」。替换蓝图里那句已经作废的落款。 */
const TAB_SOURCE: Readonly<Record<string, string>> = {
  webhooks:
    "平台今天没有业务事件 Webhook。有的是**告警外发**——三条各自独立的企业微信群机器人链路（告警 / 卡片事件 / 接码验证码），" +
    "它们把消息推给运营看，不是把事件推给外部系统消费。要做通用 Webhook 还缺四样：订阅登记（地址 + 事件类型 + 密钥引用 + 状态）、" +
    "签名、重试与逐次投递记录。下面那张表列的是**已有的**出站通道，不是 Webhook 订阅。",
  runs:
    "流程运行记录**不存在**，因为没有执行器：规则登记在此但当前不会自动执行（见「自动化流程」一格）。" +
    "今天真有的运行记录是另外两类，各有各的落点，见上方那张对照表。",
};

/** 取蓝图里的一格，并把落款换成上面那份如实的归因（同 ChangesPage 的做法）。 */
function honestBlueprintTab(tabId: string): BlueprintTab | undefined {
  const tab = BLUEPRINT?.tabs.find((item) => item.id === tabId);
  if (!tab) return undefined;
  const source = TAB_SOURCE[tabId];
  return {
    ...tab,
    ...(source ? { source } : {}),
    ...(tab.tables && source ? { tables: tab.tables.map((table) => ({ ...table, source })) } : {}),
  };
}

function BlueprintPreview({ tabId }: { tabId: string }) {
  const tab = honestBlueprintTab(tabId);
  if (!tab) {
    return (
      <PageState
        kind="unavailable"
        title="蓝图规格里没有这一格"
        description={`导航有子页签「${tabId}」，但 blueprints/ext.ts 的接口与自动化规格里没有同名条目——两份数据已经漂开，请先对齐再看这一格。`}
      />
    );
  }
  return <BlueprintTabView tab={tab} />;
}

/** 接口与自动化（/ext/integration）。
 *
 *  2026-09-08 产品负责人推翻 ADMIN-IA §5.4「只读蓝图、不得提前建后端」这条，
 *  对**这一页**改为真建（§5.4.1）。四格今天的状态：
 *
 *  - **API调用方**：接真数据。登记簿（`core.api_client`）与 `action_run` 里
 *    实际观测到的调用方**对账**，于是「登记了却从没来过」与「来过却没登记」
 *    各有一个落点。登记簿本身**不发凭据、不授权、不限流**——这句话由后端
 *    随响应下发，页面照它渲染。
 *  - **Webhook**：保持蓝图态。平台只有「告警外发」，它不是业务事件 Webhook。
 *  - **自动化流程**：接真数据，但**只有登记没有执行器**。页面必须让人一眼
 *    看见这一点，不能让人以为配了就生效。
 *  - **运行记录**：流程运行记录不存在（没有执行器）；真有的两类各在别处，
 *    这一格把它们指清楚。 */
export function ExtIntegrationPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const activeSub = rawSub === null || rawSub.trim() === "" ? DEFAULT_SUB : rawSub;
  const known = SUB_TABS.some(([value]) => value === activeSub);

  const clientsQuery = useQuery({
    queryKey: [API_CLIENTS_QUERY],
    queryFn: ({ signal }) => listApiClients({ signal }),
  });
  const rulesQuery = useQuery({
    queryKey: [AUTOMATION_RULES_QUERY],
    queryFn: ({ signal }) => listAutomationRules({ signal }),
  });

  // 认不出来的 ?sub= 不静默回落到第一格：那会让一个拼错的地址看起来像正常
  // 页面，而人以为自己看的是别的东西（同 ChangesPage / OpsPage 的处理）。
  if (!known) {
    return (
      <section>
        <PageHeader
          title={navLabel(INTEGRATION_PATH)}
          description="四格按各自的地基逐步接入；未知地址不会静默回落到第一格。"
        />
        <PageState
          kind="unavailable"
          title={`「${rawSub}」子页尚未接入`}
          description="请从已定义的接口与自动化子页中选择。"
          action={
            <Link
              to={`${INTEGRATION_PATH}?sub=${DEFAULT_SUB}`}
              className="text-sm font-medium text-accent hover:underline"
            >
              返回 API调用方
            </Link>
          }
        />
      </section>
    );
  }

  const selectSub = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("sub", value);
    // 换子页签用 replace：连点四格不该在浏览器里堆四条历史。
    setSearchParams(next, { replace: true });
  };

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={navLabel(INTEGRATION_PATH)}
        status={<Badge tone="warning">部分接入</Badge>}
        description={BLUEPRINT?.description}
      />
      <IntegrationTiles clients={clientsQuery.data} rules={rulesQuery.data} />
      <Tabs
        ariaLabel="接口与自动化子页"
        value={activeSub}
        onValueChange={selectSub}
        items={SUB_TABS.map(([value, label]) => ({
          value,
          label,
          content:
            value === "clients" ? (
              <ApiClientsTab query={clientsQuery} />
            ) : value === "flows" ? (
              <AutomationRulesTab query={rulesQuery} />
            ) : value === "webhooks" ? (
              <WebhooksTab label={label} />
            ) : (
              <RunsTab label={label} />
            ),
        }))}
      />
    </section>
  );
}

// ---------------------------------------------------------------------------
// 顶部四格
// ---------------------------------------------------------------------------

/** 统计格。标签与口径说明逐字取自冻结蓝图；能算的算真值，算不出的显式
 *  「未接入」并说清缺什么——不拿一个 0 冒充「没有异常」（宪法 12 条）。 */
function IntegrationTiles({
  clients,
  rules,
}: {
  clients?: ApiClientsResponse;
  rules?: AutomationRulesResponse;
}) {
  const tiles = BLUEPRINT?.tiles ?? [];
  const draftCount = rules?.items.filter((r) => r.status === "draft").length;
  const values: Readonly<Record<string, { value?: string; note?: string }>> = {
    API调用方: { value: clients ? String(clients.items.length) : undefined },
    Webhook异常: {
      note: "平台没有 Webhook 投递记录，这一格算不出来——只有「告警外发」，且卡片与接码两条通道连投递状态都不留。",
    },
    流程草稿: {
      value: draftCount === undefined ? undefined : String(draftCount),
      note: "登记为草稿的规则数。**已登记的规则同样不会自动执行**——这一格数的是编写进度，不是启停。",
    },
    多次失败任务: {
      note: "没有流程运行，也就没有「连续失败」可数；Action 执行记录里的失败次数在「操作与审批 → 执行记录」。",
    },
  };
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      {tiles.map((tile) => {
        const override = values[tile.label] ?? {};
        const value = override.value;
        return (
          <StatTile
            key={tile.label}
            label={tile.label}
            value={value ?? "—"}
            unavailable={value === undefined}
            note={override.note ?? tile.note}
          />
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// API调用方：登记簿 × 观测到的调用方
// ---------------------------------------------------------------------------

function formatTime(value: string): string {
  if (!value) return "—";
  const at = new Date(value);
  return Number.isNaN(at.getTime()) ? value : at.toLocaleString();
}

/** 「未接入」的列内占位。写成一个组件而不是每处一个 `—`：一个光秃秃的破折号
 *  与「这一行恰好没有值」长得一模一样。 */
function NotWired({ reason }: { reason: string }) {
  return (
    <span className="text-xs text-fg-muted" title={reason}>
      未接入
    </span>
  );
}

function clientColumns(): readonly DataTableColumn<ApiClientItem>[] {
  return [
    {
      id: "client",
      header: "API调用方",
      primary: true,
      value: (row) => `${row.display_name} ${row.principal_id}`,
      cell: (row) => (
        <div className="flex flex-col">
          <span className="font-medium">{row.display_name}</span>
          <span className="font-mono text-xs text-fg-muted [overflow-wrap:anywhere]">
            {row.principal_id}
          </span>
        </div>
      ),
    },
    { id: "purpose", header: "用途", value: (row) => row.purpose, cell: (row) => row.purpose || "—" },
    {
      id: "principalType",
      header: "身份类型",
      value: (row) => row.principal_type,
      cell: (row) => row.principal_type,
    },
    {
      id: "scopes",
      header: "权限范围",
      value: (row) => row.expected_scopes.join(" "),
      cell: (row) =>
        row.expected_scopes.length === 0 ? (
          "—"
        ) : (
          <span
            className="text-xs"
            title="这是登记的**期望**权限，不是生效的权限——真实授权在 Keycloak 角色与员工账号角色里"
          >
            {row.expected_scopes.join(" / ")}
          </span>
        ),
    },
    {
      id: "ipLimit",
      header: "IP限制",
      // 留列不留数：平台没有按调用方的 IP 允许清单，这一列不是「这一行没配」，
      // 是这个能力整体不存在（同渠道表「供给成本」三列的处理）。
      cell: () => <NotWired reason="平台没有按调用方的 IP 允许清单；今天的来源限制只有管理端登录的 adminip 白名单，与 API 调用方不是一回事。" />,
    },
    {
      id: "quota",
      header: "调用配额",
      cell: () => <NotWired reason="限流模块（internal/platform/ratelimit）今天是进程内的通用 GCRA，没有按调用方的配额，也不读这张登记表。" />,
    },
    {
      id: "lastSeen",
      header: "最近请求",
      value: (row) => row.observed?.last_seen_at ?? "",
      cell: (row) =>
        row.observed ? (
          <div className="flex flex-col">
            <span>{formatTime(row.observed.last_seen_at)}</span>
            <span className="font-mono text-xs text-fg-muted [overflow-wrap:anywhere]">
              {row.observed.last_action_id}
            </span>
          </div>
        ) : (
          <span className="text-xs text-fg-muted">窗口内没有写操作</span>
        ),
    },
    {
      id: "status",
      header: "状态",
      value: (row) => row.status,
      cell: (row) => (
        <Badge
          tone={row.status === "active" ? "success" : "neutral"}
          title={CLIENT_STATUS_HINTS[row.status]}
        >
          {CLIENT_STATUS_LABELS[row.status] ?? row.status}
        </Badge>
      ),
    },
    {
      id: "detail",
      header: "详情",
      // 没有专门的调用方详情页，也不为它编一个：这一行真正的「详情」就是
      // 它跑过哪些 Action，而那份记录已经有页面了。
      cell: (row) => (
        <Link
          to={`/actions?sub=runs&principal=${encodeURIComponent(row.principal_id)}`}
          className="text-xs font-medium text-accent hover:underline"
        >
          执行记录 →
        </Link>
      ),
    },
  ];
}

function unregisteredColumns(): readonly DataTableColumn<CallerActivity>[] {
  return [
    {
      id: "principal",
      header: "身份",
      primary: true,
      value: (row) => row.principal_id,
      cell: (row) => (
        <span className="font-mono text-xs [overflow-wrap:anywhere]">{row.principal_id}</span>
      ),
    },
    { id: "type", header: "身份类型", value: (row) => row.principal_type, cell: (row) => row.principal_type },
    { id: "runs", header: "窗口内调用", numeric: true, value: (row) => row.run_count, cell: (row) => row.run_count },
    { id: "failed", header: "其中失败", numeric: true, value: (row) => row.failed_count, cell: (row) => row.failed_count },
    {
      id: "last",
      header: "最近一次",
      value: (row) => row.last_seen_at,
      cell: (row) => (
        <div className="flex flex-col">
          <span>{formatTime(row.last_seen_at)}</span>
          <span className="font-mono text-xs text-fg-muted [overflow-wrap:anywhere]">
            {row.last_action_id}
          </span>
        </div>
      ),
    },
    {
      id: "detail",
      header: "详情",
      cell: (row) => (
        <Link
          to={`/actions?sub=runs&principal=${encodeURIComponent(row.principal_id)}`}
          className="text-xs font-medium text-accent hover:underline"
        >
          执行记录 →
        </Link>
      ),
    },
  ];
}

interface QueryLike<T> {
  data?: T;
  isPending: boolean;
  isFetching: boolean;
  error: unknown;
  dataUpdatedAt: number;
  refetch: () => unknown;
}

function ApiClientsTab({ query }: { query: QueryLike<ApiClientsResponse> }) {
  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title="API调用方"
        // 标题里就带上「写操作」这个限定，不把它全押在下面那张卡上：
        // 「谁在调我们的 API」这句话不加限定会被读成包含读请求，而读请求
        // 今天查不到（只进进程访问日志）。限定放在会被误读的那句话旁边，
        // 而不是放在读者可能不往下看的地方。
        description="谁在调我们的 API **做写操作**。左边是登记簿（我们认为谁该来），右边是 Action 执行记录里实际观测到的调用方（谁真的来做过写操作）——两侧对不上就是要处理的事。**读操作不在这份观测里**，下面第一张卡说清了完整边界。"
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
      />
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {query.data ? <ApiClientsBody data={query.data} /> : null}
      </ApiStateView>
    </section>
  );
}

function ApiClientsBody({ data }: { data: ApiClientsResponse }) {
  return (
    <div className="flex flex-col gap-3">
      <ScopeNotice registryNote={data.registry_note} observedNote={data.observed_note} data={data} />
      <RegisterClientForm />
      <DataTableV2
        caption="API 调用方登记簿，含各自在观测窗口内的活动"
        columns={clientColumns()}
        rows={data.items}
        rowKey={(row) => row.id}
        searchable
        renderExpanded={(row) => <ClientDetail row={row} />}
        emptyState={
          <PageState
            kind="empty"
            title="还没有登记任何调用方"
            description="登记簿是空的。下方「窗口内观测到的调用方」里如果有行，说明有身份在调我们却没登记。"
            compact
          />
        }
      />
      <section className="flex flex-col gap-2">
        <header className="flex flex-wrap items-baseline justify-between gap-2">
          {/* 不写「来过却没登记」：那会把这张表读成「所有来访者」，而它只
              数得到写操作。标题里带上限定比在副行补一句更难被略过。 */}
          <h3 className="text-sm font-medium text-fg">做过写操作却没登记</h3>
          <p className="text-xs text-fg-muted">
            {data.observed_source} · 近 {data.window_days} 天（自 {formatTime(data.observed_since)}）
          </p>
        </header>
        {data.observed_truncated ? (
          <p role="status" className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg">
            窗口内出现的身份数超过上限，这份观测**不完整**：下面少列了一些身份。缩短窗口再看，或去「操作与审批 → 执行记录」逐条查。
          </p>
        ) : null}
        <DataTableV2
          caption="窗口内调用过但没有登记的身份"
          columns={unregisteredColumns()}
          rows={data.unregistered}
          rowKey={(row) => row.principal_id}
          emptyState={
            <PageState
              kind="empty"
              title="没有未登记的调用方"
              description="窗口内做过写操作的身份都在登记簿里。注意这只覆盖写操作——读操作不落库。"
              compact
            />
          }
        />
      </section>
    </div>
  );
}

/** 行展开：登记的其余字段 + 启用/停用。
 *
 *  状态开关放在展开区而不是新增一列：这张表的九个列头是**冻结的设计产出**
 *  （蓝图规格与 navigation.ts 逐字对账），为一个按钮加第十列等于改设计。 */
function ClientDetail({ row }: { row: ApiClientItem }) {
  const queryClient = useQueryClient();
  const [reason, setReason] = useState("");
  const nextStatus = row.status === "active" ? "disabled" : "active";
  const mutation = useMutation({
    mutationFn: () =>
      setApiClientStatus({ client_id: row.id, status: nextStatus, reason: reason.trim() }),
    onSuccess: () => {
      setReason("");
      void queryClient.invalidateQueries({ queryKey: [API_CLIENTS_QUERY] });
    },
  });
  return (
    <div className="flex flex-col gap-3 text-xs">
      <dl className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <div>
          <dt className="text-fg-muted">凭据引用</dt>
          <dd className="font-mono text-fg [overflow-wrap:anywhere]">
            {row.credential_ref || "未登记（这个调用方没有 API Key）"}
          </dd>
        </div>
        <div>
          <dt className="text-fg-muted">负责人</dt>
          <dd className="text-fg">{row.owner || "—"}</dd>
        </div>
        <div>
          <dt className="text-fg-muted">备注</dt>
          <dd className="text-fg">{row.notes || "—"}</dd>
        </div>
        <div>
          <dt className="text-fg-muted">登记 / 最近修改</dt>
          <dd className="text-fg">
            {formatTime(row.created_at)}（{row.created_by}） / {formatTime(row.updated_at)}（
            {row.updated_by}）
          </dd>
        </div>
      </dl>
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          mutation.mutate();
        }}
      >
        <FormRow
          label={nextStatus === "disabled" ? "停用理由" : "启用理由"}
          hint="必填。停用只改登记状态，**不会挡住这个身份的任何请求**"
        >
          <input
            className={fieldClass}
            required
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
        </FormRow>
        <button type="submit" className={fieldClass} disabled={mutation.isPending}>
          {nextStatus === "disabled" ? "停用登记" : "启用登记"}
        </button>
      </form>
      <ActionFeedback error={mutation.error} />
    </div>
  );
}

/** 两句必须跟着数据一起出现的限定。
 *
 *  文案取自响应而不是写死在前端：一份读数与它的限定必须同源，否则改了口径
 *  只会改一边（宪法 12 条）。 */
function ScopeNotice({
  registryNote,
  observedNote,
  data,
}: {
  registryNote: string;
  observedNote: string;
  data: ApiClientsResponse;
}) {
  return (
    <section className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4">
      <h3 className="text-sm font-medium text-fg">这张表能回答什么、不能回答什么</h3>
      <dl className="flex flex-col gap-2 text-xs text-fg-muted">
        <div>
          <dt className="font-medium text-fg">登记簿不授权</dt>
          <dd>{registryNote}</dd>
        </div>
        <div>
          <dt className="font-medium text-fg">观测只覆盖写操作</dt>
          <dd>{observedNote}</dd>
        </div>
        <div>
          <dt className="font-medium text-fg">窗口</dt>
          <dd>
            近 {data.window_days} 天，自 {formatTime(data.observed_since)}；来源 {data.observed_source}。
          </dd>
        </div>
      </dl>
      <Link to="/actions?sub=runs" className="text-xs font-medium text-accent hover:underline">
        去「操作与审批 → 执行记录」逐条查 →
      </Link>
    </section>
  );
}

// ---------------------------------------------------------------------------
// 登记表单（两张登记簿各一个）
// ---------------------------------------------------------------------------

const fieldClass =
  "rounded-md border border-edge-strong bg-surface px-2 py-1 text-xs text-fg focus:outline-2 focus:outline-accent";

function FormRow({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-xs text-fg-muted">
      <span className="font-medium text-fg">{label}</span>
      {children}
      {hint ? <span>{hint}</span> : null}
    </label>
  );
}

function RegisterClientForm() {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({
    principal_id: "",
    principal_type: "SERVICE",
    display_name: "",
    purpose: "",
    owner: "",
    expected_scopes: "",
    credential_ref: "",
    notes: "",
  });
  const mutation = useMutation({
    mutationFn: () =>
      setApiClient({
        principal_id: form.principal_id.trim(),
        principal_type: form.principal_type,
        display_name: form.display_name.trim(),
        purpose: form.purpose.trim(),
        owner: form.owner.trim(),
        expected_scopes: form.expected_scopes
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        credential_ref: form.credential_ref.trim(),
        notes: form.notes.trim(),
      }),
    onSuccess: () => {
      setOpen(false);
      void queryClient.invalidateQueries({ queryKey: [API_CLIENTS_QUERY] });
    },
  });

  if (!open) {
    return (
      <div>
        <button type="button" className={fieldClass} onClick={() => setOpen(true)}>
          ＋ 登记调用方
        </button>
      </div>
    );
  }
  return (
    <form
      className="flex flex-col gap-3 rounded-lg border border-edge bg-surface p-4"
      onSubmit={(e) => {
        e.preventDefault();
        mutation.mutate();
      }}
    >
      <h3 className="text-sm font-medium text-fg">登记调用方</h3>
      <p className="text-xs text-fg-muted">
        登记**不会**发放任何凭据，也不会让这个身份获得任何权限——它记录的是「我们认为谁该来调我们」。
        真实授权在 Keycloak 角色与员工账号角色里。
      </p>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <FormRow label="身份 ID" hint="与执行记录里的 principal_id 逐字一致，两边同源才对得上账">
          <input
            className={fieldClass}
            required
            value={form.principal_id}
            onChange={(e) => setForm({ ...form, principal_id: e.target.value })}
          />
        </FormRow>
        <FormRow label="身份类型">
          <select
            className={fieldClass}
            value={form.principal_type}
            onChange={(e) => setForm({ ...form, principal_type: e.target.value })}
          >
            {["SERVICE", "AI", "SERVER_AGENT", "HUMAN"].map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </FormRow>
        <FormRow label="名称">
          <input
            className={fieldClass}
            required
            value={form.display_name}
            onChange={(e) => setForm({ ...form, display_name: e.target.value })}
          />
        </FormRow>
        <FormRow label="用途">
          <input
            className={fieldClass}
            value={form.purpose}
            onChange={(e) => setForm({ ...form, purpose: e.target.value })}
          />
        </FormRow>
        <FormRow label="负责人" hint="出了事找谁">
          <input
            className={fieldClass}
            value={form.owner}
            onChange={(e) => setForm({ ...form, owner: e.target.value })}
          />
        </FormRow>
        <FormRow label="期望权限范围" hint="逗号分隔。这是登记的期望值，不参与任何鉴权判定">
          <input
            className={fieldClass}
            value={form.expected_scopes}
            onChange={(e) => setForm({ ...form, expected_scopes: e.target.value })}
          />
        </FormRow>
        <FormRow
          label="凭据引用"
          hint="只填引用，形如 secret://<scope>/<name>。**绝不要粘贴凭据本身**——留空表示这个调用方没有 API Key"
        >
          <input
            className={fieldClass}
            placeholder="secret://integration/xxx"
            value={form.credential_ref}
            onChange={(e) => setForm({ ...form, credential_ref: e.target.value })}
          />
        </FormRow>
        <FormRow label="备注">
          <input
            className={fieldClass}
            value={form.notes}
            onChange={(e) => setForm({ ...form, notes: e.target.value })}
          />
        </FormRow>
      </div>
      <ActionFeedback error={mutation.error} />
      <div className="flex gap-2">
        <button type="submit" className={fieldClass} disabled={mutation.isPending}>
          {mutation.isPending ? "提交中…" : "提交"}
        </button>
        <button type="button" className={fieldClass} onClick={() => setOpen(false)}>
          取消
        </button>
      </div>
    </form>
  );
}

/** Action 失败时把服务端的话原样显示出来。
 *
 *  不自己编一句「保存失败」：服务端的错误文案里写着到底哪个字段不对
 *  （比如「credential_ref 必须形如 secret://<scope>/<name>」），换成通用文案
 *  等于把唯一有用的信息丢掉。 */
function ActionFeedback({ error }: { error: unknown }) {
  if (!error) return null;
  const message = error instanceof Error ? error.message : String(error);
  return (
    <p role="alert" className="rounded-md border border-danger bg-danger/10 px-3 py-2 text-xs text-fg">
      {message}
    </p>
  );
}

// ---------------------------------------------------------------------------
// 自动化流程：只有登记，没有执行器
// ---------------------------------------------------------------------------

function AutomationRulesTab({ query }: { query: QueryLike<AutomationRulesResponse> }) {
  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title="自动化流程"
        description="「当 X 发生时执行 Y」的规则登记。产品负责人 2026-09-08 裁定这一格先只做只读：可以登记规则，但平台没有执行器。"
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
      />
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {query.data ? <AutomationRulesBody data={query.data} /> : null}
      </ApiStateView>
      <BlueprintPreview tabId="flows" />
    </section>
  );
}

function ruleColumns(): readonly DataTableColumn<AutomationRuleItem>[] {
  return [
    {
      id: "name",
      header: "规则",
      primary: true,
      value: (row) => `${row.name} ${row.description}`,
      cell: (row) => (
        <div className="flex flex-col">
          <span className="font-medium">{row.name}</span>
          {row.description ? <span className="text-xs text-fg-muted">{row.description}</span> : null}
        </div>
      ),
    },
    {
      id: "trigger",
      header: "触发条件",
      value: (row) => `${row.trigger_kind} ${row.trigger_detail}`,
      cell: (row) => (
        <div className="flex flex-col">
          <span>{TRIGGER_KIND_LABELS[row.trigger_kind] ?? row.trigger_kind}</span>
          {row.trigger_detail ? (
            <span className="font-mono text-xs text-fg-muted [overflow-wrap:anywhere]">
              {row.trigger_detail}
            </span>
          ) : null}
        </div>
      ),
    },
    {
      id: "target",
      header: "目标操作",
      value: (row) => row.target_action_id,
      cell: (row) => (
        <div className="flex flex-col">
          <span className="font-mono text-xs [overflow-wrap:anywhere]">
            {row.target_action_id}@{row.target_action_version}
          </span>
          {row.target_action_registered ? (
            <span className="text-xs text-fg-muted">
              风险等级 {row.target_action_risk_level}（若将来真执行，这一档决定要不要人批）
            </span>
          ) : (
            <span className="text-xs text-fg-muted">这个操作当前没有注册——登记指向了一个不存在的动作</span>
          )}
        </div>
      ),
    },
    {
      id: "status",
      header: "登记状态",
      value: (row) => row.status,
      cell: (row) => (
        <Badge tone="neutral">{RULE_STATUS_LABELS[row.status] ?? row.status}</Badge>
      ),
    },
    {
      id: "updated",
      header: "最近修改",
      value: (row) => row.updated_at,
      cell: (row) => (
        <div className="flex flex-col">
          <span>{formatTime(row.updated_at)}</span>
          <span className="text-xs text-fg-muted">{row.updated_by}</span>
        </div>
      ),
    },
    {
      id: "runs",
      header: "运行记录",
      // 恒为这一句，不是「暂无」：没有执行器就不会有运行，将来也不会自己
      // 冒出来。写成「暂无」会让人以为等一等就有了。
      cell: () => <span className="text-xs text-fg-muted">不会运行</span>,
    },
  ];
}

function AutomationRulesBody({ data }: { data: AutomationRulesResponse }) {
  return (
    <div className="flex flex-col gap-3">
      <NoExecutorNotice data={data} />
      <RegisterRuleForm />
      <DataTableV2
        caption="自动化规则登记簿"
        columns={ruleColumns()}
        rows={data.items}
        rowKey={(row) => row.id}
        searchable
        renderExpanded={(row) => <RuleDetail row={row} />}
        emptyState={
          <PageState
            kind="empty"
            title="还没有登记任何规则"
            description="登记簿是空的。登记一条规则是记录意图，不会让它跑起来。"
            compact
          />
        }
      />
    </div>
  );
}

/** 行展开：改登记状态。
 *
 *  按钮文字刻意是「改登记状态」而不是「启用 / 停用」：后者会让人以为这是
 *  一个运行开关，而这张表没有执行器。 */
function RuleDetail({ row }: { row: AutomationRuleItem }) {
  const queryClient = useQueryClient();
  const [status, setStatus] = useState(row.status);
  const mutation = useMutation({
    mutationFn: () => setAutomationRuleStatus({ rule_id: row.id, status }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: [AUTOMATION_RULES_QUERY] }),
  });
  return (
    <div className="flex flex-col gap-3 text-xs">
      <dl className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <div>
          <dt className="text-fg-muted">备注</dt>
          <dd className="text-fg">{row.notes || "—"}</dd>
        </div>
        <div>
          <dt className="text-fg-muted">登记 / 最近修改</dt>
          <dd className="text-fg">
            {formatTime(row.created_at)}（{row.created_by}） / {formatTime(row.updated_at)}（
            {row.updated_by}）
          </dd>
        </div>
      </dl>
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          mutation.mutate();
        }}
      >
        <FormRow label="登记状态" hint="三个取值都不会让这条规则跑起来">
          <select className={fieldClass} value={status} onChange={(e) => setStatus(e.target.value)}>
            {Object.entries(RULE_STATUS_LABELS).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </FormRow>
        <button type="submit" className={fieldClass} disabled={mutation.isPending}>
          改登记状态
        </button>
      </form>
      <ActionFeedback error={mutation.error} />
    </div>
  );
}

/** 这一格最要紧的一块。
 *
 *  它不是免责声明而是这一格的主要内容：一张列着「当 X 发生时执行 Y」的表，
 *  如果不把「不会执行」说在最前面，任何人看一眼都会以为配了就生效。 */
function NoExecutorNotice({ data }: { data: AutomationRulesResponse }) {
  return (
    <section className="flex flex-col gap-2 rounded-lg border border-warning bg-warning/15 p-4">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-sm font-medium text-fg">规则登记在此，但当前不会自动执行</h3>
        <Badge tone={data.automatic_execution ? "danger" : "neutral"}>
          自动执行：{data.automatic_execution ? "开" : "关"}
        </Badge>
      </div>
      <p className="text-xs text-fg">{data.execution_note}</p>
      <p className="text-xs text-fg-muted">
        所以这张表里的「已登记」不等于「已生效」：三个登记状态都不会让任何操作跑起来。
        要真执行，需要先有一次单独的裁定（规则引擎能执行到哪个风险等级），再建执行器与审批接线。
      </p>
    </section>
  );
}

function RegisterRuleForm() {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({
    name: "",
    description: "",
    trigger_kind: "event",
    trigger_detail: "",
    target_action_id: "",
    target_action_version: "1",
    notes: "",
  });
  const mutation = useMutation({
    mutationFn: () =>
      setAutomationRule({
        name: form.name.trim(),
        description: form.description.trim(),
        trigger_kind: form.trigger_kind,
        trigger_detail: form.trigger_detail.trim(),
        target_action_id: form.target_action_id.trim(),
        target_action_version: form.target_action_version.trim(),
        notes: form.notes.trim(),
      }),
    onSuccess: () => {
      setOpen(false);
      void queryClient.invalidateQueries({ queryKey: [AUTOMATION_RULES_QUERY] });
    },
  });

  if (!open) {
    return (
      <div>
        <button type="button" className={fieldClass} onClick={() => setOpen(true)}>
          ＋ 登记规则
        </button>
      </div>
    );
  }
  return (
    <form
      className="flex flex-col gap-3 rounded-lg border border-edge bg-surface p-4"
      onSubmit={(e) => {
        e.preventDefault();
        mutation.mutate();
      }}
    >
      <h3 className="text-sm font-medium text-fg">登记规则</h3>
      <p className="text-xs text-fg-muted">
        提交后这条规则会出现在下面的表里，**但不会被执行**：平台没有规则执行器。
      </p>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <FormRow label="规则名">
          <input
            className={fieldClass}
            required
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
        </FormRow>
        <FormRow label="说明">
          <input
            className={fieldClass}
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
          />
        </FormRow>
        <FormRow label="触发类别" hint="只被记录，不被订阅、不被轮询、不被监听">
          <select
            className={fieldClass}
            value={form.trigger_kind}
            onChange={(e) => setForm({ ...form, trigger_kind: e.target.value })}
          >
            {Object.entries(TRIGGER_KIND_LABELS).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </FormRow>
        <FormRow label="触发条件" hint="自由文本（cron 串、事件名、来源地址…）">
          <input
            className={fieldClass}
            value={form.trigger_detail}
            onChange={(e) => setForm({ ...form, trigger_detail: e.target.value })}
          />
        </FormRow>
        <FormRow label="目标操作 ID" hint="形如 finance.upstream_account.set">
          <input
            className={fieldClass}
            required
            value={form.target_action_id}
            onChange={(e) => setForm({ ...form, target_action_id: e.target.value })}
          />
        </FormRow>
        <FormRow label="目标操作版本">
          <input
            className={fieldClass}
            required
            value={form.target_action_version}
            onChange={(e) => setForm({ ...form, target_action_version: e.target.value })}
          />
        </FormRow>
        <FormRow label="备注">
          <input
            className={fieldClass}
            value={form.notes}
            onChange={(e) => setForm({ ...form, notes: e.target.value })}
          />
        </FormRow>
      </div>
      <ActionFeedback error={mutation.error} />
      <div className="flex gap-2">
        <button type="submit" className={fieldClass} disabled={mutation.isPending}>
          {mutation.isPending ? "提交中…" : "提交"}
        </button>
        <button type="button" className={fieldClass} onClick={() => setOpen(false)}>
          取消
        </button>
      </div>
    </form>
  );
}

// ---------------------------------------------------------------------------
// Webhook：保持蓝图态 + 如实列出已有的出站通知通道
// ---------------------------------------------------------------------------

/** 平台今天真实存在的四条出站通知链路。
 *
 *  这**不是** Webhook 订阅表：它们是「告警外发」，把消息推给运营看的群机器人，
 *  不是把业务事件推给外部系统消费。两者的差别不在措辞上——业务事件 Webhook
 *  要有订阅登记、事件类型、签名与重试，这四条一样都没有。
 *
 *  数据写在前端是因为它描述的是**代码事实**而不是运行时数据：每一行都能在
 *  下面注明的那个源文件里逐字核对到。 */
const OUTBOUND_CHANNELS: readonly {
  domain: string;
  trigger: string;
  ref: string;
  delivery: string;
  source: string;
}[] = [
  {
    domain: "告警（企业微信群机器人）",
    trigger: "worker 每轮告警评估",
    ref: "secret://alerts/wecom-webhook（由 XM_ALERT_WECOM_WEBHOOK_REF 指定）",
    delivery: "**有**投递状态：落在告警行上（notify_status / notify_error / notified_at）",
    source: "internal/platform/alerts/notify.go",
  },
  {
    domain: "告警（Telegram Bot）",
    trigger: "worker 每轮告警评估",
    ref: "由 XM_ALERT_TELEGRAM_BOT_REF 指定",
    delivery: "**有**投递状态：与上一条共用告警行上的那三列",
    source: "internal/platform/alerts/notify.go",
  },
  {
    domain: "卡片事件（企业微信群机器人）",
    trigger: "收到 Infini 回调时",
    ref: "secret://cards/notify-webhook",
    delivery: "**没有**投递记录：推送是即发即忘，成功与失败都不落库",
    source: "internal/platform/cards/notify.go",
  },
  {
    domain: "接码验证码（企业微信群机器人）",
    trigger: "取到验证码时",
    ref: "secret://sms/notify-webhook",
    delivery: "**没有**投递记录：同上",
    source: "internal/platform/sms/notify.go",
  },
];

function WebhooksTab({ label }: { label: string }) {
  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={label}
        description="平台今天没有业务事件 Webhook。这一格先把「已有的出站通知通道」如实列出来，并说清它与 Webhook 订阅不是同一件事。"
      />
      <section className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4">
        <header className="flex flex-wrap items-baseline justify-between gap-2">
          <h3 className="text-sm font-medium text-fg">出站通知通道（告警外发，不是业务事件 Webhook）</h3>
          <p className="text-xs text-fg-muted">来源：仓库代码，不是运行时数据</p>
        </header>
        <div className="overflow-x-auto">
          <table className="w-full text-left text-xs">
            <caption className="sr-only">平台今天真实存在的四条出站通知链路及其凭据引用与投递记录情况</caption>
            <thead className="text-fg-muted">
              <tr>
                <th scope="col" className="px-2 py-2 font-medium">通道</th>
                <th scope="col" className="px-2 py-2 font-medium">何时发</th>
                <th scope="col" className="px-2 py-2 font-medium">凭据引用</th>
                <th scope="col" className="px-2 py-2 font-medium">投递记录</th>
                <th scope="col" className="px-2 py-2 font-medium">代码位置</th>
              </tr>
            </thead>
            <tbody className="text-fg">
              {OUTBOUND_CHANNELS.map((channel) => (
                <tr key={channel.domain} className="border-t border-edge align-top">
                  <td className="px-2 py-2">{channel.domain}</td>
                  <td className="px-2 py-2">{channel.trigger}</td>
                  <td className="px-2 py-2 font-mono [overflow-wrap:anywhere]">{channel.ref}</td>
                  <td className="px-2 py-2">{channel.delivery}</td>
                  <td className="px-2 py-2 font-mono text-fg-muted [overflow-wrap:anywhere]">
                    {channel.source}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="text-xs text-fg-muted">
          群机器人地址本身就是凭据，所以只显示引用、不显示地址（宪法 7 条）。
          引用有没有登记、能不能读到，在「设置 → 密钥引用」里看。
        </p>
      </section>
      <BlueprintPreview tabId="webhooks" />
    </section>
  );
}

// ---------------------------------------------------------------------------
// 运行记录：真有的两类在别处，流程运行记录不存在
// ---------------------------------------------------------------------------

const RUN_RECORD_SOURCES: readonly {
  kind: string;
  status: string;
  where: ReactNode;
}[] = [
  {
    kind: "Action 执行记录（谁在什么时候做了哪个写操作）",
    status: "有",
    where: (
      <Link to="/actions?sub=runs" className="text-accent hover:underline">
        操作与审批 → 执行记录
      </Link>
    ),
  },
  {
    kind: "告警投递状态（那条告警发出去没有）",
    status: "有，但只覆盖告警那条通道",
    where: (
      <Link to="/alerts" className="text-accent hover:underline">
        告警与故障
      </Link>
    ),
  },
  {
    kind: "卡片事件 / 接码验证码的推送结果",
    status: "没有",
    where: <>两条通道即发即忘，成功与失败都不落库——见「Webhook」一格那张表</>,
  },
  {
    kind: "流程运行记录",
    status: "没有",
    where: <>没有执行器就没有运行；规则登记在「自动化流程」一格，但不会跑</>,
  },
  {
    kind: "HTTP 读请求（谁拉取了哪份数据）",
    status: "没有可查询记录",
    where: <>只进进程访问日志（http_request 行），不落库、没有端点</>,
  },
];

function RunsTab({ label }: { label: string }) {
  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={label}
        description="这一页涉及的运行记录分五类，真有的两类各在别处，另外三类今天不存在。这张表先把它们说清楚，免得在这里找一份根本没有的记录。"
      />
      <section className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4">
        <h3 className="text-sm font-medium text-fg">五类运行记录，今天各在哪里</h3>
        <div className="overflow-x-auto">
          <table className="w-full text-left text-xs">
            <caption className="sr-only">这一页涉及的五类运行记录及其当前落点</caption>
            <thead className="text-fg-muted">
              <tr>
                <th scope="col" className="px-2 py-2 font-medium">记录种类</th>
                <th scope="col" className="px-2 py-2 font-medium">有没有</th>
                <th scope="col" className="px-2 py-2 font-medium">在哪里看</th>
              </tr>
            </thead>
            <tbody className="text-fg">
              {RUN_RECORD_SOURCES.map((row) => (
                <tr key={row.kind} className="border-t border-edge align-top">
                  <td className="px-2 py-2">{row.kind}</td>
                  <td className="px-2 py-2">{row.status}</td>
                  <td className="px-2 py-2">{row.where}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
      <BlueprintPreview tabId="runs" />
    </section>
  );
}
