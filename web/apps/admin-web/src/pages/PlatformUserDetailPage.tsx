import { useQuery } from "@tanstack/react-query";
import {
  FreshnessBadge,
  FreshnessNote,
  PageHeader,
  PageState,
  StatTile,
  formatUtcTimestamp,
} from "@xingmang/ui-admin";
import { Badge, Tabs } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import {
  decodePlatformUserIdSegment,
  describeMaskedEmail,
  describeUserStatus,
  getPlatformUser,
  platformHasUsers,
  type AmountBody,
  type PeriodGranularity,
  type PlatformUserItem,
  type PlatformUserLookupResult,
} from "../api/users";
import { ApiStateView } from "../components/ApiStateView";
import { PeriodControls } from "@xingmang/ui-admin";
import { appDemoDataConfig, DEMO_BANNER_TEXT, shouldShowDemoBanner } from "../lib/demoData";
import { formatMinorUnits } from "../lib/money";
import { parseBusinessDay, parseGranularity } from "../lib/period";

const PLATFORM_LABELS: Record<string, string> = {
  sub2api: "Sub2API",
  newapi: "NewAPI",
};

function platformLabel(platform: string): string {
  return PLATFORM_LABELS[platform] ?? platform;
}

/** 缺席与已知 0 分开：null 是「上游没给」，字符串 "0" 才是零。 */
function amountText(amount: AmountBody): string {
  if (amount.minor_units === null) return "—";
  return formatMinorUnits(amount.minor_units, amount.currency);
}

function lastActiveText(raw: string | null): string {
  if (raw === null) return "从未活跃";
  if (Number.isNaN(new Date(raw).getTime())) return "时间格式异常（上游值未展示）";
  return formatUtcTimestamp(raw);
}

/** 契约只允许至多 8 字符前缀；形状越界时 fail closed，不把疑似完整 Key 画出来。 */
function tokenPrefixText(raw: string): string {
  if (!raw) return "—";
  return raw.length <= 8 ? raw : "格式异常（未显示）";
}

/** 平台用户完整详情页（XM-B001）。
 *
 * 数据入口是 v2 的 canonical UserDetail Query；服务端负责精确查找与来源隔离。
 * 不调用 reqlog、request content、invoice 或 finance，也不按用户名跨域拼接。 */
export function PlatformUserDetailPage() {
  const params = useParams();
  const platform = params.serviceType ?? "";
  const userIdSegment = params.userId ?? "";
  const userId = decodePlatformUserIdSegment(userIdSegment);
  const supported = platformHasUsers(platform);
  const label = platformLabel(platform);
  const backTo = `/platforms/${encodeURIComponent(platform)}?tab=users`;
  const [searchParams, setSearchParams] = useSearchParams();
  const day = parseBusinessDay(searchParams.get("day"));
  const granularity = parseGranularity(searchParams.get("granularity"));
  const detailSub = searchParams.get("sub");

  const setParam = (key: string, value: string) => {
    const next = new URLSearchParams(searchParams);
    if (value.trim() === "") next.delete(key);
    else next.set(key, value);
    setSearchParams(next, { replace: true });
  };

  const query = useQuery({
    queryKey: ["platform-user-exact", platform, userIdSegment, { day, granularity }],
    queryFn: ({ signal }) => {
      if (userId === null) throw new Error("用户 ID 编码无效");
      return getPlatformUser(platform, userId, {
        signal,
        ...(day ? { day } : {}),
        granularity,
      });
    },
    enabled: supported && userId !== null,
    retry: false,
  });

  // 正常应用路由会在 loader 里把这一支送进统一 Not Found；保留防御性呈现，
  // 让组件单测或未来复用时也不会对不支持的平台发 Query。
  if (!supported) {
    return (
      <section>
        <PageHeader title="用户详情" />
        <PageState
          kind="unavailable"
          title="这个平台不支持终端用户详情"
          description="platformusers v1 只覆盖 Sub2API 与 NewAPI。"
          action={
            <Link to={backTo} className="text-sm font-medium text-accent hover:underline">
              返回平台
            </Link>
          }
        />
      </section>
    );
  }

  if (userId === null) {
    return (
      <section>
        <PageHeader title="用户详情" description={<span className="font-mono">{userIdSegment}</span>} />
        <PageState
          kind="unavailable"
          title="用户 ID 编码无效"
          description="链接不是 canonical 用户详情地址；为避免路径归一化或查错用户，本页没有发送查询。"
          action={
            <Link to={backTo} className="text-sm font-medium text-accent hover:underline">
              返回用户管理
            </Link>
          }
        />
      </section>
    );
  }

  const result = query.data;
  const found = result?.kind === "found" ? result : undefined;

  return (
    <section className="min-w-0">
      <Link
        to={backTo}
        aria-label={`返回 ${label} 用户管理`}
        className="mb-3 inline-flex min-h-9 items-center rounded-md px-2 text-sm font-medium text-accent hover:bg-accent-soft focus-visible:outline-2 focus-visible:outline-accent"
      >
        <span aria-hidden="true">←</span>
        <span className="ml-1">返回用户管理</span>
      </Link>

      <PageHeader
        title={found ? found.user.username || found.user.id : `${label} · 用户详情`}
        status={found ? <UserStatusBadge user={found.user} /> : undefined}
        description={
          found ? (
            <span className="break-all font-mono">{found.user.id}</span>
          ) : (
            <span className="break-all font-mono">{userId}</span>
          )
        }
        actions={found ? <FreshnessBadge freshness={found.page.freshness} /> : undefined}
      />

      <div className="mb-4">
        <PeriodControls
          day={day}
          granularity={granularity}
          period={found?.page.period}
          onDayChange={(next) => setParam("day", next)}
          onGranularityChange={(next: PeriodGranularity) => setParam("granularity", next)}
        />
      </div>

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {result === undefined ? null : (
          <LookupResult
            platform={platform}
            result={result}
            backTo={backTo}
            onRetry={() => void query.refetch()}
            detailSub={detailSub}
            onDetailSubChange={(next) => setParam("sub", next)}
          />
        )}
      </ApiStateView>
    </section>
  );
}

function UserStatusBadge({ user }: { user: PlatformUserItem }) {
  const shown = describeUserStatus(user.status);
  return (
    <Badge tone={shown.tone} title={shown.hint}>
      {shown.label}
    </Badge>
  );
}

function LookupResult({
  platform,
  result,
  backTo,
  onRetry,
  detailSub,
  onDetailSubChange,
}: {
  platform: string;
  result: PlatformUserLookupResult;
  backTo: string;
  onRetry: () => void;
  detailSub: string | null;
  onDetailSubChange: (next: string) => void;
}) {
  if (result.kind === "notFound") {
    return (
      <PageState
        kind="empty"
        title="没有这个用户"
        description="按用户 ID 精确搜索的过滤结果已完整耗尽；没有回落到模糊结果或第一条样本。"
        footnote={`已查询 ${result.pagesScanned} 次 platformusers v2 精确资源`}
        action={
          <Link to={backTo} className="text-sm font-medium text-accent hover:underline">
            返回用户管理
          </Link>
        }
      />
    );
  }

  if (result.kind === "incomplete") {
    const reason =
      result.reason === "cursorCycle"
        ? "上游返回了重复游标，无法证明搜索结果已经到底。"
        : "已达到 5 页、每页 200 条的详情查找上限，结果后面仍有游标。";
    return (
      <PageState
        kind="unavailable"
        title="无法确认用户是否存在"
        description={`${reason} 为避免误报 Not Found，本页停止查找；可以重试，或回到用户管理缩小范围。`}
        onRetry={onRetry}
        footnote={`已扫描 ${result.pagesScanned} 页 · 来源 ${result.lastPage.data_source || "—"}`}
        action={
          <Link to={backTo} className="text-sm font-medium text-accent hover:underline">
            返回用户管理
          </Link>
        }
      />
    );
  }

  return (
    <FoundUserDetail
      platform={platform}
      result={result}
      detailSub={detailSub}
      onDetailSubChange={onDetailSubChange}
    />
  );
}

function FoundUserDetail({
  platform,
  result,
  detailSub,
  onDetailSubChange,
}: {
  platform: string;
  result: Extract<PlatformUserLookupResult, { kind: "found" }>;
  detailSub: string | null;
  onDetailSubChange: (next: string) => void;
}) {
  const { user, page } = result;
  const fake = shouldShowDemoBanner([page.data_source], appDemoDataConfig);

  return (
    <div className="flex min-w-0 flex-col gap-4">
      {fake ? (
        <div
          role="status"
          className="flex items-center gap-2 rounded-md border-2 border-warning bg-warning/15 px-3 py-2 text-sm font-semibold text-fg"
        >
          <span aria-hidden="true">⚠</span>
          <span>{DEMO_BANNER_TEXT}</span>
        </div>
      ) : null}

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <AmountTile label="可用余额" amount={user.balance} note="platformusers v2 用户可用余额" />
        <AmountTile
          label="区间充值"
          amount={user.period_recharge}
          note="所选业务日区间的充值；真实上游可能不提供"
        />
        <AmountTile
          label="区间消费"
          amount={user.period_consumed}
          note="所选业务日区间的计费消费；未知不按 0 展示"
        />
        <AmountTile
          label="近 30 天消费"
          amount={user.last_30d_consumed}
          note="滚动 30 天，不随当前统计区间变化"
        />
      </div>

      <div className="rounded-lg border border-edge bg-surface p-3">
        <FreshnessNote freshness={page.freshness} />
        <p className="break-words text-xs text-fg-muted">
          来源 {page.data_source || "—"} · platformusers v2 · 精确 UserRef 匹配
        </p>
      </div>

      <BasicInformation platform={platform} user={user} />
      {platform === "newapi" ? (
        <NewApiUnsupported />
      ) : (
        <Sub2ApiUnsupported activeSub={detailSub} onSubChange={onDetailSubChange} />
      )}
    </div>
  );
}

function AmountTile({ label, amount, note }: { label: string; amount: AmountBody; note: string }) {
  const unavailable = amount.minor_units === null;
  return (
    <StatTile
      label={label}
      value={amountText(amount)}
      unavailable={unavailable}
      note={unavailable ? `上游没有提供这个值。${note}` : note}
      status={unavailable ? <Badge tone="neutral">未知</Badge> : undefined}
    />
  );
}

function BasicInformation({ platform, user }: { platform: string; user: PlatformUserItem }) {
  const email = describeMaskedEmail(user.email_masked);
  const status = describeUserStatus(user.status);
  return (
    <section className="min-w-0 rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <h3 className="mb-3 text-sm font-semibold text-fg">基本信息</h3>
      <dl className="grid min-w-0 grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2 lg:grid-cols-3">
        <Fact label="用户 ID">
          <span className="break-all font-mono text-xs">{user.id}</span>
        </Fact>
        <Fact label="邮箱" hint={email.hint}>
          <span className="break-all font-mono text-xs">{email.text}</span>
        </Fact>
        <Fact label="账号状态" hint={status.hint}>
          <Badge tone={status.tone}>{status.label}</Badge>
        </Fact>
        <Fact label="最后活跃">
          <span className="break-words tabular-nums">{lastActiveText(user.last_active_at)}</span>
        </Fact>
        {platform === "sub2api" ? (
          <Fact label="令牌前缀（脱敏证据）" hint="至多 8 字符；不是 API Key 列表或完整凭据">
            <span className="break-all font-mono text-xs">{tokenPrefixText(user.token_prefix)}</span>
          </Fact>
        ) : null}
      </dl>
    </section>
  );
}

function Fact({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-fg-muted" title={hint}>
        {label}
      </dt>
      <dd className="mt-0.5 text-sm text-fg">{children}</dd>
    </div>
  );
}

function UnavailablePanel({ title, description }: { title: string; description: ReactNode }) {
  return (
    <section className="min-w-0 rounded-lg border border-edge bg-surface p-3">
      <h3 className="mb-2 text-sm font-semibold text-fg">{title}</h3>
      <PageState kind="unavailable" description={description} compact />
    </section>
  );
}

const SUB2_DETAIL_TABS = [
  {
    value: "usage",
    label: "消费明细",
    description:
      "需要逐用户消费流水与稳定分页，归属用户 read contract v2；本页不调用 reqlog 或请求内容端点。",
  },
  {
    value: "recharge",
    label: "充值记录",
    description: "需要逐用户充值流水，归属用户 read contract v2；本页不调用 finance 端点。",
  },
  {
    value: "invoices",
    label: "开票记录",
    description:
      "用户与发票的稳定关联需经获批的开票集成契约提供；本页不调用 invoice，也不展示发票 PII。",
  },
  {
    value: "keys",
    label: "API Key",
    description:
      "API Key 列表属于凭据边界；platformusers v1 只给至多 8 字符的令牌前缀，本页不获取或展示完整 Key。",
  },
] as const;

function Sub2ApiUnsupported({
  activeSub,
  onSubChange,
}: {
  activeSub: string | null;
  onSubChange: (next: string) => void;
}) {
  const active = activeSub === null || activeSub === "" ? SUB2_DETAIL_TABS[0].value : activeSub;
  const known = SUB2_DETAIL_TABS.some((item) => item.value === active);

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-3">
        <UnavailablePanel
          title="客户类型"
          description="客户类型不在当前用户详情快照；归属 platformusers read contract v2。"
        />
        <UnavailablePanel
          title="注册时间"
          description="注册时间不在当前用户详情快照；归属 platformusers read contract v2。"
        />
        <UnavailablePanel
          title="近 7 天消费趋势"
          description="需要逐日消费序列；DailyUsage 是独立审批切片，当前保持未接入。"
        />
      </div>

      {known ? (
        <Tabs
          value={active}
          onValueChange={onSubChange}
          className="min-w-0"
          items={SUB2_DETAIL_TABS.map((item) => ({
            value: item.value,
            label: item.label,
            content: <UnavailablePanel title={item.label} description={item.description} />,
          }))}
        />
      ) : (
        <PageState
          kind="empty"
          title="没有这个子页签"
          description={`用户详情下没有名为 ${activeSub} 的子页签；现有：${SUB2_DETAIL_TABS.map((item) => item.label).join(" / ")}。`}
        />
      )}
    </div>
  );
}

function NewApiUnsupported() {
  return (
    <UnavailablePanel
      title="区间请求"
      description="reqlog 目前没有稳定的 platform-user ID 关联；本页不按展示用户名拼接，也不调用请求或内容端点。归属未来获批的用户关联契约。"
    />
  );
}
