import { useEffect, useRef } from "react";

/** 总览页的自动刷新间隔。
 *
 *  60 秒对齐 Sub2API 采集任务的量级：拉得更勤也只会拿回同一批观测，
 *  白白给后端加压，而卡片上的「数据时间」本来就会告诉人数据有多旧。 */
export const OVERVIEW_POLL_INTERVAL_MS = 60_000;

/** 当前该不该发起一次轮询拉取。
 *
 *  抽成纯函数是为了能直接断言这条规则本身：**页面不可见时一律不拉**。
 *  后台标签页每分钟打一次请求，攒够几十个标签页就是一场自己造的压测，
 *  而没人在看的界面刷新出来的数据谁也没看见。 */
export function shouldPoll(visibility: DocumentVisibilityState | undefined): boolean {
  return visibility === "visible";
}

/** 页面可见时按 intervalMs 定时回调；不可见时静默跳过，重新可见时立刻补一次。
 *
 *  「重新可见立刻补一次」是刻意的：人切回标签页时看到的第一眼必须是新数据，
 *  否则他要盯着一个最多旧 60 秒的界面做判断，还不知道它旧。
 *
 *  intervalMs <= 0 表示关掉自动刷新（手动刷新按钮仍然可用）。 */
export function useAutoRefresh(
  onRefresh: () => void,
  intervalMs: number = OVERVIEW_POLL_INTERVAL_MS,
): void {
  // 用 ref 存回调：onRefresh 每次渲染都是新函数，直接进依赖数组会让
  // 定时器每渲染一次就重建一次，于是永远等不到 60 秒
  const callbackRef = useRef(onRefresh);
  useEffect(() => {
    callbackRef.current = onRefresh;
  }, [onRefresh]);

  useEffect(() => {
    if (intervalMs <= 0) return;

    const fireIfVisible = () => {
      if (shouldPoll(document.visibilityState)) callbackRef.current();
    };
    const timer = setInterval(fireIfVisible, intervalMs);
    document.addEventListener("visibilitychange", fireIfVisible);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", fireIfVisible);
    };
  }, [intervalMs]);
}
