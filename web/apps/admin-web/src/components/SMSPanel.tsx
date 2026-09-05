import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import {
  executeSMSResourceAction,
  listSMSCatalog,
  listSMSCodes,
  listSMSOperations,
  listSMSProviders,
  listSMSResources,
  purchaseSMSNumbers,
  resolveSMSOperation,
  setSMSProviderEnabled,
  verifySMSProvider,
  type SMSOperation,
  type SMSProvider,
  type SMSResource,
} from "../api/sms";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

const PROVIDERS_QUERY = "sms-providers";
const RESOURCES_QUERY = "sms-resources";
const OPERATIONS_QUERY = "sms-operations";
const CATALOG_QUERY = "sms-catalog";

/** 供应商的中文名。未知取值原样显示。 */
function providerLabel(id: string): string {
  return { sms62: "62-US", hero_sms: "Hero-SMS" }[id] ?? id;
}

/** 七态的中文与色调。
 *
 *  **unknown 必须显眼**：它的含义是「不知道钱花没花出去」，而那正是唯一
 *  需要人立刻去做点什么的状态。其余六个都已经有定论。 */
function stateLabel(state: string): string {
  return (
    {
      prepared: "已建账",
      submitted: "已提交",
      succeeded: "成功",
      failed: "失败",
      unknown: "结果未知",
      reconciled_succeeded: "人工判定成功",
      reconciled_failed: "人工判定失败",
    }[state] ?? `${state}（未知状态）`
  );
}

function stateTone(state: string): "success" | "warning" | "danger" | "neutral" {
  switch (state) {
    case "succeeded":
    case "reconciled_succeeded":
      return "success";
    case "unknown":
      return "danger";
    case "failed":
    case "reconciled_failed":
      return "warning";
    default:
      return "neutral";
  }
}

/** 接码中心（XM-SMS0）。
 *
 *  形态跟卡片页一致（左清单 + 右详情），两页并排时不用重新适应。 */
export function SMSPanel() {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);
  const [selectedId, setSelectedId] = useState("");

  const providersQuery = useQuery({
    queryKey: [PROVIDERS_QUERY],
    queryFn: ({ signal }) => listSMSProviders({ signal }),
    staleTime: 30_000,
  });
  const resourcesQuery = useQuery({
    queryKey: [RESOURCES_QUERY],
    queryFn: ({ signal }) => listSMSResources("", { signal }),
    staleTime: 15_000,
  });

  const providers = providersQuery.data ?? [];
  const resources = resourcesQuery.data ?? [];
  const selected = resources.find((r) => r.resource_id === selectedId) ?? resources[0] ?? null;

  function afterWrite(r: ActionResult) {
    setResult(r);
    void queryClient.invalidateQueries({ queryKey: [PROVIDERS_QUERY] });
    void queryClient.invalidateQueries({ queryKey: [RESOURCES_QUERY] });
    void queryClient.invalidateQueries({ queryKey: [OPERATIONS_QUERY] });
  }

  return (
    <div className="flex min-w-0 flex-col gap-4">
      {result ? <ActionResultNote result={result} /> : null}

      <ApiStateView
        isPending={providersQuery.isPending}
        error={providersQuery.error}
        onRetry={() => void providersQuery.refetch()}
      >
        <ProviderStrip providers={providers} onChanged={afterWrite} />
      </ApiStateView>

      <PurchaseSection providers={providers} onDone={afterWrite} />

      <div className="grid min-w-0 gap-3 lg:grid-cols-[minmax(18rem,24rem)_minmax(0,1fr)]">
        <ApiStateView
          isPending={resourcesQuery.isPending}
          error={resourcesQuery.error}
          onRetry={() => void resourcesQuery.refetch()}
        >
          <ResourceRail resources={resources} selected={selected} onSelect={(r) => setSelectedId(r.resource_id)} />
        </ApiStateView>
        {selected ? (
          <ResourcePane resource={selected} providers={providers} onWrite={afterWrite} />
        ) : null}
      </div>

      <OperationsSection onResolved={afterWrite} />
    </div>
  );
}

/** 供应商状态条，同时是连接测试的入口。
 *
 *  **没验证过的那家也要显示出来**——页面要说「这家还没验证」，而不是当它
 *  不存在。买号前后端会检查这个事实，所以它必须在人点买号之前就看得见。 */
function ProviderStrip({
  providers,
  onChanged,
}: {
  providers: SMSProvider[];
  onChanged: (r: ActionResult) => void;
}) {
  return (
    <div className="flex flex-wrap gap-3">
      {providers.map((p) => (
        <ProviderCard key={p.provider} provider={p} onChanged={onChanged} />
      ))}
    </div>
  );
}

function ProviderCard({
  provider,
  onChanged,
}: {
  provider: SMSProvider;
  onChanged: (r: ActionResult) => void;
}) {
  const [error, setError] = useState<unknown>(null);
  const mutation = useMutation({
    mutationFn: () => verifySMSProvider(provider.provider),
    onSuccess: (run) => {
      onChanged({ runId: run.runId, title: `已测试 ${providerLabel(provider.provider)} 的连接` });
      setError(null);
    },
    onError: setError,
  });
  // 传 enabled 的**目标值**而不是「切换」：传切换时两个人同时点会变成
  // 一次开一次关，传目标值时同向的两次点击是幂等的。
  const toggle = useMutation({
    mutationFn: () => setSMSProviderEnabled(provider.provider, !provider.enabled),
    onSuccess: (run) => {
      onChanged({
        runId: run.runId,
        title: `${provider.enabled ? "已停用" : "已启用"} ${providerLabel(provider.provider)}`,
      });
      setError(null);
    },
    onError: setError,
  });

  return (
    <div className="border-edge flex min-w-64 flex-col gap-1 rounded-md border p-3">
      <span className="flex items-center gap-2">
        <span className="text-sm font-medium">{providerLabel(provider.provider)}</span>
        {/* 两个徽章而不是一个「可用」：关着与没验证的下一步完全不同，
            前者去这张卡上点启用，后者去做连接测试。合成一个就分不出来了。 */}
        <Badge tone={provider.enabled ? "success" : "neutral"}>
          {provider.enabled ? "已启用" : "已停用"}
        </Badge>
        <Badge tone={provider.verified ? "success" : "warning"}>
          {provider.verified ? "已验证" : "未验证"}
        </Badge>
      </span>
      {provider.verified_at ? (
        <span className="text-fg-muted text-xs">{formatUtcTimestamp(provider.verified_at)}</span>
      ) : (
        <span className="text-fg-muted text-xs">买号前必须先做一次连接测试</span>
      )}
      {/* 出口 IP 的用处不是展示是排查：上游若做 IP 白名单，它对不上就是
          后续全部 403 的原因，而那种失败从错误码上看只是「没权限」。 */}
      {provider.client_ip ? (
        <span className="text-fg-muted font-mono text-xs">出口 IP {provider.client_ip}</span>
      ) : null}
      {provider.last_error ? (
        <span className="text-danger text-xs">上次错误：{provider.last_error}</span>
      ) : null}
      <span className="flex items-center gap-2 pt-1">
        <Button
          variant="secondary"
          size="sm"
          disabled={mutation.isPending}
          onClick={() => mutation.mutate()}
        >
          {mutation.isPending ? "测试中…" : "连接测试"}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={toggle.isPending}
          onClick={() => toggle.mutate()}
        >
          {toggle.isPending ? "提交中…" : provider.enabled ? "停用" : "启用"}
        </Button>
        {provider.supports_lifecycle ? (
          <span className="text-fg-muted text-xs">支持取消/延长</span>
        ) : (
          <span className="text-fg-muted text-xs">仅支持买号</span>
        )}
      </span>
      {error ? <ActionErrorNote error={error} /> : null}
    </div>
  );
}

/** 买号。库存 + 两步确认。 */
function PurchaseSection({
  providers,
  onDone,
}: {
  providers: SMSProvider[];
  onDone: (r: ActionResult) => void;
}) {
  const [provider, setProvider] = useState("");
  const effective = providers.some((p) => p.provider === provider)
    ? provider
    : (providers[0]?.provider ?? "");
  const current = providers.find((p) => p.provider === effective);
  // 两个条件都要：开着（运营的意愿）且验证过（凭据确实能用）。
  // 后端也会各自拦一道——只靠前端不渲染是不够的，一个还留着旧页面的
  // 标签页仍然能提交，而那一次提交花的是真钱。
  const usable = Boolean(current?.enabled && current?.verified);

  const catalogQuery = useQuery({
    queryKey: [CATALOG_QUERY, effective],
    queryFn: ({ signal }) => listSMSCatalog(effective, {}, { signal }),
    // 库存是实时上游调用，只有真要挑商品时才拉。
    enabled: effective !== "" && usable,
    staleTime: 60_000,
  });

  return (
    <section className="border-edge flex min-w-0 flex-col gap-3 rounded-md border p-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">买号</h3>
          <p className="text-fg-muted text-xs">
            买号花真钱且<strong>不可退</strong>。需要 sms-operator
            角色；没有这个角色时下面会显示无权限。
          </p>
        </div>
        {/* shrink-0：不加的话「买号」这个两字按钮会被 flex 压成上下两行
            （「买」/「号」）——一个被挤断的按钮读起来像渲染坏了，
            而它恰好是这一页唯一会花钱的入口。 */}
        <span className="flex shrink-0 items-center gap-2">
          <Select
            aria-label="供应商"
            options={providers.map((p) => ({ value: p.provider, label: providerLabel(p.provider) }))}
            value={effective}
            onValueChange={setProvider}
          />
          <PurchaseDialog provider={effective} usable={usable} onDone={onDone} />
        </span>
      </div>

      {current && !current.enabled ? (
        <p className="text-fg-muted text-sm">
          {providerLabel(effective)} 在后台被停用了，暂时不能买号也读不到库存。
          到上面那张卡上点「启用」即可。
        </p>
      ) : current && !current.verified ? (
        <p className="text-fg-muted text-sm">
          {providerLabel(effective)} 还没做过连接测试，暂时不能买号也读不到库存。
        </p>
      ) : (
        <ApiStateView
          isPending={catalogQuery.isPending && catalogQuery.fetchStatus !== "idle"}
          error={catalogQuery.error}
          onRetry={() => void catalogQuery.refetch()}
          compact
        >
          <CatalogTable items={catalogQuery.data ?? []} />
        </ApiStateView>
      )}
    </section>
  );
}

function CatalogTable({ items }: { items: Awaited<ReturnType<typeof listSMSCatalog>> }) {
  const columns: DataTableColumn<(typeof items)[number]>[] = [
    { id: "id", header: "商品 / 服务", cell: (row) => row.id, primary: true },
    { id: "name", header: "名称", cell: (row) => row.name || row.service || "—" },
    { id: "country", header: "国家", cell: (row) => row.country || "—" },
    // 三个价格分开显示：它们是不同的事实，合并成一个「价格」会让人按一个
    // 不成立的数字做预算。缺失显示「—」，不补零也不补币种。
    { id: "default", header: "默认价", cell: (row) => row.default_price || "—" },
    { id: "retail", header: "零售价", cell: (row) => row.retail_price || "—" },
    { id: "min", header: "最低价", cell: (row) => row.minimum_price || "—" },
    { id: "available", header: "库存", cell: (row) => String(row.available ?? "—") },
  ];
  return (
    <div className="min-w-0 overflow-x-auto">
      <DataTableV2
        caption="可购买的号码库存"
        searchable
        rows={items}
        columns={columns}
        rowKey={(row) => row.id}
        emptyState={<p className="text-fg-muted text-sm">这家暂时没有可买的库存。</p>}
      />
    </div>
  );
}

/** 买号对话框。两步确认，幂等键在上膛时生成。 */
function PurchaseDialog({
  provider,
  usable,
  onDone,
}: {
  provider: string;
  /** 开着**且**验证过。两者缺一都不能买号。 */
  usable: boolean;
  onDone: (r: ActionResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const [quantity, setQuantity] = useState("1");
  const [goodsID, setGoodsID] = useState("");
  const [service, setService] = useState("");
  const [country, setCountry] = useState("");
  const [maxPrice, setMaxPrice] = useState("");
  const [armed, setArmed] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  const isSMS62 = provider === "sms62";

  const mutation = useMutation({
    mutationFn: (operationID: string) =>
      purchaseSMSNumbers({
        provider,
        operation_id: operationID,
        quantity: Number(quantity) || 1,
        ...(isSMS62 ? { goods_id: goodsID.trim() } : {}),
        ...(isSMS62 ? {} : { service: service.trim(), country: Number(country) || 0 }),
        ...(maxPrice.trim() ? { max_price: maxPrice.trim() } : {}),
      }),
    onSuccess: (run) => {
      onDone({ runId: run.runId, title: "已提交买号请求" });
      setOpen(false);
      setArmed(null);
      setError(null);
    },
    // 失败时**不清 armed**：那是同一笔业务的重试，幂等键必须保持不变。
    onError: setError,
  });

  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      title={`买号 · ${providerLabel(provider)}`}
      description="买到的号不可退。数量上限 200——那是我们自己的安全上限，不是供应商声明的最大值。"
      trigger={
        <Button size="sm" disabled={!usable}>
          买号
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        <FormField label="数量" htmlFor={`${formId}-qty`} hint="1–200。一次手滑就是两百个号。">
          <Input
            id={`${formId}-qty`}
            aria-label="数量"
            value={quantity}
            onChange={(e) => {
              setQuantity(e.target.value);
              setArmed(null);
            }}
          />
        </FormField>

        {isSMS62 ? (
          <FormField
            label="商品 ID"
            htmlFor={`${formId}-goods`}
            hint="三段正整数，如 1-2-3。商品详情接口只接受两段，不能自己把两段扩成三段——那样拼出来的 ID 指向别的商品。"
          >
            <Input
              id={`${formId}-goods`}
              aria-label="商品 ID"
              value={goodsID}
              onChange={(e) => {
                setGoodsID(e.target.value);
                setArmed(null);
              }}
            />
          </FormField>
        ) : (
          <>
            <FormField label="服务代号" htmlFor={`${formId}-svc`} hint="2–4 位小写字母或数字，如 op / tg。">
              <Input
                id={`${formId}-svc`}
                aria-label="服务代号"
                value={service}
                onChange={(e) => {
                  setService(e.target.value);
                  setArmed(null);
                }}
              />
            </FormField>
            <FormField label="国家代码" htmlFor={`${formId}-country`} hint="0–999。">
              <Input
                id={`${formId}-country`}
                aria-label="国家代码"
                value={country}
                onChange={(e) => {
                  setCountry(e.target.value);
                  setArmed(null);
                }}
              />
            </FormField>
            <FormField label="最高单价" htmlFor={`${formId}-max`} hint="可选，十进制文本。留空即不限价。">
              <Input
                id={`${formId}-max`}
                aria-label="最高单价"
                value={maxPrice}
                onChange={(e) => setMaxPrice(e.target.value)}
              />
            </FormField>
          </>
        )}

        {error ? <ActionErrorNote error={error} /> : null}

        <div className="flex items-center gap-2">
          {armed ? (
            <>
              <Button
                variant="danger"
                size="sm"
                disabled={mutation.isPending}
                onClick={() => mutation.mutate(armed)}
              >
                {mutation.isPending ? "提交中…" : `确认买 ${quantity} 个号`}
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setArmed(null)}>
                取消
              </Button>
            </>
          ) : (
            // 两步确认：第一次点只是上膛，幂等键在这一刻生成并保持不变。
            // 上游对同一个键有防重，两次点击若换了键就等于告诉双方
            // 「这是另一笔购买」——那正是重复买号的路径。
            <Button size="sm" onClick={() => setArmed(crypto.randomUUID())}>
              买号
            </Button>
          )}
        </div>
      </div>
    </Dialog>
  );
}

/** 左侧号码清单。 */
function ResourceRail({
  resources,
  selected,
  onSelect,
}: {
  resources: SMSResource[];
  selected: SMSResource | null;
  onSelect: (r: SMSResource) => void;
}) {
  if (resources.length === 0) {
    return <p className="text-fg-muted text-sm">还没有号码。先在上面买一批。</p>;
  }
  return (
    <ul className="border-edge flex min-w-0 flex-col rounded-md border">
      {resources.map((r) => {
        const active = selected?.resource_id === r.resource_id;
        return (
          <li key={r.resource_id} className="border-edge border-b last:border-b-0">
            <button
              type="button"
              onClick={() => onSelect(r)}
              aria-current={active ? "true" : undefined}
              className={`hover:bg-surface-muted focus-visible:outline-accent flex w-full min-w-0 items-center gap-3 px-3 py-2 text-left focus-visible:outline-2 ${
                active ? "bg-accent-soft" : ""
              }`}
            >
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="truncate font-mono text-sm font-semibold">
                  {r.phone || r.phone_mask}
                </span>
                <span className="text-fg-muted truncate text-xs">
                  {providerLabel(r.provider)} · {r.service || "—"} · {r.country || "—"}
                </span>
              </span>
              <Badge tone="neutral">{r.status || "—"}</Badge>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

/** 右侧：号码详情与验证码。 */
function ResourcePane({
  resource,
  providers,
  onWrite,
}: {
  resource: SMSResource;
  providers: SMSProvider[];
  onWrite: (r: ActionResult) => void;
}) {
  const codesQuery = useQuery({
    queryKey: ["sms-codes", resource.resource_id],
    queryFn: ({ signal }) => listSMSCodes(resource.resource_id, { signal }),
    // 验证码只有几分钟有效，刷新要勤。
    refetchInterval: 10_000,
  });
  const supportsLifecycle =
    providers.find((p) => p.provider === resource.provider)?.supports_lifecycle ?? false;

  return (
    <section className="border-edge flex min-w-0 flex-col gap-4 rounded-md border p-4">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex flex-col">
          <span className="font-mono text-lg font-semibold">
            {resource.phone || resource.phone_mask}
          </span>
          <span className="text-fg-muted text-xs">
            {providerLabel(resource.provider)} · {resource.status || "—"}
          </span>
        </span>
        {supportsLifecycle ? (
          <LifecycleButtons resource={resource} onWrite={onWrite} />
        ) : (
          // 62 一个生命周期动作都没有。**明说而不是把按钮灰掉**：
          // 一个灰按钮看起来像「暂时不能用」，而这是永远不能用。
          <span className="text-fg-muted text-xs">这家不支持取消/延长</span>
        )}
      </header>

      <div className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold">验证码</h3>
        <p className="text-fg-muted text-xs">
          码到达后会自动落库并推企业微信（配了 secret://sms/notify-webhook 才推）。
          这里每 10 秒刷新一次。
        </p>
        <ApiStateView
          isPending={codesQuery.isPending}
          error={codesQuery.error}
          onRetry={() => void codesQuery.refetch()}
          compact
        >
          {(codesQuery.data ?? []).length === 0 ? (
            <p className="text-fg-muted text-sm">还没收到码。注册后码通常几秒到几十秒到。</p>
          ) : (
            <ul className="flex flex-col gap-1">
              {(codesQuery.data ?? []).map((c) => (
                <li key={c.code_id} className="border-edge flex items-center gap-3 rounded-md border p-2">
                  <span className="font-mono text-lg font-semibold tabular-nums">
                    {c.code ?? "（需要 sms.reveal 权限）"}
                  </span>
                  <span className="text-fg-muted text-xs">
                    {c.sender || "—"} · {c.received_at ? formatUtcTimestamp(c.received_at) : "—"}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </ApiStateView>
      </div>

      <dl className="grid grid-cols-2 gap-3">
        <Field label="服务" value={resource.service || "—"} />
        <Field label="国家" value={resource.country || "—"} />
        <Field
          label="最后取到码"
          value={resource.last_code_at ? formatUtcTimestamp(resource.last_code_at) : "—"}
        />
        <Field
          label="过期时间"
          value={resource.expires_at ? formatUtcTimestamp(resource.expires_at) : "—"}
        />
      </dl>
    </section>
  );
}

function LifecycleButtons({
  resource,
  onWrite,
}: {
  resource: SMSResource;
  onWrite: (r: ActionResult) => void;
}) {
  const [error, setError] = useState<unknown>(null);
  const mutation = useMutation({
    mutationFn: (kind: string) =>
      executeSMSResourceAction({
        operation_id: crypto.randomUUID(),
        resource_id: resource.resource_id,
        kind,
      }),
    onSuccess: (run) => {
      onWrite({ runId: run.runId, title: "已提交号码动作" });
      setError(null);
    },
    onError: setError,
  });

  return (
    <span className="flex flex-wrap items-center gap-2">
      {[
        { kind: "cancel", label: "取消" },
        { kind: "finish", label: "完成" },
        { kind: "replace", label: "换号" },
      ].map((a) => (
        <Button
          key={a.kind}
          variant="secondary"
          size="sm"
          disabled={mutation.isPending}
          onClick={() => mutation.mutate(a.kind)}
        >
          {a.label}
        </Button>
      ))}
      {error ? <ActionErrorNote error={error} /> : null}
    </span>
  );
}

/** 操作台账 + unknown 的人工核对。 */
function OperationsSection({ onResolved }: { onResolved: (r: ActionResult) => void }) {
  const query = useQuery({
    queryKey: [OPERATIONS_QUERY],
    queryFn: ({ signal }) => listSMSOperations("", { signal }),
    refetchInterval: 30_000,
  });
  const rows = query.data ?? [];
  const needsReview = rows.filter((r) => r.needs_review);

  const columns: DataTableColumn<SMSOperation>[] = [
    {
      id: "started",
      header: "时间",
      cell: (row) => (row.started_at ? formatUtcTimestamp(row.started_at) : "—"),
      value: (row) => row.started_at ?? "",
      primary: true,
    },
    { id: "provider", header: "供应商", cell: (row) => providerLabel(row.provider) },
    { id: "kind", header: "动作", cell: (row) => row.kind },
    {
      id: "state",
      header: "状态",
      cell: (row) => <Badge tone={stateTone(row.state)}>{stateLabel(row.state)}</Badge>,
      value: (row) => stateLabel(row.state),
    },
    { id: "params", header: "参数", cell: (row) => row.params_summary || "—" },
    {
      id: "ref",
      header: "上游引用",
      // unknown 时它是人去供应商侧对账的唯一抓手，所以哪怕操作失败也显示。
      cell: (row) => <span className="font-mono text-xs">{row.provider_ref || "—"}</span>,
    },
    {
      id: "resolve",
      header: "处置",
      cell: (row) =>
        row.needs_review ? <ResolveDialog operation={row} onDone={onResolved} /> : null,
    },
  ];

  return (
    <section className="flex min-w-0 flex-col gap-2">
      <h3 className="text-sm font-semibold">操作台账</h3>
      {needsReview.length > 0 ? (
        <p className="border-danger text-danger rounded-md border p-2 text-sm">
          有 {needsReview.length} 笔操作结果未知，需要人工到供应商侧核对。
          **这些不会自动重试**——重试可能再买一次号。
        </p>
      ) : null}
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <div className="min-w-0 overflow-x-auto">
          <DataTableV2
            caption="接码操作台账：时间、供应商、动作、状态与上游引用"
            searchable
            rows={rows}
            columns={columns}
            rowKey={(row) => row.operation_id}
            emptyState={<p className="text-fg-muted text-sm">还没有操作记录。</p>}
          />
        </div>
      </ApiStateView>
    </section>
  );
}

/** 人工核对一笔 unknown。 */
function ResolveDialog({
  operation,
  onDone,
}: {
  operation: SMSOperation;
  onDone: (r: ActionResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const [outcome, setOutcome] = useState<"succeeded" | "failed">("failed");
  const [resourceID, setResourceID] = useState(operation.resource_id ?? "");
  const [note, setNote] = useState("");
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  const mutation = useMutation({
    mutationFn: () =>
      resolveSMSOperation({
        operation_id: operation.operation_id,
        outcome,
        ...(resourceID.trim() ? { resource_id: resourceID.trim() } : {}),
        note: note.trim(),
      }),
    onSuccess: (run) => {
      onDone({ runId: run.runId, title: "已记录核对结论" });
      setOpen(false);
      setError(null);
    },
    onError: setError,
  });

  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      title="人工核对"
      description="只改本地账本，不会向上游重发。请先到供应商后台确认这笔到底成没成。"
      trigger={
        <Button variant="secondary" size="sm">
          核对
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        <p className="text-fg-muted text-xs">
          上游引用：<span className="font-mono">{operation.provider_ref || "（无）"}</span>
          —— 拿它去供应商后台查。
        </p>
        <FormField label="结论" htmlFor={`${formId}-outcome`}>
          <Select
            aria-label="结论"
            options={[
              { value: "failed", label: "确认没成功（钱没花）" },
              { value: "succeeded", label: "确认成功了（钱花了）" },
            ]}
            value={outcome}
            onValueChange={(v) => setOutcome(v as "succeeded" | "failed")}
          />
        </FormField>
        {outcome === "succeeded" ? (
          <FormField
            label="关联号码 ID"
            htmlFor={`${formId}-res`}
            hint="判定购买成功必须关联一个本地号码——一笔「成功了但没有号」的记录，事后没有任何办法核实。"
          >
            <Input
              id={`${formId}-res`}
              value={resourceID}
              onChange={(e) => setResourceID(e.target.value)}
            />
          </FormField>
        ) : null}
        <FormField
          label="依据"
          htmlFor={`${formId}-note`}
          hint="必填。三个月后回看这条记录时，「谁凭什么这么判」只有这句话回答得了。不要写密钥、完整号码或验证码。"
        >
          <Input id={`${formId}-note`} value={note} onChange={(e) => setNote(e.target.value)} required />
        </FormField>

        {error ? <ActionErrorNote error={error} /> : null}

        <Button
          disabled={note.trim() === "" || mutation.isPending}
          onClick={() => mutation.mutate()}
        >
          {mutation.isPending ? "提交中…" : "记录结论"}
        </Button>
      </div>
    </Dialog>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-fg-muted text-xs">{label}</dt>
      <dd className="text-sm break-all">{value}</dd>
    </div>
  );
}
