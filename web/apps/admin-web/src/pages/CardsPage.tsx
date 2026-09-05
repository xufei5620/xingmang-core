import { PageHeader } from "@xingmang/ui-admin";
import { useQuery } from "@tanstack/react-query";
import { CardsPanel } from "../components/CardsPanel";
import { WithdrawPanel } from "../components/WithdrawPanel";
import { listCards } from "../api/cards";

/** 卡片管理（XM-CARD3）。
 *
 *  这一页在 ADMIN-IA 里**没有对应条目**——原型画的四个平台里没有卡片这一块。
 *  放在「平台治理」下是实现期的判断（它管的是平台自己持有的支付工具，
 *  不属于任何一个上游平台），归属需要产品负责人确认。 */
export function CardsPage() {
  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="卡片管理"
        description="Infini 虚拟卡的开卡、充值、冻结与卡面查看。所有写操作经 Action 执行并留审计。"
      />
      <CardsPanel />
      <WithdrawSection />
    </div>
  );
}

/** 提现区。
 *
 *  与卡片同页而不是另开一页：它们花的是同一个 Infini 账号的余额，
 *  「卡上还有多少」和「能提走多少」是同一个人在同一分钟里要一起看的两个数。
 *
 *  账号清单借用卡片列表那一个查询（同一个 queryKey，命中缓存不多打一次），
 *  而不是另开一个端点：账号是同一批。查询失败或未启用时给空数组——
 *  提现面板自己会显示未启用/需要授权，不必在这里再判一次。 */
function WithdrawSection() {
  const query = useQuery({
    queryKey: ["cards"],
    queryFn: ({ signal }) => listCards({ signal }),
    staleTime: 30_000,
    retry: false,
  });

  return (
    <div className="flex flex-col gap-3">
      {/* ui-admin 没有 SectionHeader，页内小节用朴素标题即可——
          为一个二级标题新建一个组件不值得。 */}
      <div>
        <h2 className="text-lg font-semibold">提现</h2>
        <p className="text-sm text-fg-muted">
          把 Infini 账号余额转到已登记的链上地址。需要 fund-operator
          角色；没有这个角色时下方会显示无权限。
        </p>
      </div>
      <WithdrawPanel accounts={query.data?.accounts ?? []} />
    </div>
  );
}
