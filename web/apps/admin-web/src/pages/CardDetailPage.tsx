import { useQuery } from "@tanstack/react-query";
import { PageHeader } from "@xingmang/ui-admin";
import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { listCards } from "../api/cards";
import { ApiStateView } from "./../components/ApiStateView";
import { CardDetailPanel, type DetailTab } from "../components/CardDetailPanel";
import { ActionResultNote, type ActionResult } from "../components/ActionResultNote";
import { NotFoundView } from "./NotFoundPage";

/** 页签在 URL 里，用 `?tab=`。
 *
 *  这是详情从弹窗改成页面之后最实在的一处收获：列表的「充值」「赎回」
 *  按钮可以直接链到对应页签，而不必先开详情再找页签；同事之间也能把
 *  「这张卡的流水」这个具体位置发给对方。弹窗给不了这两样。 */
const TABS: DetailTab[] = ["info", "usage", "topup", "redeem", "tx"];

function parseTab(raw: string | null): DetailTab {
  return TABS.includes(raw as DetailTab) ? (raw as DetailTab) : "info";
}

/** 卡片详情页（XM-CARD7）。
 *
 *  ADMIN-IA §3 对「主对象」一律要求完整详情页、明令不用右侧抽屉
 *  （快照 RECOVERY.md「No right-side detail drawers」）。此前做成弹窗是
 *  实现期的偏离，产品负责人 2026-09-05 在生产上提出后纠正。
 *
 *  **数据复用列表那一个查询**（同一个 queryKey `cards`），不另开单卡端点：
 *  从列表点进来时直接命中缓存，页面秒开；直接粘链接进来时它自己会拉一次。
 *  上游本来也没有「按 id 查一张卡」的投影读法，为一个详情页新造一条读路径
 *  只会多一处会漂的地方。 */
export function CardDetailPage() {
  const { account = "", cardId = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const [result, setResult] = useState<ActionResult | null>(null);

  const query = useQuery({
    queryKey: ["cards"],
    queryFn: ({ signal }) => listCards({ signal }),
    staleTime: 30_000,
  });

  const tab = parseTab(params.get("tab"));
  function setTab(next: DetailTab) {
    // replace 而不是 push：页签切换不该在浏览器历史里堆一串，
    // 否则点五下返回要按五次才回得到列表。
    setParams((prev) => {
      const p = new URLSearchParams(prev);
      p.set("tab", next);
      return p;
    }, { replace: true });
  }

  const card = query.data?.cards.find((c) => c.account === account && c.card_id === cardId);

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <Link
        to="/cards"
        aria-label="返回卡片管理"
        className="inline-flex min-h-9 items-center self-start rounded-md px-2 text-sm font-medium text-accent hover:bg-accent-soft focus-visible:outline-2 focus-visible:outline-accent"
      >
        <span aria-hidden="true">←</span>
        <span className="ml-1">返回卡片管理</span>
      </Link>

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {card ? (
          <>
            <PageHeader
              title={`卡片 ${card.mask || card.card_id}`}
              description={`账号 ${card.account} · 状态 ${card.status}`}
            />
            {result ? <ActionResultNote result={result} /> : null}
            <CardDetailPanel card={card} onWrite={setResult} tab={tab} onTabChange={setTab} />
          </>
        ) : (
          // 查不到就明说查不到，不画一个字段全是「—」的空壳——
          // 那种壳会让人以为卡在但数据没同步，而真实原因是账号或卡号不对。
          <NotFoundView
            pathname={`/cards/${account}/${cardId}`}
            detail={`账号 ${account || "（空）"} 下没有卡片 ${cardId || "（空）"}`}
          />
        )}
      </ApiStateView>
    </div>
  );
}
