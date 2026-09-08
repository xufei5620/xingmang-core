import { useQuery } from "@tanstack/react-query";
import {
  FreshnessBadge,
  PageHeader,
  PageState,
  ServiceStatusBadge,
  formatDuration,
  formatUtcTimestamp,
  navItemByPath,
  navLabel,
} from "@xingmang/ui-admin";
import { Badge, EmptyState, Tabs } from "@xingmang/ui-primitives";
import { useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import { appApiConfig } from "../api/config";
import {
  listConnections,
  listConnectors,
  listMetrics,
  listServices,
  serviceFreshness,
  SERVICE_STALENESS_THRESHOLD_SECONDS,
  type ConnectionItem,
  type ConnectorItem,
  type ServiceItem,
} from "../api/platform";
import { ApiStateView } from "../components/ApiStateView";
import { ObserveServiceDialog } from "../components/ObserveServiceDialog";
import { RegisterServiceDialog } from "../components/RegisterServiceDialog";

const TH = "px-3 py-2 text-left text-xs font-medium text-fg-muted";
const TD = "px-3 py-2 align-top text-sm text-fg";

/** 信息架构给 /registry 冻结的六个子页签（ui-admin/navigation.ts）。 */
const REGISTRY_SUB_TABS = (navItemByPath("/registry")?.item.subTabs ?? []).map(
  (tab) => [tab.id, tab.label] as const,
);

const REGISTRY_DEFAULT_SUB = "services";

/** 每一格的口径。
 *
 *  「连接器」与「连接」只差一个字，而它们回答的是完全不同的问题；三格摞在
 *  一起最容易被读成同一张表的三半。所以每一格都自带一句口径（§12 惯例）。 */
const TAB_HINT: Readonly<Record<string, string>> = {
  services: "平台管着哪些被管系统实例。按环境隔离；采集时间按新鲜度语义显示——「实例在册」不等于「数据是新的」。",
  connectors:
    "有哪几种连接实现。类型目录，全平台一份，**不按环境分**——core.connector 没有 environment 列。",
  connections: "谁用哪个连接器连上了哪个服务实例。按环境隔离，这里只列当前身份所属环境的。",
};

/** 注册表：被管理系统实例、连接器目录与实际连接。
 *
 *  XM-0034 起从「服务清单」改挂到平台治理段（ADMIN-IA 三、迁移映射），
 *  路径由 /services 改为 /registry——旧地址仍会重定向过来。
 *
 *  **子页签是 XM-READONLY-QUERIES 补上的**：信息架构给这一页声明了六个子页签
 *  （navigation.ts），而在此之前这一页根本不渲染 Tabs——`?sub=connectors`
 *  静默显示服务表。那比缺功能更糟：地址栏说你在看连接器，屏幕上给的是服务。 */
export function RegistryPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const activeSub = rawSub === null || rawSub.trim() === "" ? REGISTRY_DEFAULT_SUB : rawSub;
  const known = REGISTRY_SUB_TABS.some(([value]) => value === activeSub);

  // 三个 query 都挂在页面级而不是各自的格里：连接那一格要靠服务与连接器两份
  // 数据把外键 UUID 换成名字，拆到格里之后切一次页签就得重取。
  //
  // `enabled: known` 让一个拼错的地址不白打三次请求——hooks 仍然无条件调用，
  // 顺序不变（条件式 hook 是另一类 bug）。
  const query = useQuery({
    queryKey: ["services"],
    queryFn: ({ signal }) => listServices({ signal }),
    enabled: known,
  });
  // 只为拿到「当前身份属于哪个环境」而顺带读一次指标；已经缓存过就不会再发请求
  const metricsQuery = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
    enabled: known,
  });

  // 连接器与连接（XM-READONLY-QUERIES）。两条各自成一个 query 而不是并成
  // 一个：两个端点的失败面不同（连接器不按环境筛，连接会因跨环境 403），
  // 合成一个之后一边坏了会把另一边也变成错误态。
  const connectorsQuery = useQuery({
    queryKey: ["connectors"],
    queryFn: ({ signal }) => listConnectors({ signal }),
    enabled: known,
  });
  const connectionsQuery = useQuery({
    queryKey: ["connections"],
    queryFn: ({ signal }) => listConnections({ signal }),
    enabled: known,
  });

  const [notice, setNotice] = useState<string | null>(null);

  const environment = resolveEnvironment(
    query.data ?? [],
    (metricsQuery.data ?? []).map((m) => m.environment),
  );

  const afterWrite = (message: string) => {
    setNotice(message);
    void query.refetch();
  };

  // 认不出来的 ?sub= **不静默回落**到第一格（同 ChangesPage / FinancePage）：
  // 一个悄悄换了内容的地址，会让贴链接的人以为对方看到的是自己那一屏。
  // 这一页此前的行为正是那种静默——`?sub=connectors` 给的是服务表。
  if (!known) {
    return (
      <section>
        <PageHeader
          title={navLabel("/registry")}
          description="资源目录的六个子页按各自的阻塞逐步接入；未知地址不会静默回落到服务。"
        />
        <PageState
          kind="unavailable"
          title={`「${rawSub}」子页尚未接入`}
          description="请从已定义的资源目录子页中选择。"
          action={
            <Link
              to={`/registry?sub=${REGISTRY_DEFAULT_SUB}`}
              className="text-sm font-medium text-accent hover:underline"
            >
              返回服务
            </Link>
          }
        />
      </section>
    );
  }

  const selectSub = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("sub", value);
    // 换子页签用 replace：连点六格不该在浏览器里堆六条历史。
    setSearchParams(next, { replace: true });
  };

  const renderSubTab = (value: string, label: string): ReactNode => {
    switch (value) {
      case "services":
        return (
          <TabShell label={label} hint={TAB_HINT.services ?? ""}>
            <ApiStateView
              isPending={query.isPending}
              error={query.error}
              onRetry={() => void query.refetch()}
            >
              <ServicesTable items={query.data ?? []} onObserved={afterWrite} />
            </ApiStateView>
          </TabShell>
        );
      case "connectors":
        return (
          <TabShell label={label} hint={TAB_HINT.connectors ?? ""}>
            <ApiStateView
              isPending={connectorsQuery.isPending}
              error={connectorsQuery.error}
              onRetry={() => void connectorsQuery.refetch()}
            >
              <ConnectorsTable items={connectorsQuery.data ?? []} />
            </ApiStateView>
          </TabShell>
        );
      case "connections":
        return (
          <TabShell label={label} hint={TAB_HINT.connections ?? ""}>
            <ApiStateView
              isPending={connectionsQuery.isPending}
              error={connectionsQuery.error}
              onRetry={() => void connectionsQuery.refetch()}
            >
              <ConnectionsTable
                items={connectionsQuery.data ?? []}
                connectors={connectorsQuery.data ?? []}
                services={query.data ?? []}
              />
            </ApiStateView>
          </TabShell>
        );
      default:
        return <PendingTab tabId={value} label={label} />;
    }
  };

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={navLabel("/registry")}
        description={`采集时间超过 ${formatDuration(SERVICE_STALENESS_THRESHOLD_SECONDS)} 记为数据延迟（阈值由前端设定，registry 未提供）。这是一个对所有服务一刀切的临时值，各服务的真实阈值待后端契约提供（XM-0017）。`}
        // 三张表一起刷新：一个「刷新」按钮只刷当前那一格，会让另外两格停在旧
        // 数据上而页头显示的是刚刚的时间——那比不刷新更容易误导。
        onRefresh={() => {
          void query.refetch();
          void connectorsQuery.refetch();
          void connectionsQuery.refetch();
        }}
        refreshing={query.isFetching || connectorsQuery.isFetching || connectionsQuery.isFetching}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
        actions={
          <RegisterServiceDialog
            environment={environment}
            onRegistered={(runId) =>
              // 不承诺「一定能在审计页看到」：业务写、ActionRun、审计追加三段不是
              // 原子的，审计还是 fail-open（#38/#40 未关闭），写成功而审计没落库
              // 是可能发生的。UI 不能把一个尚未成立的后端保证说成事实（Codex #10）
              afterWrite(`已登记，run_id=${runId}；审计事件通常几秒内出现在审计页`)
            }
          />
        }
      />

      {/* 三张表的**后端一直是全的**：表在迁移 000001，三个写 Action 也早就注册着。
          缺的只是只读端点，XM-READONLY-QUERIES 把那两条补上了。这句要说准——
          写成「两张表尚未建」会让人以为要从建表开始（那正是这一页此前的措辞）。 */}
      <p className="text-xs text-fg-muted">
        服务、连接器、连接三张表<span className="text-fg">都已建好</span>（迁移
        000001），三个写入动作也早已注册；此前这一页只列得出服务，缺的是连接器与连接的
        <span className="text-fg">只读端点</span>，现已补上。写入仍只走 Action：登记服务是
        L1，登记连接器是 L2、建立连接是 L3——后两者要过审批中心，所以这一页只有服务那一格带写入按钮。
      </p>

      {notice ? (
        // status 而不是 alert：这是一条成功回执，不该抢走屏幕阅读器的当前焦点
        <p
          role="status"
          className="rounded-md border border-success bg-success/10 px-3 py-2 text-xs text-success"
        >
          {notice}
        </p>
      ) : null}

      <Tabs
        value={activeSub}
        onValueChange={selectSub}
        items={REGISTRY_SUB_TABS.map(([value, label]) => ({
          value,
          label,
          content: renderSubTab(value, label),
        }))}
      />
    </section>
  );
}

/** 一格的外框：标题 + 一句口径。 */
function TabShell({
  label,
  hint,
  children,
}: {
  label: string;
  hint: string;
  children: ReactNode;
}) {
  return (
    <section className="flex flex-col gap-2">
      <PageHeader title={label} description={hint} />
      {children}
    </section>
  );
}

/** 后端不存在的三格：逐格说清「在等什么」，而不是一句「尚未接入」。
 *
 *  三格等的是三类不同的东西——一个等新的聚合投影、一个等这个对象先被定义、
 *  一个等「要不要读一份被 CHECK 钉死的常量枚举」这个判断。一句话把它们抹平，
 *  只会让人以为都在排队等排期（同 ChangesPage 那五格的做法）。 */
const PENDING_TAB_COPY: Readonly<Record<string, string>> = {
  capabilities:
    "能力清单今天**不是一张独立的表**：读写能力登记在连接器上（core.connector 的 read_capabilities / write_capabilities 两列），已经逐行显示在「连接器」那一格里。这一格要的是另一个投影——按能力反查「哪些连接器支持它、哪条连接被授了它」。那需要一条新的聚合 Query，后端今天没有。在它之前，先去「连接器」与「连接」两格看逐行的能力列。",
  apps:
    "平台里没有「应用 / 模块」这个对象：core schema 只有 environment / service / connector / connection 四张表，没有应用登记表，路由表里也没有对应端点。这一格等的不是接线，是先定义这个对象要记什么。",
  environments:
    "环境在库里是 core.environment（迁移 000001），但它只有三行，而且被 CHECK 约束钉死成 development / staging / production——它是一份常量枚举，不是运营可增删的登记簿。今天没有读它的端点，而在有之前，「当前身份属于哪个环境」从上面各表的「环境」列就看得出来。这一格等的是「要不要为一份常量单开一条 Query」这个判断。",
};

function PendingTab({ tabId, label }: { tabId: string; label: string }) {
  return (
    <section className="flex flex-col gap-2">
      <PageHeader title={label} />
      <PageState
        kind="unavailable"
        title={`「${label}」尚未接入`}
        description={PENDING_TAB_COPY[tabId] ?? "该子页尚未接入。"}
      />
    </section>
  );
}

function ConnectorsTable({ items }: { items: ConnectorItem[] }) {
  if (items.length === 0) {
    return (
      <EmptyState
        title="暂无连接器"
        description="还没有登记任何连接器类型版本。登记走 registry.connector.create（L2），要过审批中心，本页不提供入口。"
      />
    );
  }
  return (
    <div className="overflow-x-auto rounded-lg border border-edge bg-surface shadow-sm">
      <table className="w-full border-collapse">
        <thead className="border-b border-edge bg-surface-muted">
          <tr>
            <th className={TH}>连接器</th>
            <th className={TH}>契约版本</th>
            <th className={TH}>目标白名单</th>
            <th className={TH}>读能力</th>
            <th className={TH}>写能力</th>
            <th className={TH}>兼容上游版本</th>
          </tr>
        </thead>
        <tbody>
          {items.map((c) => (
            <tr key={c.id} className="border-b border-edge last:border-b-0">
              <td className={TD}>
                <span className="font-medium">{c.key}</span>
                <p className="font-mono text-xs text-fg-muted">v{c.version}</p>
              </td>
              <td className={TD}>
                <span className="font-mono text-xs">{c.contract_version}</span>
                <p className="font-mono text-xs break-all text-fg-muted">
                  {c.connection_schema_path}
                </p>
              </td>
              <td className={TD}>
                <CapabilityList values={c.target_allowlist} empty="未声明" />
              </td>
              <td className={TD}>
                <CapabilityList values={c.read_capabilities} empty="无" />
              </td>
              <td className={TD}>
                {/* 写能力非空 = 这个连接器能改上游状态，对应的连接必须配
                    Kill Switch（ADR-004）。用 warning 徽章而不是普通文本：
                    这一列是「有没有写通道」，不是一个规格参数。 */}
                {c.write_capabilities.length === 0 ? (
                  <span className="text-xs text-fg-muted">无（只读）</span>
                ) : (
                  <div className="flex flex-col gap-1">
                    <Badge tone="warning">有写通道</Badge>
                    <CapabilityList values={c.write_capabilities} empty="无" />
                  </div>
                )}
              </td>
              <td className={TD}>
                {/* 空数组 = 没有声明，不是「兼容所有版本」——这句区分要写在
                    界面上，否则一个空格子会被读成「都能用」。 */}
                <CapabilityList values={c.supported_upstream_versions} empty="未声明兼容版本" />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ConnectionsTable({
  items,
  connectors,
  services,
}: {
  items: ConnectionItem[];
  connectors: ConnectorItem[];
  services: ServiceItem[];
}) {
  if (items.length === 0) {
    return (
      <EmptyState
        title="暂无连接"
        description="该环境下还没有建立任何连接。建立连接走 registry.connection.create（L3），要过审批中心，本页不提供入口。"
      />
    );
  }
  // 端点回的是 UUID。这里就地换成人认得出的名字，认不出时**原样显示 UUID**
  // 而不是留空——留空会让「这条连接指向一个已经不在清单里的对象」看起来
  // 像「这一列没有数据」。
  const connectorLabel = new Map(connectors.map((c) => [c.id, `${c.key} v${c.version}`]));
  const serviceLabel = new Map(services.map((s) => [s.id, s.instance_id]));
  return (
    <div className="overflow-x-auto rounded-lg border border-edge bg-surface shadow-sm">
      <table className="w-full border-collapse">
        <thead className="border-b border-edge bg-surface-muted">
          <tr>
            <th className={TH}>服务实例</th>
            <th className={TH}>连接器</th>
            <th className={TH}>状态</th>
            <th className={TH}>凭据引用</th>
            <th className={TH}>已授能力</th>
            <th className={TH}>上游版本</th>
            <th className={TH}>最近核验</th>
          </tr>
        </thead>
        <tbody>
          {items.map((c) => (
            <tr key={c.id} className="border-b border-edge last:border-b-0">
              <td className={TD}>
                <ResolvedRef label={serviceLabel.get(c.service_id)} id={c.service_id} />
              </td>
              <td className={TD}>
                <ResolvedRef label={connectorLabel.get(c.connector_id)} id={c.connector_id} />
              </td>
              <td className={TD}>
                <ConnectionStatusBadge status={c.status} killSwitch={c.kill_switch} />
              </td>
              <td className={TD}>
                {/* 引用不是凭据（ADR-014）。值一步都不进这个端点——
                    只有 SecretProvider 碰得到它（宪法 7 条）。 */}
                <span className="font-mono text-xs break-all text-fg-muted">
                  {c.credential_ref}
                </span>
              </td>
              <td className={TD}>
                <CapabilityList values={c.granted_capabilities} empty="无" />
              </td>
              <td className={TD}>
                {c.detected_upstream_version ? (
                  <span className="font-mono text-xs">{c.detected_upstream_version}</span>
                ) : (
                  <span className="text-xs text-fg-muted">未探测</span>
                )}
              </td>
              <td className={TD}>
                {/* null = 从未核验，与「核验过但很久以前」不是一回事 */}
                {c.last_verified_at === null ? (
                  <span className="text-xs text-fg-muted">从未核验</span>
                ) : (
                  <span className="text-xs">{formatUtcTimestamp(c.last_verified_at)}</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** 连接状态。killed 用 danger——Kill Switch 已拉闸不是一个普通的「停用」。 */
function ConnectionStatusBadge({ status, killSwitch }: { status: string; killSwitch: string }) {
  const tone = status === "enabled" ? "success" : status === "killed" ? "danger" : "neutral";
  const label = status === "enabled" ? "已启用" : status === "killed" ? "已拉闸" : "已停用";
  return (
    <div className="flex flex-col gap-1">
      <Badge tone={tone}>{label}</Badge>
      {killSwitch ? (
        <span className="font-mono text-xs text-fg-muted">闸 {killSwitch}</span>
      ) : null}
    </div>
  );
}

/** 一列字符串。空集合显示调用方给的那句话，而不是留白——留白说不清是
 *  「没有」还是「没取到」（§12 页面状态纪律）。 */
function CapabilityList({ values, empty }: { values: string[]; empty: string }) {
  if (values.length === 0) return <span className="text-xs text-fg-muted">{empty}</span>;
  return (
    <ul className="flex flex-col gap-0.5">
      {values.map((v) => (
        <li key={v} className="font-mono text-xs break-all">
          {v}
        </li>
      ))}
    </ul>
  );
}

/** 一个外键引用：认得出就显示名字并把 UUID 留在下面，认不出就只显示 UUID。 */
function ResolvedRef({ label, id }: { label: string | undefined; id: string }) {
  if (label === undefined) {
    return (
      <div className="flex flex-col gap-0.5">
        <span className="font-mono text-xs break-all">{id}</span>
        <span className="text-xs text-fg-muted">不在本页清单里</span>
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-0.5">
      <span className="font-medium">{label}</span>
      <span className="font-mono text-xs break-all text-fg-muted">{id}</span>
    </div>
  );
}

/** 当前身份所属环境。
 *
 *  前端并不真的知道服务端把自己配成了哪个环境（config.ts 说明了为什么默认不传
 *  environment），但写 Action 时 environment 是必填参数。于是按可信度取值：
 *  显式配置 > 已有服务记录 > 已有指标记录。三样都没有就返回空串，
 *  由表单显示「未知」并禁用提交——猜一个只会换来 409/403。 */
export function resolveEnvironment(services: ServiceItem[], metricEnvironments: string[]): string {
  if (appApiConfig.environment) return appApiConfig.environment;
  const fromService = services.find((s) => s.environment)?.environment;
  if (fromService) return fromService;
  return metricEnvironments.find((e) => e) ?? "";
}

function ServicesTable({
  items,
  onObserved,
}: {
  items: ServiceItem[];
  onObserved: (message: string) => void;
}) {
  if (items.length === 0) {
    return (
      <EmptyState
        title="暂无服务"
        description="该环境下还没有登记任何被管理系统实例；用右上角的「登记服务」按钮登记第一个"
      />
    );
  }
  return (
    <div className="overflow-x-auto rounded-lg border border-edge bg-surface shadow-sm">
      <table className="w-full border-collapse">
        <thead className="border-b border-edge bg-surface-muted">
          <tr>
            <th className={TH}>实例</th>
            <th className={TH}>类型</th>
            <th className={TH}>环境</th>
            <th className={TH}>负责人</th>
            <th className={TH}>状态</th>
            <th className={TH}>接入地址</th>
            <th className={TH}>数据新鲜度</th>
            <th className={TH}>
              <span className="sr-only">操作</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {items.map((s) => (
            <tr key={s.id} className="border-b border-edge last:border-b-0">
              <td className={TD}>
                <span className="font-medium">{s.instance_id}</span>
                {s.source_watermark ? (
                  <p className="font-mono text-xs text-fg-muted">水位 {s.source_watermark}</p>
                ) : null}
              </td>
              <td className={TD}>{s.service_type}</td>
              <td className={TD}>{s.environment}</td>
              <td className={TD}>{s.owner}</td>
              <td className={TD}>
                <ServiceStatusBadge status={s.status} />
              </td>
              <td className={TD}>
                {/* 不做成可点链接：清单页点出去容易误触生产后台，
                    真要跳转走服务详情页（后续任务） */}
                <span className="font-mono text-xs break-all text-fg-muted">{s.endpoint}</span>
              </td>
              <td className={TD}>
                <ServiceFreshnessCell service={s} />
              </td>
              <td className={TD}>
                <ObserveServiceDialog
                  service={s}
                  onObserved={(runId) =>
                    onObserved(
                      `已上报观测，run_id=${runId}；审计事件通常几秒内出现在审计页`,
                    )
                  }
                />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ServiceFreshnessCell({ service }: { service: ServiceItem }) {
  const freshness = serviceFreshness(service);
  return (
    <div className="flex flex-col gap-1">
      <FreshnessBadge freshness={freshness} />
      <span className="text-xs text-fg-muted">
        {service.observed_at === null
          ? "从未成功采集"
          : `数据时间 ${formatUtcTimestamp(service.observed_at)}`}
      </span>
      {service.stale_seconds === null ? null : (
        <span className="text-xs text-fg-muted">落后 {formatDuration(service.stale_seconds)}</span>
      )}
    </div>
  );
}
