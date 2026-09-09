import { useMutation } from "@tanstack/react-query";
import { Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import {
  registerSubscriptionBatch,
  SUBSCRIPTION_MANAGE_PERMISSION,
  type ProxyAssetPage,
  type UpstreamAccountItem,
} from "../api/finance";
import {
  SUPPORTED_FINANCE_CURRENCIES,
  MAX_AMOUNT_INPUT_LENGTH,
  MAX_COUNT_INPUT_LENGTH,
  buildSubscriptionBatchParams,
  effectiveDaysInclusive,
  validateSubscriptionBatchForm,
  type SubscriptionBatchFormErrors,
  type SubscriptionBatchFormValues,
} from "../lib/subscriptionForms";
import { ActionErrorNote } from "./ActionErrorNote";

export interface SubscriptionBatchDialogProps {
  account: UpstreamAccountItem;
  proxyPage?: ProxyAssetPage;
  proxyPageStatus?: "pending" | "error" | "success";
  disabled?: boolean;
  onDone: (runId: string) => void;
}

const NO_PROXY = "__none__";

export function SubscriptionBatchDialog({
  account,
  proxyPage,
  proxyPageStatus,
  disabled = false,
  onDone,
}: SubscriptionBatchDialogProps) {
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<SubscriptionBatchFormValues>(() => initialValues(account));
  const [errors, setErrors] = useState<SubscriptionBatchFormErrors>({});
  const [attempts, setAttempts] = useState(0);
  const prefix = useId();
  const summaryRef = useRef<HTMLDivElement>(null);
  const proxyGateMessage = describeProxyGate(proxyPageStatus, proxyPage);
  const proxiesReady = proxyGateMessage === null;

  const mutation = useMutation({
    mutationFn: (form: SubscriptionBatchFormValues) =>
      registerSubscriptionBatch(buildSubscriptionBatchParams(account.id, form)),
    onSuccess: (run) => {
      setOpen(false);
      setErrors({});
      setAttempts(0);
      setValues(initialValues(account));
      onDone(run.runId);
    },
  });

  const failedFields = Object.keys(errors).filter(
    (field) => errors[field as keyof SubscriptionBatchFormErrors],
  );
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(mutation.error));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [showSummary, attempts, mutation.error]);

  const set = <K extends keyof SubscriptionBatchFormValues>(
    field: K,
    value: SubscriptionBatchFormValues[K],
  ) => {
    setValues((previous) => ({ ...previous, [field]: value }));
    setErrors((previous) => {
      if (!(field in previous)) return previous;
      const next = { ...previous };
      delete next[field];
      return next;
    });
  };

  const blur = (field: keyof SubscriptionBatchFormValues) => {
    const problem = validateSubscriptionBatchForm(values)[field];
    setErrors((previous) => ({ ...previous, [field]: problem }));
  };

  const submit = () => {
    setAttempts((count) => count + 1);
    // 对话框打开后 Query 也可能从 success 变为 error；提交点再守一次，
    // 不让不可变批次在未知代理集合下落库。
    if (!proxiesReady) return;
    const found = validateSubscriptionBatchForm(values);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    mutation.mutate(values);
  };

  const days = effectiveDaysInclusive(values.startsOn, values.expiresOn);
  const proxyOptions = [
    { value: NO_PROXY, label: "不使用代理（代理成本为已知 0）" },
    ...(proxyPage?.items ?? []).map((proxy) => ({
      value: proxy.id,
      label: `${proxy.buy_platform || "未标购买平台"} · ${proxy.id} · 到期 ${proxy.expires_on}`,
    })),
  ];

  return (
    <div className="flex flex-col items-end gap-1">
      <Dialog
      open={open}
      onOpenChange={(next) => {
        if (next && !proxiesReady) return;
        setOpen(next);
        if (next) setValues(initialValues(account));
        else {
          setErrors({});
          setAttempts(0);
          mutation.reset();
        }
      }}
      trigger={
        <Button
          size="sm"
          disabled={disabled || !proxiesReady}
          title={
            disabled
              ? `需要 ${SUBSCRIPTION_MANAGE_PERMISSION}`
              : proxyGateMessage ?? undefined
          }
        >
          登记/续费新增批次
        </Button>
      }
      title="登记/续费新增批次"
      description="通过 finance.subscription_batch.register@1 新增不可变批次；续费新增一笔，不覆盖历史。"
    >
      <form
        className="flex max-h-[65vh] flex-col gap-3 overflow-y-auto"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <p className="rounded-md border border-edge bg-surface-muted px-2 py-1.5 text-xs text-fg-muted">
          批次登记与代理资产登记是两个独立 Action，不承诺原子性。代理创建成功后，即使本批次失败，
          仍可保留代理回执并在这里重试选择它。
        </p>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <FormField
            label="实际支付"
            htmlFor={`${prefix}-paid`}
            required
            error={errors.paidMajor}
            hint="人类主单位输入；提交为 scale-6 整数字符串"
          >
            <Input
              value={values.paidMajor}
              maxLength={MAX_AMOUNT_INPUT_LENGTH}
              inputMode="decimal"
              invalid={Boolean(errors.paidMajor)}
              onChange={(event) => set("paidMajor", event.target.value)}
              onBlur={() => blur("paidMajor")}
            />
          </FormField>
          <FormField
            label="附加费用"
            htmlFor={`${prefix}-surcharge`}
            error={errors.surchargeMajor}
            hint="可留空；空白明确写为 0"
          >
            <Input
              value={values.surchargeMajor}
              maxLength={MAX_AMOUNT_INPUT_LENGTH}
              inputMode="decimal"
              invalid={Boolean(errors.surchargeMajor)}
              onChange={(event) => set("surchargeMajor", event.target.value)}
              onBlur={() => blur("surchargeMajor")}
            />
          </FormField>
          <FormField label="币种" required error={errors.currency} hint="默认取当前账号币种，不猜未知币种">
            <Select
              aria-label="币种"
              value={values.currency}
              options={SUPPORTED_FINANCE_CURRENCIES.map((currency) => ({ value: currency, label: currency }))}
              invalid={Boolean(errors.currency)}
              onValueChange={(value) => set("currency", value)}
            />
          </FormField>
          <FormField label="账号数量" htmlFor={`${prefix}-accounts`} required error={errors.accountCount}>
            <Input
              value={values.accountCount}
              maxLength={MAX_COUNT_INPUT_LENGTH}
              inputMode="numeric"
              invalid={Boolean(errors.accountCount)}
              onChange={(event) => set("accountCount", event.target.value)}
              onBlur={() => blur("accountCount")}
            />
          </FormField>
          <FormField label="开始日期" htmlFor={`${prefix}-starts`} required error={errors.startsOn}>
            <Input
              type="date"
              value={values.startsOn}
              invalid={Boolean(errors.startsOn)}
              onChange={(event) => set("startsOn", event.target.value)}
              onBlur={() => blur("startsOn")}
            />
          </FormField>
          <FormField label="到期日期" htmlFor={`${prefix}-expires`} required error={errors.expiresOn}>
            <Input
              type="date"
              value={values.expiresOn}
              invalid={Boolean(errors.expiresOn)}
              onChange={(event) => set("expiresOn", event.target.value)}
              onBlur={() => blur("expiresOn")}
            />
          </FormField>
        </div>

        <p className="text-xs text-fg-muted" aria-live="polite">
          {days === null
            ? "有效天数：待填写有效起止日期"
            : `有效天数：${days} 天（起止日均计入；仅用于解释，服务端仍是权威）`}
        </p>

        <FormField label="关联代理（可选）" hint="只列出当前 read page 返回的真实代理资产">
          <Select
            aria-label="关联代理（可选）"
            value={values.proxyAssetId || NO_PROXY}
            options={proxyOptions}
            onValueChange={(value) => set("proxyAssetId", value === NO_PROXY ? "" : value)}
          />
        </FormField>
        {proxyPage?.truncated ? (
          <p role="status" className="text-xs text-warning">
            代理选择结果不完整：read page 已截断（limit {proxyPage.limit}），不能据此声称已列出全部代理。
          </p>
        ) : null}

        <div
          ref={summaryRef}
          tabIndex={-1}
          className="flex flex-col gap-1 outline-none focus-visible:ring-2 focus-visible:ring-accent"
        >
          {showSummary && failedFields.length > 0 ? (
            <p role="alert" className="text-xs text-danger">
              还有 {failedFields.length} 处需要修正，请查看对应字段。
            </p>
          ) : null}
          <ActionErrorNote error={mutation.error} permission={SUBSCRIPTION_MANAGE_PERMISSION} />
        </div>

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending} disabled={!proxiesReady}>
            登记批次
          </Button>
        </div>
      </form>
      </Dialog>
      {proxyGateMessage ? (
        <p role="status" className="max-w-xs text-right text-xs text-warning">
          {proxyGateMessage}
        </p>
      ) : null}
    </div>
  );
}

function describeProxyGate(
  status: SubscriptionBatchDialogProps["proxyPageStatus"],
  page: ProxyAssetPage | undefined,
): string | null {
  if (status === "pending") return "正在读取代理资产；批次登记暂时锁定。";
  if (status === "error") return "代理资产读取失败；批次登记已锁定，请先重试只读查询。";
  if (status !== "success" || !page) return "代理资产列表状态未知；批次登记已锁定。";
  return null;
}

function initialValues(account: UpstreamAccountItem): SubscriptionBatchFormValues {
  return {
    paidMajor: "",
    surchargeMajor: "",
    currency: account.currency,
    startsOn: "",
    expiresOn: "",
    accountCount: "1",
    proxyAssetId: "",
  };
}
