/** 蓝图页的规格类型（UI 第 6 片）。
 *
 *  ── 什么是「蓝图页」 ──────────────────────────────────────────────────────
 *  原型把服务器平台、治理段与扩展能力各页都画完了，但它们的数据源要到 M1~M3
 *  才接得上。这些页现在要建，因为**结构本身就是产出**：页签叫什么、表有哪几列、
 *  卡片放什么口径——这些是设计定稿，后续切片照它实现，改动要走文档。
 *
 *  ── 结构照抄，数字一个不抄 ────────────────────────────────────────────────
 *  原型里每张卡都有数字（`¥5,130.00`、`6 台`），它们是**样例**。原型靠顶部一条
 *  「样例数据」横幅说明这件事，但横幅救不了截图——人截的是卡片，不是横幅。
 *
 *  所以本片的取舍是：**逐字抄结构与文案，一个样例数字都不抄**。
 *    - 统计格用 `StatTile unavailable`：标签与口径说明照抄，主数位是「—」；
 *    - 表格用 `DataTableV2 rows={[]}`：列头逐字照抄，表体是「未接入」；
 *    - 每一格都必须说清**将来由谁填、什么阶段**（`source` 字段，必填）。
 *
 *  这样这些页在任何时候被截图、被演示，都不会被误读成「已经有数据了」
 *  （宪法 12 条：禁止裸数字冒充实时完整数据）。
 *
 *  ── 为什么是数据驱动而不是十一个手写页面 ──────────────────────────────────
 *  这一片有 11 个页面、约 60 个页签。手写会得到 60 段几乎相同的 JSX，
 *  而「与原型是否一致」将无法被审阅——评审得逐屏比对。写成规格之后，
 *  列头与文案集中在三个数据文件里，能与原型逐条对账，也能被测试断言。 */

/** 顶部统计格。
 *
 *  `label` 逐字抄原型；`note` 是这一格的**口径说明**，不是样例数值——
 *  原型的副行有时是口径（「跨周期统一折算」）、有时是样例数字
 *  （「¥4,300.00 / 月」）。前者抄，后者丢：一个孤零零的数字没有解释价值，
 *  而它出现在这里只会变成一个假读数。 */
export interface BlueprintTile {
  label: string;
  note: string;
}

/** 表格。列头逐字抄原型，表体恒为「未接入」。 */
export interface BlueprintTable {
  /** 读屏用的一句话（DataTableV2 的 caption，渲染成 sr-only）。 */
  caption: string;
  /** 列头，**逐字**照抄原型且**保持顺序**。 */
  columns: readonly string[];
  /** 这张表将来由什么填。显示在空状态里。 */
  source: string;
}

/** 键值卡（原型的 `kv` 卡片：左键右值）。
 *
 *  只列**键名**：值全是样例。键名本身是设计信息——它规定了这张卡要展示哪些
 *  字段，后续实现照它取数。 */
export interface BlueprintKeyCard {
  title: string;
  /** 卡片标题右侧的补充说明（原型的 `hint`）。 */
  hint?: string;
  /** 字段名清单，逐字照抄且保持顺序。 */
  keys: readonly string[];
}

/** 纯文案区块（原型的说明段、流程说明、原则卡）。 */
export interface BlueprintNote {
  title: string;
  body: string;
}

/** 一个页签（或一个没有页签的整页）的内容。 */
export interface BlueprintTab {
  /** URL 里 `?sub=` / `?tab=` 的取值。 */
  id: string;
  /** 页签名，逐字照抄。 */
  label: string;
  /** 这一格将来由什么填、归哪个阶段。**必填**——一格没有来源说明的空页，
   *  与「这个功能没有数据」在屏幕上长得一模一样。 */
  source: string;
  tiles?: readonly BlueprintTile[];
  /** 筛选条上的控件文案。只展示不实装：一个能点但筛不出东西的下拉，
   *  比没有筛选更让人困惑。 */
  filters?: readonly string[];
  tables?: readonly BlueprintTable[];
  cards?: readonly BlueprintKeyCard[];
  notes?: readonly BlueprintNote[];
}

/** 一个蓝图页。
 *
 *  **没有 title 字段**：页头标题一律取导航数据里的名字。原型给扩展能力四页的
 *  标题带「未来功能 · 」前缀，抄过来会让页头与侧栏、面包屑说不同的词——
 *  而「三处各写各的」正是 XM-0042 刚修掉的漂移（navigation.ts 顶部有记载）。
 *  「这是未来功能」由侧栏的 `未建·后置` 徽章与页顶横幅表达，不靠改标题。 */
export interface BlueprintPage {
  /** 页头副标题，逐字照抄原型。 */
  description: string;
  /** 页顶的红线提示（原型的 warnbar / futureBanner）。 */
  banner?: string;
  /** 全页共用的统计格（原型里治理段的四张卡是跨子页签共用的）。 */
  tiles?: readonly BlueprintTile[];
  tabs: readonly BlueprintTab[];
}
