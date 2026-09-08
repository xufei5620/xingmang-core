import { navItemByPath, navLabel, PageHeader, PageState } from "@xingmang/ui-admin";
import { Badge, Tabs } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import {
  BlueprintTabView,
  BlueprintTiles,
  blueprintForPath,
  type BlueprintTab,
} from "../blueprints";
import { ExtAppCatalogPanel } from "../components/ExtAppCatalogPanel";
import { ExtAppReleasesPanel } from "../components/ExtAppReleasesPanel";

const EXT_APP_SUB_TABS = (navItemByPath("/ext/app")?.item.subTabs ?? []).map(
  (tab) => [tab.id, tab.label] as const,
);

/** 默认子页 = 信息架构冻结的第一格，而它恰好也是接了真数据的那一格。 */
const EXT_APP_DEFAULT_SUB = "catalog";

/** 结构（页签、列头、统计格标签）取自冻结的蓝图规格。 */
const EXT_APP_BLUEPRINT = blueprintForPath("/ext/app");

/** 应用与配置页（/ext/app）。
 *
 *  「应用」= **平台自己纳管的前端站点**（ADMIN-IA §5.4.1，产品负责人
 *  2026-09-08 定义）：admin-web、console 这类我们自己部署的前端。不是被管
 *  平台（Sub2API / NewAPI）的前端，也不是通用低代码页面搭建器。
 *
 *  §5.4 原本裁定扩展能力四页只读蓝图、不得因此提前建后端；产品负责人当日
 *  推翻了这条对本页的适用（裁定变更逐字记在 §5.4）。四格里两格接了真数据：
 *
 *   - `应用目录`   → core.ext_app，GET /api/v1/ext/apps
 *   - `版本与发布` → core.ext_app_release，GET /api/v1/ext/apps/releases
 *
 *  另外两格（页面配置 / 页面组件）**保持蓝图态**：它们是页面搭建器的概念，
 *  平台今天没有这个对象——不是排期没到，是这个功能还没有被决定要不要做。
 *  列头照旧、表体是「未接入」，每格写清在等什么（同 ChangesPage 的做法）。
 *
 *  这一页**不挂** PlaceholderPage 那条「只读蓝图：仅预览、不保存、不发布、
 *  不执行」——它现在真的会保存，那句话已经变成假话。取而代之的是下面
 *  ExtAppGate 那条如实的门禁说明：本页只登记，不发布。 */
export function ExtAppPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const activeSub = rawSub === null || rawSub.trim() === "" ? EXT_APP_DEFAULT_SUB : rawSub;
  const known = EXT_APP_SUB_TABS.some(([value]) => value === activeSub);

  // 认不出来的 ?sub= 不静默回落到第一格：那会让一个拼错的地址看起来像正常
  // 页面，而人以为自己看的是别的东西（同 ChangesPage / OpsPage 的处理）。
  if (!known) {
    return (
      <section>
        <PageHeader
          title={navLabel("/ext/app")}
          description="应用与配置的四格按各自的阻塞逐步接入；未知地址不会静默回落到应用目录。"
        />
        <PageState
          kind="unavailable"
          title={`「${rawSub}」子页尚未接入`}
          description="请从已定义的应用与配置子页中选择。"
          action={
            <Link
              to={`/ext/app?sub=${EXT_APP_DEFAULT_SUB}`}
              className="text-sm font-medium text-accent hover:underline"
            >
              返回应用目录
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
        title={navLabel("/ext/app")}
        status={<Badge tone="warning">部分接入</Badge>}
        description={EXT_APP_BLUEPRINT?.description}
      />
      <ExtAppGate />
      <ExtAppTiles />
      <Tabs
        value={activeSub}
        onValueChange={selectSub}
        items={EXT_APP_SUB_TABS.map(([value, label]) => ({
          value,
          label,
          content: renderExtAppSubTab(value, label),
        }))}
      />
    </section>
  );
}

function renderExtAppSubTab(value: string, label: string): ReactNode {
  switch (value) {
    case "catalog":
      return <CatalogTab label={label} />;
    case "releases":
      return <ReleasesTab label={label} />;
    default:
      return <BlueprintOnlyTab tabId={value} label={label} />;
  }
}

/** 页面级门禁说明（同 ChangesPage 的 ChangesGate、ActionsPage 的
 *  AdvancedControlsGate 一条纪律）。
 *
 *  挂在 Tabs 外面而不是塞进某一格：「本页不发布任何东西」是整页的事实，
 *  只写在一格里的话，落在别的格的人根本看不到。
 *
 *  这一条**替换掉**了 PlaceholderPage 给 /ext/* 挂的那句「只读蓝图：仅预览、
 *  不保存、不发布、不执行」——这一页现在真的会保存（登记站点、记录发布都是
 *  真写入），继续挂那句就是在页面上说假话。它没保留的那半（不保存）不再成立，
 *  保留的那半（不发布、不执行）反而更要说清楚，因为这一页里出现了「版本与
 *  发布」这几个字，很容易被读成这里能发版。 */
function ExtAppGate() {
  return (
    <p
      role="status"
      className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
    >
      门禁：本页只做登记，不发布、不回滚、不重启任何站点。发布与回滚是
      Platform Lifecycle Operation（宪法 2、3 条）：走版本化脚本 + 人工批准，不经
      Action 通道，后台也没有注册任何 deploy.* / release.* 执行动作。「版本与发布」
      那一格记录的是「已经发生」的发布，登记这件事本身走 Action 与审计链
      （extapp.release.record@1）。状态列同样是登记值，不是探活结果——平台一次都
      不请求这些站点。
    </p>
  );
}

/** 四张统计格里只有第一张有来源。
 *
 *  蓝图给每格的 note 是**口径**，它没说这个数今天取不到。三个「—」摆在
 *  「应用」那个真数字旁边，很容易被读成「今天恰好都是 0」——所以没来源的
 *  每格在口径后面补一句「今天没有来源：…」，把「没接」与「真的是 0」分开
 *  （同 ChangesPage 的 TILE_WAITING）。
 *
 *  「应用」这一格**不在这里显示数字**：它的数在应用目录那一格的表上方，
 *  由真实查询给出。在页头再摆一份会立刻变成第二处要维护的读数，而且它得
 *  在四格里独自处理加载中 / 读取失败两种状态——那两种状态在一排统计格里
 *  没有诚实的表达方式。 */
const TILE_WAITING: Readonly<Record<string, string>> = {
  "应用": "这个数在下面「应用目录」那一格的表上方，随查询一起给出（含读取失败时的说明）。",
  "配置草稿": "今天没有来源：配置版本属于页面搭建器，平台没有这个对象。",
  "待审核": "今天没有来源：同上——没有配置版本，也就没有等待审核的配置。",
  "正式版本":
    "今天没有来源：这一格数的是「配置版本」，不是代码发布。代码发布记录在「版本与发布」那一格。",
};

const TILE_WAITING_FALLBACK = "今天没有来源：这一格的数据源尚未接入，具体在等什么见下方各页签。";

function ExtAppTiles() {
  const tiles = EXT_APP_BLUEPRINT?.tiles;
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
// 接了真数据的两格
// ---------------------------------------------------------------------------

function CatalogTab({ label }: { label: string }) {
  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={label}
        description="平台自己纳管的前端站点：域名、环境、登录方式、当前发布的版本、负责人与状态。全部手工登记——平台不请求这些站点，状态与域名都不做探测。"
      />
      <ExtAppCatalogPanel />
    </section>
  );
}

function ReleasesTab({ label }: { label: string }) {
  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={label}
        description="发布记录簿：哪个应用在什么时候上了哪个版本、谁发的、是发布还是回滚。只记录已经发生的发布，本页不触发任何发布。"
      />
      <ExtAppReleasesPanel />
      <ExtAppBlueprintPreview tabId="releases" />
    </section>
  );
}

// ---------------------------------------------------------------------------
// 保持蓝图态的两格
// ---------------------------------------------------------------------------

/** 页头那一行的一句话结论。比蓝图落款短：细节在落款里，这里只说这一格
 *  在等的是哪一类东西（等裁定，不是等排期）。 */
const TAB_HEADLINE: Readonly<Record<string, string>> = {
  pages: "这一格等的不是排期：平台没有页面搭建器，要不要做这个功能本身还没有裁定。",
  components: "同上——组件库属于页面搭建器，那个对象今天不存在。",
};

/** 蓝图态的结构预览（列头 / 字段名在，内容是「未接入」）。
 *
 *  复用 BlueprintTabView 而不是另写一套：这几格的结构是**冻结的设计产出**
 *  （blueprints.test.ts 与 navigation.ts 逐字对账），后续真做的切片照它实现。
 *  在这里重抄一遍，等于给同一份契约开第二个副本。 */
function ExtAppBlueprintPreview({ tabId }: { tabId: string }) {
  const tab: BlueprintTab | undefined = EXT_APP_BLUEPRINT?.tabs.find((item) => item.id === tabId);
  if (!tab) {
    // 走到这里说明导航数据与蓝图规格漂开了。显示出来而不是渲染空白：
    // 一格什么都不显示，看起来与「这一格没有内容」一模一样。
    return (
      <PageState
        kind="unavailable"
        title="蓝图规格里没有这一格"
        description={`导航有子页签「${tabId}」，但 blueprints/ext.ts 的应用与配置规格里没有同名条目——两份数据已经漂开，请先对齐再看这一格。`}
      />
    );
  }
  return <BlueprintTabView tab={tab} />;
}

function BlueprintOnlyTab({ tabId, label }: { tabId: string; label: string }) {
  return (
    <section className="flex flex-col gap-3">
      <PageHeader title={label} description={TAB_HEADLINE[tabId]} />
      <ExtAppBlueprintPreview tabId={tabId} />
    </section>
  );
}
