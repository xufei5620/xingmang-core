import { useQuery } from "@tanstack/react-query";
import { PageHeader } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import {
  listServerAssets,
  listServerDomains,
  listServerSuppliers,
  SERVER_ASSETS_QUERY,
  SERVER_DOMAINS_QUERY,
  SERVER_SUPPLIERS_QUERY,
} from "../api/server";
import { formatMinorUnits } from "../lib/money";
import { isExpiringSoon } from "../lib/serverRegistryForm";
import { ApiStateView } from "./ApiStateView";

/** 服务器概览（ADMIN-IA §2.1「服务器 · 概览」，`?tab=overview`）。
 *
 *  拍板「服务器只做记录」：这一格只做**登记汇总**——台数、按币种分别合计的
 *  月成本、30 天内到期的域名/证书/服务器数。不合成总健康分，不显示任何
 *  没有 Agent 支撑的实时判读（连接状态、资源水位仍在服务器详情页保持
 *  「未接入」，本页也不冒充它们）。 */
export function ServerOverviewPanel() {
  const assetsQuery = useQuery({
    queryKey: [SERVER_ASSETS_QUERY],
    queryFn: ({ signal }) => listServerAssets({ signal }),
  });
  const suppliersQuery = useQuery({
    queryKey: [SERVER_SUPPLIERS_QUERY],
    queryFn: ({ signal }) => listServerSuppliers({ signal }),
  });
  const domainsQuery = useQuery({
    queryKey: [SERVER_DOMAINS_QUERY],
    queryFn: ({ signal }) => listServerDomains({ signal }),
  });

  const isPending = assetsQuery.isPending || suppliersQuery.isPending || domainsQuery.isPending;
  const error = assetsQuery.error ?? suppliersQuery.error ?? domainsQuery.error;
  const refreshAll = () => {
    void assetsQuery.refetch();
    void suppliersQuery.refetch();
    void domainsQuery.refetch();
  };
  const lastRefreshedAt = Math.max(
    assetsQuery.dataUpdatedAt || 0,
    suppliersQuery.dataUpdatedAt || 0,
    domainsQuery.dataUpdatedAt || 0,
  );

  const assets = assetsQuery.data ?? [];
  const suppliers = suppliersQuery.data ?? [];
  const domains = domainsQuery.data ?? [];

  const activeCount = assets.filter((a) => a.status === "active").length;
  const retiredCount = assets.filter((a) => a.status === "retired").length;
  const plannedCount = assets.filter((a) => a.status === "planned").length;

  const costByCurrency = new Map<string, bigint>();
  for (const a of assets) {
    if (a.status === "retired") continue; // 已退役的成本不该计入「当前」月成本合计
    if (!a.monthly_cost_minor_units || !a.currency) continue;
    if (!/^-?\d+$/.test(a.monthly_cost_minor_units)) continue;
    costByCurrency.set(a.currency, (costByCurrency.get(a.currency) ?? 0n) + BigInt(a.monthly_cost_minor_units));
  }
  const costMissingCount = assets.filter(
    (a) => a.status !== "retired" && (!a.monthly_cost_minor_units || !a.currency),
  ).length;

  const assetsExpiring = assets.filter((a) => isExpiringSoon(a.expires_at)).length;
  const domainsExpiring = domains.filter((d) => isExpiringSoon(d.expires_at)).length;
  const certsExpiring = domains.filter((d) => isExpiringSoon(d.cert_expires_at)).length;

  return (
    <section className="flex flex-col gap-4">
      <PageHeader
        title="服务器 · 概览"
        description="按登记簿汇总的台数、月成本与到期风险；不合成健康分，不假装有 Agent 数据。"
        onRefresh={refreshAll}
        refreshing={assetsQuery.isFetching || suppliersQuery.isFetching || domainsQuery.isFetching}
        lastRefreshedAt={lastRefreshedAt || undefined}
      />

      <ApiStateView isPending={isPending} error={error} onRetry={refreshAll}>
        <div className="flex flex-col gap-4">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <Tile label="服务器资产" value={String(assets.length)} note={`使用中 ${activeCount} · 规划中 ${plannedCount} · 已退役 ${retiredCount}`} />
            <Tile label="供应商" value={String(suppliers.length)} note="已登记的服务器供应商数" />
            <Tile
              label="月成本合计"
              value={costByCurrency.size === 0 ? "—" : [...costByCurrency.entries()].map(([c, v]) => formatMinorUnits(v, c)).join(" + ")}
              note={
                costMissingCount > 0
                  ? `按币种分别合计，不做汇率折算；另有 ${costMissingCount} 台未登记成本`
                  : "按币种分别合计，不做汇率折算"
              }
              status={costMissingCount > 0 ? <Badge tone="warning">覆盖不全</Badge> : undefined}
            />
            <Tile
              label="30 天内到期"
              value={String(assetsExpiring + domainsExpiring + certsExpiring)}
              note={`服务器续费 ${assetsExpiring} · 域名 ${domainsExpiring} · 证书 ${certsExpiring}（含已过期）`}
              warn={assetsExpiring + domainsExpiring + certsExpiring > 0}
            />
          </div>

          <div className="rounded-lg border border-edge bg-surface p-4 shadow-sm">
            <h3 className="text-sm font-semibold text-fg">监控与告警</h3>
            <p className="mt-2 text-xs leading-5 text-fg-muted">
              按拍板「服务器只做记录」，本片不接入实时监控——主机指标、Agent 心跳与服务器专属告警仍显示「未接入」。
              需要处置的问题请前往「监控与告警」页签查看具体说明，或到全局「告警与故障」查看控制平面自身的告警。
            </p>
          </div>
        </div>
      </ApiStateView>
    </section>
  );
}

function Tile({
  label,
  value,
  note,
  warn = false,
  status,
}: {
  label: string;
  value: string;
  note: string;
  warn?: boolean;
  status?: ReactNode;
}) {
  return (
    <div className="rounded-lg border border-edge bg-surface p-3 shadow-sm">
      <div className="flex items-start justify-between gap-2">
        <p className="text-xs text-fg-muted">{label}</p>
        {status}
      </div>
      <p className={`mt-1 text-2xl font-semibold tabular-nums ${warn ? "text-warning" : "text-fg"}`}>{value}</p>
      <p className="mt-1 text-xs text-fg-muted">{note}</p>
    </div>
  );
}
