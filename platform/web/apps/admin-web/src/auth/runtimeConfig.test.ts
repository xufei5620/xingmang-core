import { afterEach, describe, expect, it } from "vitest";
import { getRuntimeConfig, resolveRuntimeConfig } from "./runtimeConfig";
afterEach(() => { delete window.__XM_CONFIG__; });
describe("runtime configuration", () => {
  it("trims a valid runtime mode", () => {
    expect(resolveRuntimeConfig({authMode:" local "},{})).toMatchObject({authMode:"local",problems:[]});
  });
  it("retired fields never retain development identity", () => {
    expect(resolveRuntimeConfig({authMode:"dev-header",oidcIssuer:"retired"},{}).authMode).toBe("local");
  });
  it("prefers runtime over build settings", () => {
    expect(resolveRuntimeConfig({authMode:"local"},{VITE_XM_AUTH_MODE:"dev-header"})).toMatchObject({authMode:"local",source:"app-config",problems:[]});
  });
  it("allows explicit nonproduction development", () => {
    expect(resolveRuntimeConfig({authMode:"dev-header",environment:"staging"},{})).toMatchObject({authMode:"dev-header",environment:"staging",problems:[]});
  });
  it("uses build settings only when runtime mode is absent", () => {
    expect(resolveRuntimeConfig({authMode:" "},{VITE_XM_AUTH_MODE:"dev-header"})).toMatchObject({authMode:"dev-header",source:"vite-env"});
  });
  it.each([[],"local",42])("malformed runtime config blocks authentication: %j", (raw) => {
    expect(resolveRuntimeConfig(raw,{}).problems.length).toBeGreaterThan(0);
  });
  it.each(["oidcIssuer","oidcClientId","oidcScopes","invoiceConsoleOrigin"])("rejects retired field %s", key => {
    expect(resolveRuntimeConfig({[key]:"retired"},{}).problems.length).toBeGreaterThan(0);
  });
  it("reads current configuration without caching", () => {
    window.__XM_CONFIG__={authMode:"dev-header"}; expect(getRuntimeConfig().authMode).toBe("dev-header");
    window.__XM_CONFIG__={authMode:"local"}; expect(getRuntimeConfig().authMode).toBe("local");
  });
});
