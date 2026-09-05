import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { formatUtcTimestamp } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, Input } from "@xingmang/ui-primitives";
import { useState } from "react";
import { useNavigate, useParams } from "react-router";
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
  const navigate = useNavigate();
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

  const cards = query.data?.cards ?? [];
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
    navigate(
      `/cards/${encodeURIComponent(card.account)}/${encodeURIComponent(card.card_id)}`,
      {
        replace: true,
      },
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
            还没有卡片。点右上角「开卡」创建第一张；在 Infini 后台直接建的卡
            会由同步作业在下一轮（5 分钟内）自动拉进来。
          </p>
        ) : (
          <div className="grid min-w-0 gap-3 lg:grid-cols-[minmax(18rem,22rem)_1fr]">
            <CardRail
              cards={cards}
              selected={selected}
              challenges={challenges}
              onSelect={select}
            />
            {selected ? (
              <CardPane
                card={selected}
                challenge={challenges.get(
                  `${selected.account}/${selected.card_id}`,
                )}
                onWrite={afterWrite}
                onError={setActionError}
              />
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
        <ul className="border-edge flex max-h-[36rem] min-w-0 flex-col overflow-y-auto rounded-md border">
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
                  className={`flex w-full min-w-0 flex-col gap-1 px-3 py-2 text-left hover:bg-surface-muted focus-visible:outline-2 focus-visible:outline-accent ${
                    active ? "bg-accent-soft" : ""
                  }`}
                >
                  <span className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-sm font-medium">
                      {c.card_alias || c.card_id}
                    </span>
                    <Badge tone={cardStatusTone(c.status)}>
                      {cardStatusLabel(c.status)}
                    </Badge>
                    {/* 有待验证的码就在清单里标出来——它只有几分钟有效，
                    藏在右栏里等人点开就来不及了。 */}
                    {challenge?.code ? (
                      <Badge tone="warning">验证码 {challenge.code}</Badge>
                    ) : null}
                  </span>
                  <span className="text-fg-muted flex min-w-0 items-center justify-between gap-2 text-xs">
                    <span className="truncate">{c.holder_name || "—"}</span>
                    <span className="font-mono whitespace-nowrap">
                      {c.mask ? `••${c.mask.slice(-4)}` : "—"}
                    </span>
                  </span>
                  <span className="text-fg-muted text-xs tabular-nums">
                    {formatMinorUnits(c.balance_minor, c.currency)}
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
          <Field label="账号" value={card.account} />
          <Field label="用途" value={card.owner_ref ?? "—"} />
          {/* 与 Infini 后台同名：那边就叫「持卡人邮箱」。 */}
          <Field label="持卡人邮箱" value={card.user_email ?? "—"} />
          <Field
            label="开卡时间"
            value={card.issued_at ? formatUtcTimestamp(card.issued_at) : "—"}
          />
          <Field label="绑定账号" value={card.bound_account ?? "—"} />
          <Field label="订阅服务" value={card.service_name ?? "—"} />
          <Field label="下次续费" value={card.next_renewal_on ?? "—"} mono />
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

      <div className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold">交易流水</h3>
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
      <span className="text-nav-fg-muted flex items-center justify-between text-xs">
        <span>{card.holder_name || "—"}</span>
        <span className="font-mono">{card.expiry_mmyy ?? "—"}</span>
      </span>
    </div>
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
