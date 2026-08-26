import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider } from "react-router/dom";
import { ApiError } from "./api/client";
import "./index.css";
import { createAppRouter } from "./router";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // 权限/参数类失败重试多少次都是同一个答案，只会刷服务端日志；
      // 只有网络不通和 5xx 值得再试一次
      retry: (failureCount, error) =>
        failureCount < 1 && error instanceof ApiError && error.retryable,
      refetchOnWindowFocus: false,
      // 看板数据本身就带「数据时间」，再叠一层前端缓存会让人分不清旧的是哪一层，
      // 所以缓存窗口只留很短一段，够抵消切页面时的重复请求
      staleTime: 15_000,
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={createAppRouter()} />
    </QueryClientProvider>
  </StrictMode>,
);
