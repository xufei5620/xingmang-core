package infini

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// Card 是一张卡在平台侧的形态。
//
// 只保留可以落库的字段：Mask 是上游给的掩码卡号，完整卡号、CVV 与有效期
// 只在 Reveal 的返回值里出现，且不进入这个类型、不落库、不进日志
// （宪法条款 7）。
type Card struct {
	ID         string
	Mask       string
	HolderName string
	Alias      string
	Status     string
	Currency   string
	// BalanceMinor 是整数最小货币单位（USD 即分）。上游给的是小数字符串，
	// 换算全程不经 float（宪法条款 13）。
	BalanceMinor int64
	UserID       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// rawCard 贴着上游 JSON 的形状，只在本文件内使用。
//
// 金额用 money.RawAmount 接住：它保留字面量原文，解码这一步绝不落到
// float64——12345.67 塞进 float64 精度就已经丢了，后面再小心也追不回来。
type rawCard struct {
	ID         string          `json:"id"`
	Mask       string          `json:"mask"`
	HolderName string          `json:"holder_name"`
	Alias      string          `json:"card_alias"`
	Status     string          `json:"status"`
	Currency   string          `json:"currency"`
	Balance    money.RawAmount `json:"available_balance"`
	UserID     string          `json:"user_id"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}

// timestampLayouts 是接受的时间格式。
//
// 上游实际用哪一种尚未对真实响应验证过（文档只给了示例）。认不出来就报错，
// 不落零值时间：零值会让「数据新鲜度」显示成 1970 年——一个看得见、
// 但没人会去查的错误。
var timestampLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05.000Z07:00",
	"2006-01-02 15:04:05",
}

func decodeCard(b []byte) (Card, error) {
	var raw rawCard
	if err := json.Unmarshal(b, &raw); err != nil {
		return Card{}, fmt.Errorf("解码卡片: %w", err)
	}
	return raw.toCard()
}

func (raw rawCard) toCard() (Card, error) {
	balance, err := raw.balanceMinor()
	if err != nil {
		return Card{}, err
	}

	createdAt, err := parseTimestamp(raw.CreatedAt, "created_at")
	if err != nil {
		return Card{}, err
	}
	updatedAt, err := parseTimestamp(raw.UpdatedAt, "updated_at")
	if err != nil {
		return Card{}, err
	}

	return Card{
		ID:           raw.ID,
		Mask:         raw.Mask,
		HolderName:   raw.HolderName,
		Alias:        raw.Alias,
		Status:       raw.Status,
		Currency:     raw.Currency,
		BalanceMinor: balance,
		UserID:       raw.UserID,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}, nil
}

// balanceMinor 把余额文本换算成整数最小单位。
//
// 余额字段缺失按 0 处理——新开的卡上游可能不返回该字段，而卡刚开出来
// 余额本就是 0。但余额存在却没有币种时必须报错：最小单位小数位无从判断，
// 猜 2 位的那 100 倍不会有任何症状。
func (raw rawCard) balanceMinor() (int64, error) {
	if raw.Balance == "" {
		return 0, nil
	}
	scale, err := money.CurrencyScale(raw.Currency)
	if err != nil {
		return 0, fmt.Errorf("卡 %s 的余额无法换算: %w", raw.ID, err)
	}
	minor, err := money.ParseMinorUnits(string(raw.Balance), scale)
	if err != nil {
		return 0, fmt.Errorf("卡 %s 的余额: %w", raw.ID, err)
	}
	return minor, nil
}

// parseTimestamp 解析上游时间并统一转成 UTC（宪法条款 14：时间库内 UTC）。
func parseTimestamp(raw, field string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("%s 为空", field)
	}
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	// 不把原始值放进错误文本之外的地方；这里放是因为时间不是敏感数据，
	// 而排查格式问题必须看到它长什么样。
	return time.Time{}, fmt.Errorf("%s 的时间格式无法解析: %q", field, raw)
}
