import { PageState } from "@xingmang/ui-admin";
import type { ReactElement, ReactNode } from "react";
import { ApiError, FeatureNotMountedError } from "../api/client";

export interface ApiStateViewProps {
  isPending: boolean;
  error: unknown;
  onRetry: () => void;
  /** 加载成功后要显示的内容。 */
  children: ReactNode;
  /** 嵌在表格或卡片内部时收窄留白。 */
  compact?: boolean;
}

/** 只读查询的加载态 / 错误态包壳。
 *
 *  它是**适配器**，不是呈现：把 react-query 的结果与 ApiError 翻译成 PageState 的
 *  props。呈现本身在 ui-admin/PageState——那边不认识 ApiError，所以 Storybook
 *  渲染它不需要任何上下文；这边认识 ApiError，所以能把「缺哪个 scope」「能不能
 *  重试」「request_id 是多少」讲清楚。两件事分在两层，是因为它们的变化速度不同:
 *  错误契约随后端走，状态的样子随设计走。
 *
 *  做成包壳而不是「返回可空节点由页面自己判空」：后者写成
 *  `{state ?? <Table/>}` 时永远为真（JSX 元素本身就是真值），页面会静默地
 *  再也不渲染数据。包壳让这种错误不可表示。 */
export function ApiStateView({
  isPending,
  error,
  onRetry,
  children,
  compact = false,
}: ApiStateViewProps): ReactElement {
  if (error) return <ApiErrorView error={error} onRetry={onRetry} compact={compact} />;
  if (isPending) return <PageState kind="loading" compact={compact} />;
  return <>{children}</>;
}

function ApiErrorView({
  error,
  onRetry,
  compact,
}: {
  error: unknown;
  onRetry: () => void;
  compact: boolean;
}): ReactElement {
  // 整组端点没挂载（连接器/模式=off），不是这一次请求失败了。**不给重试
  // 按钮**：off 不会因为再点一次就变成 on；也不带错误码/request_id——
  // 那两样是给「报障」用的，这里没有障要报。kind="unavailable" 的默认标题
  // 就是「未接入」（ui-admin/PageState），不必再传一遍
  if (error instanceof FeatureNotMountedError) {
    return <PageState kind="unavailable" description={error.description} compact={compact} />;
  }

  if (!(error instanceof ApiError)) {
    const message = error instanceof Error ? error.message : "未知错误";
    return <PageState kind="error" message={message} onRetry={onRetry} compact={compact} />;
  }

  const footnote = error.requestId ? `request_id: ${error.requestId}` : undefined;

  // 401/403：先把「缺哪个权限」讲清楚。后端 403 文案里带 scope 名
  // (httpapi/authz.go)，能抠出来就直接显示，省得人去翻服务端日志。
  // **不给重试按钮**：权限不足重试一万次都是同一个答案
  if (error.isAuthFailure) {
    return error.missingScope ? (
      <PageState
        kind="denied"
        permission={error.missingScope}
        {...(footnote ? { footnote } : {})}
        compact={compact}
      />
    ) : (
      <PageState
        kind="error"
        message={error.message}
        {...(footnote ? { footnote } : {})}
        compact={compact}
      />
    );
  }

  // 其余错误：带上错误码，报障时能对上服务端日志。重试按钮只给重试有意义的
  // 情况——对 400 这类错误再点一次还是同样的答案
  return (
    <PageState
      kind="error"
      message={`${error.message}（错误码 ${error.code}）`}
      {...(error.retryable ? { onRetry } : {})}
      {...(footnote ? { footnote } : {})}
      compact={compact}
    />
  );
}
