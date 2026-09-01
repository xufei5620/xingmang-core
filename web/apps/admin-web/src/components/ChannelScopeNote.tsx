/** 「这张表算的是什么」的口径声明（原型的 warnbar，交接文档 §9.5 / §12.2）。
 *
 *  §9.5 把渠道管理收窄成**单账号 / 单 Key 核算**；收窄的关键不在列怎么排,
 *  而在于把这句话说出来：同一个上游下的几条渠道各显示一份余额时，不写清楚
 *  「这是共享余额」的人会把它加很多遍。
 *
 *  做成组件而不是两处各写一段文字，是为了让两个平台说的是同一件事——
 *  文案漂开之后，读的人会以为 Sub2API 与 NewAPI 的核算口径不同。
 *
 *  ## `grain`：两条分支说的不是同一件事，不能共用一份文案
 *
 *  `ChannelTable.tsx` 有两条行粒度分支：`"channel"`（恰好一个已登记且 active
 *  的 service，一行 = 一个账号 / 渠道）与 `"account"`（回落分支，一行 = 一个
 *  上游账号）。2026-09-02 07:20 产品负责人裁定补充（ACCEPTANCE-LOG）把登记簿
 *  字段直接并入了行与详情页，两条分支从此都没有另一张"汇总表"可以指——
 *  之前（04:40 裁定期间）这里的第二段文字统一写"见本页下方『上游管理』区块",
 *  区块被 07:20 裁定推翻之后这句话已经不成立，因此改成按 `grain` 分别措辞,
 *  而不是继续指向一个已经不存在的目标。 */
export function ChannelScopeNote({
  platform,
  grain,
}: {
  platform: "sub2api" | "newapi";
  grain: "channel" | "account";
}) {
  return (
    <div className="flex flex-col gap-1 rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg">
      <p>
        {platform === "sub2api"
          ? "渠道管理只做单账号 / 单 Key 核算，不做跨账号汇总。上游账号的登记簿字段（分组、余额、联系人……）直接并到每一行与详情页里。"
          : "NewAPI 与 Sub2API 共用上游目录和充值成本率，但各自保留渠道、Key 消耗、我方计费和利润；登记簿字段同样直接并到每一行与详情页里，不做跨平台汇总。"}
      </p>
      {grain === "channel" ? (
        <p className="text-fg-muted">
          本表一行 = 一个<strong className="font-medium">账号 / 渠道</strong>：多行绑定同一个上游账号时,
          「余额 / 有效期」「倍率 / 上游倍率」「充值成本率」这些格子来自同一份登记簿记录,
          是共享值，不是各行各自独立的数字——把它们相加会把同一份事实数很多遍。
        </p>
      ) : (
        <p className="text-fg-muted">
          本表一行 = 一个<strong className="font-medium">上游账号</strong>：一个账号名下有多个令牌时，
          「余额 / 有效期」那一格里的余额是这些令牌共享的，把每行相加会把同一笔钱数很多遍。
        </p>
      )}
    </div>
  );
}
