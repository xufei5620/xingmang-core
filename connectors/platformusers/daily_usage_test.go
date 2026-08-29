package platformusers

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDailyUsageDistinguishesZeroFromMissingDay(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC) }
	series, err := (&FakeClient{source: SourceSub2API, now: clock}).DailyUsage(context.Background(), DailyUsageQuery{
		Ref: UserRef{Platform: SourceSub2API, ID: "u_10241"}, Day: "2026-08-27", Days: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Points) != 7 || series.Coverage.ExpectedDays != 7 {
		t.Fatalf("series=%+v", series)
	}
	if !series.Points[0].Consumed.Known || series.Points[0].Consumed.MinorUnits != 0 {
		t.Fatal("known zero must remain zero")
	}
	if series.Points[1].Consumed.Known || series.Coverage.Complete || series.Coverage.CoveredDays != 6 {
		t.Fatalf("missing day coverage=%+v", series.Coverage)
	}
}

func TestDailyUsageUnknownUserIsNotSynthesized(t *testing.T) {
	c := NewFakeClient(SourceSub2API, nil)
	_, err := c.DailyUsage(context.Background(), DailyUsageQuery{Ref: UserRef{Platform: SourceSub2API, ID: "u_missing"}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user should be not found, err=%v", err)
	}
}

func TestDailyUsageUnknownFlowStaysUnknown(t *testing.T) {
	c := NewFakeClient(SourceSub2API, nil)
	series, err := c.DailyUsage(context.Background(), DailyUsageQuery{Ref: UserRef{Platform: SourceSub2API, ID: "u_10221"}, Days: 7})
	if err != nil {
		t.Fatal(err)
	}
	if series.Coverage.CoveredDays != 0 || series.Coverage.Complete {
		t.Fatalf("unknown-flow coverage=%+v", series.Coverage)
	}
	for _, point := range series.Points {
		if point.Consumed.Known || point.Requests.Known {
			t.Fatalf("unknown-flow point synthesized: %+v", point)
		}
	}
}

func TestDailyUsageWindowAndCSTDefault(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 8, 27, 16, 30, 0, 0, time.UTC) }
	c := &FakeClient{source: SourceSub2API, now: clock}
	for _, days := range []int{-1, 32} {
		if _, err := c.DailyUsage(context.Background(), DailyUsageQuery{Ref: UserRef{Platform: SourceSub2API, ID: "u_10241"}, Days: days}); err == nil {
			t.Fatalf("days=%d accepted", days)
		}
	}
	series, err := c.DailyUsage(context.Background(), DailyUsageQuery{Ref: UserRef{Platform: SourceSub2API, ID: "u_10241"}})
	if err != nil || series.To != "2026-08-28" || series.Coverage.ExpectedDays != 7 {
		t.Fatalf("series=%+v err=%v", series, err)
	}
}
