import { filterCommandItems, NAV_GROUPS, PLATFORM_NAV_ITEMS } from "@xingmang/ui-admin";
import { describe, expect, it } from "vitest";
import {
  buildNavSearchIndex,
  NAV_SEARCH_INDEX,
  SEARCH_PLACEHOLDER,
  SEARCH_SCOPE_NOTE,
} from "./searchIndex";

const index = NAV_SEARCH_INDEX;
const byId = (id: string) => index.find((item) => item.id === id);

describe("导航搜索索引：由 navigation.ts 生成，不手抄", () => {
  it("每一个导航页都能搜到，且目标就是它的路径", () => {
    // 手写索引的话，加一页要记得改两处；漏掉的那一处就是「新页面搜不到」
    for (const group of NAV_GROUPS) {
      for (const item of group.items) {
        const hit = byId(`page:${item.path}`);
        expect(hit?.title).toBe(item.label);
        expect(hit?.to).toBe(item.path);
        expect(hit?.meta).toBe(group.title);
      }
    }
  });

  it("每一个子页签都能搜到，目标带上 ?sub=", () => {
    const alertsSub = byId("sub:/alerts:incidents");
    expect(alertsSub?.title).toBe("故障事件");
    expect(alertsSub?.to).toBe("/alerts?sub=incidents");
    // 副标题给出父级，否则「规则」「环境」这种到处都有的词分不清是哪一页的
    expect(alertsSub?.meta).toBe("全局 · 告警与故障");
  });

  it("四个平台、每个页签、每个平台子页签都能搜到", () => {
    for (const platform of PLATFORM_NAV_ITEMS) {
      expect(byId(`platform:${platform.serviceType}`)?.to).toBe(
        `/platforms/${platform.serviceType}`,
      );
      for (const tab of platform.tabs) {
        expect(byId(`tab:${platform.serviceType}:${tab.value}`)?.to).toBe(
          `/platforms/${platform.serviceType}?tab=${tab.value}`,
        );
      }
    }
    const invoiceSub = byId("sub:sub2api:finance:invoices");
    expect(invoiceSub?.title).toBe("开票");
    expect(invoiceSub?.to).toBe("/platforms/sub2api?tab=finance&sub=invoices");
    expect(invoiceSub?.meta).toBe("Sub2API · 支付与财务");
  });

  it("id 全局唯一——option 的 DOM id 由它拼出来，重了 aria-activedescendant 就会指错", () => {
    const ids = index.map((item) => item.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("每条都有目标路由，没有「搜得到但去不了」的条目", () => {
    for (const item of index) {
      expect(item.to.startsWith("/")).toBe(true);
      expect(item.title.length).toBeGreaterThan(0);
    }
  });

  it("索引里没有任何写操作入口（ADMIN-IA §七 红线：搜索只导航）", () => {
    // 一个能在搜索框里按 Enter 就执行的写操作，没有任何地方能承载「你确定吗」
    for (const item of index) {
      // 判据是「这条会不会执行点什么」，不是「名字里有没有敏感词」——
      // 「退款与冲正」是一个**页面**，导航到它当然可以
      expect(item.to).not.toContain("/api/");
      expect(item.to).not.toContain("execute");
      expect(item.to.startsWith("/")).toBe(true);
    }
  });

  it("重复调用得到同样的索引——它只依赖静态 IA", () => {
    expect(buildNavSearchIndex().map((i) => i.id)).toEqual(index.map((i) => i.id));
  });
});

describe("匹配规则", () => {
  it("中文标题按子串匹配", () => {
    const hits = filterCommandItems(index, "渠道管理");
    expect(hits.length).toBeGreaterThan(0);
    expect(hits.every((h) => h.title === "渠道管理")).toBe(true);
  });

  it("也能用路径与 service_type 敲出来", () => {
    expect(filterCommandItems(index, "/registry").some((h) => h.to === "/registry")).toBe(true);
    expect(filterCommandItems(index, "sub2api").length).toBeGreaterThan(0);
  });

  it("大小写与首尾空格不影响", () => {
    expect(filterCommandItems(index, "  SUB2API  ").length).toBe(
      filterCommandItems(index, "sub2api").length,
    );
  });

  it("空查询给全部——刚打开面板时是一份可浏览的目录", () => {
    expect(filterCommandItems(index, "")).toHaveLength(index.length);
  });

  it("搜不到就是空，不做模糊兜底", () => {
    // 模糊匹配会让「为什么这条没出来」变得没法解释
    expect(filterCommandItems(index, "查无此页签")).toHaveLength(0);
  });
});

describe("范围文案必须诚实", () => {
  it("提示语不承诺搜对象——这一版搜不到用户和订单", () => {
    // 原型的占位是「搜索用户、订单、渠道、服务器、告警或审计编号」。
    // 照抄它就是承诺一件做不到的事：人搜「张伟」搜不到，
    // 只会以为搜索坏了，或者以为系统里没有这个人
    expect(SEARCH_PLACEHOLDER).not.toContain("用户");
    expect(SEARCH_PLACEHOLDER).not.toContain("订单");
    expect(SEARCH_SCOPE_NOTE).toContain("只导航");
    expect(SEARCH_SCOPE_NOTE).toContain("后续切片");
  });
});
