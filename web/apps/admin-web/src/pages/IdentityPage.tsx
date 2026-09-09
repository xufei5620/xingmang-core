import { navItemByPath, navLabel, PageHeader, PageState } from "@xingmang/ui-admin";
import { Tabs } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import { BlueprintTabView, blueprintForPath, type BlueprintTab } from "../blueprints";
import { StaffAccountsPanel } from "../components/StaffAccountsPanel";

const IDENTITY_SUB_TABS = (navItemByPath("/identity")?.item.subTabs ?? []).map(
  (tab) => [tab.id, tab.label] as const,
);

const DEFAULT_SUB_TAB = "accounts";

/** 结构（列头、筛选条文案、说明块）取自冻结的蓝图规格。
 *
 *  只借结构、不借文案里的**归因**：见下面 TAB_SOURCE 的注释。 */
const IDENTITY_BLUEPRINT = blueprintForPath("/identity");

/** 人员与权限页。
 *
 *  五个子页里只有「账号与身份」接了真实数据（XM-LOGIN：员工账号的建立、
 *  改角色、禁用/启用、重置密码）。其余四格此前一律是同一句「尚未实现」——
 *  那句话对其中一格是**错的**：「密钥引用」要的凭据管理早就整页可用了，
 *  只是它挂在设置底下（`/settings?sub=credentials`），这一格假装它不存在。
 *
 *  这一片（XM-IDENTITY-BINDING）做两件事：
 *    1. 「密钥引用」改成交叉跳转——如实说凭据管理在哪、已经能做什么、
 *       这一格自己还差哪几列。**不做 IA 搬迁**：把凭据从设置搬进人员与
 *       权限要改导航真相源与 ADMIN-IA 文档，那是另一件事；
 *    2. 其余三格（权限规则 / 权限范围 / 会话）渲染蓝图列头 + 逐格写清
 *       在等什么，照 pages/ChangesPage.tsx 的做法。
 *
 *  结构仍是 pages/AuditPage.tsx 的子页签模式：外层只做「认不认识这个
 *  ?sub=」的判断，认识就交给 Tabs，每个子页自己决定渲染什么。 */
export function IdentityPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const activeSub = rawSub === null || rawSub.trim() === "" ? DEFAULT_SUB_TAB : rawSub;
  const known = IDENTITY_SUB_TABS.some(([value]) => value === activeSub);

  if (!known) {
    return (
      <section>
        <PageHeader
          title={navLabel("/identity")}
          description="账号与身份、权限规则、权限范围、密钥引用与会话分开呈现；未知子页不会回落到账号列表。"
        />
        <PageState
          kind="unavailable"
          title={`「${rawSub}」子页尚未接入`}
          description="请从已定义的子页中选择；系统不会把未知地址误当成账号列表。"
          action={
            <Link
              to={`/identity?sub=${DEFAULT_SUB_TAB}`}
              className="text-sm font-medium text-accent hover:underline"
            >
              返回账号与身份
            </Link>
          }
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

  // 这一页**不渲染**蓝图的页级统计格，也不渲染蓝图的页顶横幅：
  //   - 四张统计格（人员账号 / 程序与设备身份 / 高风险权限 / 异常密钥引用）
  //     一律是「—」，摆在「账号与身份」那张真实账号表上面，会让整页看起来
  //     像还没建——而这一页恰恰有一格是真的；
  //   - 蓝图横幅原话是「仅 UI 设计 · 本页不执行任何真实操作」，这句今天在
  //     这一页上是**假的**：账号与身份能建账号、改角色、重置密码。
  //  ChangesPage 渲染这两样是因为它整页确实没有写入口，与这里不同。
  return (
    <Tabs
      value={activeSub}
      onValueChange={selectSub}
      items={IDENTITY_SUB_TABS.map(([value, label]) => ({
        value,
        label,
        content: renderIdentitySubTab(value, label),
      }))}
    />
  );
}

function renderIdentitySubTab(value: string, label: string): ReactNode {
  switch (value) {
    case "accounts":
      return <IdentityAccountsPage />;
    case "credentials":
      return <IdentityCredentialsTab label={label} />;
    default:
      return <BlueprintOnlyTab tabId={value} label={label} />;
  }
}

function IdentityAccountsPage() {
  return (
    <section>
      <PageHeader
        title={navLabel("/identity")}
        description="管理台自带账号密码登录（local 模式）的员工账号：创建账号、改角色、禁用/启用、重置密码。"
      />
      <StaffAccountsPanel />
    </section>
  );
}

// ---------------------------------------------------------------------------
// 逐格「在等什么」
// ---------------------------------------------------------------------------

/** 每一格的落款，**覆盖**蓝图规格里的同名文案。
 *
 *  为什么不直接用蓝图的 `source`（那样两处永远一致，还省一份数据）：
 *  blueprints/governance.ts 里这五句都写着「随 F-A 上线」，而 F-A 里已经交付
 *  了东西——「账号与身份」（XM-LOGIN）与「密钥引用」（XM-CRED0 的凭据管理页）
 *  都已经在跑。一句「随 F-A 上线」会让人以为剩下三格也只是排期问题，而它们
 *  各自缺的东西完全不同：权限规则缺的是「可编辑的授权策略」这个对象本身，
 *  权限范围与会话缺的是一条只读 Query。
 *
 *  蓝图数据文件不归这一片改（它同时被 blueprints.test.ts 与占位页消费），
 *  所以这里就地覆写，并在交接里请求把 governance.ts 的五句一并订正。
 *  这条做法本身照抄 pages/ChangesPage.tsx 的 TAB_SOURCE。 */
const TAB_SOURCE: Readonly<Record<string, string>> = {
  rules:
    "平台今天没有「授权策略」这个可编辑对象：没有规则表、没有只读端点，也没有任何 policy.* / rule.* Action。授权由两样**部署期**的东西决定——路由上写死的 RequireScope（internal/platform/httpapi/router.go）与「角色 → 平台 scope」的翻译表（internal/platform/oidcauth/rolemap.go 的 DefaultRoleScopeMap，可由环境变量 XM_OIDC_ROLE_SCOPES 覆盖；local 与 oidc 两种登录模式共用这张表）。两者都改代码或改部署配置才动得了，不是后台里能编辑的规则。所以这一格等的不是排期，是先要有那个对象。下面那条恒定的 DENY 是个例外：它今天**已经生效**，但生效点在审批内核里（internal/platform/approval：非 HUMAN 主体投票直接被拒），不在任何规则表里。",
  scopes:
    "矩阵的数据今天存在，但取不出来：DefaultRoleScopeMap（internal/platform/oidcauth/rolemap.go）就是「角色集合 × scope」那张表，local 模式也走它（localauth.RoleScopesFrom）。缺的是一条把它读出来的只读 Query——外加定它归 staff.manage 还是另开一个 scope。注意它与「设置 → 身份与权限（只读）」不是一回事：那一块显示的是**前端这一侧带着哪些 scope 去请求**，不是服务端认可的权限，更不是全平台的角色矩阵。",
  sessions:
    "库表已经有了，端点还没有：core.staff_session（迁移 000021，XM-LOGIN）存着本地登录会话，000024 又给它加了 mfa_at（最近一次 TOTP 校验通过的时刻）。但路由里搜不到任何列会话的端点，LocalAuthHandlers 接口上也只有登录/登出/查自己/改密码/列账号/TOTP 那几项——没有「列出所有活跃会话」。缺的是一条只读 Query 加它的权限归属。另外：oidc 模式下的会话在 Keycloak 里，平台这边根本不落库，那一半要另说。",
};

/** 页头那一行的一句话结论。与 TAB_SOURCE 分开写：落款那段有一百多字，摆在
 *  页头再摆一遍，人第二次读到时已经不看了——要立刻看见的是「这一格在等的是
 *  哪一类东西」，细节留给落款。 */
const TAB_HEADLINE: Readonly<Record<string, string>> = {
  rules: "这一格等的不是排期：平台还没有「可编辑的授权策略」这个对象。",
  scopes: "这一格等的是一条只读 Query——角色 × scope 的矩阵今天只存在于代码与部署配置里。",
  sessions: "这一格等的是一条只读 Query：本地会话已经落库，但没有任何端点能把它列出来。",
};

/** 表体空状态里的一句话。比页签落款短：详细归因在落款里，这里只说这张表
 *  为什么没有行。 */
const TABLE_SOURCE: Readonly<Record<string, string>> = {
  rules: "这张表要的是一份可编辑的规则清单，而平台里没有规则这个对象——不是查出来是空的。",
  scopes: "矩阵的内容在 DefaultRoleScopeMap 里，但没有端点能把它取出来。",
  sessions: "本地会话记在 core.staff_session，但还没有只读端点能把它列出来。",
  credentials:
    "这张表的行今天在「设置 → 凭据管理」，不在这里；上面那张卡说明了两边各覆盖哪几列。",
};

/** 取蓝图里的一格，并把落款换成上面那份如实的归因。 */
function honestBlueprintTab(tabId: string): BlueprintTab | undefined {
  const tab = IDENTITY_BLUEPRINT?.tabs.find((item) => item.id === tabId);
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
 *  复用 BlueprintTabView 而不是另写一套表：这一页每一格的列结构是**冻结的
 *  设计产出**（blueprints.test.ts 与 navigation.ts 逐字对账），后续接数据的
 *  切片照它实现。在这里重抄一遍列头，等于给同一份契约开第二个副本。 */
function IdentityBlueprintPreview({ tabId }: { tabId: string }) {
  const tab = honestBlueprintTab(tabId);
  if (!tab) {
    // 走到这里说明导航数据与蓝图规格漂开了。显示出来而不是渲染空白：
    // 一格什么都不显示，看起来与「这一格没有内容」一模一样。
    return (
      <PageState
        kind="unavailable"
        title="蓝图规格里没有这一格"
        description={`导航有子页签「${tabId}」，但 blueprints/governance.ts 的人员与权限规格里没有同名条目——两份数据已经漂开，请先对齐再看这一格。`}
      />
    );
  }
  return <BlueprintTabView tab={tab} />;
}

/** 没有任何真实读数的三格（权限规则 / 权限范围 / 会话）。 */
function BlueprintOnlyTab({ tabId, label }: { tabId: string; label: string }) {
  return (
    <section className="flex flex-col gap-3">
      <PageHeader title={label} description={TAB_HEADLINE[tabId]} />
      <IdentityBlueprintPreview tabId={tabId} />
    </section>
  );
}

// ---------------------------------------------------------------------------
// 密钥引用：凭据管理早就能用了，这一格此前一直说「尚未实现」
// ---------------------------------------------------------------------------

function IdentityCredentialsTab({ label }: { label: string }) {
  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={label}
        description="凭据管理已经整页可用，只是挂在设置底下。这一格给出入口，并说明它与蓝图这张表还差哪几列。"
      />
      <CredentialsCrossLinkCard />
      <IdentityBlueprintPreview tabId="credentials" />
    </section>
  );
}

/** 「凭据管理在哪、已经能做什么、这一格还差什么」。
 *
 *  这张卡存在的理由是**这一格之前在说假话**：它显示「『密钥引用』尚未实现 ·
 *  阶段 F-A：随后续切片实现」，而 XM-CRED0 的凭据管理页早就整页可用
 *  （`/settings?sub=credentials`，三块面板全接了真实端点）。看到那句话的人
 *  会以为平台还没有凭据登记这回事，于是去别处找——或者干脆不找。
 *
 *  **只做交叉跳转，不把凭据管理搬过来**：ADMIN-IA 计划里「凭据从设置迁到
 *  人员与权限」是一次 IA 搬迁，要动导航真相源与 docs/architecture/ADMIN-IA.md，
 *  不在这一片的范围。把同一个页面在两个地址各渲染一份，比放一个链接更糟：
 *  两处的面包屑、返回路径与保存后的落点会立刻分叉。 */
function CredentialsCrossLinkCard() {
  return (
    <section className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4">
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-medium text-fg">凭据管理已经可用（在「设置」底下）</h3>
        <p className="text-xs text-fg-muted">XM-CRED0</p>
      </header>
      <p className="text-xs leading-5 text-fg-muted">
        平台需要哪些凭据（缺失 / 已配置）、各平台接入模式（fake / real）、以及全部凭据
        元数据的轮换与吊销，都在<span className="text-fg">设置 → 凭据管理</span>里，
        端点一直是通的。写入走 credential.* / connector.* Action，
        <span className="text-fg">值只在 Action 请求期间存在，保存后不会回读到页面</span>
        ——那一页能看到的只有引用名、scope、指纹前八位与版本。
      </p>
      <p className="text-xs leading-5 text-fg-muted">
        那一页覆盖了蓝图这张表的
        <span className="text-fg">「密钥引用（CredentialRef）」「用途」「状态」</span>
        三列，另外还多给了 scope、指纹前八位、版本与更新时间。这一格自己还差的是
        <span className="text-fg">「供应方」「消费者」「环境」「最近使用」「轮换 / 到期」</span>
        ——那几列要的是「谁在用这条引用、上次什么时候用的」，平台今天没有采集这份事实。
      </p>
      <Link
        to="/settings?sub=credentials"
        className="self-start text-sm font-medium text-accent hover:underline focus-visible:outline-2 focus-visible:outline-accent"
      >
        打开凭据管理 →
      </Link>
      <p className="text-xs text-fg-muted">
        这一格今天只是入口，不是第二份凭据页：把凭据从设置迁到这里是一次信息架构
        搬迁（ADMIN-IA），要另开切片。
      </p>
    </section>
  );
}
