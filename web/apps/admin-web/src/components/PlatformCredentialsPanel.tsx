import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  FreshnessBadge,
  FreshnessNote,
  PageState,
  ServiceStatusBadge,
  StatTile,
  describeServiceStatus,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import {
  listServices,
  serviceFreshness,
  type ServiceItem,
} from "../api/platform";
import { ApiStateView } from "./ApiStateView";

const PLATFORM_LABELS: Readonly<Record<string, string>> = {
  sub2api: "Sub2API",
  newapi: "NewAPI",
};

function platformLabel(platform: string): string {
  return PLATFORM_LABELS[platform] ?? platform;
}

const COLUMNS: DataTableColumn<ServiceItem>[] = [
  {
    id: "instance",
    header: "实例",
    primary: true,
    value: (service) => `${service.instance_id} ${service.id}`,
    cell: (service) => (
      <div className="min-w-36">
        <strong className="font-medium text-fg">{service.instance_id}</strong>
        <p className="font-mono text-xs text-fg-muted">{service.id}</p>
      </div>
    ),
  },
  {
    id: "environment",
    header: "环境",
    value: (service) => service.environment,
    cell: (service) => <span className="text-xs text-fg">{service.environment || "—"}</span>,
  },
  {
    id: "status",
    header: "服务状态",
    value: (service) => describeServiceStatus(service.status).label,
    cell: (service) => <ServiceStatusBadge status={service.status} />,
  },
  {
    id: "endpoint",
    header: "接入地址",
    value: (service) => service.endpoint,
    cell: (service) => (
      <span className="block max-w-72 font-mono text-xs break-all text-fg-muted">
        {service.endpoint || "—"}
      </span>
    ),
  },
  {
    id: "owner",
    header: "负责人",
    value: (service) => service.owner,
    cell: (service) => <span className="text-sm text-fg">{service.owner || "—"}</span>,
  },
  {
    id: "freshness",
    header: "最近观测",
    value: (service) => service.observed_at,
    cell: (service) => {
      const freshness = serviceFreshness(service);
      return (
        <div className="flex min-w-44 flex-col items-start gap-1">
          <FreshnessBadge freshness={freshness} />
          <FreshnessNote freshness={freshness} />
        </div>
      );
    },
  },
];

/** 平台内「连接与凭据」。
 *
 * 当前平台只有 Service Query，所以本页刻意把三类事实分开：
 * - 已登记实例、地址、负责人、状态与最近观测：真实可读；
 * - CredentialRef / 连接登记簿：尚无 Query，不能从别的业务字段猜；
 * - 独立健康探测历史：尚无 Query，Service 的 observed_at 只代表最近一次观测。
 *
 * 这样既把目标 UI 画完整，也不会拿一行样例轮换日期冒充已接入能力。 */
export function PlatformCredentialsPanel({ platform }: { platform: string }) {
  const label = platformLabel(platform);
  const query = useQuery({
    queryKey: ["services"],
    queryFn: ({ signal }) => listServices({ signal }),
  });

  // React 指南：这是 Query 结果的纯派生，不用 Effect 复制一份容易失真的 state。
  const services = (query.data ?? []).filter((service) => service.service_type === platform);
  const freshCount = services.filter((service) => serviceFreshness(service).state === "fresh").length;
  const environments = [...new Set(services.map((service) => service.environment).filter(Boolean))];

  return (
    <section className="flex flex-col gap-4">
      <div className="rounded-lg border border-edge bg-surface px-4 py-3 shadow-sm">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-fg">连接事实与凭据边界</h2>
            <p className="mt-1 max-w-4xl text-xs leading-5 text-fg-muted">
              本页只显示平台服务注册表已经提供的事实。凭据正文永不展示；连接登记簿接入后也只显示 CredentialRef 与轮换状态。
            </p>
          </div>
          <Badge tone="info">只读</Badge>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          label="已登记实例"
          value={query.isPending ? "—" : String(services.length)}
          unavailable={query.isPending}
          note={`service_type 精确匹配 ${platform}`}
        />
        <StatTile
          label="观测新鲜"
          value={query.isPending ? "—" : `${freshCount} / ${services.length}`}
          unavailable={query.isPending || services.length === 0}
          note="按服务注册表最近一次观测与当前统一阈值计算"
        />
        <StatTile
          label="CredentialRef 状态"
          value="—"
          unavailable
          status={<Badge tone="neutral">未接入</Badge>}
          note="连接登记簿 Query 尚未提供；不会从财务字段或地址猜 CredentialRef"
        />
        <StatTile
          label="探测数据状态"
          value="—"
          unavailable
          status={<Badge tone="neutral">未接入</Badge>}
          note="独立探测历史尚未接入；当前只能显示服务最近一次观测"
        />
      </div>

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <DataTableV2
          caption={`${label} 已登记连接实例、状态、地址、负责人及最近观测`}
          columns={COLUMNS}
          rows={services}
          rowKey={(service) => service.id}
          searchable
          filters={
            environments.length > 1
              ? [{ columnId: "environment", label: "环境", options: environments }]
              : []
          }
          emptyState={
            <PageState
              kind="empty"
              title={`${label} 还没有登记实例`}
              description={`前往「平台治理 → 服务注册表」登记 ${label} 实例后，这里会显示接入地址、负责人、服务状态与最近一次观测。`}
            />
          }
        />
      </ApiStateView>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <article className="rounded-lg border border-edge bg-surface p-4 shadow-sm">
          <div className="flex items-start justify-between gap-3">
            <div>
              <h3 className="text-sm font-semibold text-fg">凭据引用</h3>
              <p className="mt-1 text-xs leading-5 text-fg-muted">
                目标字段包括连接名称、CredentialRef、用途、轮换状态与最近验证；不显示账号密码、Token 或密钥正文。
              </p>
            </div>
            <Badge tone="neutral">未接入</Badge>
          </div>
          <p className="mt-4 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
            连接登记簿 Query 尚未提供。接口落地前不展示样例引用、轮换日期或验证结果。
          </p>
        </article>

        <article className="rounded-lg border border-edge bg-surface p-4 shadow-sm">
          <div className="flex items-start justify-between gap-3">
            <div>
              <h3 className="text-sm font-semibold text-fg">健康探测历史</h3>
              <p className="mt-1 text-xs leading-5 text-fg-muted">
                目标字段包括探测对象、检查项、结果、延迟、错误码与执行时间，用于区分服务观测和连接可用性。
              </p>
            </div>
            <Badge tone="neutral">未接入</Badge>
          </div>
          <p className="mt-4 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
            独立探测历史 Query 尚未提供；上表只能显示服务最近一次观测，不能证明凭据或上游模型当前可用。
          </p>
        </article>
      </div>
    </section>
  );
}
