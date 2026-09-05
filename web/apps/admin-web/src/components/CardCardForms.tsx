import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import {
  listCardTransactions,
  redeemCard,
  setCardUsage,
  topUpCard,
  type CardItem,
  type CardTransactionItem,
} from "../api/cards";
import { transactionStatusLabel, transactionTypeLabel } from "../lib/cardStatus";
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
export function FundsForm({
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
          ? "金额上限按账号配置，未配置会被直接拒绝；当前口径是不设限。"
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
export type DetailTab = "info" | "usage" | "topup" | "redeem" | "tx";

/** 这张卡的交易流水。
 *
 *  从原来的详情弹窗里搬出来的：弹窗的尺寸约束让这张可搜索可排序的表只能
 *  挤在一小块里，真要查一笔消费反而得先关掉它回列表。现在它是右栏详情的
 *  一节，和卡片信息同屏。
 *
 *  跨卡的「交易记录」是另一件事（Infini 有一个顶级页签），需要一个新的
 *  后端端点——投影表里有数据，但今天只有按卡查的读法。 */
export function CardTransactions({ card }: { card: CardItem }) {
  const query = useQuery({
    queryKey: [CARD_TX_QUERY, card.account, card.card_id],
    queryFn: ({ signal }) => listCardTransactions(card.account, card.card_id, { signal }),
  });

  const columns: DataTableColumn<CardTransactionItem>[] = [
    {
      id: "occurred",
      header: "时间",
      cell: (row) => (row.occurred_at ? formatUtcTimestamp(row.occurred_at) : "—"),
      value: (row) => row.occurred_at ?? "",
      primary: true,
    },
    { id: "merchant", header: "商户", cell: (row) => row.merchant || "—", value: (row) => row.merchant },
    {
      id: "type",
      header: "类型",
      cell: (row) => transactionTypeLabel(row.type),
      value: (row) => transactionTypeLabel(row.type),
    },
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
      id: "original",
      header: "原始金额",
      // 只在与卡本位币不同时显示：同币种消费时上游不给这两个字段，
      // 硬填一个「等于本币」的值会让跨境消费看起来和普通消费一样。
      cell: (row) =>
        row.transaction_amount && row.transaction_currency
          ? `${row.transaction_amount} ${row.transaction_currency}`
          : "—",
      value: (row) => row.transaction_amount ?? "",
    },
    {
      id: "settled",
      header: "结算时间",
      // 空 = 仅授权、尚未结算。授权可以被撤销，金额也可能变（见回调里的
      // auth_settle_adjustment），所以这一列不能拿「交易时间」顶替。
      cell: (row) => (row.settled_at ? formatUtcTimestamp(row.settled_at) : "授权中"),
      value: (row) => row.settled_at ?? "",
    },
    {
      id: "status",
      header: "状态",
      cell: (row) => <Badge tone="neutral">{transactionStatusLabel(row.status)}</Badge>,
      value: (row) => transactionStatusLabel(row.status),
    },
  ];

  return (
    <ApiStateView
      isPending={query.isPending}
      error={query.error}
      onRetry={() => void query.refetch()}
      compact
    >
      <DataTableV2
        caption="这张卡的交易流水"
        rows={query.data ?? []}
        columns={columns}
        rowKey={(row) => `${row.occurred_at}/${row.amount_minor}/${row.merchant}`}
        emptyState={
          <p className="text-fg-muted text-sm">
            暂无流水。流水同步每 5 分钟一轮，回调到达时也会即时推进这张卡。
          </p>
        }
      />
    </ApiStateView>
  );
}

const BOUND_KIND_OPTIONS = [
  { value: "", label: "（未指定）" },
  { value: "email", label: "邮箱" },
  { value: "username", label: "用户名" },
  { value: "phone", label: "手机号" },
  { value: "other", label: "其他" },
];

/** 用途登记表单：这张卡绑在哪个外部服务账号上、订了什么、什么时候续费。
 *
 *  这些字段上游一个都不知道，全是平台自己记的。续费日期是**人填的**：
 *  从流水推断周期看着聪明，但试用转正、年付转月付、涨价都会让推断悄悄错掉，
 *  而错了的提醒比没有提醒更糟——人会信它。 */
export function UsageForm({ card, onDone }: { card: CardItem; onDone: (r: ActionResult) => void }) {
  const [boundAccount, setBoundAccount] = useState(card.bound_account ?? "");
  const [boundKind, setBoundKind] = useState(card.bound_account_kind ?? "");
  const [serviceName, setServiceName] = useState(card.service_name ?? "");
  const [renewal, setRenewal] = useState(card.next_renewal_on ?? "");
  const [note, setNote] = useState(card.usage_note ?? "");
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  const mutation = useMutation({
    mutationFn: () =>
      setCardUsage({
        account: card.account,
        card_id: card.card_id,
        bound_account: boundAccount.trim(),
        bound_account_kind: boundKind,
        service_name: serviceName.trim(),
        next_renewal_on: renewal.trim(),
        note: note.trim(),
      }),
    onSuccess: (run) => {
      onDone({ runId: run.runId, title: "已更新用途登记" });
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
      <FormField
        label="绑定账号"
        htmlFor={`${formId}-bound`}
        hint="这张卡绑在哪个外部服务账号上，如 chris@example.com。"
      >
        <Input
          id={`${formId}-bound`}
          value={boundAccount}
          onChange={(e) => setBoundAccount(e.target.value)}
        />
      </FormField>
      <FormField
        label="账号类型"
        htmlFor={`${formId}-kind`}
        hint="显式选择，不靠长相猜——有些服务的账号是用户名或手机号。"
      >
        <Select
          aria-label="账号类型"
          options={BOUND_KIND_OPTIONS}
          value={boundKind}
          onValueChange={setBoundKind}
        />
      </FormField>
      <FormField label="订阅服务" htmlFor={`${formId}-service`} hint="如 OpenAI Plus。">
        <Input
          id={`${formId}-service`}
          value={serviceName}
          onChange={(e) => setServiceName(e.target.value)}
        />
      </FormField>
      <FormField
        label="下次续费日期"
        htmlFor={`${formId}-renewal`}
        hint="YYYY-MM-DD，留空表示不是订阅。续费日临近而卡上没钱时列表会标红。"
      >
        <Input
          id={`${formId}-renewal`}
          type="date"
          value={renewal}
          onChange={(e) => setRenewal(e.target.value)}
        />
      </FormField>
      <FormField label="备注" htmlFor={`${formId}-note`}>
        <Input id={`${formId}-note`} value={note} onChange={(e) => setNote(e.target.value)} />
      </FormField>
      {error ? <ActionErrorNote error={error} /> : null}
      <Button type="submit" disabled={mutation.isPending}>
        {mutation.isPending ? "保存中…" : "保存登记"}
      </Button>
    </form>
  );
}
