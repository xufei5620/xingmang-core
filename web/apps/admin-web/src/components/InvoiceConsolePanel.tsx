import { EmbeddedConsoleFrame, PageState, type EmbeddedConsolePath } from "@xingmang/ui-admin";
import { useCallback, useEffect, useRef, useState } from "react";
import { appApiConfig } from "../api/config";
import { ApiError, looksLikeUnmountedRoute } from "../api/client";
import {
  issueConsoleAssertion,
  type ConsoleAssertionResult,
  type ConsoleAssertionScope,
} from "../api/consoleAssertion";
import { FINANCE_READ_PERMISSION } from "../api/finance";
import { stepUpTotp } from "../auth/localSession";
import { getRuntimeConfig } from "../auth/runtimeConfig";
import { TotpVerifyForm } from "./TotpVerifyForm";

/** CR-0005 平台线 g：开票控制台嵌入的三个位置。Sub2API / NewAPI 各自的
 *  「支付与财务 → 开票」按平台过滤；`global` 是治理「跨平台财务 → 开票集成」，
 *  只展示开票系统的设置与两平台源健康总览，不按平台过滤（开票线负责按
 *  路径分流到哪种模式，平台侧只决定拼哪个路径）。 */
export type InvoiceConsoleMode = "sub2api" | "newapi" | "global";

const EMBED_PATH: Readonly<Record<InvoiceConsoleMode, EmbeddedConsolePath>> = {
  sub2api: "/embed/admin/sub2api",
  newapi: "/embed/admin/newapi",
  global: "/embed/admin/global",
};

/** 三处入口共用的标题。治理页的页签本身叫「开票集成」，但内容与另外两处是
 *  同一个开票系统管理端；未配置/无权限时的提示统一叫「开票」——CR-0005
 *  平台线 i 原文写的就是这个标题，不跟着页签名走。 */
const TITLE = "开票";

/** 重新签发的安全余量：在断言真正过期前这么久就主动换新的一份，留出网络
 *  往返与用户观察延迟的余地——真过期了才补救，iframe 至少会经历一次
 *  "已登录→掉线"的闪烁，不如提前换。 */
const REISSUE_SAFETY_MARGIN_MS = 60_000;
/** 解析不出 expires_at（不该发生，但网络层什么畸形响应都可能出现）时的
 *  兜底重签发间隔——断言硬上限是 5 分钟，4 分钟给了一次重试的余地。 */
const FALLBACK_REISSUE_DELAY_MS = 4 * 60_000;
/** 重签发间隔下限：即使服务端返回一个几乎立刻过期的 expires_at，也不该把
 *  定时器钉成几乎连续重试的忙轮询。 */
const MIN_REISSUE_DELAY_MS = 5_000;

type AssertionState =
  | { kind: "loading" }
  | { kind: "ready"; assertion: string }
  | { kind: "step-up" }
  | { kind: "denied"; message: string }
  | { kind: "unavailable" }
  | { kind: "error"; message: string };

function mapAssertionError(cause: unknown): AssertionState {
  if (looksLikeUnmountedRoute(cause)) return { kind: "unavailable" };
  if (cause instanceof ApiError) {
    switch (cause.code) {
      case "FINANCE_SCOPE_REQUIRED":
      case "ADMIN_NETWORK_DENIED":
        return { kind: "denied", message: cause.message };
      case "ADMIN_STEP_UP_REQUIRED":
        return { kind: "step-up" };
      default:
        return { kind: "error", message: cause.message };
    }
  }
  return { kind: "error", message: cause instanceof Error ? cause.message : "签发断言失败，请重试。" };
}

/** 步进校验失败时的文案映射——与 LoginPage 的 totpLoginErrorMessage 同一
 *  条纪律，只是这里没有"临时令牌过期"这个概念（步进用的是已有会话，不是
 *  temp_token），所以 expired 恒为 false。 */
function stepUpErrorMessage(cause: unknown): { message: string; expired: boolean } {
  if (cause instanceof ApiError) {
    switch (cause.code) {
      case "INVALID_CREDENTIALS":
        return { message: "验证码或恢复码不正确。", expired: false };
      case "ACCOUNT_LOCKED":
        return { message: "验证失败次数过多，账号已被锁定，请稍后重试或联系管理员。", expired: false };
      case "ADMIN_NETWORK_DENIED":
        return { message: "当前网络不在管理员访问名单内，请更换网络或联系管理员。", expired: false };
      case "RATE_LIMITED":
        return { message: "尝试过于频繁，请稍后重试。", expired: false };
      default:
        return { message: `${cause.message}（错误码 ${cause.code}）`, expired: false };
    }
  }
  return { message: cause instanceof Error ? cause.message : "验证失败，请重试。", expired: false };
}

/** 管理断言签发/重签发生命周期的 hook：首次挂载即签发，成功后按
 *  `expires_at` 排一次提前重签发；`reissue` 供步进完成后或 iframe 主动要求
 *  时手动触发。`active=false`（非 local 模式）时完全不发起任何请求——
 *  开票系统在这些模式下仍走它自己原有的弹窗 OIDC 登录，不受影响。 */
function useConsoleAssertion(scope: ConsoleAssertionScope, active: boolean) {
  const [state, setState] = useState<AssertionState>({ kind: "loading" });
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  // 用递增序号而不是布尔"是否已卸载"：这里还要处理"这次响应虽然还没过时，
  // 但已经被一次更晚发起的 reissue() 超过"的情形（例如步进完成后立刻手动
  // reissue，而上一次因为权限问题失败的请求这时才姗姗来迟）。
  const seqRef = useRef(0);

  const issue = useCallback(() => {
    if (!active) return;
    const seq = ++seqRef.current;
    clearTimeout(timerRef.current);
    issueConsoleAssertion(scope)
      .then((result: ConsoleAssertionResult) => {
        if (seqRef.current !== seq) return;
        setState({ kind: "ready", assertion: result.assertion });
        const expiresAtMs = Date.parse(result.expiresAt);
        const delay = Number.isFinite(expiresAtMs)
          ? Math.max(MIN_REISSUE_DELAY_MS, expiresAtMs - Date.now() - REISSUE_SAFETY_MARGIN_MS)
          : FALLBACK_REISSUE_DELAY_MS;
        timerRef.current = setTimeout(issue, delay);
      })
      .catch((cause: unknown) => {
        if (seqRef.current !== seq) return;
        setState(mapAssertionError(cause));
      });
  }, [active, scope]);

  useEffect(() => {
    if (!active) return undefined;
    issue();
    return () => {
      seqRef.current += 1;
      clearTimeout(timerRef.current);
    };
  }, [active, issue]);

  return { state, reissue: issue };
}

/** 开票控制台嵌入面板（CR-0005 平台线 g/h/i/j/k；CR-0006 XM-INVCON1 新增
 *  断言签发/转交/步进）。
 *
 *  可见性判断（scope 与配置）与 EmbeddedConsoleFrame 承载仍是纯展示——真正
 *  的鉴权、审批、双人复核与审计全部在开票系统里（CR-0005「明确不变」）。
 *  断言机制没有改变这条：它只是把"打开哪个 iframe"之外新增了"控制台先证明
 *  一次自己刚验过 TOTP，再把这份证明转交给 iframe"这一步，服务端裁决权
 *  仍在开票系统自己的兑换端点。
 *
 *  k：平台侧不展示任何开票数字。三种状态（denied / unavailable / 正常）都不
 *  读取、不显示开票记录数或金额，iframe 内部的内容对这个组件永远不透明——
 *  断言本身也只是一枚不透明的字符串，本组件不解析它的 claims。 */
export function InvoiceConsolePanel({
  mode,
  scopes = appApiConfig.scopes,
}: {
  mode: InvoiceConsoleMode;
  /** 注入点：测试不必伪造 appApiConfig。默认来源与其它财务子页签的 scope
   *  判断同一处（见 UpstreamAccountDetail 的同一模式）。 */
  scopes?: readonly string[];
}) {
  // j：可见性以 finance.read 控制，与其它财务子页签同一条规矩；菜单可见
  // 不等于授权，真正的裁决在开票系统自己的登录与授权里。不传 title——沿用
  // PageState 的默认「无权访问」，与 ApiStateView 里其它 403 的呈现方式一致
  if (!scopes.includes(FINANCE_READ_PERMISSION)) {
    return <PageState kind="denied" permission={FINANCE_READ_PERMISSION} />;
  }

  const { invoiceConsoleOrigin, authMode } = getRuntimeConfig();
  if (!invoiceConsoleOrigin) {
    return (
      <PageState
        kind="unavailable"
        title={TITLE}
        description="未配置开票控制台来源（XM_INVOICE_CONSOLE_ORIGIN）"
      />
    );
  }

  // 断言登录只在 local 模式下有意义（依赖 core.staff_session.mfa_at，
  // oidc/dev-header 两种身份解析器都不填充它，见后端 consoleassertion 包的
  // 注释）；其余模式原样回落到"iframe 内自行弹窗 OIDC 登录"，行为与
  // XM-INVCON0 交付时完全一致，不受本次改动影响。
  return <InvoiceConsoleFrameWithAssertion mode={mode} origin={invoiceConsoleOrigin} isLocalAuth={authMode === "local"} />;
}

function InvoiceConsoleFrameWithAssertion({
  mode,
  origin,
  isLocalAuth,
}: {
  mode: InvoiceConsoleMode;
  origin: string;
  isLocalAuth: boolean;
}) {
  const { state, reissue } = useConsoleAssertion(mode, isLocalAuth);

  if (!isLocalAuth) {
    return <EmbeddedConsoleFrame origin={origin} path={EMBED_PATH[mode]} title={TITLE} />;
  }

  switch (state.kind) {
    case "loading":
      return <PageState kind="loading" title={TITLE} />;
    case "denied":
      // 不传 title——沿用 PageState 默认的「无权访问」，与上方 finance.read
      // 缺失时的呈现方式一致（同一件事只有一种说法）。
      return <PageState kind="denied" description={state.message} />;
    case "unavailable":
      return (
        <PageState
          kind="unavailable"
          title={TITLE}
          description="开票系统未启用断言登录，请联系运维确认 XM_INVOICE_CONSOLE_ASSERTION_ENABLED 配置。"
        />
      );
    case "error":
      return <PageState kind="error" title={TITLE} message={state.message} onRetry={reissue} />;
    case "step-up":
      return (
        <div className="mx-auto max-w-md">
          <TotpVerifyForm
            title="需要二次验证"
            description="距上次验证已超过有效期，请重新输入认证器 App 中显示的 6 位动态码后继续。"
            submitLabel="验证并继续"
            mapError={stepUpErrorMessage}
            onVerify={async (input) => {
              await stepUpTotp(input);
              reissue();
            }}
          />
        </div>
      );
    case "ready":
      return (
        <EmbeddedConsoleFrame
          origin={origin}
          path={EMBED_PATH[mode]}
          title={TITLE}
          assertion={{ assertion: state.assertion }}
          onAssertionNeeded={reissue}
        />
      );
  }
}
