import { PageState, StatTile } from "@xingmang/ui-admin";
import type { BlueprintKeyCard, BlueprintTab, BlueprintTable, BlueprintTile } from "./types";

/** 蓝图页签的统一渲染（UI 第 6 片）。
 *
 *  一格里可能有：统计格、筛选条、表格、键值卡、说明块。顺序固定，不由规格控制
 *  ——原型里这五类的相对位置在所有页上都一样，让规格能调它只会让 11 个页面
 *  各排各的。
 *
 *  **这里没有任何样例数字**：统计格是 `unavailable`（主数位「—」），
 *  表格只渲染表头、表体是「未接入」。见 types.ts 顶部对这条取舍的说明。 */
export function BlueprintTabView({ tab }: { tab: BlueprintTab }) {
  return (
    <div className="flex flex-col gap-3">
      {tab.tiles && tab.tiles.length > 0 ? <BlueprintTiles tiles={tab.tiles} /> : null}
      {tab.filters && tab.filters.length > 0 ? <BlueprintFilters filters={tab.filters} /> : null}
      {tab.tables?.map((table) => <BlueprintTableView key={table.caption} table={table} />)}
      {tab.cards && tab.cards.length > 0 ? <BlueprintCards cards={tab.cards} /> : null}
      {tab.notes?.map((note) => (
        <section key={note.title} className="rounded-lg border border-edge bg-surface p-4">
          <h3 className="text-sm font-medium text-fg">{note.title}</h3>
          <p className="mt-1 text-xs text-fg-muted">{note.body}</p>
        </section>
      ))}
      {/* 每一格的落款：这一格将来由什么填。没有它，一屏「未接入」看不出
          是在等采集、等后端，还是等产品拍板 */}
      <p className="text-xs text-fg-muted">{tab.source}</p>
    </div>
  );
}

/** 统计格。**主数位恒为「—」**：原型的数值是样例，抄进来就是假读数。 */
export function BlueprintTiles({ tiles }: { tiles: readonly BlueprintTile[] }) {
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      {tiles.map((tile) => (
        <StatTile key={tile.label} label={tile.label} value="—" unavailable note={tile.note} />
      ))}
    </div>
  );
}

/** 筛选条：**只展示，不实装**。
 *
 *  渲染成禁用的按钮而不是真下拉：一个能点开、选完却什么都不变的筛选器，
 *  比没有筛选更让人困惑——人会以为筛出来的就是全部。禁用态则一眼看出
 *  「这里将来有个筛选」。 */
function BlueprintFilters({ filters }: { filters: readonly string[] }) {
  return (
    <div className="flex flex-wrap items-center gap-2" aria-label="筛选条（尚未启用）">
      {filters.map((label) => (
        <span
          key={label}
          className="rounded-md border border-edge bg-surface-muted px-2 py-1 text-xs text-fg-muted"
        >
          {label}
        </span>
      ))}
      <span className="text-xs text-fg-muted">筛选条随数据源一并启用。</span>
    </div>
  );
}

/** 表格：列头逐字照抄，表体是「未接入」。
 *
 *  **为什么不是 DataTableV2**（前端红线要求先查 Storybook 再新建，所以这里要交代
 *  清楚）：它在 `rows.length === 0` 时**直接返回 emptyState**，整张表连表头都不渲染
 *  （DataTableV2.tsx:207）。那个行为对它自己的场景是对的——一张真表在没数据时，
 *  摆一排空列头只是噪音。
 *
 *  但蓝图页要的恰恰是那排列头：这一片的产出就是「将来这张表有哪几列、什么顺序」。
 *  两种需求相反，所以不去改 DataTableV2（那会影响审计、请求、告警等每一张真表），
 *  而是在这里渲染一个**只有表头的预览**。
 *
 *  样式沿用与 DataTableV2 相同的 token 类，看起来是同一套表；语义上
 *  「有 thead、没有 tbody 行」正是这一格想说的话：表在，列定了，数据还没有。 */
function BlueprintTableView({ table }: { table: BlueprintTable }) {
  return (
    <section className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-3">
      <div className="max-w-full overflow-x-auto">
        <table className="w-full border-collapse text-sm">
          <caption className="sr-only">{`${table.caption}（列结构预览，尚无数据）`}</caption>
          <thead className="border-b border-edge bg-surface-muted">
            <tr>
              {table.columns.map((header, index) => (
                <th
                  // 列头文案可能重复（原型里不止一张表的末列都叫「详情」），
                  // 所以 key 用序号而不是文案
                  key={`${header}-${index}`}
                  scope="col"
                  className="px-3 py-2 text-left text-xs font-medium text-fg-muted"
                >
                  {header}
                </th>
              ))}
            </tr>
          </thead>
        </table>
      </div>
      <PageState kind="unavailable" description={table.source} compact />
    </section>
  );
}

/** 键值卡：只列字段名。
 *
 *  值全是样例，一个都不抄。字段名本身是设计信息——它规定了这张卡要展示哪些
 *  字段，后续实现照它取数。 */
function BlueprintCards({ cards }: { cards: readonly BlueprintKeyCard[] }) {
  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
      {cards.map((card) => (
        <section key={card.title} className="rounded-lg border border-edge bg-surface p-4">
          <header className="flex flex-wrap items-baseline justify-between gap-2">
            <h3 className="text-sm font-medium text-fg">{card.title}</h3>
            {card.hint ? <p className="text-xs text-fg-muted">{card.hint}</p> : null}
          </header>
          <dl className="mt-2 flex flex-col gap-1">
            {card.keys.map((key) => (
              <div key={key} className="flex items-baseline justify-between gap-3 text-xs">
                <dt className="text-fg-muted">{key}</dt>
                {/* 值位固定是「—」：这一格的取值全部来自还没接上的数据源 */}
                <dd className="tabular-nums text-fg-muted">—</dd>
              </div>
            ))}
          </dl>
        </section>
      ))}
    </div>
  );
}

/** 页顶红线提示（原型的 warnbar / futureBanner）。
 *
 *  `role="status"` 而不是纯文本：它说的是「这一页不会执行真实操作」，
 *  属于读屏用户同样要听到的信息，与 PlaceholderGate 的门禁提示同一处理。 */
export function BlueprintBanner({ text }: { text: string }) {
  return (
    <p
      role="status"
      className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
    >
      {text}
    </p>
  );
}
