import { useMutation } from "@tanstack/react-query";
import { Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import {
  setProxyAsset,
  SUBSCRIPTION_MANAGE_PERMISSION,
  type ProxyAssetItem,
  type UpstreamAccountItem,
} from "../api/finance";
import { formatScaledMinorUnits } from "../lib/money";
import {
  SUPPORTED_FINANCE_CURRENCIES,
  buildProxyAssetParams,
  effectiveDaysInclusive,
  validateProxyAssetForm,
  type ProxyAssetFormErrors,
  type ProxyAssetFormValues,
} from "../lib/subscriptionForms";
import { ActionErrorNote } from "./ActionErrorNote";

export interface ProxyAssetDialogProps {
  account: UpstreamAccountItem;
  proxy?: ProxyAssetItem;
  disabled?: boolean;
  onDone: (runId: string) => void;
}

export function ProxyAssetDialog({
  account,
  proxy,
  disabled = false,
  onDone,
}: ProxyAssetDialogProps) {
  const mode = proxy ? "edit" : "create";
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<ProxyAssetFormValues>(() => initialValues(account, proxy));
  const [errors, setErrors] = useState<ProxyAssetFormErrors>({});
  const [attempts, setAttempts] = useState(0);
  const prefix = useId();
  const summaryRef = useRef<HTMLDivElement>(null);

  const mutation = useMutation({
    mutationFn: (form: ProxyAssetFormValues) =>
      proxy
        ? setProxyAsset(buildProxyAssetParams(form, "edit", proxy.id))
        : setProxyAsset(buildProxyAssetParams(form, "create")),
    onSuccess: (run) => {
      setOpen(false);
      setErrors({});
      setAttempts(0);
      setValues(initialValues(account, proxy));
      onDone(run.runId);
    },
  });

  const failedFields = Object.keys(errors).filter((field) => errors[field as keyof ProxyAssetFormErrors]);
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(mutation.error));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [showSummary, attempts, mutation.error]);

  const set = <K extends keyof ProxyAssetFormValues>(field: K, value: ProxyAssetFormValues[K]) => {
    setValues((previous) => ({ ...previous, [field]: value }));
    setErrors((previous) => {
      if (!(field in previous)) return previous;
      const next = { ...previous };
      delete next[field];
      return next;
    });
  };

  const blur = (field: keyof ProxyAssetFormValues) => {
    const problem = validateProxyAssetForm(values, mode)[field];
    setErrors((previous) => ({ ...previous, [field]: problem }));
  };

  const submit = () => {
    setAttempts((count) => count + 1);
    const found = validateProxyAssetForm(values, mode);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    mutation.mutate(values);
  };

  const days = effectiveDaysInclusive(values.openedOn, values.expiresOn);

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) setValues(initialValues(account, proxy));
        else {
          setErrors({});
          setAttempts(0);
          mutation.reset();
        }
      }}
      trigger={
        <Button
          size="sm"
          variant={proxy ? "secondary" : "primary"}
          disabled={disabled}
          title={disabled ? `需要 ${SUBSCRIPTION_MANAGE_PERMISSION}` : undefined}
        >
          {proxy ? "修改代理" : "登记代理资产"}
        </Button>
      }
      title={proxy ? "修改代理资产" : "登记代理资产"}
      description={
        proxy
          ? "finance.proxy_asset.set@1 只更新购买信息、CredentialRef 与挂载状态；金额和期间保持冻结。"
          : "finance.proxy_asset.set@1 单独登记代理资产；它不会与订阅批次组成原子事务。"
      }
    >
      <form
        className="flex max-h-[65vh] flex-col gap-3 overflow-y-auto"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        {proxy ? (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <FormField label="实际支付（不可修改）" htmlFor={`${prefix}-frozen-paid`}>
              <Input
                readOnly
                value={formatScaledMinorUnits(proxy.paid.amount_minor, proxy.currency, proxy.paid.scale)}
              />
            </FormField>
            <FormField label="币种（不可修改）" htmlFor={`${prefix}-frozen-currency`}>
              <Input readOnly value={proxy.currency} />
            </FormField>
            <FormField label="有效期（不可修改）" htmlFor={`${prefix}-frozen-period`}>
              <Input readOnly value={`${proxy.opened_on} → ${proxy.expires_on}`} />
            </FormField>
            <FormField label="共享账号数（不可修改）" htmlFor={`${prefix}-frozen-count`}>
              <Input readOnly value={String(proxy.shared_account_count)} />
            </FormField>
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <FormField label="实际支付" htmlFor={`${prefix}-paid`} required error={errors.paidMajor}>
              <Input
                value={values.paidMajor}
                inputMode="decimal"
                invalid={Boolean(errors.paidMajor)}
                onChange={(event) => set("paidMajor", event.target.value)}
                onBlur={() => blur("paidMajor")}
              />
            </FormField>
            <FormField label="附加费用" htmlFor={`${prefix}-surcharge`} error={errors.surchargeMajor} hint="可留空；空白明确写为 0">
              <Input
                value={values.surchargeMajor}
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
            <FormField label="共享账号数量" htmlFor={`${prefix}-shared-count`} required error={errors.sharedAccountCount}>
              <Input
                value={values.sharedAccountCount}
                inputMode="numeric"
                invalid={Boolean(errors.sharedAccountCount)}
                onChange={(event) => set("sharedAccountCount", event.target.value)}
                onBlur={() => blur("sharedAccountCount")}
              />
            </FormField>
            <FormField label="开通日期" htmlFor={`${prefix}-opened`} required error={errors.openedOn}>
              <Input
                type="date"
                value={values.openedOn}
                invalid={Boolean(errors.openedOn)}
                onChange={(event) => set("openedOn", event.target.value)}
                onBlur={() => blur("openedOn")}
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
            <p className="text-xs text-fg-muted sm:col-span-2" aria-live="polite">
              {days === null
                ? "有效天数：待填写有效起止日期"
                : `有效天数：${days} 天（起止日均计入；仅用于解释）`}
            </p>
          </div>
        )}

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <FormField label="购买平台" htmlFor={`${prefix}-buy-platform`}>
            <Input
              value={values.buyPlatform}
              onChange={(event) => set("buyPlatform", event.target.value)}
            />
          </FormField>
          <FormField label="购买地址" htmlFor={`${prefix}-buy-address`} error={errors.buyAddress} hint="纯文字可填；URL 不得含 user:pass@">
            <Input
              value={values.buyAddress}
              invalid={Boolean(errors.buyAddress)}
              onChange={(event) => set("buyAddress", event.target.value)}
              onBlur={() => blur("buyAddress")}
            />
          </FormField>
          <FormField label="CredentialRef" htmlFor={`${prefix}-credential`} error={errors.credentialRef} hint="可留空；填写时只接受 secret:// 引用，绝不填明文">
            <Input
              value={values.credentialRef}
              invalid={Boolean(errors.credentialRef)}
              onChange={(event) => set("credentialRef", event.target.value)}
              onBlur={() => blur("credentialRef")}
            />
          </FormField>
          <div className="flex items-center">
            <label htmlFor={`${prefix}-mounted`} className="inline-flex min-h-11 cursor-pointer items-center gap-2 text-sm text-fg">
              <input
                id={`${prefix}-mounted`}
                type="checkbox"
                checked={values.mounted}
                onChange={(event) => set("mounted", event.target.checked)}
                className="size-4 accent-accent"
              />
              当前挂载
            </label>
          </div>
        </div>
        <p className="text-xs text-fg-muted">
          取消挂载是可逆状态，只让当日代理成本成为已知 0；它不是终止、删除或损失结转。
        </p>

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
          <Button type="submit" size="sm" loading={mutation.isPending}>
            {proxy ? "保存代理" : "登记代理资产"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function initialValues(
  account: UpstreamAccountItem,
  proxy: ProxyAssetItem | undefined,
): ProxyAssetFormValues {
  return {
    paidMajor: "",
    surchargeMajor: "",
    currency: account.currency,
    openedOn: "",
    expiresOn: "",
    sharedAccountCount: "1",
    buyPlatform: proxy?.buy_platform ?? "",
    buyAddress: proxy?.buy_address ?? "",
    credentialRef: proxy?.credential_ref ?? "",
    mounted: proxy?.mounted ?? true,
  };
}
