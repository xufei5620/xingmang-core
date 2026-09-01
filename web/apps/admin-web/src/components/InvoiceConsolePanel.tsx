import { EmbeddedConsoleFrame, PageState, type EmbeddedConsolePath } from "@xingmang/ui-admin";
import { appApiConfig } from "../api/config";
import { FINANCE_READ_PERMISSION } from "../api/finance";
import { getRuntimeConfig } from "../auth/runtimeConfig";

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

/** 开票控制台嵌入面板（CR-0005 平台线 g/h/i/j/k）。
 *
 *  只做两件事：可见性判断（scope 与配置），把结果交给纯展示的
 *  EmbeddedConsoleFrame。真正的鉴权、审批、双人复核与审计全部在开票系统里
 *  （CR-0005「明确不变」）；这里的 scope 判断只是体验层的门禁，不是安全
 *  边界——服务端（这里是开票系统自己的 OIDC）才是最终裁决者。
 *
 *  k：平台侧不展示任何开票数字。三种状态（denied / unavailable / 正常）都不
 *  读取、不显示开票记录数或金额，iframe 内部的内容对这个组件永远不透明。 */
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

  const { invoiceConsoleOrigin } = getRuntimeConfig();
  if (!invoiceConsoleOrigin) {
    return (
      <PageState
        kind="unavailable"
        title={TITLE}
        description="未配置开票控制台来源（XM_INVOICE_CONSOLE_ORIGIN）"
      />
    );
  }

  return <EmbeddedConsoleFrame origin={invoiceConsoleOrigin} path={EMBED_PATH[mode]} title={TITLE} />;
}
