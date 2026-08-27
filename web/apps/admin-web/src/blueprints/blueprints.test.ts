import { GOVERNANCE_NAV_ITEMS, EXT_NAV_ITEMS, platformNavSpec } from "@xingmang/ui-admin";
import { describe, expect, it } from "vitest";
import { ALL_BLUEPRINTS, blueprintForPath, blueprintForPlatform } from "./index";
import type { BlueprintPage } from "./types";

/** 蓝图规格与信息架构之间的对账（UI 第 6 片）。
 *
 *  蓝图规格是**第二份**记着「有哪些页签、叫什么」的数据——第一份是
 *  ui-admin/navigation.ts（ADMIN-IA 的可执行副本）。两份数据一旦漂开，
 *  症状是每一格看起来都正常、只是内容对不上标题：那种错在页面上完全看不出来。
 *
 *  所以本文件的第一组断言不测「蓝图好不好」，只测**两份数据没漂**。 */

/** navigation.ts 里各页的子页签 id，按路径取。 */
function navSubTabIds(path: string): readonly string[] {
  const item = [...GOVERNANCE_NAV_ITEMS, ...EXT_NAV_ITEMS].find((entry) => entry.path === path);
  if (!item) throw new Error(`navigation.ts 里没有 ${path}——蓝图指向了一个不存在的页`);
  return item.subTabs.map((tab) => tab.id);
}

describe("蓝图与信息架构对账", () => {
  const governancePaths = ["/identity", "/finance", "/ops", "/changes", "/design"] as const;
  const extPaths = ["/ext/app", "/ext/integration", "/ext/publishing", "/ext/ai"] as const;

  it.each([...governancePaths, ...extPaths])("%s 的页签 id 与 navigation.ts 逐一相等", (path) => {
    const blueprint = blueprintForPath(path);
    expect(blueprint, `${path} 应当有蓝图`).toBeDefined();
    expect(blueprint?.tabs.map((tab) => tab.id)).toEqual(navSubTabIds(path));
  });

  it.each([...governancePaths, ...extPaths])("%s 的页签名与 navigation.ts 逐字相同", (path) => {
    const item = [...GOVERNANCE_NAV_ITEMS, ...EXT_NAV_ITEMS].find((entry) => entry.path === path);
    const labels = Object.fromEntries(item!.subTabs.map((tab) => [tab.id, tab.label]));
    for (const tab of blueprintForPath(path)!.tabs) {
      // 页签名两处都写了一遍，但**只有 navigation.ts 那份是权威**：
      // 侧栏、面包屑与 Storybook 都读它。这里对齐是为了让页内页签与侧栏
      // 说的是同一个词——XM-0042 之前「注册表 / 资源目录」就同屏出现过
      expect(tab.label, `${path} 的 ${tab.id}`).toBe(labels[tab.id]);
    }
  });

  it("服务器平台的 7 个页签与 navigation.ts 逐一相等", () => {
    const nav = platformNavSpec("server");
    expect(nav, "navigation.ts 里应当有 server 平台").toBeDefined();
    const blueprint = blueprintForPlatform("server");
    expect(blueprint?.tabs.map((tab) => tab.id)).toEqual(nav!.tabs.map((tab) => tab.value));
    expect(blueprint?.tabs.map((tab) => tab.label)).toEqual(nav!.tabs.map((tab) => tab.label));
  });

  it("服务器页签用新 id，不用旧路由别名", () => {
    // ADMIN-IA §三 的三条别名：containers→services、certs→domains、logs→monitoring。
    // 别名由路由层处理；蓝图只认新 id——两处都认的话，一个拼错的页签名
    // 会被静默当成别名放行，而那正是别名机制最容易被滥用的方式
    const ids = blueprintForPlatform("server")!.tabs.map((tab) => tab.id);
    for (const alias of ["containers", "certs", "logs"]) {
      expect(ids, `${alias} 是旧别名，不该出现在蓝图里`).not.toContain(alias);
    }
  });
});

describe("每一格都说得出自己在等什么", () => {
  it.each(ALL_BLUEPRINTS)("%s 的每个页签都有非空 source", (_name, page: BlueprintPage) => {
    for (const tab of page.tabs) {
      // 一格没有来源说明的空页，与「这个功能没有数据」在屏幕上长得一模一样。
      // source 是把这两者分开的那句话，所以它必填
      expect(tab.source.trim(), `${tab.id} 缺少 source`).not.toBe("");
    }
  });

  it.each(ALL_BLUEPRINTS)("%s 的每张表都说得出数据将来从哪来", (_name, page: BlueprintPage) => {
    for (const tab of page.tabs) {
      for (const table of tab.tables ?? []) {
        expect(table.source.trim(), `${tab.id}/${table.caption} 缺少 source`).not.toBe("");
        expect(table.columns.length, `${tab.id}/${table.caption} 没有列`).toBeGreaterThan(0);
      }
    }
  });

  it.each(ALL_BLUEPRINTS)("%s 的每个统计格都有口径说明", (_name, page: BlueprintPage) => {
    const tiles = [...(page.tiles ?? []), ...page.tabs.flatMap((tab) => tab.tiles ?? [])];
    for (const tile of tiles) {
      // 一个孤零零的「3」在运营台上没有意义，人第一个问题永远是「哪三个」
      expect(tile.note.trim(), `${tile.label} 缺少口径说明`).not.toBe("");
    }
  });
});

/** 蓝图里**不许出现样例数字**。
 *
 *  原型每张卡都有数字（`¥5,130.00`、`6 台`、`98.7%`），它们是样例。抄进来的话，
 *  这些页在任何一次截图或演示里都会被读成「已经有数据了」——原型靠顶部一条
 *  横幅说明这件事，但横幅救不了截图，人截的是卡片。
 *
 *  这条测试扫的是**规格数据**而不是渲染结果：渲染层已经把主数位钉成「—」了，
 *  真正的风险是有人后来往 note / source / 列头里补了一句「当前 6 台」。 */
describe("规格里没有样例数字", () => {
  // 金额、百分比、带单位的计数——这三类是原型样例数字的全部形态
  const SAMPLE_NUMBER = /[¥$]\s?\d|\d+(\.\d+)?\s?%|\d+\s?(台|条|个|人|次)/;

  // 白名单：这些是**口径或引文**的一部分，不是读数。
  //   「30 天内续费」是卡片的定义（它数的就是 30 天内的），「保留 30 天」是策略；
  //   「宪法 7 条」里的「条」是条款序号不是计量单位——这一条是本正则最容易
  //   误伤的形状，因为规格引用在这些文案里到处都是。
  const CALIBRATION = /宪法|ADR-\d+|XM-\d+|§|30 天|24 小时|4px|14 天|L3|L4/;

  function texts(page: BlueprintPage): readonly string[] {
    const out: string[] = [page.description, page.banner ?? ""];
    for (const tile of page.tiles ?? []) out.push(tile.label, tile.note);
    for (const tab of page.tabs) {
      out.push(tab.label, tab.source);
      for (const tile of tab.tiles ?? []) out.push(tile.label, tile.note);
      for (const filter of tab.filters ?? []) out.push(filter);
      for (const table of tab.tables ?? []) out.push(table.caption, table.source, ...table.columns);
      for (const card of tab.cards ?? []) out.push(card.title, card.hint ?? "", ...card.keys);
      for (const note of tab.notes ?? []) out.push(note.title, note.body);
    }
    return out;
  }

  it.each(ALL_BLUEPRINTS)("%s 不含样例读数", (_name, page: BlueprintPage) => {
    for (const text of texts(page)) {
      if (CALIBRATION.test(text)) continue;
      expect(SAMPLE_NUMBER.test(text), `疑似样例数字：${text}`).toBe(false);
    }
  });
});
