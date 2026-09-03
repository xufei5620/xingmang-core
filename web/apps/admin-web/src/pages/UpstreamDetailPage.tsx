import { PageHeader, PageState, StatTile } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { Link, useParams } from "react-router";
import { NotFoundView } from "./NotFoundPage";
import {
  isSupplyPlatform,
  supplyPlatformLabel,
  upstreamDetailPath,
  type SupplyPlatform,
} from "./ChannelDetailPage";

/** 上游详情 UI-only 壳。
 *
 *  上游是供应商/账号的汇总对象：一个上游可以挂多个平台渠道、分组和 Key。
 *  当前没有获批的详情读契约，因此只根据 URL 确认平台与对象 ID，所有余额、
 *  比例、成本和利润均保持「未接入」，不从列表缓存或任何 API 侧拼接。
 */
export function UpstreamDetailPage() {
  const { serviceType = "", upstreamId = "" } = useParams();

  if (!isSupplyPlatform(serviceType)) {
    return (
      <NotFoundView
        pathname={`/platforms/${serviceType || "（空平台）"}/suppliers/${upstreamId}`}
        detail={`平台 ${serviceType || "（空平台）"} 不支持上游详情`}
      />
    );
  }

  if (!upstreamId.trim() || upstreamId === "new") {
    return (
      <NotFoundView
        pathname={upstreamDetailPath(serviceType, upstreamId)}
        detail={
          upstreamId === "new"
            ? "新增上游请到渠道管理页使用「＋ 添加上游」（专用评审蓝图页已于 2026-09-03 按产品负责人裁定下线）"
            : "上游 ID 为空，无法定位详情"
        }
      />
    );
  }

  return <UpstreamDetailShell platform={serviceType} upstreamId={upstreamId} />;
}

function UpstreamDetailShell({ platform, upstreamId }: { platform: SupplyPlatform; upstreamId: string }) {
  const label = supplyPlatformLabel(platform);
  // 2026-09-02 起「上游管理」不再是独立页签：登记簿字段并入了渠道管理页
  // 表格的行与渠道详情页（07:20 补充裁定）——返回目标跟着从 ?tab=suppliers
  // 改成 ?tab=upstream，不带锚点：已经没有独立区块可滚，落地在页顶即可
  const backTo = `/platforms/${platform}?tab=upstream`;

  return (
    <section className="min-w-0">
      <Link
        to={backTo}
        aria-label={`返回 ${label} 上游管理`}
        className="mb-3 inline-flex min-h-9 items-center rounded-md px-2 text-sm font-medium text-accent hover:bg-accent-soft focus-visible:outline-2 focus-visible:outline-accent"
      >
        <span aria-hidden="true">←</span>
        <span className="ml-1">返回上游管理</span>
      </Link>

      <PageHeader
        title="上游详情"
        status={<Badge tone="warning">未接入·UI</Badge>}
        description={
          <span className="break-all">
            平台 {label} · 上游 ID <code className="font-mono">{upstreamId}</code> · 多渠道汇总视角
          </span>
        }
      />

      <div className="flex min-w-0 flex-col gap-4">
        <p
          role="status"
          className="rounded-md border-2 border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
        >
          只读结构预览：上游详情读契约尚未接入。本页不读取上游 API、不保存账号或密码，
          不执行充值比例、分组、凭据或渠道绑定修改。
        </p>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-5">
          <StatTile label="接入平台" value={label} note="由当前平台路由确定" />
          <StatTile
            label="接入渠道"
            value="—"
            unavailable
            status={<Badge tone="neutral">未接入</Badge>}
            note="该上游下的账号 / Key 渠道数量"
          />
          <StatTile
            label="上游余额"
            value="—"
            unavailable
            status={<Badge tone="neutral">未知</Badge>}
            note="上游账号余额或订阅有效期"
          />
          <StatTile
            label="本期总消耗"
            value="—"
            unavailable
            status={<Badge tone="neutral">未接入</Badge>}
            note="该上游下所有渠道的我方计费消耗"
          />
          <StatTile
            label="本期总利润"
            value="—"
            unavailable
            status={<Badge tone="neutral">未接入</Badge>}
            note="跨平台渠道合计的毛利"
          />
        </div>

        <div className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-2">
          <DetailSection title="上游资料" hint="由平台登记，不从渠道名称或网址反推">
            <DetailList>
              <Fact label="上游 ID">
                <span className="break-all font-mono text-xs">{upstreamId}</span>
              </Fact>
              <UnavailableFact label="上游实际名称" />
              <UnavailableFact label="上游网址" />
              <Fact label="接入平台">
                <Badge tone="info">{label}</Badge>
              </Fact>
              <UnavailableFact label="上游账号" />
              <UnavailableFact label="账号密码状态" hint="密码只经 CredentialRef，页面永不显示明文" />
              <UnavailableFact label="联系人" />
              <UnavailableFact label="状态" />
              <UnavailableFact label="环境" />
            </DetailList>
          </DetailSection>

          <DetailSection title="充值比例与成本口径" hint="比例用于解释计量型渠道的上游成本">
            <DetailList>
              <UnavailableFact label="当前充值比例" />
              <UnavailableFact label="充值成本率" hint="成本率是充值比例的展示投影，不在前端重复计算" />
              <UnavailableFact label="最近一笔比例" />
              <UnavailableFact label="比例更新时间" />
              <UnavailableFact label="计量 / 订阅口径" />
              <UnavailableFact label="币种 / 额度单位" />
              <UnavailableFact label="成本观测时间" />
              <UnavailableFact label="收入观测时间" />
              <UnavailableFact label="数据来源" />
            </DetailList>
          </DetailSection>

          <DetailSection title="余额与预计补充" hint="余额按上游账号共享，不能在渠道行重复累加">
            <DetailList>
              <UnavailableFact label="总余额" />
              <UnavailableFact label="订阅最近到期" />
              <UnavailableFact label="近 7 日日均消耗" />
              <UnavailableFact label="预计可用天数" />
              <UnavailableFact label="预计补充时间" />
              <UnavailableFact label="余额观测时间" />
              <UnavailableFact label="覆盖窗口" />
              <UnavailableFact label="覆盖完整度" />
            </DetailList>
          </DetailSection>

          <DetailSection title="联系人与支持" hint="用于补充上游异常、余额和续费沟通">
            <DetailList>
              <UnavailableFact label="联系人姓名" />
              <UnavailableFact label="主要沟通渠道" />
              <UnavailableFact label="邮箱" />
              <UnavailableFact label="电话" />
              <UnavailableFact label="最近联系" />
              <UnavailableFact label="支持等级" />
            </DetailList>
          </DetailSection>
        </div>

        <ReadOnlyTableSection
          title="上游全部分组"
          hint="接入与未接入分组都要保留，避免把未接入误读成不存在"
          columns={["分组实际名", "倍率", "接入状态", "接入平台", "Key / 账号", "可用模型"]}
          description="上游分组目录尚未接入；后续每个分组应显示实际名、倍率、接入状态与支持模型。"
        />

        <ReadOnlyTableSection
          title="关联渠道"
          hint="同一上游下的 Sub2API / NewAPI 渠道分别核算，再汇总到本页"
          columns={["平台", "渠道", "分组 / Key", "供给成本", "我方计费", "毛利", "状态"]}
          description="渠道关联读契约尚未接入；不会把其它上游或第一条渠道的数值带入本页。"
        />

        <ReadOnlyTableSection
          title="上游凭据"
          hint="只显示引用、轮换与验证状态"
          columns={["类型", "账号别名", "密钥引用（CredentialRef）", "状态", "最近轮换", "最近验证"]}
          description="上游账号密码、API Key 与 Token 永不回显；需要使用时由服务端按授权 Action 取 CredentialRef。"
        />

        <p className="text-xs text-fg-muted">
          上游总利润 = 该上游下所有渠道的我方计费消耗 − 对应渠道供给成本；本页只定义汇总视图，
          不在前端自行相加或重复套用充值比例。
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
