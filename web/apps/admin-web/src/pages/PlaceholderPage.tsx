import { navItemByPath, navStageHint, PageHeader, type NavItemSpec } from "@xingmang/ui-admin";
import { Badge, EmptyState, Tabs } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { useLocation, useSearchParams } from "react-router";
import {
  BlueprintBanner,
  BlueprintTabView,
  BlueprintTiles,
  blueprintForPath,
  type BlueprintPage,
} from "../blueprints";
import { InvoiceConsolePanel } from "../components/InvoiceConsolePanel";
import { NotFoundView } from "./NotFoundPage";

/*  这里曾经有一份 `PLACEHOLDER_COPY`：给每一页写死一句「将来放什么、归谁做」，
 *  作为页头 description 的兜底。**2026-09-08 全部删除，因为 11 条无一可达。**
 *
 *  两类死法，各自的成因不同，值得分开记：
 *
 *  - `/actions` `/jobs` `/identity` `/finance` `/ops` `/changes` `/design` 七条——
 *    这些页都已建成（`built: true`），而 `placeholderRoutes` 只收 `!item.built`
 *    的条目，所以它们**根本不由本组件渲染**。页建成时没人回头删这里的副本。
 *  - `/ext/*` 四条——取值是 `blueprint?.description ?? PLACEHOLDER_COPY[path]`，
 *    而这四个路径**都有蓝图**（`blueprints/index.ts`），蓝图那份恒胜出。
 *    它们从写下的那天起就没生效过。
 *
 *  留着的代价不是占地方，是**它们的内容还在过期**：其中几条写着「随
 *  Foundation-B 上线」，而 Foundation-B 已经交付并启用。一份不会渲染的副本，
 *  唯一的作用就是在下一次改文案时被漏掉、然后误导读到源码的人。同一个理由
 *  此前已经删掉过本文件里的 `/actions` 门禁横幅与 `governanceSubTabOverride`。
 *
 *  将来若真有一个未建成、又没有蓝图的页需要一句说明：**给它写蓝图**，
 *  别在这里恢复第二份真相源。 */

/** 未实装页的统一骨架：页头 + 子页签条 + 每格一个诚实占位。
 *
 *  为什么要建这些空页，而不是等内容做好再一起上：导航是这次重构的产出，而一个
 *  指向 404 的导航项比没有这一项更糟。摆在这里的是**路由位**，页头上「未建」的
 *  徽章与每一格里的说明保证它不会被误读成「这个功能没有数据」。
 *
 *  页面本身不认识自己是哪一页——它从路径反查导航数据（ADMIN-IA 的可执行副本）。
 *  于是「加一页」= 在 navigation.ts 里加一条，侧栏、路由、面包屑、这一页同时到位，
 *  没有第二个地方需要记得改。 */
export function PlaceholderPage() {
  const { pathname } = useLocation();
  const hit = navItemByPath(pathname);
  // 路由表是由同一份导航数据生成的，所以这一支正常走不到；真走到了说明两者
  // 已经漂开，那时候显示 Not Found 比显示一个没有名字的空壳诚实
  if (!hit) return <NotFoundView pathname={pathname} />;
  return <PlaceholderBody item={hit.item} />;
}

function PlaceholderBody({ item }: { item: NavItemSpec }) {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const hint = navStageHint(item);

  // 子页签也进 Search Params：一格是可以贴给同事的地址（交接文档 §8）。
  // 认不出来的 ?sub= 与认不出来的 ?tab= 同样处理——不静默回落第一格
  const active = rawSub === null || rawSub === "" ? item.subTabs[0]?.id : rawSub;
  const known = item.subTabs.some((tab) => tab.id === active);
  if (item.subTabs.length > 0 && !known) {
    return (
      <NotFoundView
        pathname={`${item.path}?sub=${rawSub ?? ""}`}
        detail={`「${item.label}」没有名为 ${rawSub} 的子页签`}
      />
    );
  }

  const selectSub = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("sub", value);
    // 换子页签用 replace：连点五格不该在浏览器里堆五条历史，
    // 否则「后退」变成逐格倒着走，而人想回的是上一个页面
    setSearchParams(next, { replace: true });
  };

  // 有蓝图规格的页走蓝图（UI 第 6 片）：页签、列头与卡片结构照原型，
  // 数字一个不显示。没有的继续走下面的通用占位，行为不变。
  const blueprint = blueprintForPath(item.path);

  return (
    <section>
      <PageHeader
        title={item.label}
        status={hint ? <Badge tone="warning">{hint}</Badge> : null}
        description={blueprint?.description}
      />
      <div className="flex flex-col gap-3">
        {blueprint?.banner ? <BlueprintBanner text={blueprint.banner} /> : null}
        <PlaceholderGate item={item} />
        {blueprint?.tiles ? <BlueprintTiles tiles={blueprint.tiles} /> : null}
        {item.subTabs.length > 0 ? (
          <Tabs
            value={active}
            onValueChange={selectSub}
            items={item.subTabs.map((tab) => ({
              value: tab.id,
              label: tab.label,
              content: subTabContent(item.path, blueprint, tab.id, tab.label, item.stage),
            }))}
          />
        ) : (
          <EmptyState
            title={`「${item.label}」尚未实现`}
            description={`本次（XM-0042）只重构了导航与路由。阶段 ${item.stage}。`}
          />
        )}
      </div>
    </section>
  );
}

/** 一个子页签渲染什么：先看有没有蓝图，没有就还是
 *  那句诚实的「尚未实现」。
 *
 *  按 **id** 匹配而不是按下标：navigation.ts 与蓝图规格是两份数据，
 *  按下标对齐的话，其中一边插一格就会让后面全部错位——而错位之后每一格
 *  看起来都仍然正常，只是内容对不上标题。`blueprints.test.ts` 另有一条
 *  断言两边的 id 集合逐一相等，这里是运行时的第二道。 */
function subTabContent(
  path: string,
  blueprint: BlueprintPage | undefined,
  tabId: string,
  tabLabel: string,
  stage: string,
) {
  // 这里曾经有一支 governanceSubTabOverride，把「跨平台财务 → 开票集成」
  // 换成真实的 InvoiceConsolePanel。XM-FINANCE-GLOBAL0（2026-09-07）把
  // /finance 建成了真实页面（built: true），于是这条路径再也走不到它——
  // 那一支随即成为死代码，且与 FinancePage 里的实现逐字重复。删掉。
  // 同类教训见 XM-0030b-ui：一个不会渲染的副本除了制造措辞漂移没有别的作用。
  const spec = blueprint?.tabs.find((tab) => tab.id === tabId);
  if (spec) return <BlueprintTabView tab={spec} />;
  return (
    <EmptyState
      title={`「${tabLabel}」尚未实现`}
      description={`本次（XM-0042）只重构了导航与路由：这一格的位置、命名与地址已经定下来，内容按实施计划的后续切片实现。阶段 ${stage}。`}
    />
  );
}

/** 页面级门禁说明。
 *
 *  红线要求在页面上**看得见**，不能只写在文档里：实施计划 §2.5——扩展能力
 *  四页是只读蓝图，明确标注仅预览、不保存、不发布、不执行。
 *
 *  文案里原本有一句「**这一段**不会因为页面存在就提前建后端」。判据从
 *  路径前缀改成 `!item.built` 之后，「这一段」这个指代就不再成立——它只对
 *  扩展能力段为真，而条件现在覆盖任何未建成的页。改成「页面存在不代表后端
 *  已经建好」，对每一个能走到这里的页都成立。**条件放宽时，跟着放宽的
 *  文案里那些只对旧条件成立的指代必须一起改**，否则就留下一句在新条件下
 *  为假的话——本轮清理的正是这一类。
 *
 *  这里**曾经还有一支 `/actions` 的门禁横幅**，是死代码：`/actions` 在
 *  navigation.ts 里是 `built: true`，永远走 ActionsPage 而不是本页
 *  （placeholderRoutes 只收 `!item.built` 的条目）。它与 ActionsPage 里那份
 *  逐字重复，于是 XM-0030b-ui 更新措辞时只改到了活的那一份——一个不会渲染
 *  的副本除了制造这种漂移没有别的作用，删掉。操作与审批页的门禁由
 *  ActionsPage 的 AdvancedControlsGate 负责（ADMIN-IA §七）。 */
function PlaceholderGate({ item }: { item: NavItemSpec }) {
  // 判据是 `!item.built`，**不是** `path.startsWith("/ext/")`。
  //
  // 原来那条按路径前缀判断的写法今天**恰好**为真，但不是因为它自己对——
  // 它靠的是「扩展能力各页还走不走本组件」这个**外部事实**。产品负责人
  // 2026-09-08 推翻 ADMIN-IA §5.4 之后，`/ext/app`、`/ext/integration`、
  // `/ext/publishing` 三页各自有了真实路由，根本不经过 PlaceholderPage，
  // 于是那条前缀判断在它们身上再也走不到——**问题被外部条件掩盖了，
  // 而不是被修好了**。只要有人把某页临时挂回本组件，就会重现
  // 「页面能执行了、横幅还在说不执行」这个组合，且没有任何东西会提醒他。
  //
  // 按 `built` 判断让这个条件为它自己负责：本组件只渲染 `!item.built` 的页
  // （placeholderRoutes 只收这些），所以对能走到这里的每一页它都成立，
  // 不依赖别处有没有抢先接管。
  //
  // **这个改动今天没有测试能区分新旧，如实记在这里而不是假装验证过。**
  // 现存未建成的页恰好全在 `/ext/` 下，两种条件选中的是同一批页，所以
  // router.test.tsx 那条断言在旧写法下也照样绿（变异验证只证明了「这条
  // 横幅确实被断言着」，没证明「判据换了」）。要真正钉住它，得有一个
  // **未建成且不在 `/ext/` 下**的页——那天到来时，加一条用它做样本的用例，
  // 旧写法会漏掉它、新写法不会。在那之前这条改动的价值是把一个「恰好为真」
  // 变成「为自己负责」，不是修掉一个当前可观测的 bug。
  if (!item.built) {
    return (
      <p
        role="status"
        className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted"
      >
        只读蓝图：仅预览、不保存、不发布、不执行。页面存在不代表后端已经建好。
      </p>
    );
  }
  return null;
}
