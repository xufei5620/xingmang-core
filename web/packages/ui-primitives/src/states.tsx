import type { ReactNode } from "react";

function StateShell({ children }: { children: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-edge bg-surface p-8 text-center">
      {children}
    </div>
  );
}

export function LoadingState({ label = "加载中…" }: { label?: string }) {
  return (
    <StateShell>
      <span
        aria-hidden
        className="size-6 animate-spin rounded-full border-2 border-accent border-t-transparent"
      />
      <p role="status" className="text-sm text-fg-muted">{label}</p>
    </StateShell>
  );
}

export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <StateShell>
      <p className="text-sm font-medium text-fg">{title}</p>
      {description ? <p className="text-xs text-fg-muted">{description}</p> : null}
      {action}
    </StateShell>
  );
}

export function ErrorState({
  message,
  onRetry,
}: {
  message: string;
  onRetry?: () => void;
}) {
  return (
    <StateShell>
      <p className="text-sm font-medium text-danger">加载失败</p>
      <p className="text-xs text-fg-muted">{message}</p>
      {onRetry ? (
        <button
          type="button"
          onClick={onRetry}
          className="text-xs font-medium text-accent hover:underline"
        >
          重试
        </button>
      ) : null}
    </StateShell>
  );
}

export function PermissionDenied({ permission }: { permission?: string }) {
  return (
    <StateShell>
      <p className="text-sm font-medium text-fg">无权访问</p>
      <p className="text-xs text-fg-muted">
        {permission ? `需要权限：${permission}（` : "（"}服务端为最终裁决，
        前端隐藏不构成安全控制）
      </p>
    </StateShell>
  );
}
