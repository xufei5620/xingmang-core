import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
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
});
