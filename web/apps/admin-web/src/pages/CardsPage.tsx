import { useQuery } from "@tanstack/react-query";
import { PageHeader } from "@xingmang/ui-admin";
import { Button } from "@xingmang/ui-primitives";
import { useSearchParams } from "react-router";
import { listCards } from "../api/cards";
import { AccountBalancesStrip, AttentionBanner, IssueCardDialog } from "../components/CardsShared";
import { CardWorkbench } from "../components/CardWorkbench";
import { WithdrawPanel } from "../components/WithdrawPanel";

/** 顶级页签。照 Infini 后台的三个来，**「订阅」刻意不做**。
 *
 *  Infini 那个页签自己在页面上写着「基于交易记录自动识别，仅供参考，可能与
 *  实际订阅不一致」——那是他们的一套商户名识别逻辑，我们没有。照着画一个
 *  只会得到空页签，或者更糟：一堆猜出来的订阅，而人会当真。 */
const TABS = [
  { id: "cards", label: "卡片管理" },
  { id: "withdraw", label: "提现" },
] as const;

type TabID = (typeof TABS)[number]["id"];

function parseTab(raw: string | null): TabID {
  return TABS.some((t) => t.id === raw) ? (raw as TabID) : "cards";
}

/** 卡片管理（XM-CARD3，2026-09-05 改为 Infini 同构布局）。
 *
 *  这一页在 ADMIN-IA 里**没有对应条目**——原型画的四个平台里没有卡片这一块。
 *  放在「平台治理」下是实现期的判断（它管的是平台自己持有的支付工具，
 *  不属于任何一个上游平台）。
 *
 *  布局照 Infini 后台做（产品负责人裁定），对 ADMIN-IA §3 的推翻已记在那份
 *  文档的例外一节。 */
export function CardsPage() {
  const [params, setParams] = useSearchParams();
  const tab = parseTab(params.get("tab"));

  const query = useQuery({
    queryKey: ["cards"],
    queryFn: ({ signal }) => listCards({ signal }),
    staleTime: 30_000,
    retry: false,
  });

  function setTab(next: TabID) {
    // replace：页签切换是浏览行为，不该在历史里堆一串。
    setParams(
      (prev) => {
        const p = new URLSearchParams(prev);
        p.set("tab", next);
        return p;
      },
      { replace: true },
    );
  }

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <PageHeader
        title="企业卡"
        description="Infini 虚拟卡的开卡、充值、冻结与卡面查看，以及资金提现。所有写操作经 Action 执行并留审计。"
      />

      <div className="border-edge flex flex-wrap items-center justify-between gap-3 border-b pb-2">
        <div className="flex flex-wrap gap-2" role="tablist">
          {TABS.map((t) => (
            <Button
              key={t.id}
              size="sm"
              variant={tab === t.id ? "primary" : "secondary"}
              onClick={() => setTab(t.id)}
              role="tab"
              aria-selected={tab === t.id}
            >
              {t.label}
            </Button>
          ))}
        </div>
        {tab === "cards" ? (
          <IssueCardDialog
            accounts={query.data?.accounts ?? []}
            memberEmails={query.data?.memberEmails ?? []}
          />
        ) : null}
      </div>

      {tab === "cards" ? (
        <>
          {/* 待人工处置的操作与资金池余额留在页签之上：前者是红条，
              后者是「还能开几张卡」的前提，切到哪个页签都该看得见。 */}
          <AttentionBanner />
          <AccountBalancesStrip />
          <CardWorkbench />
        </>
      ) : (
        <WithdrawPanel accounts={query.data?.accounts ?? []} />
      )}
    </div>
  );
}
