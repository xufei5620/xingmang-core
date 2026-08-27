import { useQuery } from "@tanstack/react-query";
import {
  FreshnessBadge,
  ServiceStatusBadge,
  formatDuration,
  formatUtcTimestamp,
} from "@xingmang/ui-admin";
import { EmptyState } from "@xingmang/ui-primitives";
import { useState } from "react";
import { appApiConfig } from "../api/config";
import {
  listMetrics,
  listServices,
  serviceFreshness,
  SERVICE_STALENESS_THRESHOLD_SECONDS,
  type ServiceItem,
} from "../api/platform";
import { ApiStateView } from "../components/ApiStateView";
import { ObserveServiceDialog } from "../components/ObserveServiceDialog";
import { PageHeader } from "../components/PageHeader";
import { RegisterServiceDialog } from "../components/RegisterServiceDialog";

const TH = "px-3 py-2 text-left text-xs font-medium text-fg-muted";
const TD = "px-3 py-2 align-top text-sm text-fg";

/** 服务清单：被管理系统实例一览 + 两个写路径（登记 / 上报观测）。
 *
 *  采集时间同样按新鲜度语义展示——「实例在册」不等于「数据是新的」。 */
export function ServicesPage() {
  const query = useQuery({
    queryKey: ["services"],
    queryFn: ({ signal }) => listServices({ signal }),
  });
  // 只为拿到「当前身份属于哪个环境」而顺带读一次指标；已经缓存过就不会再发请求
  const metricsQuery = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });

  const [notice, setNotice] = useState<string | null>(null);

  const environment = resolveEnvironment(
    query.data ?? [],
    (metricsQuery.data ?? []).map((m) => m.environment),
  );

  const afterWrite = (message: string) => {
    setNotice(message);
    void query.refetch();
  };

  return (
    <section>
      <PageHeader
        title="服务清单"
        description={`采集时间超过 ${formatDuration(SERVICE_STALENESS_THRESHOLD_SECONDS)} 记为数据延迟（阈值由前端设定，registry 未提供）。`}
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
        actions={
          <RegisterServiceDialog
            environment={environment}
            onRegistered={(runId) =>
              afterWrite(`已登记，run_id=${runId}，可在审计页查看这条事件`)
            }
          />
        }
      />

      {notice ? (
        // status 而不是 alert：这是一条成功回执，不该抢走屏幕阅读器的当前焦点
        <p
          role="status"
          className="mb-3 rounded-md border border-success bg-success/10 px-3 py-2 text-xs text-success"
        >
          {notice}
        </p>
      ) : null}

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <ServicesTable items={query.data ?? []} onObserved={afterWrite} />
      </ApiStateView>
    </section>
  );
}

/** 当前身份所属环境。
 *
 *  前端并不真的知道服务端把自己配成了哪个环境（config.ts 说明了为什么默认不传
 *  environment），但写 Action 时 environment 是必填参数。于是按可信度取值：
 *  显式配置 > 已有服务记录 > 已有指标记录。三样都没有就返回空串，
 *  由表单显示「未知」并禁用提交——猜一个只会换来 409/403。 */
export function resolveEnvironment(services: ServiceItem[], metricEnvironments: string[]): string {
  if (appApiConfig.environment) return appApiConfig.environment;
  const fromService = services.find((s) => s.environment)?.environment;
  if (fromService) return fromService;
  return metricEnvironments.find((e) => e) ?? "";
}

function ServicesTable({
  items,
  onObserved,
}: {
  items: ServiceItem[];
  onObserved: (message: string) => void;
}) {
  if (items.length === 0) {
    return (
      <EmptyState
        title="暂无服务"
        description="该环境下还没有登记任何被管理系统实例；用右上角的「登记服务」按钮登记第一个"
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
            <th className={TH}>
              <span className="sr-only">操作</span>
            </th>
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
              <td className={TD}>
                <ObserveServiceDialog
                  service={s}
                  onObserved={(runId) =>
                    onObserved(
                      `已上报观测，run_id=${runId}，可在审计页查看这条事件`,
                    )
                  }
                />
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
