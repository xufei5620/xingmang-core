import { afterAll, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import type { UserEligibilitySummary } from "./types";

// XM-INV-UNIT-DISPLAY。负责人的原话是「用户端这里不要显示这种后台代码类的余额，
// 而是转化后的真实余额」，指的就是这两格。所以这里断言的是**渲染出来的文字**，
// 不是某个函数的返回值：换算函数单测在 lib/service-units.test.ts，这一份回答的是
// 「用户打开页面看到的是不是那个数」。
//
// XM-INV-UNIT-DISPLAY-USERONLY（09-09）。负责人补了一句「原始单位不应该给用户看，
// 这个我们后端自己知道就行」，于是原来那条「原始值退到 title 里」的断言换成了缺席型
// 断言：整段渲染结果里不许出现原始刻度和单位码，**任何属性里都不许**。缺席型断言
// 最容易恒真，所以每一条都配了一句正向断言（那两个换算数必须在），以证明格子确实
// 渲染过；变异验证记在 docs/handoffs/XM-INV-UNIT-DISPLAY.md。
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

// 只截「两格」那一块。属性面的断言限定在这里，否则会把面板别处合法的
// aria-label（「资格状态原因」）也算进来。调用方必须先确认它非空——类名改掉时
// 这个函数会返回空串，而空串对任何「不包含」都成立。
function unitGrid(html: string) {
  const afterGrid = html.split('class="eligibility-unit-grid"')[1] ?? "";
  return afterGrid.split('class="eligibility-reasons"')[0] ?? "";
}

// 形状而不是名单：契约里将来多一个单位码，这条照样拦得住。渲染结果里合法的英文
// （SoloV API、New API、类名、SVG 路径）都不含下划线，所以下划线是这里的判据。
const unitCodeShape = /[A-Z][A-Z0-9]*_[A-Z0-9_]+/;

// 原始刻度：先把非数字字符全去掉再比，于是换一种千分位分组也躲不过去，而且这条
// 判据不是 groupThousands 的副本——那份逻辑改了不会让这道断言悄悄失效。
function containsRawScale(html: string, rawDigits: string) {
  return html.replace(/[^0-9]/g, "").includes(rawDigits);
}

describe("资格卡片的两格源服务单位", () => {
  it("两格都显示换算后的余额数", () => {
    const html = render([summary()]);
    expect(html).toContain(">301.80<");
    expect(html).toContain(">999.49<");
  });

  it("原始刻度与单位码不出现在渲染结果的任何位置——正文不行，属性也不行", () => {
    const html = render([summary()]);
    // 先证明格子真的渲染出来了。少了这两句，下面每一条「不出现」在组件被删空、
    // 或者 items 被过滤掉时也一样成立，那就是一条恒真的断言。
    expect(html).toContain(">301.80<");
    expect(html).toContain(">999.49<");
    expect(containsRawScale(html, RAW_LEGACY)).toBe(false);
    expect(containsRawScale(html, RAW_NONCASH)).toBe(false);
    expect(html).not.toMatch(unitCodeShape);
  });

  it("两格里没有任何承载值的属性——上一版正是从 title 漏出去的", () => {
    const html = render([summary()]);
    const grid = unitGrid(html);
    // unitGrid 靠类名切片，切不到就返回空串。先钉住它切到了东西。
    expect(grid).toContain("切点前旧余额 · 不可开票");
    expect(grid).toContain(">301.80<");
    expect(grid).not.toMatch(/\s(?:title|aria-label|data-[a-z-]+)=/);
  });

  it("每格都带来源口径标签，数字不会孤零零地出现", () => {
    const html = render([summary()]);
    // 断言的是结构不是子串：来源口径必须是那个带 service-unit-origin 的 span，
    // 而不是散落在别处的一段同名文字。这个类名在 styles.css 里**没有**自己的规则
    // （它落在 .eligibility-unit-grid span 上），所以这条断言就是它现在的用处——
    // 顺手删掉类名会在这里变红，而不是等到有人想按它选中元素时才发现没了。
    const labelled = html.match(
      /<span class="service-unit-origin">SoloV API 余额<\/span>/g,
    );
    expect(labelled).toHaveLength(2);
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

  it("单位码为空时显示「暂无法换算」，原因仍是「单位合同待建立」", () => {
    const html = render([
      summary({
        legacyNoninvoiceable: { serviceUnits: RAW_LEGACY, unitCode: null },
        noncash: { serviceUnits: RAW_NONCASH, unitCode: null },
      }),
    ]);
    expect(html).toContain("暂无法换算");
    expect(html).toContain("（单位合同待建立）");
    expect(html).not.toContain("SoloV API 余额");
    // 没有单位码也照样是后台刻度，一样不给用户看。
    expect(containsRawScale(html, RAW_LEGACY)).toBe(false);
    expect(containsRawScale(html, RAW_NONCASH)).toBe(false);
  });

  it("这份 bundle 不认识的单位码降级显示，页面照样出，但不摊开原始值", () => {
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
    expect(html).toContain("暂无法换算");
    expect(html).toContain("（未识别单位）");
    // 旧版在这条分支上把原始数字和单位码一起摆出来给「看截图的人」。改成缺席型
    // 断言后，看截图的人改去管理端账本详情或后端日志拿这两样。
    expect(containsRawScale(html, RAW_LEGACY)).toBe(false);
    expect(html).not.toContain("ZZ_MYSTERY_UNIT");
    expect(html).not.toMatch(unitCodeShape);
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
    // 另一个单位码也一样不给用户看——上面那条缺席断言只喂过 SUB2 的数据。
    expect(html).not.toContain("NEWAPI_QUOTA");
    expect(html).not.toMatch(unitCodeShape);
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
