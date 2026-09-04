import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import {
  freezeCard,
  issueCard,
  listCardOperationsNeedingAttention,
  listCards,
  revealCard,
  unfreezeCard,
  type CardFreshness,
  type CardItem,
  type CardOperationItem,
  type RevealedCard,
} from "../api/cards";
import { formatMinorUnits } from "../lib/money";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

const CARDS_QUERY = "cards";
const CARD_ATTENTION_QUERY = "card-operations-attention";

/** 充值币种。与后端 Action 契约的枚举一致——多一个值会被后端当场拒掉。 */
const TOKEN_TYPE_OPTIONS = [
  { value: "USDT", label: "USDT" },
  { value: "USDC", label: "USDC" },
];

/** Infini 卡产品。id 是上游的 product_id，改动要跟着上游走。 */
const CARD_PRODUCTS = [
  { value: "1", label: "Infini Lite" },
  { value: "2", label: "Infini Pro" },
  { value: "102", label: "Infini AI" },
];

/** 新鲜度徽章。
 *
 *  不复用 ui-admin 的 FreshnessBadge：那个吃的是采集链路的 `state` +
 *  `last_error_code` 形状，与卡片投影的三态（正常 / 陈旧 / 从未同步）对不上。
 *  硬套会让「从未同步」显示成某种采集错误，排查方向就错了。 */
function CardFreshnessBadge({ freshness }: { freshness: CardFreshness }) {
  if (freshness.never_synced) {
    return (
      <Badge tone="warning" title="这张卡自建立起从未被同步作业刷新过，与「很久没同步」不是一回事">
        从未同步
      </Badge>
    );
  }
  if (freshness.stale) {
    return (
      <Badge tone="warning" title={`数据落后约 ${freshness.age_seconds} 秒，可能已过期`}>
        数据可能过期
      </Badge>
    );
  }
  return (
    <Badge tone="success" title={freshness.synced_at ? formatUtcTimestamp(freshness.synced_at) : ""}>
      最新
    </Badge>
  );
}

/** 卡状态徽章。冻结与删除都用 danger：它们都意味着这张卡现在刷不了。 */
function cardStatusTone(status: string): "neutral" | "success" | "warning" | "danger" {
  switch (status) {
    case "active":
      return "success";
    case "init":
    case "pending":
      return "warning";
    case "frozen":
    case "deleted":
    case "pending_delete":
      return "danger";
    default:
      return "neutral";
  }
}

/** 待人工处置的横幅。
 *
 *  每一条都意味着「有一笔花钱操作，我们至今不知道它到底成没成」。
 *  这是整个页面最该被看见的东西，所以排在表格之前而不是折叠在角落。 */
function AttentionBanner({ items }: { items: CardOperationItem[] }) {
  if (items.length === 0) return null;

  return (
    <section className="rounded-md border border-danger bg-danger/10 p-3" role="alert">
      <h3 className="text-sm font-medium text-danger">
        {items.length} 笔操作需要人工确认
      </h3>
      <p className="mt-1 text-xs text-fg-muted">
        上游没有幂等能力，这些操作超时后无法判定是否已经生效。请到 Infini
        后台核对后再决定——**在确认之前不要重试**，重试可能重复扣钱。
      </p>
      <ul className="mt-2 flex flex-col gap-1">
        {items.map((op) => (
          <li key={op.idempotency_key} className="text-xs text-fg">
            <span className="font-mono">{op.idempotency_key}</span>
            {" · "}
            {op.kind}
            {op.amount ? ` · ${op.amount} ${op.token_type ?? ""}` : ""}
            {" · 发起于 "}
            {formatUtcTimestamp(op.started_at)}
            {op.reason ? ` · ${op.reason}` : ""}
            {op.retry_allowed ? null : " · 已锁定重试"}
          </li>
        ))}
      </ul>
    </section>
  );
}

/** 查看明文卡面的对话框。
 *
 *  明文只存在于组件的局部 state 里，关闭即清空：不写 URL、不写
 *  localStorage、不进任何全局状态容器。后端那边它也不落库、不进日志、
 *  不进审计正文——审计只记「谁在何时看了哪张卡」。 */
function RevealDialog({ card }: { card: CardItem }) {
  const [open, setOpen] = useState(false);
  const [revealed, setRevealed] = useState<RevealedCard | null>(null);
  const [error, setError] = useState<unknown>(null);

  const mutation = useMutation({
    mutationFn: () => revealCard({ account: card.account, card_id: card.card_id }),
    onSuccess: (data) => {
      setRevealed(data);
      setError(null);
    },
    onError: (err) => setError(err),
  });

  const close = () => {
    setOpen(false);
    // 关闭即清空，不等组件卸载
    setRevealed(null);
    setError(null);
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => (next ? setOpen(true) : close())}
      title="查看卡面信息"
      description="每次查看都会被审计记录。明文不会被保存在任何地方，关闭后需重新获取。"
      trigger={
        <Button variant="secondary" size="sm">
          查看卡面
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        {!revealed ? (
          <>
            <p className="text-sm text-fg-muted">
              将向上游请求 {card.mask} 的完整卡号、CVV 与有效期。
            </p>
            <Button onClick={() => mutation.mutate()} disabled={mutation.isPending}>
              {mutation.isPending ? "获取中…" : "获取卡面信息"}
            </Button>
          </>
        ) : (
          <dl className="flex flex-col gap-2 text-sm">
            <div>
              <dt className="text-fg-muted">卡号</dt>
              <dd className="font-mono">{revealed.Number}</dd>
            </div>
            <div>
              <dt className="text-fg-muted">CVV</dt>
              <dd className="font-mono">{revealed.CVV}</dd>
            </div>
            <div>
              <dt className="text-fg-muted">有效期（MMYY）</dt>
              <dd className="font-mono">{revealed.ExpiryMMYY}</dd>
            </div>
          </dl>
        )}
        {error ? <ActionErrorNote error={error} /> : null}
      </div>
    </Dialog>
  );
}

/** 开卡表单。
 *
 *  幂等键在**打开对话框时生成一次**，整个表单生命周期内不变：
 *  同一次提交的重试必须带同一个键，换一个键等于告诉后端「这是另一次开卡」，
 *  而上游没有幂等能力。 */
function IssueCardDialog({
  accounts,
  onIssued,
}: {
  accounts: string[];
  onIssued: (result: ActionResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const [account, setAccount] = useState("");
  const [idempotencyKey, setIdempotencyKey] = useState(() => crypto.randomUUID());
  const [productId, setProductId] = useState("1");
  const [amount, setAmount] = useState("");
  const [tokenType, setTokenType] = useState("USDT");
  const [email, setEmail] = useState("");
  const [holder, setHolder] = useState("");
  const [ownerRef, setOwnerRef] = useState("");
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  // 账号清单是异步到达的（与卡片列表同一个查询）。初值只在首次渲染取一次
  // 会永远停在空，于是表单提交一个空账号——后端会拒，但表单本身是坏的。
  // 每次渲染校正：选中的账号必须在当前清单里，否则回落到第一个。
  const effectiveAccount = accounts.includes(account) ? account : (accounts[0] ?? "");

  const mutation = useMutation({
    mutationFn: () =>
      issueCard({
        account: effectiveAccount,
        idempotency_key: idempotencyKey,
        product_id: Number(productId),
        top_up_amount: amount.trim(),
        token_type: tokenType,
        user_email: email.trim(),
        holder_name: holder.trim(),
        ...(ownerRef.trim() ? { owner_ref: ownerRef.trim() } : {}),
      }),
    onSuccess: (run) => {
      onIssued({ runId: run.runId, title: "已提交开卡请求" });
      setOpen(false);
      // 下一次开卡是另一笔业务，必须换一个幂等键
      setIdempotencyKey(crypto.randomUUID());
      setAmount("");
      setEmail("");
      setHolder("");
      setError(null);
    },
    onError: (err) => setError(err),
  });

  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      title="开卡"
      description="开卡会真实扣款且不可撤销。金额受单笔与单日上限约束，超限会被直接拒绝。"
      trigger={<Button>开卡</Button>}
    >
      <form
        id={formId}
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          mutation.mutate();
        }}
      >
        <FormField
          label="使用账号"
          htmlFor={`${formId}-account`}
          hint="这张卡的钱从哪个 Infini 账号出。两个账号的资金与额度是分开的。"
        >
          <Select
            aria-label="使用账号"
            options={accounts.map((a) => ({ value: a, label: a }))}
            value={effectiveAccount}
            onValueChange={setAccount}
          />
        </FormField>
        <FormField label="卡产品" htmlFor={`${formId}-product`}>
          <Select
            aria-label="卡产品"
            options={CARD_PRODUCTS}
            value={productId}
            onValueChange={setProductId}
          />
        </FormField>
        <FormField
          label="充值金额"
          htmlFor={`${formId}-amount`}
          hint="十进制文本，如 10.00。单位是所选代币本身，不做汇率换算。"
        >
          <Input
            id={`${formId}-amount`}
            value={amount}
            onChange={(e) => setAmount(e.target.value)}
            required
          />
        </FormField>
        <FormField label="代币" htmlFor={`${formId}-token`}>
          <Select
            aria-label="充值代币"
            options={TOKEN_TYPE_OPTIONS}
            value={tokenType}
            onValueChange={setTokenType}
          />
        </FormField>
        <FormField label="企业成员邮箱" htmlFor={`${formId}-email`}>
          <Input
            id={`${formId}-email`}
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
          />
        </FormField>
        <FormField label="持卡人姓名" htmlFor={`${formId}-holder`}>
          <Input
            id={`${formId}-holder`}
            value={holder}
            onChange={(e) => setHolder(e.target.value)}
            required
          />
        </FormField>
        <FormField
          label="用途标签"
          htmlFor={`${formId}-owner`}
          hint="选填。记录这张卡归谁用，便于以后按归属筛选。"
        >
          <Input
            id={`${formId}-owner`}
            value={ownerRef}
            onChange={(e) => setOwnerRef(e.target.value)}
          />
        </FormField>

        <p className="text-xs text-fg-muted">
          幂等键 <span className="font-mono">{idempotencyKey}</span>
          ：这次提交若超时，重试会带同一个键，不会重复开卡。
        </p>

        {error ? <ActionErrorNote error={error} /> : null}

        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending ? "提交中…" : "确认开卡"}
        </Button>
      </form>
    </Dialog>
  );
}

/** 卡片管理面板（XM-CARD3）。
 *
 *  读走 `/api/v1/cards*` 三个只读端点，写全部经 `cards.card.*` Action
 *  （宪法 2 条）。页面本身不做任何金额算术：余额是整数最小单位，
 *  只交给 formatMinorUnits 显示。 */
export function CardsPanel() {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);
  const [actionError, setActionError] = useState<unknown>(null);

  const query = useQuery({
    queryKey: [CARDS_QUERY],
    queryFn: ({ signal }) => listCards({ signal }),
  });
  const attentionQuery = useQuery({
    queryKey: [CARD_ATTENTION_QUERY],
    queryFn: ({ signal }) => listCardOperationsNeedingAttention({ signal }),
  });

  const afterWrite = (written: ActionResult) => {
    setResult(written);
    setActionError(null);
    void queryClient.invalidateQueries({ queryKey: [CARDS_QUERY] });
    void queryClient.invalidateQueries({ queryKey: [CARD_ATTENTION_QUERY] });
  };

  const switchMutation = useMutation({
    mutationFn: ({ card, freeze }: { card: CardItem; freeze: boolean }) => {
      const params = {
        account: card.account,
        idempotency_key: crypto.randomUUID(),
        card_id: card.card_id,
      };
      return freeze ? freezeCard(params) : unfreezeCard(params);
    },
    onSuccess: (run, variables) =>
      afterWrite({
        runId: run.runId,
        title: variables.freeze ? "已提交冻结请求" : "已提交解冻请求",
      }),
    onError: (err) => setActionError(err),
  });

  const rows = query.data?.cards ?? [];
  const accounts = query.data?.accounts ?? [];

  const columns: DataTableColumn<CardItem>[] = [
    {
      id: "account",
      header: "账号",
      cell: (row) => <Badge tone="neutral">{row.account}</Badge>,
      value: (row) => row.account,
    },
    {
      id: "mask",
      header: "卡号（掩码）",
      cell: (row) => <span className="font-mono">{row.mask || "—"}</span>,
      value: (row) => row.mask,
      primary: true,
    },
    {
      id: "holder",
      header: "持卡人",
      cell: (row) => row.holder_name || "—",
      value: (row) => row.holder_name,
    },
    {
      id: "status",
      header: "状态",
      cell: (row) => <Badge tone={cardStatusTone(row.status)}>{row.status}</Badge>,
      value: (row) => row.status,
    },
    {
      id: "balance",
      header: "余额",
      cell: (row) => formatMinorUnits(row.balance_minor, row.currency),
      value: (row) => row.balance_minor,
      numeric: true,
    },
    {
      id: "owner",
      header: "用途",
      cell: (row) => row.owner_ref || "—",
      value: (row) => row.owner_ref ?? "",
    },
    {
      id: "freshness",
      header: "数据新鲜度",
      cell: (row) => <CardFreshnessBadge freshness={row.freshness} />,
      value: (row) => (row.freshness.stale ? 1 : 0),
    },
    {
      id: "actions",
      header: "操作",
      // 只有按钮的列不给 value：它不该参与排序与搜索
      cell: (row) => (
        <div className="flex flex-wrap gap-2">
          <RevealDialog card={row} />
          <Button
            variant="secondary"
            size="sm"
            disabled={switchMutation.isPending}
            onClick={() =>
              switchMutation.mutate({ card: row, freeze: row.status !== "frozen" })
            }
          >
            {row.status === "frozen" ? "解冻" : "冻结"}
          </Button>
        </div>
      ),
    },
  ];

  return (
    <section className="flex flex-col gap-3">
      <AttentionBanner items={attentionQuery.data ?? []} />

      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="text-sm text-fg-muted">
          卡片数据由后台作业周期同步，不是实时读取——每行的新鲜度徽章说明它有多新。
          「账号」是内部资金来源，与「用途」那一列（面向使用方的归属）是两回事。
        </p>
        <IssueCardDialog accounts={accounts} onIssued={afterWrite} />
      </div>

      {result ? <ActionResultNote result={result} /> : null}
      {actionError ? <ActionErrorNote error={actionError} /> : null}

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <DataTableV2
          caption="Infini 卡片清单：掩码卡号、持卡人、状态、余额与数据新鲜度"
          rows={rows}
          columns={columns}
          // 键必须带账号：卡 id 只在自己账号内唯一（投影表的唯一键是
          // (environment, account, upstream_card_id)），只用 card_id 会让
          // 两个账号的同名卡撞 React key，把一行渲染两遍。
          rowKey={(row) => `${row.account}/${row.card_id}`}
          emptyState={
            <p className="text-sm text-fg-muted">
              还没有卡片。点击「开卡」创建第一张，或等待同步作业拉取已有的卡。
            </p>
          }
        />
      </ApiStateView>
    </section>
  );
}
