import { apiClient, type ApiClient } from "./client";
import type { ListOptions } from "./platform";

/** 渠道绑定历史（`GET /api/v1/finance/platform-channel-bindings`，
 *  `internal/platform/httpapi/channel_bindings.go`）。
 *
 *  这条端点自挂载起前端一次都没调过（全 `web/` 搜 `platform-channel-bindings`
 *  = 0 命中）：写入侧（确认 / 改绑 / 解绑）走的是两个 L1 Action，读的当前态
 *  走的是 `/platforms/{platform}/channels`，**历史从来没有显示过**。
 *
 *  ── 为什么单独一个模块，而不是塞进 api/platformChannels.ts ──────────────
 *  两条端点的响应形状不同，且**同一个东西的键名都不一样**：渠道目录里渠道名
 *  叫 `name`，这条端点里叫 `channel_name`；渠道目录的 `binding` 只有四个字段
 *  （id / upstream_account_id / valid_from / reason），这条端点的 `binding` 与
 *  `history[]` 是同一个更全的结构（多 `provenance` 与 `created_by`）。合成一个
 *  模块会立刻出现「哪个 binding 类型」的歧义。 */

const PATH = "/api/v1/finance/platform-channel-bindings";

/** 一页取多少条候选。上限来自服务端的 `parseBindingLimit`（1–200，超出直接
 *  400「limit 必须在 1 到 200 之间」）。取满 200 是为了把翻页次数压到最少：
 *  服务端在 `include_history=true` 时**逐条**去查历史，页大小不影响总的历史
 *  查询次数（要翻到目标那一页为止都会查），只影响往返次数。 */
const PAGE_LIMIT = 200;

/** 最多翻几页。防的是「服务端一直回同一个 next_cursor」这种情况把浏览器挂死，
 *  不是业务上限——一个 service 有 1000 条以上渠道时这一块会诚实说自己没找到，
 *  而不是无声无息地转下去。
 *
 *  实践中恒为 1 页：渠道详情页自己那条 `listPlatformChannels` 不带 limit，
 *  吃服务端默认的 50，也就是说详情页**只可能**打开排序前 50 的渠道；那 50 条
 *  必然落在这里的第一页里。 */
const MAX_PAGES = 5;

/** 一条绑定记录。字段名逐字对应服务端的 `channelBindingResponse`
 *  （channel_bindings.go 约 58 行）。
 *
 *  ⚠️ **没有 `valid_to`**：库表 `finance.platform_channel_binding` 有这一列
 *  （解绑与改绑都靠 `ClosePlatformChannelBinding` 写它），但响应结构没有把它
 *  序列化出来。所以前端拿到的历史只有「从什么时候开始」，没有「到什么时候
 *  结束」——这一点必须显示给人看，不能靠「下一条的生效时间」去推：解绑之后
 *  可以长期没有任何绑定，那段空档在这份数据里完全不可见。 */
export interface ChannelBindingRecord {
  id: string;
  upstreamAccountId: string;
  /** RFC3339Nano（服务端 `ValidFrom.UTC().Format(time.RFC3339Nano)`）。 */
  validFrom: string;
  provenance: string;
  reason: string;
  createdBy: string;
}

/** 取历史的三种结局。分开命名而不是「有没有数据」两态：
 *  「这条渠道没有绑定过」与「这条渠道压根不在候选清单里」在屏幕上长得一样，
 *  但前者是事实、后者是我们没查到，下一步完全不同。 */
export type ChannelBindingHistoryState =
  /** 在候选清单里找到了这条渠道，`history` 是它的完整绑定历史（可能为空）。 */
  | "ok"
  /** 翻完了整份候选清单也没有这条渠道（服务端不再给 next_cursor）。 */
  | "channel_absent"
  /** 翻到 MAX_PAGES 还没找到，且服务端还在给 next_cursor。 */
  | "truncated";

export interface ChannelBindingHistoryResult {
  state: ChannelBindingHistoryState;
  /** 服务端 `ORDER BY valid_from DESC, id DESC`（db/queries/finance.sql 的
   *  `ListPlatformChannelBindingHistory`）——**最新的在前**，原样保留顺序，
   *  不在前端重排。 */
  history: ChannelBindingRecord[];
  /** 同一次响应里 `binding` 字段的 id，也就是当前生效的那一条；没有绑定时为
   *  null。历史里没有 `valid_to`，只能靠这个 id 把「现在生效的是哪一条」标
   *  出来。 */
  currentBindingId: string | null;
  /** 翻了几页。truncated 的说明文案要用到，也方便排障。 */
  pagesFetched: number;
}

interface RawBinding {
  id?: string;
  upstream_account_id?: string;
  valid_from?: string;
  provenance?: string;
  reason?: string;
  created_by?: string;
}

interface RawItem {
  channel_ref?: { service_id?: string; external_channel_id?: string };
  channel_name?: string;
  binding?: RawBinding | null;
  /** 服务端是 `json:"history,omitempty"`：`include_history=false` 时整个键不
   *  出现，`include_history=true` 且这条渠道没绑定过时是 `[]`——两者都按空
   *  数组处理，因为我们**永远**带着 include_history=true 去问。 */
  history?: RawBinding[] | null;
}

interface RawPage {
  items?: RawItem[] | null;
  next_cursor?: string | null;
}

function toRecord(raw: RawBinding): ChannelBindingRecord {
  return {
    id: raw.id ?? "",
    upstreamAccountId: raw.upstream_account_id ?? "",
    validFrom: raw.valid_from ?? "",
    provenance: raw.provenance ?? "",
    reason: raw.reason ?? "",
    createdBy: raw.created_by ?? "",
  };
}

export interface ChannelBindingHistoryParams {
  serviceId: string;
  externalChannelId: string;
}

/** 取一条渠道的绑定历史。
 *
 *  端点是**按 service 分页的候选清单**，不是按渠道的详情——想要一条渠道的
 *  历史，只能带着 `include_history=true` 翻页、从 items 里挑出 `channel_ref.
 *  external_channel_id` 等于目标的那一条。
 *
 *  ── 为什么不自己拼一个 cursor 直接跳到目标那一条 ──────────────────────────
 *  服务端的 cursor 就是 `external_channel_id` 的 base64url（无填充），候选按
 *  id 升序排、过滤条件是 `> cursor`，所以理论上可以拼一个「目标的前一条」的
 *  cursor + `limit=1`，让服务端只查一次历史。**没有这么做**：cursor 是不透明
 *  凭据，服务端换个编码（比如改成带时间戳的复合游标）前端会静默失灵。这里
 *  只用服务端自己给的 `next_cursor` 翻页。
 *
 *  代价是服务端会为这一页里**每一条**渠道各查一次历史。可接受：详情页是按需
 *  打开的，且实测只会翻一页。真嫌贵的话该加的是一条按 ChannelRef 取历史的
 *  端点，而不是让前端去猜 cursor 的编码。 */
export async function getChannelBindingHistory(
  params: ChannelBindingHistoryParams,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ChannelBindingHistoryResult> {
  let cursor: string | undefined = undefined;

  for (let page = 1; page <= MAX_PAGES; page += 1) {
    const body: RawPage = await client.get<RawPage>(PATH, {
      searchParams: {
        service_id: params.serviceId,
        include_history: "true",
        limit: String(PAGE_LIMIT),
        ...(cursor ? { cursor } : {}),
      },
      ...(options.signal ? { signal: options.signal } : {}),
    });

    const item = (body.items ?? []).find(
      (candidate) => candidate.channel_ref?.external_channel_id === params.externalChannelId,
    );
    if (item) {
      return {
        state: "ok",
        history: (item.history ?? []).map(toRecord),
        currentBindingId: item.binding?.id ?? null,
        pagesFetched: page,
      };
    }

    const next = body.next_cursor ?? null;
    if (!next) {
      return { state: "channel_absent", history: [], currentBindingId: null, pagesFetched: page };
    }
    cursor = next;
  }

  return { state: "truncated", history: [], currentBindingId: null, pagesFetched: MAX_PAGES };
}

/** 绑定历史查询的 queryKey 前缀。写入侧（ChannelBindingCard 的确认 / 解绑）
 *  按这个前缀失效缓存——绑定一改，历史就多了一条。 */
export const CHANNEL_BINDING_HISTORY_QUERY_KEY = "platform-channel-binding-history";

/** truncated / channel_absent 两种结局各自的说明。放在这里而不是组件里：
 *  这两句说的是**端点的形状**（按 service 分页、翻了几页），与它们对应的
 *  判定逻辑就在上面几行，分开写迟早对不上。 */
export function channelBindingHistoryStateNote(
  result: ChannelBindingHistoryResult,
  externalChannelId: string,
): string {
  if (result.state === "truncated") {
    return `翻了 ${result.pagesFetched} 页（每页 ${PAGE_LIMIT} 条）仍没有在绑定候选清单里找到渠道 ${externalChannelId}，且服务端还有下一页。这一块因此不显示历史，而不是显示「没有历史」——两者不是一回事。`;
  }
  return `绑定候选清单里没有渠道 ${externalChannelId}。这份清单由库存观测（渠道目录）与已确认的绑定合并而成，渠道刚下线、或库存观测还没跑过时都会出现这种情况；它不代表这条渠道没有绑定过。`;
}
