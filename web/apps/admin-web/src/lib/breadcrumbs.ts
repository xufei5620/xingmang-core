import { navItemByPath, PLATFORM_GROUP_TITLE } from "@xingmang/ui-admin";
import { PLATFORM_CATALOG } from "./platforms";

export interface Breadcrumb {
  key: string;
  label: string;
}

/** 当前路径 → 面包屑。
 *
 *  段名与页名都取自 ui-admin 的导航数据（ADMIN-IA v3 的可执行副本），不再另抄
 *  一份路由表。XM-0042 之前这里有一份手抄的 ROUTES，于是侧栏改名之后面包屑
 *  还在说旧名字——同一个页面在屏幕上有两个名字，坐标就不是坐标了。
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
      { key: "section", label: PLATFORM_GROUP_TITLE },
      // 目录里没有的 serviceType 直接显示原值：编一个好听的名字会把
      // 「这个平台我们不认识」这条信息盖掉
      { key: "page", label: spec?.label ?? second },
    ];
  }

  const hit = navItemByPath(`/${segments.join("/")}`);
  if (!hit) return [];
  return [
    { key: "section", label: hit.group.title },
    { key: "page", label: hit.item.label },
  ];
}

/** 环境标识的显示文案。
 *
 *  `undefined` 是一个**有意**的配置值，不是缺失：前端默认不传 environment,
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
