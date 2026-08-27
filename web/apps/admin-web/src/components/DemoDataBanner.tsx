import { useQuery } from "@tanstack/react-query";
import { listMetrics } from "../api/platform";
import { appDemoDataConfig, DEMO_BANNER_TEXT, shouldShowDemoBanner } from "../lib/demoData";

/** 演示数据全局横幅（Codex #8）。
 *
 *  三个刻意的选择：
 *  1. **不可关闭**：没有关闭按钮、不记 localStorage。可关闭的警告等于「点一次
 *     就永远看不见的警告」，而这条警告要在人做决定的**每一次**都在场。
 *  2. **走指标 source 判定**，不是构建期开关：开关只能说明我们以为在哪，
 *     source 才说明画出来的这批数字实际来自哪（详见 lib/demoData）。
 *  3. **判不出来就不挂**：指标还没回来、请求失败时保持沉默。对着一个连数据
 *     都没有的空页面喊「这是演示数据」除了制造噪音没有别的作用。
 *
 *  这里复用 ['metrics'] 这个 query key：总览/渠道/服务页本来就要拉它，
 *  横幅因此在大多数页面上不产生额外请求。 */
export function DemoDataBanner() {
  const query = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
    retry: false,
  });

  const sources = (query.data ?? []).map((m) => m.source);
  if (!shouldShowDemoBanner(sources, appDemoDataConfig)) return null;

  return (
    <div
      // status 而不是 alert：它常驻页面顶部，用 alert 会在每次路由切换时
      // 打断读屏用户正在听的内容
      role="status"
      className="flex items-center justify-center gap-2 border-b-2 border-warning bg-warning/15 px-4 py-2 text-center text-sm font-semibold text-warning"
    >
      {/* 图形与文字同时给：只靠一条黄色横条的人，和只读文字的人，都要接得住 */}
      <span aria-hidden="true">⚠</span>
      <span>{DEMO_BANNER_TEXT}</span>
    </div>
  );
}
