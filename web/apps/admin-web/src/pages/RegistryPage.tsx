import { useQuery } from "@tanstack/react-query";
import {
  FreshnessBadge,
  PageHeader,
  ServiceStatusBadge,
  formatDuration,
  formatUtcTimestamp,
  navLabel,
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
import { RegisterServiceDialog } from "../components/RegisterServiceDialog";

const TH = "px-3 py-2 text-left text-xs font-medium text-fg-muted";
const TD = "px-3 py-2 align-top text-sm text-fg";

/** 注册表：被管理系统实例一览 + 两个写路径（登记 / 上报观测）。
 *
 *  XM-0034 起从「服务清单」改挂到平台治理段（ADMIN-IA 三、迁移映射），
 *  路径由 /services 改为 /registry，页面内容整体照搬——旧地址仍会重定向过来。
 *
 *  采集时间同样按新鲜度语义展示——「实例在册」不等于「数据是新的」。 */
export function RegistryPage() {
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
        title={navLabel("/registry")}
        description={`采集时间超过 ${formatDuration(SERVICE_STALENESS_THRESHOLD_SECONDS)} 记为数据延迟（阈值由前端设定，registry 未提供）。这是一个对所有服务一刀切的临时值，各服务的真实阈值待后端契约提供（XM-0017）。`}
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
        actions={
          <RegisterServiceDialog
            environment={environment}
            onRegistered={(runId) =>
              // 不承诺「一定能在审计页看到」：业务写、ActionRun、审计追加三段不是
              // 原子的，审计还是 fail-open（#38/#40 未关闭），写成功而审计没落库
              // 是可能发生的。UI 不能把一个尚未成立的后端保证说成事实（Codex #10）
              afterWrite(`已登记，run_id=${runId}；审计事件通常几秒内出现在审计页`)
            }
          />
        }
      />

      {/* ADMIN-IA 给注册表的职责是「服务/连接器/连接三张表」，眼下只有服务这一张。
          少的两张明说出来，否则这一页看起来就像注册表的全部（§12 惯例） */}
      <p className="mb-3 text-xs text-fg-muted">
        当前只有「服务」一张表；连接器与连接两张表尚未建，登记同样走 Action。
      </p>

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
                      `已上报观测，run_id=${runId}；审计事件通常几秒内出现在审计页`,
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
