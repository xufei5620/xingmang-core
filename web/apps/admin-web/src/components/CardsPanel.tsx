import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import {
  freezeCard,
  listCardBalances,
  deleteCard,
  listCardChallenges,
  issueCard,
  listCardOperationsNeedingAttention,
  listCards,
  unfreezeCard,
  type CardFreshness,
  type CardChallenge,
  type CardItem,
  type CardOperationItem,
} from "../api/cards";
import { cardStatusLabel, cardStatusTone, isCardLocked } from "../lib/cardStatus";
import { formatMinorUnits } from "../lib/money";
import { CardDetailDialog } from "./CardDetailDialog";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

const CARDS_QUERY = "cards";
const CARD_BALANCES_QUERY = "card-balances";
const CARD_CHALLENGES_QUERY = "card-challenges";
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

/** 卡号单元格：有明文显示明文并可一键复制，没有则显示掩码。
 *
 *  明文由后端按 card.reveal 权限决定回不回——只有 card.read 的人这里
 *  看到的就是掩码，前端不需要（也不应该）自己判断。 */
function CardNumberCell({ card }: { card: CardItem }) {
  const [copied, setCopied] = useState(false);
  const shown = card.pan ?? card.mask;

  if (!card.pan) {
    return <span className="font-mono">{shown || "—"}</span>;
  }
  return (
    <span className="flex items-center gap-2">
      <span className="font-mono">{shown}</span>
      <Button
        size="sm"
        variant="secondary"
        onClick={() => {
          void navigator.clipboard?.writeText(card.pan ?? "");
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1500);
        }}
      >
        {copied ? "已复制" : "复制"}
      </Button>
    </span>
  );
}

/** 绑定账号单元格：标识 + 类型。类型显式显示，不靠长相猜——
 *  有些服务的账号是用户名或手机号。 */
function BoundAccountCell({ card }: { card: CardItem }) {
  if (!card.bound_account) return <span className="text-fg-muted">—</span>;
  return (
    <span className="flex flex-col">
      <span>{card.bound_account}</span>
      {card.bound_account_kind ? (
        <span className="text-xs text-fg-muted">{card.bound_account_kind}</span>
      ) : null}
    </span>
  );
}

/** 续费单元格：日期 + 风险徽章。
 *
 *  风险等级由**服务端**判定（none / soon / unfunded / overdue），前端只翻译成
 *  文案与语气——两处各算一遍迟早分叉，而分叉的那一边会把「续不上」显示成正常。
 *
 *  unfunded 是这一列真正的用处：续费日快到了而卡上没钱，订阅会直接掉，
 *  而这种事通常没人提前发现。 */
function RenewalCell({ card }: { card: CardItem }) {
  if (!card.next_renewal_on) return <span className="text-fg-muted">—</span>;

  const risk = card.renewal_risk;
  const badge =
    risk === "unfunded" ? (
      <Badge tone="danger" title="续费日临近且卡上没钱——订阅会掉">
        余额不足
      </Badge>
    ) : risk === "overdue" ? (
      <Badge tone="warning" title="续费日已过：可能已经扣过（该更新日期），也可能没扣上（该查）">
        已过期
      </Badge>
    ) : risk === "soon" ? (
      <Badge tone="info" title="七天内续费">
        即将续费
      </Badge>
    ) : null;

  return (
    <span className="flex flex-col gap-1">
      <span className="font-mono text-sm">{card.next_renewal_on}</span>
      {badge}
    </span>
  );
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

/** 开卡表单。
 *
 *  幂等键在**打开对话框时生成一次**，整个表单生命周期内不变：
 *  同一次提交的重试必须带同一个键，换一个键等于告诉后端「这是另一次开卡」，
 *  而上游没有幂等能力。 */
function IssueCardDialog({
  accounts,
  memberEmails,
  onIssued,
}: {
  accounts: string[];
  memberEmails: string[];
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
      description="开卡会真实扣款。金额上限按账号配置，未配置会被直接拒绝；当前口径是不设限，实际的顶是 Infini 账户余额。"
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
        <FormField
          label="企业成员邮箱"
          htmlFor={`${formId}-email`}
          hint={
            memberEmails.length > 0
              ? "可从历史用过的成员里选，也可以直接输入新的。"
              : "上游没有成员列表接口，这里的候选来自平台开过的卡——第一次开卡时是空的。"
          }
        >
          <Input
            id={`${formId}-email`}
            type="email"
            list={`${formId}-emails`}
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
          />
        </FormField>
        {/* 用 datalist 而不是 select：上游没有成员列表接口，候选只是
            「以前用过的」，不该把没用过的新成员挡在外面。 */}
        <datalist id={`${formId}-emails`}>
          {memberEmails.map((m) => (
            <option key={m} value={m} />
          ))}
        </datalist>
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
        title: variables.freeze ? "已提交锁定请求" : "已提交解锁请求",
      }),
    onError: (err) => setActionError(err),
  });

  // 待验证的 3DS 挑战：轮询得比卡片列表勤，因为验证码通常只有几分钟有效，
  // 拿到时已经过期就等于没拿到。
  const challengeQuery = useQuery({
    queryKey: [CARD_CHALLENGES_QUERY],
    queryFn: ({ signal }) => listCardChallenges({ signal }),
    refetchInterval: 30_000,
  });
  const challengesByCard = new Map(
    (challengeQuery.data ?? []).map((c) => [`${c.account}/${c.card_id}`, c]),
  );

  const rows = query.data?.cards ?? [];
  const accounts = query.data?.accounts ?? [];
  const memberEmails = query.data?.memberEmails ?? [];

  const columns: DataTableColumn<CardItem>[] = [
    {
      id: "account",
      header: "账号",
      cell: (row) => <Badge tone="neutral">{row.account}</Badge>,
      value: (row) => row.account,
    },
    {
      id: "pan",
      header: "卡号",
      // 有明文就显示明文（后端按 card.reveal 权限决定回不回），
      // 没有就退回掩码——前端不做「本地隐藏」那种假控制。
      cell: (row) => <CardNumberCell card={row} />,
      value: (row) => row.pan ?? row.mask,
      primary: true,
    },
    {
      id: "challenge",
      header: "验证码",
      // 单独一列而不是挂在状态下面：在线支付时人要的就是这个数字，
      // 它得能被一眼扫到、能被复制。没有待验证时留空而不是「—」，
      // 让有码的那几行在满屏里跳出来。
      cell: (row) => <ChallengeCell challenge={challengesByCard.get(`${row.account}/${row.card_id}`)} />,
      value: (row) => challengesByCard.get(`${row.account}/${row.card_id}`)?.code ?? "",
    },
    {
      id: "cvv",
      header: "CVV",
      // 与卡号同一逻辑：明文由后端按 card.reveal 权限决定回不回，
      // 前端只显示它拿到的东西。
      //
      // 原先默认隐藏，理由是「摊在列表上等于长期暴露在任何一次截屏里」。
      // 产品负责人 2026-09-05 要求默认显示，而那条理由本来也站不住：
      // 卡号整串就在左边显示着，藏起 CVV 只是个半拉子措施——真要防截屏
      // 泄露，该藏的是卡号。既然卡面明文已经按 card.reveal 权限回到了
      // 这一页，就让要用它的人一眼看全，而不是每次去勾三个框。
      cell: (row) => <span className="font-mono">{row.cvv || "—"}</span>,
      value: (row) => row.cvv ?? "",
    },
    {
      id: "expiry",
      header: "有效期",
      // 上游字段名叫 expiration_mmyy，但实测返回的是 MM/YYYY（11/2031），
      // 与文档示例的 1228 不同。原样显示，不解析、不重排——
      // 解析一个格式尚未定论的字段，只会在上游改回去时静默显示错。
      cell: (row) => <span className="font-mono">{row.expiry_mmyy || "—"}</span>,
      value: (row) => row.expiry_mmyy ?? "",
    },
    {
      id: "issue_fee",
      header: "开卡费",
      cell: (row) => (row.issue_fee ? `${row.issue_fee} ${row.currency}` : "—"),
      value: (row) => row.issue_fee ?? "",
      defaultHidden: true,
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
      cell: (row) => <Badge tone={cardStatusTone(row.status)}>{cardStatusLabel(row.status)}</Badge>,
      value: (row) => cardStatusLabel(row.status),
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
      id: "bound",
      header: "绑定账号",
      cell: (row) => <BoundAccountCell card={row} />,
      value: (row) => row.bound_account ?? "",
    },
    {
      id: "service",
      header: "订阅服务",
      cell: (row) => row.service_name || "—",
      value: (row) => row.service_name ?? "",
    },
    {
      id: "renewal",
      header: "下次续费",
      cell: (row) => <RenewalCell card={row} />,
      // 排序按日期文本即可（YYYY-MM-DD 字典序 = 时间序）；
      // 没登记的排最后，用一个不会出现的大值。
      value: (row) => row.next_renewal_on || "9999-12-31",
    },
    {
      id: "issued",
      // 「时间」而不是「日期」：显示到秒（formatUtcTimestamp 给的是
      // YYYY-MM-DD HH:mm:ss UTC）。叫「日期」会让人以为只精确到天，
      // 而排查一次开卡时最要紧的恰恰是分秒——那是和审计事件对得上的东西。
      header: "开卡时间",
      cell: (row) => (row.issued_at ? formatUtcTimestamp(row.issued_at) : "—"),
      value: (row) => row.issued_at ?? "",
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
          <CardDetailDialog card={row} onWrite={afterWrite} />
          {/* 对齐上游后台的动作列：充值 / 赎回 / 锁定|解锁。
              此前充值与赎回只藏在详情弹窗里，要多点两层才能找到。 */}
          <CardDetailDialog card={row} onWrite={afterWrite} initialTab="topup" triggerLabel="充值" />
          <CardDetailDialog card={row} onWrite={afterWrite} initialTab="redeem" triggerLabel="赎回" />
          <Button
            variant="secondary"
            size="sm"
            disabled={switchMutation.isPending}
            onClick={() =>
              switchMutation.mutate({ card: row, freeze: !isCardLocked(row.status) })
            }
          >
            {isCardLocked(row.status) ? "解锁" : "锁定"}
          </Button>
          <DeleteCardButton card={row} onDone={afterWrite} />
        </div>
      ),
    },
  ];

  return (
    <section className="flex flex-col gap-3">
      <AttentionBanner items={attentionQuery.data ?? []} />
      <AccountBalancesStrip />

      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="text-sm text-fg-muted">
          卡片数据由后台作业周期同步，不是实时读取——每行的新鲜度徽章说明它有多新。
          「账号」是内部资金来源，与「用途」那一列（面向使用方的归属）是两回事。
        </p>
        <IssueCardDialog accounts={accounts} memberEmails={memberEmails} onIssued={afterWrite} />
      </div>

      {result ? <ActionResultNote result={result} /> : null}
      {actionError ? <ActionErrorNote error={actionError} /> : null}

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <DataTableV2
          caption="Infini 卡片清单：账号、卡号、持卡人、状态、余额与数据新鲜度"
          searchable
          filters={[
            { columnId: "account", label: "账号", options: accounts },
            {
              columnId: "status",
              label: "状态",
              // 取值来自实际数据而不是写死的枚举：上游加新状态时不该被漏掉。
              // 用显示文案而不是原值，好与徽章上看到的一致。
              options: Array.from(new Set(rows.map((r) => cardStatusLabel(r.status)))).filter(Boolean),
            },
          ]}
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

/** 各账号资金池的可用余额。
 *
 *  它回答的是「还能开多少张卡」——卡上的余额是已经花出去的钱，资金池才是
 *  没花的。两者混在一起看会得出完全相反的结论。
 *
 *  单独一个查询而不是并进卡片列表：这是实时上游调用，比列表慢；而且一个
 *  账号取不到不该影响另一个（凭据、权限、IP 白名单都是各自独立的）。
 */
function AccountBalancesStrip() {
  const query = useQuery({
    queryKey: [CARD_BALANCES_QUERY],
    queryFn: ({ signal }) => listCardBalances({ signal }),
    // 余额不随卡片列表轮询：它慢，而且没人盯着看的时候不需要新。
    staleTime: 60_000,
  });

  if (query.isPending || !query.data?.length) return null;

  return (
    <div className="flex flex-wrap gap-3">
      {query.data.map((b) => (
        <div key={b.account} className="rounded-lg border border-edge bg-surface px-3 py-2">
          <div className="flex items-center gap-2">
            <Badge tone="neutral">{b.account}</Badge>
            <span className="text-xs text-fg-muted">资金池可用</span>
          </div>
          {b.error ? (
            // 取不到显示成「—」而不是 0：把取不到显示成零余额，
            // 会让人以为钱花光了。
            <div className="mt-1 text-sm text-fg-muted" title={`读取失败：${b.error}`}>
              — <span className="text-xs">（读取失败）</span>
            </div>
          ) : (
            <div className="mt-1 flex gap-3 font-mono text-sm">
              <span>{b.usdt || "0"} USDT</span>
              <span>{b.usdc || "0"} USDC</span>
              <span>{b.usd || "0"} USD</span>
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

/** 验证码单元格。
 *
 *  持卡人在线支付时上游会要一次验证码。把它摆在这里，用卡的人就不用去翻
 *  邮件或 Infini App。
 *
 *  **验证码可能不在回调里**：官方文档的示例带 challenge 字段，但生产收到的
 *  真实事件没有。所以这里分两种显示——有码就显示码，没码就只提示「有一笔
 *  待验证」。按文档假定它一定存在，会做出一个永远空白的栏位。
 */
function ChallengeCell({ challenge }: { challenge?: CardChallenge }) {
  // 没有待验证时留空：满屏的「—」会把真正有码的那几行淹掉。
  if (!challenge) return null;

  if (!challenge.code) {
    return (
      <Badge tone="warning" title="上游要求验证，但回调里没有给出验证码——请到 Infini 后台或邮件里查看">
        待验证
      </Badge>
    );
  }
  return (
    <span className="flex items-center gap-2">
      <span className="font-mono text-base font-semibold" title="在线支付验证码，仅数分钟内有效">
        {challenge.code}
      </span>
      <Button
        size="sm"
        variant="secondary"
        onClick={() => void navigator.clipboard?.writeText(challenge.code ?? "")}
      >
        复制
      </Button>
    </span>
  );
}

/** 关停一张卡。
 *
 *  **不可逆**，所以走两步：第一次点击只把按钮变成「确认关停」，再点一次才
 *  真的发出去。一个不可逆的动作不该和「详情」「充值」一样一点就走——
 *  它们在同一行、按钮长得一样，误点的代价却完全不同。
 *
 *  幂等键在第一次点击时生成并保持不变：确认阶段的重试必须带同一个键，
 *  换一个键等于告诉后端「这是另一次关停」。
 */
function DeleteCardButton({
  card,
  onDone,
}: {
  card: CardItem;
  onDone: (result: ActionResult) => void;
}) {
  const [armed, setArmed] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: (key: string) =>
      deleteCard({ account: card.account, idempotency_key: key, card_id: card.card_id }),
    onSuccess: (run) => {
      onDone({ runId: run.runId, title: "已提交关停请求" });
      setArmed(null);
    },
    onError: () => setArmed(null),
  });

  // 已经在关停流程里的卡不再提供这个按钮：再点一次没有意义。
  if (card.status === "pending_delete" || card.status === "deleted") return null;

  if (!armed) {
    return (
      <Button
        variant="secondary"
        size="sm"
        onClick={() => setArmed(crypto.randomUUID())}
        title="关停不可逆：卡会结清余额后删除，无法恢复"
      >
        关停
      </Button>
    );
  }
  return (
    <Button
      variant="danger"
      size="sm"
      disabled={mutation.isPending}
      onClick={() => mutation.mutate(armed)}
      title="再点一次将真的关停这张卡"
    >
      {mutation.isPending ? "关停中…" : "确认关停"}
    </Button>
  );
}
