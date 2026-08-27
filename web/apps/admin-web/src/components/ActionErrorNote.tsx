import { ApiError } from "../api/client";

export interface ActionErrorNoteProps {
  error: unknown;
  /** 该动作声明的 Permission。403 时用来兜底提示「缺哪个权限」。 */
  permission?: string;
}

/** 写路径失败时在弹窗里就地显示的错误说明。
 *
 *  和只读页的 ApiStateView 分开：读失败可以把整页换成错误态，写失败**必须**
 *  把表单留在原地——人刚填完的内容不能因为一次 403 就消失。
 *
 *  一律带上错误码与 request_id：报障时这两样能直接对上服务端日志与审计事件
 *  （规格 §5.8、§18.4）。 */
export function ActionErrorNote({ error, permission }: ActionErrorNoteProps) {
  if (!error) return null;

  if (!(error instanceof ApiError)) {
    return (
      <p role="alert" className="text-xs text-danger">
        {error instanceof Error ? error.message : "未知错误"}
      </p>
    );
  }

  // 后端 403 文案形如「缺少权限 registry.service.manage」（action/kernel.go），
  // 能抠出来就用真话；抠不出来才退回调用方声明的 Permission
  const missing = error.missingScope ?? permission;

  return (
    <div className="flex flex-col gap-1">
      <p role="alert" className="text-xs text-danger">
        {error.message}（错误码 {error.code}）
      </p>
      {error.status === 403 && missing ? (
        <p className="text-xs text-fg-muted">
          需要权限：{missing}（前端隐藏不构成安全控制，服务端为最终裁决）
        </p>
      ) : null}
      {error.requestId ? (
        <p className="font-mono text-xs text-fg-muted">request_id: {error.requestId}</p>
      ) : null}
    </div>
  );
}
