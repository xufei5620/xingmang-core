package infini

import (
	"errors"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

const cardJSON = `{
  "id": "card_123",
  "mask": "533228******1234",
  "holder_name": "ZHANG WEI",
  "card_alias": "xm-ops-001",
  "status": "active",
  "currency": "USD",
  "available_balance": "12.34",
  "user_id": "u_9",
  "created_at": "2026-09-01T10:00:00Z",
  "updated_at": "2026-09-02T11:30:00Z"
}`

func TestDecodeCardKeepsMaskAndConvertsBalanceToMinorUnits(t *testing.T) {
	card, err := decodeCard([]byte(cardJSON))
	if err != nil {
		t.Fatal(err)
	}

	// mask 是上游给的掩码卡号，这就是允许落库的那一份（明文只在 reveal 时出现）
	if got, want := card.Mask, "533228******1234"; got != want {
		t.Fatalf("Mask = %q, want %q", got, want)
	}
	// 12.34 USD = 1234 分。全程整数，不经 float（宪法条款 13）
	if got, want := card.BalanceMinor, int64(1234); got != want {
		t.Fatalf("BalanceMinor = %d, want %d", got, want)
	}
	if got, want := card.Alias, "xm-ops-001"; got != want {
		t.Fatalf("Alias = %q, want %q", got, want)
	}
}

// 币种不认识时**报错，不猜 2 位**：猜错的那 100 倍不会有任何症状，
// 只会让所有金额静静地错着。
func TestDecodeCardRejectsUnknownCurrency(t *testing.T) {
	const j = `{"id":"c1","currency":"XYZ","available_balance":"12.34","created_at":"2026-09-01T10:00:00Z","updated_at":"2026-09-01T10:00:00Z"}`

	_, err := decodeCard([]byte(j))
	if err == nil {
		t.Fatal("未知币种必须报错")
	}
	if !errors.Is(err, money.ErrUnknownCurrency) {
		t.Fatalf("错误应包裹 ErrUnknownCurrency, got %v", err)
	}
}

func TestDecodeCardParsesTimestampsAsUTC(t *testing.T) {
	card, err := decodeCard([]byte(cardJSON))
	if err != nil {
		t.Fatal(err)
	}

	want := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC)
	if !card.CreatedAt.Equal(want) {
		t.Fatalf("CreatedAt = %v, want %v", card.CreatedAt, want)
	}
	if card.CreatedAt.Location() != time.UTC {
		t.Fatalf("时间必须转成 UTC 存放（宪法条款 14），got %v", card.CreatedAt.Location())
	}
}

// 时间格式是从文档示例推断的，尚未对真实响应验证过。格式不认识时必须
// 报错而不是落一个零值时间——零值时间会让「数据新鲜度」显示成 1970 年，
// 那是个看得见但没人会去查的错误。
func TestDecodeCardRejectsUnparseableTimestamp(t *testing.T) {
	const j = `{"id":"c1","currency":"USD","available_balance":"1.00","created_at":"09/01/2026 10:00","updated_at":"2026-09-01T10:00:00Z"}`

	if _, err := decodeCard([]byte(j)); err == nil {
		t.Fatal("无法解析的时间格式必须报错，不能静默落零值")
	}
}

// 余额字段缺失与余额为 0 是两回事，但上游对新卡可能不返回该字段。
// 缺失按 0 处理是安全的（卡刚开出来余额本就是 0），但币种缺失不行。
func TestDecodeCardRequiresCurrencyWhenBalancePresent(t *testing.T) {
	const j = `{"id":"c1","available_balance":"12.34","created_at":"2026-09-01T10:00:00Z","updated_at":"2026-09-01T10:00:00Z"}`

	if _, err := decodeCard([]byte(j)); err == nil {
		t.Fatal("有余额却没有币种时必须报错")
	}
}

// 上游的时间是 **Unix 秒时间戳数字**，不是字符串——2026-09-04 真实端点验证
// 打脸得到的事实，文档没写清楚。字段值用合成数据，不放真实卡信息。
//
//	"created_at": 1786095722   ← 数字，不是 "2026-..." 字符串
func TestDecodeCardAcceptsUnixTimestampNumbers(t *testing.T) {
	const j = `{
	  "id": "00000000-0000-0000-0000-000000000000",
	  "mask": "400000******0000",
	  "holder_name": "ops@example.com",
	  "card_alias": "sample",
	  "status": "active",
	  "currency": "USD",
	  "available_balance": "0.49",
	  "user_id": "11111111-1111-1111-1111-111111111111",
	  "created_at": 1786095722,
	  "updated_at": 1788488360
	}`

	card, err := decodeCard([]byte(j))
	if err != nil {
		t.Fatalf("上游的真实形状必须能解析: %v", err)
	}

	if got, want := card.CreatedAt.Unix(), int64(1786095722); got != want {
		t.Fatalf("CreatedAt = %d, want %d", got, want)
	}
	if card.CreatedAt.Location() != time.UTC {
		t.Fatalf("时间必须转成 UTC 存放，got %v", card.CreatedAt.Location())
	}
	// 0.49 USD = 49 分
	if card.BalanceMinor != 49 {
		t.Fatalf("BalanceMinor = %d, want 49", card.BalanceMinor)
	}
}

// 字符串格式仍然要认：文档里给的是字符串示例，上游哪天改回去不该炸。
func TestDecodeCardStillAcceptsStringTimestamps(t *testing.T) {
	card, err := decodeCard([]byte(cardJSON))
	if err != nil {
		t.Fatal(err)
	}
	if card.CreatedAt.IsZero() {
		t.Fatal("字符串格式的时间仍应能解析")
	}
}

// 0 与负数不是合法时间戳：0 会变成 1970-01-01，那正是我们要防的
// 「看得见但没人会去查的错误」。
func TestDecodeCardRejectsZeroTimestamp(t *testing.T) {
	const j = `{"id":"c1","currency":"USD","available_balance":"1.00","created_at":0,"updated_at":0}`

	if _, err := decodeCard([]byte(j)); err == nil {
		t.Fatal("时间戳为 0 必须报错，不能落成 1970 年")
	}
}
