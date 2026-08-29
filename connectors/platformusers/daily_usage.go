package platformusers

import (
	"context"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

type DailyUsageQuery struct {
	Ref  UserRef
	Day  string
	Days int
}

type DailyUsagePoint struct {
	Day      string
	Consumed Amount
	Requests CountValue
}

type SeriesCoverage struct {
	ExpectedDays int
	CoveredDays  int
	Complete     bool
}

type DailyUsageSeries struct {
	From, To string
	Points   []DailyUsagePoint
	Coverage SeriesCoverage
	Snapshot EvidenceSnapshot
}

type DailyUsageReader interface {
	DailyUsage(context.Context, DailyUsageQuery) (DailyUsageSeries, error)
}

func (c *FakeClient) DailyUsage(ctx context.Context, query DailyUsageQuery) (DailyUsageSeries, error) {
	if err := ctx.Err(); err != nil {
		return DailyUsageSeries{}, err
	}
	if err := query.Ref.Validate(); err != nil || query.Ref.Platform != c.source {
		return DailyUsageSeries{}, connectorBadSource(query.Ref.Platform)
	}
	days := query.Days
	if days == 0 {
		days = 7
	}
	if days < 1 || days > 31 {
		return DailyUsageSeries{}, connectorBadPeriod(ErrInvalidDay)
	}
	now := c.now().UTC()
	day := query.Day
	if day == "" {
		day = now.In(DefaultBusinessDayLocation()).Format(BusinessDayLayout)
	}
	if err := ValidateBusinessDay(day); err != nil {
		return DailyUsageSeries{}, connectorBadPeriod(err)
	}
	anchor, _ := time.ParseInLocation(BusinessDayLayout, day, DefaultBusinessDayLocation())
	from := anchor.AddDate(0, 0, -(days - 1))
	points := make([]DailyUsagePoint, 0, days)
	covered := 0
	for i := 0; i < days; i++ {
		pointDay := from.AddDate(0, 0, i).Format(BusinessDayLayout)
		// Fake 保留一个明确的“已知零”和一个“未知日”，让 UI 永远能演示
		// 0 与缺失不是一回事；其余日期用稳定的样本基线。
		if i == 0 {
			points = append(points, DailyUsagePoint{Day: pointDay, Consumed: KnownAmount(0, "CNY"), Requests: KnownCount(0)})
			covered++
			continue
		}
		if i == 1 {
			points = append(points, DailyUsagePoint{Day: pointDay, Consumed: UnknownAmount(), Requests: UnknownCount()})
			continue
		}
		base := int64(31200)
		for _, raw := range fakeUsers {
			if raw.id == query.Ref.ID {
				base = raw.dayConsumed
				break
			}
		}
		weight := int64(70 + ((from.YearDay() + i*13) % 61))
		points = append(points, DailyUsagePoint{Day: pointDay, Consumed: KnownAmount(base*weight/100, "CNY"), Requests: KnownCount(base/100 + int64(i))})
		covered++
	}
	return DailyUsageSeries{From: from.Format(BusinessDayLayout), To: day, Points: points,
		Coverage: SeriesCoverage{ExpectedDays: days, CoveredDays: covered, Complete: covered == days},
		Snapshot: EvidenceSnapshot{ObservedAt: now, Source: c.source + "-fake", Watermark: "wm-fake-daily-" + now.Format("20060102T150405Z")}}, nil
}

func (c *FakeClient) V2DailyCapabilities() []registry.Capability {
	return []registry.Capability{CapabilityUserDetailRead, CapabilityUserDailyUsageRead}
}
