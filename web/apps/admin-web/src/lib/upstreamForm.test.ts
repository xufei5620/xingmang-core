import { describe, expect, it } from "vitest";
import {
  buildTokenMapParams,
  buildUpstreamParams,
  EMPTY_UPSTREAM_FORM,
  hasUpstreamErrors,
  ratioRequirement,
  validateCredentialRef,
  validateRatioText,
  validateTokenMapForm,
  validateUpstreamForm,
  type UpstreamFormValues,
} from "./upstreamForm";

function form(over: Partial<UpstreamFormValues> = {}): UpstreamFormValues {
  return {
    ...EMPTY_UPSTREAM_FORM,
    system_type: "sub2api",
    access_method: "upstream_key",
    credential_ref: "secret://sub2api/prod-key",
    base_url: "https://relay.example.com",
    recharge_ratio: "1.15",
    platform_id: "sub2api",
    ...over,
  };
}

describe("凭据引用的形状校验", () => {
  it("认 secret://<scope>/<name>", () => {
    expect(validateCredentialRef("secret://sub2api/prod-key")).toBeUndefined();
  });

  it("拒绝没有前缀、缺一段、以及非法字符", () => {
    expect(validateCredentialRef("sub2api/prod-key")).toContain("secret://");
    expect(validateCredentialRef("secret://sub2api")).toContain("两段");
    expect(validateCredentialRef("secret://Sub2API/prod")).toContain("小写");
    expect(validateCredentialRef("secret://-bad/prod")).toContain("小写");
  });

  it("空引用有独立的说法——它是必填，不是「格式错」", () => {
    const problem = validateCredentialRef("");
    expect(problem).toContain("必填");
    expect(problem).not.toContain("secret:// 开头");
  });
});

describe("倍率文本校验", () => {
  it("接受定点十进制", () => {
    for (const ok of ["1", "1.15", "0.9", "+2.5", "1.123456789"]) {
      expect(validateRatioText(ok)).toBeUndefined();
    }
  });

  it("**不用 Number 解析**：科学计数法、十六进制、空白都要被拒", () => {
    // Number("1e3") / Number("0x10") / Number(" 1 ") 全都「合法」，
    // 而后端 money.ParseRatio 一个都不认。拿 Number 当校验会放进去一批
    // 必然被服务端拒绝的写法
    for (const bad of ["1e3", "0x10", "1,5", "一点五"]) {
      expect(validateRatioText(bad)).toBeTruthy();
    }
  });

  it("小数位上限是 9 位", () => {
    expect(validateRatioText("1.123456789")).toBeUndefined();
    expect(validateRatioText("1.1234567891")).toContain("9 位小数");
  });

  it("零与负数被拒，且说清为什么", () => {
    // 非正倍率会被算术层静默当成 1，让「上游涨价」与「倍率填错」
    // 在台账上长得一模一样
    expect(validateRatioText("0")).toContain("不能是 0");
    expect(validateRatioText("0.000")).toContain("不能是 0");
    expect(validateRatioText("-1.2")).toContain("必须为正");
  });
});

describe("倍率与接入方式的三套要求", () => {
  it("计量型必填、订阅型不许填、官方 API 不约束", () => {
    expect(ratioRequirement("upstream_key")).toBe("required");
    expect(ratioRequirement("subscription_account")).toBe("forbidden");
    expect(ratioRequirement("official_api")).toBe("optional");
  });

  it("计量型漏填倍率当场报错，而不是等夜里跑批时台账出现一列空成本", () => {
    const errors = validateUpstreamForm(form({ recharge_ratio: "" }));
    expect(errors.recharge_ratio).toContain("必须配倍率");
  });

  it("订阅型填了倍率也报错——留一个用不上的倍率早晚有人拿去乘一遍", () => {
    const errors = validateUpstreamForm(
      form({ access_method: "subscription_account", recharge_ratio: "1.2" }),
    );
    expect(errors.recharge_ratio).toContain("订阅型");
  });

  it("订阅型不填倍率就过", () => {
    const errors = validateUpstreamForm(
      form({ access_method: "subscription_account", recharge_ratio: "" }),
    );
    expect(hasUpstreamErrors(errors)).toBe(false);
  });
});

describe("上游网址校验", () => {
  it("必须 https", () => {
    expect(validateUpstreamForm(form({ base_url: "http://relay.example.com" })).base_url).toContain(
      "https://",
    );
  });

  it("拒绝 @：user:pass@host 是只读通道最常见的凭据泄漏形态", () => {
    const errors = validateUpstreamForm(form({ base_url: "https://u:p@relay.example.com" }));
    expect(errors.base_url).toContain("@");
    // 报错文案里**不回显**那个串——被拒的正是可能带着凭据的东西
    expect(errors.base_url).not.toContain("u:p");
  });

  it("留空合法：订阅型账号可能没有可读端点", () => {
    expect(validateUpstreamForm(form({ base_url: "" })).base_url).toBeUndefined();
  });
});

describe("其余字段", () => {
  it("币种必须是三位大写", () => {
    expect(validateUpstreamForm(form({ currency: "usd" })).currency).toBeTruthy();
    expect(validateUpstreamForm(form({ currency: "USD" })).currency).toBeUndefined();
  });

  it("日切时区只认固定偏移，不认 IANA 名", () => {
    expect(validateUpstreamForm(form({ business_day_tz: "Asia/Shanghai" })).business_day_tz)
      .toBeTruthy();
    expect(validateUpstreamForm(form({ business_day_tz: "+08:00" })).business_day_tz)
      .toBeUndefined();
  });

  it("platform_id 形态错会被拦：错形态的归属永远匹配不上任何平台", () => {
    expect(validateUpstreamForm(form({ platform_id: "Sub2API" })).platform_id).toBeTruthy();
    // 留空是合法的——未配对是一种状态，不是错误
    expect(validateUpstreamForm(form({ platform_id: "" })).platform_id).toBeUndefined();
  });
});

describe("组装 upstream_account.set 的参数", () => {
  it("可选字段留空也照传空串：这个 Action 是整行替换", () => {
    // 漏传等于清空，所以「表单里看到什么」必须与「写进去什么」一致
    const params = buildUpstreamParams(form({ base_url: "", platform_id: "" }));
    expect(params.base_url).toBe("");
    expect(params.platform_id).toBe("");
    expect(Object.keys(params).sort()).toEqual(
      [
        "access_method",
        "base_url",
        "business_day_tz",
        "credential_ref",
        "currency",
        "platform_id",
        "recharge_ratio",
        "status",
        "system_type",
      ].sort(),
    );
  });

  it("upstream_account_id 是唯一的例外：留空 = 新建，不是清空某个字段", () => {
    expect(buildUpstreamParams(form()).upstream_account_id).toBeUndefined();
    expect(buildUpstreamParams(form({ upstream_account_id: "abc" })).upstream_account_id).toBe(
      "abc",
    );
  });

  it("值先 trim：末尾一个空格换来一个 400，人还得自己去数空格", () => {
    const params = buildUpstreamParams(form({ credential_ref: "  secret://a/b  " }));
    expect(params.credential_ref).toBe("secret://a/b");
  });
});

describe("令牌映射表单", () => {
  it("两侧标识都必填", () => {
    const errors = validateTokenMapForm(
      { upstream_token_id: "", own_account_id: "", credential_ref: "" },
      false,
    );
    expect(errors.upstream_token_id).toBeTruthy();
    expect(errors.own_account_id).toBeTruthy();
  });

  it("sub2api 计量型必须带凭据引用：成本侧要用它打 /v1/usage", () => {
    const errors = validateTokenMapForm(
      { upstream_token_id: "t1", own_account_id: "a1", credential_ref: "" },
      true,
    );
    expect(errors.credential_ref).toContain("/v1/usage");
  });

  it("其余情形允许没有凭据——newapi 走账号级会话，那是**正常状态**", () => {
    const errors = validateTokenMapForm(
      { upstream_token_id: "t1", own_account_id: "a1", credential_ref: "" },
      false,
    );
    expect(errors.credential_ref).toBeUndefined();
  });

  it("填了就要形状对", () => {
    const errors = validateTokenMapForm(
      { upstream_token_id: "t1", own_account_id: "a1", credential_ref: "not-a-ref" },
      false,
    );
    expect(errors.credential_ref).toBeTruthy();
  });

  it("空的凭据引用**不传这个键**：token_map.set 是逐字段写入，不是整行替换", () => {
    const params = buildTokenMapParams("acc-1", {
      upstream_token_id: " t1 ",
      own_account_id: " a1 ",
      credential_ref: "  ",
    });
    expect(params).toEqual({
      upstream_account_id: "acc-1",
      upstream_token_id: "t1",
      own_account_id: "a1",
    });
    expect("credential_ref" in params).toBe(false);
  });
});
