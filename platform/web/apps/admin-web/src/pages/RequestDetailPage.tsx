import { useQuery } from "@tanstack/react-query";
import { FreshnessBadge, FreshnessNote, PageHeader, formatLocalTimestamp } from "@xingmang/ui-admin";
import { Badge, Button, EmptyState } from "@xingmang/ui-primitives";
import { useState } from "react";
import { Link, useParams } from "react-router";
import {
  getPlatformRequestContent,
  type RequestMessage,
  type RequestPayload,
  type RequestSummary,
} from "../api/requests";
import { ApiStateView } from "../components/ApiStateView";
import { appDemoDataConfig, DEMO_BANNER_TEXT, shouldShowDemoBanner } from "../lib/demoData";
import { platformHasRequests } from "../lib/platforms";
import {
  describeRole,
  describeStatus,
  formatBytes,
  formatMillis,
  formatTokens,
  formatTTFB,
  formatUsername,
  MISSING_VALUE_TEXT,
  truncationNote,
  utf8Bytes,
} from "../lib/requests";

/** 请求详情**完整页**（交接文档 §9.4 + §11.4「核心对象用完整详情页」）。
 *
 *  三条产品纪律直接可见：
 *
 *    分角色渲染   system/user/assistant 各自成气泡，一眼看出谁说了什么；
 *    分级截断     超长内容折叠，展开是一次显式动作，且始终标出「共多少」；
 *    **默认无导出** 页面上没有任何下载/复制全文按钮。这是有意与 reqlog 自带
 *                控制台的「下载原文」不同——那条路绕开平台的权限与审计。
 *
 *  另外：**打开这一页就会写一条审计事件**（后端 request.content.viewed）。
 *  所以这一页不做预取、不做自动刷新——每刷一次就多一条「谁看了什么」的记录，
 *  而那份清单要能被人读。 */
export function RequestDetailPage() {
  const params = useParams();
  const platform = params.serviceType ?? "";
  const requestId = params.requestId ?? "";

  // 平台压根没有请求数据时连问都不问：后端会 404，但那个 404 的文案说的是
  // 「没有这条记录」，而真正的原因是「这个平台不在抄录范围内」。
  // 更要紧的是这条请求**本该是一次高敏读取**——不该为了拿一个注定的 404
  // 而发出去（见下方 platformHasRequests 的早返回）
  const supported = platformHasRequests(platform);

  const query = useQuery({
    queryKey: ["platform-request-content", platform, requestId],
    queryFn: ({ signal }) => getPlatformRequestContent(platform, requestId, { signal }),
    enabled: supported,
    // 不自动重取：每次读取都在审计链上留一条记录。窗口切回来就自动多留一条，
    // 会让「谁看过什么」这份清单里混进一堆没人真的在看的条目
    refetchOnWindowFocus: false,
    // 权限不足、记录不存在，重试多少次都是同一个答案（见 client.ts 的 retryable）
    retry: false,
  });

  const backTo = `/platforms/${platform}?tab=requests`;
  const content = query.data;

  if (!supported) {
    return (
      <section>
        <PageHeader title="请求详情" />
        <EmptyState
          title="这个平台没有请求数据"
          description={`请求审计只覆盖 NewAPI 与 Sub2API 的调用；${platform || "该平台"} 不在抄录范围内。`}
          action={
            <Link to={backTo} className="text-sm font-medium text-accent hover:underline">
              返回平台
            </Link>
          }
        />
      </section>
    );
  }

  return (
    <section>
      <PageHeader
        title="请求详情"
        description={
          <>
            <span className="font-mono">{requestId}</span>
            {" · "}
            <Link to={backTo} className="text-accent hover:underline">
              返回请求列表
            </Link>
          </>
        }
        actions={content ? <FreshnessBadge freshness={content.freshness} /> : undefined}
      />
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {content === undefined ? null : (
          <div className="flex flex-col gap-4">
            {shouldShowDemoBanner([content.dataSource], appDemoDataConfig) ? (
              <div
                role="status"
                className="flex items-center gap-2 rounded-md border-2 border-warning bg-warning/15 px-3 py-2 text-sm font-semibold text-fg"
              >
                <span aria-hidden="true">⚠</span>
                <span>{DEMO_BANNER_TEXT}——本页对话为样本，不是真实用户与模型的往来</span>
              </div>
            ) : null}

            <SummaryCard summary={content.summary} freshnessNote={content.freshness} />

            <Conversation
              messages={content.messages}
              messagesParsed={content.messagesParsed}
              finalReply={content.finalReply}
              finalReplyTruncated={content.finalReplyTruncated}
              finalReplyBytes={content.finalReplyBytes}
            />

            <RawPayloads request={content.rawRequest} response={content.rawResponse} />

            {/* 「默认禁止导出」写在页面上而不是只写在代码注释里：
                找不到下载按钮的人会以为是没做，而不是有意不做 */}
            <p className="text-xs text-fg-muted">
              本页不提供导出。请求正文属于高敏数据，每次查看都已记入审计（
              <Link to="/audit" className="text-accent hover:underline">
                审计事件
              </Link>
              ）。确需导出请走变更单。
            </p>
          </div>
        )}
      </ApiStateView>
    </section>
  );
}

function SummaryCard({
  summary,
  freshnessNote,
}: {
  summary: RequestSummary;
  freshnessNote: React.ComponentProps<typeof FreshnessNote>["freshness"];
}) {
  const status = describeStatus(summary.status);
  return (
    <div className="rounded-lg border border-edge bg-surface p-3 shadow-sm">
      <dl className="grid grid-cols-2 gap-x-6 gap-y-2 sm:grid-cols-3 lg:grid-cols-4">
        <Field label="发生时间">
          <span className="tabular-nums">{formatLocalTimestamp(summary.occurred_at)}</span>
        </Field>
        <Field label="用户">
          <span title={`令牌前缀 ${summary.token_prefix || MISSING_VALUE_TEXT}`}>
            {formatUsername(summary.username)}
          </span>
        </Field>
        <Field label="模型">{summary.model || MISSING_VALUE_TEXT}</Field>
        <Field label="平台">{summary.source}</Field>
        <Field label="状态">
          <Badge tone={status.tone}>{status.label}</Badge>
          {summary.stream ? (
            <Badge tone="neutral" className="ml-1" title="SSE 流式响应">
              流式
            </Badge>
          ) : null}
        </Field>
        <Field label="耗时">
          <span className="tabular-nums">{formatMillis(summary.duration_ms)}</span>
        </Field>
        <Field label="首字节" hint="「—」表示未记录，与 0 ms（缓存命中）不是一回事">
          <span className="tabular-nums">{formatTTFB(summary.ttfb_ms)}</span>
        </Field>
        <Field label="Token" hint="输入 / 输出 / 缓存">
          <span className="tabular-nums">{formatTokens(summary)}</span>
        </Field>
        <Field label="来源 IP" hint="末段已脱敏">
          <span className="font-mono text-xs">{summary.client_ip || MISSING_VALUE_TEXT}</span>
        </Field>
        <Field label="上游请求 ID" hint="用它去站内日志里对同一次调用">
          <span className="font-mono text-xs break-all">
            {summary.upstream_request_id || MISSING_VALUE_TEXT}
          </span>
        </Field>
      </dl>
      <div className="mt-2 border-t border-edge pt-2">
        <FreshnessNote freshness={freshnessNote} />
      </div>
    </div>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-fg-muted" title={hint}>
        {label}
      </dt>
      <dd className="text-sm text-fg">{children}</dd>
    </div>
  );
}

function Conversation({
  messages,
  messagesParsed,
  finalReply,
  finalReplyTruncated,
  finalReplyBytes,
}: {
  messages: RequestMessage[];
  messagesParsed: boolean;
  finalReply: string;
  finalReplyTruncated: boolean;
  finalReplyBytes: number;
}) {
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-sm font-semibold text-fg">对话</h3>
      {!messagesParsed ? (
        // 「请求体里就没有消息」与「我们没解析出来」必须分开：后者要让人去看
        // 下面的原始载荷，前者不用。画成同一句「暂无对话」两种情况都会被忽略
        <EmptyState
          title="请求体里没有可识别的对话结构"
          description="这可能是嵌入、图像或非对话类调用；也可能是上游改了请求格式。原文见下方「原始载荷」。"
        />
      ) : messages.length === 0 ? (
        <EmptyState title="这次请求没有消息" description="请求体里的 messages 是空数组。" />
      ) : (
        <ol className="flex flex-col gap-2">
          {messages.map((message, index) => (
            <MessageBubble key={`${message.role}-${index}`} message={message} />
          ))}
        </ol>
      )}

      <h3 className="mt-2 text-sm font-semibold text-fg">最终回复</h3>
      {finalReply.trim() === "" ? (
        <EmptyState
          title="没有回复内容"
          description="失败的请求通常如此；流式请求中断时也会这样。原始响应见下方。"
        />
      ) : (
        <div className="rounded-lg border border-edge bg-surface p-3">
          <LongText
            text={finalReply}
            truncated={finalReplyTruncated}
            originalBytes={finalReplyBytes}
          />
        </div>
      )}
    </section>
  );
}

/** 一条消息的气泡。
 *
 *  角色靠徽章而不是靠左右对齐来区分：这一页可能有 system / user / assistant /
 *  tool 四种以上角色，左右两侧摆不下，而且运营读的是「谁说了什么」，
 *  不是在还原一个聊天软件的样子。 */
function MessageBubble({ message }: { message: RequestMessage }) {
  const role = describeRole(message.role);
  return (
    <li className="rounded-lg border border-edge bg-surface p-3">
      <div className="mb-1 flex items-center gap-2">
        <Badge tone={role.tone}>{role.label}</Badge>
        <span className="text-xs text-fg-muted tabular-nums">
          {formatBytes(message.original_bytes)}
        </span>
      </div>
      <LongText
        text={message.content}
        truncated={message.truncated}
        originalBytes={message.original_bytes}
      />
    </li>
  );
}

/** 超过这个字符数的内容默认折叠。
 *
 *  这是**显示层**的分级，与后端按字节的硬截断是两回事：后端那一刀决定
 *  「多少内容离开了服务器」（安全边界），这一刀只决定「首屏铺多长」。
 *  两者都要——只有后端截断的话，一条 60 KiB 的消息会把整页顶到看不见头。 */
const COLLAPSE_THRESHOLD_CHARS = 800;

/** 分级截断渲染：短的直接显示，长的先折叠、可展开、始终标出「共多少」。 */
function LongText({
  text,
  truncated,
  originalBytes,
}: {
  text: string;
  truncated: boolean;
  originalBytes: number;
}) {
  const [expanded, setExpanded] = useState(false);
  const isLong = text.length > COLLAPSE_THRESHOLD_CHARS;
  const shown = isLong && !expanded ? `${text.slice(0, COLLAPSE_THRESHOLD_CHARS)}…` : text;
  const note = truncationNote(truncated, originalBytes, utf8Bytes(text));

  return (
    <div className="flex flex-col gap-1">
      {/* whitespace-pre-wrap + break-words：对话里有代码块与超长 URL，
          不换行会把表格/卡片撑出页面（§11.2 只允许容器内滚动） */}
      <p className="whitespace-pre-wrap break-words text-sm text-fg">{shown}</p>
      {isLong ? (
        <Button
          variant="ghost"
          size="sm"
          className="self-start"
          aria-expanded={expanded}
          onClick={() => setExpanded(!expanded)}
        >
          {expanded ? "收起" : `展开全部（${formatBytes(utf8Bytes(text))}）`}
        </Button>
      ) : null}
      {/* 服务端截断的提示与前端折叠分开显示：一个说「你看到的不是全部，
          而且平台也没有更多了」，另一个只说「点一下能看完」。
          混成一句会让人以为展开就能看到完整原文 */}
      {note ? <p className="text-xs font-medium text-warning">{note}</p> : null}
    </div>
  );
}

function RawPayloads({
  request,
  response,
}: {
  request: RequestPayload;
  response: RequestPayload;
}) {
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-sm font-semibold text-fg">原始载荷</h3>
      <p className="text-xs text-fg-muted">
        分角色对话是**解析结果**，这里是事实。上游协议变化或非对话类调用时，
        只有原文说得清发生了什么。
      </p>
      <RawPayloadBlock title="请求" payload={request} />
      <RawPayloadBlock title="响应" payload={response} />
    </section>
  );
}

function RawPayloadBlock({ title, payload }: { title: string; payload: RequestPayload }) {
  const [expanded, setExpanded] = useState(false);
  const note = truncationNote(payload.truncated, payload.original_bytes, utf8Bytes(payload.body));

  return (
    <div className="rounded-lg border border-edge bg-surface">
      <div className="flex items-center justify-between gap-2 border-b border-edge px-3 py-2">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-fg">{title}</span>
          <span className="font-mono text-xs text-fg-muted">
            {payload.content_type || MISSING_VALUE_TEXT}
          </span>
          <span className="text-xs text-fg-muted tabular-nums">
            {formatBytes(payload.original_bytes)}
          </span>
        </div>
        <Button
          variant="ghost"
          size="sm"
          aria-expanded={expanded}
          onClick={() => setExpanded(!expanded)}
        >
          {expanded ? "收起" : "展开"}
        </Button>
      </div>
      {expanded ? (
        <div className="flex flex-col gap-1 p-3">
          {/* 溢出只发生在这个容器里，不撑宽页面（§11.2） */}
          <pre className="max-h-96 overflow-auto rounded-md bg-surface-muted p-2 font-mono text-xs text-fg">
            {payload.body || MISSING_VALUE_TEXT}
          </pre>
          {note ? <p className="text-xs font-medium text-warning">{note}</p> : null}
        </div>
      ) : null}
    </div>
  );
}
