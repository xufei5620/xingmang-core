import { useEffect, useState, type ReactNode } from "react";
import {
  DataTableV2,
  DENSITY_LABELS,
  FreshnessBadge,
  FreshnessNote,
  PageHeader,
  PageState,
  ServiceStatusBadge,
  describeServiceStatus,
  formatUtcTimestamp,
  navItemByPath,
  navLabel,
  navStageHint,
  type DataTableColumn,
  type FreshnessContract,
  type FreshnessState,
  type PageStateKind,
  type ServiceStatus,
} from "@xingmang/ui-admin";
import {
  Badge,
  Button,
  FormField,
  Input,
  Select,
  Tabs,
  type BadgeTone,
  type ButtonProps,
} from "@xingmang/ui-primitives";
import { Link, useSearchParams } from "react-router";
import { BlueprintBanner, blueprintForPath } from "../blueprints";
import { environmentLabel } from "../lib/breadcrumbs";
import {
  COLOR_TOKEN_REFS,
  CONTROL_HEIGHT_VARIABLES,
  DESIGN_DEMO_ROWS,
  FONT_TOKEN_REFS,
  NAV_TOKEN_REFS,
  PENDING_COMPLEX_COMPONENTS,
  RADIUS_TOKEN_REFS,
  RADIUS_USAGE,
  SHADOW_TOKEN_REFS,
  SPACING_VARIABLES,
  TOKEN_NOTES,
  TYPE_SCALE,
  resolvedTokenValue,
  type DesignDemoRow,
  type TokenRef,
} from "../lib/designSpec";

const DESIGN_SUB_TABS = (navItemByPath("/design")?.item.subTabs ?? []).map(
  (tab) => [tab.id, tab.label] as const,
);

/** 默认子页 = 第一格。这一页六格里有五格今天就接了真组件，没有「只有一格是真的」
 *  那种需要特意指向某一格的情况（对比 OpsPage 的 OPS_DEFAULT_SUB）。 */
const DESIGN_DEFAULT_SUB = "color";

const DESIGN_BLUEPRINT_PAGE = blueprintForPath("/design");

/** 界面规范页（XM-DESIGN0）。
 *
 *  这一页在整个后台里是唯一一页**零后端依赖**的：它不读任何 Query 端点、
 *  不调用任何 Action（router.go 里也确实没有任何 design / tokens / ui-spec 路由）。
 *  屏幕上的色块、按钮、表单、徽章、表格全是仓库里真实组件的当场渲染，
 *  真身在 @xingmang/design-tokens 与 ui-primitives / ui-admin。
 *
 *  于是这一页的「诚实」是另一种形状：别的页要说清「值还没有数据源」，
 *  这一页要说清「这个组件仓库里还没有」。六格里五格接真组件，第六格
 *  「复杂组件」的四个组件一个都不存在，保持诚实占位。 */
export function DesignPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const activeSub = rawSub === null || rawSub.trim() === "" ? DESIGN_DEFAULT_SUB : rawSub;
  const known = DESIGN_SUB_TABS.some(([value]) => value === activeSub);
  const item = navItemByPath("/design")?.item;
  const stageHint = item ? navStageHint(item) : undefined;

  if (!known) {
    return (
      <section>
        <PageHeader
          title={navLabel("/design")}
          description="界面规范的子页按格逐步接入；未知地址不会静默回落到第一格。"
        />
        <PageState
          kind="unavailable"
          title={`「${rawSub}」子页尚未接入`}
          description="请从已定义的界面规范子页中选择。"
          action={
            <Link
              to={`/design?sub=${DESIGN_DEFAULT_SUB}`}
              className="text-sm font-medium text-accent hover:underline"
            >
              返回颜色与排版
            </Link>
          }
        />
      </section>
    );
  }

  const selectSub = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("sub", value);
    setSearchParams(next, { replace: true });
  };

  return (
    <section>
      <PageHeader
        title={navLabel("/design")}
        status={stageHint ? <Badge tone="warning">{stageHint}</Badge> : null}
        description="设计系统的活文档：颜色与排版、按钮与表单、卡片与状态、表格与详情、页面状态、复杂组件。与 Storybook 呼应，不是另一套规范。"
      />
      <div className="flex flex-col gap-3">
        <ScopeNote />
        <Tabs
          value={activeSub}
          onValueChange={selectSub}
          items={DESIGN_SUB_TABS.map(([value, label]) => ({
            value,
            label,
            content: <DesignTabContent tabId={value} label={label} />,
          }))}
        />
      </div>
    </section>
  );
}

/** 这一页的取数边界。
 *
 *  别的页在这个位置写的是「数据从哪来、这一屏是不是全部」。这一页要写的恰恰
 *  相反：它一条端点都不读，所以既没有服务端截断，也没有「今天真的是 0」与
 *  「链路还没接」的区分问题——屏幕上没有的，就是仓库里没有的。这句话必须写
 *  出来，否则一个空着的格子仍然会被读成「数据没回来」。 */
function ScopeNote() {
  return (
    <p
      role="status"
      className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted"
    >
      这一页不读任何后端端点，也不调用任何 Action：内容全部来自仓库内的
      @xingmang/design-tokens 与 ui-primitives / ui-admin，在你眼前当场渲染。所以它没有
      「这一屏可能不是全部」的取数上限，也没有「今天真的是 0」这种空态——这里显示不出来的
      东西，就是仓库里还没有的东西，页面会逐条说清在等谁。可交互样例在 Storybook：仓库内跑
      <code className="mx-1 font-mono">pnpm --filter ui-storybook dev</code>
      （:6006）。生产镜像只构建 admin-web，Storybook 不部署，所以这里不给外链。
    </p>
  );
}

function DesignTabContent({ tabId, label }: { tabId: string; label: string }) {
  const body =
    tabId === "color" ? (
      <ColorTypePanel />
    ) : tabId === "controls" ? (
      <ControlsPanel />
    ) : tabId === "cards" ? (
      <CardsStatesPanel />
    ) : tabId === "tables" ? (
      <TablesPanel />
    ) : tabId === "states" ? (
      <PageStatesPanel />
    ) : tabId === "components" ? (
      <ComplexComponentsPanel />
    ) : null;

  // 认得出 id 却没有面板的情况只可能是本文件与 navigation.ts 漂开了；
  // 那时显示「尚未接入」比显示一片空白诚实
  if (!body) {
    return <PageState kind="unavailable" title={`「${label}」尚未接入`} />;
  }

  return (
    <div className="flex flex-col gap-3">
      {body}
      <BlueprintSourceNote tabId={tabId} />
    </div>
  );
}

/** 每一格的落款，逐字取自 DESIGN_BLUEPRINT 的 `tabs[].source`。
 *
 *  从蓝图数据读而不是在这里再抄一遍：那份数据是与 navigation.ts 对账的那一份
 *  （blueprints.test.ts），抄第二遍就多一处会漂的措辞。 */
function BlueprintSourceNote({ tabId }: { tabId: string }) {
  const source = DESIGN_BLUEPRINT_PAGE?.tabs.find((tab) => tab.id === tabId)?.source;
  if (!source) return null;
  return <p className="text-xs text-fg-muted">{source}</p>;
}

// ---------------------------------------------------------------------------
// 一、颜色与排版
// ---------------------------------------------------------------------------

/** 要实读的颜色变量名。模块级常量，身份稳定——放进 useEffect 依赖不会每次重跑。 */
const COLOR_VARIABLES: readonly string[] = [...COLOR_TOKEN_REFS, ...NAV_TOKEN_REFS]
  .map((ref) => ref.variable)
  .filter((variable): variable is string => variable !== null);

const SCALE_VARIABLES: readonly string[] = [
  ...SPACING_VARIABLES,
  ...CONTROL_HEIGHT_VARIABLES,
  ...RADIUS_TOKEN_REFS.map((ref) => ref.variable),
  ...SHADOW_TOKEN_REFS.map((ref) => ref.variable),
  ...FONT_TOKEN_REFS.map((ref) => ref.variable),
].filter((variable): variable is string => variable !== null);

/** 读一组 CSS 变量在**某个容器上**的当前计算值。
 *
 *  容器级而不是从 documentElement 读：深色那一列包在 `<div data-theme="dark">` 里
 *  就地翻色（tokens.css 的第二条暗色守卫特意不带 `:root` 前缀就是为了这个），
 *  从根上读回来的永远是浅色那一份，两列会显示出一模一样的值。 */
function useTokenValues(
  variables: readonly string[],
): [(node: HTMLElement | null) => void, ReadonlyMap<string, string | null>] {
  const [node, setNode] = useState<HTMLElement | null>(null);
  const [values, setValues] = useState<ReadonlyMap<string, string | null>>(() => new Map());

  useEffect(() => {
    if (!node) return;
    const style = getComputedStyle(node);
    const next = new Map<string, string | null>();
    for (const variable of variables) {
      next.set(variable, resolvedTokenValue(style.getPropertyValue(variable)));
    }
    setValues(next);
  }, [node, variables]);

  return [setNode, values];
}

/** 蓝图「核心颜色」卡上的六个称呼各自指哪个令牌。
 *
 *  蓝图那张卡只列称呼（原型的说法），令牌名才是可执行的那一份。两边并排放，
 *  是为了让人拿着原型的词也能查到令牌。 */
const CORE_COLOR_ALIASES: readonly (readonly [string, string])[] = [
  ["交互主色", "color.accent"],
  ["页面底色", "color.canvas"],
  ["内容表面", "color.surface"],
  ["正常", "color.success"],
  ["提醒", "color.warning"],
  ["危险", "color.danger"],
];

function ColorTypePanel() {
  return (
    <div className="flex flex-col gap-3">
      <SpecSection
        title="核心颜色"
        hint="基础色 → 语义色 → 组件色"
        description="色值一个都不写在这一页里：色块直接引用令牌，右侧的当前值由 getComputedStyle 从渲染结果上实读。读不到就写「读不到」——那说明 tokens.css 里没有这个变量，而不是它是白色。"
      >
        <dl className="mb-3 flex flex-wrap gap-x-6 gap-y-1 text-xs">
          {CORE_COLOR_ALIASES.map(([alias, token]) => (
            <div key={alias} className="flex items-baseline gap-1">
              <dt className="text-fg-muted">{alias}</dt>
              <dd className="font-mono text-fg">{token}</dd>
            </div>
          ))}
        </dl>
        <div className="grid grid-cols-1 gap-3 xl:grid-cols-2">
          <ThemeColorColumn theme="light" />
          <ThemeColorColumn theme="dark" />
        </div>
      </SpecSection>

      <SpecSection title="字体层级" hint="字族由 --xm-font-sans / --xm-font-mono 决定">
        <FontFamilyRow />
        <ul className="mt-2 flex flex-col gap-2">
          {TYPE_SCALE.map((entry) => (
            <li
              key={entry.label}
              className="flex flex-col gap-1 border-t border-edge pt-2 first:border-t-0 first:pt-0"
            >
              <p className={entry.className}>{entry.sample}</p>
              <p className="text-xs text-fg-muted">
                {entry.label} · <code className="font-mono">{entry.className}</code> · 现用于
                {entry.usedBy}
              </p>
            </li>
          ))}
        </ul>
      </SpecSection>

      <SpecSection
        title="间距与圆角"
        hint="4px 基准网格"
        description="刻度条的宽度直接写成 var(--xm-space-N)，不换算成像素：这一页画出来的就是令牌本身。"
      >
        <ScaleRuler />
      </SpecSection>

      <SpecNote title="前端红线：禁止硬编码颜色、圆角与阴影">
        这一页是规范的说明面，Storybook 是它的可交互面，design-tokens 是它的实现。三者同源——在这里改一个色值不会生效，要改 tokens。
      </SpecNote>
    </div>
  );
}

function ThemeColorColumn({ theme }: { theme: "light" | "dark" }) {
  const [ref, values] = useTokenValues(COLOR_VARIABLES);
  return (
    <div
      // 深色一列靠容器级 data-theme 就地翻色，不动 documentElement：
      // 改全局会把整页（包括浅色那一列）一起翻掉
      {...(theme === "dark" ? { "data-theme": "dark" } : {})}
      ref={ref}
      className="rounded-lg border border-edge bg-canvas p-3"
    >
      <p className="mb-2 text-xs font-medium text-fg">
        {theme === "dark" ? "深色（容器级 data-theme=\"dark\"）" : "浅色（默认）"}
      </p>
      <ul className="flex flex-col gap-2">
        {[...COLOR_TOKEN_REFS, ...NAV_TOKEN_REFS].map((tokenRef) => (
          <TokenSwatchRow key={tokenRef.name} tokenRef={tokenRef} values={values} />
        ))}
      </ul>
    </div>
  );
}

function TokenSwatchRow({
  tokenRef,
  values,
}: {
  tokenRef: TokenRef;
  values: ReadonlyMap<string, string | null>;
}) {
  const note = TOKEN_NOTES[tokenRef.name];
  return (
    <li className="flex items-start gap-2">
      <span
        aria-hidden="true"
        // 色块的底色**就是**那个令牌，不是它的副本
        style={{ background: tokenRef.reference }}
        className="mt-0.5 size-8 shrink-0 rounded-md border border-edge-strong"
      />
      <div className="min-w-0">
        <p className="font-mono text-xs text-fg">{tokenRef.name}</p>
        <p className="font-mono text-xs text-fg-muted">{tokenRef.variable ?? tokenRef.reference}</p>
        <TokenValue tokenRef={tokenRef} values={values} />
        {note ? <p className="mt-0.5 text-xs text-fg-muted">{note}</p> : null}
      </div>
    </li>
  );
}

/** 当前计算值。三种结果各说各的话，不互相冒充。 */
function TokenValue({
  tokenRef,
  values,
}: {
  tokenRef: TokenRef;
  values: ReadonlyMap<string, string | null>;
}) {
  if (tokenRef.variable === null) {
    return (
      <p className="text-xs text-fg-muted">
        当前值 —（这条不是单一 var() 引用，页面读不出它对应哪个变量）
      </p>
    );
  }
  const value = values.get(tokenRef.variable);
  if (value === undefined) {
    return <p className="text-xs text-fg-muted">当前值 读取中…</p>;
  }
  if (value === null) {
    return (
      <p className="text-xs text-warning">
        当前值 读不到（这个变量在当前样式里没有定义，不是白色）
      </p>
    );
  }
  return <p className="font-mono text-xs text-fg tabular-nums">当前值 {value}</p>;
}

function FontFamilyRow() {
  const [ref, values] = useTokenValues(SCALE_VARIABLES);
  return (
    <ul ref={ref} className="flex flex-col gap-1">
      {FONT_TOKEN_REFS.map((tokenRef) => (
        <li key={tokenRef.name} className="text-xs">
          <span className="font-mono text-fg">{tokenRef.name}</span>
          <span className="mx-1 font-mono text-fg-muted">
            {tokenRef.variable ?? tokenRef.reference}
          </span>
          <TokenValue tokenRef={tokenRef} values={values} />
        </li>
      ))}
    </ul>
  );
}

function ScaleRuler() {
  const [ref, values] = useTokenValues(SCALE_VARIABLES);
  return (
    <div ref={ref} className="flex flex-col gap-3">
      <div>
        <p className="text-xs font-medium text-fg">间距刻度</p>
        <ul className="mt-1 flex flex-col gap-1">
          {SPACING_VARIABLES.map((variable) => (
            <li key={variable} className="flex items-center gap-2 text-xs">
              <span
                aria-hidden="true"
                style={{ width: `var(${variable})` }}
                className="h-3 shrink-0 bg-accent"
              />
              <span className="font-mono text-fg-muted">{variable}</span>
              <ScaleValue variable={variable} values={values} />
            </li>
          ))}
        </ul>
      </div>

      <div>
        <p className="text-xs font-medium text-fg">控件高度</p>
        <ul className="mt-1 flex flex-col gap-1">
          {CONTROL_HEIGHT_VARIABLES.map((variable) => (
            <li key={variable} className="flex items-center gap-2 text-xs">
              <span className="font-mono text-fg-muted">{variable}</span>
              <ScaleValue variable={variable} values={values} />
            </li>
          ))}
        </ul>
        <p className="mt-1 text-xs text-fg-muted">
          Button / Input / Select 直接引用这三档（如 Button 的
          <code className="mx-1 font-mono">h-(--xm-control-h-md)</code>），所以「按钮多高」不是各页各写的。
        </p>
      </div>

      <div>
        <p className="text-xs font-medium text-fg">圆角四档</p>
        <ul className="mt-1 flex flex-col gap-1">
          {RADIUS_USAGE.map((usage) => {
            const tokenRef = RADIUS_TOKEN_REFS.find((entry) => entry.name === `radius.${usage.token}`);
            return (
              <li key={usage.token} className="flex items-center gap-2 text-xs">
                <span
                  aria-hidden="true"
                  className={`size-6 shrink-0 border border-edge-strong bg-surface-muted ${usage.className}`}
                />
                <span className="font-mono text-fg">radius.{usage.token}</span>
                <span className="font-mono text-fg-muted">{tokenRef?.variable ?? "—"}</span>
                <span className="text-fg-muted">{usage.usedBy}</span>
                {tokenRef?.variable ? (
                  <ScaleValue variable={tokenRef.variable} values={values} />
                ) : null}
              </li>
            );
          })}
        </ul>
        <p className="mt-1 text-xs text-fg-muted">
          蓝图这张卡还列了一档「行内详情圆角」：实现里行内详情是表内的一整行（DataTableV2
          的展开行，bg-surface-muted，没有自己的圆角），圆角由表格外框的 lg 承担——这一档在实现里没有独立取值。
        </p>
      </div>

      <div>
        <p className="text-xs font-medium text-fg">阴影两档</p>
        <ul className="mt-1 flex flex-wrap gap-3">
          <li className="rounded-lg border border-edge bg-surface px-3 py-2 text-xs shadow-sm">
            <span className="font-mono">shadow.sm</span> · 卡片常态
          </li>
          <li className="rounded-lg border border-edge bg-surface px-3 py-2 text-xs shadow-md">
            <span className="font-mono">shadow.md</span> · 浮层（Select 下拉、Dialog）
          </li>
        </ul>
      </div>
    </div>
  );
}

function ScaleValue({
  variable,
  values,
}: {
  variable: string;
  values: ReadonlyMap<string, string | null>;
}) {
  const value = values.get(variable);
  if (value === undefined) return <span className="text-fg-muted">读取中…</span>;
  if (value === null) return <span className="text-warning">读不到</span>;
  return <span className="font-mono text-fg tabular-nums">{value}</span>;
}

// ---------------------------------------------------------------------------
// 二、按钮与表单
// ---------------------------------------------------------------------------

type ButtonVariant = NonNullable<ButtonProps["variant"]>;
type ButtonSize = NonNullable<ButtonProps["size"]>;

/** 写成 Record<联合类型, string> 而不是数组：Button 将来多一种 variant，
 *  这里会**编译不过**（缺键），而不是安静地少展示一档。下面几个状态表同理。 */
const BUTTON_VARIANT_NOTES: Record<ButtonVariant, string> = {
  primary: "一屏一个：当前上下文里最想让人点的那一个。",
  secondary: "描边 + 浅底。并列的次要动作走它。",
  ghost: "无底无边，只在悬停时给一层浅底。用于工具条这类密集排布。",
  danger: "不可逆或有破坏性的动作。危险不靠文案提醒，靠这一档颜色。",
};

const BUTTON_SIZE_NOTES: Record<ButtonSize, string> = {
  sm: "工具条、表内按钮",
  md: "默认",
  lg: "独立表单的提交",
};

function ControlsPanel() {
  return (
    <div className="flex flex-col gap-3">
      <SpecSection
        title="按钮"
        hint="四种 variant × 三种 size，全部是真的 <Button>"
        description="这些按钮是可以点的，但它们不触发任何写操作——这一页不调用任何 Action。"
      >
        <div className="flex flex-col gap-3">
          {(Object.entries(BUTTON_VARIANT_NOTES) as [ButtonVariant, string][]).map(
            ([variant, note]) => (
              <div key={variant} className="flex flex-col gap-1 border-t border-edge pt-2 first:border-t-0 first:pt-0">
                <div className="flex flex-wrap items-center gap-2">
                  {(Object.keys(BUTTON_SIZE_NOTES) as ButtonSize[]).map((size) => (
                    <Button key={size} variant={variant} size={size}>
                      {variant} / {size}
                    </Button>
                  ))}
                  <Button variant={variant} disabled>
                    禁用
                  </Button>
                  <Button variant={variant} loading>
                    加载中
                  </Button>
                </div>
                <p className="text-xs text-fg-muted">
                  <code className="font-mono">{variant}</code>：{note}
                </p>
              </div>
            ),
          )}
        </div>
        <dl className="mt-3 flex flex-col gap-1 text-xs">
          {(Object.entries(BUTTON_SIZE_NOTES) as [ButtonSize, string][]).map(([size, note]) => (
            <div key={size} className="flex items-baseline gap-2">
              <dt className="font-mono text-fg">size={size}</dt>
              <dd className="text-fg-muted">{note}</dd>
            </div>
          ))}
        </dl>
        <p className="mt-2 text-xs text-fg-muted">
          蓝图这张卡用的是原型的四个称呼（主要 / 次要 / 边框 / 危险），按顺序对应实现的 primary /
          secondary / ghost / danger。注意「边框按钮」这一档对不上：实现里带描边的是
          secondary，ghost 是无底无边的那一档。两处措辞怎么统一需产品负责人一句话——改蓝图文案要先改
          docs/architecture/ADMIN-IA.md，所以这一页不擅自改写称呼。
        </p>
        <p className="mt-1 text-xs text-fg-muted">
          「暂未开放」= <code className="font-mono">disabled</code>，「加载中」=
          <code className="mx-1 font-mono">loading</code>（自动带上 disabled 与 aria-busy，防重复提交）。
        </p>
      </SpecSection>

      <SpecSection title="表单" hint="标签、说明和校验不可缺失">
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          <FormField label="默认输入" htmlFor="design-demo-default" hint="说明写在这里，不写进占位符。">
            <Input id="design-demo-default" placeholder="示例占位文本" />
          </FormField>
          {/* Select 那一格不给 htmlFor：Select 的 props 里没有 id，FormField 克隆时补上的
              id 落不到触发器上，label 会指向一个不存在的元素。改用 aria-label 关联，
              与 UpstreamAccountDialog 等既有表单一致 */}
          <FormField label="选择状态" hint="Select 是真的，可以打开。">
            <Select
              aria-label="选择状态"
              options={[
                { value: "a", label: "示例选项 A" },
                { value: "b", label: "示例选项 B" },
                { value: "c", label: "示例选项 C（禁用）", disabled: true },
              ]}
              defaultValue="a"
            />
          </FormField>
          <FormField
            label="错误状态"
            htmlFor="design-demo-invalid"
            required
            error="示例校验信息：错在哪、怎么改，一句话说完。"
          >
            <Input id="design-demo-invalid" invalid defaultValue="不合法的示例取值" />
          </FormField>
          <FormField label="不可编辑" htmlFor="design-demo-readonly" hint="只读与禁用不是一回事：只读仍可选中复制。">
            <Input id="design-demo-readonly" readOnly defaultValue="只读示例取值" />
          </FormField>
        </div>
        <p className="mt-2 text-xs text-fg-muted">
          校验信息挂在 FormField 的 error 槽（role="alert" + aria-describedby），不是画一圈红边了事——只
          靠颜色表达的错误，色觉障碍的人接不住。
        </p>
      </SpecSection>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 三、卡片与状态
// ---------------------------------------------------------------------------

const BADGE_TONE_NOTES: Record<BadgeTone, string> = {
  neutral: "陈述事实，不带判断（已下线、未配置）。",
  info: "与交互主色同源的提示（当前环境、当前视图）。",
  success: "达成或健康（数据新鲜、运行中）。",
  warning: "需要留意但还没坏（数据延迟、数据不完整、未建）。",
  danger: "已经坏了或不可逆（同步失败、危险动作）。",
};

const FRESHNESS_STATE_NOTES: Record<FreshnessState, string> = {
  uninitialized: "从未成功采集过。用 neutral 而不是告警色：这通常是「还没接上」，不是故障。",
  failed: "最近一次同步失败，下方数值是上一次成功的结果。",
  stale: "已超过该指标的新鲜度阈值。蓝图里那句「数据已过期」落在这里。",
  partial: "采集成功但数据不全，数值可能偏小。这是宪法 12 条在 UI 上最要紧的一档。",
  fresh: "在新鲜度阈值内且数据完整。",
};

const SERVICE_STATUS_NOTES: Record<ServiceStatus, string> = {
  active: "服务正常提供能力。",
  degraded: "服务可用但能力受限。",
  retired: "服务已退役，仅保留记录。",
};

/** 演示用的新鲜度契约。
 *
 *  时间取明显合成的整点值、错误码带 DEMO 前缀：这一格要展示的是五个状态**长什么样**，
 *  而任何看起来像真实观测的时间都会在截图里被读成真数据。 */
function demoFreshness(
  state: string,
  overrides: Partial<FreshnessContract> = {},
): FreshnessContract {
  return {
    state,
    staleness_seconds: 0,
    threshold_seconds: 300,
    is_partial: false,
    observed_at: "2026-01-01T00:00:00Z",
    last_success: "2026-01-01T00:00:00Z",
    last_error_code: "",
    ...overrides,
  };
}

const FRESHNESS_DEMOS: Readonly<Record<FreshnessState, FreshnessContract>> = {
  uninitialized: demoFreshness("uninitialized", {
    observed_at: null,
    last_success: null,
    staleness_seconds: null,
  }),
  failed: demoFreshness("failed", { last_error_code: "DEMO_UPSTREAM_TIMEOUT" }),
  stale: demoFreshness("stale", { staleness_seconds: 3600 }),
  partial: demoFreshness("partial", { is_partial: true }),
  fresh: demoFreshness("fresh"),
};

function CardsStatesPanel() {
  return (
    <div className="flex flex-col gap-3">
      <SpecSection
        title="标准卡片与可点击卡片"
        hint="无点击行为时不显示箭头"
        description="两张卡的边框、圆角、阴影完全相同；区别只在「点了会不会走」。"
      >
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          <article className="rounded-lg border border-edge bg-surface p-4 shadow-sm">
            <h4 className="text-sm font-medium text-fg">标准卡片</h4>
            <p className="mt-1 text-xs text-fg-muted">
              用途：把一组属于同一件事的内容框起来。没有点击行为，所以不带箭头——一个点不动的箭头会让人反复去点。
            </p>
          </article>
          <Link
            to="/design?sub=tables"
            className="flex items-start justify-between gap-2 rounded-lg border border-edge bg-surface p-4 shadow-sm hover:border-accent"
          >
            <span className="min-w-0">
              <span className="block text-sm font-medium text-fg">可点击卡片</span>
              <span className="mt-1 block text-xs text-fg-muted">
                用途：核心对象进入完整详情页面。这张是演示，点它会跳到本页的「表格与详情」格。
              </span>
            </span>
            <span aria-hidden="true" className="shrink-0 text-fg-muted">
              →
            </span>
          </Link>
        </div>
      </SpecSection>

      <SpecSection
        title="数据新鲜度标签"
        hint="任何数字都要能回答「什么时候的」"
        description="下面五档是 ui-admin/freshness.ts 里真实定义的全部状态，加上一条未知状态的兜底。徽章上的时间与错误码是合成的示例值，不是平台数据。"
      >
        <ul className="flex flex-col gap-2">
          {(Object.entries(FRESHNESS_STATE_NOTES) as [FreshnessState, string][]).map(
            ([state, note]) => (
              <li key={state} className="flex flex-wrap items-baseline gap-2 text-xs">
                <FreshnessBadge freshness={FRESHNESS_DEMOS[state]} />
                <code className="font-mono text-fg-muted">{state}</code>
                <span className="text-fg-muted">{note}</span>
              </li>
            ),
          )}
          <li className="flex flex-wrap items-baseline gap-2 border-t border-edge pt-2 text-xs">
            <FreshnessBadge freshness={demoFreshness("demo_unknown_state")} />
            <code className="font-mono text-fg-muted">未知取值</code>
            <span className="text-fg-muted">
              后端将来新增状态时，前端宁可显示「未知状态」让人来查，也不静默降级成看起来正常的样子。
            </span>
          </li>
        </ul>
      </SpecSection>

      <SpecSection title="运行环境标签" hint="development / staging / production">
        <ul className="flex flex-col gap-2">
          {["development", "staging", "production"].map((environment) => {
            const env = environmentLabel(environment);
            return (
              <li key={environment} className="flex flex-wrap items-baseline gap-2 text-xs">
                <Badge tone="info" title={env.hint}>
                  {env.label}
                </Badge>
                <span className="text-fg-muted">{env.hint}</span>
              </li>
            );
          })}
          <li className="flex flex-wrap items-baseline gap-2 border-t border-edge pt-2 text-xs">
            <Badge tone="info" title={environmentLabel(undefined).hint}>
              {environmentLabel(undefined).label}
            </Badge>
            <span className="text-fg-muted">{environmentLabel(undefined).hint}</span>
          </li>
        </ul>
        <p className="mt-2 text-xs text-fg-muted">
          文案由 lib/breadcrumbs.ts 的 environmentLabel 产出，常驻位置是顶部的 ContextStrip
          （每一页同一个位置），不是各页自己画一个。三档取值来自后端的 registry.Environment。
        </p>
      </SpecSection>

      <SpecSection title="服务状态标签" hint="ServiceStatusBadge 的三态">
        <ul className="flex flex-col gap-2">
          {(Object.entries(SERVICE_STATUS_NOTES) as [ServiceStatus, string][]).map(
            ([status, note]) => (
              <li key={status} className="flex flex-wrap items-baseline gap-2 text-xs">
                <ServiceStatusBadge status={status} />
                <code className="font-mono text-fg-muted">{status}</code>
                <span className="text-fg-muted">{note}</span>
                <span className="text-fg-muted">
                  （{describeServiceStatus(status).hint}）
                </span>
              </li>
            ),
          )}
        </ul>
      </SpecSection>

      <SpecSection title="徽章语气" hint="按语义取名，不按颜色取名">
        <ul className="flex flex-col gap-2">
          {(Object.entries(BADGE_TONE_NOTES) as [BadgeTone, string][]).map(([tone, note]) => (
            <li key={tone} className="flex flex-wrap items-baseline gap-2 text-xs">
              <Badge tone={tone}>{tone}</Badge>
              <span className="text-fg-muted">{note}</span>
            </li>
          ))}
        </ul>
        <p className="mt-2 text-xs text-fg-muted">
          业务代码不该知道 danger 是红的：换主题时只改 tokens.css，不动调用点。warning
          一档的文字刻意用 text-fg 而不是 text-warning——琥珀是浅底上最难读的一档，语气交给底色与描边。
        </p>
      </SpecSection>

      <SpecSection
        title="风险等级标签"
        hint="待产品负责人一句话，这一格不擅自二选一"
        description="蓝图这张卡沿用原型的三档（低风险 / 中风险 / 高风险），而平台真实的风险分级是 ADR-003 的 L0–L4 五档，且 L2 及以上当前会被内核以 ADVANCED_CONTROLS_REQUIRED 拒执行。三档与五档不是同一套分级，映射关系没有定过。"
      >
        <PageState
          kind="unavailable"
          title="风险等级标签的最终措辞未定"
          description="要么改成 L0–L4 并注明 L2 及以上当前不可执行，要么明确保留原型三档并说明它与 L0–L4 的映射。改蓝图文案要先改 docs/architecture/ADMIN-IA.md，所以这一页不先画一套。"
          action={
            <Link to="/actions?sub=risk" className="text-sm font-medium text-accent hover:underline">
              去看真实的 L0–L4 分级表
            </Link>
          }
          compact
        />
      </SpecSection>

      <SpecSection title="空状态与骨架" hint="空不等于坏">
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          <PageState
            kind="empty"
            title="还没有示例记录"
            description="empty 必须自己传标题：「暂无数据」什么也没说明，人要知道空的是什么。"
            compact
          />
          <div className="rounded-lg border border-edge bg-surface p-4">
            <p className="text-xs font-medium text-fg">骨架</p>
            <div className="mt-2 flex flex-col gap-2" aria-hidden="true">
              <div className="h-4 w-1/2 animate-pulse rounded-sm bg-surface-muted" />
              <div className="h-8 w-2/3 animate-pulse rounded-sm bg-surface-muted" />
            </div>
            <p className="mt-2 text-xs text-fg-muted">
              仓库里没有独立的骨架组件：这块形状来自 ui-admin 的 MetricCard 故事，页面级加载态由
              PageState 的 loading 承担（转圈 + 「加载中…」）。骨架用在「已知会回来、且要占住高度不让页面跳」的地方。
            </p>
          </div>
        </div>
      </SpecSection>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 四、表格与详情
// ---------------------------------------------------------------------------

const DEMO_COLUMNS: readonly DataTableColumn<DesignDemoRow>[] = [
  {
    id: "name",
    header: "示例行",
    primary: true,
    value: (row) => row.name,
    cell: (row) => <span className="font-medium">{row.name}</span>,
  },
  {
    id: "status",
    header: "状态",
    value: (row) => describeServiceStatus(row.status).label,
    cell: (row) => <ServiceStatusBadge status={row.status} />,
  },
  {
    id: "detailKind",
    header: "行内详情类型",
    value: (row) => (row.detailKind === "evidence" ? "证据行内详情" : "普通行内详情"),
    cell: (row) => (
      <span>{row.detailKind === "evidence" ? "证据行内详情" : "普通行内详情"}</span>
    ),
  },
  {
    id: "observedAt",
    header: "观测时间",
    value: (row) => row.observedAt,
    cell: (row) => <span className="tabular-nums">{formatUtcTimestamp(row.observedAt)}</span>,
  },
  {
    id: "source",
    header: "来源",
    value: (row) => row.source,
    cell: (row) => <span className="font-mono text-xs">{row.source}</span>,
  },
];

function TablesPanel() {
  return (
    <div className="flex flex-col gap-3">
      <BlueprintBanner text="本表是规范演示：三条「示例行」是合成的，不是平台数据。这是全站唯一允许出现演示行的地方——因为这一格要说明的正是表格交互本身，而一张空表说明不了任何交互。" />

      <SpecSection
        title="数据表格"
        hint="固定表头 · 数值右对齐"
        description="下面是真的 DataTableV2：排序、表内搜索、状态筛选、列显隐、密度三档、分页、行展开都真的点得动。"
      >
        <DataTableV2
          caption="界面规范演示表：示例行、状态、行内详情类型、观测时间与来源（合成数据）"
          columns={DEMO_COLUMNS}
          rows={DESIGN_DEMO_ROWS}
          rowKey={(row) => row.id}
          searchable
          filters={[
            {
              columnId: "status",
              label: "状态",
              options: (["active", "degraded", "retired"] as ServiceStatus[]).map(
                (status) => describeServiceStatus(status).label,
              ),
            },
          ]}
          // 每页两条、共三条：分页控件因此是活的，而不是一个永远点不动的摆设
          pageSize={2}
          renderExpanded={(row) => <DemoRowDetail row={row} />}
          emptyState={<PageState kind="empty" title="演示行被移除了" compact />}
        />
        <dl className="mt-2 flex flex-col gap-1 text-xs">
          <div className="flex items-baseline gap-2">
            <dt className="text-fg-muted">筛选条</dt>
            <dd className="text-fg">按「状态」列筛；当前条件写在工具条上，不用人自己记。</dd>
          </div>
          <div className="flex items-baseline gap-2">
            <dt className="text-fg-muted">列显隐</dt>
            <dd className="text-fg">主标识列不可隐藏——把「是哪一行」藏掉，剩下的表没法读。</dd>
          </div>
          <div className="flex items-baseline gap-2">
            <dt className="text-fg-muted">密度</dt>
            <dd className="text-fg">
              {Object.values(DENSITY_LABELS).join(" / ")}，默认紧凑（管理后台默认密集）。
            </dd>
          </div>
          <div className="flex items-baseline gap-2">
            <dt className="text-fg-muted">分页</dt>
            <dd className="text-fg">客户端分页；数据源自己在游标翻页时不叠这一层。</dd>
          </div>
          <div className="flex items-baseline gap-2">
            <dt className="text-fg-muted">横向滚动</dt>
            <dd className="text-fg">只发生在表格容器内部，页面 body 永远不横滚。</dd>
          </div>
        </dl>
      </SpecSection>

      <SpecSection
        title="行内详情"
        hint="轻量信息行内展开，重对象进完整详情页"
        description="展开上面任意一行都能看到真的展开区。普通详情放对象属性、关联关系与常用入口；证据详情必须带观测时间、来源、哈希与审计编号——少一样就没法复核。"
      >
        <p className="text-xs text-fg-muted">
          怎么选：能在两三行内说完、且人看完就回到列表的，走行内展开；需要单独一屏、要能贴给同事的，
          走完整详情页（那时地址栏里得有它自己的地址）。
        </p>
      </SpecSection>
    </div>
  );
}

function DemoRowDetail({ row }: { row: DesignDemoRow }) {
  if (row.detailKind === "evidence") {
    return (
      <dl className="grid grid-cols-1 gap-x-6 gap-y-1 text-xs sm:grid-cols-2">
        <DetailItem label="观测时间" value={formatUtcTimestamp(row.observedAt)} />
        <DetailItem label="来源" value={row.source} mono />
        <DetailItem label="哈希" value={row.hash} mono />
        <DetailItem label="审计编号" value={row.auditId} mono />
        <div className="text-fg-muted sm:col-span-2">
          这四个字段是证据详情的必含项，少一样就没法复核。这里的值全是合成的：哈希是全 0，编号带 demo 前缀。
        </div>
      </dl>
    );
  }
  return (
    <dl className="grid grid-cols-1 gap-x-6 gap-y-1 text-xs sm:grid-cols-2">
      <DetailItem label="对象属性" value={`${row.name}（示例对象，非平台数据）`} />
      <DetailItem label="关联关系" value="示例：属于「界面规范」这一页的演示集合" />
      <DetailItem label="常用入口" value="真实场景下这里放一两个跳转，演示行不给可点的入口" />
      <DetailItem label="观测时间" value={formatUtcTimestamp(row.observedAt)} />
    </dl>
  );
}

function DetailItem({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-baseline gap-2">
      <dt className="shrink-0 text-fg-muted">{label}</dt>
      <dd className={mono ? "font-mono break-all text-fg" : "text-fg"}>{value}</dd>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 五、页面状态
// ---------------------------------------------------------------------------

const PAGE_STATE_NOTES: Record<PageStateKind, string> = {
  loading: "这次请求还在路上。下一步是等。",
  empty: "读到了，里面没有。下一步是去造一条数据。",
  error: "这次请求失败了。下一步是重试或报障。",
  denied: "你没有这个权限。下一步是去要权限，重试一万次都一样。",
  unavailable: "这块我们还没建。下一步是等排期，而不是「没有数据」。",
};

function PageStatesPanel() {
  return (
    <div className="flex flex-col gap-3">
      <SpecSection
        title="五种组件态"
        hint="ui-admin/PageState 实现的全部 kind"
        description="同一个页面位置，不同的诚实说法。把 unavailable 说成 empty，就是让人以为「这个平台今天没有告警」，而事实是我们没接。"
      >
        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          <PageState kind="loading" compact />
          <PageState
            kind="empty"
            title="还没有示例记录"
            description="空的是什么，只有调用方说得清，所以 empty 没有通用默认标题。"
            compact
          />
          <PageState
            kind="error"
            message="DEMO_EXAMPLE_ERROR：这是示例错误正文，不是真实故障。"
            description="范围：界面规范演示。"
            onRetry={() => {}}
            footnote="request_id demo-0000-0000"
            compact
          />
          <PageState
            kind="denied"
            permission="示例scope.read"
            description="服务端为最终裁决，前端隐藏不构成安全控制。"
            compact
          />
          <PageState
            kind="unavailable"
            title="「示例功能」尚未接入"
            description="说清在等谁：等采集、等后端，还是等产品拍板。"
            compact
          />
        </div>
        <dl className="mt-3 flex flex-col gap-1 text-xs">
          {(Object.entries(PAGE_STATE_NOTES) as [PageStateKind, string][]).map(([kind, note]) => (
            <div key={kind} className="flex items-baseline gap-2">
              <dt className="shrink-0 font-mono text-fg">{kind}</dt>
              <dd className="text-fg-muted">{note}</dd>
            </div>
          ))}
        </dl>
        <p className="mt-2 text-xs text-fg-muted">
          上面 error 那一格的「重试」是样例形状，点它不会发起任何请求——这一页一条端点都不读。
          真实场景下只有重试有意义时才传 onRetry：对 400 这类错误给一个重试按钮，是在请人做一件必然失败的事。
        </p>
      </SpecSection>

      <SpecSection
        title="另外三种状态落在哪儿"
        hint="蓝图列了八种，PageState 只实现五种——剩下三种在平台里不是 PageState 的 kind"
        description="这一格按「五个组件态 + 三个新鲜度契约态」如实写。要不要把八态都做成 PageState 的 kind 需产品负责人一句话——那是改公共组件，影响面比这一页大得多。"
      >
        <ul className="flex flex-col gap-3">
          <li className="flex flex-col gap-1 text-xs">
            <p className="font-medium text-fg">No Results 没有匹配的数据</p>
            <p className="text-fg-muted">
              = empty + 查询条件描述。实现在 DataTableV2 内部：筛完为空时显示「没有匹配的数据」「调整搜索或筛选条件后重试。」并给一个「清除筛选」按钮，当前条件由 describeCriteria
              写在工具条上。它与「这张表本来就空」是两件事，所以不共用 emptyState。
            </p>
          </li>
          <li className="flex flex-col gap-1 text-xs">
            <p className="font-medium text-fg">数据已过期</p>
            <div className="flex flex-wrap items-center gap-2">
              <FreshnessBadge freshness={FRESHNESS_DEMOS.stale} />
              <span className="text-fg-muted">= FreshnessBadge 的 stale，不是一种页面态。</span>
            </div>
            <FreshnessNote freshness={FRESHNESS_DEMOS.stale} />
          </li>
          <li className="flex flex-col gap-1 text-xs">
            <p className="font-medium text-fg">部分数据</p>
            <div className="flex flex-wrap items-center gap-2">
              <FreshnessBadge freshness={FRESHNESS_DEMOS.partial} />
              <span className="text-fg-muted">
                = FreshnessBadge 的 partial；<code className="font-mono">is_partial</code>{" "}
                为真而状态不是 partial 时，FreshnessNote 会在数据时间后面补一句「（数据不完整）」。
              </span>
            </div>
            <FreshnessNote
              freshness={demoFreshness("fresh", { is_partial: true, staleness_seconds: 30 })}
            />
          </li>
        </ul>
      </SpecSection>

      <SpecSection
        title="每种状态必须携带的证据"
        hint="「出错了」不是状态，是感想"
        description="下面每一项都指得出它在代码里的落点，不是一句原则。"
      >
        <dl className="flex flex-col gap-1 text-xs">
          <DetailItem label="来源" value="PageState 的 description / footnote；指标卡走 MetricCard 的 source" />
          <DetailItem label="范围 / 查询条件" value="DataTableV2 工具条上的 describeCriteria（视图 · 当前条件）" />
          <DetailItem label="观测时间" value="FreshnessContract.observed_at，一律 UTC 且写明时区后缀" />
          <DetailItem label="阈值" value="FreshnessContract.threshold_seconds，精确秒数在徽章的悬停里" />
          <DetailItem label="缺失的是哪一部分" value="FreshnessContract.is_partial + FreshnessNote 的「（数据不完整）」" />
        </dl>
      </SpecSection>

      <SpecNote title="「部分数据」与「空」不是一回事">
        可用来源已展示、缺失来源不会被静默忽略——这是宪法 12 条在页面状态上的落点。把部分数据显示成完整数据，比显示成空更糟。
      </SpecNote>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 六、复杂组件
// ---------------------------------------------------------------------------

function ComplexComponentsPanel() {
  return (
    <div className="flex flex-col gap-3">
      <PageState
        kind="unavailable"
        title="这四个组件仓库里一个都还没有"
        description="所以这一格不摆样例，也不预先造壳：等真功能来的时候壳的形状多半是错的，而错壳比没有壳更难拆。"
      />
      <ul className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        {PENDING_COMPLEX_COMPONENTS.map((entry) => (
          <li key={entry.title} className="rounded-lg border border-edge bg-surface p-4">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <h4 className="text-sm font-medium text-fg">{entry.title}</h4>
              <Badge tone="warning">尚未实现</Badge>
            </div>
            <p className="mt-1 text-xs text-fg-muted">{entry.waitingFor}</p>
          </li>
        ))}
      </ul>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 共用外壳
// ---------------------------------------------------------------------------

/** 规范页里的一节。结构与蓝图页的键值卡一致（标题 + 右上角提示 + 正文），
 *  这样同一页里「已接真组件的格」与「还是蓝图的格」看起来是同一套东西。 */
function SpecSection({
  title,
  hint,
  description,
  children,
}: {
  title: string;
  hint?: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <section className="rounded-lg border border-edge bg-surface p-4">
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-medium text-fg">{title}</h3>
        {hint ? <p className="text-xs text-fg-muted">{hint}</p> : null}
      </header>
      {description ? <p className="mt-1 mb-3 text-xs text-fg-muted">{description}</p> : null}
      {children}
    </section>
  );
}

function SpecNote({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="rounded-lg border border-edge bg-surface p-4">
      <h3 className="text-sm font-medium text-fg">{title}</h3>
      <p className="mt-1 text-xs text-fg-muted">{children}</p>
    </section>
  );
}
