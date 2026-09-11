import { PageState } from "@xingmang/ui-admin";
import { lazy, Suspense, useCallback, useState } from "react";
import type { NativeAdminMode } from "solov-invoice-web/native-admin";
import { appApiConfig } from "../api/config";
import { ApiError } from "../api/client";
import { FINANCE_READ_PERMISSION } from "../api/finance";
import { stepUpTotp } from "../auth/localSession";
import { TotpVerifyForm } from "./TotpVerifyForm";

const NativeAdminWorkspace = lazy(() => import("solov-invoice-web/native-admin").then(module => ({ default: module.NativeAdminWorkspace })));

export type InvoiceConsoleMode = NativeAdminMode;
export function InvoiceConsolePanel({ mode, scopes = appApiConfig.scopes }: { mode: InvoiceConsoleMode; scopes?: readonly string[] }) {
  const [stepUp, setStepUp] = useState(false);
  const [revision, setRevision] = useState(0);
  const requestStepUp = useCallback(() => setStepUp(true), []);
  if (!scopes.includes(FINANCE_READ_PERMISSION)) return <PageState kind="denied" permission={FINANCE_READ_PERMISSION} />;
  return <>
    {stepUp && <div className="mx-auto max-w-md">
      <TotpVerifyForm title="需要二次验证" description="验证当前控制台账号后继续开票管理。" submitLabel="验证并继续"
        mapError={cause => ({ message: cause instanceof ApiError ? cause.message : "验证失败，请重试。", expired: false })}
        onVerify={async input => { await stepUpTotp(input); setStepUp(false); setRevision(value => value + 1); }} />
    </div>}
    <div hidden={stepUp} inert={stepUp}>
      <Suspense fallback={<PageState kind="loading" title="开票" />}>
        <NativeAdminWorkspace mode={mode} onStepUp={requestStepUp} sessionRevision={revision} />
      </Suspense>
    </div>
  </>;
}
