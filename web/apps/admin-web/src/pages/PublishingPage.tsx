import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { navItemByPath, navLabel, PageHeader, PageState } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select, Tabs } from "@xingmang/ui-primitives";
import { useMemo, useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import { listApprovals, type ApprovalItem } from "../api/approvals";
import {
  archivePublishingDraft,
  listPublishingAssets,
  listPublishingChannels,
  listPublishingDrafts,
  listPublishingRecords,
  savePublishingAsset,
  savePublishingChannel,
  savePublishingDraft,
  setPublishingChannelStatus,
  submitPublish,
  PUBLISHING_CHANNEL_STATUS_LABELS,
  PUBLISHING_DRAFT_STATUS_LABELS,
  PUBLISHING_PLATFORM_LABELS,
  PUBLISHING_PUBLISH_ACTION_ID,
  type PublishingChannel,
  type PublishingDraft,
  type PublishingPlatform,
  type PublishingRecord,
} from "../api/publishing";
import { ApiStateView } from "../components/ApiStateView";

const PUBLISHING_SUB_TABS = (navItemByPath("/ext/publishing")?.item.subTabs ?? []).map(
  (tab) => [tab.id, tab.label] as const,
);

const PUBLISHING_DEFAULT_SUB = "calendar";

/** 这一页最要紧的一句话，页头下面常驻。
 *
 *  挂在 Tabs 外面而不是塞进某一格：「排期与审批是真的，发出去还没接」是整页的
 *  事实，只写在一格里的话，落在别的格的人根本看不到（同 ChangesPage 的
 *  ChangesGate）。
 *
 *  **横幅文案里那个「哪些平台能发」不是写死的**：它来自服务端的
 *  `platforms_with_deliverer`。写死的那一刻，接上投递器的那天没人会想起来改它。 */
function DeliveryGate({ platformsWithDeliverer }: { platformsWithDeliverer: string[] }) {
  if (platformsWithDeliverer.length > 0) {
    return (
      <p
        role="status"
        className="rounded-md border border-edge bg-surface px-3 py-2 text-xs text-fg"
      >
        出站投递：已接入 {platformsWithDeliverer.join(" / ")}。其余平台仍只到审批为止。
      </p>
    );
  }
  return (
    <p
      role="status"
      className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
    >
      门禁：草稿、版本、素材、排期、渠道登记与发布审批都是真的，
      <strong>但平台还没有任何出站投递器</strong>
      ——审批通过并执行之后，内容不会被发送到 X 或任何外部社交平台，只会留下一条
      结果为「未投递」的发布记录。对接 X 的 Connector、OAuth、速率限制与失败重试
      是第二层，尚未开始。站内公告是另一条线：Sub2API 与 NewAPI 的公告接口都要求
      平台向它们发写请求，被 ADR-018（对这两个上游只建只读账号）与 ADR-021
      （写通道仅限平台作为客户购买服务的供应商）排除，需要产品负责人另立 ADR，
      因此这一页不提供站内公告渠道。
    </p>
  );
}

/** 内容发布页（/ext/publishing）。
 *
 *  ADMIN-IA §5.4 原本把扩展能力四页一律定为只读蓝图；产品负责人 2026-09-08
 *  推翻了这一条在这一页上的适用（§5.4.1），要求真建。本片交付第一层
 *  ——「内容的生命周期在平台内是真的」。 */
export function PublishingPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const activeSub = rawSub === null || rawSub.trim() === "" ? PUBLISHING_DEFAULT_SUB : rawSub;
  const known = PUBLISHING_SUB_TABS.some(([value]) => value === activeSub);

  // 渠道查询提在最上层：横幅要用它的 platforms_with_deliverer，而这条读数
  // 是整页的门禁事实，不该只在「渠道与账号」那一格才拉。
  const channels = useQuery({
    queryKey: ["publishing", "channels"],
    queryFn: ({ signal }) => listPublishingChannels({ signal }),
  });

  // 认不出来的 ?sub= 不静默回落到第一格：那会让一个拼错的地址看起来像正常
  // 页面，而人以为自己看的是别的东西（同 ChangesPage / OpsPage 的处理）。
  if (!known) {
    return (
      <section>
        <PageHeader
          title={navLabel("/ext/publishing")}
          description="内容发布的五格各自独立；未知地址不会静默回落到内容日历。"
        />
        <PageState
          kind="unavailable"
          title={`「${rawSub}」子页尚未接入`}
          description="请从已定义的内容发布子页中选择。"
          action={
            <Link
              to={`/ext/publishing?sub=${PUBLISHING_DEFAULT_SUB}`}
              className="text-sm font-medium text-accent hover:underline"
            >
              返回内容日历
            </Link>
          }
        />
      </section>
    );
  }

  const selectSub = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("sub", value);
    setSearchParams(next, { replace: true });
  };

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title={navLabel("/ext/publishing")}
        status={<Badge tone="warning">未接出站投递</Badge>}
        description="内容日历、草稿与素材、审批队列、渠道与账号、发布记录。"
      />
      <DeliveryGate platformsWithDeliverer={channels.data?.platformsWithDeliverer ?? []} />
      <Tabs
        value={activeSub}
        onValueChange={selectSub}
        items={PUBLISHING_SUB_TABS.map(([value, label]) => ({
          value,
          label,
          content: renderSubTab(value, label),
        }))}
      />
    </section>
  );
}

function renderSubTab(value: string, label: string): ReactNode {
  switch (value) {
    case "calendar":
      return <CalendarTab />;
    case "drafts":
      return <DraftsTab />;
    case "approvals":
      return <ApprovalQueueTab />;
    case "channels":
      return <ChannelsTab />;
    case "records":
      return <RecordsTab />;
    default:
      return (
        <PageState
          kind="unavailable"
          title={`「${label}」尚未实现`}
          description="导航数据与本页的子页签实现已经漂开，请先对齐。"
        />
      );
  }
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

/** 把 RFC3339 显示成本地时刻；空串显示成「—」。
 *
 *  显式区分「没有排期」与「排在某个时刻」：留一个空格子会让人分不清是没排
 *  还是没加载出来。 */
function showTime(value: string): string {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleString();
}

/** 排期日期（YYYY-MM-DD），用于内容日历分组。 */
function scheduleDay(value: string): string {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return "未知错误";
}

/** 一次写操作之后的回执条。**成功也要显示 run_id**（规格 §5.8）。 */
function ActionResultNote({ runId, error }: { runId?: string; error?: unknown }) {
  if (error) {
    return (
      <p role="alert" className="text-xs text-danger">
        失败：{errorMessage(error)}
      </p>
    );
  }
  if (!runId) return null;
  return (
    <p role="status" className="text-xs text-muted">
      已执行，action_run_id: {runId}
    </p>
  );
}

const TABLE_CLASS = "w-full border-collapse text-xs";
const TH_CLASS = "border-b border-edge px-2 py-1.5 text-left font-medium text-muted";
const TD_CLASS = "border-b border-edge px-2 py-1.5 align-top text-fg";

// ---------------------------------------------------------------------------
// 内容日历
// ---------------------------------------------------------------------------

/** 内容日历：按排期日期分组的已排期草稿。
 *
 *  刻意**不画月份网格**：网格的价值在于一眼看出疏密，而它要的是一整套
 *  「拖拽改期 / 跨月翻页 / 同日多条折叠」的交互，本片给不出。按日期分组的
 *  列表回答的是同一个问题（什么时候发什么），且不会在数据少的时候显示成
 *  一片空格子。 */
function CalendarTab() {
  const scheduled = useQuery({
    queryKey: ["publishing", "drafts", "SCHEDULED"],
    queryFn: ({ signal }) => listPublishingDrafts({ status: "SCHEDULED", signal }),
  });
  const channels = useQuery({
    queryKey: ["publishing", "channels"],
    queryFn: ({ signal }) => listPublishingChannels({ signal }),
  });

  const grouped = useMemo(() => {
    const map = new Map<string, PublishingDraft[]>();
    for (const draft of scheduled.data?.items ?? []) {
      const day = scheduleDay(draft.scheduled_at);
      const bucket = map.get(day);
      if (bucket) bucket.push(draft);
      else map.set(day, [draft]);
    }
    return [...map.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [scheduled.data]);

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title="内容日历"
        description="已排期的草稿按计划时间排列。排期是真实数据；到点不会自动发出去——平台没有出站投递器，也没有到点触发的任务。"
      />
      <ApiStateView
        isPending={scheduled.isPending}
        error={scheduled.error}
        onRetry={() => void scheduled.refetch()}
      >
        {grouped.length === 0 ? (
          <PageState
            kind="empty"
            title="还没有排期的内容"
            description="在「草稿与素材」里给一条草稿填上计划时间，它就会出现在这里。"
          />
        ) : (
          <div className="flex flex-col gap-3">
            {grouped.map(([day, drafts]) => (
              <div key={day} className="rounded-lg border border-edge bg-surface p-3">
                <p className="mb-2 text-xs font-medium text-fg">{day}</p>
                <ul className="flex flex-col gap-1.5">
                  {drafts.map((draft) => (
                    <li key={draft.id} className="flex flex-wrap items-baseline gap-2 text-xs">
                      <span className="text-muted">{showTime(draft.scheduled_at)}</span>
                      <span className="font-medium text-fg">{draft.title}</span>
                      <Badge tone="neutral">v{draft.current_version}</Badge>
                      <span className="text-muted">
                        {PUBLISHING_DRAFT_STATUS_LABELS[draft.status]}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </div>
        )}
      </ApiStateView>
      <p className="text-xs text-muted">
        渠道共 {channels.data?.items.length ?? 0} 个，其中能真的发出去的：
        {channels.data?.items.filter((c) => c.can_deliver).length ?? 0} 个。
      </p>
    </section>
  );
}

// ---------------------------------------------------------------------------
// 草稿与素材
// ---------------------------------------------------------------------------

function DraftsTab() {
  const queryClient = useQueryClient();
  const drafts = useQuery({
    queryKey: ["publishing", "drafts", "all"],
    queryFn: ({ signal }) => listPublishingDrafts({ signal }),
  });
  const assets = useQuery({
    queryKey: ["publishing", "assets"],
    queryFn: ({ signal }) => listPublishingAssets({ signal }),
  });

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["publishing"] });
  };

  const archive = useMutation({
    mutationFn: (draftId: string) => archivePublishingDraft(draftId),
    onSuccess: invalidate,
  });

  return (
    <section className="flex flex-col gap-4">
      <section className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <PageHeader
            title="草稿"
            description="每次保存落一条不可变修订；发布记录钉住的是某一版，事后改草稿不会改写已经提交的那一版。"
          />
          <DraftDialog onSaved={invalidate} />
        </div>
        <ApiStateView
          isPending={drafts.isPending}
          error={drafts.error}
          onRetry={() => void drafts.refetch()}
        >
          {drafts.data && drafts.data.items.length > 0 ? (
            <table className={TABLE_CLASS}>
              <thead>
                <tr>
                  <th className={TH_CLASS}>标题</th>
                  <th className={TH_CLASS}>状态</th>
                  <th className={TH_CLASS}>版本</th>
                  <th className={TH_CLASS}>计划时间</th>
                  <th className={TH_CLASS}>素材</th>
                  <th className={TH_CLASS}>更新时间</th>
                  <th className={TH_CLASS}>操作</th>
                </tr>
              </thead>
              <tbody>
                {drafts.data.items.map((draft) => (
                  <tr key={draft.id}>
                    <td className={TD_CLASS}>{draft.title}</td>
                    <td className={TD_CLASS}>{PUBLISHING_DRAFT_STATUS_LABELS[draft.status]}</td>
                    <td className={TD_CLASS}>v{draft.current_version}</td>
                    <td className={TD_CLASS}>{showTime(draft.scheduled_at)}</td>
                    <td className={TD_CLASS}>{draft.asset_ids.length}</td>
                    <td className={TD_CLASS}>{showTime(draft.updated_at)}</td>
                    <td className={TD_CLASS}>
                      <div className="flex flex-wrap gap-1.5">
                        <PublishDialog draft={draft} onSubmitted={invalidate} />
                        {draft.status === "ARCHIVED" ? null : (
                          <Button
                            variant="ghost"
                            onClick={() => archive.mutate(draft.id)}
                            disabled={archive.isPending}
                          >
                            归档
                          </Button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <PageState kind="empty" title="还没有草稿" description="点右上角「新建草稿」开始。" />
          )}
        </ApiStateView>
        <ActionResultNote error={archive.error} />
      </section>

      <section className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <PageHeader
            title="素材"
            description="素材是外部地址的引用，不是上传——平台没有对象存储。地址必须是 https，因为它会随对外内容一起公开。"
          />
          <AssetDialog onSaved={invalidate} />
        </div>
        <ApiStateView
          isPending={assets.isPending}
          error={assets.error}
          onRetry={() => void assets.refetch()}
        >
          {assets.data && assets.data.items.length > 0 ? (
            <table className={TABLE_CLASS}>
              <thead>
                <tr>
                  <th className={TH_CLASS}>名称</th>
                  <th className={TH_CLASS}>类型</th>
                  <th className={TH_CLASS}>地址</th>
                  <th className={TH_CLASS}>备注</th>
                </tr>
              </thead>
              <tbody>
                {assets.data.items.map((asset) => (
                  <tr key={asset.id}>
                    <td className={TD_CLASS}>{asset.name}</td>
                    <td className={TD_CLASS}>{asset.kind}</td>
                    <td className={TD_CLASS}>{asset.uri}</td>
                    <td className={TD_CLASS}>{asset.note || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <PageState kind="empty" title="还没有素材" description="素材引用登记后可以挂到草稿上。" />
          )}
        </ApiStateView>
      </section>
    </section>
  );
}

function DraftDialog({ onSaved }: { onSaved: () => void }) {
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [scheduledAt, setScheduledAt] = useState("");
  const [note, setNote] = useState("");

  const save = useMutation({
    mutationFn: () =>
      savePublishingDraft({
        title,
        body,
        // datetime-local 给的是不带时区的本地时刻；转成带偏移的 RFC3339，
        // 因为后端刻意拒绝不带时区的输入——「10:00」在哪个时区是运营的判断。
        scheduledAt: scheduledAt ? new Date(scheduledAt).toISOString() : "",
        note,
        assetIds: [],
      }),
    onSuccess: () => {
      onSaved();
      setOpen(false);
      setTitle("");
      setBody("");
      setScheduledAt("");
      setNote("");
    },
  });

  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      trigger={<Button>新建草稿</Button>}
      title="新建草稿"
      description="保存后落一条 v1 修订。填了计划时间就会进入内容日历。"
    >
      <div className="flex flex-col gap-3">
        <FormField label="标题" required>
          <Input value={title} onChange={(e) => setTitle(e.target.value)} />
        </FormField>
        <FormField label="正文">
          <Input value={body} onChange={(e) => setBody(e.target.value)} />
        </FormField>
        <FormField label="计划时间" hint="留空表示先不排期。">
          <Input
            type="datetime-local"
            value={scheduledAt}
            onChange={(e) => setScheduledAt(e.target.value)}
          />
        </FormField>
        <FormField label="这一版改了什么" hint="写进修订记录，给下一个人看。">
          <Input value={note} onChange={(e) => setNote(e.target.value)} />
        </FormField>
        <div className="flex items-center gap-2">
          <Button onClick={() => save.mutate()} disabled={save.isPending || title.trim() === ""}>
            保存
          </Button>
          <ActionResultNote error={save.error} />
        </div>
      </div>
    </Dialog>
  );
}

function AssetDialog({ onSaved }: { onSaved: () => void }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"image" | "video" | "link">("image");
  const [uri, setUri] = useState("");
  const [note, setNote] = useState("");

  const save = useMutation({
    mutationFn: () => savePublishingAsset({ name, kind, uri, note }),
    onSuccess: () => {
      onSaved();
      setOpen(false);
      setName("");
      setUri("");
      setNote("");
    },
  });

  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      trigger={<Button variant="secondary">登记素材</Button>}
      title="登记素材引用"
      description="登记的是外部地址，不是上传文件。"
    >
      <div className="flex flex-col gap-3">
        <FormField label="名称" required>
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </FormField>
        <FormField label="类型">
          <Select
            aria-label="素材类型"
            value={kind}
            onValueChange={(v) => setKind(v as "image" | "video" | "link")}
            options={[
              { value: "image", label: "图片" },
              { value: "video", label: "视频" },
              { value: "link", label: "链接" },
            ]}
          />
        </FormField>
        <FormField label="地址" required hint="必须是 https://，因为它会随对外内容一起公开。">
          <Input value={uri} onChange={(e) => setUri(e.target.value)} />
        </FormField>
        <FormField label="备注">
          <Input value={note} onChange={(e) => setNote(e.target.value)} />
        </FormField>
        <div className="flex items-center gap-2">
          <Button
            onClick={() => save.mutate()}
            disabled={save.isPending || name.trim() === "" || uri.trim() === ""}
          >
            保存
          </Button>
          <ActionResultNote error={save.error} />
        </div>
      </div>
    </Dialog>
  );
}

/** 提交发布：L3，一定被受理成审批单。
 *
 *  两件事在这个弹窗里说清：
 *   1. **必须填理由**——内核对 L2+ 要求非空 reason，它会原样进审批单；
 *   2. 提交成功的结局是「已受理为审批单 X」，**不是「已发布」**；即便审批
 *      通过并执行，内容也不会发出去（没有出站投递器）。 */
function PublishDialog({
  draft,
  onSubmitted,
}: {
  draft: PublishingDraft;
  onSubmitted: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [channelId, setChannelId] = useState("");
  const [reason, setReason] = useState("");
  const [approvalId, setApprovalId] = useState("");

  const channels = useQuery({
    queryKey: ["publishing", "channels"],
    queryFn: ({ signal }) => listPublishingChannels({ signal }),
    enabled: open,
  });
  const options = (channels.data?.items ?? [])
    .filter((c) => c.status === "ACTIVE")
    .map((c) => ({
      value: c.id,
      label: `${PUBLISHING_PLATFORM_LABELS[c.platform]} · ${c.handle}`,
    }));

  const submit = useMutation({
    mutationFn: () => submitPublish(draft.id, channelId, reason),
    onSuccess: (outcome) => {
      onSubmitted();
      // 两种结局都画出来。**不把「已受理」显示成成功**——那正是
      // XM-ACTION-REASON 修掉的那个 bug。
      setApprovalId(
        outcome.kind === "approval_pending"
          ? outcome.approvalRequestId || "（响应未带单号）"
          : "",
      );
    },
  });

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) setApprovalId("");
      }}
      trigger={<Button variant="secondary">提交发布</Button>}
      title={`提交发布：${draft.title}`}
      description="对外发布是不可逆动作，风险等级 L3——提交后会落一张审批单，由两个不同的人批准后才能执行。"
    >
      <div className="flex flex-col gap-3">
        <p className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg">
          即便审批通过并执行，内容<strong>也不会真的发出去</strong>
          ：平台还没有出站投递器，执行只会留下一条结果为「未投递」的发布记录。
        </p>
        <FormField label="发布到" required>
          <Select
            aria-label="渠道"
            value={channelId}
            onValueChange={setChannelId}
            options={options}
            placeholder={options.length === 0 ? "没有启用中的渠道" : "选择渠道"}
            disabled={options.length === 0}
          />
        </FormField>
        <FormField
          label="为什么要发这一篇"
          required
          hint="这句话会原样进审批单给审批人看，请自己写清楚。"
        >
          <Input value={reason} onChange={(e) => setReason(e.target.value)} />
        </FormField>
        <div className="flex items-center gap-2">
          <Button
            onClick={() => submit.mutate()}
            disabled={submit.isPending || channelId === "" || reason.trim() === ""}
          >
            提交审批
          </Button>
          {submit.error ? (
            <p role="alert" className="text-xs text-danger">
              失败：{errorMessage(submit.error)}
            </p>
          ) : null}
          {approvalId ? (
            <p role="status" className="text-xs text-muted">
              已受理为审批单 {approvalId}；动作尚未发生，去「审批队列」跟进。
            </p>
          ) : null}
        </div>
      </div>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// 审批队列
// ---------------------------------------------------------------------------

/** 审批队列直接复用审批中心（XM-0030），不另建一套。
 *
 *  这一格显示的是**真实的审批单**：内核受理 L3 发布时落的那些。筛选按
 *  action_id，不是按状态——「本页有哪些单」比「全平台有哪些待办」更贴题，
 *  后者在操作与审批页已经有了。
 *
 *  **投票与执行不在这里做**：那两个动作在审批中心自己的页面上，权限与资格
 *  由服务端裁决。在这里再开一套入口，等于把同一件事实现两遍。 */
function ApprovalQueueTab() {
  const approvals = useQuery({
    queryKey: ["publishing", "approvals"],
    queryFn: ({ signal }) => listApprovals({ limit: 200, signal }),
  });
  const mine = (approvals.data?.items ?? []).filter(
    (item: ApprovalItem) => item.action_id === PUBLISHING_PUBLISH_ACTION_ID,
  );

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title="审批队列"
        description="对外发布属于有外部影响的操作，必须人工审核，不会有自动放行。这里列出的是审批中心里由本页发起的单——投票与触发执行在审批中心页面完成。"
      />
      <ApiStateView
        isPending={approvals.isPending}
        error={approvals.error}
        onRetry={() => void approvals.refetch()}
      >
        {mine.length === 0 ? (
          <PageState
            kind="empty"
            title="还没有发布审批单"
            description="在「草稿与素材」里对一条草稿点「提交发布」，会在这里出现一张待批的单。"
          />
        ) : (
          <table className={TABLE_CLASS}>
            <thead>
              <tr>
                <th className={TH_CLASS}>单号</th>
                <th className={TH_CLASS}>提交人</th>
                <th className={TH_CLASS}>理由</th>
                <th className={TH_CLASS}>状态</th>
                <th className={TH_CLASS}>票数</th>
                <th className={TH_CLASS}>过期时间</th>
              </tr>
            </thead>
            <tbody>
              {mine.map((item) => (
                <tr key={item.id}>
                  <td className={TD_CLASS}>{item.id}</td>
                  <td className={TD_CLASS}>{item.requester_id}</td>
                  <td className={TD_CLASS}>{item.reason}</td>
                  <td className={TD_CLASS}>{item.status}</td>
                  <td className={TD_CLASS}>
                    {item.votes_cast} / {item.votes_required}
                  </td>
                  <td className={TD_CLASS}>{showTime(item.expires_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </ApiStateView>
    </section>
  );
}

// ---------------------------------------------------------------------------
// 渠道与账号
// ---------------------------------------------------------------------------

function ChannelsTab() {
  const queryClient = useQueryClient();
  const channels = useQuery({
    queryKey: ["publishing", "channels"],
    queryFn: ({ signal }) => listPublishingChannels({ signal }),
  });
  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["publishing"] });
  };
  const setStatus = useMutation({
    mutationFn: (input: { id: string; status: PublishingChannel["status"] }) =>
      setPublishingChannelStatus(input.id, input.status),
    onSuccess: invalidate,
  });

  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <PageHeader
          title="渠道与账号"
          description="登记是真的；凭据只登记引用（secret://…），明文不进库、不回前端、不进日志。「能否投递」一列由服务端回答。"
        />
        <ChannelDialog onSaved={invalidate} />
      </div>
      <ApiStateView
        isPending={channels.isPending}
        error={channels.error}
        onRetry={() => void channels.refetch()}
      >
        {channels.data && channels.data.items.length > 0 ? (
          <table className={TABLE_CLASS}>
            <thead>
              <tr>
                <th className={TH_CLASS}>渠道账号</th>
                <th className={TH_CLASS}>平台</th>
                <th className={TH_CLASS}>用途</th>
                <th className={TH_CLASS}>凭据引用</th>
                <th className={TH_CLASS}>能否投递</th>
                <th className={TH_CLASS}>状态</th>
                <th className={TH_CLASS}>操作</th>
              </tr>
            </thead>
            <tbody>
              {channels.data.items.map((channel) => (
                <tr key={channel.id}>
                  <td className={TD_CLASS}>
                    {channel.handle}
                    {channel.display_name ? (
                      <span className="text-muted"> · {channel.display_name}</span>
                    ) : null}
                  </td>
                  <td className={TD_CLASS}>{PUBLISHING_PLATFORM_LABELS[channel.platform]}</td>
                  <td className={TD_CLASS}>{channel.purpose || "—"}</td>
                  <td className={TD_CLASS}>
                    {channel.credential_ref_present ? (
                      <code className="text-xs">{channel.credential_ref}</code>
                    ) : (
                      <span className="text-muted">未登记</span>
                    )}
                  </td>
                  <td className={TD_CLASS}>
                    {channel.can_deliver ? (
                      <Badge tone="success">可投递</Badge>
                    ) : (
                      // 逐行写，而不是靠页头那条横幅：横幅读一次就被忽略，
                      // 每一行都写着「未接投递器」的表格骗不了人。
                      <span className="text-muted">未接投递器</span>
                    )}
                  </td>
                  <td className={TD_CLASS}>
                    {PUBLISHING_CHANNEL_STATUS_LABELS[channel.status]}
                  </td>
                  <td className={TD_CLASS}>
                    <div className="flex flex-wrap gap-1.5">
                      {channel.status === "ACTIVE" ? (
                        <Button
                          variant="ghost"
                          onClick={() => setStatus.mutate({ id: channel.id, status: "PAUSED" })}
                          disabled={setStatus.isPending}
                        >
                          暂停
                        </Button>
                      ) : (
                        <Button
                          variant="ghost"
                          onClick={() => setStatus.mutate({ id: channel.id, status: "ACTIVE" })}
                          disabled={setStatus.isPending}
                        >
                          启用
                        </Button>
                      )}
                      {channel.status === "RETIRED" ? null : (
                        <Button
                          variant="ghost"
                          onClick={() => setStatus.mutate({ id: channel.id, status: "RETIRED" })}
                          disabled={setStatus.isPending}
                        >
                          退役
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <PageState
            kind="empty"
            title="还没有登记渠道"
            description="登记一个账号之后，才能对草稿提交发布。"
          />
        )}
      </ApiStateView>
      <ActionResultNote error={setStatus.error} />
      <p className="text-xs text-muted">
        站内公告不在这个列表里：Sub2API 与 NewAPI 的公告接口都要求平台向它们发写
        请求，被 ADR-018 与 ADR-021 排除。列一个发不出去的渠道类型等于摆一个假入口。
      </p>
    </section>
  );
}

function ChannelDialog({ onSaved }: { onSaved: () => void }) {
  const [open, setOpen] = useState(false);
  const [platform, setPlatform] = useState<PublishingPlatform>("x");
  const [handle, setHandle] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [purpose, setPurpose] = useState("");
  const [credentialRef, setCredentialRef] = useState("");
  const [note, setNote] = useState("");

  const save = useMutation({
    mutationFn: () =>
      savePublishingChannel({ platform, handle, displayName, purpose, credentialRef, note }),
    onSuccess: () => {
      onSaved();
      setOpen(false);
      setHandle("");
      setDisplayName("");
      setPurpose("");
      setCredentialRef("");
      setNote("");
    },
  });

  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      trigger={<Button>登记渠道</Button>}
      title="登记渠道账号"
      description="登记的是「以后以谁的名义、用哪把钥匙发」。凭据本身不在这里填。"
    >
      <div className="flex flex-col gap-3">
        <FormField label="平台" required>
          <Select
            aria-label="平台"
            value={platform}
            onValueChange={(v) => setPlatform(v as PublishingPlatform)}
            options={[
              { value: "x", label: PUBLISHING_PLATFORM_LABELS.x },
              { value: "telegram", label: PUBLISHING_PLATFORM_LABELS.telegram },
              { value: "other", label: PUBLISHING_PLATFORM_LABELS.other },
            ]}
          />
        </FormField>
        <FormField label="账号标识" required hint="例：@xingmang">
          <Input value={handle} onChange={(e) => setHandle(e.target.value)} />
        </FormField>
        <FormField label="显示名">
          <Input value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
        </FormField>
        <FormField label="用途">
          <Input value={purpose} onChange={(e) => setPurpose(e.target.value)} />
        </FormField>
        <FormField
          label="凭据引用"
          hint="形如 secret://publishing-x/mkt-token。**不要粘贴 Token 本身**——服务端会当场拒绝，凭据要先在「密钥引用」里登记。"
        >
          <Input value={credentialRef} onChange={(e) => setCredentialRef(e.target.value)} />
        </FormField>
        <FormField label="备注">
          <Input value={note} onChange={(e) => setNote(e.target.value)} />
        </FormField>
        <div className="flex items-center gap-2">
          <Button onClick={() => save.mutate()} disabled={save.isPending || handle.trim() === ""}>
            保存
          </Button>
          <ActionResultNote error={save.error} />
        </div>
      </div>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// 发布记录
// ---------------------------------------------------------------------------

function RecordsTab() {
  const records = useQuery({
    queryKey: ["publishing", "records"],
    queryFn: ({ signal }) => listPublishingRecords({ signal }),
  });
  const channels = useQuery({
    queryKey: ["publishing", "channels"],
    queryFn: ({ signal }) => listPublishingChannels({ signal }),
  });
  const drafts = useQuery({
    queryKey: ["publishing", "drafts", "all"],
    queryFn: ({ signal }) => listPublishingDrafts({ signal }),
  });

  const channelLabel = (id: string) => {
    const hit = channels.data?.items.find((c) => c.id === id);
    return hit ? `${PUBLISHING_PLATFORM_LABELS[hit.platform]} · ${hit.handle}` : id;
  };
  const draftLabel = (record: PublishingRecord) => {
    const hit = drafts.data?.items.find((d) => d.id === record.draft_id);
    return hit ? `${hit.title}（v${record.draft_version}）` : `${record.draft_id} v${record.draft_version}`;
  };

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title="发布记录"
        description="记录真实发生过什么：谁在什么时候请求把哪一版发到哪个渠道。它不是「已发出」的清单。"
      />
      <ApiStateView
        isPending={records.isPending}
        error={records.error}
        onRetry={() => void records.refetch()}
      >
        {records.data && records.data.items.length > 0 ? (
          <table className={TABLE_CLASS}>
            <thead>
              <tr>
                <th className={TH_CLASS}>内容</th>
                <th className={TH_CLASS}>渠道</th>
                <th className={TH_CLASS}>计划时间</th>
                <th className={TH_CLASS}>提交人</th>
                <th className={TH_CLASS}>结果</th>
                <th className={TH_CLASS}>平台返回编号</th>
                <th className={TH_CLASS}>互动数据</th>
                <th className={TH_CLASS}>时间</th>
              </tr>
            </thead>
            <tbody>
              {records.data.items.map((record) => (
                <tr key={record.id}>
                  <td className={TD_CLASS}>{draftLabel(record)}</td>
                  <td className={TD_CLASS}>{channelLabel(record.channel_id)}</td>
                  <td className={TD_CLASS}>{showTime(record.scheduled_at)}</td>
                  <td className={TD_CLASS}>{record.requested_by}</td>
                  <td className={TD_CLASS}>
                    {record.delivered ? (
                      <Badge tone="success">已投递</Badge>
                    ) : (
                      <>
                        <Badge tone="warning">未投递</Badge>
                        {/* 服务端给的原文，前端不复写——改软它就成了假话。 */}
                        <p className="mt-1 text-xs text-muted">{record.detail}</p>
                      </>
                    )}
                  </td>
                  <td className={TD_CLASS}>{record.external_ref || "—"}</td>
                  {/* 互动数据：没有出站投递就没有回读。留列不留数（同「上游管理」
                      那几列的处理）。 */}
                  <td className={TD_CLASS}>
                    <span className="text-muted">—</span>
                  </td>
                  <td className={TD_CLASS}>{showTime(record.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <PageState
            kind="empty"
            title="还没有发布记录"
            description="一条记录来自一次通过审批并执行的发布提交。"
          />
        )}
      </ApiStateView>
      <p className="text-xs text-muted">
        「互动数据」一列留列不留数：它要从外部平台回读，而平台既没有出站投递也没有
        回读通道。接上第二层之后这一列才有来源。
      </p>
    </section>
  );
}
