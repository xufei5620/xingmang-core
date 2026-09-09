import { afterAll, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import type { UserEligibilitySummary } from "./types";

// XM-INV-UNIT-DISPLAY。负责人的原话是「用户端这里不要显示这种后台代码类的余额，
// 而是转化后的真实余额」，指的就是这两格。所以这里断言的是**渲染出来的文字**，
// 不是某个函数的返回值：换算函数单测在 lib/service-units.test.ts，这一份回答的是
// 「用户打开页面看到的是不是那个数」。
//
// 与已有的 App.eligibility-summary-panel.test.tsx 同样的渲染方式（仓库里没有
// jsdom / RTL），同样在导入前打桩 window。

vi.stubGlobal("window", { location: { href: "http://127.0.0.1/" } });
const { EligibilitySummaryPanel, ServiceUnitConversionHint } = await import(
  "./App"
);
afterAll(() => {
  vi.unstubAllGlobals();
});

const RAW_LEGACY = "30179629498";
const RAW_NONCASH = "99948771408";

function summary(
  overrides: Partial<UserEligibilitySummary> = {},
): UserEligibilitySummary {
  return {
    source: "sub2api",
    sourceInstanceId: "10000000-0000-4000-8000-000000000001",
    sourceLabel: "SoloV API",
    bindingStatus: "verified",
    status: "active",
    currency: "CNY",
    availableMinor: 12_300,
    consumedMinor: 12_300,
    unconsumedMinor: 0,
    reservedMinor: 0,
    issuedMinor: 0,
    legacyNoninvoiceable: {
      serviceUnits: RAW_LEGACY,
      unitCode: "SUB2_BALANCE_1E8",
    },
    noncash: { serviceUnits: RAW_NONCASH, unitCode: "SUB2_BALANCE_1E8" },
    reasons: ["READY"],
    eligibilityDegraded: false,
    ...overrides,
  };
}

function render(items: UserEligibilitySummary[]) {
  return renderToStaticMarkup(
    <EligibilitySummaryPanel items={items} loading={false} />,
  );
}

describe("资格卡片的两格源服务单位", () => {
  it("两格都显示换算后的余额数", () => {
    const html = render([summary()]);
    expect(html).toContain(">301.80<");
    expect(html).toContain(">999.49<");
  });

  it("原始刻度不再作为正文出现，只留在 title 里供核对", () => {
    const html = render([summary()]);
    // 这一整串就是负责人说的「后台代码类的余额」。
    expect(html).not.toContain(">30,179,629,498 SUB2_BALANCE_1E8<");
    expect(html).not.toContain(">30179629498<");
    expect(html).toContain('title="30,179,629,498 SUB2_BALANCE_1E8"');
    expect(html).toContain('title="99,948,771,408 SUB2_BALANCE_1E8"');
  });

  it("每格都带来源口径标签，数字不会孤零零地出现", () => {
    const html = render([summary()]);
    expect(html).toContain("SoloV API 余额");
    // 两格各一次。
    expect(html.split("SoloV API 余额").length - 1).toBe(2);
  });

  it("两格的标题文案没有变，还是标着不可开票", () => {
    const html = render([summary()]);
    expect(html).toContain("切点前旧余额 · 不可开票");
    expect(html).toContain("赠送 / 返利 / 管理员额度 · 不可开票");
  });

  it("换算值不带人民币符号——非现金额度不是现金", () => {
    const html = render([summary()]);
    const cells = html.split('class="eligibility-unit-grid"')[1] ?? "";
    const unitBlock = cells.split('class="eligibility-reasons"')[0] ?? "";
    expect(unitBlock).toContain("301.80");
    expect(unitBlock).not.toContain("¥");
    expect(unitBlock).not.toContain("$");
  });

  it("单位码为空时保持「单位合同待建立」的现状文案", () => {
    const html = render([
      summary({
        legacyNoninvoiceable: { serviceUnits: "0", unitCode: null },
        noncash: { serviceUnits: "0", unitCode: null },
      }),
    ]);
    expect(html).toContain("（单位合同待建立）");
    expect(html).not.toContain("SoloV API 余额");
  });

  it("这份 bundle 不认识的单位码降级显示，页面照样出", () => {
    // 类型上 unitCode 是契约里的窄联合，因为 mapServiceUnitSummary 会逐条校验后
    // 才让它进来。这里绕过类型是故意的：渲染层要能挡住「后端比 bundle 新一版」
    // 那一天，而那一天不会先来问类型。
    const unknown = {
      serviceUnits: RAW_LEGACY,
      unitCode: "ZZ_MYSTERY_UNIT",
    } as unknown as UserEligibilitySummary["noncash"];
    const html = render([
      summary({ legacyNoninvoiceable: unknown, noncash: unknown }),
    ]);
    expect(html).toContain("（未识别单位）");
    // 降级时把原始数字和单位码都摆出来，看截图的人能直接说出后端发了什么。
    expect(html).toContain("30,179,629,498 ZZ_MYSTERY_UNIT");
    // 而且没有假装换算成功。
    expect(html).not.toContain(">301.80<");
  });

  it("New API 的来源用 New API 的标签", () => {
    const html = render([
      summary({
        source: "newapi",
        sourceLabel: "SoloV 模型平台",
        legacyNoninvoiceable: { serviceUnits: "500000", unitCode: "NEWAPI_QUOTA" },
        noncash: { serviceUnits: "750000", unitCode: "NEWAPI_QUOTA" },
      }),
    ]);
    expect(html).toContain(">1.00<");
    expect(html).toContain(">1.50<");
    expect(html).toContain("New API 额度");
    expect(html).not.toContain("SoloV API 余额");
  });
});

describe("管理端账本详情的折合提示", () => {
  // 管理端保留原始单位不动，只在后面补一句折合值。
  it("认识的单位码补一句折合值", () => {
    const html = renderToStaticMarkup(
      <ServiceUnitConversionHint
        value={{ serviceUnits: RAW_NONCASH, unitCode: "SUB2_BALANCE_1E8" }}
      />,
    );
    expect(html).toContain("折合");
    expect(html).toContain("999.49");
    expect(html).toContain("SoloV API 余额");
    expect(html).not.toContain("¥");
  });

  it("换算不出来时整句不出现，而不是出现一句空的折合", () => {
    for (const value of [
      { serviceUnits: "0", unitCode: null },
      { serviceUnits: RAW_NONCASH, unitCode: "ZZ_MYSTERY_UNIT" },
      { serviceUnits: "-1", unitCode: "SUB2_BALANCE_1E8" },
    ]) {
      const html = renderToStaticMarkup(
        <ServiceUnitConversionHint value={value} />,
      );
      expect(html).toBe("");
    }
  });

  it("旧的一格保留原样：管理端不丢原始单位", () => {
    // 折合提示是**追加**的，不替换原始值——原始值是管理端对账用的口径。
    // 这条断言盯的是组件本身不含原始数字（原始数字由 <dd> 自己输出）。
    const html = renderToStaticMarkup(
      <ServiceUnitConversionHint
        value={{ serviceUnits: RAW_NONCASH, unitCode: "SUB2_BALANCE_1E8" }}
      />,
    );
    expect(html).not.toContain(RAW_NONCASH);
  });
});
