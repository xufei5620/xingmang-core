import { Input } from "@xingmang/ui-primitives";
import type { PeriodBody, PeriodGranularity } from "../api/users";
import { describePeriod, GRANULARITY_OPTIONS } from "../lib/period";

/** 「统计区间」控件：一个日期 + 日/周/月三个按钮（原型 `periodControls()`）。
 *
 *  ## 为什么右边显示的是**服务端回显**的区间，而不是本地拼的
 *
 *  「这一周是哪七天」由服务端算（周从周一起、月按自然月、业务日按 CST +08:00）。
 *  前端自己再算一遍，两份实现在跨月那一周对不上的那天没人说得清哪个是对的——
 *  而它们的产出都是一组合理日期，界面上分辨不出来。
 *
 *  所以 `period` 是**服务端回传的那个**：在第一次取数落地前它是 undefined，
 *  这时显示「—」而不是拿本地日期顶上（宪法 12 条：不拿一个编出来的值占位）。
 *
 *  ## 为什么日期框不预填「今天」
 *
 *  空的日期框 = 「用服务端的今天」。预填浏览器本地的今天，在 UTC-5 的机器上
 *  会填进账面上的**昨天**，而那一天的数字同样合理，没人会怀疑（宪法 14 条）。 */
export function PeriodControls({
  day,
  granularity,
  period,
  onDayChange,
  onGranularityChange,
}: {
  /** 已选业务日；空串 = 跟随服务端的今天。 */
  day: string;
  granularity: PeriodGranularity;
  /** 服务端回显的区间；还没取到时传 undefined。 */
  period: PeriodBody | undefined;
  onDayChange: (next: string) => void;
  onGranularityChange: (next: PeriodGranularity) => void;
}) {
  return (
    <div className="rounded-lg border border-edge bg-surface p-3">
      <div className="flex flex-wrap items-center gap-2">
        <div className="mr-auto">
          <b className="text-sm font-semibold text-fg">统计区间</b>{" "}
          <span className="text-xs text-fg-muted">
            {describePeriod(period)}
          </span>
        </div>

        <label className="flex items-center gap-1.5 text-xs text-fg-muted">
          <span>日期</span>
          <Input
            type="date"
            aria-label="统计区间的日期"
            value={day}
            onChange={(event) => onDayChange(event.target.value)}
            className="w-40"
          />
        </label>

        {/* 空日期框是有意义的一档（=跟随服务端的今天），所以给一个显式的清除入口。
            没有它的话，选过一次日期就再也回不到「今天」了——而人分不清
            「我选的 2026-08-27」和「今天恰好是 2026-08-27」 */}
        {day === "" ? null : (
          <button
            type="button"
            onClick={() => onDayChange("")}
            className="rounded-md border border-edge px-2 py-1 text-xs text-fg-muted hover:bg-surface-muted"
          >
            回到今天
          </button>
        )}

        <div role="group" aria-label="统计粒度" className="flex gap-1">
          {GRANULARITY_OPTIONS.map((option) => {
            const active = option.value === granularity;
            return (
              <button
                key={option.value}
                type="button"
                // aria-pressed 而不是只把选中态画成颜色：读屏用户要能听出
                // 当前是按哪个粒度看的（与 OverviewPage 的筛选片同一口径）
                aria-pressed={active}
                onClick={() => onGranularityChange(option.value)}
                className={
                  active
                    ? "rounded-md border border-accent bg-accent-soft px-3 py-1 text-xs font-medium text-accent"
                    : "rounded-md border border-edge px-3 py-1 text-xs text-fg-muted hover:bg-surface-muted"
                }
              >
                {option.label}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}
