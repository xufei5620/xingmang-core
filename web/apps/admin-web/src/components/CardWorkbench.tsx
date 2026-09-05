import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { formatUtcTimestamp } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, Input } from "@xingmang/ui-primitives";
import { useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import {
  deleteCard,
  freezeCard,
  listCardChallenges,
  listCards,
  unfreezeCard,
  type CardChallenge,
  type CardItem,
} from "../api/cards";
import {
  cardStatusLabel,
  cardStatusTone,
  isCardLocked,
} from "../lib/cardStatus";
import { formatMinorUnits } from "../lib/money";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";
import { CardTransactions, UsageForm } from "./CardCardForms";
import { CardFundsDialog } from "./CardFundsDialog";

const CARDS_QUERY = "cards";
const CARD_CHALLENGES_QUERY = "card-challenges";

/** 卡片工作台：左侧清单 + 右侧详情（XM-CARD8）。
 *
 *  形态照 Infini 后台做，产品负责人 2026-09-05 定的——他每天在用那个界面，
 *  两边长得一样就不用来回适应。**这是对 ADMIN-IA §3 的一次显式推翻**
 *  （那里写「主对象一律用完整详情页，不用右侧详情面板」），已记进
 *  docs/architecture/ADMIN-IA.md 的例外一节，不是悄悄改的。
 *
 *  但 §3 真正在乎的实质保住了：**选中哪张卡写进路由**
 *  （`/cards/:account/:cardId`）。Infini 自己的右栏选中态只活在内存里，
 *  刷新回到第一张、也没法把「这张卡」发给同事；我们这版可以。
 *  推翻的是视觉形态，不是可链接性。 */
export function CardWorkbench() {
  const { account, cardId } = useParams();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  // 账号筛选写在查询串里（`?account=`），与页签、选中卡同一条纪律：
  // 这一页上所有「我在看什么」的状态都可链接、可刷新。
  const accountFilter = params.get("account") ?? "";
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);
  const [actionError, setActionError] = useState<unknown>(null);

  const query = useQuery({
    queryKey: [CARDS_QUERY],
    queryFn: ({ signal }) => listCards({ signal }),
    staleTime: 30_000,
  });
  const challengesQuery = useQuery({
    queryKey: [CARD_CHALLENGES_QUERY],
    queryFn: ({ signal }) => listCardChallenges({ signal }),
    // 验证码只有几分钟有效，刷新要比卡片列表勤。
    refetchInterval: 20_000,
  });

  const all = query.data?.cards ?? [];
  // 不筛就是全部。默认只显示某个账号会让人以为卡丢了——两个账号的卡数
  // 悬殊时，那种「丢了」看起来非常真。
  //
  // 选中态也只在**筛选后**的集合里找（见下面的 selected）：筛掉了当前
  // 选中的卡时落到还看得见的第一张，否则右栏会显示一张左边根本看不到的
  // 卡，那种不一致比空右栏更让人困惑。
  const cards = accountFilter ? all.filter((c) => c.account === accountFilter) : all;
  const challenges = new Map(
    (challengesQuery.data ?? []).map((c) => [`${c.account}/${c.card_id}`, c]),
  );

  // 路由里指到哪张就选哪张；没指或指不到就回落到第一张。
  //
  // 回落而不是留空右栏：一个空右栏看起来和「加载失败」没有区别，
  // 而这一页几乎总有卡可看。
  const selected =
    cards.find((c) => c.account === account && c.card_id === cardId) ??
    cards[0] ??
    null;

  function select(card: CardItem) {
    // replace 而不是 push：在清单里点着看是浏览行为，不该在历史里堆一串，
    // 否则看完五张卡要按五次返回才离得开这一页。
    // 带上当前的查询串：换卡不该顺手把账号筛选与页签清掉。
    const qs = params.toString();
    navigate(
      `/cards/${encodeURIComponent(card.account)}/${encodeURIComponent(card.card_id)}` +
        (qs ? `?${qs}` : ""),
      { replace: true },
    );
  }

  function afterWrite(r: ActionResult) {
    setResult(r);
    setActionError(null);
    void queryClient.invalidateQueries({ queryKey: [CARDS_QUERY] });
  }

  return (
    <div className="flex min-w-0 flex-col gap-3">
      {result ? <ActionResultNote result={result} /> : null}
      {actionError ? <ActionErrorNote error={actionError} /> : null}

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {cards.length === 0 ? (
          <p className="text-fg-muted text-sm">
            {accountFilter
              ? `账号 ${accountFilter} 名下还没有卡片。再点一次上面那个账号可以取消筛选。`
              : "还没有卡片。点右上角「开卡」创建第一张；在 Infini 后台直接建的卡会由同步作业在下一轮（5 分钟内）自动拉进来。"}
          </p>
        ) : (
          // 三栏：清单 | 卡片详情 | 交易流水。
          //
          // 流水从详情里分出来单独占一栏（产品负责人 2026-09-05）：详情是
          // 「这张卡是什么」，流水是「这张卡发生过什么」，两件事都要看但不
          // 争同一块地方——原先流水挂在详情最底下，看一笔消费要先滚过整个
          // 卡片信息，而右边那半屏是空的。
          //
          // xl 才三栏：中屏上第三栏会把前两栏挤到读不动，那时仍是两栏、
          // 流水回到详情下方。
          <div className="grid min-w-0 gap-3 lg:grid-cols-[minmax(18rem,22rem)_minmax(0,1fr)] xl:grid-cols-[minmax(18rem,22rem)_minmax(0,1fr)_minmax(0,1fr)]">
            <CardRail
              cards={cards}
              selected={selected}
              challenges={challenges}
              onSelect={select}
            />
            {selected ? (
              <>
                <CardPane
                  card={selected}
                  challenge={challenges.get(
                    `${selected.account}/${selected.card_id}`,
                  )}
                  onWrite={afterWrite}
                  onError={setActionError}
                />
                <CardLedgerColumn card={selected} />
              </>
            ) : null}
          </div>
        )}
      </ApiStateView>
    </div>
  );
}

/** 左侧卡片清单。
 *
 *  每行给的四样和 Infini 一致：卡名、邮箱、后四位、状态与余额。选卡时人
 *  认的是**卡名**（Two.V / flower / 小拙），不是卡号——卡号那一串在清单里
 *  只能靠后四位区分。 */
function CardRail({
  cards,
  selected,
  challenges,
  onSelect,
}: {
  cards: CardItem[];
  selected: CardItem | null;
  challenges: Map<string, CardChallenge>;
  onSelect: (card: CardItem) => void;
}) {
  const [q, setQ] = useState("");

  // 卡名、邮箱、卡号后四位、用途都能搜。
  //
  // 改成分栏之后表格那套筛选与排序没有了，卡一多就只能靠滚——而这一页的
  // 卡数只会越来越多（同步现在会把上游直接建的卡也拉进来）。
  const needle = q.trim().toLowerCase();
  const shown = needle
    ? cards.filter((c) =>
        [c.card_alias, c.holder_name, c.mask, c.owner_ref, c.card_id].some(
          (v) => (v ?? "").toLowerCase().includes(needle),
        ),
      )
    : cards;

  return (
    <div className="flex min-w-0 flex-col gap-2">
      <Input
        aria-label="搜索卡片"
        placeholder="搜卡名、邮箱、卡号后四位或用途"
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      {shown.length === 0 ? (
        <p className="text-fg-muted text-sm">没有匹配的卡片。</p>
      ) : (
        // 不设 max-height、不开 overflow：框里再滚一层会让人先找到内滚动条
        // 才够得着下面的卡，而页面本身已经能滚。
        <ul className="border-edge flex min-w-0 flex-col rounded-md border">
          {shown.map((c) => {
            const active =
              selected?.account === c.account &&
              selected?.card_id === c.card_id;
            const challenge = challenges.get(`${c.account}/${c.card_id}`);
            return (
              <li
                key={`${c.account}/${c.card_id}`}
                className="border-edge border-b last:border-b-0"
              >
                <button
                  type="button"
                  onClick={() => onSelect(c)}
                  aria-current={active ? "true" : undefined}
                  className={`flex w-full min-w-0 items-center gap-3 px-3 py-2 text-left hover:bg-surface-muted focus-visible:outline-2 focus-visible:outline-accent ${
                    active ? "bg-accent-soft" : ""
                  }`}
                >
                  {/* 行的排布照 Infini 后台：卡图标 | 卡名 / 邮箱·后四位·状态 | 余额。
                      三段各自定位——
                      左段固定宽（图标），中段吃掉剩余宽度，右段按内容收缩并右对齐。

                      状态原先是紧跟卡名的徽章，于是名字一长一短它就左右跳，
                      扫一列卡时眼睛得逐行重新找它在哪儿。挪到第二行、且**邮箱定宽**
                      之后，后四位与状态每行都落在同一个横坐标上。 */}
                  <CardGlyph />
                  <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                    <span className="flex min-w-0 items-center gap-2">
                      <span className="truncate text-sm font-semibold">
                        {c.card_alias || c.card_id}
                      </span>
                      {/* Infini 这里还有个 Lite / Pro 档位徽章，**我们画不出来**：
                          product_id 只出现在开卡请求里，卡对象上没有这个字段
                          （见 contracts/connectors/infini/openapi/card.yaml），
                          上游读不回来。凭开卡时的记忆猜一个会在同步拉进来的
                          卡上指错档位。 */}
                    </span>
                    <span className="text-fg-muted flex items-center gap-2 text-xs">
                      {/* 邮箱定宽截断，好让后面两项每行对齐——这正是「状态固定
                          一个位置」的做法，Infini 也是这么排的。 */}
                      <span className="w-32 truncate">{c.holder_name || "—"}</span>
                      <span aria-hidden="true" className="text-edge">
                        |
                      </span>
                      {/* 后四位放大一档（text-xs → text-sm）：选卡时人常常是拿着
                          手上那张卡或另一个页面的卡号来对，而这四位就是唯一的
                          对照物，小到要凑近看就失职了。 */}
                      <span className="text-fg font-mono text-sm whitespace-nowrap tabular-nums">
                        {c.mask ? `****${c.mask.slice(-4)}` : "—"}
                      </span>
                      <span aria-hidden="true" className="text-edge">
                        |
                      </span>
                      <span className={`whitespace-nowrap ${statusTextClass(c.status)}`}>
                        {cardStatusLabel(c.status)}
                      </span>
                      {/* 验证码接在状态后面：它只有几分钟有效，而这一行正是
                          眼睛扫过每张卡时会落到的地方。没有待验证时不渲染，
                          让有码的那张在一列里跳出来。 */}
                      {challenge?.code ? (
                        <Badge tone="warning">验证码 {challenge.code}</Badge>
                      ) : null}
                    </span>
                  </span>
                  <span className="flex shrink-0 flex-col items-end">
                    <span className="text-sm font-semibold tabular-nums">
                      {formatMinorUnits(c.balance_minor, c.currency)}
                    </span>
                    <span className="text-fg-muted text-xs">总余额</span>
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

/** 右侧详情：卡面、余额与四个动作、卡片信息。 */
function CardPane({
  card,
  challenge,
  onWrite,
  onError,
}: {
  card: CardItem;
  challenge: CardChallenge | undefined;
  onWrite: (r: ActionResult) => void;
  onError: (e: unknown) => void;
}) {
  return (
    // max-w 定宽：宽屏上不限宽会把「卡片信息」的双列拉到屏幕两端，
    // 标签和值之间隔着半个屏幕，眼睛要横扫才对得上——一个两列表格的
    // 可读性不该随窗口变宽而变差。
    // 不再自己设 max-w：宽度由三栏栅格分配。原先那条 max-w-3xl 是两栏时
    // 防止详情被拉到满屏两端的补丁，三栏之后它反而会在超宽屏上留出空隙。
    <section className="border-edge flex min-w-0 flex-col gap-4 rounded-md border p-4">
      <header className="flex flex-col items-center gap-1 text-center">
        <h2 className="flex items-center gap-2 text-lg font-semibold">
          <span className="min-w-0 break-all">
            {card.card_alias || card.card_id}
          </span>
          <Badge tone={cardStatusTone(card.status)}>
            {cardStatusLabel(card.status)}
          </Badge>
        </h2>
        <p className="text-fg-muted text-sm">{card.holder_name || "—"}</p>
      </header>

      <CardFace card={card} />
      <div className="flex justify-center">
        <CopyCardSecrets card={card} />
      </div>

      <div className="flex flex-wrap items-center justify-center gap-4">
        <div className="text-center">
          <p className="text-fg-muted text-xs">可用余额</p>
          <p className="text-2xl font-semibold tabular-nums">
            {formatMinorUnits(card.balance_minor, card.currency)}
          </p>
        </div>
        <CardActions card={card} onWrite={onWrite} onError={onError} />
      </div>

      {challenge?.code ? (
        <p className="border-edge rounded-md border p-2 text-center text-sm">
          待验证：
          <span className="font-mono text-base font-semibold">
            {challenge.code}
          </span>
          {challenge.expires_at ? (
            <span className="text-fg-muted">
              {" "}
              · {formatUtcTimestamp(challenge.expires_at)} 过期
            </span>
          ) : null}
        </p>
      ) : null}

      <div className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold">卡片信息</h3>
        <dl className="grid grid-cols-2 gap-3">
          <Field label="卡号" value={card.pan ?? card.mask} mono />
          <Field label="持卡人姓名" value={card.holder_name || "—"} />
          <Field label="有效期" value={card.expiry_mmyy ?? "—"} mono />
          <Field label="CVV" value={card.cvv ?? "—"} mono />
          {/* 验证码紧跟 CVV，与卡号→有效期→CVV→验证码的**填写次序**一致。
              上面那块高亮的「待验证」仍然留着——它带过期时间，而且要在人
              还没往下看信息栏时就抓住注意力；这里这一份是给正在逐项填表的
              人的，让眼睛不用往回跳。没有待验证时留空而不是「—」。 */}
          <Field label="验证码" value={challenge?.code ?? ""} mono />
          <Field label="账号" value={card.account} />
          <Field label="用途" value={card.owner_ref ?? "—"} />
          {/* 与 Infini 后台同名：那边就叫「持卡人邮箱」。 */}
          <Field label="持卡人邮箱" value={card.user_email ?? "—"} />
          <Field
            label="创建时间"
            value={card.issued_at ? formatUtcTimestamp(card.issued_at) : "—"}
          />
          <Field label="绑定账号" value={card.bound_account ?? "—"} />
          <Field label="订阅服务" value={card.service_name ?? "—"} />
          <Field
            label="订阅金额"
            value={
              card.subscription_amount
                ? `${card.subscription_amount}${card.subscription_cycle ? ` / ${cycleLabel(card.subscription_cycle)}` : ""}`
                : "—"
            }
          />
          <Field label="下次扣款日期" value={card.next_renewal_on ?? "—"} mono />
          <Field
            label="数据同步于"
            value={
              card.freshness.synced_at
                ? formatUtcTimestamp(card.freshness.synced_at)
                : "从未同步"
            }
          />
        </dl>
        {card.pan ? null : (
          <p className="text-fg-muted text-xs">
            卡面明文尚未拉取。卡要先变成 active，同步作业才拉得到；
            若你看不到卡号而别人看得到，是缺 card.reveal 权限。
          </p>
        )}
      </div>

      {/* 用途登记是**平台自己的**字段（这张卡给谁用、绑了哪个账号、
          什么时候续费），Infini 那边没有对应的东西。放在卡片信息下面，
          因为它填的正是上面那几行显示的值。 */}
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-semibold">用途登记</h3>
        <Dialog
          title="用途登记"
          description="这张卡给谁用、绑了哪个上游账号、什么时候续费。这些是平台自己的字段，不写回 Infini。"
          trigger={
            <Button variant="secondary" size="sm">
              登记用途
            </Button>
          }
        >
          <UsageForm card={card} onDone={onWrite} />
        </Dialog>
      </div>

      {/* 交易流水已分到第三栏（见 CardLedgerColumn）。中屏（lg 但非 xl）
          上没有第三栏，那时它显示在这一栏下方——由 CardLedgerColumn 自己的
          栅格位置决定，这里不再重复渲染。 */}
    </section>
  );
}

/** 第三栏：这张卡的交易流水。
 *
 *  与卡片详情分栏而不是叠在它下面：详情回答「这张卡是什么」，流水回答
 *  「这张卡发生过什么」——两件事都要看，但不该争同一块地方。原先流水挂在
 *  详情最底下，看一笔消费要先滚过整个卡片信息，而右边那半屏是空的。 */
function CardLedgerColumn({ card }: { card: CardItem }) {
  return (
    <section className="border-edge flex min-w-0 flex-col gap-2 rounded-md border p-4">
      <h3 className="text-sm font-semibold">交易流水</h3>
      {/* 表有七八列，让**表自己**横向滚，而不是把页面撑宽——
          页面横滚会让左边的卡片清单也跟着跑掉。 */}
      <div className="min-w-0 overflow-x-auto">
        <CardTransactions card={card} />
      </div>
    </section>
  );
}

/** 卡面。
 *
 *  用导航栏那套深色令牌（`nav-surface` / `nav-fg`）而不是自己调一个黑：
 *  仓库禁止硬编码颜色，而这是现成的、会跟着主题走的深色面。
 *  **不画发卡组织标志**——那是别人的商标，我们没有理由复制它。 */
function CardFace({ card }: { card: CardItem }) {
  return (
    <div className="bg-nav-surface text-nav-fg mx-auto flex aspect-[1.6/1] w-full max-w-xs flex-col justify-between rounded-lg p-4">
      <span className="text-nav-fg-muted text-xs">{card.account}</span>
      <span className="font-mono text-lg tracking-widest break-all">
        {card.pan ?? card.mask ?? "—"}
      </span>
      {/* 持卡人 / 有效期 / CVV 三格，与 Infini 卡面同一排布。
          在线支付要连着填这几样，卡面上没有就得往下翻到信息栏。 */}
      <span className="text-nav-fg-muted flex items-end justify-between gap-2 text-xs">
        <span className="flex min-w-0 flex-col">
          <span className="text-[0.65rem] uppercase">持卡人</span>
          <span className="truncate">{card.holder_name || "—"}</span>
        </span>
        <span className="flex shrink-0 flex-col">
          <span className="text-[0.65rem] uppercase">有效期</span>
          <span className="font-mono">{card.expiry_mmyy ?? "—"}</span>
        </span>
        <span className="flex shrink-0 flex-col">
          <span className="text-[0.65rem] uppercase">CVV</span>
          <span className="font-mono">{card.cvv ?? "—"}</span>
        </span>
      </span>
    </div>
  );
}

/** 一键复制卡号、有效期与 CVV。
 *
 *  在线支付要连着填这三样，分三次复制就要在页面和表单之间来回切三趟，
 *  而每一趟都是一次贴错位置的机会。
 *
 *  **明文没拉到就不渲染这个按钮**：一个复制出「—」的按钮比没有按钮更糟——
 *  人会以为复制成功了，直到粘进付款页才发现。 */
function CopyCardSecrets({ card }: { card: CardItem }) {
  const [copied, setCopied] = useState(false);
  if (!card.pan) return null;

  const text = [card.pan, card.expiry_mmyy ?? "", card.cvv ?? ""]
    .filter(Boolean)
    .join(" ");

  return (
    <Button
      variant="secondary"
      size="sm"
      aria-label="复制卡号、有效期与 CVV"
      onClick={() => {
        void navigator.clipboard?.writeText(text);
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1500);
      }}
    >
      {copied ? "已复制" : "复制卡号 / 有效期 / CVV"}
    </Button>
  );
}

/** 余额旁边的四个动作，与 Infini 的位置一致。
 *
 *  充值/赎回开对话框（它们要填金额与代币）；锁定/解锁一点即走；
 *  关停两步确认——不可逆的动作不该和其它三个一样一点就走。 */
function CardActions({
  card,
  onWrite,
  onError,
}: {
  card: CardItem;
  onWrite: (r: ActionResult) => void;
  onError: (e: unknown) => void;
}) {
  const [armed, setArmed] = useState<string | null>(null);

  const lockMutation = useMutation({
    mutationFn: (freeze: boolean) => {
      const params = {
        account: card.account,
        idempotency_key: crypto.randomUUID(),
        card_id: card.card_id,
      };
      return freeze ? freezeCard(params) : unfreezeCard(params);
    },
    onSuccess: (run) =>
      onWrite({ runId: run.runId, title: "已提交锁定/解锁请求" }),
    onError,
  });

  const deleteMutation = useMutation({
    mutationFn: (key: string) =>
      deleteCard({
        account: card.account,
        idempotency_key: key,
        card_id: card.card_id,
      }),
    onSuccess: (run) => {
      onWrite({ runId: run.runId, title: "已提交关停请求" });
      setArmed(null);
    },
    onError: (e) => {
      setArmed(null);
      onError(e);
    },
  });

  const gone = card.status === "pending_delete" || card.status === "deleted";

  return (
    <div className="flex flex-wrap items-center gap-2">
      <CardFundsDialog card={card} kind="topup" onDone={onWrite} />
      <CardFundsDialog card={card} kind="redeem" onDone={onWrite} />
      <Button
        variant="secondary"
        size="sm"
        disabled={lockMutation.isPending}
        onClick={() => lockMutation.mutate(!isCardLocked(card.status))}
      >
        {isCardLocked(card.status) ? "解锁" : "锁定"}
      </Button>
      {gone ? null : armed ? (
        <Button
          variant="danger"
          size="sm"
          disabled={deleteMutation.isPending}
          onClick={() => deleteMutation.mutate(armed)}
          title="再点一次将真的关停这张卡"
        >
          {deleteMutation.isPending ? "关停中…" : "确认关停"}
        </Button>
      ) : (
        <Button
          variant="secondary"
          size="sm"
          onClick={() => setArmed(crypto.randomUUID())}
          title="关停不可逆：卡会结清余额后删除，无法恢复"
        >
          关停
        </Button>
      )}
    </div>
  );
}

/** 卡片图标：清单每行最左边那个小卡片轮廓。
 *
 *  照 Infini 的行式来。它不承载任何信息，作用是给每一行一个固定的左锚点，
 *  让卡名的起始位置不随内容变化——一列几十张卡时，这种对齐比图标本身值钱。
 *  用 currentColor 的描边而不是实心块，免得它比卡名还抢眼。 */
function CardGlyph() {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className="text-fg-muted size-6 shrink-0"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
    >
      <rect x="2.5" y="5.5" width="19" height="13" rx="2" />
      <path d="M2.5 9.5h19" />
      <path d="M6 14.5h4" />
    </svg>
  );
}

/** 状态的文字色。
 *
 *  Infini 把状态显示成一行里的**彩色文字**而不是徽章。照搬：一列里每行都挂
 *  一个徽章会让整列都是色块，反而看不出哪张不正常；只有异常状态需要跳出来。
 *  未知取值走默认的灰，与 cardStatusLabel 的「原样显示」一致。 */
function statusTextClass(status: string): string {
  switch (cardStatusTone(status)) {
    case "success":
      return "text-success";
    case "danger":
      return "text-danger";
    case "warning":
      return "text-warning";
    default:
      return "text-fg-muted";
  }
}

/** 扣款周期的中文。未知取值原样显示，不归到已知分类里——
 *  静默归类会让第一个没见过的周期在最需要被看见的时候消失。 */
export function cycleLabel(cycle: string): string {
  const map: Record<string, string> = {
    monthly: "每月",
    yearly: "每年",
    weekly: "每周",
    other: "其它",
  };
  return map[cycle] ?? cycle;
}

function Field({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="min-w-0">
      <dt className="text-fg-muted text-xs">{label}</dt>
      <dd className={`text-sm break-all ${mono ? "font-mono" : ""}`}>
        {value}
      </dd>
    </div>
  );
}
