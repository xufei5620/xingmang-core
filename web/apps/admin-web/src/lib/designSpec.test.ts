import { tokens } from "@xingmang/design-tokens";
import { describe, expect, it } from "vitest";
import {
  COLOR_TOKEN_REFS,
  CONTROL_HEIGHT_VARIABLES,
  DESIGN_DEMO_ROWS,
  NAV_TOKEN_REFS,
  RADIUS_TOKEN_REFS,
  RADIUS_USAGE,
  SPACING_VARIABLES,
  TYPE_SCALE,
  cssVariableOf,
  resolvedTokenValue,
  tokenRefs,
} from "./designSpec";

describe("cssVariableOf", () => {
  it("从单一 var() 引用里取出变量名", () => {
    expect(cssVariableOf("var(--xm-color-accent)")).toBe("--xm-color-accent");
    expect(cssVariableOf("  var(--xm-radius-sm)  ")).toBe("--xm-radius-sm");
  });

  it("组合值与字面量一律取不出来，而不是猜一个", () => {
    // 从 `1px solid var(--a)` 里挑出 --a 去实读，读回来的是「那一个颜色」而不是
    // 这条声明本身的值——显示出来是错的，而且错得看不出来
    expect(cssVariableOf("1px solid var(--xm-color-edge)")).toBeNull();
    expect(cssVariableOf("var(--xm-color-accent) var(--xm-color-fg)")).toBeNull();
    expect(cssVariableOf("#4353b8")).toBeNull();
    expect(cssVariableOf("")).toBeNull();
  });
});

describe("resolvedTokenValue", () => {
  it("空串与全空白都是「读不到」，不是空色值", () => {
    // 浏览器对一个**没有定义**的自定义属性返回的正是空串
    expect(resolvedTokenValue("")).toBeNull();
    expect(resolvedTokenValue("   ")).toBeNull();
    expect(resolvedTokenValue(null)).toBeNull();
    expect(resolvedTokenValue(undefined)).toBeNull();
  });

  it("读到的值去掉两端空白后原样返回", () => {
    // Chrome 取自定义属性时会带上声明里的前导空格
    expect(resolvedTokenValue(" #4353b8")).toBe("#4353b8");
    expect(resolvedTokenValue("0.375rem")).toBe("0.375rem");
  });
});

describe("tokenRefs", () => {
  it("按 tokens 逐条摊平，名字带上分组前缀", () => {
    const refs = tokenRefs("demo", { alpha: "var(--demo-alpha)", beta: "var(--demo-beta)" });
    expect(refs).toEqual([
      { name: "demo.alpha", variable: "--demo-alpha", reference: "var(--demo-alpha)" },
      { name: "demo.beta", variable: "--demo-beta", reference: "var(--demo-beta)" },
    ]);
  });

  it("取不出变量名的条目仍然进清单，只是 variable 为 null", () => {
    // 直接跳过的话，这一页会安静地少展示一条——而「少了一条」在一屏色卡里看不出来
    const refs = tokenRefs("demo", { weird: "1px solid red" });
    expect(refs).toEqual([{ name: "demo.weird", variable: null, reference: "1px solid red" }]);
  });

  it("颜色与深色导航两组令牌一条不漏地进了清单", () => {
    // 名单从 tokens 推导而不是手抄：design-tokens 新增一个语义色，这一页跟着长
    expect(COLOR_TOKEN_REFS.map((ref) => ref.name)).toEqual(
      Object.keys(tokens.color).map((key) => `color.${key}`),
    );
    expect(NAV_TOKEN_REFS.map((ref) => ref.name)).toEqual(
      Object.keys(tokens.nav).map((key) => `nav.${key}`),
    );
    // 深色导航自成一组、不随主题翻转，所以它必须与 color.* 分开展示
    expect(NAV_TOKEN_REFS.map((ref) => ref.variable)).toContain("--xm-color-nav-surface");
    expect(COLOR_TOKEN_REFS.map((ref) => ref.variable)).toContain("--xm-color-table-stripe");
  });

  it("要实读的变量名全部形如 --xm-", () => {
    for (const ref of [...COLOR_TOKEN_REFS, ...NAV_TOKEN_REFS, ...RADIUS_TOKEN_REFS]) {
      expect(ref.variable, `${ref.name} 取不出变量名`).toMatch(/^--xm-/);
    }
  });
});

describe("刻度名单", () => {
  it("间距与控件高度只列名字、不抄值", () => {
    for (const variable of [...SPACING_VARIABLES, ...CONTROL_HEIGHT_VARIABLES]) {
      expect(variable).toMatch(/^--xm-(space|control-h)-[\w-]+$/);
    }
  });

  it("圆角四档的用途说明与 tokens.radius 的键一一对应", () => {
    // 用途表少一档，页面上就会有一个圆角没人说得出它用在哪
    expect(RADIUS_USAGE.map((usage) => usage.token)).toEqual(Object.keys(tokens.radius));
  });
});

describe("字体层级", () => {
  it("每一档都说得出它现在由谁在用", () => {
    // 「这一档给标题用」是同义反复；要的是「PageHeader 的 h2 用的就是它」，好去核对
    for (const entry of TYPE_SCALE) {
      expect(entry.usedBy.trim(), `${entry.label} 没写现用于谁`).not.toBe("");
      expect(entry.className.trim(), `${entry.label} 没写类组合`).not.toBe("");
    }
  });

  it("蓝图「字体层级」卡的五个称呼一个不少", () => {
    expect(TYPE_SCALE.map((entry) => entry.label)).toEqual([
      "页面标题",
      "卡片标题",
      "正文内容",
      "辅助文字",
      "数字与时间",
    ]);
  });
});

describe("表格演示行", () => {
  /** 金额、百分比、带单位的计数——原型样例数字的全部形态（同 blueprints.test.ts）。 */
  const READING_SHAPED = /[¥$]\s?\d|\d+(\.\d+)?\s?%|\d+\s?(台|条|个|人|次)/;

  it("每个字段都不含读数形状的数字", () => {
    // 这一页是全站唯一允许出现演示行的地方，所以这条断言是它的门禁：
    // 有人往演示行里补一个「¥120」，任何一张截图都会被读成「已经有数据了」
    for (const row of DESIGN_DEMO_ROWS) {
      for (const [field, value] of Object.entries(row)) {
        expect(READING_SHAPED.test(String(value)), `${row.id}/${field}：${String(value)}`).toBe(
          false,
        );
      }
    }
  });

  it("行标识一眼看得出是合成的，哈希是全 0、编号带 demo 前缀", () => {
    expect(DESIGN_DEMO_ROWS.map((row) => row.name)).toEqual(["示例行 A", "示例行 B", "示例行 C"]);
    for (const row of DESIGN_DEMO_ROWS) {
      expect(row.hash).toBe(`sha256:${"0".repeat(64)}`);
      expect(row.auditId.startsWith("audit_demo_")).toBe(true);
    }
  });

  it("两种行内详情都有样例行，否则展开区有一半演示不出来", () => {
    const kinds = new Set(DESIGN_DEMO_ROWS.map((row) => row.detailKind));
    expect([...kinds].sort()).toEqual(["evidence", "plain"]);
  });

  it("行数多于每页两条，分页控件才是活的", () => {
    // 页面把 pageSize 定成 2；只剩两行的话「下一页」永远点不动，
    // 而这一格要说明的正是分页长什么样
    expect(DESIGN_DEMO_ROWS.length).toBeGreaterThan(2);
  });
});
