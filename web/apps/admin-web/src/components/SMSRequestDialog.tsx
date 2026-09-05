import { useMutation } from "@tanstack/react-query";
import { Button, Dialog, FormField, Input } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import {
  readSMSRequestResult,
  requestSMSNumbers,
  type SMSProvider,
  type SMSRequestResult,
} from "../api/sms";
import { ActionErrorNote } from "./ActionErrorNote";
import type { ActionResult } from "./ActionResultNote";

const MAX_QUANTITY = 200;

/** 要号（XM-SMS2 #6，ADR-022 决策 3）：服务 + 国家 + 数量，供应商由路由规则选，
 *  人只在想指定时才选。第一家明确失败回落下一家，每家最多试一次；结果未知就停。
 *
 *  **花真钱且不可退。** 两步确认；`request_id` 在上膛那一刻生成，传输失败重试
 *  带同一个（后端回放，永远不会多买）；业务上失败（全部没要到）或未知则清掉——
 *  改完规则再要是一次新的要号，不该沿用旧的幂等键。 */
export function SMSRequestDialog({
  providers,
  onDone,
  onRequested,
}: {
  providers: SMSProvider[];
  onDone: (r: ActionResult) => void;
  /** 要到号之后把号码 ID 交给页面：选中第一个，自动取码由页面驱动。 */
  onRequested: (resourceIds: string[]) => void;
}) {
  const [open, setOpen] = useState(false);
  const [service, setService] = useState("");
  const [country, setCountry] = useState("");
  const [quantity, setQuantity] = useState("1");
  // provider 为空 = 自动（按路由规则）。
  const [provider, setProvider] = useState("");
  const [armed, setArmed] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [outcome, setOutcome] = useState<SMSRequestResult | null>(null);
  const formId = useId();

  const usable = providers.filter(
    (p) => (p.capabilities ?? []).includes("purchase") && p.enabled && p.verified,
  );
  const labelOf = (id: string) => providers.find((p) => p.provider === id)?.label ?? id;
  const qty = Number(quantity) || 0;
  const canArm = service.trim() !== "" && country.trim() !== "" && qty >= 1 && qty <= MAX_QUANTITY;

  const mutation = useMutation({
    mutationFn: (requestId: string) =>
      requestSMSNumbers({
        request_id: requestId,
        service: service.trim(),
        country: country.trim(),
        quantity: qty,
        ...(provider ? { provider } : {}),
      }),
    onSuccess: (run) => {
      const result = readSMSRequestResult(run.result);
      setOutcome(result);
      setError(null);
      if (result?.state === "succeeded") {
        onDone({
          runId: run.runId,
          title: `已要到 ${result.resource_ids.length} 个号（${labelOf(result.provider ?? "")}）`,
        });
        onRequested(result.resource_ids);
        setOpen(false);
        setArmed(null);
        return;
      }
      // 失败 / 未知：留在对话框里把每家的原因摆出来。清掉 armed——再要是新的一次。
      setArmed(null);
      onDone({
        runId: run.runId,
        title: result?.state === "unknown" ? "要号结果未知，需人工核对" : "要号失败：全部供应商都没要到",
      });
    },
    // 传输失败**不清 armed**：那是同一笔业务的重试，幂等键必须保持不变。
    onError: setError,
  });

  function resetField<T>(setter: (v: T) => void) {
    return (v: T) => {
      setter(v);
      setArmed(null);
      setOutcome(null);
    };
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setArmed(null);
          setOutcome(null);
          setError(null);
        }
      }}
      title="要号"
      description="填服务与国家，供应商按路由规则自动选；第一家明确失败回落下一家，每家最多试一次。买到的号不可退。"
      trigger={
        <Button size="sm" disabled={usable.length === 0} className="whitespace-nowrap">
          要号
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        <div className="grid gap-3 md:grid-cols-3">
          <FormField label="服务" htmlFor={`${formId}-svc`} hint="Hero 的服务代号；62 按商品名匹配（纯数字当平台 ID）。">
            <Input
              id={`${formId}-svc`}
              aria-label="要号服务"
              value={service}
              onChange={(e) => resetField(setService)(e.target.value)}
            />
          </FormField>
          <FormField label="国家" htmlFor={`${formId}-country`} hint="要具体，不接受 *。Hero 是数字 ID。">
            <Input
              id={`${formId}-country`}
              aria-label="要号国家"
              value={country}
              onChange={(e) => resetField(setCountry)(e.target.value)}
            />
          </FormField>
          <FormField label="数量" htmlFor={`${formId}-qty`} hint={`1–${MAX_QUANTITY}。`}>
            <Input
              id={`${formId}-qty`}
              aria-label="要号数量"
              inputMode="numeric"
              value={quantity}
              onChange={(e) => resetField(setQuantity)(e.target.value)}
            />
          </FormField>
        </div>

        <div className="flex flex-col gap-1">
          <span className="text-xs font-medium">供应商</span>
          <div className="flex flex-wrap items-center gap-2" role="group" aria-label="供应商选择">
            <Button
              size="sm"
              variant={provider === "" ? "primary" : "secondary"}
              aria-pressed={provider === ""}
              onClick={() => resetField(setProvider)("")}
            >
              自动选供应商
            </Button>
            {usable.map((p) => (
              <Button
                key={p.provider}
                size="sm"
                variant={provider === p.provider ? "primary" : "secondary"}
                aria-pressed={provider === p.provider}
                onClick={() => resetField(setProvider)(p.provider)}
              >
                {labelOf(p.provider)}
              </Button>
            ))}
          </div>
          <p className="text-fg-muted text-xs">
            {provider ? `只试 ${labelOf(provider)}，不回落。` : "按「路由规则」页签里的规则选；没有规则时按默认顺序逐家试。"}
          </p>
        </div>

        {outcome && outcome.state !== "succeeded" ? <RequestOutcomeNote outcome={outcome} labelOf={labelOf} /> : null}
        {error ? <ActionErrorNote error={error} /> : null}

        <div className="flex items-center gap-2">
          {armed ? (
            <>
              <Button variant="danger" size="sm" disabled={mutation.isPending} onClick={() => mutation.mutate(armed)}>
                {mutation.isPending ? "要号中…" : `确认要 ${qty} 个`}
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setArmed(null)}>
                取消
              </Button>
            </>
          ) : (
            <Button size="sm" disabled={!canArm} onClick={() => setArmed(crypto.randomUUID())}>
              要号
            </Button>
          )}
        </div>
      </div>
    </Dialog>
  );
}

/** 失败 / 未知的说明：每家为什么没要到，人下一步该做什么。 */
function RequestOutcomeNote({
  outcome,
  labelOf,
}: {
  outcome: SMSRequestResult;
  labelOf: (id: string) => string;
}) {
  const unknown = outcome.state === "unknown";
  return (
    <div
      role="status"
      className={`flex flex-col gap-1 rounded-md border p-2 text-sm ${unknown ? "border-danger" : "border-edge"}`}
    >
      {unknown ? (
        <p>
          {`要号结果未知：钱可能已经花了，需人工核对。到「操作台账」按操作 ID ${outcome.operation_id ?? "—"} 核对后再决定要不要重来。`}
        </p>
      ) : (
        <p>全部供应商都没要到。改规则、充值或换国家之后再要一次（那是新的一次要号）。</p>
      )}
      {outcome.attempts.length ? (
        <ul className="text-fg-muted flex flex-col gap-0.5 text-xs">
          {outcome.attempts.map((a) => (
            <li key={`${a.provider}-${a.operation_id}`}>
              {`${labelOf(a.provider)}：${a.reason || a.state || "—"}${a.replayed ? "（回放）" : ""}`}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
