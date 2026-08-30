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
});
