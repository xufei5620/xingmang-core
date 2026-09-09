/** 中文对照表的对账测试要把后端 Go 源码当文本读一遍（labels.reconcile.test.ts），
 *  用得上的只有这两个函数。
 *
 *  自己声明而不是加 `@types/node`：admin-web 是浏览器包，钉一条 Node 类型依赖
 *  换来的是整个 Node 类型面，而这里要的只是读文件和列目录。做法与
 *  web/packages/design-tokens/src/node-fs.d.ts 一致。 */
declare module "node:fs" {
  export function readFileSync(path: URL | string, encoding: "utf8"): string;
  export function readdirSync(
    path: URL | string,
    options?: { recursive?: boolean },
  ): string[];
}
