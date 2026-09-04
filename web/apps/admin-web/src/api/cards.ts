/** Infini 卡服务的 API 客户端（XM-CARD3）。
 *
 *  读走三个只读端点，写全部走 `cards.card.*` Action——后端没有第二条写路径，
 *  这里也不该有。
 *
 *  条目形状**保持后端 snake_case 原样**，不另建一层驼峰映射：这批端点只有
 *  一个消费者（卡片页），映射层的价值全在「多处消费时形状一致」，
 *  为一个消费者建一层只是多一个会漂的地方（同 api/server.ts 的选择）。
 *
 *  **明文卡面数据只从 `revealCard` 的返回值里出现，永远不要把它写进任何
 *  状态容器、localStorage、URL 或日志。** 后端的读端点里根本没有这些字段。
 */

import {
  apiClient,
  FeatureNotMountedError,
  looksLikeUnmountedRoute,
  type ApiClient,
} from "./client";
import { executeAction, type ActionRun, type ListOptions } from "./platform";

interface ListResponse<T> {
  items: T[] | null;
}

/** 「卡片管理」在当前环境未启用的说明文案。
 *
 *  XM_CARDS_MODE=off 时后端整组不挂载这三个只读端点（router.go 的
 *  `d.Cards != nil`），而侧栏里的「卡片管理」是无条件显示的。不做这层翻译，
 *  未启用的环境点进去看到的是一个泛型报错，读的人会以为它坏了而不是没开。 */
const CARDS_NOT_MOUNTED_DESCRIPTION =
  "卡片管理在当前环境未启用（XM_CARDS_MODE=off）。配好 Infini 账号与凭据后会自动出现，无需手动开启。";

/** 把「整组路由没挂载」的 404 翻成 FeatureNotMountedError。
 *
 *  只认没有 error.code 的裸 404（chi 的默认 404）。带 error.code 的 404 是
 *  「这一条没找到」，两者混为一谈会把一次真实的查不到显示成「功能未启用」，
 *  让人跑去改配置。 */
function translateUnmounted(error: unknown): never {
  if (looksLikeUnmountedRoute(error)) {
    throw new FeatureNotMountedError(error, CARDS_NOT_MOUNTED_DESCRIPTION);
  }
  throw error;
}

/** 卡片列表的响应：条目 + 已配置的账号清单。
 *
 *  账号清单由后端给，不从卡片数据里反推——一个还没开过卡的环境反推不出
 *  任何账号，开卡表单会没有可选项。 */
interface CardListResponse {
  items: CardItem[] | null;
  accounts: string[] | null;
  /** 平台开过卡时用过的企业成员邮箱。上游没有成员列表接口，
   *  开卡表单的下拉只能用这个。第一次开卡时它是空的。 */
  member_emails: string[] | null;
}

/** 投影数据的新鲜度。判定在服务端做，前端只负责显示——
 *  两处各判一次迟早会分叉，而分叉的那一边会把陈旧数据显示成实时的。 */
export interface CardFreshness {
  synced_at?: string;
  age_seconds: number;
  stale: boolean;
  /** 与「很久没同步」是两件事：这条说明这张卡从建立起就没被同步过。 */
  never_synced: boolean;
}

/** 卡片列表一行。 */
export interface CardItem {
  /** 内部运营维度：这张卡的钱从哪个 Infini 账号出。
   *  以后开放外部用户时，那一侧不暴露这个字段。 */
  account: string;
  card_id: string;
  /** 掩码卡号，任何人都看得到。 */
  mask: string;
  /** 卡面明文，**只有持有 card.reveal 权限时后端才会回**；否则字段缺席。
   *
   *  明文落库是产品负责人 2026-09-04 的决定。原设计里明文只经
   *  `revealCard` 取得且每次留审计，落库之后那条审计链不复存在，
   *  权限是「谁能看卡号」剩下的唯一约束。 */
  pan?: string;
  cvv?: string;
  expiry_mmyy?: string;
  holder_name: string;
  card_alias: string;
  status: string;
  currency: string;
  /** 整数最小单位（USD 即分）。前端只做除法显示，不参与任何计算。 */
  balance_minor: number;
  owner_ref?: string;
  user_email?: string;
  /** 以下是平台自己的用途登记，上游一个都不知道。 */
  bound_account?: string;
  bound_account_kind?: string;
  service_name?: string;
  /** YYYY-MM-DD 或缺席。 */
  next_renewal_on?: string;
  usage_note?: string;
  /** 续费风险由**服务端**判定：none / soon / unfunded / overdue。
   *  前端不自己算——两处各算一遍迟早分叉，而分叉的那一边会把
   *  「续不上」显示成正常。 */
  renewal_risk: string;
  /** 上游记的开卡时刻（RFC3339）；缺失时字段不出现。 */
  issued_at?: string;
  /** 开卡手续费与实付额（十进制文本，币种为申请时所选代币）。
   *  实测手续费固定 1 USD——小额卡的成本占比很高，所以这一列要能被看到。 */
  issue_fee?: string;
  issue_pay_amount?: string;
  freshness: CardFreshness;
}

/** 一笔卡交易。 */
export interface CardTransactionItem {
  account: string;
  card_id: string;
  type: string;
  amount_minor: number;
  fee_minor: number;
  currency: string;
  status: string;
  merchant: string;
  occurred_at?: string;
  /** 商户侧原始币种的金额；同币种消费时不出现。
   *  一张 USD 卡在欧元商户消费，amount_minor 是折成 USD 的，这里才是 EUR 原值——
   *  没有它看不出跨境消费与汇率加价。 */
  transaction_amount?: string;
  transaction_currency?: string;
  /** 缺席表示尚未结算（授权中，金额还可能变）。 */
  settled_at?: string;
}

/** 一笔待人工处置的操作。
 *
 *  每一条都意味着「有一笔花钱操作，我们至今不知道它到底成没成」。 */
export interface CardOperationItem {
  idempotency_key: string;
  account: string;
  kind: string;
  state: string;
  card_id?: string;
  card_alias?: string;
  amount?: string;
  token_type?: string;
  reason?: string;
  started_at: string;
  /** 由**服务端**判定。前端不要按 state 自己推：迟早会推出一个
   *  「看起来该能重试」的不确定态，而重试可能重复扣钱。 */
  retry_allowed: boolean;
}

/** 卡片列表 + 可选账号。 */
export interface CardsView {
  cards: CardItem[];
  accounts: string[];
  memberEmails: string[];
}

export async function listCards(
  options: ListOptions & { account?: string; ownerRef?: string } = {},
  client: ApiClient = apiClient,
): Promise<CardsView> {
  const params = new URLSearchParams();
  if (options.account) params.set("account", options.account);
  if (options.ownerRef) params.set("owner_ref", options.ownerRef);
  const query = params.toString() ? `?${params.toString()}` : "";

  const body = await client
    .get<CardListResponse>(`/api/v1/cards${query}`, {
      ...(options.signal ? { signal: options.signal } : {}),
    })
    .catch(translateUnmounted);
  return {
    cards: body.items ?? [],
    accounts: body.accounts ?? [],
    memberEmails: body.member_emails ?? [],
  };
}

export async function listCardTransactions(
  account: string,
  cardId: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<CardTransactionItem[]> {
  const body = await client
    .get<ListResponse<CardTransactionItem>>(
      `/api/v1/cards/${encodeURIComponent(cardId)}/transactions?account=${encodeURIComponent(account)}`,
      { ...(options.signal ? { signal: options.signal } : {}) },
    )
    .catch(translateUnmounted);
  return body.items ?? [];
}

export async function listCardOperationsNeedingAttention(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<CardOperationItem[]> {
  const body = await client
    .get<ListResponse<CardOperationItem>>("/api/v1/cards/operations/attention", {
      ...(options.signal ? { signal: options.signal } : {}),
    })
    .catch(translateUnmounted);
  return body.items ?? [];
}

/** 开卡（`cards.card.issue@1`）。
 *
 *  `idempotency_key` 必填且必须由调用方**稳定**生成：它决定发给上游的
 *  card_alias，也是超时后对账的唯一抓手。同一次提交重试要带同一个键——
 *  换一个键等于告诉后端「这是另一次开卡」，而上游没有幂等能力。 */
export function issueCard(
  params: {
    account: string;
    idempotency_key: string;
    product_id: number;
    top_up_amount: string;
    token_type: string;
    user_email: string;
    holder_name: string;
    owner_ref?: string;
  },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "cards.card.issue", version: "1", params }, options, client);
}

/** 给已有的卡充值（`cards.card.topup@1`）。同样受金额上限约束。 */
export function topUpCard(
  params: {
    account: string;
    idempotency_key: string;
    card_id: string;
    amount: string;
    token_type: string;
    note?: string;
  },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "cards.card.topup", version: "1", params }, options, client);
}

/** 把卡上的余额退回账户（`cards.card.redeem@1`）。
 *
 *  不受金额上限约束：赎回是资金回流不是花钱，用上限卡住它会在最需要
 *  止损的时候拦住止损动作。 */
export function redeemCard(
  params: {
    account: string;
    idempotency_key: string;
    card_id: string;
    amount: string;
    token_type: string;
    note?: string;
  },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "cards.card.redeem", version: "1", params }, options, client);
}

export function freezeCard(
  params: { account: string; idempotency_key: string; card_id: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "cards.card.freeze", version: "1", params }, options, client);
}

export function unfreezeCard(
  params: { account: string; idempotency_key: string; card_id: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "cards.card.unfreeze", version: "1", params }, options, client);
}

/** 明文卡面数据。**只存在于这一次响应里。** */
export interface RevealedCard {
  Number: string;
  CVV: string;
  ExpiryMMYY: string;
  Currency: string;
}

/** 查看明文卡号 / CVV / 有效期（`cards.card.reveal@1`）。
 *
 *  这是一个读操作却走 Action，因为它需要审计：「谁在何时看了哪张卡的明文」
 *  是这个功能最该留痕的一条记录。
 *
 *  返回值**绝不能**写进任何状态容器、localStorage、URL 或日志——
 *  用完即弃，组件卸载时清空。 */
export async function revealCard(
  params: { account: string; card_id: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<RevealedCard> {
  const run = await executeAction(
    { actionId: "cards.card.reveal", version: "1", params },
    options,
    client,
  );
  return run.result as RevealedCard;
}

/** 登记卡片的业务用途（`cards.card.usage.set@1`）。
 *
 *  绑定账号、订阅服务、下次续费日期这些上游一个都不知道，是平台自己记的。
 *  续费日期是人填的而不是从流水推断的：试用转正、年付转月付、涨价都会让
 *  推断悄悄错掉，而错了的提醒比没有提醒更糟。 */
export function setCardUsage(
  params: {
    account: string;
    card_id: string;
    bound_account?: string;
    bound_account_kind?: string;
    service_name?: string;
    next_renewal_on?: string;
    note?: string;
  },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "cards.card.usage.set", version: "1", params }, options, client);
}

/** 一个账号的资金池可用余额。 */
export interface CardAccountBalance {
  account: string;
  usdt?: string;
  usdc?: string;
  usd?: string;
  /** 有值表示这个账号这次没取到；只给失败**分类**，不含上游原文。 */
  error?: string;
}

/** 读各账号的资金池可用余额。
 *
 *  这是**实时上游调用**，不是投影——比其余读端点慢，所以单独一个查询、
 *  按需拉取，不跟卡片列表一起轮询。 */
export async function listCardBalances(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<CardAccountBalance[]> {
  const body = await client
    .get<ListResponse<CardAccountBalance>>("/api/v1/cards/balances", {
      ...(options.signal ? { signal: options.signal } : {}),
    })
    .catch(translateUnmounted);
  return body.items ?? [];
}

/** 一次尚未过期的 3DS 验证挑战。 */
export interface CardChallenge {
  account: string;
  card_id: string;
  challenge_id: string;
  challenge_type?: string;
  /** 验证码。只回给持有 card.reveal 的调用方，**而且上游不一定给**——
   *  2026-09-05 生产收到的真实事件里就没有这个字段。 */
  code?: string;
  expires_at?: string;
}

/** 读尚未过期的验证挑战。过期的由服务端过滤掉，不到前端。 */
export async function listCardChallenges(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<CardChallenge[]> {
  const body = await client
    .get<ListResponse<CardChallenge>>("/api/v1/cards/challenges", {
      ...(options.signal ? { signal: options.signal } : {}),
    })
    .catch(translateUnmounted);
  return body.items ?? [];
}

/** 关停一张卡（`cards.card.delete@1`）。
 *
 *  **不可逆**：上游接受后卡进 pending_delete，结清余额后变 deleted，
 *  没有任何接口能把它恢复。调用方必须先向人确认。 */
export function deleteCard(
  params: { account: string; idempotency_key: string; card_id: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "cards.card.delete", version: "1", params }, options, client);
}
