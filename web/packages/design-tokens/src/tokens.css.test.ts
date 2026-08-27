import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { tokens } from "./index";

const read = (name: string): string => readFileSync(new URL(name, import.meta.url), "utf8");
const tokensCss = read("./tokens.css");
const tailwindCss = read("./tailwind.css");

/** 三态守卫的选择器。写死在测试里而不是从文件里推：这三条**就是**契约本身，
 *  测试的意义正是在有人改动其中一条时失败。
 *
 *  一、`:root` 裸块给出亮色全量；
 *  二、`prefers-color-scheme: dark` 里用 `:not([data-theme="light"])` 守卫，
 *      于是系统深色下显式选亮色仍然选得动；
 *  三、`[data-theme="dark"]` 显式暗色，且**不带 :root 前缀**——容器级暗色
 *      （Storybook 的暗色故事、页面里的暗色预览块）靠的就是这一条。 */
const LIGHT_SELECTOR = ":root";
const MEDIA_DARK_SELECTOR = ':root:not([data-theme="light"])';
const EXPLICIT_DARK_SELECTOR = '[data-theme="dark"]';

const withoutComments = (css: string): string => css.replace(/\/\*[\s\S]*?\*\//g, "");

const bareTokensCss = withoutComments(tokensCss);

/** 取某个选择器的声明块正文。令牌文件里没有嵌套块（@media 内也只有一层），
 *  所以「到第一个 }」就是块尾。 */
function block(selector: string): string {
  const at = bareTokensCss.indexOf(`${selector} {`);
  expect(at, `令牌文件里找不到选择器 ${selector}`).toBeGreaterThanOrEqual(0);
  const from = at + selector.length + 2;
  const to = bareTokensCss.indexOf("}", from);
  expect(to, `选择器 ${selector} 的声明块没有闭合`).toBeGreaterThan(from);
  return bareTokensCss.slice(from, to);
}

/** 块里声明（而不是引用）的自定义属性名。 */
function declared(text: string): Set<string> {
  return new Set([...text.matchAll(/(--[\w-]+)\s*:/g)].map((m) => m[1] as string));
}

/** 文本里引用到的自定义属性名。 */
function referenced(text: string): Set<string> {
  return new Set([...text.matchAll(/var\((--[\w-]+)/g)].map((m) => m[1] as string));
}

const lightBlock = block(LIGHT_SELECTOR);
const mediaDarkBlock = block(MEDIA_DARK_SELECTOR);
const explicitDarkBlock = block(EXPLICIT_DARK_SELECTOR);
const lightDeclared = declared(lightBlock);

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

  it("overlay 令牌存在且为 var(--xm-) 引用", () => {
    expect(tokens.color.overlay).toMatch(/^var\(--xm-/);
  });

  it("深色导航令牌与主题色分开，且同样只是变量引用", () => {
    for (const v of Object.values(tokens.nav)) {
      expect(v).toMatch(/^var\(--xm-color-nav-/);
    }
  });
});

describe("令牌契约", () => {
  it("index.ts 里的每个令牌都在 tokens.css 的亮色 :root 里声明过", () => {
    const groups = [tokens.color, tokens.nav, tokens.font, tokens.radius, tokens.shadow];
    for (const group of groups) {
      for (const [name, value] of Object.entries(group)) {
        for (const ref of referenced(value)) {
          expect(lightDeclared, `index.ts 的 ${name} 引用了未声明的 ${ref}`).toContain(ref);
        }
      }
    }
  });

  it("tailwind.css 映射的每个令牌都在亮色 :root 里声明过", () => {
    for (const ref of referenced(withoutComments(tailwindCss))) {
      expect(lightDeclared, `tailwind.css 映射了未声明的 ${ref}`).toContain(ref);
    }
  });
});

describe("暗色三态守卫", () => {
  it("两个暗色块覆盖的令牌集合完全一致", () => {
    // 三态最容易出的事故：改了一个守卫忘了另一个，跟随系统与手动切换从此分叉，
    // 而且只有「系统是深色」的那台机器上看得出来。
    const media = [...declared(mediaDarkBlock)].sort();
    const explicit = [...declared(explicitDarkBlock)].sort();
    expect(explicit).toEqual(media);
  });

  it("两个暗色块的取值来源完全一致", () => {
    const media = [...referenced(mediaDarkBlock)].sort();
    const explicit = [...referenced(explicitDarkBlock)].sort();
    expect(explicit).toEqual(media);
  });

  it("暗色取值全部来自 :root 上的单一定义，没有就地写死的色值", () => {
    for (const text of [mediaDarkBlock, explicitDarkBlock]) {
      // 每条声明都必须形如 `--x: var(--y)`：暗色色值只有 :root 上那一份，
      // 守卫块只做转接。就地写色值就等于又出现了第二份可漂移的定义。
      for (const line of text.split(";")) {
        if (!line.includes(":")) continue;
        expect(line, `暗色守卫块里出现了非 var() 取值：${line.trim()}`).toMatch(
          /--[\w-]+\s*:\s*var\(--[\w-]+\)\s*$/,
        );
      }
      for (const ref of referenced(text)) {
        expect(lightDeclared, `暗色守卫引用了未声明的 ${ref}`).toContain(ref);
      }
    }
  });

  it("暗色覆盖到的令牌，亮色 :root 上都有全量定义", () => {
    // 系统深色 + 显式 data-theme="light" 时，页面拿到的就是 :root 这一份。
    // 少一个令牌，那台机器上就会有一处颜色回落到浏览器默认值。
    for (const name of declared(mediaDarkBlock)) {
      expect(lightDeclared, `${name} 只在暗色块里有，亮色 :root 缺定义`).toContain(name);
    }
  });
});
