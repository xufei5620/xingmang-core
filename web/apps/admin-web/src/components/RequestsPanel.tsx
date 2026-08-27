import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  FreshnessBadge,
  formatLocalTimestamp,
  FreshnessNote,
  PageState,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge, Button, Input, Select } from "@xingmang/ui-primitives";
import { useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import {
  listPlatformRequests,
  REQUEST_PAGE_SIZE,
  type RequestStatusFilter,
  type RequestSummary,
} from "../api/requests";
import { appDemoDataConfig, DEMO_BANNER_TEXT, shouldShowDemoBanner } from "../lib/demoData";
import {
  describeStatus,
  formatMillis,
  formatTokens,
  formatTTFB,
  formatUsername,
  isUnmappedUser,
  MISSING_VALUE_TEXT,
  retentionNote,
} from "../lib/requests";
import { ApiStateView } from "./ApiStateView";


/** 状态筛选的选项。空串是「全部」——Radix Select 不接受空串作为 value，
 *  所以用一个显式的哨兵值，在读写两侧各翻译一次。 */
const STATUS_ALL = "all";
const STATUS_OPTIONS = [
  { value: STATUS_ALL, label: "全部状态" },
  { value: "success", label: "仅成功（2xx）" },
  { value: "error", label: "仅失败（非 2xx）" },
];

/** 请求列表（交接文档 §9.4）。
 *
 *  只显示元数据：时间、用户、模型、来源、状态、耗时、Token。**没有摘要列**
 *  ——§9.4 说的「脱敏摘要」需要从正文截出一段，而截一段正文出来放进列表，
 *  等于让 request.read 这一档权限顺带看到对话内容的开头。那正是本切片要
 *  分开的两件事，所以摘要留在详情页（见 PR 描述的 follow_ups）。
 *
 *  筛选在前、搜索在后（§11.3），且筛选条件进 URL Search Params——
 *  一次筛出来的结果要能贴给同事。 */
export function RequestsPanel({ platform }: { platform: string }) {
  const [searchParams, setSearchParams] = useSearchParams();
  // 游标是页内状态，不进 URL：它是不透明字符串，贴给同事也复现不出同一页
  // （数据一直在写入），而「第几页」本来就不是一个值得分享的东西
  const [cursor, setCursor] = useState("");
  const [cursorStack, setCursorStack] = useState<string[]>([]);

  const username = searchParams.get("username") ?? "";
  const model = searchParams.get("model") ?? "";
  const status = (searchParams.get("status") ?? "") as RequestStatusFilter;
  const since = searchParams.get("since") ?? "";
  const until = searchParams.get("until") ?? "";

  const query = useQuery({
    queryKey: ["platform-requests", platform, { username, model, status, since, until, cursor }],
    queryFn: ({ signal }) =>
      listPlatformRequests(platform, {
        username,
        model,
        status,
        since,
        until,
        cursor,
        limit: REQUEST_PAGE_SIZE,
        signal,
      }),
  });

  /** 改筛选条件时回到第一页：留在第 3 页的游标对一组新条件毫无意义，
   *  用它取回来的那一页与筛选结果没有任何关系。 */
  const setFilter = (key: string, value: string) => {
    const next = new URLSearchParams(searchParams);
    if (value.trim() === "") next.delete(key);
    else next.set(key, value);
    setSearchParams(next, { replace: true });
    setCursor("");
    setCursorStack([]);
  };

  const clearFilters = () => {
    const next = new URLSearchParams(searchParams);
    for (const key of ["username", "model", "status", "since", "until"]) next.delete(key);
    setSearchParams(next, { replace: true });
    setCursor("");
    setCursorStack([]);
  };

  const hasFilters = [username, model, status, since, until].some((v) => v !== "");
  const page = query.data;
  const isDemo =
    page !== undefined && shouldShowDemoBanner([page.dataSource], appDemoDataConfig);

  return (
    <section className="flex flex-col gap-3">
      {/* 演示数据横幅：与全局横幅同一套判定（lib/demoData），但挂在这一页上。
          全局横幅看的是指标 source，而请求详情不走指标表——它的来源由读取
          结果自己带上。这一页尤其不能分不清真假：一屏编造的用户对话与真实
          问答长得一模一样，而有人会照着它回复客诉 */}
      {isDemo ? (
        <div
          role="status"
          className="flex items-center gap-2 rounded-md border-2 border-warning bg-warning/15 px-3 py-2 text-sm font-semibold text-fg"
        >
          <span aria-hidden="true">⚠</span>
          <span>{DEMO_BANNER_TEXT}——下列请求与对话内容均为样本，不是真实用户数据</span>
        </div>
      ) : null}

      <RequestFilters
        username={username}
        model={model}
        status={status}
        since={since}
        until={until}
        hasFilters={hasFilters}
        onChange={setFilter}
        onClear={clearFilters}
      />

      <div className="flex items-start justify-between gap-3">
        <p className="text-xs text-fg-muted">
          {page ? retentionNote(page.retentionDays) : "逐条查看用户的真实请求与模型返回。"}
        </p>
        {page ? <FreshnessBadge freshness={page.freshness} /> : null}
      </div>

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {page === undefined ? null : (
          <div className="flex flex-col gap-3">
            <div className="rounded-lg border border-edge bg-surface p-3">
              <FreshnessNote freshness={page.freshness} />
              <p className="text-xs text-fg-muted">
                来源 {page.dataSource || MISSING_VALUE_TEXT} · 本页 {page.items.length} 条
                {hasFilters ? " · 已筛选" : null}
              </p>
            </div>

            <RequestTable
              platform={platform}
              rows={page.items}
              emptyState={
                <PageState
                  kind="empty"
                  title={hasFilters ? "没有符合条件的请求" : "这个平台还没有请求记录"}
                  description={
                    hasFilters
                      ? "换个条件，或清除筛选看全部。也可能是这段时间已经出了保留窗口。"
                      : "请求审计系统还没有抄到这个平台的调用；也可能这条链路尚未接通。"
                  }
                  action={
                    hasFilters ? (
                      <Button variant="secondary" size="sm" onClick={clearFilters}>
                        清除筛选
                      </Button>
                    ) : undefined
                  }
                />
              }
              footerExtra={
                <Pager
                  hasPrev={cursorStack.length > 0}
                  hasNext={page.nextCursor !== ""}
                  onPrev={() => {
                    const stack = [...cursorStack];
                    const prev = stack.pop() ?? "";
                    setCursorStack(stack);
                    setCursor(prev);
                  }}
                  onNext={() => {
                    setCursorStack([...cursorStack, cursor]);
                    setCursor(page.nextCursor);
                  }}
                />
              }
            />
          </div>
        )}
      </ApiStateView>
    </section>
  );
}

function RequestFilters({
  username,
  model,
  status,
  since,
  until,
  hasFilters,
  onChange,
  onClear,
}: {
  username: string;
  model: string;
  status: RequestStatusFilter;
  since: string;
  until: string;
  hasFilters: boolean;
  onChange: (key: string, value: string) => void;
  onClear: () => void;
}) {
  return (
    <div className="flex flex-wrap items-end gap-3 rounded-lg border border-edge bg-surface p-3">
      <FilterField label="用户名" hint="精确匹配，不是模糊搜索">
        <Input
          value={username}
          placeholder="如 zhang.wei"
          onChange={(e) => onChange("username", e.target.value)}
          className="w-40"
        />
      </FilterField>
      <FilterField label="模型" hint="精确匹配">
        <Input
          value={model}
          placeholder="如 gpt-4o"
          onChange={(e) => onChange("model", e.target.value)}
          className="w-48"
        />
      </FilterField>
      <FilterField label="状态">
        <Select
          aria-label="按状态筛选"
          options={STATUS_OPTIONS}
          value={status === "" ? STATUS_ALL : status}
          onValueChange={(v) => onChange("status", v === STATUS_ALL ? "" : v)}
          className="w-40"
        />
      </FilterField>
      <FilterField label="起始时间" hint="本地时区，含该时刻">
        <Input
          type="datetime-local"
          value={toLocalInput(since)}
          onChange={(e) => onChange("since", fromLocalInput(e.target.value))}
          className="w-52"
        />
      </FilterField>
      <FilterField label="结束时间" hint="本地时区，不含该时刻">
        <Input
          type="datetime-local"
          value={toLocalInput(until)}
          onChange={(e) => onChange("until", fromLocalInput(e.target.value))}
          className="w-52"
        />
      </FilterField>
      {/* 无结果时要有明确清除入口（§11.3）。一直显示而不是只在无结果时显示：
          筛完还有结果、但人想推翻重来的时候同样需要它 */}
      {hasFilters ? (
        <Button variant="secondary" size="sm" onClick={onClear}>
          清除筛选
        </Button>
      ) : null}
    </div>
  );
}

function FilterField({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-xs font-medium text-fg-muted" title={hint}>
        {label}
      </span>
      {children}
    </label>
  );
}

/** RFC3339（UTC）→ `<input type="datetime-local">` 要的本地时间串。
 *
 *  两边转换都走这一对函数：URL 里存 UTC（可分享、无歧义），输入框显示本地
 *  （人按自己的时区想问题）。混着来的话，同一个链接在两个时区的人手里
 *  会筛出不同的区间。 */
function toLocalInput(rfc3339: string): string {
  if (rfc3339 === "") return "";
  const d = new Date(rfc3339);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fromLocalInput(local: string): string {
  if (local === "") return "";
  const d = new Date(local);
  if (Number.isNaN(d.getTime())) return "";
  return d.toISOString();
}

function requestColumns(platform: string): DataTableColumn<RequestSummary>[] {
  return [
    {
      id: "time",
      header: "时间",
      primary: true,
      value: (row) => row.occurred_at,
      cell: (row) => (
        <span className="tabular-nums whitespace-nowrap">
          {formatLocalTimestamp(row.occurred_at)}
          {row.stream ? <p className="text-xs text-fg-muted">流式</p> : null}
        </span>
      ),
    },
    {
      id: "username",
      header: "用户",
      value: (row) => row.username,
      cell: (row) =>
        isUnmappedUser(row.username) ? (
          <span
            className="text-fg-muted"
            title={`令牌 ${row.token_prefix} 没有映射到用户名——不是「没有用户」，是没查到它叫什么`}
          >
            {formatUsername(row.username)}
          </span>
        ) : (
          <span className="font-medium">{row.username}</span>
        ),
    },
    {
      id: "model",
      header: "模型",
      value: (row) => row.model,
      cell: (row) => row.model || MISSING_VALUE_TEXT,
    },
    {
      id: "status",
      header: "状态",
      value: (row) => describeStatus(row.status).label,
      // 按状态码排，不按文案：「成功」「失败」按中文比较排出来没有意义
      sortAs: (row) => row.status,
      cell: (row) => <StatusBadge status={row.status} />,
    },
    {
      id: "duration",
      header: "耗时",
      numeric: true,
      value: (row) => row.duration_ms,
      cell: (row) => formatMillis(row.duration_ms),
    },
    {
      id: "ttfb",
      header: "首字节",
      numeric: true,
      headerTitle:
        "首字节时间。「—」表示上游没记（通常是非流式请求），与 0 ms（缓存命中）不是一回事",
      value: (row) => row.ttfb_ms,
      cell: (row) => formatTTFB(row.ttfb_ms),
    },
    {
      id: "tokens",
      header: "Token",
      numeric: true,
      headerTitle: "输入 / 输出 / 缓存",
      // 三个数拼成的一格没有单一排序键，所以只显示、不排序
      cell: (row) => formatTokens(row),
    },
    {
      id: "clientIp",
      header: "来源 IP",
      value: (row) => row.client_ip,
      headerTitle: "末段已脱敏",
      cell: (row) => (
        <span className="font-mono text-xs">{row.client_ip || MISSING_VALUE_TEXT}</span>
      ),
    },
    {
      id: "detail",
      header: "详情",
      cell: (row) => (
        // 核心对象走完整详情页，不用右侧抽屉（§11.4）。
        // 链接而不是按钮：详情页要能被贴给同事、被收藏
        <Link
          to={`/platforms/${platform}/requests/${encodeURIComponent(row.id)}`}
          className="text-sm font-medium text-accent hover:underline"
        >
          查看内容
        </Link>
      ),
    },
  ];
}

function RequestTable({
  platform,
  rows,
  emptyState,
  footerExtra,
}: {
  platform: string;
  rows: RequestSummary[];
  emptyState: ReactNode;
  footerExtra: ReactNode;
}) {
  return (
    <DataTableV2
      caption="请求列表：时间、用户、模型、状态、耗时与 Token 用量"
      columns={requestColumns(platform)}
      rows={rows}
      rowKey={(row) => row.id}
      // 不传 pageSize、不开表内搜索：筛选与翻页都在服务端（reqlog 游标分页）。
      // 再加一个只筛当前这一页的搜索框，人会以为自己搜的是全部请求
      footerExtra={footerExtra}
      emptyState={emptyState}
    />
  );
}

function StatusBadge({ status }: { status: number }) {
  const d = describeStatus(status);
  return (
    <Badge tone={d.tone} title={status === 0 ? "请求审计系统没有记到状态码，通常是连接中断" : undefined}>
      {d.label}
    </Badge>
  );
}

/** 游标翻页。
 *
 *  只有上一页/下一页，没有页码：游标分页给不出总数，编一个页码等于编一个
 *  总页数。上一页靠本地保存的游标栈，不靠后端提供反向游标——后者需要
 *  一个我们还没有的 API 形状（真实控制台 API 未核实）。 */
function Pager({
  hasPrev,
  hasNext,
  onPrev,
  onNext,
}: {
  hasPrev: boolean;
  hasNext: boolean;
  onPrev: () => void;
  onNext: () => void;
}) {
  if (!hasPrev && !hasNext) return null;
  return (
    <div className="flex items-center justify-end gap-2">
      <Button variant="secondary" size="sm" disabled={!hasPrev} onClick={onPrev}>
        上一页
      </Button>
      <Button variant="secondary" size="sm" disabled={!hasNext} onClick={onNext}>
        下一页
      </Button>
    </div>
  );
}
