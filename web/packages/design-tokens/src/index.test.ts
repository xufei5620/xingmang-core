import { describe, expect, it } from "vitest";
import { tokens } from "./index";

describe("design tokens", () => {
  it("每个颜色令牌都是 CSS 变量引用", () => {
    for (const v of Object.values(tokens.color)) {
      expect(v).toMatch(/^var\(--xm-/);
    }
  });
  it("语义色齐全（状态色 danger/success/warning 必须存在）", () => {
    expect(tokens.color.danger).toBeDefined();
    expect(tokens.color.success).toBeDefined();
    expect(tokens.color.warning).toBeDefined();
  });
});
