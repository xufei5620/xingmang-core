/** 令牌契约测试要把 tokens.css / tailwind.css 当文本读一遍，只用得上这一个函数。
 *
 *  自己声明而不是加 `@types/node`：那会给一个零依赖的令牌包多钉一条依赖版本，
 *  换来的是整个 Node 类型面——而这里要的只是 readFileSync。
 *  （Vite 的 `?raw` 走不通：CSS 在 node 环境下会被 Vite 的 CSS 插件掏空成空串。） */
declare module "node:fs" {
  export function readFileSync(path: URL | string, encoding: "utf8"): string;
}
