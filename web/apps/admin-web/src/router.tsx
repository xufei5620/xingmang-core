import { AdminShell } from "@xingmang/ui-admin";
import { NavLink, Outlet, createBrowserRouter, redirect, useNavigate } from "react-router";
import { devLogout, isAuthenticated } from "./auth";
import { DashboardPage } from "./pages/DashboardPage";
import { LoginPage } from "./pages/LoginPage";

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
      nav={
        <NavLink to="/dashboard" className={navLinkClass}>
          运营总览
        </NavLink>
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
      { path: "dashboard", Component: DashboardPage },
    ],
  },
];

export function createAppRouter() {
  return createBrowserRouter(routes);
}
