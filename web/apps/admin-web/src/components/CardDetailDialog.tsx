import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import {
  listCardTransactions,
  redeemCard,
  topUpCard,
  type CardItem,
  type CardTransactionItem,
} from "../api/cards";
import { formatMinorUnits } from "../lib/money";
import { ActionErrorNote } from "./ActionErrorNote";
import { type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

const CARD_TX_QUERY = "card-transactions";

const TOKEN_TYPE_OPTIONS = [
  { value: "USDT", label: "USDT" },
  { value: "USDC", label: "USDC" },
];

/** 一行「标签 + 值」。值用等宽字体，便于核对卡号这类逐位要对的东西。 */
function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className="text-xs text-fg-muted">{label}</dt>
      <dd className={mono ? "font-mono text-sm" : "text-sm"}>{value || "—"}</dd>
    </div>
  );
}

/** 资金操作表单（充值与赎回共用一套字段）。
 *
 *  幂等键在**打开表单时生成一次**：同一次提交的重试必须带同一个键，
 *  换一个键等于告诉后端「这是另一笔」，而上游没有幂等能力。 */
function FundsForm({
  card,
  kind,
  onDone,
}: {
  card: CardItem;
  kind: "topup" | "redeem";
  onDone: (result: ActionResult) => void;
}) {
  const [idempotencyKey, setIdempotencyKey] = useState(() => crypto.randomUUID());
  const [amount, setAmount] = useState("");
  const [tokenType, setTokenType] = useState("USDT");
  const [note, setNote] = useState("");
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  const isTopUp = kind === "topup";

  const mutation = useMutation({
    mutationFn: () => {
      const params = {
        account: card.account,
        idempotency_key: idempotencyKey,
        card_id: card.card_id,
        amount: amount.trim(),
        token_type: tokenType,
        ...(note.trim() ? { note: note.trim() } : {}),
      };
      return isTopUp ? topUpCard(params) : redeemCard(params);
    },
    onSuccess: (run) => {
      onDone({ runId: run.runId, title: isTopUp ? "已提交充值请求" : "已提交赎回请求" });
      setIdempotencyKey(crypto.randomUUID());
      setAmount("");
      setNote("");
      setError(null);
    },
    onError: (err) => setError(err),
  });

  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        mutation.mutate();
      }}
    >
      <p className="text-xs text-fg-muted">
        {isTopUp
          ? "充值受单笔与单日上限约束，超限会被直接拒绝。"
          : "赎回把余额退回账户，不受金额上限约束——用上限卡住止损动作没有道理。"}
      </p>
      <FormField label="金额" htmlFor={`${formId}-amount`} hint="十进制文本，单位是所选代币本身。">
        <Input
          id={`${formId}-amount`}
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
          required
        />
      </FormField>
      <FormField label="代币" htmlFor={`${formId}-token`}>
        <Select
          aria-label="代币"
          options={TOKEN_TYPE_OPTIONS}
          value={tokenType}
          onValueChange={setTokenType}
        />
      </FormField>
      <FormField label="备注" htmlFor={`${formId}-note`} hint="选填，会带给上游。">
        <Input id={`${formId}-note`} value={note} onChange={(e) => setNote(e.target.value)} />
      </FormField>
      {error ? <ActionErrorNote error={error} /> : null}
      <Button type="submit" disabled={mutation.isPending}>
        {mutation.isPending ? "提交中…" : isTopUp ? "确认充值" : "确认赎回"}
      </Button>
    </form>
  );
}

/** 卡片详情：卡面信息、资金操作、交易流水。
 *
 *  卡面明文由后端按 card.reveal 权限决定回不回——前端只负责显示它拿到的
 *  东西，不做「本地隐藏」那种假控制。 */
export function CardDetailDialog({
  card,
  onWrite,
}: {
  card: CardItem;
  onWrite: (result: ActionResult) => void;
}) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<"info" | "topup" | "redeem" | "tx">("info");

  const txQuery = useQuery({
    queryKey: [CARD_TX_QUERY, card.account, card.card_id],
    queryFn: ({ signal }) => listCardTransactions(card.account, card.card_id, { signal }),
    enabled: open && tab === "tx",
  });

  const afterWrite = (result: ActionResult) => {
    onWrite(result);
    void queryClient.invalidateQueries({ queryKey: [CARD_TX_QUERY, card.account, card.card_id] });
  };

  const columns: DataTableColumn<CardTransactionItem>[] = [
    {
      id: "occurred",
      header: "时间",
      cell: (row) => (row.occurred_at ? formatUtcTimestamp(row.occurred_at) : "—"),
      value: (row) => row.occurred_at ?? "",
      primary: true,
    },
    { id: "merchant", header: "商户", cell: (row) => row.merchant || "—", value: (row) => row.merchant },
    { id: "type", header: "类型", cell: (row) => row.type || "—", value: (row) => row.type },
    {
      id: "amount",
      header: "金额",
      cell: (row) => formatMinorUnits(row.amount_minor, row.currency),
      value: (row) => row.amount_minor,
      numeric: true,
    },
    {
      id: "fee",
      header: "手续费",
      cell: (row) => formatMinorUnits(row.fee_minor, row.currency),
      value: (row) => row.fee_minor,
      numeric: true,
    },
    {
      id: "status",
      header: "状态",
      cell: (row) => <Badge tone="neutral">{row.status || "—"}</Badge>,
      value: (row) => row.status,
    },
  ];

  const tabs = [
    { id: "info" as const, label: "卡面信息" },
    { id: "topup" as const, label: "充值" },
    { id: "redeem" as const, label: "赎回" },
    { id: "tx" as const, label: "交易流水" },
  ];

  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      title={`卡片详情 · ${card.mask || card.card_id}`}
      description={`账号 ${card.account}`}
      trigger={
        <Button variant="secondary" size="sm">
          详情
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap gap-2" role="tablist">
          {tabs.map((t) => (
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

        {tab === "info" ? (
          <dl className="grid grid-cols-2 gap-3">
            <Field label="账号" value={card.account} />
            <Field label="状态" value={card.status} />
            <Field label="卡号" value={card.pan ?? card.mask} mono />
            <Field label="CVV" value={card.cvv ?? "—"} mono />
            <Field label="有效期（MMYY）" value={card.expiry_mmyy ?? "—"} mono />
            <Field label="持卡人" value={card.holder_name} />
            <Field label="余额" value={formatMinorUnits(card.balance_minor, card.currency)} />
            <Field label="用途" value={card.owner_ref ?? "—"} />
            <Field label="卡别名（幂等信标）" value={card.card_alias} mono />
            <Field
              label="数据同步于"
              value={card.freshness.synced_at ? formatUtcTimestamp(card.freshness.synced_at) : "从未同步"}
            />
            {card.pan ? null : (
              <p className="col-span-2 text-xs text-fg-muted">
                卡面明文尚未拉取。卡要先变成 active，同步作业才拉得到；
                若你看不到卡号但别人看得到，是缺 card.reveal 权限。
              </p>
            )}
          </dl>
        ) : null}

        {tab === "topup" ? <FundsForm card={card} kind="topup" onDone={afterWrite} /> : null}
        {tab === "redeem" ? <FundsForm card={card} kind="redeem" onDone={afterWrite} /> : null}

        {tab === "tx" ? (
          <ApiStateView
            isPending={txQuery.isPending}
            error={txQuery.error}
            onRetry={() => void txQuery.refetch()}
            compact
          >
            <DataTableV2
              caption="这张卡的交易流水"
              rows={txQuery.data ?? []}
              columns={columns}
              rowKey={(row) => `${row.occurred_at}/${row.amount_minor}/${row.merchant}`}
              emptyState={
                <p className="text-sm text-fg-muted">
                  暂无流水。流水同步默认关闭（调用量与卡数成正比，上游限流阈值未知），
                  需要时用 XM_CARDS_SYNC_TRANSACTIONS=true 打开。
                </p>
              }
            />
          </ApiStateView>
        ) : null}
      </div>
    </Dialog>
  );
}
