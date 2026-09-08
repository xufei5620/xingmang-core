import { useQuery } from "@tanstack/react-query";
import { navItemByPath, navLabel, PageHeader, PageState } from "@xingmang/ui-admin";
import { Badge, Tabs } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import { listApprovals } from "../api/approvals";
import { FeatureNotMountedError } from "../api/client";
import { getOpsOverview, type OpsOverview } from "../api/ops";
import { getMigrations, type MigrationsReport } from "../api/platform";
import {
  BlueprintTabView,
  BlueprintTiles,
  blueprintForPath,
  type BlueprintTab,
} from "../blueprints";
import { ApiStateView } from "../components/ApiStateView";

const CHANGES_SUB_TABS = (navItemByPath("/changes")?.item.subTabs ?? []).map(
  (tab) => [tab.id, tab.label] as const,
);

/** 默认子页 = 信息架构冻结的第一格。
 *
 *  这里**不**照 OpsPage 那样把默认落在「唯一接了真数据的一格」：这一页有两格
 *  各带一条真实读数（变更单格的「审批中心接没接」、发布格的「现在跑的是哪个
 *  提交」），挑其中一格当默认就是在替人排优先级，不如与侧栏顺序一致。 */
const CHANGES_DEFAULT_SUB = "requests";

/** 结构（页签、列头、统计格标签、筛选条文案）取自冻结的蓝图规格。
 *
 *  只借结构、不借文案里的**归因**：见下面 TAB_SOURCE 的注释。 */
const CHANGES_BLUEPRINT = blueprintForPath("/changes");

/** 版本与发布页（/changes）。
 *
 *  五格里今天有真实读数的只有两条，且两条都很窄：
 *
 *  - 「发布与回滚」能回答「控制平面**现在**跑的是哪个提交」（GET
 *    /api/v1/ops/overview 的 build 字段）。它不是发布历史——平台没有发布
 *    记录表，一次回滚之后这条读数直接变成回滚后的提交，不留上一条。
 *  - 「变更单」能回答「审批中心在这套后台接没接」（GET /api/v1/approvals
 *    通不通）。它回答的**不是**这一格要的变更单台账，见 ApprovalCenterScopeCard。
 *
 *  其余三格（自动测试与质量 / 发布包与安全检查 / 数据库变更）后端一条端点
 *  都没有，保持蓝图态：列头照旧、表体是「未接入」，每格写清在等什么。 */
export function ChangesPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const activeSub = rawSub === null || rawSub.trim() === "" ? CHANGES_DEFAULT_SUB : rawSub;
  const known = CHANGES_SUB_TABS.some(([value]) => value === activeSub);

  // 认不出来的 ?sub= 不静默回落到第一格：那会让一个拼错的地址看起来像正常
  // 页面，而人以为自己看的是别的东西（同 OpsPage / ActionsPage 的处理）。
  if (!known) {
    return (
      <section>
        <PageHeader
          title={navLabel("/changes")}
          description="版本与发布的五格按各自的阻塞逐步接入；未知地址不会静默回落到变更单。"
        />
        <PageState
          kind="unavailable"
          title={`「${rawSub}」子页尚未接入`}
          description="请从已定义的版本与发布子页中选择。"
          action={
            <Link
              to={`/changes?sub=${CHANGES_DEFAULT_SUB}`}
              className="text-sm font-medium text-accent hover:underline"
            >
              返回变更单
            </Link>
          }
        />
      </section>
    );
  }

  const selectSub = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("sub", value);
    // 换子页签用 replace：连点五格不该在浏览器里堆五条历史。
    setSearchParams(next, { replace: true });
  };

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={navLabel("/changes")}
        status={<Badge tone="warning">部分接入</Badge>}
        description={CHANGES_BLUEPRINT?.description}
      />
      <ChangesGate />
      <ChangesTiles />
      <Tabs
        value={activeSub}
        onValueChange={selectSub}
        items={CHANGES_SUB_TABS.map(([value, label]) => ({
          value,
          label,
          content: renderChangesSubTab(value, label),
        }))}
      />
    </section>
  );
}

function renderChangesSubTab(value: string, label: string): ReactNode {
  switch (value) {
    case "requests":
      return <ChangeRequestsTab label={label} />;
    case "releases":
      return <ReleasesTab label={label} />;
    case "database":
      return <DatabaseTab label={label} />;
    default:
      return <BlueprintOnlyTab tabId={value} label={label} />;
  }
}

/** 页面级门禁说明（与 ActionsPage 的 AdvancedControlsGate 同一条纪律）。
 *
 *  挂在 Tabs 外面而不是塞进某一格：「本页不执行任何发布动作」是整页的事实，
 *  只写在一格里的话，落在别的格的人根本看不到。 */
function ChangesGate() {
  return (
    <p
      role="status"
      className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
    >
      门禁：本页只读，不提供发布、回滚、重跑流水线或执行迁移的入口。发布与迁移是
      Platform Lifecycle Operation（宪法 2、3 条）：走版本化脚本 + 人工批准，不经
      Action 通道，后台也没有注册任何 release.* / change.* / deploy.* Action。这一页
      今天只有两条真实读数——当前部署的构建信息、审批中心在本环境接没接；其余各格
      没有数据源，逐格在等什么写在各自的页签里。
    </p>
  );
}

/** 四张统计格今天全部没有来源，主数位恒为「—」（蓝图的 StatTile unavailable）。
 *
 *  蓝图给每格的 note 是**口径**（「已提交、等待评审的变更单数」），它没说这个
 *  数今天取不到。四个「—」摆在一起，很容易被读成「今天恰好都是 0」——所以每格
 *  在口径后面补一句「今天没有来源：…」，把「没接」与「真的是 0」分开。 */
const TILE_WAITING: Readonly<Record<string, string>> = {
  "待评审变更": "今天没有来源：变更单还没有在平台里落库，CR 仍是仓库里的 Markdown。",
  "等待生产批准":
    "今天没有来源：平台没有发布记录表。审批中心里的单是 Action 执行审批，不是发布批准，不能拿来充数。",
  "自动测试失败":
    "今天没有来源：GitHub Actions 自 2026-08-29 停摆，过渡期改由分支 Handoff + 服务器本地门禁承担。",
  "供应链异常": "今天没有来源：平台镜像没有签名、SBOM 与漏洞扫描环节。",
};

const TILE_WAITING_FALLBACK = "今天没有来源：这一格的数据源尚未接入，具体在等什么见下方各页签。";

function ChangesTiles() {
  const tiles = CHANGES_BLUEPRINT?.tiles;
  if (!tiles || tiles.length === 0) return null;
  return (
    <BlueprintTiles
      tiles={tiles.map((tile) => ({
        ...tile,
        note: `${tile.note}。${TILE_WAITING[tile.label] ?? TILE_WAITING_FALLBACK}`,
      }))}
    />
  );
}

// ---------------------------------------------------------------------------
// 逐格「在等什么」
// ---------------------------------------------------------------------------

/** 每一格的落款，**覆盖**蓝图规格里的同名文案。
 *
 *  为什么不直接用蓝图的 `source`（那样两处永远一致，还省一份数据）：
 *  blueprints/governance.ts 里那五句今天都写着「随 Foundation-B（XM-0030）
 *  上线」，而 Foundation-B 的审批中心已经在 2026-09-07 启用了——五格里没有
 *  任何一格是在等它。一句错的归因比没有归因更糟：它让人以为只要等排期就行，
 *  而实际上有的格等的是产品负责人拍板（变更单要不要进平台）、有的等一次复审
 *  （CI 双轨恢复还是废止）、有的等供应链环节本身先存在。
 *
 *  蓝图数据文件不归这一片改（它同时被 blueprints.test.ts 与占位页消费），
 *  所以这里就地覆写，并在交接里请求把 governance.ts 的五句一并订正。 */
const TAB_SOURCE: Readonly<Record<string, string>> = {
  requests:
    "变更单本体今天不在平台里：没有 change_request 表、没有只读端点，也没有任何 change.* Action。真实的变更单是仓库 docs/change-requests/ 下的 CR-xxxx（Markdown），后台没有读仓库文件的路径。审批中心覆盖的是 Action 执行审批，不覆盖这张台账（见上方那张卡）。要不要把变更单搬进平台登记，待产品负责人裁定——这一格等的是那个裁定，不是排期。",
  releases:
    "平台没有发布记录表，也没有发布 / 回滚端点：上面那张卡回答的是「现在跑的是哪个提交」，不是发布历史——回滚之后它会直接变成回滚后的提交，不留上一条。已发生的发布记录目前只在仓库的 docs/handoffs/ACCEPTANCE-LOG.md 与 docs/handoffs/RELEASE-*.md 里，尚不可查询。",
  tests:
    "没有数据源，连由谁来供都还没定：GitHub Actions 自 2026-08-29 起停摆，过渡期改由「分支内 Handoff + 服务器本地门禁状态」承担追溯义务（宪法 §六，有效期至 2026-09-12，产品负责人须在该日前复审是恢复还是正式废止 Actions/PR 双轨）。那次复审之前，这一格接什么都定不下来。另注：后台任务页的运行记录不是 CI，别拿它顶这一格。",
  packages:
    "平台自己的镜像没有供应链环节：全仓找不到 cosign / syft / trivy / grype 的调用，镜像由 deploy/scripts/deploy-local.sh 在目标机上 compose build，不推 registry，因此没有 digest 台账、没有 SBOM、没有签名、也没有漏洞扫描结果。（开票线是另一条独立流水线，它自带镜像门禁与签名，但那条线的产物不经过这个后台。）这一格等的不是接线，是先要有这些环节。",
  database:
    "迁移是 Platform Lifecycle Operation（宪法 2、3 条）：走版本化脚本 + 人工批准，不经 Action 通道——所以这一格与 Foundation-B 无关。只读 Query 已接（GET /api/v1/ops/migrations，权限 ops.read）：版本号来自生产库的 public.schema_migrations，脚本清单来自二进制里嵌着的 db/migrations。蓝图那张表还要「迁移前后行数 / 执行人 / 验证 / 回滚路径」，平台没有逐条迁移台账，这四列取不到——下面显示的是取得到的那部分，取不到的不编。",
};

/** 页头那一行的一句话结论。
 *
 *  与 TAB_SOURCE 分开写，而不是两处摆同一段：落款那段有一百多字，摆在页头再摆
 *  一遍，人第二次读到时已经不看了——真正要立刻看见的是「这一格在等的是哪一类
 *  东西」（等裁定 / 等复审 / 等环节本身），细节留给落款。 */
const TAB_HEADLINE: Readonly<Record<string, string>> = {
  tests: "这一格今天没有数据源，也还没定由谁来供——它等的是一次复审，不是排期。",
  packages: "这一格等的不是接线：平台自己的镜像还没有签名、SBOM 与漏洞扫描环节。",
  database:
    "这一格接的是库自己记着的迁移版本：跑到哪一版、有没有卡在半截、二进制里还带着几版没跑。",
};

/** 表体空状态里的一句话。比页签落款短：详细归因在落款里，这里只说这张表为什么没有行。 */
const TABLE_SOURCE: Readonly<Record<string, string>> = {
  requests: "这张表还没有落库面：变更单今天是仓库里的 Markdown，不是平台里的记录。",
  releases: "这张表要的是发布历史，而平台没有发布记录表；上面那张卡只回答「现在跑的是哪个提交」。",
  tests: "CI 结果没有来源：GitHub Actions 停摆中，过渡期由分支 Handoff + 服务器本地门禁承担。",
  packages: "没有签名 / SBOM / 漏洞扫描环节，也就没有这张表的行。",
  database: "已应用的迁移版本见上面那张表；蓝图这张表要的逐条台账平台没有留。",
};

/** 取蓝图里的一格，并把落款换成上面那份如实的归因。 */
function honestBlueprintTab(tabId: string): BlueprintTab | undefined {
  const tab = CHANGES_BLUEPRINT?.tabs.find((item) => item.id === tabId);
  if (!tab) return undefined;
  const source = TAB_SOURCE[tabId];
  const tableSource = TABLE_SOURCE[tabId];
  return {
    ...tab,
    ...(source ? { source } : {}),
    ...(tab.tables && tableSource
      ? { tables: tab.tables.map((table) => ({ ...table, source: tableSource })) }
      : {}),
  };
}

/** 蓝图态的表结构预览（列头在、表体是「未接入」）。
 *
 *  复用 BlueprintTabView 而不是另写一套表：这一页每一格的列结构是**冻结的设计
 *  产出**（blueprints.test.ts 与 navigation.ts 逐字对账），后续接数据的切片照它
 *  实现。在这里重抄一遍列头，等于给同一份契约开第二个副本。 */
function ChangesBlueprintPreview({ tabId }: { tabId: string }) {
  const tab = honestBlueprintTab(tabId);
  if (!tab) {
    // 走到这里说明导航数据与蓝图规格漂开了。显示出来而不是渲染空白：
    // 一格什么都不显示，看起来与「这一格没有内容」一模一样。
    return (
      <PageState
        kind="unavailable"
        title="蓝图规格里没有这一格"
        description={`导航有子页签「${tabId}」，但 blueprints/governance.ts 的版本与发布规格里没有同名条目——两份数据已经漂开，请先对齐再看这一格。`}
      />
    );
  }
  return <BlueprintTabView tab={tab} />;
}

/** 没有任何真实读数的三格（自动测试与质量 / 发布包与安全检查 / 数据库变更）。 */
function BlueprintOnlyTab({ tabId, label }: { tabId: string; label: string }) {
  return (
    <section className="flex flex-col gap-3">
      <PageHeader title={label} description={TAB_HEADLINE[tabId]} />
      <ChangesBlueprintPreview tabId={tabId} />
    </section>
  );
}

// ---------------------------------------------------------------------------
// 变更单：说清审批中心在这一格里管的是哪一部分
// ---------------------------------------------------------------------------

function ChangeRequestsTab({ label }: { label: string }) {
  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={label}
        description="蓝图这一格要的是变更单台账：一次改动的评审、影响面、审批与验证证据。平台里今天没有这个对象，下面那张卡说明与它最容易混淆的审批中心到底管的是哪一部分。"
      />
      <ApprovalCenterScopeCard />
      <ChangesBlueprintPreview tabId="requests" />
    </section>
  );
}

/** 「审批中心刚启用，这一格接的是哪一部分」。
 *
 *  这张卡存在的唯一理由是**防混淆**：审批中心 2026-09-07 才在这套后台接上
 *  （cmd/platform-api/main.go 注入 approvalService），于是「审批链上线了」很容易
 *  被读成「变更单也有了」。两者不是同一件事：
 *
 *  - 审批单冻结的是**一次 Action 调用的参数**，回答「这一次写操作要不要放行」；
 *  - 变更单记的是**一次改动的评审与验证证据**，回答「这个改动凭什么可以上」。
 *
 *  卡上唯一的读数是「端点通不通」。**不显示待审批条数**：蓝图自己写着「审批
 *  执行仍在受控操作中心统一查看」，在这里再摆一份队列长度，会立刻变成第二处
 *  要同时维护的读数，而且极容易被当成蓝图那张「等待生产批准」统计格——那是
 *  发布批准，不是 Action 审批。 */
function ApprovalCenterScopeCard() {
  const query = useQuery({
    // 只探端点通不通，limit=1 足够；内容一律不显示，所以不与 ApprovalQueue
    // 共用 queryKey（那边按状态筛选，缓存的是队列本身）。
    queryKey: ["approvals-reachability-probe"],
    queryFn: ({ signal }) => listApprovals({ limit: 1, signal }),
  });

  return (
    <section className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4">
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-medium text-fg">审批中心在这一格里管的是哪一部分</h3>
        <p className="text-xs text-fg-muted">XM-0030 · 2026-09-07 在本后台启用</p>
      </header>
      <p className="text-xs text-fg-muted">
        审批中心承载的是 <span className="text-fg">Action 执行审批</span>：L2
        及以上的写操作由内核落成一张审批单，冻结那一次调用的参数，批准后由人显式触发执行。
        它回答的是「这一次写操作要不要放行」。
      </p>
      <p className="text-xs text-fg-muted">
        蓝图这一格要的 <span className="text-fg">变更单</span>
        是另一件事：一次改动的评审、影响资源、环境、验证证据与状态。平台里没有这个对象，
        所以审批中心接上并不等于这一格接上了——它覆盖的是审批链，不覆盖变更单台账。
      </p>
      <p className="text-xs text-fg-muted">
        队列、投票与执行入口统一在「操作与审批 → 待审批」（蓝图原话：审批执行仍在受控
        操作中心统一查看）。这一页因此不复制队列，也不显示待审批条数——下面这一行只探端点
        通不通，不取队列内容。
      </p>
      <ApprovalReachability
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      />
      <Link to="/actions?sub=pending" className="text-xs font-medium text-accent hover:underline">
        去「操作与审批 → 待审批」查看审批队列 →
      </Link>
    </section>
  );
}

function ApprovalReachability({
  isPending,
  error,
  onRetry,
}: {
  isPending: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  // 整组路由没挂载（router.go 里 Approvals 为 nil 时 /approvals* 压根不存在）
  // 与「这次请求失败了」不是一回事，两者的下一步也不同。这里不用
  // ApiStateView 的默认说明：api/approvals.ts 那句写的是「等 XM-0030c 先就位」，
  // 而 XM-0030c 已经就位、仓库里也已经接上了——今天看到 404，更可能是这个
  // 部署还停在接上之前的提交。
  if (error instanceof FeatureNotMountedError) {
    return (
      <PageState
        kind="unavailable"
        compact
        title="这套后台还没接审批中心"
        description="GET /api/v1/approvals 返回 404：当前运行的 platform-api 没有注入审批服务。仓库里已按产品负责人 2026-09-07 的指示接上，所以更可能是这个部署还停在接上之前的提交——对照「发布与回滚 → 当前部署」的 Commit。没接的部署上，内核对 L2 及以上一律拒绝执行（ADVANCED_CONTROLS_REQUIRED），不会有动作停在这里等审批。"
      />
    );
  }
  return (
    <ApiStateView isPending={isPending} error={error} onRetry={onRetry} compact>
      <p className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
        <Badge tone="success">审批中心已在本环境启用</Badge>
        <span>
          GET /api/v1/approvals 可达。这一行只说明链路通，不代表队列里有或没有单。
        </span>
      </p>
    </ApiStateView>
  );
}

// ---------------------------------------------------------------------------
// 发布与回滚：当前部署（这一页唯一的真实构建事实）
// ---------------------------------------------------------------------------

function ReleasesTab({ label }: { label: string }) {
  const query = useQuery({
    // 与 OpsPage 共用同一个 queryKey：读的是同一个端点的同一份快照，
    // 分两个键只会在切页时多打一次请求，还让两处显示的时刻可能不同。
    queryKey: ["ops-overview"],
    queryFn: ({ signal }) => getOpsOverview(undefined, undefined, signal),
  });

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={label}
        description="平台没有发布记录表，也没有发布或回滚端点。这一格今天只有一条真实事实：控制平面现在跑的是哪个提交。"
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
      />
      <section className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4">
        <header className="flex flex-wrap items-baseline justify-between gap-2">
          <h3 className="text-sm font-medium text-fg">当前部署</h3>
          <p className="text-xs text-fg-muted">GET /api/v1/ops/overview 的 build 字段</p>
        </header>
        <ApiStateView
          isPending={query.isPending}
          error={query.error}
          onRetry={() => void query.refetch()}
          compact
        >
          {query.data ? <CurrentDeployment data={query.data} /> : null}
        </ApiStateView>
        <p className="text-xs text-fg-muted">
          同源提示：这几行与「运行保障 → 控制平面健康」读的是同一个端点的同一份 build
          字段，不是第二处独立事实；两处对不上就是有一处缓存旧了。
        </p>
        <Link to="/ops?sub=health" className="text-xs font-medium text-accent hover:underline">
          去「运行保障 → 控制平面健康」看同一份 build 信息 →
        </Link>
      </section>
      <ChangesBlueprintPreview tabId="releases" />
    </section>
  );
}

function CurrentDeployment({ data }: { data: OpsOverview }) {
  const { build } = data;
  return (
    <div className="flex flex-col gap-2">
      <dl className="flex flex-col gap-1">
        <DeploymentRow label="环境" value={build.environment} />
        <DeploymentRow label="Commit" value={build.commit} />
        <DeploymentRow
          label="构建标签"
          value={build.version}
          // 刻意不叫「版本」：deploy/scripts/deploy-local.sh 今天把 BUILD_VERSION
          // 写成环境名（`export BUILD_VERSION="$expected_environment"`），
          // 标成「版本」会让人把 production 读成版本号。
          hint="部署脚本今天把它写成环境名，不是语义化版本号"
        />
      </dl>
      {build.commit === "" ? (
        <p className="text-xs text-fg-muted">
          这次部署没有注入 BUILD_COMMIT（deploy/scripts/deploy-local.sh
          正常会写入目标 SHA）。空值说明取不到构建信息，不代表没有发布过。
        </p>
      ) : null}
      <p className="text-xs text-fg-muted">
        这是「现在跑的是什么」，不是发布历史：回滚之后这里直接变成回滚后的提交，不留上一条。
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 数据库变更：库跑到哪一版（XM-READONLY-QUERIES）
// ---------------------------------------------------------------------------

/** 「数据库变更」格。
 *
 *  这一格此前是纯蓝图态，落款写着「缺的是一条新 Query」。那条 Query 现在有了
 *  （GET /api/v1/ops/migrations，ops.read），但它只答得出两件事：
 *
 *  - 库自己记着跑到哪一版、有没有卡在半截（public.schema_migrations 就两列）；
 *  - 二进制里带着哪些脚本、还有几版没跑（db/migrations 嵌进了二进制）。
 *
 *  蓝图那张表还要「迁移前后行数 / 执行人 / 验证 / 回滚路径」——平台**没有**
 *  逐条迁移台账，那四列今天取不到。所以这一格是「真实读数 + 保留蓝图预览」
 *  的组合，而不是把蓝图那张表填满：编四列出来比空着更糟。 */
function DatabaseTab({ label }: { label: string }) {
  const query = useQuery({
    queryKey: ["ops-migrations"],
    queryFn: ({ signal }) => getMigrations({ signal }),
  });

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={label}
        description={TAB_HEADLINE.database}
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
      />
      <section className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4">
        <header className="flex flex-wrap items-baseline justify-between gap-2">
          <h3 className="text-sm font-medium text-fg">迁移状态</h3>
          <p className="text-xs text-fg-muted">GET /api/v1/ops/migrations</p>
        </header>
        <ApiStateView
          isPending={query.isPending}
          error={query.error}
          onRetry={() => void query.refetch()}
          compact
        >
          {query.data ? <MigrationState data={query.data} /> : null}
        </ApiStateView>
        <p className="text-xs text-fg-muted">
          本页只读：执行迁移与回滚是 Platform Lifecycle Operation（宪法 2、3
          条），走版本化脚本 + 变更单 + 人工批准，不经 Action 通道，后台也没有对应端点。
        </p>
      </section>
      <ChangesBlueprintPreview tabId="database" />
    </section>
  );
}

function MigrationState({ data }: { data: MigrationsReport }) {
  const pending = data.items.filter((m) => !m.applied);
  return (
    <div className="flex flex-col gap-3">
      <dl className="flex flex-col gap-1">
        <DeploymentRow label="已应用到" value={`第 ${data.applied_version} 版`} />
        <DeploymentRow label="脚本总数" value={`${data.items.length} 版`} />
      </dl>

      {/* dirty 是这一格最要紧的一位：库卡在半截时服务照常起、端点照常回
          200，别处一点征兆都没有。所以它用 alert 而不是普通文本。 */}
      {data.dirty ? (
        <p
          role="alert"
          className="rounded-md border border-danger bg-danger/10 px-3 py-2 text-xs text-danger"
        >
          库标着 dirty：上一次迁移跑到一半失败了，schema 处在半截状态。这种库既不能继续迁移也不该继续服务
          ——处置见 docs/runbooks/，不要在后台重试，这里也没有重试入口。
        </p>
      ) : (
        <p className="text-xs text-fg-muted">
          dirty = false：最近一次迁移是跑完的，schema 不在半截状态。
        </p>
      )}

      {data.ahead > 0 ? (
        <p
          role="alert"
          className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
        >
          还有 {data.ahead} 版没应用：这个进程的二进制里带着比库更新的迁移脚本，通常意味着镜像更新了而迁移容器没跑（或跑失败了没人看日志）。
          未应用的是第 {pending.map((m) => m.version).join("、")} 版。
        </p>
      ) : (
        <p className="text-xs text-fg-muted">
          没有落后的版本：二进制里带的每一版都已应用。这一行比较的是**这个进程**与它连着的库，不是仓库与库。
        </p>
      )}

      <div className="overflow-x-auto rounded-lg border border-edge">
        <table className="w-full border-collapse">
          <thead className="border-b border-edge bg-surface-muted">
            <tr>
              <th className="px-3 py-2 text-left text-xs font-medium text-fg-muted">编号</th>
              <th className="px-3 py-2 text-left text-xs font-medium text-fg-muted">名称</th>
              <th className="px-3 py-2 text-left text-xs font-medium text-fg-muted">状态</th>
              <th className="px-3 py-2 text-left text-xs font-medium text-fg-muted">
                down 脚本
              </th>
            </tr>
          </thead>
          <tbody>
            {data.items.map((m) => (
              <tr key={m.version} className="border-b border-edge last:border-b-0">
                <td className="px-3 py-2 font-mono text-xs text-fg">{m.version}</td>
                <td className="px-3 py-2 text-sm text-fg">{m.name}</td>
                <td className="px-3 py-2">
                  <Badge tone={m.applied ? "success" : "warning"}>
                    {m.applied ? "已应用" : "未应用"}
                  </Badge>
                </td>
                <td className="px-3 py-2 text-xs text-fg-muted">
                  {/* 刻意不叫「可回滚」：迁移是 forward-only（规格 §5.7），
                      down 脚本存在只是让退路有据可查，不是一个按钮。 */}
                  {m.has_down ? "有" : "无"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="text-xs text-fg-muted">
        「已应用」是按版本号推断的：public.schema_migrations 只记一个版本号加一个 dirty
        标记，库里没有逐条台账，所以不大于那个版本号的都算已应用。也正因为如此，蓝图这张表要的「迁移前后行数 /
        执行人 / 验证 / 回滚路径」四列取不到。
      </p>
    </div>
  );
}

function DeploymentRow({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-2 text-xs">
      <dt className="text-fg-muted">
        {label}
        {hint ? <span className="ml-1 text-fg-muted">（{hint}）</span> : null}
      </dt>
      <dd className="font-mono text-fg [overflow-wrap:anywhere]">{value || "—"}</dd>
    </div>
  );
}
