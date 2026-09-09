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
import { Link } from "react-router";
import { ActionErrorNote } from "./ActionErrorNote";
import { actionResultOf, ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";
import { ApprovalReasonField, isApprovalReasonUsable } from "./ApprovalReasonField";

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
export function CardFreshnessBadge({ freshness }: { freshness: CardFreshness }) {
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




/** 待人工处置的横幅。
 *
 *  每一条都意味着「有一笔花钱操作，我们至今不知道它到底成没成」。
 *  这是整个页面最该被看见的东西，所以排在表格之前而不是折叠在角落。 */
/** 待人工处置的操作红条。**自己取数据**，页面直接放上去即可。
 *
 *  不从外面穿 items 进来：它有自己的轮询节奏（比卡片列表勤），而把它的
 *  查询挂在调用方身上，就意味着每个想显示它的页面都要重复接一次线。 */
export function AttentionBanner() {
  const query = useQuery({
    queryKey: [CARD_ATTENTION_QUERY],
    queryFn: ({ signal }) => listCardOperationsNeedingAttention({ signal }),
    refetchInterval: 60_000,
  });
  return <AttentionBannerView items={query.data ?? []} />;
}

function AttentionBannerView({ items }: { items: CardOperationItem[] }) {
  if (items.length === 0) return null;

  return (
    <section className="rounded-md border border-danger bg-danger/10 p-3" role="alert">
      <h3 className="text-sm font-medium text-danger">
        {items.length} 笔操作需要人工确认
      </h3>
      <p className="mt-1 text-xs text-fg-muted">
        上游没有幂等能力，这些操作超时后无法判定是否已经生效。请到 Infini
        后台核对后再决定——<strong>在确认之前不要重试</strong>，重试可能重复扣钱。
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
export function IssueCardDialog({
  accounts,
  memberEmails,
  onIssued,
}: {
  accounts: string[];
  memberEmails: string[];
  /** 不传时只刷新卡片列表——页面上没有承接结果的地方时，
   *  硬要求调用方传一个空函数只是噪音。 */
  onIssued?: (result: ActionResult) => void;
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
  const [reason, setReason] = useState("");
  const [reasonTouched, setReasonTouched] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  // 账号清单是异步到达的（与卡片列表同一个查询）。初值只在首次渲染取一次
  // 会永远停在空，于是表单提交一个空账号——后端会拒，但表单本身是坏的。
  // 每次渲染校正：选中的账号必须在当前清单里，否则回落到第一个。
  const effectiveAccount = accounts.includes(account) ? account : (accounts[0] ?? "");

  const queryClient = useQueryClient();

  const mutation = useMutation({
    mutationFn: () =>
      issueCard(
        {
          account: effectiveAccount,
          idempotency_key: idempotencyKey,
          product_id: Number(productId),
          top_up_amount: amount.trim(),
          token_type: tokenType,
          user_email: email.trim(),
          holder_name: holder.trim(),
          ...(ownerRef.trim() ? { owner_ref: ownerRef.trim() } : {}),
        },
        reason.trim(),
      ),
    onSuccess: (outcome) => {
      // 开卡是 L2：多半落成审批单而不是当场开出来。两种结局的措辞必须分开
      // ——把「已受理为审批单」说成「已提交开卡请求」，人会以为卡在路上了。
      onIssued?.(
        actionResultOf(outcome, {
          executed: "已提交开卡请求",
          approvalPending: "开卡已提交审批，卡还没有开",
        }),
      );
      void queryClient.invalidateQueries({ queryKey: [CARDS_QUERY] });
      setOpen(false);
      // 下一次开卡是另一笔业务，必须换一个幂等键
      setIdempotencyKey(crypto.randomUUID());
      setAmount("");
      setEmail("");
      setHolder("");
      setReason("");
      setReasonTouched(false);
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
          setReasonTouched(true);
          if (!isApprovalReasonUsable(reason)) return;
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

        <ApprovalReasonField
          id={`${formId}-reason`}
          value={reason}
          onChange={setReason}
          subject="开卡"
          touched={reasonTouched}
        />

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

/** 各账号的资金池余额，**同时也是账号选择器**。
 *
 *  产品负责人 2026-09-05：「卡片根据账户分开，点击不同的账户显示账户下的
 *  卡片」。发现遍历上线后卡数从 2 张涨到十几张，两个账号的卡混在一列里，
 *  而「这张卡的钱从哪个账号出」恰恰是操作前要先确定的事。
 *
 *  把选择器做在余额条上而不是另加一排页签：这两件事本来就是同一个问题的
 *  两面——看某个账号的卡时，最想同时知道的就是那个账号还剩多少钱。
 *
 *  再点一次选中的账号 = 取消筛选。不做「全部」按钮：多一个按钮就多一处要
 *  解释「全部和不选有什么区别」的地方，而它们本来就是一回事。 */
export function AccountBalancesStrip({
  selected = "",
  onSelect,
}: {
  selected?: string;
  onSelect?: (account: string) => void;
} = {}) {
  const query = useQuery({
    queryKey: [CARD_BALANCES_QUERY],
    queryFn: ({ signal }) => listCardBalances({ signal }),
    // 余额不随卡片列表轮询：它慢，而且没人盯着看的时候不需要新。
    staleTime: 60_000,
  });

  if (query.isPending || !query.data?.length) return null;

  const clickableStrip = typeof onSelect === "function";

  return (
    <div className="flex flex-wrap items-stretch gap-3">
      {/* 「全部」显式成一个按钮。
       *
       *  我原先的想法是「再点一次选中的账号就取消，多一个按钮就多一处要
       *  解释的地方」——那条站不住：**取消筛选这件事本身得有个看得见的
       *  入口**。只靠「再点一次」是一条藏起来的规则，人得先猜到它存在。
       *  产品负责人 2026-09-05 直接要求加上。 */}
      {clickableStrip ? (
        <button
          type="button"
          onClick={() => onSelect("")}
          aria-pressed={selected === ""}
          className={`rounded-lg border px-3 py-2 text-left text-sm font-medium hover:bg-surface-muted focus-visible:outline-2 focus-visible:outline-accent ${
            selected === "" ? "border-accent bg-accent-soft" : "border-edge bg-surface"
          }`}
        >
          全部
        </button>
      ) : null}
      {query.data.map((b) => {
        const active = selected === b.account;
        const clickable = typeof onSelect === "function";
        const Tag = clickable ? "button" : "div";
        return (
        <Tag
          key={b.account}
          {...(clickable
            ? {
                type: "button" as const,
                onClick: () => onSelect(active ? "" : b.account),
                "aria-pressed": active,
                "aria-label": `只看账号 ${b.account} 的卡片`,
              }
            : {})}
          className={`rounded-lg border px-3 py-2 text-left ${
            active ? "border-accent bg-accent-soft" : "border-edge bg-surface"
          } ${clickable ? "hover:bg-surface-muted focus-visible:outline-2 focus-visible:outline-accent" : ""}`}
        >
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
        </Tag>
        );
      })}
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
export function ChallengeCell({ challenge }: { challenge?: CardChallenge }) {
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

