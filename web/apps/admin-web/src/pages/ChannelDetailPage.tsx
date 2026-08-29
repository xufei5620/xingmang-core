import { PageHeader, PageState, StatTile } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { Link, useParams } from "react-router";
import { NotFoundView } from "./NotFoundPage";

export type SupplyPlatform = "sub2api" | "newapi";

export const SUPPLY_PLATFORM_LABELS: Record<SupplyPlatform, string> = {
  sub2api: "Sub2API",
  newapi: "NewAPI",
};

/** 渠道/上游详情只在已有两个上游登记簿平台上提供结构壳。 */
export function isSupplyPlatform(value: string): value is SupplyPlatform {
  return value === "sub2api" || value === "newapi";
}

export function supplyPlatformLabel(platform: string): string {
  return isSupplyPlatform(platform) ? SUPPLY_PLATFORM_LABELS[platform] : platform;
}

export function channelDetailPath(platform: string, channelId: string): string {
  return `/platforms/${encodeURIComponent(platform)}/upstream/detail/${encodeURIComponent(channelId)}`;
}

export function upstreamDetailPath(platform: string, upstreamId: string): string {
  return `/platforms/${encodeURIComponent(platform)}/suppliers/${encodeURIComponent(upstreamId)}`;
}

export function upstreamCreatePath(platform: string): string {
  return `/platforms/${encodeURIComponent(platform)}/suppliers/new`;
}

/** 渠道详情 UI-only 壳。
 *
 *  渠道粒度是一个平台账号/Key 到上游分组的映射。当前只确定了 URL 中的
 *  `platform` 与 `channelId`，并没有获批的逐渠道详情读契约，因此本页不会
 *  请求 API 或从列表缓存拼接数据；所有经营字段都显式标为「未接入」。
 */
export function ChannelDetailPage() {
  const { serviceType = "", channelId = "" } = useParams();

  if (!isSupplyPlatform(serviceType)) {
    return (
      <NotFoundView
        pathname={`/platforms/${serviceType || "（空平台）"}/upstream/detail/${channelId}`}
        detail={`平台 ${serviceType || "（空平台）"} 不支持渠道详情`}
      />
    );
  }

  if (!channelId.trim()) {
    return (
      <NotFoundView
        pathname={channelDetailPath(serviceType, channelId)}
        detail="渠道 ID 为空，无法定位详情"
      />
    );
  }

  return <ChannelDetailShell platform={serviceType} channelId={channelId} />;
}

function ChannelDetailShell({ platform, channelId }: { platform: SupplyPlatform; channelId: string }) {
  const label = supplyPlatformLabel(platform);
  const backTo = `/platforms/${platform}?tab=upstream`;

  return (
    <section className="min-w-0">
      <Link
        to={backTo}
        aria-label={`返回 ${label} 渠道管理`}
        className="mb-3 inline-flex min-h-9 items-center rounded-md px-2 text-sm font-medium text-accent hover:bg-accent-soft focus-visible:outline-2 focus-visible:outline-accent"
      >
        <span aria-hidden="true">←</span>
        <span className="ml-1">返回渠道管理</span>
      </Link>

      <PageHeader
        title="渠道详情"
        status={<Badge tone="warning">未接入·UI</Badge>}
        description={
          <span className="break-all">
            平台 {label} · 渠道 ID <code className="font-mono">{channelId}</code> · 单渠道经营核算
          </span>
        }
      />

      <div className="flex min-w-0 flex-col gap-4">
        <p
          role="status"
          className="rounded-md border-2 border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
        >
          只读结构预览：渠道详情读契约尚未接入。本页不读取渠道、上游或请求 API，
          不显示凭据明文，也不提供倍率修改、停用或其它写操作。
        </p>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
          <StatTile label="来源平台" value={label} note="由路由平台段确定，不从名称推断" />
          <StatTile
            label="上游分组"
            value="未接入"
            unavailable
            status={<Badge tone="neutral">未知</Badge>}
            note="等待渠道绑定与分组目录契约"
          />
          <StatTile
            label="今日供给成本"
            value="—"
            unavailable
            status={<Badge tone="neutral">未接入</Badge>}
            note="上游 Key / 订阅 / 代理成本投影"
          />
          <StatTile
            label="今日我方计费"
            value="—"
            unavailable
            status={<Badge tone="neutral">未接入</Badge>}
            note="本渠道令牌产生的计费消耗"
          />
          <StatTile
            label="今日毛利"
            value="—"
            unavailable
            status={<Badge tone="neutral">未接入</Badge>}
            note="我方计费消耗 − 供给成本"
          />
        </div>

        <div className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-2">
          <DetailSection title="渠道与映射" hint="一行 = 一个账号/Key 到一个上游分组的映射">
            <DetailList>
              <Fact label="渠道 ID">
                <span className="break-all font-mono text-xs">{channelId}</span>
              </Fact>
              <Fact label="平台">
                <Badge tone="info">{label}</Badge>
              </Fact>
              <UnavailableFact label="渠道名称 / 账号" />
              <UnavailableFact label="来源上游" />
              <UnavailableFact label="上游分组实际名" />
              <UnavailableFact label="分组倍率" hint="独立展示，不并入充值比例，也不在前端重复乘算" />
              <UnavailableFact label="接入方式" />
              <UnavailableFact label="Key / 账号别名" hint="只显示脱敏别名；完整凭据永不进入页面响应" />
              <UnavailableFact label="可用模型" />
              <UnavailableFact label="登记状态" />
            </DetailList>
          </DetailSection>

          <DetailSection title="经营核算" hint="成本、计费与毛利按渠道单独核算">
            <DetailList>
              <UnavailableFact label="统计区间" />
              <UnavailableFact label="上游供给成本" />
              <UnavailableFact label="我方计费消耗" />
              <UnavailableFact label="毛利" />
              <UnavailableFact label="毛利率" />
              <UnavailableFact label="币种 / 计费单位" />
              <UnavailableFact label="成本口径" hint="计量型按实扣 ÷ 充值比例；订阅型按批次日摊；官方 API 口径待定" />
              <UnavailableFact label="成本观测时间" />
              <UnavailableFact label="计费观测时间" />
              <UnavailableFact label="数据来源" />
            </DetailList>
          </DetailSection>

          <DetailSection title="余额与预计补充" hint="余额只作为上游账号共享引用，不在多个渠道重复合计">
            <DetailList>
              <UnavailableFact label="上游共享余额" />
              <UnavailableFact label="本渠道近 7 日日均消耗" />
              <UnavailableFact label="上游全部渠道日均" />
              <UnavailableFact label="预计可用天数" />
              <UnavailableFact label="预计补充时间" />
              <UnavailableFact label="充值比例" />
              <UnavailableFact label="充值成本率" />
              <UnavailableFact label="余额观测时间" />
              <UnavailableFact label="可用天数覆盖窗口" />
            </DetailList>
          </DetailSection>

          <DetailSection title="渠道保障" hint="模型检测、成功率与延迟属于独立保障观测">
            <DetailList>
              <UnavailableFact label="最近保障状态" />
              <UnavailableFact label="24h 成功率" />
              <UnavailableFact label="最近模型检测" />
              <UnavailableFact label="已验证模型数" />
              <UnavailableFact label="已配置模型数" />
              <UnavailableFact label="最近检测时间" />
            </DetailList>
          </DetailSection>
        </div>

        <ReadOnlyTableSection
          title="可用模型"
          hint="模型名称、验证结果与最近检测时间"
          columns={["模型", "可用状态", "最近检测", "证据"]}
          description="模型清单属于渠道保障（M1.5）读契约；当前不把空列表解释成「没有可用模型」。"
        />

        <ReadOnlyTableSection
          title="凭据边界"
          hint="只显示引用与状态"
          columns={["类型", "账号别名", "密钥引用（CredentialRef）", "状态", "最近验证"]}
          description="凭据只经 CredentialRef；密码、私钥、Token 与完整 API Key 不进入页面响应。"
        />

        <p className="text-xs text-fg-muted">
          页面字段依据渠道管理蓝图与 ADMIN-IA 的渠道详情语义。当前只确认平台与渠道 ID，
          其它字段均等待对应的只读契约和观测证据；未知值不会被折算为 0、1 或「正常」。
        </p>
      </div>
    </section>
  );
}

function DetailSection({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <section className="min-w-0 rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <header className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-semibold text-fg">{title}</h3>
        {hint ? <p className="text-xs text-fg-muted">{hint}</p> : null}
      </header>
      {children}
    </section>
  );
}

function DetailList({ children }: { children: ReactNode }) {
  return <dl className="grid min-w-0 grid-cols-1 gap-x-6 gap-y-3 sm:grid-cols-2">{children}</dl>;
}

function Fact({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-fg-muted" title={hint}>
        {label}
      </dt>
      <dd className="mt-0.5 min-w-0 text-sm text-fg">{children}</dd>
    </div>
  );
}

function UnavailableFact({ label, hint }: { label: string; hint?: string }) {
  return (
    <Fact label={label} hint={hint}>
      <span className="text-fg-muted">—</span>
      <Badge tone="neutral" className="ml-1">
        未接入
      </Badge>
    </Fact>
  );
}

function ReadOnlyTableSection({
  title,
  hint,
  columns,
  description,
}: {
  title: string;
  hint?: string;
  columns: readonly string[];
  description: string;
}) {
  return (
    <section className="min-w-0 rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <header className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-semibold text-fg">{title}</h3>
        {hint ? <p className="text-xs text-fg-muted">{hint}</p> : null}
      </header>
      <div className="max-w-full overflow-x-auto rounded-md border border-edge">
        <table className="w-full min-w-max border-collapse text-sm">
          <caption className="sr-only">{`${title}（只读结构预览，尚无数据）`}</caption>
          <thead className="border-b border-edge bg-surface-muted">
            <tr>
              {columns.map((column) => (
                <th key={column} scope="col" className="px-3 py-2 text-left text-xs font-medium text-fg-muted">
                  {column}
                </th>
              ))}
            </tr>
          </thead>
        </table>
      </div>
      <PageState kind="unavailable" description={description} compact />
    </section>
  );
}
