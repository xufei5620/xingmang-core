package finance

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func projectionChannel(id string, account uuid.UUID) PlatformChannel {
	return PlatformChannel{Channel: ChannelRef{ServiceID: uuid.New(), ExternalChannelID: id}, Name: id, UpstreamAccountID: account}
}

func projectionBinding(channel PlatformChannel, account uuid.UUID) PlatformChannelBinding {
	return PlatformChannelBinding{ID: uuid.New(), Environment: "staging", Channel: channel.Channel, UpstreamAccountID: account, ValidFrom: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)}
}

func projectionFact(day string, account uuid.UUID, revenue, cost *int64) ProfitFact {
	parsed, _ := time.Parse(ProfitBusinessDayLayout, day)
	return ProfitFact{BusinessDay: parsed, UpstreamAccountID: account, AccountID: "channel-1", RevenueMinor: revenue, CostMinor: cost, Currency: "CNY", BusinessDayTZ: "+08:00", PlatformID: "newapi"}
}

func TestProjectionFailsClosedOnDuplicateRevenue(t *testing.T) {
	account := uuid.New()
	channel := projectionChannel("channel-1", account)
	binding := projectionBinding(channel, account)
	a, b := int64(100), int64(100)
	got := ProjectChannel(channel, []PlatformChannelBinding{binding}, []ProfitFact{
		projectionFact("2026-08-28", account, &a, nil),
		projectionFact("2026-08-28", account, &b, nil),
	}, nil)
	if got.Conflict != ConflictDuplicateRevenue || got.Economics != nil {
		t.Fatalf("projection=%+v; duplicate revenue must fail closed", got)
	}
}

func TestProjectionSumsMultipleTokenCostsAgainstOneRevenue(t *testing.T) {
	account := uuid.New()
	channel := projectionChannel("channel-1", account)
	binding := projectionBinding(channel, account)
	revenue, costA, costB := int64(100), int64(30), int64(20)
	got := ProjectChannel(channel, []PlatformChannelBinding{binding}, []ProfitFact{
		projectionFact("2026-08-28", account, &revenue, &costA),
		projectionFact("2026-08-28", account, nil, &costB),
	}, nil)
	if got.Economics == nil || *got.Economics.RevenueMinor != 100 || *got.Economics.CostMinor != 50 || *got.Economics.GrossProfitMinor != 50 {
		t.Fatalf("projection=%+v", got)
	}
}

func TestProjectionFailsClosedOnMixedCurrencyAndUpstreamMismatch(t *testing.T) {
	account, other := uuid.New(), uuid.New()
	channel := projectionChannel("channel-1", account)
	binding := projectionBinding(channel, account)
	revenue, cost := int64(100), int64(20)
	mixed := projectionFact("2026-08-28", account, &revenue, &cost)
	mixed.Currency = "USD"
	got := ProjectChannel(channel, []PlatformChannelBinding{binding}, []ProfitFact{projectionFact("2026-08-28", account, &revenue, &cost), mixed}, nil)
	if got.Conflict != ConflictMixedCurrency || got.Economics != nil {
		t.Fatalf("mixed projection=%+v", got)
	}
	bad := projectionFact("2026-08-28", other, &revenue, &cost)
	got = ProjectChannel(channel, []PlatformChannelBinding{binding}, []ProfitFact{bad}, nil)
	if got.Conflict != ConflictUpstreamMismatch || got.Economics != nil {
		t.Fatalf("mismatch projection=%+v", got)
	}
}

func TestSharedRunwayTotalsDistinctAccounts(t *testing.T) {
	account := uuid.New()
	serviceID := uuid.New()
	channels := []PlatformChannelProjection{
		{Channel: ChannelRef{ServiceID: serviceID, ExternalChannelID: "1"}, UpstreamAccountID: account},
		{Channel: ChannelRef{ServiceID: serviceID, ExternalChannelID: "2"}, UpstreamAccountID: account},
	}
	page := ProjectChannelPage(channels, []SharedRunway{{UpstreamAccountID: account, Days: intPtr(12)}})
	if len(page.Items) != 2 || page.RunwayCoverage.Total != 1 {
		t.Fatalf("items=%d coverage=%+v", len(page.Items), page.RunwayCoverage)
	}
	for _, item := range page.Items {
		if item.SharedChannelCount != 2 {
			t.Fatalf("shared count=%d", item.SharedChannelCount)
		}
	}
}

func intPtr(value int) *int { return &value }
