import { Link } from "react-router";
import { ECONOMICS_PENDING_NOTE } from "../lib/channelEconomics";

/** 「这张表算的是什么」的口径声明（交接文档 §9.5，原型的 warnbar）。
 *
 *  §9.5 把渠道管理收窄成**一行 = 一个账号 / 一把 Key**，上游层面的汇总归
 *  「上游管理」。收窄的关键不在列怎么排，而在于把这句话说出来：同一个上游下
 *  的三条渠道各显示一份余额时，不写清楚「这是共享余额」的人会把它加三遍。
 *
 *  做成组件而不是两处各写一段文字，是为了让两个平台说的是同一件事——
 *  文案漂开之后，读的人会以为 Sub2API 与 NewAPI 的核算口径不同。 */
export function ChannelScopeNote({ platform }: { platform: "sub2api" | "newapi" }) {
  return (
    <div className="flex flex-col gap-1 rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg">
      <p>
        {platform === "sub2api"
          ? "渠道管理只做单账号 / 单 Key 核算，不在这里汇总上游。多个账号属于同一上游时，余额是共享的——把每行的余额加起来会把同一笔钱数很多遍。"
          : "NewAPI 与 Sub2API 共用上游目录和充值成本率，但各自保留渠道、Key 消耗、我方计费与利润。跨平台的整体成本要到上游那一层看。"}
      </p>
      <p className="text-fg-muted">
        共享余额与整体毛利见
        <Link
          to={`/platforms/${platform}?tab=suppliers`}
          className="mx-1 underline underline-offset-2"
        >
          上游管理
        </Link>
        。本表的供给成本 / 毛利 / 毛利率三列还接不上：{ECONOMICS_PENDING_NOTE}。
      </p>
    </div>
  );
}
