import { afterEach, describe, expect, it } from "vitest";
import {
  DEFAULT_OIDC_SCOPES,
  getRuntimeConfig,
  resolveRuntimeConfig,
  type RuntimeEnv,
} from "./runtimeConfig";

const emptyEnv: RuntimeEnv = {};

describe("resolveRuntimeConfig：运行时配置的三层回落", () => {
  it("什么都没配就是 dev-header（本地开发的现状）", () => {
    const cfg = resolveRuntimeConfig(undefined, emptyEnv);
    expect(cfg.authMode).toBe("dev-header");
    expect(cfg.source).toBe("default");
    expect(cfg.oidcScopes).toBe(DEFAULT_OIDC_SCOPES);
    expect(cfg.problems).toEqual([]);
  });

  it("window.__XM_CONFIG__ 优先于 Vite 变量", () => {
    const cfg = resolveRuntimeConfig(
      {
        authMode: "oidc",
        oidcIssuer: "https://auth.example.test/realms/solov-staff/",
        oidcClientId: "xingmang-admin-web",
      },
      {
        VITE_XM_AUTH_MODE: "dev-header",
        VITE_XM_OIDC_ISSUER: "https://build-time.example.test/realms/x",
      },
    );
    expect(cfg.authMode).toBe("oidc");
    expect(cfg.source).toBe("app-config");
    // 末尾斜杠去掉：发现文档地址与后端 issuer 精确比对都不要它
    expect(cfg.oidcIssuer).toBe("https://auth.example.test/realms/solov-staff");
    expect(cfg.oidcClientId).toBe("xingmang-admin-web");
    expect(cfg.problems).toEqual([]);
  });

  it("没有运行时文件时回落到 VITE_XM_* 变量", () => {
    const cfg = resolveRuntimeConfig(undefined, {
      VITE_XM_AUTH_MODE: "oidc",
      VITE_XM_OIDC_ISSUER: "https://auth.example.test/realms/solov-staff",
      VITE_XM_OIDC_CLIENT_ID: "xingmang-admin-web",
      VITE_XM_OIDC_SCOPES: "openid profile",
      VITE_XM_ENVIRONMENT: "staging",
    });
    expect(cfg.authMode).toBe("oidc");
    expect(cfg.source).toBe("vite-env");
    expect(cfg.oidcScopes).toBe("openid profile");
    expect(cfg.environment).toBe("staging");
  });

  it("逐字段回落：运行时只给了 authMode，issuer 仍可来自构建期变量", () => {
    const cfg = resolveRuntimeConfig(
      { authMode: "oidc" },
      {
        VITE_XM_OIDC_ISSUER: "https://auth.example.test/realms/solov-staff",
        VITE_XM_OIDC_CLIENT_ID: "xingmang-admin-web",
      },
    );
    expect(cfg.authMode).toBe("oidc");
    expect(cfg.oidcIssuer).toBe("https://auth.example.test/realms/solov-staff");
    expect(cfg.problems).toEqual([]);
  });

  it("authMode 不认识：回落 dev-header 并记下问题，不弄垮应用", () => {
    const cfg = resolveRuntimeConfig({ authMode: "keycloak" }, emptyEnv);
    expect(cfg.authMode).toBe("dev-header");
    expect(cfg.problems.join("\n")).toContain("keycloak");
  });

  it("window.__XM_CONFIG__ 不是对象（脚本写坏了）：忽略并记下问题", () => {
    const cfg = resolveRuntimeConfig("oidc", emptyEnv);
    expect(cfg.authMode).toBe("dev-header");
    expect(cfg.problems).toHaveLength(1);
  });

  it("oidc 但缺 issuer / client id：模式保持 oidc（不能悄悄降级成开发头），问题逐条列出", () => {
    const cfg = resolveRuntimeConfig({ authMode: "oidc" }, emptyEnv);
    expect(cfg.authMode).toBe("oidc");
    expect(cfg.problems).toHaveLength(2);
    expect(cfg.problems[0]).toContain("XM_WEB_OIDC_ISSUER");
    expect(cfg.problems[1]).toContain("XM_WEB_OIDC_CLIENT_ID");
  });

  it("空串与空白当作没配", () => {
    const cfg = resolveRuntimeConfig(
      { authMode: "  ", oidcIssuer: "", oidcScopes: " " },
      { VITE_XM_AUTH_MODE: "" },
    );
    expect(cfg.authMode).toBe("dev-header");
    expect(cfg.oidcScopes).toBe(DEFAULT_OIDC_SCOPES);
    // 空白 authMode 视为未提供，不算问题
    expect(cfg.problems).toEqual([]);
  });
});

describe("invoiceConsoleOrigin：静态直传，不参与 authMode 那套多层回落（CR-0005 平台线 i）", () => {
  it("没配就是 undefined，且不算问题", () => {
    const cfg = resolveRuntimeConfig(undefined, emptyEnv);
    expect(cfg.invoiceConsoleOrigin).toBeUndefined();
    expect(cfg.problems).toEqual([]);
  });

  it("合法的 https 来源：原样接受", () => {
    const cfg = resolveRuntimeConfig({ invoiceConsoleOrigin: "https://invoice.solov.cc" }, emptyEnv);
    expect(cfg.invoiceConsoleOrigin).toBe("https://invoice.solov.cc");
    expect(cfg.problems).toEqual([]);
  });

  it("末尾斜杠去掉，与 oidcIssuer 的归一化口径一致", () => {
    const cfg = resolveRuntimeConfig({ invoiceConsoleOrigin: "https://invoice.solov.cc/" }, emptyEnv);
    expect(cfg.invoiceConsoleOrigin).toBe("https://invoice.solov.cc");
  });

  it("带端口的来源（本地联调）也接受", () => {
    const cfg = resolveRuntimeConfig(
      { invoiceConsoleOrigin: "https://localhost:5173" },
      emptyEnv,
    );
    expect(cfg.invoiceConsoleOrigin).toBe("https://localhost:5173");
  });

  it("带路径：拒绝，按未配置处理并记问题", () => {
    const cfg = resolveRuntimeConfig(
      { invoiceConsoleOrigin: "https://invoice.solov.cc/admin" },
      emptyEnv,
    );
    expect(cfg.invoiceConsoleOrigin).toBeUndefined();
    expect(cfg.problems).toHaveLength(1);
    expect(cfg.problems[0]).toContain("invoiceConsoleOrigin");
  });

  it("带查询参数：拒绝", () => {
    const cfg = resolveRuntimeConfig(
      { invoiceConsoleOrigin: "https://invoice.solov.cc?x=1" },
      emptyEnv,
    );
    expect(cfg.invoiceConsoleOrigin).toBeUndefined();
    expect(cfg.problems).toHaveLength(1);
  });

  it("带片段：拒绝", () => {
    const cfg = resolveRuntimeConfig(
      { invoiceConsoleOrigin: "https://invoice.solov.cc#section" },
      emptyEnv,
    );
    expect(cfg.invoiceConsoleOrigin).toBeUndefined();
    expect(cfg.problems).toHaveLength(1);
  });

  it("http（非 https）：拒绝", () => {
    const cfg = resolveRuntimeConfig({ invoiceConsoleOrigin: "http://invoice.solov.cc" }, emptyEnv);
    expect(cfg.invoiceConsoleOrigin).toBeUndefined();
    expect(cfg.problems).toHaveLength(1);
  });

  it("不是合法 URL：拒绝，不抛异常", () => {
    const cfg = resolveRuntimeConfig({ invoiceConsoleOrigin: "不是网址" }, emptyEnv);
    expect(cfg.invoiceConsoleOrigin).toBeUndefined();
    expect(cfg.problems).toHaveLength(1);
  });

  it("空串/空白当作没配，不算问题", () => {
    const cfg = resolveRuntimeConfig({ invoiceConsoleOrigin: "   " }, emptyEnv);
    expect(cfg.invoiceConsoleOrigin).toBeUndefined();
    expect(cfg.problems).toEqual([]);
  });

  it("没有 VITE_* 回落层：只认 window.__XM_CONFIG__", () => {
    // authMode/oidcIssuer 有 vite-env 兜底,但这个字段是「静态环境变量模式」
    // （同 reqlog/CPA 先例），刻意没有第二层——这里钉住这条边界不被以后悄悄补上
    const competingEnv = {
      VITE_XM_AUTH_MODE: "dev-header",
      VITE_XM_INVOICE_CONSOLE_ORIGIN: "https://stale-build.example.test",
    } as RuntimeEnv;
    const cfg = resolveRuntimeConfig(undefined, competingEnv);
    expect(cfg.invoiceConsoleOrigin).toBeUndefined();
    const configured = resolveRuntimeConfig({ invoiceConsoleOrigin: "https://runtime.example.test" }, competingEnv);
    expect(configured.invoiceConsoleOrigin).toBe("https://runtime.example.test");
  });
});

describe("getRuntimeConfig：从 window 读取", () => {
  afterEach(() => {
    delete window.__XM_CONFIG__;
  });

  it("读 window.__XM_CONFIG__", () => {
    window.__XM_CONFIG__ = {
      authMode: "oidc",
      oidcIssuer: "https://auth.example.test/realms/solov-staff",
      oidcClientId: "xingmang-admin-web",
    };
    expect(getRuntimeConfig().authMode).toBe("oidc");
  });

  it("没有注入时是 dev-header", () => {
    expect(getRuntimeConfig().authMode).toBe("dev-header");
  });

  it("读 window.__XM_CONFIG__.invoiceConsoleOrigin", () => {
    window.__XM_CONFIG__ = { invoiceConsoleOrigin: "https://invoice.solov.cc" };
    expect(getRuntimeConfig().invoiceConsoleOrigin).toBe("https://invoice.solov.cc");
  });

  it("没有注入时 invoiceConsoleOrigin 是 undefined（三处开票页签据此显示未配置）", () => {
    expect(getRuntimeConfig().invoiceConsoleOrigin).toBeUndefined();
  });
});
