import { ApiError } from "../api/client";
import { errorCodeHint, errorCodeNextStep, errorCodeNote } from "../lib/labels";

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
  const nextStep = errorCodeNextStep(error.code);

  return (
    <div className="flex flex-col gap-1">
      {/* 错误码的中文与原码在同一段文字里，不拆进子元素：中文是给读不懂
          英文的人看的，原码是拿去 grep 服务端日志的，两样都要能被整段复制。
          完整解释挂 title——它是一整句话，摊在每一次报错下面只会淹没主文案。 */}
      <p
        role="alert"
        className="text-xs text-danger"
        // 不认识的码没有解释，那就**不要**这个属性——一个空的 title 会让人
        // 悬停上去等出一句话，然后什么也没有。
        title={errorCodeHint(error.code) || undefined}
      >
        {error.message}（{errorCodeNote(error.code)}）
      </p>
      {/* 「下一步」是可见的一行，不是悬停：光一句「需要高级管控」，使用者
          不知道该干什么，而这正是本片要解决的那种「码摆在那儿等于没说」。
          没有下一步的码不渲染这一行——空段落比没有更糟。 */}
      {nextStep ? <p className="text-xs text-fg-muted">{nextStep}</p> : null}
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
