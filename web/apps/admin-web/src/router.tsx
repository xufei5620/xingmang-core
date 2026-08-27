import { AdminShell } from "@xingmang/ui-admin";
import { NavLink, Outlet, createBrowserRouter, redirect, useNavigate } from "react-router";
import { devLogout, isAuthenticated } from "./auth";
import { DemoDataBanner } from "./components/DemoDataBanner";
import { AuditPage } from "./pages/AuditPage";
import { ChannelsPage } from "./pages/ChannelsPage";
import { LoginPage } from "./pages/LoginPage";
import { OverviewPage } from "./pages/OverviewPage";
import { ServicesPage } from "./pages/ServicesPage";

function requireAuth() {
  if (!isAuthenticated()) return redirect("/login");
  return null;
}

function navLinkClass({ isActive }: { isActive: boolean }): string {
  return isActive
    ? "block rounded-md bg-surface-muted px-3 py-2 font-medium text-accent"
    : "block rounded-md px-3 py-2 text-fg-muted hover:bg-surface-muted";
}

export function ShellLayout() {
  const navigate = useNavigate();
  return (
    <AdminShell
      // 横幅挂在壳上而不是各页页头：它要盖住每一个页面，包括审计页
      banner={<DemoDataBanner />}
      nav={
        <>
          <NavLink to="/dashboard" className={navLinkClass}>
            运营总览
          </NavLink>
          <NavLink to="/channels" className={navLinkClass}>
            渠道余额
          </NavLink>
          <NavLink to="/services" className={navLinkClass}>
            服务清单
          </NavLink>
          <NavLink to="/audit" className={navLinkClass}>
            审计事件
          </NavLink>
        </>
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
      // 路径保持 /dashboard 不变：XM-0006 起就是这个地址，改了会打断已有书签
      { path: "dashboard", Component: OverviewPage },
      { path: "channels", Component: ChannelsPage },
      { path: "services", Component: ServicesPage },
      { path: "audit", Component: AuditPage },
    ],
  },
];

export function createAppRouter() {
  return createBrowserRouter(routes);
}
