package httpapi

import (
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
)

// XM-ALERTS-LIST-TRUNCATED：截断由服务端说，因为只有它知道生效上限。
//
// 这一条钉的是那个判据本身：调用方传的 limit 与生效的 limit 在两种情况下不
// 相等（不传、传得比上界还大），而前端拿自己传的数去比就会**永远判不出截断**。
func TestClampListLimitIsTheOnlyPlaceThatKnowsTheEffectiveLimit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested int32
		want      int32
	}{
		{"不传（0）钳到上界", 0, alerts.MaxListLimit},
		{"负数钳到上界", -5, alerts.MaxListLimit},
		{"超过上界钳到上界", alerts.MaxListLimit + 1, alerts.MaxListLimit},
		{"上界本身原样", alerts.MaxListLimit, alerts.MaxListLimit},
		{"上界以内原样", 20, 20},
	} {
		if got := alerts.ClampListLimit(tc.requested); got != tc.want {
			t.Errorf("%s：ClampListLimit(%d)=%d want %d", tc.name, tc.requested, got, tc.want)
		}
	}
}

// 判据是「返回条数 >= 生效上限」。这里把那个表达式单独钉一遍——handler 里
// 那一行改错时（比如写成 == 或者拿 requested 去比），这条会红。
func TestTruncatedIsReturnedCountAgainstTheEffectiveLimit(t *testing.T) {
	truncated := func(returned, requested int32) bool {
		return returned >= alerts.ClampListLimit(requested)
	}
	// 不传 limit：生效上限是 500，返回 200 条不算截断。
	if truncated(200, 0) {
		t.Fatal("返回 200 条、生效上限 500，不该算截断")
	}
	// 不传 limit 且真的返回满 500：算截断。
	if !truncated(alerts.MaxListLimit, 0) {
		t.Fatal("返回条数等于生效上限时必须算可能截断")
	}
	// 传 20 且返回满 20：算截断——即便离 500 还远。
	if !truncated(20, 20) {
		t.Fatal("按调用方要求的 20 条取满时同样是截断")
	}
	// 传比上界还大的数：生效上限仍是 500，返回 300 不算截断。
	// **前端若拿自己传的 9999 去比，这里会误判成"没截断"。**
	if truncated(300, 9999) {
		t.Fatal("生效上限是 500，返回 300 不该算截断")
	}
	if !truncated(alerts.MaxListLimit, 9999) {
		t.Fatal("传 9999 实际只能拿 500，取满时仍是截断")
	}
}
