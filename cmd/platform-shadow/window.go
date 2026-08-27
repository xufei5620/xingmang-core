package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/shadow"
)

// 区间与容差的解析。单独成文件是为了能不连库就测——它们是两处最容易
// 悄悄错掉的地方：算错一天会让报告漏掉或多出一整天的数据，
// 而容差解析错会让「严格 0」变成别的东西。

// cstOffsetSeconds 是业务日时区：CST 固定 +08:00，无夏令时。
//
// ★ 口径常量（设计稿 §4，与 SoloAI 的 cstNow 一致）。默认区间按它算，
// 否则「昨天」在 UTC 与 CST 下会是不同的一天，14 天窗口会整体错位。
const cstOffsetSeconds = 8 * 3600

// shadowWindow 是解析后的业务日闭区间。
type shadowWindow struct {
	from time.Time
	to   time.Time
}

// defaultWindowDays 是默认窗口长度：14 天。
//
// 直接对上规格 §22.3 的「连续 14 个自然日」——不带参数跑一次就是那个窗口，
// 省得每天有人手算日期（算错一天，验收就少一天）。
const defaultWindowDays = 14

// resolveWindow 解析业务日区间。
//
// 两个都省略 → [昨天-13, 昨天]，即规格 §22.3 的 14 天窗口。
// 为什么默认到**昨天**而不是今天：今天的行还在被采集任务反复覆盖
// （§5.3「今日可覆盖、过去冻结」），拿它对比会得到一个随时间变化的结论——
// 早上跑是差异、下午跑又对上了，那种报告没法作为验收证据。
//
// 只给 to → 从 to 往前推满窗口；只给 from → 到昨天为止。
func resolveWindow(fromText, toText string, now func() time.Time) (shadowWindow, error) {
	cst := time.FixedZone("CST", cstOffsetSeconds)
	yesterday := now().In(cst).AddDate(0, 0, -1)
	yesterday = time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, cst)

	to := yesterday
	if s := strings.TrimSpace(toText); s != "" {
		parsed, err := parseDay(s, cst)
		if err != nil {
			return shadowWindow{}, fmt.Errorf("-to: %w", err)
		}
		to = parsed
	}

	from := to.AddDate(0, 0, -(defaultWindowDays - 1))
	if s := strings.TrimSpace(fromText); s != "" {
		parsed, err := parseDay(s, cst)
		if err != nil {
			return shadowWindow{}, fmt.Errorf("-from: %w", err)
		}
		from = parsed
	}

	if from.After(to) {
		return shadowWindow{}, fmt.Errorf("起始日 %s 晚于结束日 %s",
			from.Format(shadow.DayLayout), to.Format(shadow.DayLayout))
	}
	return shadowWindow{from: from, to: to}, nil
}

// parseDay 严格解析 YYYY-MM-DD。
//
// time.Parse 对 "2026-8-1" 是宽容的，但业务日一旦有两种写法，
// 归档文件名与报告里的日期就会出现两种形态，14 天的文件排序也跟着乱。
func parseDay(s string, loc *time.Location) (time.Time, error) {
	parsed, err := time.ParseInLocation(shadow.DayLayout, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("业务日 %q 须形如 %s", s, shadow.DayLayout)
	}
	if parsed.Format(shadow.DayLayout) != s {
		return time.Time{}, fmt.Errorf("业务日 %q 必须严格为 %s（不接受少位写法）", s, shadow.DayLayout)
	}
	return parsed, nil
}

// resolveTolerance 解析容差旋钮，单位**分**。
//
// 空 → 0（严格），这是 §12 的拍板结论：「影子对比严格 0 差异 + 容差旋钮默认关」。
//
// 负数报错而不是取绝对值：一个写了负号的容差说明写的人搞错了方向，
// 静默纠正会让那个误解留在配置里。非整数同样报错——分是最小单位，
// 「0.5 分」这个要求本身就说明口径没想清楚。
func resolveTolerance(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("须为整数分（严格模式填 0 或留空），got %q", raw)
	}
	if v < 0 {
		return 0, fmt.Errorf("不能为负，got %d", v)
	}
	return v, nil
}
