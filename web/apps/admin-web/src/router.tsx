import { useQuery } from "@tanstack/react-query";
import {
  AdminShell,
  ContextStrip,
  NavItemDisabled,
  NavItemLabel,
  NavSection,
  NavSectionCollapsible,
  navItemClass,
  navStageHint,
  NAV_GROUPS,
  type NavGroupSpec,
} from "@xingmang/ui-admin";
import {
  NavLink,
  Outlet,
  createBrowserRouter,
  redirect,
  useLocation,
  useNavigate,
  type LoaderFunctionArgs,
} from "react-router";
import { appApiConfig } from "./api/config";
import { listServices } from "./api/platform";
import { decodePlatformUserIdSegment, platformHasUsers } from "./api/users";
import { RequireAuth } from "./auth/RequireAuth";
import { currentUserLabel, signOut } from "./auth/session";
import { DemoDataBanner } from "./components/DemoDataBanner";
import { GlobalSearch } from "./components/GlobalSearch";
import { RouteErrorBoundary } from "./components/RouteErrorBoundary";
import { breadcrumbsFor, environmentLabel } from "./lib/breadcrumbs";
import {
  groupPlatforms,
  platformNavHint,
  resolvePlatformTab,
  CHANNELS_REDIRECT,
  LEGACY_PLATFORM_ROUTES,
  type PlatformEntry,
  type RegistryState,
} from "./lib/platforms";
import { ActionsPage } from "./pages/ActionsPage";
import { AlertsPage } from "./pages/AlertsPage";
import { AuditPage } from "./pages/AuditPage";
import { AuthCallbackPage } from "./pages/AuthCallbackPage";
import { ChangePasswordPage } from "./pages/ChangePasswordPage";
import { ChannelDetailPage, isSupplyPlatform } from "./pages/ChannelDetailPage";
import { IdentityPage } from "./pages/IdentityPage";
import { LoginPage } from "./pages/LoginPage";
import { NotFoundPage } from "./pages/NotFoundPage";
import { OverviewPage } from "./pages/OverviewPage";
import { PlaceholderPage } from "./pages/PlaceholderPage";
import { PlatformDetailPage } from "./pages/PlatformDetailPage";
import { PlatformUserDetailPage } from "./pages/PlatformUserDetailPage";
import { RegistryPage } from "./pages/RegistryPage";
import { RequestDetailPage } from "./pages/RequestDetailPage";
import { SettingsPage } from "./pages/SettingsPage";
import { ServerDetailPage } from "./pages/ServerDetailPage";
import { isServerDetailPreviewId } from "./blueprints/server";
import { SupplierCreatePage } from "./pages/SupplierCreatePage";
import { UpstreamDetailPage } from "./pages/UpstreamDetailPage";

// 登录门禁见 ./auth/RequireAuth.tsx（XM-AUTH1）：dev-header 模式看 localStorage 的
// 开发开关，oidc 模式看 sessionStorage 里有没有会话；没有就带着 next 去 /login。

// 导航项的样式由 ui-admin 给（navItemClass）：左栏在深色轨道上，用的是不随主题
// 翻转的 nav-* 令牌，应用侧照着内容区的 surface/fg 再拼一份就会一半亮一半暗。

/** 「平台」段：由 `GET /api/v1/services` 驱动。
 *
 *  段里的条目 = 平台目录（ADMIN-IA v3 §一 分组 2）叠上 Registry 的实际登记情况。
 *  两者都要：只看 Registry，还没接的平台会从导航上消失（§12 惯例不允许）；
 *  只看目录，新登记的平台要等有人改代码才出现（ADMIN-IA 要求「登记即出现」）。
 *
 *  复用 ['services'] 这个 query key：资源目录页本来就要拉它，导航因此在大多数
 *  页面上不产生额外请求。 */
function PlatformNav() {
  const query = useQuery({
    queryKey: ["services"],
    queryFn: ({ signal }) => listServices({ signal }),
  });

  // 读取失败与「读到了、但里面没有」是两件事，必须分开传下去
  const registryState: RegistryState = query.isError
    ? "error"
    : query.isPending
      ? "loading"
      : "ready";

  return (
    <>
      {groupPlatforms(query.data ?? []).map((entry) => (
        <PlatformNavItem
          key={entry.spec.serviceType}
          entry={entry}
          registryState={registryState}
        />
      ))}
    </>
  );
}

function PlatformNavItem({
  entry,
  registryState,
}: {
  entry: PlatformEntry;
  registryState: RegistryState;
}) {
  const { spec } = entry;

  // Registry 还没读到／读失败时，不敢说任何平台「未接入」——那是拿一次
  // 加载中或一次 403 去断言平台没接。说不知道就是说不知道（§9.1 同一条道理）
  if (registryState !== "ready") {
    return (
      <NavItemDisabled
        label={spec.label}
        hint={registryState === "loading" ? "读取中" : "读取失败"}
        title={
          registryState === "loading"
            ? "正在读取服务注册表，暂时无法判断该平台是否已接入"
            : "服务注册表读取失败，无法判断该平台是否已接入"
        }
      />
    );
  }

  // 4 个平台一律可点。原型把它们全画成可进入的（CPA 是占位页、服务器是 wip），
  // 而「点进去只有骨架」这件事由右侧的状态标签与各页签里的占位说清楚——
  // 一个灰掉的条目只能表达「不能点」，表达不了「能看结构、还没有数据」
  return (
    <NavLink to={`/platforms/${spec.serviceType}`} className={navItemClass} title={spec.scope}>
      <NavItemLabel label={spec.label} hint={platformNavHint(entry)} />
    </NavLink>
  );
}

function NavItems({ group }: { group: NavGroupSpec }) {
  return (
    <>
      {group.items.map((item) => (
        <NavLink key={item.path} to={item.path} className={navItemClass}>
          <NavItemLabel label={item.label} hint={navStageHint(item)} />
        </NavLink>
      ))}
    </>
  );
}

/** 四分组侧边导航（ADMIN-IA v3 §一）。
 *
 *  分组标题、条目名称与顺序全部来自 ui-admin 的 NAV_GROUPS——这一份数据同时
 *  喂给面包屑、路由表与 Storybook。XM-0042 之前这里是手抄的，于是侧栏、面包屑、
 *  Storybook 三处各自漂了一点。段序不按「常用程度」重排：这份导航是运营心智的
 *  外化——先看全平台横切，再看某个被管系统，再是管平台自己，最后才是后置能力。 */
function ShellNav({ pathname }: { pathname: string }) {
  return (
    <>
      {NAV_GROUPS.map((group) => {
        const children =
          group.id === "platforms" ? <PlatformNav /> : <NavItems group={group} />;

        if (!group.collapsible) {
          return (
            <NavSection key={group.id} title={group.title}>
              {children}
            </NavSection>
          );
        }
        return (
          <NavSectionCollapsible
            key={group.id}
            title={group.title}
            hint={group.stage}
            // 原型的行为：默认收起，进入这一段时自动展开
            open={group.items.some((item) => pathname.startsWith(item.path))}
          >
            {children}
          </NavSectionCollapsible>
        );
      })}
    </>
  );
}

export function ShellLayout() {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const env = environmentLabel(appApiConfig.environment);
  return (
    <AdminShell
      // 横幅挂在壳上而不是各页页头：它要盖住每一个页面，包括审计页
      banner={<DemoDataBanner />}
      nav={<ShellNav pathname={pathname} />}
      // 顶栏的全局搜索。XM-0043 之前这里是壳自带的禁用占位——一个能看见
      // 却点不动的搜索框。现在换成真的:Ctrl/Cmd + K 打开，只导航不执行
      search={<GlobalSearch />}
      // 面包屑与环境同样挂在壳上：它们回答的是「你在哪、这屏数据算不算数」，
      // 换页时这两个问题都还在，答案不该跟着页面一起被重画
      contextStrip={
        <ContextStrip crumbs={breadcrumbsFor(pathname)} environment={{ ...env, tone: "info" }} />
      }
      // oidc 模式显示 id_token 里的用户名，退出走 Keycloak 的 end_session；
      // dev-header 模式保留「开发模式」四个字与本地开关（auth/session.ts）
      user={{ name: currentUserLabel() }}
      onLogout={() => signOut((to) => void navigate(to))}
    >
      <Outlet />
    </AdminShell>
  );
}

/** 平台详情页的 `?tab=` 处理：旧名字改跳、认不出来给 404。
 *
 *  放在 loader 而不是组件里，是因为改名的那一半必须真的**换掉地址**——在组件
 *  里悄悄把旧 tab 画成新页签，人下次收藏的还是旧地址，书签永远修不好。
 *
 *  三条出路对应三种不同的事实（见 resolvePlatformTab）：认识且这里有 → 直接渲染；
 *  认识但换了名字 → 301 到新名字；认不出来 → Not Found,**不回落概览**
 *  （交接文档 §8：不允许默默回退到第一条）。 */
function platformTabLoader({ request, params }: LoaderFunctionArgs) {
  const url = new URL(request.url);
  const serviceType = params.serviceType ?? "";
  const resolution = resolvePlatformTab(serviceType, url.searchParams.get("tab"));

  switch (resolution.kind) {
    case "redirect": {
      const next = new URL(url);
      next.searchParams.set("tab", resolution.tab);
      return redirect(`${next.pathname}${next.search}`);
    }
    case "moved":
      return redirect(resolution.path);
    case "notFound":
      // 原因走 body 而不是 statusText：后者是 HTTP 的 reason-phrase，规范只允许
      // ASCII，塞中文会让 Response 构造函数直接抛 TypeError——于是 404 变成
      // 一屏「这一页出错了」，而真正的原因（页签名不认识）反而丢了。
      // React Router 会把 body 读出来放进 ErrorResponse.data
      throw new Response(`${serviceType} 没有名为 ${resolution.tab} 的页签`, {
        status: 404,
      });
    case "ok":
      return null;
  }
}

/** 用户详情只覆盖 platformusers v2 明确支持的两类平台。
 *
 * 用统一 RouteErrorBoundary 的 404，而不是在详情组件里画一个看似成功的空页；
 * 更不能把 CPA/服务器悄悄回落到 Sub2API 的第一条样本。 */
function platformUserDetailLoader({ params }: LoaderFunctionArgs) {
  const serviceType = params.serviceType ?? "";
  if (!platformHasUsers(serviceType)) {
    throw new Response(`${serviceType || "该平台"} 不支持终端用户详情`, { status: 404 });
  }
  if (decodePlatformUserIdSegment(params.userId ?? "") === null) {
    throw new Response("用户 ID 编码无效", { status: 404 });
  }
  return null;
}

/** 服务器资产详情的 UI-only 深链。
 *
 *  详情蓝图只声明了原型中的稳定 fixture ID；在 Server Agent 契约接入前，
 *  任何其它 ID 都不能被画成一张「看起来存在」的资产页。loader 先挡住未知
 *  对象，组件本身仍保留同一校验，便于独立挂载测试。 */
function serverDetailLoader({ params }: LoaderFunctionArgs) {
  const serverId = params.serverId ?? "";
  if (!isServerDetailPreviewId(serverId)) {
    throw new Response(`没有这个服务器资产：${serverId || "（空 ID）"}`, { status: 404 });
  }
  return null;
}

/** 渠道/上游详情的 UI-only 路由门禁。 */
function supplyPlatformLoader({ params }: LoaderFunctionArgs) {
  assertSupplyPlatform(params.serviceType ?? "");
  return null;
}

function channelDetailLoader({ params }: LoaderFunctionArgs) {
  const platform = params.serviceType ?? "";
  assertSupplyPlatform(platform);
  if (!(params.channelId ?? "").trim()) {
    throw new Response("渠道 ID 为空，无法定位详情", { status: 404 });
  }
  return null;
}

function upstreamDetailLoader({ params }: LoaderFunctionArgs) {
  const platform = params.serviceType ?? "";
  assertSupplyPlatform(platform);
  const upstreamId = params.upstreamId ?? "";
  if (!upstreamId.trim() || upstreamId === "new") {
    throw new Response(
      upstreamId === "new" ? "新增上游请使用专用登记页面" : "上游 ID 为空，无法定位详情",
      { status: 404 },
    );
  }
  return null;
}

function assertSupplyPlatform(platform: string): asserts platform is "sub2api" | "newapi" {
  if (!isSupplyPlatform(platform)) {
    throw new Response(`平台 ${platform || "（空平台）"} 不支持上游/渠道详情`, { status: 404 });
  }
}

/** 未实装页的路由位，由导航数据生成。
 *
 *  逐条手写的话，「加一页」就变成两处要改（navigation.ts + 这里），而漏改的那一半
 *  正好是本任务要消灭的那种漂移：侧栏上有条目、点进去 404。 */
const placeholderRoutes = NAV_GROUPS.flatMap((group) => group.items)
  .filter((item) => !item.built)
  .map((item) => ({ path: item.path.slice(1), Component: PlaceholderPage }));

export const routes = [
  { path: "/login", Component: LoginPage },
  // Keycloak 授权码回调（XM-AUTH1）。在门禁**之外**：这一步正是为了拿到会话
  { path: "/auth/callback", Component: AuthCallbackPage },
  {
    path: "/",
    // 无路径的门禁布局路由：没登录就 <Navigate> 去 /login，登录了渲染 <Outlet />
    Component: RequireAuth,
    children: [
      // local 模式的强制改密页（XM-LOGIN）：必须登录才能进，但**不**套壳
      // （没有侧栏/顶栏）——must_change_password 为真时 RequireAuth 会把人
      // 无论要去哪都先带到这里，套壳只会让人多一条「先去点别的」的岔路
      { path: "account/password", Component: ChangePasswordPage },
      {
        Component: ShellLayout,
        children: [
          {
            // 无路径的布局路由，只为把 ErrorBoundary 挂在壳**内部**:404 与页面级异常
            // 渲染在主内容区，导航、面包屑、演示横幅都还在。挂到上面那层的话，
            // 一次 404 会连左栏一起换掉，人连「回哪去」都没得选
            ErrorBoundary: RouteErrorBoundary,
            children: [
              { index: true, loader: () => redirect("/dashboard") },
              // 路径保持 /dashboard 与 /audit 不变：XM-0006 起就是这两个地址，
              // 改了会打断已有书签。页面**改名**(运营总览→运营工作台、
              // 审计事件→审计记录)不改地址，这正是交接文档 §8 要的那种迁移
              { path: "dashboard", Component: OverviewPage },
              { path: "alerts", Component: AlertsPage },
              { path: "audit", Component: AuditPage },
              { path: "actions", Component: ActionsPage },
              {
                path: "platforms/:serviceType/upstream/detail/:channelId",
                loader: channelDetailLoader,
                Component: ChannelDetailPage,
              },
              {
                path: "platforms/:serviceType/suppliers/new",
                loader: supplyPlatformLoader,
                Component: SupplierCreatePage,
              },
              {
                path: "platforms/:serviceType/suppliers/:upstreamId",
                loader: upstreamDetailLoader,
                Component: UpstreamDetailPage,
              },
              {
                path: "platforms/server/detail/:serverId",
                loader: serverDetailLoader,
                Component: ServerDetailPage,
              },
              {
                path: "platforms/:serviceType",
                loader: platformTabLoader,
                Component: PlatformDetailPage,
              },
              {
                // 用户 ID 是不透明值；列表 Link 统一编码成带前缀的 UTF-8 hex 段，
                // 详情页严格解码后请求 v2 canonical UserDetail 资源。
                path: "platforms/:serviceType/users/:userId",
                loader: platformUserDetailLoader,
                Component: PlatformUserDetailPage,
              },
              // 请求详情是**完整页**而不是抽屉(§11.4、原型 RECOVERY.md「No right-side
              // detail drawers」)。挂在平台下面而不是全局 /requests/:id：同一个 id 在
              // 两个来源之间不保证唯一，路径里少了平台就没法保证读的是哪一条
              { path: "platforms/:serviceType/requests/:requestId", Component: RequestDetailPage },
              { path: "registry", Component: RegistryPage },
              { path: "identity", Component: IdentityPage },
              { path: "settings", Component: SettingsPage },
              ...placeholderRoutes,

              // --- 旧路径 redirect(ADMIN-IA v3 §4.1 全表)---
              // 只重定向、不再渲染页面。静态段在 react-router 里排在动态段之前，
              // 所以这三条一定压过 platforms/:serviceType，与书写顺序无关
              ...Object.entries(LEGACY_PLATFORM_ROUTES).map(([serviceType, to]) => ({
                path: `platforms/${serviceType}`,
                loader: () => redirect(to),
              })),
              // v1 旧路径。运行手册与 Issue 里贴过这两个地址，导航重构不该让它们变成 404
              { path: "services", loader: () => redirect("/registry") },
              { path: "channels", loader: () => redirect(CHANNELS_REDIRECT) },

              // 兜底 404。没有它，任何没匹配上的旧书签会落到 react-router 的默认
              // 错误页——一屏英文堆栈，既不说这是 404，也回不去
              { path: "*", Component: NotFoundPage },
            ],
          },
        ],
      },
    ],
  },
];

export function createAppRouter() {
  return createBrowserRouter(routes);
}
