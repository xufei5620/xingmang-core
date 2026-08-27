import { navItemByPath, navStageHint, PageHeader, type NavItemSpec } from "@xingmang/ui-admin";
import { Badge, EmptyState, Tabs } from "@xingmang/ui-primitives";
import { useLocation, useSearchParams } from "react-router";
import {
  BlueprintBanner,
  BlueprintTabView,
  BlueprintTiles,
  blueprintForPath,
  type BlueprintPage,
} from "../blueprints";
import { NotFoundView } from "./NotFoundPage";

/** 每一页「将来放什么、归谁做」的一句话。
 *
 *  逐页写死而不是拿一句通用文案套所有页：一屏「敬请期待」等于什么都没说，
 *  运营看不出这块是排期在等、还是需要他去催谁。ADMIN-IA §五 对每一页都写了
 *  差距要点，这里是它面向使用者的一句话版本。 */
const PLACEHOLDER_COPY: Readonly<Record<string, string>> = {
  "/actions": "Action 目录、待审批队列、执行记录与风险条件。写操作一律走 Action(宪法 2、3 条)，审批链随 Foundation-B（XM-0030）上线。",
  "/jobs": "River 队列的运行中任务、定时任务、同步批次与失败重试。每一屏都要带 Watermark 与数据新鲜度。",
  "/identity": "账号与身份、权限规则、权限范围、密钥引用与会话。现在这两节还在「设置」页里，随本页上线迁出。密钥引用永不显示明文（只显示 CredentialRef）。",
  "/finance": "跨平台的财务总览、支付通道、对账、异常与冻结、开票集成与财务配置。各平台的「支付与财务」页只做本平台聚合，这一页是 ADR-006「统一体验」的落点，两者并存不冲突。",
  "/ops": "控制平面健康、稳定性与外部监控、备份与恢复、故障处理手册、迁移与数据对比，以及跨平台的模型质量保障。",
  "/changes": "变更单、发布与回滚、自动测试与质量、发布包与安全检查、数据库变更。随 Foundation-B 上线。",
  "/design": "设计系统的活文档：颜色与排版、按钮与表单、卡片与状态、表格与详情、页面状态、复杂组件。与 Storybook 呼应，不是另一套规范。",
  "/ext/app": "应用目录、页面配置、页面组件与版本发布的只读蓝图。",
  "/ext/integration": "API 调用方、Webhook、自动化流程与运行记录的只读蓝图。",
  "/ext/publishing": "内容日历、草稿与素材、审批队列、渠道与账号、发布记录的只读蓝图。",
  "/ext/ai": "模型线路、AI 角色、AI 工具与运行预算的只读蓝图。",
};

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
        description={blueprint?.description ?? PLACEHOLDER_COPY[item.path]}
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
              content: subTabContent(blueprint, tab.id, tab.label, item.stage),
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

/** 一个子页签渲染什么：有蓝图就渲染蓝图，没有就还是那句诚实的「尚未实现」。
 *
 *  按 **id** 匹配而不是按下标：navigation.ts 与蓝图规格是两份数据，
 *  按下标对齐的话，其中一边插一格就会让后面全部错位——而错位之后每一格
 *  看起来都仍然正常，只是内容对不上标题。`blueprints.test.ts` 另有一条
 *  断言两边的 id 集合逐一相等，这里是运行时的第二道。 */
function subTabContent(
  blueprint: BlueprintPage | undefined,
  tabId: string,
  tabLabel: string,
  stage: string,
) {
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
 *  两条红线要求在页面上**看得见**，不能只写在文档里：
 *  - ADMIN-IA §七：F-B 未完成前，操作与审批页必须显示门禁，不可伪造执行；
 *  - 实施计划 §2.5：扩展能力四页是只读蓝图，明确标注仅预览、不保存、不发布、不执行。 */
function PlaceholderGate({ item }: { item: NavItemSpec }) {
  if (item.path === "/actions") {
    return (
      <p
        role="status"
        className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
      >
        门禁：审批链（Foundation-B）尚未上线，本页不提供任何执行入口。此处永远不会出现
        「假装执行成功」的按钮——写操作只走 Action，且 L2 及以上需要人工审批。
      </p>
    );
  }
  if (item.path.startsWith("/ext/")) {
    return (
      <p
        role="status"
        className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted"
      >
        只读蓝图：仅预览、不保存、不发布、不执行。这一段不会因为页面存在就提前建后端。
      </p>
    );
  }
  return null;
}
