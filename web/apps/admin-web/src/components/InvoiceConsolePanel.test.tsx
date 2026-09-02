import { fireEvent, render, screen } from "@testing-library/react";
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

    it("端点未挂载（404，未启用断言登录）：显示未接入说明", async () => {
      setLocalConfig(ORIGIN);
      stubFetch({
        "/api/v1/auth/console-assertion": () => ({ status: 404, body: {} }),
      });
      render(<InvoiceConsolePanel mode="sub2api" scopes={[FINANCE_READ_PERMISSION]} />);
      expect(await screen.findByText(/开票系统未启用断言登录/)).not.toBeNull();
    });

    it("其它错误码：显示加载失败与可点的重试按钮", async () => {
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
