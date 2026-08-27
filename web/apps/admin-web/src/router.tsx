import { useQuery } from "@tanstack/react-query";
import {
  AdminShell,
  ContextStrip,
  NavItemDisabled,
  NavSection,
  navItemClass,
} from "@xingmang/ui-admin";
import {
  NavLink,
  Outlet,
  createBrowserRouter,
  redirect,
  useLocation,
  useNavigate,
} from "react-router";
import { appApiConfig } from "./api/config";
import { listServices } from "./api/platform";
import { devLogout, isAuthenticated } from "./auth";
import { DemoDataBanner } from "./components/DemoDataBanner";
import { breadcrumbsFor, environmentLabel } from "./lib/breadcrumbs";
import {
  groupPlatforms,
  pendingBadge,
  platformOpens,
  RESOURCES_TAB,
  type PlatformEntry,
  type RegistryState,
} from "./lib/platforms";
import { AlertsPage } from "./pages/AlertsPage";
import { AuditPage } from "./pages/AuditPage";
import { LoginPage } from "./pages/LoginPage";
import { OverviewPage } from "./pages/OverviewPage";
import { PlatformDetailPage } from "./pages/PlatformDetailPage";
import { RegistryPage } from "./pages/RegistryPage";
import { SettingsPage } from "./pages/SettingsPage";

function requireAuth() {
  if (!isAuthenticated()) return redirect("/login");
  return null;
}

// 导航项的样式由 ui-admin 给（navItemClass）：左栏在深色轨道上，用的是不随主题
// 翻转的 nav-* 令牌，应用侧照着内容区的 surface/fg 再拼一份就会一半亮一半暗。

/** 「被管平台」段：由 `GET /api/v1/services` 驱动。
 *
 *  段里的条目 = 平台目录（ADMIN-IA 平台表）叠上 Registry 的实际登记情况。
 *  两者都要：只看 Registry，还没接的平台会从导航上消失（§12 惯例不允许）；
 *  只看目录，新登记的平台要等有人改代码才出现（ADMIN-IA 要求「登记即出现」）。
 *
 *  复用 ['services'] 这个 query key：注册表页本来就要拉它，导航因此在大多数
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

  // 判据是「点进去有没有东西」而不是「注册表里有没有」：NewAPI 的内容来自
  // newapi.* 指标，与注册表无关（见 lib/platforms 的 platformOpens）。
  if (!platformOpens(entry)) {
    return <NavItemDisabled label={spec.label} hint={pendingBadge(spec.plan)} title={spec.scope} />;
  }

  return (
    <NavLink to={`/platforms/${spec.serviceType}`} className={navItemClass}>
      {spec.label}
    </NavLink>
  );
}

/** 三段式侧边导航（ADMIN-IA 一、三段式总树）。
 *
 *  段序与条目顺序都照文档，不按「常用程度」重排：这份导航是运营心智的
 *  外化——先看全平台横切，再看某个被管系统，最后才是管平台自己。 */
function ShellNav() {
  return (
    <>
      <NavSection title="全局">
        <NavLink to="/dashboard" className={navItemClass}>
          运营总览
        </NavLink>
        {/* 告警中心归 XM-0033，合并 release 时已上线，占位换成了真链接。
            位置照 ADMIN-IA 的全局段顺序（运营总览 / 告警中心 / 审计事件），
            而不是 XM-0033 在旧扁平导航里的那个位置 */}
        <NavLink to="/alerts" className={navItemClass}>
          告警中心
        </NavLink>
        <NavLink to="/audit" className={navItemClass}>
          审计事件
        </NavLink>
      </NavSection>

      <NavSection title="被管平台">
        <PlatformNav />
      </NavSection>

      <NavSection title="平台治理">
        <NavLink to="/registry" className={navItemClass}>
          注册表
        </NavLink>
        <NavItemDisabled
          label="财务中心"
          hint="未接入·M3"
          title="跨支付与开票的统一视图（ADR-006 刻意保留，不拆进各平台）；规划于 M3 与开票期"
        />
        <NavItemDisabled
          label="变更与审批"
          hint="未接入"
          title="变更单列表与审批中心，随 XM-0030（Foundation-B）上线"
        />
        <NavLink to="/settings" className={navItemClass}>
          设置
        </NavLink>
      </NavSection>
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
      nav={<ShellNav />}
      // 面包屑与环境同样挂在壳上：它们回答的是「你在哪、这屏数据算不算数」，
      // 换页时这两个问题都还在，答案不该跟着页面一起被重画
      contextStrip={
        <ContextStrip crumbs={breadcrumbsFor(pathname)} environment={{ ...env, tone: "info" }} />
      }
      user={{ name: "开发模式" }}
      onLogout={() => {
        devLogout();
        void navigate("/login");
      }}
    >
      <Outlet />
    </AdminShell>
  );
}

export const routes = [
  { path: "/login", Component: LoginPage },
  {
    path: "/",
    loader: requireAuth,
    Component: ShellLayout,
    children: [
      { index: true, loader: () => redirect("/dashboard") },
      // 路径保持 /dashboard 与 /audit 不变：XM-0006 起就是这两个地址，
      // 改了会打断已有书签
      { path: "dashboard", Component: OverviewPage },
      // 告警中心来自 XM-0033；/channels 与 /services 不再挂页面，
      // 改为下面的重定向（ADMIN-IA 三、迁移映射）
      { path: "alerts", Component: AlertsPage },
      { path: "audit", Component: AuditPage },
      { path: "platforms/:serviceType", Component: PlatformDetailPage },
      { path: "registry", Component: RegistryPage },
      { path: "settings", Component: SettingsPage },

      // --- v1 旧路径（ADMIN-IA 三、迁移映射）---
      // 只重定向、不再渲染页面。运行手册与 Issue 里贴过这两个地址，
      // 导航重构不该让它们变成 404
      { path: "services", loader: () => redirect("/registry") },
      { path: "channels", loader: () => redirect(`/platforms/sub2api?tab=${RESOURCES_TAB}`) },
    ],
  },
];

export function createAppRouter() {
  return createBrowserRouter(routes);
}
