import { render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resolveRuntimeConfig } from "./runtimeConfig";
import { LoginPage } from "../pages/LoginPage";

afterEach(() => { delete window.__XM_CONFIG__; vi.unstubAllGlobals(); });

describe("retired platform authentication", () => {
  it("defaults to local cookie sessions", () => {
    expect(resolveRuntimeConfig(undefined, {}).authMode).toBe("local");
  });
  it.each(["oidc", "invalid"])("rejects %s without a developer or provider login", (mode) => {
    window.__XM_CONFIG__ = { authMode: mode };
    const cfg = resolveRuntimeConfig(window.__XM_CONFIG__, { VITE_XM_AUTH_MODE: "dev-header" });
    expect(cfg.authMode).toBe("local");
    expect(cfg.problems.length).toBeGreaterThan(0);
    const fetch = vi.fn(); vi.stubGlobal("fetch", fetch);
    render(<RouterProvider router={createMemoryRouter([{path:"/login",Component:LoginPage}],{initialEntries:["/login"]})} />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByRole("alert")).toBeTruthy();
    expect(fetch).not.toHaveBeenCalled();
  });
  it("rejects developer headers in production", () => {
    const cfg = resolveRuntimeConfig({authMode:"dev-header",environment:"production"}, {});
    expect(cfg.authMode).toBe("local");
    expect(cfg.problems.length).toBeGreaterThan(0);
  });
});
