/** Infini 提现的 API 客户端（XM-CARD6）。
 *
 *  与 api/cards.ts 分文件，理由和后端把 WithdrawQuerier 与 CardQuerier
 *  分开定义是同一条：这两组端点的权限不同（card.read / fund.withdraw）。
 *  混在一个文件里，「谁能读什么」会变成一件要读路由才知道的事。
 */

import {
  apiClient,
  FeatureNotMountedError,
  looksLikeUnmountedRoute,
  type ApiClient,
} from "./client";
import {
  executeAction,
  submitAction,
  type ActionOutcome,
  type ActionRun,
  type ListOptions,
} from "./platform";

const WITHDRAW_NOT_MOUNTED_DESCRIPTION =
  "提现在当前环境未启用。它跟随卡片功能一起挂载（XM_CARDS_MODE 不为 off），" +
  "并且需要 fund-operator 角色——没有那个角色时这里会是 403 而不是 404。";

/** 把「整组路由没挂载」的 404 翻成 FeatureNotMountedError。
 *
 *  同 api/cards.ts：只认没有 error.code 的裸 404。权限不足是 403，
 *  走另一条路径显示成「需要权限」，两者不能混——一个要改配置，
 *  一个要找人授权，处置完全不同。 */
function translateUnmounted(error: unknown): never {
  if (looksLikeUnmountedRoute(error)) {
    throw new FeatureNotMountedError(error, WITHDRAW_NOT_MOUNTED_DESCRIPTION);
  }
  throw error;
}

interface ListResponse<T> {
  items?: T[];
}

/** 一条已登记的可提现地址。
 *
 *  `address` 是完整地址，不是掩码：能提现的人必须核对它，让他去别处查
 *  等于逼他在页面之外做核对——那才是出错的地方。 */
export interface WithdrawAddress {
  address_id: string;
  account: string;
  chain: string;
  address: string;
  label: string;
  /** 停用的地址仍在清单里（可查、可重新启用），但提现表单不提供它。 */
  enabled: boolean;
}

export interface WithdrawItem {
  request_id: string;
  account: string;
  chain: string;
  token_type: string;
  amount: string;
  address_id: string;
  address: string;
  status: string;
  /** 链上哈希。有它才能自己去区块浏览器核对钱到没到。 */
  tx_hash?: string;
  actual_amount?: string;
  gas_fee?: string;
  gas_fee_currency?: string;
  note?: string;
  started_at?: string;
  updated_at?: string;
}

/** 一个账号的提现额度。
 *
 *  没设过额度的账号**不会出现在清单里**——「设成 0」和「没设过」是两件事，
 *  前者是有人刻意关掉了这个账号的提现，后者是还没人管过它。 */
export interface WithdrawLimit {
  account: string;
  per_operation: string;
  per_day: string;
  updated_by?: string;
  updated_at?: string;
}

export async function listWithdrawLimits(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<WithdrawLimit[]> {
  const body = await client
    .get<ListResponse<WithdrawLimit>>("/api/v1/cards/withdraw/limits", {
      ...(options.signal ? { signal: options.signal } : {}),
    })
    .catch(translateUnmounted);
  return body.items ?? [];
}

/** 调整某账号的提现额度（`cards.withdraw.limit.set@1`）。
 *
 *  权限是 `fund.limit.manage`（admin 持有），**与提现的 `fund.withdraw`
 *  不是同一个**。额度从环境变量搬进数据库之后，「改不了」这道屏障就没有了；
 *  替代它的是两把钥匙分持——被盗用的 fund-operator 抬不高自己的天花板。
 *
 *  两个值一起提交：只改单笔不改单日会得到一组谁也没打算过的组合。 */
export function setWithdrawLimits(
  params: { account: string; per_operation: string; per_day: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "cards.withdraw.limit.set", version: "1", params },
    options,
    client,
  );
}

/** 上线或下线一条登记地址（`cards.withdraw.address.set_enabled@1`）。
 *
 *  **不删除**：一条曾经被列入白名单的地址，它存在过这件事本身就是审计
 *  事实——「这条地址当初是谁登记的、什么时候下线的」正是出事之后第一个
 *  要问的问题。已发出的提现也不受影响，台账里存的是登记时的地址快照。 */
export function setWithdrawAddressEnabled(
  params: { address_id: string; enabled: boolean },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "cards.withdraw.address.set_enabled", version: "1", params },
    options,
    client,
  );
}

export async function listWithdrawAddresses(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<WithdrawAddress[]> {
  const body = await client
    .get<ListResponse<WithdrawAddress>>("/api/v1/cards/withdraw/addresses", {
      ...(options.signal ? { signal: options.signal } : {}),
    })
    .catch(translateUnmounted);
  return body.items ?? [];
}

export async function listWithdrawals(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<WithdrawItem[]> {
  const body = await client
    .get<ListResponse<WithdrawItem>>("/api/v1/cards/withdrawals", {
      ...(options.signal ? { signal: options.signal } : {}),
    })
    .catch(translateUnmounted);
  return body.items ?? [];
}

/** 登记一条可提现地址（`cards.withdraw.address.register@1`）。
 *
 *  `address_id` 由调用方给定并充当幂等键：同一条地址重复登记应当得到
 *  同一条记录，而不是两条内容相同、id 不同的白名单项。 */
export function registerWithdrawAddress(
  params: {
    account: string;
    address_id: string;
    chain: string;
    address: string;
    label?: string;
  },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "cards.withdraw.address.register", version: "1", params },
    options,
    client,
  );
}

/** 发起一次提现（`cards.withdraw.execute@1`，**L3**）。
 *
 *  只传 `address_id`，**不传地址本身**：地址由服务端从白名单里取。
 *  传地址进来再比对是另一回事——那样一个比对逻辑的疏漏就能让任意地址过去。
 *
 *  `request_id` 必须由调用方稳定生成：上游对它有真幂等（重复会回
 *  is_duplicate 而不是转两次），换一个键等于告诉双方「这是另一笔提现」。
 *
 *  L3 是全平台最高的一档（宪法 9 条：L3/L4 必须审批）。这次调用**不会**当场
 *  转账，内核会受理成一张审批单并回 202；`reason` 必填，它是审批人唯一能据以
 *  判断「这笔钱该不该转」的东西。 */
export function executeWithdraw(
  params: {
    account: string;
    request_id: string;
    chain: string;
    token_type: string;
    amount: string;
    address_id: string;
    source_currency?: string;
    note?: string;
  },
  reason: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionOutcome> {
  return submitAction(
    { actionId: "cards.withdraw.execute", version: "1", params, reason },
    options,
    client,
  );
}
