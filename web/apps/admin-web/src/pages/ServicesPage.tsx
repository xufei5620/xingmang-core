import { useQuery } from "@tanstack/react-query";
import { FreshnessBadge, ServiceStatusBadge, formatDuration, formatUtcTimestamp } from "@xingmang/ui-admin";
import { EmptyState } from "@xingmang/ui-primitives";
import {
  listServices,
  serviceFreshness,
  SERVICE_STALENESS_THRESHOLD_SECONDS,
  type ServiceItem,
} from "../api/platform";
import { ApiStateView } from "../components/ApiStateView";
import { PageHeader } from "../components/PageHeader";

const TH = "px-3 py-2 text-left text-xs font-medium text-fg-muted";
const TD = "px-3 py-2 align-top text-sm text-fg";

/** 服务清单：被管理系统实例一览。
 *
 *  采集时间同样按新鲜度语义展示——「实例在册」不等于「数据是新的」。 */
export function ServicesPage() {
  const query = useQuery({
    queryKey: ["services"],
    queryFn: ({ signal }) => listServices({ signal }),
  });

  return (
    <section>
      <PageHeader
        title="服务清单"
        description={`采集时间超过 ${formatDuration(SERVICE_STALENESS_THRESHOLD_SECONDS)} 记为数据延迟（阈值由前端设定，registry 未提供）。`}
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
      />
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <ServicesTable items={query.data ?? []} />
      </ApiStateView>
    </section>
  );
}

function ServicesTable({ items }: { items: ServiceItem[] }) {
  if (items.length === 0) {
    return (
      <EmptyState
        title="暂无服务"
        description="该环境下还没有登记任何被管理系统实例；通过 registry.service.create Action 登记后会出现在这里"
      />
    );
  }
  return (
    <div className="overflow-x-auto rounded-lg border border-edge bg-surface shadow-sm">
      <table className="w-full border-collapse">
        <thead className="border-b border-edge bg-surface-muted">
          <tr>
            <th className={TH}>实例</th>
            <th className={TH}>类型</th>
            <th className={TH}>环境</th>
            <th className={TH}>负责人</th>
            <th className={TH}>状态</th>
            <th className={TH}>接入地址</th>
            <th className={TH}>数据新鲜度</th>
          </tr>
        </thead>
        <tbody>
          {items.map((s) => (
            <tr key={s.id} className="border-b border-edge last:border-b-0">
              <td className={TD}>
                <span className="font-medium">{s.instance_id}</span>
                {s.source_watermark ? (
                  <p className="font-mono text-xs text-fg-muted">水位 {s.source_watermark}</p>
                ) : null}
              </td>
              <td className={TD}>{s.service_type}</td>
              <td className={TD}>{s.environment}</td>
              <td className={TD}>{s.owner}</td>
              <td className={TD}>
                <ServiceStatusBadge status={s.status} />
              </td>
              <td className={TD}>
                {/* 不做成可点链接：清单页点出去容易误触生产后台，
                    真要跳转走服务详情页（后续任务） */}
                <span className="font-mono text-xs break-all text-fg-muted">{s.endpoint}</span>
              </td>
              <td className={TD}>
                <ServiceFreshnessCell service={s} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ServiceFreshnessCell({ service }: { service: ServiceItem }) {
  const freshness = serviceFreshness(service);
  return (
    <div className="flex flex-col gap-1">
      <FreshnessBadge freshness={freshness} />
      <span className="text-xs text-fg-muted">
        {service.observed_at === null
          ? "从未成功采集"
          : `数据时间 ${formatUtcTimestamp(service.observed_at)}`}
      </span>
      {service.stale_seconds === null ? null : (
        <span className="text-xs text-fg-muted">落后 {formatDuration(service.stale_seconds)}</span>
      )}
    </div>
  );
}
