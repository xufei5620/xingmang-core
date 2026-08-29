import {
  CHANGES_BLUEPRINT,
  DESIGN_BLUEPRINT,
  FINANCE_BLUEPRINT,
  IDENTITY_BLUEPRINT,
  OPS_BLUEPRINT,
} from "./governance";
import {
  EXT_AI_BLUEPRINT,
  EXT_APP_BLUEPRINT,
  EXT_INTEGRATION_BLUEPRINT,
  EXT_PUBLISHING_BLUEPRINT,
} from "./ext";
import { SERVER_BLUEPRINT } from "./server";
import type { BlueprintPage, BlueprintTab } from "./types";

export type { BlueprintLink, BlueprintPage, BlueprintTab } from "./types";
export { BlueprintBanner, BlueprintTabView, BlueprintTiles } from "./BlueprintView";

/** 侧栏路径 → 蓝图页。
 *
 *  键是 navigation.ts 里的 `path`，于是「这一页有没有蓝图」由路径决定，
 *  PlaceholderPage 查一次表就知道该渲染蓝图还是通用占位——不需要在
 *  navigation.ts 里加一个 `hasBlueprint` 字段（那会让信息架构那份数据
 *  开始记录实现进度，两件事迟早互相绊住）。
 *
 *  **不在这张表里的路径继续走通用占位页**，行为与本片之前完全一致。 */
const BLUEPRINTS_BY_PATH: Readonly<Record<string, BlueprintPage>> = {
  "/identity": IDENTITY_BLUEPRINT,
  "/finance": FINANCE_BLUEPRINT,
  "/ops": OPS_BLUEPRINT,
  "/changes": CHANGES_BLUEPRINT,
  "/design": DESIGN_BLUEPRINT,
  "/ext/app": EXT_APP_BLUEPRINT,
  "/ext/integration": EXT_INTEGRATION_BLUEPRINT,
  "/ext/publishing": EXT_PUBLISHING_BLUEPRINT,
  "/ext/ai": EXT_AI_BLUEPRINT,
};

/** 平台 serviceType → 蓝图页。
 *
 *  与侧栏那张表分开：平台页的页签走 `?tab=`、由 PlatformDetailPage 渲染，
 *  治理页的子页签走 `?sub=`、由 PlaceholderPage 渲染。两条路径的宿主不同，
 *  合成一张表只会让查表的人先判断自己在哪一侧。 */
const PLATFORM_BLUEPRINTS: Readonly<Record<string, BlueprintPage>> = {
  server: SERVER_BLUEPRINT,
};

/** 按侧栏路径取蓝图；没有则 undefined（调用方回落到通用占位）。 */
export function blueprintForPath(path: string): BlueprintPage | undefined {
  const normalized = path.length > 1 && path.endsWith("/") ? path.slice(0, -1) : path;
  return BLUEPRINTS_BY_PATH[normalized];
}

/** 按平台 serviceType 取蓝图。 */
export function blueprintForPlatform(serviceType: string): BlueprintPage | undefined {
  return PLATFORM_BLUEPRINTS[serviceType];
}

/** 取某个平台某个页签的蓝图内容。
 *
 *  页签 id 认不出来返回 undefined——调用方据此回落到「尚未实现」，
 *  而不是静默显示第一格：那会让一个拼错的 `?tab=` 看起来像是正常页面。 */
export function blueprintTabForPlatform(
  serviceType: string,
  tabId: string,
): BlueprintTab | undefined {
  return blueprintForPlatform(serviceType)?.tabs.find((tab) => tab.id === tabId);
}

/** 全部蓝图，供测试遍历（「每一格都必须有 source」这类断言）。 */
export const ALL_BLUEPRINTS: readonly (readonly [string, BlueprintPage])[] = [
  ...Object.entries(BLUEPRINTS_BY_PATH),
  ...Object.entries(PLATFORM_BLUEPRINTS).map(
    ([serviceType, page]) => [`platform:${serviceType}`, page] as const,
  ),
];
