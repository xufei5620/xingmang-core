import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import {
  executeSMSEmailAction,
  executeSMSResourceAction,
  getHeroBalance,
  getHeroHistory,
  getHeroStats,
  getSMS62GoodsDetail,
  getSMSEmail,
  listExtendOptions,
  listHeroCountries,
  listHeroCustomDurations,
  listHeroEmailDomains,
  listHeroPrices,
  listHeroServices,
  listHeroTopCountries,
  listProlongHistory,
  listSMS62Orders,
  listSMSEmails,
  listUpstreamCodes,
  purchaseSMSEmails,
  rentSMSNumber,
  setSMSFavorite,
  type HeroCountry,
  type HeroCustomDuration,
  type HeroExtendOption,
  type HeroHistoryItem,
  type HeroPriceRow,
  type HeroProlongRecord,
  type HeroService,
  type HeroStatsEntry,
  type HeroTopCountry,
  type SMS62Order,
  type SMSCode,
  type SMSEmail,
  type SMSResource,
} from "../api/sms";
import { ActionErrorNote } from "./ActionErrorNote";
import { type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

/** XM-SMS1：两家官方文档补齐后的扩展面板。
 *
 *  每个面板都是**实时上游调用**，不是投影——历史、统计、目录、报价这些只在人要
 *  看的时候才有用，落库只会让页面显示一份过期的价格。写全部走 Action。
 *
 *  金额一律是十进制文本（`*_text`），**原样显示、不做 Number()**：服务端已经
 *  把它当文本传过来了，前端再过一遍浮点正是误差进来的地方。 */

const EMAILS_QUERY = "sms-emails";

function opId(): string {
  return crypto.randomUUID();
}

// ---------- 供应商卡上的余额 ----------

/** Hero 的余额（兼容层 getBalance）。币种上游没说，**不补一个 USD**。 */
export function HeroBalance() {
  const query = useQuery({
    queryKey: ["sms-hero-balance"],
    queryFn: ({ signal }) => getHeroBalance({ signal }),
    staleTime: 60_000,
    retry: false,
  });
  if (query.isError) {
    return <span className="text-fg-muted text-xs">余额读取失败</span>;
  }
  if (!query.data) return null;
  return (
    <span className="text-fg-muted font-mono text-xs">余额 {query.data.balance_text}（币种以后台为准）</span>
  );
}

// ---------- 目录与价格（Hero） ----------

export function HeroCatalogPanel({ onWrite }: { onWrite: (r: ActionResult) => void }) {
  const [country, setCountry] = useState("");
  const [service, setService] = useState("");
  const countryNum = Number(country) || 0;

  const countries = useQuery({
    queryKey: ["sms-hero-countries"],
    queryFn: ({ signal }) => listHeroCountries({ signal }),
    staleTime: 10 * 60_000,
  });
  const services = useQuery({
    queryKey: ["sms-hero-services", countryNum],
    queryFn: ({ signal }) => listHeroServices(countryNum, "cn", { signal }),
    staleTime: 10 * 60_000,
  });
  const prices = useQuery({
    queryKey: ["sms-hero-prices", service, countryNum],
    queryFn: ({ signal }) => listHeroPrices(service, countryNum, { signal }),
    staleTime: 60_000,
  });
  const top = useQuery({
    queryKey: ["sms-hero-top", service],
    queryFn: ({ signal }) => listHeroTopCountries(service, false, { signal }),
    enabled: service !== "",
    staleTime: 60_000,
  });
  const durations = useQuery({
    queryKey: ["sms-hero-custom-durations"],
    queryFn: ({ signal }) => listHeroCustomDurations({ signal }),
    staleTime: 10 * 60_000,
  });

  const countryName = (id: string | number) => {
    const c = (countries.data ?? []).find((x) => String(x.id) === String(id));
    return c ? `${c.name_cn || c.name_en}（${c.id}）` : String(id);
  };
  const serviceName = (code: string) => {
    const s = (services.data ?? []).find((x) => x.code === code);
    return s ? `${s.name}（${s.code}）` : code;
  };

  return (
    <section className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-start gap-3">
        <FormField label="国家" htmlFor="hero-country" hint="留空 = 全部。">
          <Select
            aria-label="国家"
            value={country}
            onValueChange={setCountry}
            options={[
              { value: "", label: "全部国家" },
              ...(countries.data ?? [])
                .filter((c) => c.visible)
                .map((c: HeroCountry) => ({ value: String(c.id), label: `${c.name_cn || c.name_en}（${c.id}）` })),
            ]}
          />
        </FormField>
        <FormField label="服务" htmlFor="hero-service" hint="留空 = 全部；选了才有 Top 国家。">
          <Select
            aria-label="服务"
            value={service}
            onValueChange={setService}
            options={[
              { value: "", label: "全部服务" },
              ...(services.data ?? []).map((s: HeroService) => ({ value: s.code, label: `${s.name}（${s.code}）` })),
            ]}
          />
        </FormField>
        {service && country ? (
          <FavoriteButton service={service} country={countryNum} onWrite={onWrite} />
        ) : null}
      </div>

      <div className="grid min-w-0 gap-4 xl:grid-cols-2">
        <div className="border-edge flex min-w-0 flex-col gap-2 rounded-md border p-3">
          <h3 className="text-sm font-semibold">价格与库存</h3>
          <p className="text-fg-muted text-xs">
            兼容层 getPrices。physical_count 是实体号数量，count 含虚拟号——两个数不一样是正常的。
          </p>
          <ApiStateView isPending={prices.isPending} error={prices.error} onRetry={() => void prices.refetch()} compact>
            <DataTableV2
              caption="Hero-SMS 各国家各服务的价格与库存"
              columns={
                [
                  { id: "country", header: "国家", primary: true, cell: (r) => countryName(r.country), value: (r) => r.country },
                  { id: "service", header: "服务", cell: (r) => serviceName(r.service), value: (r) => r.service },
                  { id: "cost", header: "价格", numeric: true, cell: (r) => r.cost_text, value: (r) => r.cost_text },
                  { id: "count", header: "库存", numeric: true, cell: (r) => String(r.count), value: (r) => r.count },
                  { id: "physical", header: "实体号", numeric: true, cell: (r) => String(r.physical_count), value: (r) => r.physical_count },
                ] satisfies DataTableColumn<HeroPriceRow>[]
              }
              rows={prices.data ?? []}
              rowKey={(r) => `${r.country}/${r.service}`}
              pageSize={20}
              emptyState={<p className="text-fg-muted text-sm">没有匹配的价格。</p>}
            />
          </ApiStateView>
        </div>

        <div className="flex min-w-0 flex-col gap-4">
          <div className="border-edge flex min-w-0 flex-col gap-2 rounded-md border p-3">
            <h3 className="text-sm font-semibold">Top 国家</h3>
            {!service ? (
              <p className="text-fg-muted text-sm">先选一个服务。</p>
            ) : (
              <ApiStateView isPending={top.isPending} error={top.error} onRetry={() => void top.refetch()} compact>
                <DataTableV2
                  caption="某服务的推荐国家"
                  columns={
                    [
                      { id: "country", header: "国家", primary: true, cell: (r) => countryName(r.country), value: (r) => r.country },
                      { id: "price", header: "价格", numeric: true, cell: (r) => r.price_text, value: (r) => r.price_text },
                      { id: "retail", header: "零售价", numeric: true, cell: (r) => r.retail_price_text || "—" },
                      { id: "count", header: "库存", numeric: true, cell: (r) => String(r.count), value: (r) => r.count },
                    ] satisfies DataTableColumn<HeroTopCountry>[]
                  }
                  rows={top.data ?? []}
                  rowKey={(r) => String(r.country)}
                  emptyState={<p className="text-fg-muted text-sm">这个服务没有推荐国家。</p>}
                />
              </ApiStateView>
            )}
          </div>

          <div className="border-edge flex min-w-0 flex-col gap-2 rounded-md border p-3">
            <h3 className="text-sm font-semibold">非标准时长</h3>
            <p className="text-fg-muted text-xs">
              这些服务在这些国家的号<strong>不是默认 20 分钟</strong>。买之前看一眼，免得以为买到的是短时号。
            </p>
            <ApiStateView isPending={durations.isPending} error={durations.error} onRetry={() => void durations.refetch()} compact>
              <DataTableV2
                caption="非标准激活时长目录"
                columns={
                  [
                    { id: "service", header: "服务", primary: true, cell: (r) => serviceName(r.service), value: (r) => r.service },
                    { id: "country", header: "国家", cell: (r) => countryName(r.country), value: (r) => r.country },
                    { id: "hours", header: "小时", numeric: true, cell: (r) => String(r.hours), value: (r) => r.hours },
                  ] satisfies DataTableColumn<HeroCustomDuration>[]
                }
                rows={durations.data ?? []}
                rowKey={(r) => `${r.service}/${r.country}`}
                pageSize={10}
                emptyState={<p className="text-fg-muted text-sm">全部是默认时长。</p>}
              />
            </ApiStateView>
          </div>
        </div>
      </div>
    </section>
  );
}

function FavoriteButton({ service, country, onWrite }: { service: string; country: number; onWrite: (r: ActionResult) => void }) {
  const [error, setError] = useState<unknown>(null);
  const mutation = useMutation({
    mutationFn: () => setSMSFavorite({ service, country }),
    onSuccess: (run) => {
      onWrite({ runId: run.runId, title: `已收藏 ${service} / ${country}` });
      setError(null);
    },
    onError: setError,
  });
  return (
    <span className="flex items-center gap-2">
      <Button variant="secondary" size="sm" disabled={mutation.isPending} onClick={() => mutation.mutate()}>
        收藏这个组合
      </Button>
      {error ? <ActionErrorNote error={error} /> : null}
    </span>
  );
}

// ---------- 历史与统计（Hero） ----------

const HISTORY_STATUS: Record<number, string> = {
  1: "等待码", 2: "等待重发", 3: "已请求重发", 4: "已收到", 6: "已完成", 7: "已过期", 8: "已取消", 10: "已退款",
};

function isoDate(d: Date): string {
  return d.toISOString().slice(0, 10);
}

export function HeroHistoryPanel() {
  const [from, setFrom] = useState(isoDate(new Date(Date.now() - 7 * 24 * 3600 * 1000)));
  const [to, setTo] = useState(isoDate(new Date()));
  const [page, setPage] = useState(1);

  const history = useQuery({
    queryKey: ["sms-hero-history", from, to, page],
    queryFn: ({ signal }) => getHeroHistory({ from, to, page, size: 50 }, { signal }),
    staleTime: 30_000,
  });
  const stats = useQuery({
    queryKey: ["sms-hero-stats", to],
    queryFn: ({ signal }) => getHeroStats(to, { signal }),
    staleTime: 60_000,
  });

  return (
    <section className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-start gap-3">
        <FormField label="从" htmlFor="hist-from">
          <Input id="hist-from" aria-label="从" type="date" value={from} onChange={(e) => { setFrom(e.target.value); setPage(1); }} />
        </FormField>
        <FormField label="到" htmlFor="hist-to" hint="统计取「到」这一天。">
          <Input id="hist-to" aria-label="到" type="date" value={to} onChange={(e) => { setTo(e.target.value); setPage(1); }} />
        </FormField>
      </div>

      <div className="border-edge flex min-w-0 flex-col gap-2 rounded-md border p-3">
        <h3 className="text-sm font-semibold">{to} 的统计</h3>
        <ApiStateView isPending={stats.isPending} error={stats.error} onRetry={() => void stats.refetch()} compact>
          <DataTableV2
            caption="当天按国家与服务的激活统计"
            columns={
              [
                { id: "country", header: "国家", primary: true, cell: (r) => r.country, value: (r) => r.country },
                { id: "service", header: "服务", cell: (r) => r.service, value: (r) => r.service },
                { id: "count", header: "数量", numeric: true, cell: (r) => String(r.count), value: (r) => r.count },
                { id: "sum", header: "金额", numeric: true, cell: (r) => r.sum_text || "—" },
              ] satisfies DataTableColumn<HeroStatsEntry>[]
            }
            rows={stats.data?.items ?? []}
            rowKey={(r) => `${r.country}/${r.service}`}
            emptyState={<p className="text-fg-muted text-sm">这一天没有激活。</p>}
          />
        </ApiStateView>
      </div>

      <div className="border-edge flex min-w-0 flex-col gap-2 rounded-md border p-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-sm font-semibold">激活历史</h3>
          {history.data ? (
            <span className="text-fg-muted text-xs">
              本页合计 {history.data.page_sum_text || "—"} · 成功 {history.data.page_success_count} · 共 {history.data.total} 条
            </span>
          ) : null}
        </div>
        <ApiStateView isPending={history.isPending} error={history.error} onRetry={() => void history.refetch()} compact>
          <DataTableV2
            caption="Hero-SMS 激活历史"
            columns={
              [
                { id: "date", header: "时间", primary: true, cell: (r) => r.create_date, value: (r) => r.create_date },
                { id: "service", header: "服务", cell: (r) => r.service, value: (r) => r.service },
                { id: "country", header: "国家", cell: (r) => String(r.country), value: (r) => r.country },
                { id: "phone", header: "号码", cell: (r) => r.phone },
                { id: "cost", header: "费用", numeric: true, cell: (r) => r.cost_text },
                { id: "status", header: "状态", cell: (r) => <Badge tone="neutral">{HISTORY_STATUS[r.status] ?? String(r.status)}</Badge>, value: (r) => r.status },
              ] satisfies DataTableColumn<HeroHistoryItem>[]
            }
            rows={history.data?.items ?? []}
            rowKey={(r) => r.id}
            emptyState={<p className="text-fg-muted text-sm">这个区间没有激活。</p>}
          />
          <div className="flex items-center gap-2">
            <Button variant="secondary" size="sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>上一页</Button>
            <span className="text-fg-muted text-xs">第 {page} 页</span>
            <Button variant="secondary" size="sm" disabled={!history.data?.has_more} onClick={() => setPage(page + 1)}>下一页</Button>
          </div>
        </ApiStateView>
      </div>
    </section>
  );
}

// ---------- 邮箱接码（Hero） ----------

export function HeroEmailsPanel({ onWrite }: { onWrite: (r: ActionResult) => void }) {
  const queryClient = useQueryClient();
  const emails = useQuery({
    queryKey: [EMAILS_QUERY],
    queryFn: ({ signal }) => listSMSEmails({ signal }),
    staleTime: 15_000,
  });

  function afterWrite(r: ActionResult) {
    onWrite(r);
    void queryClient.invalidateQueries({ queryKey: [EMAILS_QUERY] });
  }

  return (
    <section className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">邮箱接码</h3>
          <p className="text-fg-muted text-xs">
            按「站点 + 域名」买一个邮箱，收到的验证内容显示在这里。花真钱；批量最多 10 个。
          </p>
        </div>
        <EmailPurchaseDialog onDone={afterWrite} />
      </div>
      <ApiStateView isPending={emails.isPending} error={emails.error} onRetry={() => void emails.refetch()} compact>
        {(emails.data ?? []).length === 0 ? (
          <p className="text-fg-muted text-sm">还没有邮箱。先在右上角买一个。</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {(emails.data ?? []).map((e) => (
              <EmailRow key={e.email_id} email={e} onWrite={afterWrite} />
            ))}
          </ul>
        )}
      </ApiStateView>
    </section>
  );
}

function emailTone(status: string): "success" | "warning" | "neutral" {
  if (status === "SUCCESS") return "success";
  if (status === "WAIT") return "warning";
  return "neutral";
}

function EmailRow({ email, onWrite }: { email: SMSEmail; onWrite: (r: ActionResult) => void }) {
  const queryClient = useQueryClient();
  const [error, setError] = useState<unknown>(null);
  const refresh = useMutation({
    // **人发起**的刷新，不轮询：常驻轮询会把配额烧在没人看的邮箱上。
    mutationFn: () => getSMSEmail(email.email_id, true),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: [EMAILS_QUERY] }),
    onError: setError,
  });
  const act = useMutation({
    mutationFn: (kind: "email_cancel" | "email_reorder") =>
      executeSMSEmailAction({ operation_id: opId(), email_id: email.email_id, kind }),
    onSuccess: (run) => {
      onWrite({ runId: run.runId, title: "已提交邮箱动作" });
      setError(null);
    },
    onError: setError,
  });
  return (
    <li className="border-edge flex flex-wrap items-center justify-between gap-3 rounded-md border p-3">
      <span className="flex min-w-0 flex-col">
        <span className="flex items-center gap-2">
          <span className="font-mono text-sm font-semibold">{email.email}</span>
          <Badge tone={emailTone(email.status)}>{email.status || "—"}</Badge>
        </span>
        <span className="text-fg-muted text-xs">
          {email.site} · {email.cost_text} · {email.upstream_date ? formatUtcTimestamp(email.upstream_date) : "—"}
        </span>
        {email.has_value ? (
          <span className="font-mono text-lg font-semibold tabular-nums">
            {email.value ?? "（需要 sms.reveal 权限）"}
          </span>
        ) : (
          <span className="text-fg-muted text-xs">还没收到验证内容。点「刷新」从上游读一次。</span>
        )}
      </span>
      <span className="flex flex-wrap items-center gap-2">
        <Button variant="secondary" size="sm" disabled={refresh.isPending} onClick={() => refresh.mutate()}>
          {refresh.isPending ? "读取中…" : "刷新"}
        </Button>
        <Button variant="secondary" size="sm" disabled={act.isPending} onClick={() => act.mutate("email_reorder")}>
          重下单
        </Button>
        <Button variant="secondary" size="sm" disabled={act.isPending} onClick={() => act.mutate("email_cancel")}>
          取消
        </Button>
        {error ? <ActionErrorNote error={error} /> : null}
      </span>
    </li>
  );
}

function EmailPurchaseDialog({ onDone }: { onDone: (r: ActionResult) => void }) {
  const [open, setOpen] = useState(false);
  const [site, setSite] = useState("");
  const [domain, setDomain] = useState("");
  const [count, setCount] = useState("1");
  const [armed, setArmed] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  const domains = useQuery({
    queryKey: ["sms-hero-email-domains", site],
    queryFn: ({ signal }) => listHeroEmailDomains(site, { signal }),
    enabled: open,
    staleTime: 60_000,
  });
  const mutation = useMutation({
    mutationFn: (operationId: string) =>
      purchaseSMSEmails({ operation_id: operationId, site: site.trim(), domain: domain.trim(), count: Number(count) || 1 }),
    onSuccess: (run) => {
      onDone({ runId: run.runId, title: "已提交邮箱购买" });
      setOpen(false);
      setArmed(null);
      setError(null);
    },
    // 失败**不清 armed**：那是同一笔业务的重试，幂等键必须保持不变。
    onError: setError,
  });

  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      title="买邮箱"
      description="买到的邮箱不可退。批量最多 10 个——那是官方上限。"
      trigger={<Button size="sm" className="whitespace-nowrap">买邮箱</Button>}
    >
      <div className="flex flex-col gap-3">
        <FormField label="站点" htmlFor={`${formId}-site`} hint="要注册的网站，如 example.com。">
          <Input id={`${formId}-site`} aria-label="站点" value={site} onChange={(e) => { setSite(e.target.value); setArmed(null); }} />
        </FormField>
        <FormField label="域名" htmlFor={`${formId}-domain`} hint="从上游可用域名里选；价格是每个邮箱的价。">
          <Select
            aria-label="域名"
            value={domain}
            onValueChange={(v) => { setDomain(v); setArmed(null); }}
            options={(domains.data ?? []).map((d) => ({ value: d.name, label: `${d.name} · ${d.cost_text} · 剩 ${d.count}` }))}
          />
        </FormField>
        <FormField label="数量" htmlFor={`${formId}-count`} hint="1–10。">
          <Input id={`${formId}-count`} aria-label="数量" value={count} onChange={(e) => { setCount(e.target.value); setArmed(null); }} />
        </FormField>
        {error ? <ActionErrorNote error={error} /> : null}
        <div className="flex items-center gap-2">
          {armed ? (
            <>
              <Button variant="danger" size="sm" disabled={mutation.isPending} onClick={() => mutation.mutate(armed)}>
                {mutation.isPending ? "提交中…" : `确认买 ${count} 个邮箱`}
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setArmed(null)}>取消</Button>
            </>
          ) : (
            <Button size="sm" disabled={!site.trim() || !domain} onClick={() => setArmed(opId())}>
              下一步
            </Button>
          )}
        </div>
      </div>
    </Dialog>
  );
}

// ---------- 租用（Hero 兼容层） ----------

export function HeroRentPanel({ onWrite }: { onWrite: (r: ActionResult) => void }) {
  const [service, setService] = useState("");
  const [country, setCountry] = useState("");
  const [hours, setHours] = useState("4");
  const [operator, setOperator] = useState("");
  const [armed, setArmed] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  const mutation = useMutation({
    mutationFn: (operationId: string) =>
      rentSMSNumber({
        operation_id: operationId,
        service: service.trim(),
        country: Number(country) || 0,
        duration_hours: Number(hours) || 0,
        ...(operator.trim() ? { operator: operator.trim() } : {}),
      }),
    onSuccess: (run) => {
      onWrite({ runId: run.runId, title: "已提交租用请求" });
      setArmed(null);
      setError(null);
    },
    onError: setError,
  });

  return (
    <section className="border-edge flex min-w-0 flex-col gap-3 rounded-md border p-3">
      <div>
        <h3 className="text-sm font-semibold">租用号码</h3>
        <p className="text-fg-muted text-xs">
          按小时租一个号（官方 getRentNumber）。<strong>花真钱，按小时计费，租出去不退。</strong>
          租到的号在「号码」页里标为「租用」，可以延长。
        </p>
      </div>
      <div className="grid gap-3 md:grid-cols-4">
        <FormField label="服务代号" htmlFor={`${formId}-svc`} hint="2–4 位小写字母或数字。">
          <Input id={`${formId}-svc`} aria-label="租用服务代号" value={service} onChange={(e) => { setService(e.target.value); setArmed(null); }} />
        </FormField>
        <FormField label="国家代码" htmlFor={`${formId}-country`} hint="0–999。">
          <Input id={`${formId}-country`} aria-label="租用国家代码" value={country} onChange={(e) => { setCountry(e.target.value); setArmed(null); }} />
        </FormField>
        <FormField label="小时数" htmlFor={`${formId}-hours`} hint="官方 RentDuration，必填。">
          <Input id={`${formId}-hours`} aria-label="租用小时数" value={hours} onChange={(e) => { setHours(e.target.value); setArmed(null); }} />
        </FormField>
        <FormField label="运营商" htmlFor={`${formId}-op`} hint="可选。">
          <Input id={`${formId}-op`} aria-label="租用运营商" value={operator} onChange={(e) => setOperator(e.target.value)} />
        </FormField>
      </div>
      {error ? <ActionErrorNote error={error} /> : null}
      <div className="flex items-center gap-2">
        {armed ? (
          <>
            <Button variant="danger" size="sm" disabled={mutation.isPending} onClick={() => mutation.mutate(armed)}>
              {mutation.isPending ? "提交中…" : `确认租 ${hours} 小时`}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setArmed(null)}>取消</Button>
          </>
        ) : (
          <Button size="sm" disabled={!service.trim() || !country || !(Number(hours) > 0)} onClick={() => setArmed(opId())}>
            下一步
          </Button>
        )}
      </div>
    </section>
  );
}

// ---------- 62：订单与商品详情 ----------

export function SMS62OrdersPanel() {
  const [page, setPage] = useState(1);
  const [goodsId, setGoodsId] = useState("");
  const orders = useQuery({
    queryKey: ["sms-62-orders", page],
    queryFn: ({ signal }) => listSMS62Orders(page, 20, { signal }),
    staleTime: 30_000,
  });
  const detail = useQuery({
    queryKey: ["sms-62-goods", goodsId],
    queryFn: ({ signal }) => getSMS62GoodsDetail(goodsId, { signal }),
    enabled: /^[1-9][0-9]*-[1-9][0-9]*$/.test(goodsId),
    staleTime: 60_000,
    retry: false,
  });

  return (
    <section className="flex min-w-0 flex-col gap-4">
      <div className="border-edge flex min-w-0 flex-col gap-2 rounded-md border p-3">
        <h3 className="text-sm font-semibold">商品详情</h3>
        <FormField label="商品 ID（两段）" htmlFor="goods-detail-id" hint="平台-国家，如 12-1。买号用的三段 ID 是它再加天数。">
          <Input id="goods-detail-id" aria-label="商品 ID（两段）" value={goodsId} onChange={(e) => setGoodsId(e.target.value.trim())} />
        </FormField>
        {detail.data ? (
          <dl className="grid grid-cols-2 gap-2 text-sm md:grid-cols-4">
            <div><dt className="text-fg-muted text-xs">名称</dt><dd>{detail.data.name || "—"}</dd></div>
            <div><dt className="text-fg-muted text-xs">价格</dt><dd>{detail.data.price_text || "—"}</dd></div>
            <div><dt className="text-fg-muted text-xs">库存</dt><dd>{detail.data.stock}</dd></div>
            <div>
              <dt className="text-fg-muted text-xs">可选天数</dt>
              <dd>{detail.data.durations.length ? detail.data.durations.join(" / ") : "上游没给，自己填"}</dd>
            </div>
          </dl>
        ) : detail.isError ? (
          <ActionErrorNote error={detail.error} />
        ) : null}
      </div>

      <div className="border-edge flex min-w-0 flex-col gap-2 rounded-md border p-3">
        <h3 className="text-sm font-semibold">上游订单</h3>
        <p className="text-fg-muted text-xs">
          这是 62 那边的说法，含在 62 后台手工下的单；本地台账只记通过平台下的。人工核对 unknown 时两边都要看。
        </p>
        <ApiStateView isPending={orders.isPending} error={orders.error} onRetry={() => void orders.refetch()} compact>
          <DataTableV2
            caption="62-US 上游订单列表"
            columns={
              [
                { id: "order", header: "订单", primary: true, cell: (r) => r.order_id, value: (r) => r.order_id },
                { id: "goods", header: "商品", cell: (r) => r.goods_id },
                { id: "qty", header: "数量", numeric: true, cell: (r) => String(r.quantity), value: (r) => r.quantity },
                { id: "amount", header: "金额", numeric: true, cell: (r) => r.amount_text || "—" },
                { id: "status", header: "状态", cell: (r) => r.status_text || String(r.status) },
                { id: "time", header: "时间", cell: (r) => (r.created_at ? formatUtcTimestamp(r.created_at) : "—") },
              ] satisfies DataTableColumn<SMS62Order>[]
            }
            rows={orders.data?.items ?? []}
            rowKey={(r) => r.order_id}
            emptyState={<p className="text-fg-muted text-sm">没有订单。</p>}
          />
          <div className="flex items-center gap-2">
            <Button variant="secondary" size="sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>上一页</Button>
            <span className="text-fg-muted text-xs">第 {page} 页 · 共 {orders.data?.total ?? 0} 条</span>
            <Button variant="secondary" size="sm" disabled={(orders.data?.items.length ?? 0) < 20} onClick={() => setPage(page + 1)}>下一页</Button>
          </div>
        </ApiStateView>
      </div>
    </section>
  );
}

// ---------- 号码级：上游验证码列表、延长/重激活、延长历史 ----------

export function UpstreamCodes({ resource }: { resource: SMSResource }) {
  const query = useQuery({
    queryKey: ["sms-upstream-codes", resource.resource_id],
    queryFn: ({ signal }) => listUpstreamCodes(resource.resource_id, { signal }),
    staleTime: 10_000,
  });
  return (
    <div className="flex flex-col gap-2">
      <h3 className="text-sm font-semibold">上游的全部验证码</h3>
      <p className="text-fg-muted text-xs">直接读上游（GET /otp），含来电类验证——那种码本来就是空的。</p>
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()} compact>
        {(query.data ?? []).length === 0 ? (
          <p className="text-fg-muted text-sm">上游还没有码。</p>
        ) : (
          <ul className="flex flex-col gap-1">
            {(query.data ?? []).map((c: SMSCode) => (
              <li key={c.code_id} className="border-edge flex items-center gap-3 rounded-md border p-2">
                <span className="font-mono text-lg font-semibold tabular-nums">{c.code ?? "（需要 sms.reveal 权限）"}</span>
                <span className="text-fg-muted text-xs">{c.sender || "—"} · {c.received_at ? formatUtcTimestamp(c.received_at) : "—"}</span>
              </li>
            ))}
          </ul>
        )}
      </ApiStateView>
    </div>
  );
}

/** 延长 / 重激活。先读档位（价格 + 时长），人选一个再确认——**花钱**。 */
export function ExtendDialog({ resource, kind, onWrite }: { resource: SMSResource; kind: "prolong" | "reactivate"; onWrite: (r: ActionResult) => void }) {
  const [open, setOpen] = useState(false);
  const [picked, setPicked] = useState<HeroExtendOption | null>(null);
  const [error, setError] = useState<unknown>(null);
  const options = useQuery({
    queryKey: ["sms-extend-options", resource.resource_id, kind],
    queryFn: ({ signal }) => listExtendOptions(resource.resource_id, kind, { signal }),
    enabled: open,
    staleTime: 30_000,
  });
  const mutation = useMutation({
    mutationFn: (o: HeroExtendOption) =>
      executeSMSResourceAction({ operation_id: opId(), resource_id: resource.resource_id, kind, duration: o.duration }),
    onSuccess: (run) => {
      onWrite({ runId: run.runId, title: kind === "prolong" ? "已提交延长" : "已提交重激活" });
      setOpen(false);
      setPicked(null);
      setError(null);
    },
    onError: setError,
  });
  const label = kind === "prolong" ? "延长" : "重激活";
  return (
    <Dialog
      open={open}
      onOpenChange={(v) => { setOpen(v); if (!v) setPicked(null); }}
      title={`${label} · ${resource.phone || resource.phone_mask}`}
      description="档位与价格来自上游此刻的报价；执行时的实际价格以上游为准。"
      trigger={<Button variant="secondary" size="sm">{label}</Button>}
    >
      <div className="flex flex-col gap-3">
        <ApiStateView isPending={options.isPending && options.fetchStatus !== "idle"} error={options.error} onRetry={() => void options.refetch()} compact>
          {(options.data ?? []).length === 0 ? (
            <p className="text-fg-muted text-sm">上游没有给出可选档位。</p>
          ) : (
            <ul className="flex flex-col gap-1">
              {(options.data ?? []).map((o) => (
                <li key={`${o.duration}${o.unit}`}>
                  <Button
                    variant={picked === o ? "primary" : "secondary"}
                    size="sm"
                    onClick={() => setPicked(o)}
                    aria-pressed={picked === o}
                  >
                    {o.duration} {o.unit === "hour" ? "小时" : "分钟"} · {o.price_text}
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </ApiStateView>
        {error ? <ActionErrorNote error={error} /> : null}
        <Button variant="danger" size="sm" disabled={!picked || mutation.isPending} onClick={() => picked && mutation.mutate(picked)}>
          {mutation.isPending ? "提交中…" : picked ? `确认${label} ${picked.duration} ${picked.unit === "hour" ? "小时" : "分钟"}（${picked.price_text}）` : `先选一个档位`}
        </Button>
      </div>
    </Dialog>
  );
}

export function ProlongHistory({ resource }: { resource: SMSResource }) {
  const query = useQuery({
    queryKey: ["sms-prolong-history", resource.resource_id],
    queryFn: ({ signal }) => listProlongHistory(resource.resource_id, { signal }),
    staleTime: 30_000,
  });
  const rows = query.data ?? [];
  if (query.isPending || rows.length === 0) return null;
  return (
    <div className="flex flex-col gap-1">
      <h3 className="text-sm font-semibold">延长历史</h3>
      <ul className="text-fg-muted flex flex-col gap-1 text-xs">
        {rows.map((r: HeroProlongRecord, i) => (
          <li key={i}>
            {r.duration} {r.unit === "hour" ? "小时" : "分钟"} · {r.price_text} · {r.created_at ? formatUtcTimestamp(r.created_at) : "—"}
          </li>
        ))}
      </ul>
    </div>
  );
}
