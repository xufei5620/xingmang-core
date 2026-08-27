import type { ReactNode } from "react";
import { cx } from "@xingmang/ui-primitives";

/** 一屏（或一块）当前处在哪种状态。
 *
 *  五种而不是三种：`denied` 与 `unavailable` 常被塞进 `error` 或 `empty`,
 *  但它们对人意味着完全不同的下一步——
 *  - `error` 是「这次请求失败了」，下一步是重试或报障;
 *  - `denied` 是「你没有这个权限」，下一步是去要权限，重试一万次都一样;
 *  - `empty` 是「读到了，里面没有」，下一步是去造一条数据;
 *  - `unavailable` 是「这块我们还没建」，下一步是等排期——**不是**没有数据。
 *  把后两者混起来，就是让运营以为「这个平台今天没有告警」，而事实是我们没接。 */
export type PageStateKind = "loading" | "empty" | "error" | "denied" | "unavailable";

export interface PageStateProps {
  kind: PageStateKind;
  /** 标题。不传时按 kind 取默认值(`empty` 必须自己传——空的是什么只有调用方知道)。 */
  title?: string;
  description?: ReactNode;
  /** `error` 用：错误正文。 */
  message?: string;
  /** `denied` 用：缺哪个 scope。 */
  permission?: string;
  /** 传了才显示重试按钮。对 400 这类错误重试没有意义，就别给按钮。 */
  onRetry?: () => void;
  /** 底部补充行（request_id、阶段说明等）。 */
  footnote?: ReactNode;
  /** 主操作槽（「去登记一个」这类）。 */
  action?: ReactNode;
  /** 嵌在表格或卡片内部时收窄留白。整页状态用默认值。 */
  compact?: boolean;
}

const DEFAULT_TITLE: Record<PageStateKind, string> = {
  loading: "加载中…",
  // empty 没有通用默认值：「暂无数据」什么也没说明。调用方必须说清楚空的是什么
  empty: "没有数据",
  error: "加载失败",
  denied: "无权访问",
  unavailable: "未接入",
};

/** 页面/区块状态的统一呈现（UI 交接文档 §12 的八态里的五种数据态）。
 *
 *  收敛成一个组件的理由不是省代码，是**让这五种状态说的话保持一致**:
 *  XM-0045 之前每个页面各自拼 EmptyState/ErrorState，于是同一件事在不同页上
 *  有不同说法——「暂无渠道」「没有渠道」「该环境没有渠道余额指标」都出现过,
 *  而其中只有第三个说清楚了到底是什么空了。
 *
 *  这个组件是**纯展示**的：它不认识 ApiError，也不认识 react-query。
 *  把查询结果翻译成这里的 props 是应用侧的事（admin-web/ApiStateView）,
 *  于是 Storybook 渲染它不需要任何上下文。 */
export function PageState({
  kind,
  title,
  description,
  message,
  permission,
  onRetry,
  footnote,
  action,
  compact = false,
}: PageStateProps) {
  const heading = title ?? DEFAULT_TITLE[kind];

  return (
    <div
      className={cx(
        "flex flex-col items-center justify-center gap-2 rounded-lg border border-edge bg-surface text-center",
        compact ? "p-4" : "p-8",
      )}
    >
      {kind === "loading" ? (
        <span
          aria-hidden="true"
          className="size-6 animate-spin rounded-full border-2 border-accent border-t-transparent"
        />
      ) : null}

      <p
        // loading 用 status 让读屏播报「加载中」；其余四种是静态说明,
        // 播报它们会在每次换页时打断使用者
        {...(kind === "loading" ? { role: "status" } : {})}
        className={cx(
          "text-sm font-medium",
          kind === "error" ? "text-danger" : kind === "loading" ? "text-fg-muted" : "text-fg",
        )}
      >
        {heading}
      </p>

      {kind === "error" && message ? (
        <p className="text-xs text-fg-muted">{message}</p>
      ) : null}

      {kind === "denied" ? (
        <p className="text-xs text-fg-muted">
          {permission ? `需要权限：${permission}。` : null}
          服务端为最终裁决，前端隐藏不构成安全控制。
        </p>
      ) : null}

      {description ? <p className="text-xs text-fg-muted">{description}</p> : null}

      {onRetry ? (
        <button
          type="button"
          onClick={onRetry}
          className="text-xs font-medium text-accent hover:underline"
        >
          重试
        </button>
      ) : null}

      {action}
      {footnote ? <p className="font-mono text-xs text-fg-muted">{footnote}</p> : null}
    </div>
  );
}
