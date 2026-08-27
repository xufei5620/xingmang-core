import {
  type CommandItem,
  type NavGroupSpec,
  type NavItemSpec,
  NAV_GROUPS,
  PLATFORM_GROUP_TITLE,
  PLATFORM_NAV_ITEMS,
} from "@xingmang/ui-admin";

/** 一条可跳转的导航结果。 */
export interface NavSearchItem extends CommandItem {
  /** 目标路由。带在条目上而不是按 id 反查：反查表是第二份真相，迟早与索引漂开。 */
  to: string;
}

/** 全局搜索的**范围**。
 *
 *  这一版只索引导航：页面、平台、页签、子页签。用户、订单、渠道、服务器、
 *  告警编号这些**对象**搜索需要后端的搜索端点（实施计划把它排在后面的切片）,
 *  现在没有。
 *
 *  于是输入框的提示语与这句范围说明都必须照实写。原型的占位文案是
 *  「搜索用户、订单、渠道、服务器、告警或审计编号」——照抄它就是承诺一件
 *  做不到的事：人搜「张伟」搜不到，只会以为搜索坏了，或者以为系统里没有这个人。
 *  §12 惯例要的是别把没接的说成接了。 */
export const SEARCH_SCOPE_NOTE =
  "本阶段只搜导航：页面、平台、页签与子页签，选中即跳转。搜索只导航，不执行任何写操作；用户、订单、渠道、服务器、告警编号等对象搜索随后续切片上线。";

export const SEARCH_PLACEHOLDER = "搜索页面、平台、页签（例如：告警、渠道管理、开票）";

/** 类别标签。左列显示，也参与匹配，所以顺手支持了「搜『页签』列出全部页签」。 */
const TYPE_PAGE = "页面";
const TYPE_SUB_TAB = "子页签";
const TYPE_PLATFORM = "平台";
const TYPE_TAB = "页签";

function pageItem(group: NavGroupSpec, item: NavItemSpec): NavSearchItem {
  return {
    id: `page:${item.path}`,
    type: TYPE_PAGE,
    title: item.label,
    meta: group.title,
    // 路径与原型 id 一起进关键词：habitual 用户会直接敲 `audit`、`gov/ops`
    keywords: `${item.path} ${item.id} ${group.id}`,
    to: item.path,
  };
}

function subTabItem(
  group: NavGroupSpec,
  item: NavItemSpec,
  sub: { id: string; label: string },
): NavSearchItem {
  return {
    id: `sub:${item.path}:${sub.id}`,
    type: TYPE_SUB_TAB,
    title: sub.label,
    // 副标题给出父级路径，否则「规则」「环境」这种到处都有的词分不清是哪一页的
    meta: `${group.title} · ${item.label}`,
    keywords: `${item.path} ${item.id} ${sub.id}`,
    to: `${item.path}?sub=${sub.id}`,
  };
}

/** 全部可跳转的导航条目。
 *
 *  从 ui-admin 的 navigation.ts 生成——那是 ADMIN-IA v3 的可执行副本，也是侧栏、
 *  面包屑与路由表的同一份来源。手写一份搜索索引的话，加一页就要记得加两处,
 *  而漏掉的那一处正好是「搜得到、点进去 404」或者「新页面搜不到」。 */
export function buildNavSearchIndex(): NavSearchItem[] {
  const items: NavSearchItem[] = [];

  for (const group of NAV_GROUPS) {
    if (group.id === "platforms") {
      // 平台段的条目在 navigation.ts 里是空的（运行时由 Registry 驱动）,
      // 但**平台目录本身**是静态 IA，可以直接索引
      for (const platform of PLATFORM_NAV_ITEMS) {
        const base = `/platforms/${platform.serviceType}`;
        items.push({
          id: `platform:${platform.serviceType}`,
          type: TYPE_PLATFORM,
          title: platform.label,
          meta: PLATFORM_GROUP_TITLE,
          keywords: `${platform.serviceType} ${platform.prototypeId} ${base}`,
          to: base,
        });
        for (const tab of platform.tabs) {
          items.push({
            id: `tab:${platform.serviceType}:${tab.value}`,
            type: TYPE_TAB,
            title: tab.label,
            meta: `${platform.label}`,
            keywords: `${platform.serviceType} ${tab.value} ${base}`,
            to: `${base}?tab=${tab.value}`,
          });
          for (const sub of tab.subTabs) {
            items.push({
              id: `sub:${platform.serviceType}:${tab.value}:${sub.id}`,
              type: TYPE_SUB_TAB,
              title: sub.label,
              meta: `${platform.label} · ${tab.label}`,
              keywords: `${platform.serviceType} ${tab.value} ${sub.id}`,
              to: `${base}?tab=${tab.value}&sub=${sub.id}`,
            });
          }
        }
      }
      continue;
    }

    for (const item of group.items) {
      items.push(pageItem(group, item));
      for (const sub of item.subTabs) items.push(subTabItem(group, item, sub));
    }
  }

  return items;
}

/** 索引建一次就够：它只依赖静态的 IA 数据，不随渲染变化。 */
export const NAV_SEARCH_INDEX: readonly NavSearchItem[] = buildNavSearchIndex();

