import { describe, expect, it } from "vitest";
import {
  buildProxyAssetParams,
  buildSubscriptionBatchParams,
  effectiveDaysInclusive,
  parseScale6MajorUnits,
  validateProxyAssetForm,
  validateSubscriptionBatchForm,
  type ProxyAssetFormValues,
  type SubscriptionBatchFormValues,
} from "./subscriptionForms";

function batch(over: Partial<SubscriptionBatchFormValues> = {}): SubscriptionBatchFormValues {
  return {
    paidMajor: "29.99",
    surchargeMajor: "",
    currency: "USD",
    startsOn: "2026-08-01",
    expiresOn: "2026-08-31",
    accountCount: "2",
    proxyAssetId: "",
    ...over,
  };
}

function proxy(over: Partial<ProxyAssetFormValues> = {}): ProxyAssetFormValues {
  return {
    paidMajor: "6.20",
    surchargeMajor: "",
    currency: "USD",
    openedOn: "2026-08-01",
    expiresOn: "2026-08-31",
    sharedAccountCount: "2",
    buyPlatform: "Example",
    buyAddress: "https://example.test",
    credentialRef: "secret://finance/proxy-a",
    mounted: true,
    ...over,
  };
}

describe("scale-6 人类金额输入", () => {
  it("只用字符串 / BigInt 归一化零、前导零、尾随零与 6 位小数", () => {
    expect(parseScale6MajorUnits("0", true)).toEqual({ ok: true, minor: "0" });
    expect(parseScale6MajorUnits("0001.230000", true)).toEqual({
      ok: true,
      minor: "1230000",
    });
    expect(parseScale6MajorUnits("0.000001", true)).toEqual({ ok: true, minor: "1" });
    expect(parseScale6MajorUnits("9223372036854.775807", true)).toEqual({
      ok: true,
      minor: "9223372036854775807",
    });
  });

  it("附加费留空是已知零；实付留空则报必填", () => {
    expect(parseScale6MajorUnits("", false)).toEqual({ ok: true, minor: "0" });
    expect(parseScale6MajorUnits("", true)).toEqual({ ok: false, error: "金额必填" });
  });

  it("拒绝符号、负数、指数、分隔符、非法字符、超 6 位小数与 int64 溢出", () => {
    for (const raw of [
      "+1",
      "-1",
      "1e3",
      "1,000",
      ".5",
      "1.",
      "1.0000001",
      "abc",
      "9223372036854.775808",
    ]) {
      const parsed = parseScale6MajorUnits(raw, true);
      expect(parsed.ok, raw).toBe(false);
    }
  });
});

describe("订阅批次表单", () => {
  it("日期含两端，结束早于开始会被拒", () => {
    expect(effectiveDaysInclusive("2026-08-01", "2026-08-01")).toBe(1);
    expect(effectiveDaysInclusive("2026-08-01", "2026-08-31")).toBe(31);
    expect(validateSubscriptionBatchForm(batch({ expiresOn: "2026-07-31" })).expiresOn).toContain(
      "不得早于",
    );
  });

  it("账号数必须是 PostgreSQL integer 可表示的正整数", () => {
    for (const bad of ["", "0", "-1", "1.5", "2147483648", "9007199254740992"]) {
      expect(validateSubscriptionBatchForm(batch({ accountCount: bad })).accountCount, bad).toBeTruthy();
    }
    expect(validateSubscriptionBatchForm(batch({ accountCount: "2147483647" })).accountCount)
      .toBeUndefined();
  });

  it("币种必须是后端已经支持的三位代码，不猜未知币种", () => {
    expect(validateSubscriptionBatchForm(batch({ currency: "USD" })).currency).toBeUndefined();
    expect(validateSubscriptionBatchForm(batch({ currency: "usd" })).currency).toBeTruthy();
    expect(validateSubscriptionBatchForm(batch({ currency: "XYZ" })).currency).toBeTruthy();
  });

  it("paid + surcharge 也必须落在 signed int64 内", () => {
    const errors = validateSubscriptionBatchForm(
      batch({ paidMajor: "9223372036854.775807", surchargeMajor: "0.000001" }),
    );
    expect(errors.surchargeMajor).toContain("合计");
  });

  it("组装 register 参数：空附加费变 0、代理可省略、无退款/终止/环境", () => {
    expect(buildSubscriptionBatchParams("acc-1", batch())).toEqual({
      upstream_account_id: "acc-1",
      paid_minor: "29990000",
      surcharge_minor: "0",
      currency: "USD",
      starts_on: "2026-08-01",
      expires_on: "2026-08-31",
      account_count: 2,
    });
    expect(buildSubscriptionBatchParams("acc-1", batch({ proxyAssetId: "proxy-1" }))).toEqual(
      expect.objectContaining({ proxy_asset_id: "proxy-1" }),
    );
  });
});

describe("代理资产表单", () => {
  it("新建校验金额、期间、币种与正的共享账号数", () => {
    expect(validateProxyAssetForm(proxy(), "create")).toEqual({});
    expect(validateProxyAssetForm(proxy({ paidMajor: "" }), "create").paidMajor).toBeTruthy();
    expect(validateProxyAssetForm(proxy({ expiresOn: "2026-07-31" }), "create").expiresOn)
      .toBeTruthy();
    expect(validateProxyAssetForm(proxy({ sharedAccountCount: "0" }), "create").sharedAccountCount)
      .toBeTruthy();
    expect(
      validateProxyAssetForm(proxy({ sharedAccountCount: "2147483648" }), "create")
        .sharedAccountCount,
    ).toBeTruthy();
  });

  it("CredentialRef 只认 secret://；错误不回显被拒的疑似明文", () => {
    const raw = "plain-password-do-not-echo";
    const error = validateProxyAssetForm(proxy({ credentialRef: raw }), "create").credentialRef;
    expect(error).toContain("secret://");
    expect(error).not.toContain(raw);
  });

  it("拒绝购买 URL 的 userinfo，错误不回显 user:pass", () => {
    const raw = "https://sensitive-user:sensitive-pass@example.test/buy";
    const error = validateProxyAssetForm(proxy({ buyAddress: raw }), "create").buyAddress;
    expect(error).toContain("user:pass@");
    expect(error).not.toContain("sensitive-user");
    expect(error).not.toContain("sensitive-pass");
  });

  it("新建参数保留 mounted=false；编辑参数严格排除冻结字段", () => {
    expect(buildProxyAssetParams(proxy({ mounted: false }), "create")).toEqual({
      paid_minor: "6200000",
      surcharge_minor: "0",
      currency: "USD",
      opened_on: "2026-08-01",
      expires_on: "2026-08-31",
      shared_account_count: 2,
      buy_platform: "Example",
      buy_address: "https://example.test",
      credential_ref: "secret://finance/proxy-a",
      mounted: false,
    });

    expect(buildProxyAssetParams(proxy({ paidMajor: "999", currency: "CNY" }), "edit", "proxy-1"))
      .toEqual({
        proxy_asset_id: "proxy-1",
        buy_platform: "Example",
        buy_address: "https://example.test",
        credential_ref: "secret://finance/proxy-a",
        mounted: true,
      });
  });
});
