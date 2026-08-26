import { ErrorState, LoadingState, PermissionDenied } from "@xingmang/ui-primitives";
import type { ReactElement, ReactNode } from "react";
import { ApiError } from "../api/client";

export interface ApiStateViewProps {
  isPending: boolean;
  error: unknown;
  onRetry: () => void;
  /** 加载成功后要显示的内容。 */
  children: ReactNode;
}

/** 只读查询的加载态 / 错误态包壳。
 *
 *  做成包壳而不是「返回可空节点由页面自己判空」：后者写成
 *  `{state ?? <Table/>}` 时永远为真（JSX 元素本身就是真值），页面会静默地
 *  再也不渲染数据。包壳让这种错误不可表示。 */
export function ApiStateView({
  isPending,
  error,
  onRetry,
  children,
}: ApiStateViewProps): ReactElement {
  if (error) return <ApiErrorView error={error} onRetry={onRetry} />;
  if (isPending) return <LoadingState />;
  return <>{children}</>;
}

function ApiErrorView({ error, onRetry }: { error: unknown; onRetry: () => void }): ReactElement {
  if (!(error instanceof ApiError)) {
    const message = error instanceof Error ? error.message : "未知错误";
    return <ErrorState message={message} onRetry={onRetry} />;
  }

  // 401/403：先把「缺哪个权限」讲清楚。后端 403 文案里带 scope 名
  // （httpapi/authz.go），能抠出来就直接显示，省得人去翻服务端日志
  if (error.isAuthFailure) {
    return (
      <div className="flex flex-col gap-2">
        {error.missingScope ? (
          <PermissionDenied permission={error.missingScope} />
        ) : (
          <ErrorState message={error.message} />
        )}
        <RequestIdLine error={error} />
      </div>
    );
  }

  // 其余错误：带上错误码，报障时能对上服务端日志。重试按钮只给重试有意义的
  // 情况——对 400 这类错误再点一次还是同样的答案
  return (
    <div className="flex flex-col gap-2">
      <ErrorState
        message={`${error.message}（错误码 ${error.code}）`}
        {...(error.retryable ? { onRetry } : {})}
      />
      <RequestIdLine error={error} />
    </div>
  );
}

function RequestIdLine({ error }: { error: ApiError }): ReactElement | null {
  if (!error.requestId) return null;
  return <p className="text-center font-mono text-xs text-fg-muted">request_id: {error.requestId}</p>;
}
