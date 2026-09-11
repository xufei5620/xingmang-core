import {describe,expect,it} from "vitest";
import {safeNextPath} from "./paths";
describe("safeNextPath retains local redirect validation", () => {
  it.each([null,undefined,"","https://evil.test/","//evil.test/","/\\evil.test/","/login","/auth/callback"])("rejects %s", raw => {expect(safeNextPath(raw)).toBe("/dashboard");});
  it("retains a local route and its query",()=>{expect(safeNextPath("/alerts?state=open")).toBe("/alerts?state=open");});
});
