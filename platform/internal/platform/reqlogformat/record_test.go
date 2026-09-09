package reqlogformat

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDayDirUsesCSTCalendarDay(t *testing.T) {
	// 2026-08-29 16:30 UTC = 2026-08-30 00:30 CST（UTC+8）：跨零点，
	// 必须落进 CST 的那一天，不是 UTC 的那一天——这是磁盘目录名的既有口径
	// （原实现 `time.UnixMilli(...).In(cst).Format(...)`），不是本次新定的。
	ts := time.Date(2026, 8, 29, 16, 30, 0, 0, time.UTC)
	if got := DayDir(ts); got != "20260830" {
		t.Fatalf("DayDir = %q, want %q", got, "20260830")
	}
}

func TestDayDirPatternAndRecordIDPattern(t *testing.T) {
	for _, ok := range []string{"20260829", "00000000"} {
		if !DayDirPattern.MatchString(ok) {
			t.Errorf("DayDirPattern 应接受 %q", ok)
		}
	}
	for _, bad := range []string{"2026082", "202608299", "2026-08-29", ""} {
		if DayDirPattern.MatchString(bad) {
			t.Errorf("DayDirPattern 不应接受 %q", bad)
		}
	}
	for _, ok := range []string{"153045-000123", "000000-000000"} {
		if !RecordIDPattern.MatchString(ok) {
			t.Errorf("RecordIDPattern 应接受 %q", ok)
		}
	}
	for _, bad := range []string{"15304-000123", "153045_000123", "153045-00012", "../../etc"} {
		if RecordIDPattern.MatchString(bad) {
			t.Errorf("RecordIDPattern 不应接受 %q", bad)
		}
	}
}

func TestRecordJSONTagsMatchDiskFormat(t *testing.T) {
	// 逐字段核对 json 标签与桌面端原型 reqlogger.go 的 Record 一致——
	// 这条测试红了就意味着老数据可能读不出来了。
	r := Record{
		ID: "153045-000123", TsMs: 1, Time: "2026-08-29 15:30:45.000",
		Source: "newapi", Method: "POST", Path: "/v1/chat/completions",
		Status: 200, DurMs: 10, TtfbMs: 5, ClientIP: "203.0.113.42",
		UA: "curl", TokenPfx: "sk-abc", UpReqID: "req_1", RespID: "resp_1",
		Model: "gpt-4o", Stream: true, ReqSize: 100, RespSize: 200,
		Truncated: true, EndNote: "eof", Preview: "hi",
		InTok: 1, OutTok: 2, CacheTok: 3,
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantKeys := []string{
		"id", "ts_ms", "time", "source", "method", "path", "status", "dur_ms",
		"ttfb_ms", "client_ip", "ua", "token_prefix", "upstream_request_id",
		"resp_body_id", "model", "stream", "req_size", "resp_size",
		"truncated", "end_note", "preview", "in_tok", "out_tok", "cache_tok",
	}
	for _, k := range wantKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("缺少字段 %q", k)
		}
	}
	// omitempty 字段为零值时必须真的从 JSON 里消失，与老写入端行为一致
	zero, _ := json.Marshal(Record{ID: "x"})
	var zm map[string]any
	_ = json.Unmarshal(zero, &zm)
	for _, k := range []string{"truncated", "end_note", "preview", "in_tok", "out_tok", "cache_tok"} {
		if _, ok := zm[k]; ok {
			t.Errorf("字段 %q 零值时应因 omitempty 被省略", k)
		}
	}
}

func TestMeasuredTTFBDistinguishesUnmeasuredFromZero(t *testing.T) {
	// RespSize==0：first 从未被置位，TtfbMs 停留在 Go 零值——"从未测量"
	unmeasured := Record{TtfbMs: 0, RespSize: 0}
	if unmeasured.MeasuredTTFB() {
		t.Fatal("RespSize=0 时应判定为未测量")
	}
	// RespSize>0 且 TtfbMs==0：首字节测量值恰好是 0（极快的响应），
	// 这是一次真实测量，不能与"没测"混同（契约样本 #6：缓存命中）
	zeroButMeasured := Record{TtfbMs: 0, RespSize: 40}
	if !zeroButMeasured.MeasuredTTFB() {
		t.Fatal("RespSize>0 时 ttfb=0 应判定为已测量（合法的 0ms 观测）")
	}
	measured := Record{TtfbMs: 120, RespSize: 40}
	if !measured.MeasuredTTFB() {
		t.Fatal("应判定为已测量")
	}
}
