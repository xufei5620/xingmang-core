package sub2api

import (
	"testing"
	"time"
)

func TestManagedChannelBalanceIsNullableAndLegacyOmitsOnlyNil(t *testing.T) {
	value := int64(0)
	reported := int64(2)
	directory := ManagedChannelDirectory{
		Snapshot:        Snapshot{ObservedAt: time.Unix(10, 0), Watermark: "wm"},
		Completeness:    DirectoryCompleteness{Complete: true, ReportedCount: &reported, FetchedCount: 2},
		CoveragePartial: true,
		Items: []ManagedChannel{
			{ChannelID: "zero", Status: "active", BalanceMinorUnits: &value, Currency: "CNY"},
			{ChannelID: "subscription", Status: "active", BalanceMinorUnits: nil, Currency: "CNY"},
		},
	}
	legacy := LegacyChannelBalances(directory)
	if len(legacy) != 1 || legacy[0].BalanceMinorUnits != 0 || !legacy[0].IsPartial {
		t.Fatalf("legacy=%+v", legacy)
	}
}

// ---------------------------------------------------------------------------
// XM-CHAN-FIELDS0：白盒测试（未导出的分类/编码函数，只能从包内测）
// ---------------------------------------------------------------------------

func TestClassifyAccountKind(t *testing.T) {
	cases := map[string]*string{
		"oauth":               strPtr("subscription"),
		"setup-token":         strPtr("subscription"),
		"apikey":              strPtr("upstream"),
		"upstream":            strPtr("upstream"),
		"bedrock":             strPtr("upstream"),
		"service_account":     strPtr("upstream"),
		"":                    nil,
		"cookie":              nil, // ent schema 注释里出现过的历史值，不是真的 Type 取值
		"future-unknown-type": nil,
	}
	for accountType, want := range cases {
		got := classifyAccountKind(accountType)
		switch {
		case want == nil && got != nil:
			t.Fatalf("classifyAccountKind(%q) = %q, want nil", accountType, *got)
		case want != nil && (got == nil || *got != *want):
			t.Fatalf("classifyAccountKind(%q) = %v, want %q", accountType, got, *want)
		}
	}
}

func strPtr(s string) *string { return &s }

// TestCatalogFieldsOmitsAbsentKeysNotNull 锁住既有纪律（channelsObservation
// 的同款注释）：某个维度完全没数据时**不写这个键**，而不是写 JSON null——
// 否则前端要区分「没这个维度」与「这个维度的值恰好是 null」，多一层歧义。
func TestCatalogFieldsOmitsAbsentKeysNotNull(t *testing.T) {
	empty := ManagedChannel{ChannelID: "x", Snapshot: Snapshot{ObservedAt: time.Unix(1, 0)}}
	row := empty.catalogFields()
	for _, key := range []string{
		"kind", "vendor", "capacity", "scheduling", "today", "usage_window",
		"proxy", "rate_multiplier", "upstream_multiplier",
		"last_used_at", "created_at", "expires_at",
	} {
		if _, present := row[key]; present {
			t.Fatalf("空 ManagedChannel 不该写出键 %q: %+v", key, row)
		}
	}

	kind := channelKindSubscription
	used, limit := int64(2), int64(5)
	full := ManagedChannel{
		ChannelID: "y", Kind: &kind, CapacityUsed: &used, CapacityLimit: &limit,
	}
	fullRow := full.catalogFields()
	capacity, ok := fullRow["capacity"].(map[string]any)
	if !ok {
		t.Fatalf("capacity 应该是嵌套 map: %+v", fullRow)
	}
	if capacity["used"] != used || capacity["limit"] != limit {
		t.Fatalf("capacity 嵌套字段 = %+v, want used=%d limit=%d", capacity, used, limit)
	}
	if fullRow["kind"] != channelKindSubscription {
		t.Fatalf("kind = %v, want %q", fullRow["kind"], channelKindSubscription)
	}
	// scheduling/today/usage_window/proxy/rate_multiplier/... 一个都没配，
	// 依旧不该出现——即便同一行的另一些字段配了。
	for _, key := range []string{"scheduling", "today", "usage_window", "proxy", "rate_multiplier"} {
		if _, present := fullRow[key]; present {
			t.Fatalf("未配置的维度 %q 不该出现在部分填充的行里: %+v", key, fullRow)
		}
	}
}

func TestDirectoryObservationKeepsReportedUnknownDistinctFromZero(t *testing.T) {
	unknown := ToChannelDirectoryObservation(time.Unix(20, 0), "s", "staging",
		ManagedChannelDirectory{Completeness: DirectoryCompleteness{Complete: true, ReportedCount: nil}})
	zero := int64(0)
	explicit := ToChannelDirectoryObservation(time.Unix(20, 0), "s", "staging",
		ManagedChannelDirectory{Completeness: DirectoryCompleteness{Complete: true, ReportedCount: &zero}})
	u := unknown.Value["inventory_completeness"].(map[string]any)["reported_count"]
	z := explicit.Value["inventory_completeness"].(map[string]any)["reported_count"]
	if u != (*int64)(nil) || z == nil || *(z.(*int64)) != 0 {
		t.Fatalf("unknown=%#v zero=%#v", u, z)
	}
}
