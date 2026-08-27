import type { ReactNode } from "react";
import { Badge, type BadgeProps } from "@xingmang/ui-primitives";

export interface ContextCrumb {
  key: string;
  /** 面包屑项的内容。做成 ReactNode 而不是 `href`：包不依赖 router，链接由应用
   *  给（同 AdminShell 的 nav 槽位）。最后一项由本组件渲染成当前位置，
   *  调用方给纯文本即可，不必自己判断「最后一项不该是链接」。 */
  label: ReactNode;
}

export interface ContextStripProps {
  /** 从上到下的层级。最后一项是当前页，会被标成 aria-current="page"。 */
  crumbs?: ContextCrumb[];
  /** 环境标识：staging / production / 演示数据。 */
  environment?: { label: string; tone?: BadgeProps["tone"]; hint?: string };
  /** 环境右侧的一句话说明：这一屏的数据是什么性质、从哪来。 */
  note?: ReactNode;
  /** 右端附加内容（阶段标签、说明入口等）。 */
  actions?: ReactNode;
}

/** 面包屑 + 环境提示条（UI 交接文档 §11.2 顶栏合同）。
 *
 *  单独成条而不是塞进顶栏：顶栏那一行要放搜索与用户，挤进面包屑之后三样都被压扁；
 *  更要紧的是这条信息回答的是「你在哪、这屏数据算不算数」，它得在**每一页**上
 *  都在同一个位置，才可能被当成可依赖的坐标而不是某几页的装饰。
 *
 *  环境标识和演示数据横幅不重复：横幅只在判定出演示数据时才出现且不可关闭
 *  （见应用侧 DemoDataBanner），这里则是常驻的「当前环境是什么」。 */
export function ContextStrip({ crumbs = [], environment, note, actions }: ContextStripProps) {
  const hasTrailing = Boolean(environment || note || actions);
  if (crumbs.length === 0 && !hasTrailing) return null;

  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-edge bg-surface px-4 py-1.5 text-xs">
      {crumbs.length > 0 ? (
        <nav aria-label="面包屑" className="min-w-0">
          <ol className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-fg-muted">
            {crumbs.map((crumb, index) => {
              const isCurrent = index === crumbs.length - 1;
              return (
                <li key={crumb.key} className="flex min-w-0 items-center gap-x-1.5">
                  {/* 分隔符只是形状，不该被读屏念成内容 */}
                  {index > 0 ? <span aria-hidden="true">/</span> : null}
                  {isCurrent ? (
                    <span aria-current="page" className="truncate font-medium text-fg">
                      {crumb.label}
                    </span>
                  ) : (
                    <span className="truncate">{crumb.label}</span>
                  )}
                </li>
              );
            })}
          </ol>
        </nav>
      ) : null}

      {hasTrailing ? (
        <div className="ml-auto flex shrink-0 flex-wrap items-center gap-2">
          {environment ? (
            <Badge tone={environment.tone ?? "neutral"} title={environment.hint}>
              {environment.label}
            </Badge>
          ) : null}
          {note ? <span className="text-fg-muted">{note}</span> : null}
          {actions}
        </div>
      ) : null}
    </div>
  );
}
