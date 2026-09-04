import { PageHeader } from "@xingmang/ui-admin";
import { CardsPanel } from "../components/CardsPanel";

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
    </div>
  );
}
