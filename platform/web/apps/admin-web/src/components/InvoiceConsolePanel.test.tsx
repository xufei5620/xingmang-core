import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { FINANCE_READ_PERMISSION } from "../api/finance";
import { InvoiceConsolePanel, type InvoiceConsoleMode } from "./InvoiceConsolePanel";

const ORIGIN = "https://invoice.example.test";
const OTHER_SCOPE = "ops.read";

const MODE_CASES: readonly (readonly [InvoiceConsoleMode, string])[] = [
  ["sub2api", "/embed/admin/sub2api"],
  ["newapi", "/embed/admin/newapi"],
  ["global", "/embed/admin/global"],
];

function setInvoiceConsoleOrigin(origin: string) {
  window.__XM_CONFIG__ = { invoiceConsoleOrigin: origin };
}

describe("InvoiceConsolePanel（CR-0005 平台线 g/h/i/j/k）", () => {
  afterEach(() => {
    delete window.__XM_CONFIG__;
  });

  it("缺少 finance.read：显示无权访问，不渲染 iframe（j：可见性以 finance.read 控制）", () => {
    setInvoiceConsoleOrigin(ORIGIN);
    render(<InvoiceConsolePanel mode="sub2api" scopes={[OTHER_SCOPE]} />);
    expect(screen.getByText("无权访问")).not.toBeNull();
    expect(screen.getByText(new RegExp(FINANCE_READ_PERMISSION))).not.toBeNull();
    expect(screen.queryByTitle("开票")).toBeNull();
  });

  it("有 finance.read 但未配置来源：显示未接入，写明缺哪个环境变量，不渲染 iframe（i）", () => {
    render(<InvoiceConsolePanel mode="sub2api" scopes={[FINANCE_READ_PERMISSION]} />);
    expect(screen.getByText("开票")).not.toBeNull();
    expect(screen.getByText(/未配置开票控制台来源（XM_INVOICE_CONSOLE_ORIGIN）/)).not.toBeNull();
    expect(screen.queryByTitle("开票")).toBeNull();
  });

  it.each(MODE_CASES)("scope 与来源都在场时，%s 模式拼出 %s", (mode, path) => {
    setInvoiceConsoleOrigin(ORIGIN);
    render(<InvoiceConsolePanel mode={mode} scopes={[FINANCE_READ_PERMISSION]} />);
    const frame = screen.getByTitle("开票") as HTMLIFrameElement;
    expect(frame.src).toBe(`${ORIGIN}${path}`);
    expect(frame.tagName).toBe("IFRAME");
  });

  it("k：三种状态都不显示任何开票编号、笔数或金额", () => {
    // 无权访问态
    setInvoiceConsoleOrigin(ORIGIN);
    const denied = render(<InvoiceConsolePanel mode="sub2api" scopes={[OTHER_SCOPE]} />);
    expect(denied.container.textContent).not.toMatch(/[¥$]\s?\d/);
    denied.unmount();

    // 未接入态
    delete window.__XM_CONFIG__;
    const unavailable = render(<InvoiceConsolePanel mode="sub2api" scopes={[FINANCE_READ_PERMISSION]} />);
    expect(unavailable.container.textContent).not.toMatch(/[¥$]\s?\d/);
    unavailable.unmount();

    // 正常态：内容完全在 iframe 内部，平台侧读不到也不展示
    setInvoiceConsoleOrigin(ORIGIN);
    const framed = render(<InvoiceConsolePanel mode="sub2api" scopes={[FINANCE_READ_PERMISSION]} />);
    expect(framed.container.querySelector("iframe")).not.toBeNull();
    expect(framed.container.textContent).toBe("");
  });

  describe("CR-0006 XM-INVCON1：local 模式下的断言签发/步进", () => {
    function setLocalConfig(origin: string) {
      window.__XM_CONFIG__ = { invoiceConsoleOrigin: origin, authMode: "local" };
    }

    /** 按路径分派响应的 fake fetch——同一个测试文件里，签发端点与步进端点
     *  经常需要在同一次渲染里先后各答一次。 */
    function stubFetch(handlers: Record<string, () => { status: number; body: unknown }>) {
      vi.stubGlobal(
        "fetch",
        vi.fn((input: string) => {
          const path = new URL(String(input), "http://localhost").pathname;
          const handler = handlers[path];
          if (!handler) {
            return Promise.reject(new Error(`unexpected fetch: ${path}`));
          }
          const { status, body } = handler();
          return Promise.resolve({
            ok: status >= 200 && status < 300,
            status,
            json: () => Promise.resolve(body),
          });
        }),
      );
    }

    afterEach(() => {
      vi.unstubAllGlobals();
      vi.restoreAllMocks();
    });

    it("签发成功：渲染带 iframe 的正常态", async () => {
      setLocalConfig(ORIGIN);
      stubFetch({
        "/api/v1/auth/console-assertion": () => ({
          status: 200,
          body: { assertion: "jws-value", expires_at: new Date(Date.now() + 5 * 60_000).toISOString() },
        }),
      });
      render(<InvoiceConsolePanel mode="sub2api" scopes={[FINANCE_READ_PERMISSION]} />);
      const frame = (await screen.findByTitle("开票")) as HTMLIFrameElement;
      expect(frame.src).toBe(`${ORIGIN}/embed/admin/sub2api`);
      const postMessage = vi.spyOn(frame.contentWindow!, "postMessage");
      fireEvent.load(frame);
      expect(postMessage).toHaveBeenCalledWith(
        { type: "xm-embed", version: 1, kind: "admin-assertion", assertion: "jws-value" },
        ORIGIN,
      );
    });

    it("iframe 请求刷新时重新签发断言", async () => {
      setLocalConfig(ORIGIN);
      let issued = 0;
      stubFetch({
        "/api/v1/auth/console-assertion": () => {
          issued += 1;
          return { status: 200, body: { assertion: `synthetic-${issued}`, expires_at: new Date(Date.now() + 300_000).toISOString() } };
        },
      });
      render(<InvoiceConsolePanel mode="sub2api" scopes={[FINANCE_READ_PERMISSION]} />);
      const frame = (await screen.findByTitle("开票")) as HTMLIFrameElement;
      act(() => window.dispatchEvent(new MessageEvent("message", {
        origin: ORIGIN,
        source: frame.contentWindow,
        data: { type: "xm-embed", version: 1, kind: "admin-assertion-needed" },
      })));
      await waitFor(() => expect(issued).toBe(2));
    });

    it("FINANCE_SCOPE_REQUIRED：显示无权访问，不渲染 iframe", async () => {
      setLocalConfig(ORIGIN);
      stubFetch({
        "/api/v1/auth/console-assertion": () => ({
          status: 403,
          body: { error: { code: "FINANCE_SCOPE_REQUIRED", message: "当前账号缺少 finance.read 权限" } },
        }),
      });
      render(<InvoiceConsolePanel mode="sub2api" scopes={[FINANCE_READ_PERMISSION]} />);
      expect(await screen.findByText("无权访问")).not.toBeNull();
      expect(screen.queryByTitle("开票")).toBeNull();
    });

    it("ADMIN_NETWORK_DENIED：显示无权访问", async () => {
      setLocalConfig(ORIGIN);
      stubFetch({
        "/api/v1/auth/console-assertion": () => ({
          status: 403,
          body: { error: { code: "ADMIN_NETWORK_DENIED", message: "当前网络不在管理员访问名单内" } },
        }),
      });
      render(<InvoiceConsolePanel mode="sub2api" scopes={[FINANCE_READ_PERMISSION]} />);
      expect(await screen.findByText("无权访问")).not.toBeNull();
    });

    it("XM-INVCON1-FALLBACK：端点未挂载（404，未启用断言登录）时回落到旧版直接 iframe，并带诚实提示，不是错误/无权访问态", async () => {
      setLocalConfig(ORIGIN);
      stubFetch({
        // chi 对未挂载路由的响应体没有 error.code（looksLikeUnmountedRoute
        // 判据），与 XM-INVCON1 交接文档记录的既定纪律一致。
        "/api/v1/auth/console-assertion": () => ({ status: 404, body: {} }),
      });
      render(<InvoiceConsolePanel mode="sub2api" scopes={[FINANCE_READ_PERMISSION]} />);
      expect(
        await screen.findByText("控制台断言登录尚未启用，当前使用开票系统自身的登录（过渡期）"),
      ).not.toBeNull();
      const frame = screen.getByTitle("开票") as HTMLIFrameElement;
      // 与 XM-INVCON0 完全相同的直接 iframe：同一个 URL 拼法，且不带
      // assertion（这里没有直接手段断言"没有传某个 prop"，但可以确认没有
      // 因为断言路径而产生额外的签发/登录相关的错误状态）。
      expect(frame.src).toBe(`${ORIGIN}/embed/admin/sub2api`);
      expect(screen.queryByText("无权访问")).toBeNull();
      expect(screen.queryByText("加载失败")).toBeNull();
    });

    it("XM-INVCON1-FALLBACK：未启用断言登录时不出现「无权访问」措辞（那是 finance.read/denied 专用文案）", async () => {
      setLocalConfig(ORIGIN);
      stubFetch({
        "/api/v1/auth/console-assertion": () => ({ status: 404, body: {} }),
      });
      render(<InvoiceConsolePanel mode="newapi" scopes={[FINANCE_READ_PERMISSION]} />);
      await screen.findByTitle("开票");
      expect(screen.queryByText(/无权/)).toBeNull();
    });

    it("已挂载但返回其它错误码（如 INTERNAL/500）：真正的错误态，显示加载失败与可点的重试按钮，不回落到旧版 iframe", async () => {
      setLocalConfig(ORIGIN);
      let calls = 0;
      stubFetch({
        "/api/v1/auth/console-assertion": () => {
          calls += 1;
          return { status: 500, body: { error: { code: "INTERNAL", message: "服务器开小差了" } } };
        },
      });
      render(<InvoiceConsolePanel mode="sub2api" scopes={[FINANCE_READ_PERMISSION]} />);
      expect(await screen.findByText("服务器开小差了")).not.toBeNull();
      expect(calls).toBe(1);
      fireEvent.click(screen.getByRole("button", { name: "重试" }));
      await vi.waitFor(() => expect(calls).toBe(2));
    });

    it("ADMIN_STEP_UP_REQUIRED：展示二次验证表单，验证通过后自动重新签发并渲染 iframe", async () => {
      setLocalConfig(ORIGIN);
      let assertionCalls = 0;
      stubFetch({
        "/api/v1/auth/console-assertion": () => {
          assertionCalls += 1;
          if (assertionCalls === 1) {
            return { status: 403, body: { error: { code: "ADMIN_STEP_UP_REQUIRED", message: "二次验证已过期" } } };
          }
          return {
            status: 200,
            body: { assertion: "jws-value", expires_at: new Date(Date.now() + 5 * 60_000).toISOString() },
          };
        },
        "/api/v1/auth/login/totp": () => ({
          status: 200,
          body: { username: "alice", display_name: "Alice", roles: ["admin"], must_change_password: false },
        }),
      });
      render(<InvoiceConsolePanel mode="sub2api" scopes={[FINANCE_READ_PERMISSION]} />);
      expect(await screen.findByText("需要二次验证")).not.toBeNull();

      fireEvent.change(screen.getByLabelText(/动态码/), { target: { value: "123456" } });
      fireEvent.click(screen.getByRole("button", { name: "验证并继续" }));

      const frame = (await screen.findByTitle("开票")) as HTMLIFrameElement;
      expect(frame.src).toBe(`${ORIGIN}/embed/admin/sub2api`);
      expect(assertionCalls).toBe(2);
    });

    it("步进校验码不对：就地显示错误，不跳出表单", async () => {
      setLocalConfig(ORIGIN);
      stubFetch({
        "/api/v1/auth/console-assertion": () => ({
          status: 403,
          body: { error: { code: "ADMIN_STEP_UP_REQUIRED", message: "二次验证已过期" } },
        }),
        "/api/v1/auth/login/totp": () => ({
          status: 401,
          body: { error: { code: "INVALID_CREDENTIALS", message: "验证码不正确" } },
        }),
      });
      render(<InvoiceConsolePanel mode="sub2api" scopes={[FINANCE_READ_PERMISSION]} />);
      expect(await screen.findByText("需要二次验证")).not.toBeNull();

      fireEvent.change(screen.getByLabelText(/动态码/), { target: { value: "000000" } });
      fireEvent.click(screen.getByRole("button", { name: "验证并继续" }));

      expect(await screen.findByText("验证码或恢复码不正确。")).not.toBeNull();
      expect(screen.getByText("需要二次验证")).not.toBeNull();
    });
  });
});
