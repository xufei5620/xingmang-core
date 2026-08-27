import { PageHeader } from "@xingmang/ui-admin";
import { EmptyState } from "@xingmang/ui-primitives";
import { Link, useLocation } from "react-router";

export interface NotFoundViewProps {
  /** 没找到的地址。原样回显——把它藏起来，人就没法判断是自己贴错了还是链接过期。 */
  pathname: string;
  /** 更具体的一句话（例如「这个平台没有名为 xxx 的页签」）。 */
  detail?: string;
}

/** Not Found 的正文。
 *
 *  单独抽出来是因为它有两个入口：未匹配的路径（splat 路由）与未知的
 *  `?tab=`/`?sub=`（ErrorBoundary 接住的 404）。两处必须长得一样，
 *  否则「这个地址不对」在屏幕上会有两种说法。 */
export function NotFoundView({ pathname, detail }: NotFoundViewProps) {
  return (
    <section>
      <PageHeader title="页面不存在" />
      <EmptyState
        title={detail ?? "没有这个地址"}
        description={`${pathname} 不对应任何页面。地址可能已过期、拼写有误，或者这一页还没建。导航是完整的：左侧列出了全部页面，已建的可以点进去，未建的标着「未建」。`}
        action={
          <Link to="/dashboard" className="text-xs font-medium text-accent hover:underline">
            回到运营工作台
          </Link>
        }
      />
    </section>
  );
}

/** splat(`*`)路由落点。
 *
 *  在 XM-0042 之前 router.tsx 既没有 splat 也没有 errorElement，任何没匹配上的
 *  旧书签会落到 react-router 的默认错误页——一屏英文堆栈，既不说这是 404,
 *  也回不去。ADMIN-IA v3 §四把「改名必须加 redirect、旧书签不许失效」列为硬性
 *  要求，而兜底 404 是这条要求的最后一格：redirect 表漏了哪条，人至少知道
 *  自己在哪、能走回去。 */
export function NotFoundPage() {
  const { pathname, search } = useLocation();
  return <NotFoundView pathname={`${pathname}${search}`} />;
}
