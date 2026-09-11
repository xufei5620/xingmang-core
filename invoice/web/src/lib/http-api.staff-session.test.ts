import { afterEach, expect, it, vi } from "vitest";
import { httpInvoiceApi } from "./http-api";
import type { InvoiceRequest } from "../types";

afterEach(() => vi.unstubAllGlobals());
const token = "staff-csrf-fixture-".repeat(4);
const session = (role = "admin") => ({ authenticated: true, csrf_token: token,
  admin_step_up_required: false, user: { id: "11111111-1111-4111-8111-111111111111", role,
    display_name: "Operator", email: "operator@example.test", email_verified: true } });
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { "content-type": "application/json" } });

it("uses the staff session and sends both write protections on the unified namespace", async () => {
  vi.stubGlobal("window", { setTimeout, clearTimeout });
  const fetcher = vi.fn(async (url: string) => json(url.endsWith("/staff-session") ? session() : { ok: true }));
  vi.stubGlobal("fetch", fetcher);
  const result = await httpInvoiceApi.getStaffSession();
  expect(result.authenticated && result.user.id).toBe("11111111-1111-4111-8111-111111111111");
  await httpInvoiceApi.saveNoticeWebhook("https://example.test/synthetic");
  expect(fetcher.mock.calls[0][0]).toBe("/invoice-api/v1/auth/staff-session");
  const [url, init] = fetcher.mock.calls[1] as unknown as [string, RequestInit];
  expect(url).toBe("/invoice-api/v1/admin/settings/notice-webhook");
  expect(new Headers(init.headers).get("X-CSRF-Token")).toBe(token);
  expect(new Headers(init.headers).get("X-Requested-With")).toBe("xingmang");
});

it("protects the PDF upload with the same staff CSRF and same-origin cookie", async () => {
  vi.stubGlobal("window", { setTimeout, clearTimeout });
  const fetcher = vi.fn(async (url: string, _init: RequestInit) => json(url.endsWith("/staff-session") ? session() : { ok: true }));
  vi.stubGlobal("fetch", fetcher);
  await httpInvoiceApi.getStaffSession();
  await httpInvoiceApi.adminUploadInvoice({ id: "request-fixture", version: 3, workflowStatus: "issued_awaiting_document" } as InvoiceRequest,
    new File(["%PDF-test"], "invoice.pdf", { type: "application/pdf" }), "fixture-invoice", "2026-09-11T08:00:00Z");
  const [url, init] = fetcher.mock.calls[1]!;
  expect(url).toBe("/invoice-api/v1/admin/invoice-requests/request-fixture/documents/upload");
  expect(new Headers(init.headers).get("X-CSRF-Token")).toBe(token);
  expect(new Headers(init.headers).get("X-Requested-With")).toBe("xingmang");
  expect(init.credentials).toBe("same-origin");
  expect(init.body).toBeInstanceOf(FormData);
});

it.each([undefined, "false", 0])("rejects a staff session without a real MFA state (%s)", async mfa => {
  vi.stubGlobal("window", { setTimeout, clearTimeout });
  vi.stubGlobal("fetch", vi.fn(async () => json({ ...session(), admin_step_up_required: mfa })));
  await expect(httpInvoiceApi.getStaffSession()).rejects.toMatchObject({ code: "INVALID_STAFF_SESSION_RESPONSE" });
});

it("rejects a customer identity returned from the staff session endpoint", async () => {
  vi.stubGlobal("window", { setTimeout, clearTimeout });
  vi.stubGlobal("fetch", vi.fn(async () => json(session("user"))));
  await expect(httpInvoiceApi.getStaffSession()).rejects.toMatchObject({ code: "INVALID_STAFF_SESSION_RESPONSE" });
});

it("keeps the separate customer session on the unified namespace with its source scope", async () => {
  vi.stubGlobal("window", { setTimeout, clearTimeout });
  const value = session("user");
  const fetcher = vi.fn(async (_url: string) => json({ ...value, user: { ...value.user, platform: "sub2api", platform_user_id: "42" } }));
  vi.stubGlobal("fetch", fetcher);
  const result = await httpInvoiceApi.getSession();
  expect(fetcher.mock.calls[0][0]).toBe("/invoice-api/v1/auth/session");
  expect(result.authenticated && result.user.platform).toBe("sub2api");
});
