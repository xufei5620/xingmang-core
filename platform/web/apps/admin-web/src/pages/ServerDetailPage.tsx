import { PageHeader, PageState, StatTile } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { Link, useParams } from "react-router";
import { isServerDetailPreviewId } from "../blueprints/server";
import { NotFoundView } from "./NotFoundPage";

/** 服务器详情页的只读结构壳。
 *
 *  这页是 M2 Server Agent 接入前的 UI 交付物：路由、字段与信息层级已经定稿，
 *  但不会为了填充页面而请求服务注册表、Agent 或供应商接口。除 URL 中携带的
 *  资产 ID 外，所有值都明确显示为「未接入」/「—」。这样页面可以被产品与实现
 *  同学直接演示，又不会把样例数字冒充成实时服务器状态。
 */
export function ServerDetailPage() {
  const { serverId = "" } = useParams();
  const pathname = `/platforms/server/detail/${serverId}`;

  // 路由 loader 已经做过一次校验；组件保留同一层防御，方便 Storybook/单测
  // 直接挂载时也不会对未知 ID 画出一个看似成功的资产详情。
  if (!isServerDetailPreviewId(serverId)) {
    return <NotFoundView pathname={pathname} detail={`没有这个服务器资产：${serverId || "（空 ID）"}`} />;
  }

  return <ServerDetailShell serverId={serverId} />;
}

function ServerDetailShell({ serverId }: { serverId: string }) {
  return (
    <section className="min-w-0">
      <Link
        to="/platforms/server?tab=assets"
        aria-label="返回服务器资产"
        className="mb-3 inline-flex min-h-9 items-center rounded-md px-2 text-sm font-medium text-accent hover:bg-accent-soft focus-visible:outline-2 focus-visible:outline-accent"
      >
        <span aria-hidden="true">←</span>
        <span className="ml-1">返回服务器资产</span>
      </Link>

      <PageHeader
        title="服务器资产详情"
        status={<Badge tone="warning">未接入·M2</Badge>}
        description={
          <span className="break-all">
            资产 ID <code className="font-mono">{serverId}</code> · 只读结构预览
          </span>
        }
      />

      <div className="flex min-w-0 flex-col gap-4">
        <p
          role="status"
          className="rounded-md border-2 border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
        >
          服务器详情蓝图：Server Agent 尚未接入。此页不读取 API、不保存凭据，
          不提供关机、重启、任意命令、密码明文查看或直接续费操作。
        </p>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <StatTile
            label="连接状态"
            value="未接入"
            unavailable
            status={<Badge tone="warning">Server Agent</Badge>}
            note="等待 Server Agent 只读心跳与数据新鲜度"
          />
          <StatTile
            label="折算月成本"
            value="—"
            unavailable
            status={<Badge tone="neutral">未知</Badge>}
            note="供应商采购记录与汇率快照尚未接入"
          />
          <StatTile
            label="下次续费"
            value="—"
            unavailable
            status={<Badge tone="neutral">未知</Badge>}
            note="续费日期与自动续费状态尚未接入"
          />
          <StatTile
            label="部署服务"
            value="—"
            unavailable
            status={<Badge tone="neutral">未知</Badge>}
            note="工作负载清单随 Agent 采集器接入"
          />
        </div>

        <div className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-2">
          <DetailSection title="资产与采购" hint="服务器资产、购买入口与责任归属">
            <DetailList>
              <Fact label="资产编号">
                <span className="break-all font-mono text-xs">{serverId}</span>
              </Fact>
              <UnavailableFact label="资产状态" />
              <UnavailableFact label="供应商" />
              <UnavailableFact label="购买账号" hint="供应商门户账号只保存 CredentialRef，不回显密码" />
              <UnavailableFact label="购买地址" />
              <UnavailableFact label="订单号" />
              <UnavailableFact label="实例 ID" />
              <UnavailableFact label="购买日期" />
              <UnavailableFact label="负责人" />
            </DetailList>
          </DetailSection>

          <DetailSection title="费用与续费" hint="成本登记与续费风险的只读口径">
            <DetailList>
              <UnavailableFact label="原始价格" />
              <UnavailableFact label="币种 / 周期" />
              <UnavailableFact label="基础金额" />
              <UnavailableFact label="附加费用" />
              <UnavailableFact label="折算汇率" />
              <UnavailableFact label="折算依据" />
              <UnavailableFact label="折算月成本" />
              <UnavailableFact label="下次续费" />
              <UnavailableFact label="续费风险" />
              <UnavailableFact label="自动续费" />
              <UnavailableFact label="付款方式" />
            </DetailList>
          </DetailSection>

          <DetailSection title="配置与网络" hint="采购配置与 Agent 实测配置的对照">
            <DetailList>
              <UnavailableFact label="区域" />
              <UnavailableFact label="操作系统" />
              <UnavailableFact label="CPU" />
              <UnavailableFact label="内存" />
              <UnavailableFact label="磁盘" />
              <UnavailableFact label="带宽 / 流量" />
              <UnavailableFact label="公网 IP" />
              <UnavailableFact label="内网 IP" />
            </DetailList>
          </DetailSection>

          <ResourceSection />
        </div>

        <ReadOnlyTableSection
          title="访问与凭据"
          hint="只显示引用、状态与审计字段；不返回密码、私钥或 Token"
          columns={["类型", "账号", "密钥引用（CredentialRef）", "状态", "最近轮换", "最近验证"]}
          description="CredentialRef 登记与验证状态将在服务器凭据契约接入后填充；当前没有任何凭据读取请求。"
        />

        <ReadOnlyTableSection
          title="部署服务与容器"
          hint="从业务服务反查服务器、镜像版本与健康状态"
          columns={["服务", "平台", "镜像", "端口", "版本", "状态"]}
          description="工作负载目录尚未接入；页面不会提供重启、删除容器或任意命令入口。"
        />

        <div className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-2">
          <ReadOnlyTableSection
            title="关联域名与证书"
            hint="域名目标、证书生命周期与续期方式"
            columns={["域名", "服务", "到期", "剩余", "续期"]}
            description="域名与证书探测随 M2 接入；当前不显示任何假定的域名或到期日。"
          />
          <ReadOnlyTableSection
            title="告警与审计"
            hint="关联告警、连接验证和凭据审计证据"
            columns={["时间", "类型", "对象", "结果", "审计证据"]}
            description="服务器告警与审计事件尚未接入；控制平面自身的告警仍在「告警与故障」查看。"
          />
        </div>

        <p className="text-xs text-fg-muted">
          字段结构依据 SERVER_BLUEPRINT 与 ADMIN-IA §三、§四；数据来源为未来的
          Server Agent（ADR-015）。当前页面仅用于 UI 评审，不代表服务器已经纳管。
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

function ResourceSection() {
  return (
    <DetailSection title="实时资源水位" hint="所有数值必须带观测时间与来源">
      <div className="flex flex-col gap-4">
        <p className="text-xs text-fg-muted">当前没有 Server Agent 观测；不会把缺席显示为 0%。</p>
        {(["CPU", "内存", "磁盘"] as const).map((label) => (
          <div key={label}>
            <div className="mb-1 flex items-center justify-between gap-2 text-sm">
              <span className="text-fg">{label}</span>
              <Badge tone="neutral">未接入</Badge>
            </div>
            <div
              className="h-2 rounded-full bg-surface-muted"
              role="img"
              aria-label={`${label}资源水位未接入`}
            />
          </div>
        ))}
        <div className="border-t border-edge pt-3 text-xs text-fg-muted">
          最后观测：— · 数据新鲜度：未接入
        </div>
      </div>
    </DetailSection>
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
    <DetailSection title={title} hint={hint}>
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
    </DetailSection>
  );
}
