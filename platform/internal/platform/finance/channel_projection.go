package finance

import (
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

type PlatformChannel struct {
	Channel           ChannelRef
	Name              string
	UpstreamAccountID uuid.UUID
}

type ProfitFact struct {
	BusinessDay       time.Time
	BusinessDayTZ     string
	UpstreamAccountID uuid.UUID
	AccountID         string
	PlatformID        string
	RevenueMinor      *int64
	CostMinor         *int64
	Currency          string
}

type ChannelEconomicsConflict string

const (
	ConflictDuplicateRevenue   ChannelEconomicsConflict = "duplicate_revenue"
	ConflictUpstreamMismatch   ChannelEconomicsConflict = "upstream_mismatch"
	ConflictMixedCurrency      ChannelEconomicsConflict = "mixed_currency"
	ConflictBindingIntervalGap ChannelEconomicsConflict = "binding_interval_gap"
)

type ChannelEconomics struct {
	RevenueMinor     *int64
	CostMinor        *int64
	GrossProfitMinor *int64
	GrossMargin      string
	Currency         string
	RowCount         int64
	RevenueKnownRows int64
	CostKnownRows    int64
}

type SharedRunway struct {
	UpstreamAccountID uuid.UUID
	Days              *int
	BalanceMinor      *int64
	Currency          string
}

type PlatformChannelProjection struct {
	Channel            ChannelRef
	Name               string
	UpstreamAccountID  uuid.UUID
	Binding            *PlatformChannelBinding
	Economics          *ChannelEconomics
	Conflict           ChannelEconomicsConflict
	SharedRunway       *SharedRunway
	SharedChannelCount int
}

type ChannelRunwayCoverage struct {
	Total int
	Known int
}

type PlatformChannelPage struct {
	Items          []PlatformChannelProjection
	RunwayCoverage ChannelRunwayCoverage
}

func dayEnd(day time.Time) time.Time {
	return time.Date(day.UTC().Year(), day.UTC().Month(), day.UTC().Day(), 23, 59, 59, 999999999, time.UTC)
}

func bindingCoversDay(binding PlatformChannelBinding, day time.Time) bool {
	start := time.Date(day.UTC().Year(), day.UTC().Month(), day.UTC().Day(), 0, 0, 0, 0, time.UTC)
	if binding.ValidFrom.After(start) {
		return false
	}
	return binding.ValidTo == nil || binding.ValidTo.After(dayEnd(day))
}

func ProjectChannel(
	channel PlatformChannel,
	bindings []PlatformChannelBinding,
	facts []ProfitFact,
	runway *SharedRunway,
) PlatformChannelProjection {
	projection := PlatformChannelProjection{
		Channel: channel.Channel, Name: channel.Name, UpstreamAccountID: channel.UpstreamAccountID,
		SharedRunway: runway,
	}
	for i := range bindings {
		if bindings[i].Channel == channel.Channel && bindings[i].ValidTo == nil {
			binding := bindings[i]
			projection.Binding = &binding
			break
		}
	}
	if projection.Binding == nil {
		return projection
	}
	byDay := map[string][]ProfitFact{}
	for _, fact := range facts {
		if fact.UpstreamAccountID != projection.Binding.UpstreamAccountID {
			projection.Conflict = ConflictUpstreamMismatch
			return projection
		}
		if !bindingCoversDay(*projection.Binding, fact.BusinessDay) {
			projection.Conflict = ConflictBindingIntervalGap
			return projection
		}
		key := fact.BusinessDay.UTC().Format(ProfitBusinessDayLayout)
		byDay[key] = append(byDay[key], fact)
	}
	if len(byDay) == 0 {
		return projection
	}
	var revenue, cost int64
	var revenueKnown, costKnown, rowCount int64
	currency := ""
	for _, dayFacts := range byDay {
		rowCount += int64(len(dayFacts))
		var dayRevenue *int64
		for _, fact := range dayFacts {
			if fact.Currency != "" && currency == "" {
				currency = fact.Currency
			} else if fact.Currency != "" && fact.Currency != currency {
				projection.Conflict = ConflictMixedCurrency
				return projection
			}
			if fact.RevenueMinor != nil {
				revenueKnown++
				if dayRevenue != nil {
					projection.Conflict = ConflictDuplicateRevenue
					return projection
				}
				value := *fact.RevenueMinor
				dayRevenue = &value
			}
			if fact.CostMinor != nil {
				costKnown++
				cost += *fact.CostMinor
			}
		}
		if dayRevenue != nil {
			revenue += *dayRevenue
		}
	}
	if revenueKnown != int64(len(byDay)) || costKnown != rowCount || currency == "" {
		return projection
	}
	profit := revenue - cost
	margin := ""
	if revenue > 0 {
		margin, _ = money.RatioString(profit, revenue, MarginScale)
	}
	projection.Economics = &ChannelEconomics{
		RevenueMinor: &revenue, CostMinor: &cost, GrossProfitMinor: &profit,
		GrossMargin: margin, Currency: currency, RowCount: rowCount,
		RevenueKnownRows: revenueKnown, CostKnownRows: costKnown,
	}
	return projection
}

func ProjectChannelPage(items []PlatformChannelProjection, runways []SharedRunway) PlatformChannelPage {
	counts := map[uuid.UUID]int{}
	for _, item := range items {
		if item.UpstreamAccountID != uuid.Nil {
			counts[item.UpstreamAccountID]++
		}
	}
	runwayByAccount := map[uuid.UUID]SharedRunway{}
	for _, runway := range runways {
		runwayByAccount[runway.UpstreamAccountID] = runway
	}
	for i := range items {
		items[i].SharedChannelCount = counts[items[i].UpstreamAccountID]
		if runway, ok := runwayByAccount[items[i].UpstreamAccountID]; ok {
			r := runway
			items[i].SharedRunway = &r
		}
	}
	accounts := map[uuid.UUID]struct{}{}
	for _, item := range items {
		if item.SharedRunway != nil && item.SharedRunway.Days != nil {
			accounts[item.UpstreamAccountID] = struct{}{}
		}
	}
	return PlatformChannelPage{Items: items, RunwayCoverage: ChannelRunwayCoverage{Total: len(counts), Known: len(accounts)}}
}

func sortProjections(items []PlatformChannelProjection) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Channel.ServiceID != items[j].Channel.ServiceID {
			return items[i].Channel.ServiceID.String() < items[j].Channel.ServiceID.String()
		}
		return items[i].Channel.ExternalChannelID < items[j].Channel.ExternalChannelID
	})
}
