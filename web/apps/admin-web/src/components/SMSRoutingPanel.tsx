import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, FormField, Input } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import {
  listSMSProviders,
  listSMSRoutingRules,
  removeSMSRoutingRule,
  setSMSRoutingRule,
  type SMSRoutingRule,
} from "../api/sms";
import { ActionErrorNote } from "./ActionErrorNote";
import type { ActionResult } from "./ActionResultNote";

const ROUTING_KEY = ["sms", "routing"] as const;

/** 路由规则（XM-SMS2 #5，ADR-022 决策 3）。
 *
 *  「要号」默认由系统按规则选供应商，人只在想指定时才选。规则按「服务 × 国家」
 *  配：供应商的优先级列表（先加的先试，失败回落下一家，每家最多试一次）与
 *  单价上限。同「服务 × 国家」只有一条，再保存就是覆盖。
 *
 *  改规则不花钱，但决定以后每次要号的钱花到哪家——写走 `sms.routing.*` Action，
 *  需要 sms.manage。 */
export function SMSRoutingPanel({ onWrite }: { onWrite: (r: ActionResult) => void }) {
  const queryClient = useQueryClient();
  const providersQuery = useQuery({ queryKey: ["sms", "providers"], queryFn: () => listSMSProviders() });
  const rulesQuery = useQuery({ queryKey: ROUTING_KEY, queryFn: () => listSMSRoutingRules() });

  const providers = providersQuery.data ?? [];
  // 只有能买号的供应商才能进规则：邮箱专用或只读的那家出现在这里没有意义。
  const purchasable = providers.filter((p) => (p.capabilities ?? []).includes("purchase"));
  const labelOf = (id: string) => providers.find((p) => p.provider === id)?.label ?? id;

  const [service, setService] = useState("");
  const [country, setCountry] = useState("*");
  const [order, setOrder] = useState<string[]>([]);
  const [maxPrice, setMaxPrice] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [armedRemove, setArmedRemove] = useState<string | null>(null);
  const formId = useId();

  function edit(rule: SMSRoutingRule) {
    setService(rule.service);
    setCountry(rule.country);
    setOrder([...rule.providers]);
    setMaxPrice(rule.max_unit_price ?? "");
    setEnabled(rule.enabled);
    setError(null);
  }

  function reset() {
    setService("");
    setCountry("*");
    setOrder([]);
    setMaxPrice("");
    setEnabled(true);
    setError(null);
  }

  function move(id: string, delta: -1 | 1) {
    setOrder((prev) => {
      const i = prev.indexOf(id);
      const j = i + delta;
      if (i < 0 || j < 0 || j >= prev.length) return prev;
      const next = [...prev];
      [next[i], next[j]] = [next[j]!, next[i]!];
      return next;
    });
  }

  const save = useMutation({
    mutationFn: () =>
      setSMSRoutingRule({
        service: service.trim(),
        country: country.trim(),
        providers: order,
        ...(maxPrice.trim() ? { max_unit_price: maxPrice.trim() } : {}),
        enabled,
      }),
    onSuccess: (run) => {
      onWrite({ runId: run.runId, title: `已保存路由 ${service.trim()} / ${country.trim()}` });
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ROUTING_KEY });
    },
    onError: setError,
  });

  const remove = useMutation({
    mutationFn: (ruleId: string) => removeSMSRoutingRule(ruleId),
    onSuccess: (run) => {
      onWrite({ runId: run.runId, title: "已删除路由规则" });
      setArmedRemove(null);
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ROUTING_KEY });
    },
    onError: setError,
  });

  const canSave = service.trim() !== "" && country.trim() !== "" && order.length > 0 && !save.isPending;
  const rules = rulesQuery.data?.items ?? [];
  const defaultOrder = rulesQuery.data?.default_order ?? [];

  const columns: DataTableColumn<SMSRoutingRule>[] = [
    { id: "service", header: "服务", primary: true, cell: (r) => r.service, value: (r) => r.service },
    { id: "country", header: "国家", cell: (r) => r.country, value: (r) => r.country },
    {
      id: "providers",
      header: "供应商顺序",
      cell: (r) => r.providers.map(labelOf).join(" → "),
      value: (r) => r.providers.map(labelOf).join(" → "),
    },
    { id: "price", header: "单价上限", numeric: true, cell: (r) => r.max_unit_price || "不限" },
    {
      id: "enabled",
      header: "状态",
      cell: (r) => <Badge tone={r.enabled ? "success" : "neutral"}>{r.enabled ? "启用" : "停用"}</Badge>,
      value: (r) => (r.enabled ? "启用" : "停用"),
    },
    { id: "updated", header: "更新时间", cell: (r) => (r.updated_at ? formatUtcTimestamp(r.updated_at) : "—") },
    {
      id: "ops",
      header: "操作",
      cell: (r) => (
        <span className="flex flex-wrap items-center gap-2">
          <Button variant="secondary" size="sm" onClick={() => edit(r)}>
            编辑
          </Button>
          {armedRemove === r.rule_id ? (
            <>
              <Button variant="danger" size="sm" disabled={remove.isPending} onClick={() => remove.mutate(r.rule_id)}>
                确认删除
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setArmedRemove(null)}>
                取消
              </Button>
            </>
          ) : (
            <Button variant="ghost" size="sm" onClick={() => setArmedRemove(r.rule_id)}>
              删除
            </Button>
          )}
        </span>
      ),
    },
  ];

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <section className="border-edge flex min-w-0 flex-col gap-3 rounded-md border p-3">
        <div>
          <h3 className="text-sm font-semibold">新建 / 覆盖规则</h3>
          <p className="text-fg-muted text-xs">
            按「服务 × 国家」配供应商的优先级；同一组合只有一条，再保存就是覆盖。命中顺序：
            精确 &gt; 服务通配国家 &gt; 国家通配服务 &gt; 全通配。
          </p>
        </div>
        <div className="grid gap-3 md:grid-cols-3">
          <FormField label="服务" htmlFor={`${formId}-svc`} hint="Hero 的服务代号或 62 的平台；* = 任意服务。">
            <Input
              id={`${formId}-svc`}
              aria-label="路由服务"
              value={service}
              onChange={(e) => setService(e.target.value)}
            />
          </FormField>
          <FormField label="国家" htmlFor={`${formId}-country`} hint="* = 任意国家。">
            <Input
              id={`${formId}-country`}
              aria-label="路由国家"
              value={country}
              onChange={(e) => setCountry(e.target.value)}
            />
          </FormField>
          <FormField
            label="单价上限"
            htmlFor={`${formId}-price`}
            hint="留空不限。按该家自己的币种比较，不折算（62 是 USD）。"
          >
            <Input
              id={`${formId}-price`}
              aria-label="路由单价上限"
              inputMode="decimal"
              value={maxPrice}
              onChange={(e) => setMaxPrice(e.target.value)}
            />
          </FormField>
        </div>

        <div className="flex flex-col gap-2">
          <span className="text-xs font-medium">供应商优先级（先加的先试，失败回落下一家，每家最多试一次）</span>
          {order.length ? (
            <ol className="flex flex-col gap-1">
              {order.map((id, i) => (
                <li key={id} className="flex flex-wrap items-center gap-2 text-sm">
                  <span className="min-w-32">{`${i + 1}. ${labelOf(id)}`}</span>
                  <Button variant="ghost" size="sm" aria-label={`上移 ${labelOf(id)}`} disabled={i === 0} onClick={() => move(id, -1)}>
                    上移
                  </Button>
                  <Button variant="ghost" size="sm" aria-label={`移除 ${labelOf(id)}`} onClick={() => setOrder((p) => p.filter((x) => x !== id))}>
                    移除
                  </Button>
                </li>
              ))}
            </ol>
          ) : (
            <p className="text-fg-muted text-xs">还没有加入供应商。</p>
          )}
          <div className="flex flex-wrap items-center gap-2">
            {purchasable
              .filter((p) => !order.includes(p.provider))
              .map((p) => (
                <Button
                  key={p.provider}
                  variant="secondary"
                  size="sm"
                  aria-label={`加入 ${labelOf(p.provider)}`}
                  onClick={() => setOrder((prev) => [...prev, p.provider])}
                >
                  + {labelOf(p.provider)}
                </Button>
              ))}
          </div>
        </div>

        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          启用这条规则
        </label>

        {error ? <ActionErrorNote error={error} /> : null}
        <div className="flex items-center gap-2">
          <Button size="sm" disabled={!canSave} onClick={() => save.mutate()}>
            {save.isPending ? "保存中…" : "保存规则"}
          </Button>
          <Button variant="ghost" size="sm" onClick={reset}>
            清空表单
          </Button>
        </div>
      </section>

      <section className="flex min-w-0 flex-col gap-2">
        {rulesQuery.isError ? <ActionErrorNote error={rulesQuery.error} /> : null}
        <DataTableV2
          caption="路由规则"
          columns={columns}
          rows={rules}
          rowKey={(r) => r.rule_id}
          emptyState={<p className="text-fg-muted text-sm">还没有路由规则。要号会按默认顺序逐家尝试。</p>}
        />
        <p className="text-fg-muted text-xs">
          {`没有规则命中时，要号按默认顺序：${defaultOrder.map(labelOf).join(" → ") || "—"}（即装配顺序）。人指定了供应商就只用那一家。`}
        </p>
      </section>
    </div>
  );
}
