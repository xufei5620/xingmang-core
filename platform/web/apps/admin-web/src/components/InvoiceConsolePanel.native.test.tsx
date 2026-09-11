import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, useLocation, useNavigate } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { InvoiceConsolePanel } from "./InvoiceConsolePanel";
import { httpInvoiceApi } from "../../../../../../invoice/web/src/lib/http-api";
import type { SourceHealthReport } from "../../../../../../invoice/web/src/types";

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); delete window.__XM_CONFIG__; });
const staff = { authenticated: true, csrf_token: "staff-token-fixture-".repeat(4), admin_step_up_required: false,
  user: { id: "11111111-1111-4111-8111-111111111111", role: "admin", display_name: "Operator", email: "operator@example.test" } };

it("renders invoice navigation inside the host Router using only the staff session", async () => {
  window.__XM_CONFIG__ = { authMode: "local" };
  const fetcher = vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith("/staff-session") ? staff : {}), {
    headers: { "content-type": "application/json" },
  }));
  vi.stubGlobal("fetch", fetcher);
  const view = render(<MemoryRouter initialEntries={["/finance?tab=invoicing"]}>
    <InvoiceConsolePanel mode="global" scopes={["finance.read"]} />
  </MemoryRouter>);
  await waitFor(() => expect(fetcher.mock.calls.some(([url]) => url === "/invoice-api/v1/auth/staff-session")).toBe(true));
  expect(await screen.findByRole("navigation", { name: "开票管理" })).toBeTruthy();
  expect(view.container.querySelector("iframe")).toBeNull();
  expect(screen.queryByText("使用统一身份账号登录")).toBeNull();
  expect(fetcher.mock.calls.some(([url]) => /console-assertion|\/auth\/session$/.test(url))).toBe(false);
});

it("does not contact the invoice API without finance.read", () => {
  const fetcher = vi.fn(); vi.stubGlobal("fetch", fetcher);
  const view = render(<MemoryRouter><InvoiceConsolePanel mode="newapi" scopes={[]} /></MemoryRouter>);
  expect(screen.queryByText("无权访问")).toBeTruthy();
  expect(fetcher).not.toHaveBeenCalled();
  expect(view.container.querySelector("iframe")).toBeNull();
});

it.each([401, 403, 500])("blocks staff HTTP %s without opening another login or accepting a message", async status => {
  const open = vi.spyOn(window, "open");
  const fetcher = vi.fn(async () => new Response(JSON.stringify({ error: { code: "LOGIN_REQUIRED" } }), {
    status, headers: { "content-type": "application/json" },
  }));
  vi.stubGlobal("fetch", fetcher);
  const view = render(<MemoryRouter><InvoiceConsolePanel mode="global" scopes={["finance.read"]} /></MemoryRouter>);
  expect(await screen.findByRole("alert")).toBeTruthy();
  const count = fetcher.mock.calls.length;
  act(() => window.dispatchEvent(new MessageEvent("message", { origin: window.location.origin,
    data: { type: "xm-embed", version: 1, kind: "admin-assertion", assertion: "retired-synthetic-credential" } })));
  expect(fetcher.mock.calls.length).toBe(count);
  expect(open).not.toHaveBeenCalled();
  expect(view.container.querySelector("iframe")).toBeNull();
  expect(screen.queryByRole("textbox", { name: "账号" })).toBeNull();
});

it("refreshes the same staff session after native TOTP, without customer logout or an assertion", async () => {
  window.__XM_CONFIG__ = { authMode: "local" };
  let verified = false;
  const fetcher = vi.fn(async (url: string, init: RequestInit) => {
    let body: unknown = {};
    if (url.endsWith("/staff-session")) body = { ...staff, admin_step_up_required: !verified };
    if (url === "/api/v1/auth/login/totp") {
      expect(JSON.parse(String(init.body))).toEqual({ code: "123456" });
      expect(new Headers(init.headers).get("X-Requested-With")).toBe("xingmang");
      verified = true;
      body = { username: "operator", roles: ["finance"], totp_enrolled: true };
    }
    return new Response(JSON.stringify(body), { headers: { "content-type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetcher);
  render(<MemoryRouter><InvoiceConsolePanel mode="global" scopes={["finance.read"]} /></MemoryRouter>);
  fireEvent.click(await screen.findByRole("button", { name: "继续管理员验证" }));
  fireEvent.change(screen.getByLabelText(/动态码/), { target: { value: "123456" } });
  fireEvent.click(screen.getByRole("button", { name: "验证并继续" }));
  expect(await screen.findByRole("navigation", { name: "开票管理" })).toBeTruthy();
  expect(fetcher.mock.calls.filter(([url]) => url.endsWith("/staff-session"))).toHaveLength(2);
  expect(fetcher.mock.calls.some(([url]) => /console-assertion|logout|\/auth\/session$/.test(url))).toBe(false);
});

function HostURL() {
  const location = useLocation(); const navigate = useNavigate();
  return <><output aria-label="host-url">{location.pathname}{location.search}</output><button onClick={() => navigate(-1)}>host-back</button></>;
}
const subSource = "22222222-2222-4222-8222-222222222222";
const newSource = "33333333-3333-4333-8333-333333333333";
it("keeps host finance tabs and browser history while opening native invoice subpages", async () => {
  vi.spyOn(httpInvoiceApi, "getSourceHealth").mockResolvedValue({ items: [
    { sourceType: "sub2api", sourceInstanceId: subSource }, { sourceType: "newapi", sourceInstanceId: newSource },
  ] } as SourceHealthReport);
  const fetcher = vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith("/staff-session") ? staff : { items: [], has_more: false }), {
    headers: { "content-type": "application/json" },
  }));
  vi.stubGlobal("fetch", fetcher);
  render(<MemoryRouter initialEntries={["/platforms/sub2api?tab=finance&sub=invoices"]}>
    <HostURL /><InvoiceConsolePanel mode="sub2api" scopes={["finance.read"]} />
  </MemoryRouter>);
  const nav = await screen.findByRole("navigation", { name: "开票管理" });
  await waitFor(() => expect(fetcher.mock.calls.some(([url]) => url.includes(`/admin/invoice-requests?limit=100&source_instance_id=${subSource}`))).toBe(true));
  expect(within(nav).queryByText("支付核验")).toBeNull();
  await act(async () => fireEvent.click(within(nav).getByRole("link", { name: "用户账本" })));
  expect(screen.getByLabelText("host-url").textContent).toContain("/platforms/sub2api?tab=finance&sub=invoices&invoice_path=");
  const ledgerNav = screen.getByRole("navigation", { name: "开票管理" });
  expect(within(ledgerNav).getByRole("link", { name: "用户账本" }).getAttribute("aria-current")).toBe("page");
  expect(ledgerNav.querySelectorAll('[aria-current="page"]')).toHaveLength(1);
  await act(async () => fireEvent.click(screen.getByRole("button", { name: "host-back" })));
  expect(screen.getByLabelText("host-url").textContent).toBe("/platforms/sub2api?tab=finance&sub=invoices");
  expect(fetcher.mock.calls.filter(([url]) => /\/admin\/(invoice-requests|accounts\/ledger)\?/.test(url)).every(([url]) => url.includes(subSource))).toBe(true);
});

it("retires old platform requests when the native workspace changes scope", async () => {
  vi.spyOn(httpInvoiceApi, "getSourceHealth").mockResolvedValue({ items: [
    { sourceType: "sub2api", sourceInstanceId: subSource }, { sourceType: "newapi", sourceInstanceId: newSource },
  ] } as SourceHealthReport);
  let finishOld!: (response: Response) => void;
  const oldResponse = new Promise<Response>(resolve => { finishOld = resolve; });
  const fetcher = vi.fn(async (url: string) => {
    if (url.includes(subSource)) return oldResponse;
    return new Response(JSON.stringify(url.endsWith("/staff-session") ? staff : { items: [], has_more: false }), {
      headers: { "content-type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetcher);
  const tree = (mode: "sub2api" | "newapi") => <MemoryRouter><InvoiceConsolePanel mode={mode} scopes={["finance.read"]} /></MemoryRouter>;
  const view = render(tree("sub2api"));
  await waitFor(() => expect(fetcher.mock.calls.some(([url]) => url.includes(subSource))).toBe(true));
  view.rerender(tree("newapi"));
  await waitFor(() => expect(fetcher.mock.calls.some(([url]) => url.includes(newSource))).toBe(true));
  await act(async () => finishOld(new Response(JSON.stringify({ error: { code: "LOGIN_REQUIRED" } }), {
    status: 401, headers: { "content-type": "application/json" },
  })));
  expect(screen.getByRole("navigation", { name: "开票管理" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "支付核验" })).toBeTruthy();
  expect(screen.queryByText("控制台管理员会话无效，请重新登录控制台。")).toBeNull();
});
