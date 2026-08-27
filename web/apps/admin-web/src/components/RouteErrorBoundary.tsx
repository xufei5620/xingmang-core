import { PageHeader, PageState } from "@xingmang/ui-admin";
import { isRouteErrorResponse, useLocation, useRouteError } from "react-router";
import { NotFoundView } from "../pages/NotFoundPage";

/** 页面级错误兜底。
 *
 *  挂在一个无路径的布局路由上，于是它渲染在 AdminShell **内部**：导航、面包屑、
 *  演示数据横幅都还在。挂到外层壳上的话，一次 404 会把整个左栏一起吃掉——人
 *  连「回哪去」都没得选，而这正是最需要导航在场的时刻。
 *
 *  404 与「真出错了」分开显示：前者是地址的问题（贴错、书签过期、还没建），
 *  后者是代码的问题。合成一句「出错了」会让人去重试一个永远不会成功的地址。 */
export function RouteErrorBoundary() {
  const error = useRouteError();
  const { pathname, search } = useLocation();

  if (isRouteErrorResponse(error) && error.status === 404) {
    // 抛 404 的地方（见 router 的页签解析）把原因写在响应体里，
    // 例如「sub2api 没有名为 拼错了 的页签」。React Router 把它放进 data
    const detail = typeof error.data === "string" && error.data ? error.data : undefined;
    return <NotFoundView pathname={`${pathname}${search}`} detail={detail} />;
  }

  const message =
    error instanceof Error
      ? error.message
      : isRouteErrorResponse(error)
        ? `${error.status} ${error.statusText}`
        : "未知错误";

  return (
    <section>
      <PageHeader title="这一页出错了" />
      {/* 不提供「重试」：这一类错误是渲染时抛出来的，重试同一个地址会再抛一次。
          能做的是把错误原样给出来，好让人贴给我们 */}
      <PageState kind="error" message={message} />
    </section>
  );
}
