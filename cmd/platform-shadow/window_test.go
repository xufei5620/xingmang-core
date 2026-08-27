package main

import (
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/shadow"
)

// 固定时钟。跟着真实时钟走的话，这套测试会在某个未来的日子突然变红，
// 而且红得毫无道理。CST 2026-08-28 14:00（= UTC 06:00）。
var shadowClock = func() time.Time { return time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC) }

func day(w shadowWindow) (string, string) {
	return w.from.Format(shadow.DayLayout), w.to.Format(shadow.DayLayout)
}

// TestResolveWindowDefaultsToFourteenDaysEndingYesterday。
//
// 两条判据各有实打实的后果：
//   - **14 天**直接对上规格 §22.3 的窗口，省得每天有人手算日期（算错一天，
//     验收就少一天）；
//   - **到昨天为止**是因为今天的行还在被采集任务反复覆盖（§5.3），
//     拿它对比会得到一个随时间变化的结论——早上跑是差异、下午跑又对上了，
//     那种报告作不了验收证据。
func TestResolveWindowDefaultsToFourteenDaysEndingYesterday(t *testing.T) {
	w, err := resolveWindow("", "", shadowClock)
	if err != nil {
		t.Fatal(err)
	}
	from, to := day(w)
	if to != "2026-08-27" {
		t.Fatalf("默认结束日 = %s, want 2026-08-27（昨天，CST）", to)
	}
	if from != "2026-08-14" {
		t.Fatalf("默认起始日 = %s, want 2026-08-14（凑满 14 天）", from)
	}
	// 闭区间共 14 天。
	if got := int(w.to.Sub(w.from).Hours()/24) + 1; got != defaultWindowDays {
		t.Fatalf("窗口长度 = %d 天, want %d", got, defaultWindowDays)
	}
}

// TestResolveWindowUsesCSTNotUTC：「昨天」按 CST 算。
//
// UTC 00:30 时，CST 已经是第二天 08:30——两者的「昨天」差一整天。
// 按 UTC 算会让整个 14 天窗口错位一天，而报告不会有任何异常表现。
func TestResolveWindowUsesCSTNotUTC(t *testing.T) {
	// UTC 2026-08-28 00:30 = CST 2026-08-28 08:30 → 昨天是 CST 的 08-27。
	clock := func() time.Time { return time.Date(2026, 8, 28, 0, 30, 0, 0, time.UTC) }
	w, err := resolveWindow("", "", clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, to := day(w); to != "2026-08-27" {
		t.Fatalf("结束日 = %s, want 2026-08-27（CST 的昨天）", to)
	}

	// UTC 2026-08-27 17:00 = CST 2026-08-28 01:00 → 昨天同样是 08-27。
	// 若按 UTC 算，「昨天」会是 08-26，整个窗口错位一天。
	clock = func() time.Time { return time.Date(2026, 8, 27, 17, 0, 0, 0, time.UTC) }
	w, err = resolveWindow("", "", clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, to := day(w); to != "2026-08-27" {
		t.Fatalf("结束日 = %s, want 2026-08-27——按 UTC 算会得到 08-26，整窗错位一天", to)
	}
}

func TestResolveWindowExplicitBounds(t *testing.T) {
	w, err := resolveWindow("2026-08-01", "2026-08-03", shadowClock)
	if err != nil {
		t.Fatal(err)
	}
	if from, to := day(w); from != "2026-08-01" || to != "2026-08-03" {
		t.Fatalf("区间 = %s..%s", from, to)
	}

	// 只给 to → 往前推满窗口。
	w, err = resolveWindow("", "2026-08-20", shadowClock)
	if err != nil {
		t.Fatal(err)
	}
	if from, to := day(w); from != "2026-08-07" || to != "2026-08-20" {
		t.Fatalf("只给 to 时区间 = %s..%s, want 2026-08-07..2026-08-20", from, to)
	}

	// 只给 from → 到昨天为止。
	w, err = resolveWindow("2026-08-25", "", shadowClock)
	if err != nil {
		t.Fatal(err)
	}
	if from, to := day(w); from != "2026-08-25" || to != "2026-08-27" {
		t.Fatalf("只给 from 时区间 = %s..%s", from, to)
	}
}

// TestResolveWindowRejectsBadInput：宽松解析会让归档文件名出现两种日期形态。
func TestResolveWindowRejectsBadInput(t *testing.T) {
	for _, tc := range []struct{ from, to string }{
		{"2026-8-1", ""},             // 少位写法
		{"", "2026/08/27"},           // 分隔符不对
		{"", "2026-13-01"},           // 非法月份
		{"", "yesterday"},            // 不是日期
		{"2026-08-28", "2026-08-01"}, // 起始晚于结束
	} {
		if _, err := resolveWindow(tc.from, tc.to, shadowClock); err == nil {
			t.Fatalf("from=%q to=%q 应被拒绝", tc.from, tc.to)
		}
	}
}

// TestResolveToleranceDefaultsToStrict 钉住 §12 的拍板：**默认严格 0**。
//
// 这个默认值是本工具的全部意义所在——一个默认容忍 1 分的影子对比，
// 会在 14 天里放过它唯一要找的那类问题。
func TestResolveToleranceDefaultsToStrict(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		got, err := resolveTolerance(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got != 0 {
			t.Fatalf("默认容差 = %d, want 0（§12：严格 0 差异 + 旋钮默认关）", got)
		}
	}
	if got, err := resolveTolerance("1"); err != nil || got != 1 {
		t.Fatalf("显式 1 分 = %d, %v", got, err)
	}
	if got, err := resolveTolerance("0"); err != nil || got != 0 {
		t.Fatalf("显式 0 = %d, %v", got, err)
	}
}

// TestResolveToleranceRejectsBadValues。
//
// 负数不取绝对值、小数不四舍五入：两者都说明写的人搞错了口径，
// 静默纠正会把那个误解留在配置里，然后 14 天都用一个没人理解的容差跑。
func TestResolveToleranceRejectsBadValues(t *testing.T) {
	for _, raw := range []string{"-1", "0.5", "1分", "abc", "1e3"} {
		if _, err := resolveTolerance(raw); err == nil {
			t.Fatalf("%q 应被拒绝", raw)
		}
	}
	if _, err := resolveTolerance("-1"); err == nil || !strings.Contains(err.Error(), "不能为负") {
		t.Fatalf("负数的错误信息应说清楚: %v", err)
	}
}
