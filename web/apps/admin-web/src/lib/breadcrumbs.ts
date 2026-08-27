import { PLATFORM_CATALOG } from "./platforms";

export interface Breadcrumb {
  key: string;
  label: string;
}

/** 三段式导航的段名（ADMIN-IA 一、三段式总树）。面包屑的第一级就是它。 */
const SECTION_GLOBAL = "全局";
const SECTION_PLATFORMS = "被管平台";
const SECTION_GOVERNANCE = "平台治理";

/** 路由第一段 → 段名与页名。
 *
 *  与 router.tsx 的 ShellNav 抄自同一张表（ADMIN-IA），但**不共享数据结构**：
 *  导航要的是「有哪些入口、哪些还没接」，面包屑要的是「当前这一页叫什么」。
 *  硬把两者合成一份，会逼着导航去描述它不关心的层级关系。真正的事实源是
 *  docs/architecture/ADMIN-IA.md，两处都照它写。 */
const ROUTES: Record<string, { section: string; label: string }> = {
  dashboard: { section: SECTION_GLOBAL, label: "运营总览" },
  alerts: { section: SECTION_GLOBAL, label: "告警中心" },
  audit: { section: SECTION_GLOBAL, label: "审计事件" },
  registry: { section: SECTION_GOVERNANCE, label: "注册表" },
  settings: { section: SECTION_GOVERNANCE, label: "设置" },
};

/** 当前路径 → 面包屑。
 *
 *  认不出的路径返回空数组而不是编一个「首页 / 未知」：提示条宁可不显示，
 *  也不能给出一个不存在的位置——面包屑一旦说错，它就再也不能被当成坐标用了。 */
export function breadcrumbsFor(pathname: string): Breadcrumb[] {
  const segments = pathname.split("/").filter((s) => s.length > 0);
  const [first, second] = segments;
  if (!first) return [];

  if (first === "platforms") {
    if (!second) return [];
    const spec = PLATFORM_CATALOG.find((p) => p.serviceType === second);
    return [
      { key: "section", label: SECTION_PLATFORMS },
      // 目录里没有的 serviceType 直接显示原值：编一个好听的名字会把
      // 「这个平台我们不认识」这条信息盖掉
      { key: "page", label: spec?.label ?? second },
    ];
  }

  const route = ROUTES[first];
  if (!route) return [];
  return [
    { key: "section", label: route.section },
    { key: "page", label: route.label },
  ];
}

/** 环境标识的显示文案。
 *
 *  `undefined` 是一个**有意**的配置值，不是缺失：前端默认不传 environment，
 *  由后端按调用者自身的环境解析（见 api/config 的 configFromEnv）。所以这里
 *  显示「环境 由服务端解析」而不是「未知」或猜一个 production——
 *  在运营台上，把「服务端说了算」误读成「不知道在哪个环境」会让人不敢操作。 */
export function environmentLabel(environment: string | undefined): {
  label: string;
  hint: string;
} {
  if (!environment) {
    return {
      label: "环境 由服务端解析",
      hint: "前端未显式指定 environment，读取范围由服务端按调用者所在环境决定（跨环境读取本就不允许）",
    };
  }
  return { label: `环境 ${environment}`, hint: `前端显式请求 environment=${environment}` };
}
