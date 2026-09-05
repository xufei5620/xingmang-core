import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import {
  executeWithdraw,
  listWithdrawAddresses,
  listWithdrawLimits,
  listWithdrawals,
  registerWithdrawAddress,
  setWithdrawLimits,
  type WithdrawAddress,
  type WithdrawItem,
  type WithdrawLimit,
} from "../api/withdraw";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

const WITHDRAW_ADDRESSES_QUERY = "withdraw-addresses";
const WITHDRAWALS_QUERY = "withdrawals";
const WITHDRAW_LIMITS_QUERY = "withdraw-limits";

/** 提现代币。与后端 Action 契约一致。 */
const TOKEN_TYPE_OPTIONS = [
  { value: "USDT", label: "USDT" },
  { value: "USDC", label: "USDC" },
];

/** 支持的链。
 *
 *  写死一张表而不是让人自由输入：链名拼错的后果是钱转到另一条链上，
 *  而那种钱找不回来。表要跟着上游的 chain 取值走，加链是一次代码改动
 *  ——这正是想要的摩擦。 */
const CHAIN_OPTIONS = [
  { value: "TRON", label: "TRON (TRC20)" },
  { value: "ETH", label: "Ethereum (ERC20)" },
  { value: "BSC", label: "BNB Smart Chain (BEP20)" },
  { value: "POLYGON", label: "Polygon" },
];

/** 区块浏览器。给的是**交易哈希**页，让人自己核对钱到没到。
 *
 *  「平台说转成功了」和「链上确实有这笔」是两件事，后者才是依据。
 *  没有对应链时不给链接（返回 null），而不是拼一个会 404 的地址。 */
function explorerUrl(chain: string, txHash: string): string | null {
  const base: Record<string, string> = {
    TRON: "https://tronscan.org/#/transaction/",
    ETH: "https://etherscan.io/tx/",
    BSC: "https://bscscan.com/tx/",
    POLYGON: "https://polygonscan.com/tx/",
  };
  const prefix = base[chain.toUpperCase()];
  return prefix ? prefix + txHash : null;
}

/** 提现状态的中文文案。
 *
 *  未知取值原样显示并标注，不归到任何已知分类里——同 cardStatus 的纪律：
 *  静默归类会让第一个没见过的状态在最需要被看见的时候消失。 */
function withdrawStatusLabel(status: string): string {
  const map: Record<string, string> = {
    pending: "已提交",
    processing: "处理中",
    completed: "已完成",
    failed: "已失败",
    rejected: "被拒绝",
  };
  return map[status.toLowerCase()] ?? `${status}（未知状态）`;
}

function withdrawStatusTone(status: string): "success" | "warning" | "danger" | "neutral" {
  switch (status.toLowerCase()) {
    case "completed":
      return "success";
    case "failed":
    case "rejected":
      return "danger";
    case "pending":
    case "processing":
      return "warning";
    default:
      return "neutral";
  }
}

/** 提现（XM-CARD6）。
 *
 *  这一整块由 `fund.withdraw` 把守，与卡片的 `card.read` 是**两个权限**：
 *  没有 fund-operator 角色的人拿到 403。
 *
 *  提现是平台里唯一把钱转出平台的动作，链上转账没有撤回。页面这一侧的
 *  护栏有三道：只能从已登记地址里选（不能手输）、链跟着地址走（拼错链名
 *  的钱找不回来）、两步确认（幂等键在第一步生成并保持不变）。 */
export function WithdrawPanel({ accounts }: { accounts: string[] }) {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);

  const addressesQuery = useQuery({
    queryKey: [WITHDRAW_ADDRESSES_QUERY],
    queryFn: ({ signal }) => listWithdrawAddresses({ signal }),
    staleTime: 60_000,
  });
  const historyQuery = useQuery({
    queryKey: [WITHDRAWALS_QUERY],
    queryFn: ({ signal }) => listWithdrawals({ signal }),
    // 提现是低频动作，但发起后的几分钟里状态会变（pending → completed）。
    // 30 秒够看，也不至于把一个没人盯着的页面变成轮询器。
    refetchInterval: 30_000,
  });

  const addresses = addressesQuery.data ?? [];
  const rows = historyQuery.data ?? [];

  function afterWrite(r: ActionResult) {
    setResult(r);
    void queryClient.invalidateQueries({ queryKey: [WITHDRAWALS_QUERY] });
    void queryClient.invalidateQueries({ queryKey: [WITHDRAW_ADDRESSES_QUERY] });
    void queryClient.invalidateQueries({ queryKey: [WITHDRAW_LIMITS_QUERY] });
  }

  const columns: DataTableColumn<WithdrawItem>[] = [
    { id: "account", header: "账号", cell: (row) => row.account },
    { id: "chain", header: "链", cell: (row) => `${row.chain} / ${row.token_type}` },
    {
      id: "amount",
      header: "金额",
      cell: (row) => (
        <span className="tabular-nums">
          {row.actual_amount && row.actual_amount !== row.amount ? (
            <>
              {row.actual_amount}
              {/* 申请额与到账额不同是常态（手续费），两个都显示出来，
                  免得有人以为少到了一笔要去追。 */}
              <span className="text-fg-muted"> （申请 {row.amount}）</span>
            </>
          ) : (
            row.amount
          )}
        </span>
      ),
    },
    {
      id: "fee",
      header: "手续费",
      cell: (row) => (row.gas_fee ? `${row.gas_fee} ${row.gas_fee_currency ?? ""}`.trim() : "—"),
    },
    {
      id: "address",
      header: "到账地址",
      cell: (row) => (
        // 地址很长，截断显示但 title 给全值——核对时要能看到完整的。
        <span className="font-mono text-xs" title={row.address}>
          {row.address.length > 16
            ? `${row.address.slice(0, 8)}…${row.address.slice(-6)}`
            : row.address}
        </span>
      ),
    },
    {
      id: "status",
      header: "状态",
      cell: (row) => (
        <Badge tone={withdrawStatusTone(row.status)}>{withdrawStatusLabel(row.status)}</Badge>
      ),
    },
    {
      id: "tx",
      header: "链上哈希",
      cell: (row) => {
        if (!row.tx_hash) return <span className="text-fg-muted">—</span>;
        const href = explorerUrl(row.chain, row.tx_hash);
        const short = `${row.tx_hash.slice(0, 10)}…`;
        if (!href) return <span className="font-mono text-xs">{short}</span>;
        return (
          <a
            className="font-mono text-xs underline"
            href={href}
            target="_blank"
            rel="noreferrer noopener"
            title={`到区块浏览器核对 ${row.tx_hash}`}
          >
            {short}
          </a>
        );
      },
    },
    {
      id: "started_at",
      header: "发起时间",
      cell: (row) => (row.started_at ? formatUtcTimestamp(row.started_at) : "—"),
    },
  ];

  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="text-sm text-fg-muted">
          提现把钱转出平台，链上转账没有撤回。只能转到已登记的地址，
          且额度与卡片额度分开配置（不接受「不限」）。每一笔都留审计。
        </p>
        <RegisterAddressDialog accounts={accounts} onDone={afterWrite} />
      </div>

      <WithdrawLimitsStrip accounts={accounts} onChanged={afterWrite} />

      <ApiStateView
        isPending={addressesQuery.isPending}
        error={addressesQuery.error}
        onRetry={() => void addressesQuery.refetch()}
      >
        {addresses.length === 0 ? (
          <p className="text-sm text-fg-muted">
            还没有登记任何提现地址。先点「登记地址」——提现只能转到已登记的地址，
            这是误操作与后台滥用之间唯一的那道闸。
          </p>
        ) : (
          <WithdrawForm addresses={addresses} onDone={afterWrite} />
        )}
      </ApiStateView>

      {result ? <ActionResultNote result={result} /> : null}

      <ApiStateView
        isPending={historyQuery.isPending}
        error={historyQuery.error}
        onRetry={() => void historyQuery.refetch()}
      >
        <DataTableV2
          caption="提现记录：账号、链、金额、手续费、到账地址、状态与链上哈希"
          searchable
          rows={rows}
          columns={columns}
          rowKey={(row) => row.request_id}
          emptyState={<p className="text-sm text-fg-muted">还没有提现记录。</p>}
        />
      </ApiStateView>
    </section>
  );
}

/** 各账号的提现额度，以及就地修改的入口。
 *
 *  额度存在库里、在这里改（产品负责人 2026-09-05 决定），不再是服务器上的
 *  环境变量：一个要登服务器改文件再重启才能动的数字，实际上没人会去动——
 *  它会永远停在第一次拍脑袋定的那个值上，然后在真正要用的时候挡住正事。
 *
 *  换个地方存不等于放松。改额度的权限是 `fund.limit.manage`（admin），
 *  提现是 `fund.withdraw`（fund-operator）——**两把钥匙**：拿到任一把都
 *  搬不空资金池。没有 fund.limit.manage 的人点保存会拿到 403，
 *  按钮不隐藏是刻意的：藏起来只会让人以为功能坏了。 */
function WithdrawLimitsStrip({
  accounts,
  onChanged,
}: {
  accounts: string[];
  onChanged: (result: ActionResult) => void;
}) {
  const query = useQuery({
    queryKey: [WITHDRAW_LIMITS_QUERY],
    queryFn: ({ signal }) => listWithdrawLimits({ signal }),
    staleTime: 60_000,
  });

  const byAccount = new Map((query.data ?? []).map((l) => [l.account, l]));

  return (
    <ApiStateView
      isPending={query.isPending}
      error={query.error}
      onRetry={() => void query.refetch()}
      compact
    >
      <div className="flex flex-wrap gap-2">
        {accounts.map((account) => (
          <WithdrawLimitCard
            key={account}
            account={account}
            limit={byAccount.get(account)}
            onChanged={onChanged}
          />
        ))}
      </div>
    </ApiStateView>
  );
}

function WithdrawLimitCard({
  account,
  limit,
  onChanged,
}: {
  account: string;
  limit: WithdrawLimit | undefined;
  onChanged: (result: ActionResult) => void;
}) {
  return (
    <div className="border-edge flex min-w-56 flex-col gap-1 rounded-md border p-2">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium">{account}</span>
        <EditWithdrawLimitDialog account={account} limit={limit} onChanged={onChanged} />
      </div>
      {limit ? (
        <>
          <span className="text-fg-muted text-xs tabular-nums">
            单笔 {limit.per_operation} · 单日 {limit.per_day}
          </span>
          {limit.updated_by ? (
            // 「这个上限是谁定的」就放在额度旁边：审计里也有，但为一个数字
            // 去翻审计太贵，而这恰恰是看到一个上限时第一个会冒出来的问题。
            <span className="text-fg-muted text-xs">
              由 {limit.updated_by} 设定
              {limit.updated_at ? ` · ${formatUtcTimestamp(limit.updated_at)}` : ""}
            </span>
          ) : null}
        </>
      ) : (
        // 「未设置」而不是 0：前者是还没人管过这个账号，后者是有人刻意
        // 关掉了它的提现，两者的下一步动作完全不同。
        <span className="text-fg-muted text-xs">未设置——该账号目前不能提现</span>
      )}
    </div>
  );
}

function EditWithdrawLimitDialog({
  account,
  limit,
  onChanged,
}: {
  account: string;
  limit: WithdrawLimit | undefined;
  onChanged: (result: ActionResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const [perOperation, setPerOperation] = useState(limit?.per_operation ?? "");
  const [perDay, setPerDay] = useState(limit?.per_day ?? "");
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  const mutation = useMutation({
    mutationFn: () =>
      setWithdrawLimits({
        account,
        per_operation: perOperation.trim(),
        per_day: perDay.trim(),
      }),
    onSuccess: (run) => {
      onChanged({ runId: run.runId, title: `已更新 ${account} 的提现额度` });
      setOpen(false);
      setError(null);
    },
    onError: (e) => setError(e),
  });

  const canSave = perOperation.trim() !== "" && perDay.trim() !== "" && !mutation.isPending;

  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      title={`提现额度：${account}`}
      description="上限对提现生效，与卡片额度是两套。改动会进审计，记录下是谁在什么时候调的。"
      trigger={
        <Button variant="ghost" size="sm" aria-label={`改额度 ${account}`}>
          改额度
        </Button>
      }
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
          label="单笔上限"
          htmlFor={`${formId}-per-operation`}
          hint="十进制文本，如 500。单位是代币本身（USDT/USDC），不做汇率换算。不接受「不限」——把余额搬空正是提现的目的，余额不构成上限。"
        >
          <Input
            id={`${formId}-per-operation`}
            aria-label="单笔上限"
            value={perOperation}
            onChange={(e) => setPerOperation(e.target.value)}
            required
          />
        </FormField>
        <FormField
          label="单日上限"
          htmlFor={`${formId}-per-day`}
          hint="当天累计。**未收敛的那几笔也算在内**——它们可能真的已经转出去了。"
        >
          <Input
            id={`${formId}-per-day`}
            aria-label="单日上限"
            value={perDay}
            onChange={(e) => setPerDay(e.target.value)}
            required
          />
        </FormField>

        {error ? <ActionErrorNote error={error} /> : null}

        <Button type="submit" disabled={!canSave}>
          {mutation.isPending ? "保存中…" : "保存额度"}
        </Button>
      </form>
    </Dialog>
  );
}

/** 提现表单。
 *
 *  幂等键在**上膛时**（第一次点「提现」）生成，两步之间保持不变：
 *  上游对 request_id 有真幂等，两次点击若换了键就等于告诉双方
 *  「这是另一笔提现」——那正是重复转账的路径。 */
function WithdrawForm({
  addresses,
  onDone,
}: {
  addresses: WithdrawAddress[];
  onDone: (result: ActionResult) => void;
}) {
  const [addressId, setAddressId] = useState("");
  const [tokenType, setTokenType] = useState("USDT");
  const [amount, setAmount] = useState("");
  const [note, setNote] = useState("");
  const [armed, setArmed] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  // 选中的地址必须在当前清单里，否则回落到第一条。清单是异步到达的，
  // 初值只取一次会永远停在空，于是提交一个空 address_id。
  const selected = addresses.find((a) => a.address_id === addressId) ?? addresses[0] ?? null;

  const mutation = useMutation({
    mutationFn: (requestId: string) => {
      if (!selected) throw new Error("没有可用的提现地址");
      return executeWithdraw({
        account: selected.account,
        request_id: requestId,
        // 链与账号都跟着选中的地址走，不让人另选一遍：两处各选一次
        // 就有了不一致的可能，而链不一致的钱找不回来。后端也会再核一遍。
        chain: selected.chain,
        token_type: tokenType,
        amount: amount.trim(),
        address_id: selected.address_id,
        ...(note.trim() ? { note: note.trim() } : {}),
      });
    },
    onSuccess: (run) => {
      onDone({ runId: run.runId, title: "已提交提现请求" });
      setArmed(null);
      setAmount("");
      setNote("");
      setError(null);
    },
    // 失败时**不清 armed**：那是同一笔业务的重试，键必须保持不变。
    onError: (e) => setError(e),
  });

  if (!selected) return null;

  const canSubmit = amount.trim() !== "" && !mutation.isPending;

  return (
    <div className="border-border flex flex-col gap-3 rounded-md border p-3">
      <div className="grid gap-3 md:grid-cols-4">
        <FormField
          label="到账地址"
          htmlFor={`${formId}-address`}
          hint="只能从已登记的地址里选。账号与链跟着它走。"
        >
          <Select
            aria-label="到账地址"
            options={addresses.map((a) => ({
              value: a.address_id,
              label: `${a.label || a.address_id}（${a.account} · ${a.chain}）`,
            }))}
            value={selected.address_id}
            onValueChange={(v) => {
              setAddressId(v);
              // 换地址就卸膛：上膛时看到的是另一条地址，那个确认不该延续。
              setArmed(null);
            }}
          />
        </FormField>
        <FormField label="代币" htmlFor={`${formId}-token`}>
          <Select
            aria-label="代币"
            options={TOKEN_TYPE_OPTIONS}
            value={tokenType}
            onValueChange={(v) => {
              setTokenType(v);
              setArmed(null);
            }}
          />
        </FormField>
        <FormField
          label="金额"
          htmlFor={`${formId}-amount`}
          hint="十进制文本，如 100.00。单位是所选代币本身，不做汇率换算。"
        >
          <Input
            id={`${formId}-amount`}
            aria-label="金额"
            value={amount}
            onChange={(e) => {
              setAmount(e.target.value);
              setArmed(null);
            }}
            required
          />
        </FormField>
        <FormField label="备注" htmlFor={`${formId}-note`} hint="可选，进台账。">
          <Input id={`${formId}-note`} value={note} onChange={(e) => setNote(e.target.value)} />
        </FormField>
      </div>

      <p className="text-fg-muted font-mono text-xs" title={selected.address}>
        将转到 {selected.chain}：{selected.address}
      </p>

      <div className="flex items-center gap-2">
        {armed ? (
          <>
            <Button
              variant="danger"
              size="sm"
              disabled={mutation.isPending}
              onClick={() => mutation.mutate(armed)}
              title="再点一次将真的发起转账，链上转账没有撤回"
            >
              {mutation.isPending ? "提交中…" : `确认提现 ${amount} ${tokenType}`}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setArmed(null)}>
              取消
            </Button>
          </>
        ) : (
          <Button
            variant="secondary"
            size="sm"
            disabled={!canSubmit}
            onClick={() => setArmed(crypto.randomUUID())}
          >
            提现
          </Button>
        )}
      </div>

      {error ? <ActionErrorNote error={error} /> : null}
    </div>
  );
}

/** 登记一条可提现地址。
 *
 *  address_id 由前端生成并充当幂等键：同一条地址重复登记应当得到同一条
 *  记录，而不是两条内容相同、id 不同的白名单项。 */
function RegisterAddressDialog({
  accounts,
  onDone,
}: {
  accounts: string[];
  onDone: (result: ActionResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const [account, setAccount] = useState("");
  const [addressId, setAddressId] = useState(() => crypto.randomUUID());
  const [chain, setChain] = useState("TRON");
  const [address, setAddress] = useState("");
  const [label, setLabel] = useState("");
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  const effectiveAccount = accounts.includes(account) ? account : (accounts[0] ?? "");

  const mutation = useMutation({
    mutationFn: () =>
      registerWithdrawAddress({
        account: effectiveAccount,
        address_id: addressId,
        chain,
        address: address.trim(),
        ...(label.trim() ? { label: label.trim() } : {}),
      }),
    onSuccess: (run) => {
      onDone({ runId: run.runId, title: "已登记提现地址" });
      setOpen(false);
      setAddress("");
      setLabel("");
      // 下一条地址是另一条记录，换一个键。
      setAddressId(crypto.randomUUID());
      setError(null);
    },
    onError: (e) => setError(e),
  });

  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      title="登记提现地址"
      description="登记之后这条地址就成为可选的提现目标。登记错一条地址与提现到错地址是同一个后果，只是晚一步发生。"
      trigger={
        <Button variant="secondary" size="sm">
          登记地址
        </Button>
      }
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
            label="账号"
            htmlFor={`${formId}-account`}
            hint="这条地址只能用于该账号的提现。两个账号的资金是分开的。"
          >
            <Select
              aria-label="账号"
              options={accounts.map((a) => ({ value: a, label: a }))}
              value={effectiveAccount}
              onValueChange={setAccount}
            />
          </FormField>
          <FormField
            label="链"
            htmlFor={`${formId}-chain`}
            hint="从固定表里选，不能手输——链名拼错的钱找不回来。"
          >
            <Select aria-label="链" options={CHAIN_OPTIONS} value={chain} onValueChange={setChain} />
          </FormField>
          <FormField
            label="地址"
            htmlFor={`${formId}-address`}
            hint="粘贴前后请自己核对首尾几位。平台不校验地址格式——那需要按链实现，而校验不全比不校验更容易让人放松。"
          >
            <Input
              id={`${formId}-address`}
              value={address}
              onChange={(e) => setAddress(e.target.value)}
              required
            />
          </FormField>
          <FormField label="标签" htmlFor={`${formId}-label`} hint="给人看的名字，如「冷钱包」。">
            <Input id={`${formId}-label`} value={label} onChange={(e) => setLabel(e.target.value)} />
          </FormField>
          {error ? <ActionErrorNote error={error} /> : null}

        <Button type="submit" disabled={address.trim() === "" || mutation.isPending}>
          {mutation.isPending ? "登记中…" : "登记"}
        </Button>
      </form>
    </Dialog>
  );
}
