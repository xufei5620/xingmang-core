import { describe, expect, it } from "vitest";
import {
  describeMaskedEmail,
  describeUserStatus,
  platformHasUsers,
  UNPARSED_CONTACT,
} from "./users";

describe("账号状态的展示口径", () => {
  it("三种已知状态各有各的语气", () => {
    expect(describeUserStatus("active")).toMatchObject({ label: "正常", tone: "success" });
    expect(describeUserStatus("limited")).toMatchObject({ label: "受限", tone: "warning" });
    expect(describeUserStatus("disabled")).toMatchObject({ label: "停用", tone: "neutral" });
  });

  it("不认识的状态按需关注处理，**不回落成正常**", () => {
    // 把一个被停用的账号显示成绿色「正常」，人会据此排除掉真正的故障原因
    const shown = describeUserStatus("shadow_banned");
    expect(shown.label).toBe("未知");
    expect(shown.tone).not.toBe("success");
    // 原始值进悬停：前端不认识不等于运维不认识
    expect(shown.hint).toContain("shadow_banned");
  });
});

describe("打码邮箱的展示口径", () => {
  it("有值就原样显示——二次处理会让「脱敏在哪一层做」变成两个答案", () => {
    expect(describeMaskedEmail("zh***@example.com").text).toBe("zh***@example.com");
  });

  it("「上游没记」与「记了但形状不认识」分开说", () => {
    // 压成一个「—」会把「我们没解析出来」这条要有人看一眼的信号盖掉
    expect(describeMaskedEmail("").text).toBe("—");
    expect(describeMaskedEmail(UNPARSED_CONTACT).text).toBe("格式不认识");
    expect(describeMaskedEmail("").text).not.toBe(describeMaskedEmail(UNPARSED_CONTACT).text);
  });

  it("每一种都带一句悬停说明", () => {
    for (const raw of ["zh***@example.com", "", UNPARSED_CONTACT]) {
      expect(describeMaskedEmail(raw).hint).toBeTruthy();
    }
  });
});

describe("哪些平台有终端用户", () => {
  it("只有 Sub2API 与 NewAPI", () => {
    expect(platformHasUsers("sub2api")).toBe(true);
    expect(platformHasUsers("newapi")).toBe(true);
  });

  it("CPA 与服务器没有——给它们挂一个永远空的用户页签等于说「这个平台没有用户」", () => {
    // CPA 是渠道代理，它的「用户」是代理商（语义不同）；服务器根本没有终端用户
    for (const p of ["cpa", "server", "invoice", ""]) {
      expect(platformHasUsers(p)).toBe(false);
    }
  });
});
