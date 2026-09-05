import { Button, Dialog } from "@xingmang/ui-primitives";
import type { CardItem } from "../api/cards";
import { FundsForm } from "./CardCardForms";
import type { ActionResult } from "./ActionResultNote";

/** 充值 / 赎回的对话框。
 *
 *  **这两个保留弹窗是刻意的,不是漏改。** 产品负责人要求去掉的是「详情」
 *  和「交易流水」那两个弹窗——它们装的是要看、要搜、要比对的内容,弹窗的
 *  尺寸约束让人反而更难用。充值与赎回是**一次性输入**:填个金额选个代币
 *  就走,做成弹窗恰恰合适,而 Infini 后台自己也正是这么做的
 *  （2026-09-05 的截图里「充值」「赎回卡片余额」都是模态框）。
 *
 *  幂等键在表单内部生成并在其生命周期内不变，见 FundsForm。 */
export function CardFundsDialog({
  card,
  kind,
  onDone,
}: {
  card: CardItem;
  kind: "topup" | "redeem";
  onDone: (result: ActionResult) => void;
}) {
  const label = kind === "topup" ? "充值" : "赎回";
  return (
    <Dialog
      title={kind === "topup" ? "充值" : "赎回卡片余额"}
      description={
        kind === "topup"
          ? "从 Infini 现金账户转入这张卡。金额上限按账号配置。"
          : "把卡上的余额退回 Infini 现金账户。不受金额上限约束——赎回是资金回流不是花钱。"
      }
      trigger={
        <Button variant="secondary" size="sm">
          {label}
        </Button>
      }
    >
      <FundsForm card={card} kind={kind} onDone={onDone} />
    </Dialog>
  );
}
