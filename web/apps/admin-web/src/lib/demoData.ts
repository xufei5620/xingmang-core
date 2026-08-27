/** 「这是不是演示数据」的判定。
 *
 *  背景（Codex #8）：staging 上跑的是 Fake 连接器，指标 source 是
 *  `sub2api-staging`，画出来的卡片、折线、渠道明细与真实运营数据长得一模一样。
 *  一张分不出真假的看板比没有看板更危险——有人会拿它做决定。
 *
 *  所以判定放在**运行时**而不是构建期开关：构建期开关只能保证「我们以为的」
 *  环境，运行时看 source 才能反映「实际画出来的这批数字来自哪里」。 */

/** 已知的演示实例（Fake 连接器写入的 source）。可用 VITE_XM_DEMO_SOURCES 覆盖。
 *
 *  每一项与后端的默认来源标识**逐字对应**（jobs.DefaultSub2APIInstanceID、
 *  jobs.DefaultNewAPIInstanceID、reqlog.FakeInstance）。匹配是整串相等而不是
 *  前缀，所以接入新的 Fake 连接器时必须回来加一行——漏了不会报错，只会让那批
 *  演示数字在页面上与真实运营读数长得一模一样，正是本文件要防的那件事。
 *
 *  `reqlog-fake` 的赌注比另外两项高一档（XM-0039）：它挂的不是几个假数字，
 *  而是一整页编造的用户对话。真实 reqlog 实例叫 `reqlog-<环境>`，
 *  永远不会命中这一行。 */
export const DEFAULT_DEMO_SOURCES = ["sub2api-staging", "newapi-staging", "reqlog-fake"];

/** 横幅文案。写死在这里而不是散在组件里：它是一句对外承诺的反面，
 *  改动应当显眼到能被 review 抓住。 */
export const DEMO_BANNER_TEXT = "当前展示的是演示数据（Fake 连接器），非真实运营数据";

/** 横幅模式。
 *  - auto：默认，看指标 source 是否命中已知演示实例
 *  - demo：强制显示（部署方明确知道这套是演示环境，不必等指标回来）
 *  - real：强制不显示（真实实例恰好取了个演示名字时的逃生口） */
export type DataBadgeMode = "auto" | "demo" | "real";

export interface DemoDataConfig {
  mode: DataBadgeMode;
  demoSources: string[];
}

function parseMode(raw: string | undefined): DataBadgeMode {
  const value = (raw ?? "").trim().toLowerCase();
  if (value === "demo") return "demo";
  if (value === "real") return "real";
  // 认不出的值按 auto 处理：拼错一个环境变量不该变成「悄悄关掉警告」
  return "auto";
}

function parseSources(raw: string | undefined): string[] {
  const parsed = (raw ?? "")
    .split(",")
    .map((s) => s.trim().toLowerCase())
    .filter((s) => s.length > 0);
  return parsed.length > 0 ? parsed : DEFAULT_DEMO_SOURCES;
}

export function demoDataConfigFromEnv(env: ImportMetaEnv): DemoDataConfig {
  return {
    mode: parseMode(env.VITE_XM_DATA_BADGE),
    demoSources: parseSources(env.VITE_XM_DEMO_SOURCES),
  };
}

/** 应用默认配置。测试里请自己造 DemoDataConfig，不要依赖它。 */
export const appDemoDataConfig: DemoDataConfig = demoDataConfigFromEnv(import.meta.env);

/** 指标来源命中演示实例了吗。
 *
 *  按整串相等比（大小写不敏感）而不是前缀/包含：`sub2api-staging-real` 这种
 *  名字不该被误判成演示数据，误报会把横幅变成人人无视的噪音。 */
export function isDemoSource(source: string, demoSources: string[]): boolean {
  const value = source.trim().toLowerCase();
  return value.length > 0 && demoSources.includes(value);
}

/** 当前这批指标该不该挂演示数据横幅。
 *
 *  只要有**一个** source 命中就挂：混着一半真一半假的看板同样不能被当真。 */
export function shouldShowDemoBanner(sources: string[], config: DemoDataConfig): boolean {
  if (config.mode === "demo") return true;
  if (config.mode === "real") return false;
  return sources.some((s) => isDemoSource(s, config.demoSources));
}
