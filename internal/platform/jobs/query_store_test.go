package jobs

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"
)

// 本文件测试 query_store.go 里不依赖数据库的纯函数——DB 相关行为在
// query_store_integration_test.go。两边合起来才是完整覆盖。

func TestSafeTruncateUTF8(t *testing.T) {
	t.Run("不需要截断时原样返回", func(t *testing.T) {
		s, truncated := safeTruncateUTF8("hello", 10)
		if truncated || s != "hello" {
			t.Fatalf("got (%q, %v), want (\"hello\", false)", s, truncated)
		}
	})

	t.Run("ASCII 截断到字节数", func(t *testing.T) {
		s, truncated := safeTruncateUTF8("0123456789", 5)
		if !truncated || s != "01234" {
			t.Fatalf("got (%q, %v), want (\"01234\", true)", s, truncated)
		}
	})

	t.Run("不切碎多字节字符", func(t *testing.T) {
		// 每个"错"是 3 字节 UTF-8；上限 5 字节应该只保留一个完整字符（3 字节），
		// 而不是把第二个字符切掉 2/3。
		s, truncated := safeTruncateUTF8("错错错", 5)
		if !truncated {
			t.Fatal("want truncated = true")
		}
		if s != "错" {
			t.Fatalf("got %q (%d bytes), want exactly one complete rune", s, len(s))
		}
	})

	t.Run("空字符串", func(t *testing.T) {
		s, truncated := safeTruncateUTF8("", 5)
		if truncated || s != "" {
			t.Fatalf("got (%q, %v), want (\"\", false)", s, truncated)
		}
	})
}

func TestDecodeAllowedArgs(t *testing.T) {
	t.Run("只保留白名单字段", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"run_id": "r1", "environment": "staging",
			"credential_ref": "secret://should/not/leak", "endpoint": "https://internal.example",
		})
		got, err := decodeAllowedArgs(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got["run_id"] != "r1" || got["environment"] != "staging" {
			t.Fatalf("dropped an allowlisted field: %+v", got)
		}
		if _, leaked := got["credential_ref"]; leaked {
			t.Fatalf("leaked a non-allowlisted field: %+v", got)
		}
		if len(got) != 2 {
			t.Fatalf("got %d fields, want exactly 2: %+v", len(got), got)
		}
	})

	t.Run("空或缺失 args 返回空 map 而不是 nil", func(t *testing.T) {
		got, err := decodeAllowedArgs(nil)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("got %#v, want an empty non-nil map", got)
		}
	})

	t.Run("非法 JSON 报错而不是静默吞掉", func(t *testing.T) {
		if _, err := decodeAllowedArgs([]byte("{not json")); err == nil {
			t.Fatal("want an error for malformed JSON")
		}
	})
}

func TestDecodeLastError(t *testing.T) {
	t.Run("没有错误时返回 nil 与计数 0", func(t *testing.T) {
		last, count, err := decodeLastError([]byte("[]"))
		if err != nil {
			t.Fatal(err)
		}
		if last != nil || count != 0 {
			t.Fatalf("got (%+v, %d), want (nil, 0)", last, count)
		}
	})

	t.Run("多次尝试取最后一条并保留总数", func(t *testing.T) {
		attempts := []rivertype.AttemptError{
			{At: time.Unix(1000, 0), Attempt: 1, Error: "first failure"},
			{At: time.Unix(2000, 0), Attempt: 2, Error: "second failure"},
		}
		raw, _ := json.Marshal(attempts)
		last, count, err := decodeLastError(raw)
		if err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("error count = %d, want 2", count)
		}
		if last == nil || last.Message != "second failure" {
			t.Fatalf("last error = %+v, want the second (most recent) attempt", last)
		}
		if last.Truncated {
			t.Fatal("a short message should not be marked truncated")
		}
		if !last.At.Equal(time.Unix(2000, 0).UTC()) {
			t.Fatalf("last error At = %v, want %v", last.At, time.Unix(2000, 0).UTC())
		}
	})

	t.Run("超长错误文案按上限截断", func(t *testing.T) {
		longMsg := make([]byte, maxRunErrorMessageBytes+100)
		for i := range longMsg {
			longMsg[i] = 'a'
		}
		raw, _ := json.Marshal([]rivertype.AttemptError{{Error: string(longMsg)}})
		last, _, err := decodeLastError(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !last.Truncated {
			t.Fatal("want truncated = true")
		}
		if len(last.Message) > maxRunErrorMessageBytes {
			t.Fatalf("message is %d bytes, want <= %d", len(last.Message), maxRunErrorMessageBytes)
		}
		if last.OriginalLength != len(longMsg) {
			t.Fatalf("original_length = %d, want %d", last.OriginalLength, len(longMsg))
		}
	})
}

func TestRunRecordDurationMS(t *testing.T) {
	base := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)

	t.Run("尚未开始尝试时为 nil", func(t *testing.T) {
		r := RunRecord{}
		if r.DurationMS() != nil {
			t.Fatalf("want nil, got %v", r.DurationMS())
		}
	})

	t.Run("已开始但未结束时为 nil，不给一个会持续增长的假耗时", func(t *testing.T) {
		attempted := base
		r := RunRecord{AttemptedAt: &attempted}
		if r.DurationMS() != nil {
			t.Fatalf("want nil, got %v", r.DurationMS())
		}
	})

	t.Run("已结束时给出精确毫秒差", func(t *testing.T) {
		attempted := base
		finalized := base.Add(1500 * time.Millisecond)
		r := RunRecord{AttemptedAt: &attempted, FinalizedAt: &finalized}
		d := r.DurationMS()
		if d == nil || *d != 1500 {
			t.Fatalf("got %v, want 1500", d)
		}
	})
}

func TestScheduleActivityWindow(t *testing.T) {
	t.Run("没有观测周期时回落到 1 小时下限", func(t *testing.T) {
		if got := scheduleActivityWindow(nil); got != time.Hour {
			t.Fatalf("got %v, want 1h", got)
		}
	})

	t.Run("周期很短时仍不低于 1 小时下限", func(t *testing.T) {
		short := int64(60) // 1 分钟，3 倍只有 3 分钟
		if got := scheduleActivityWindow(&short); got != time.Hour {
			t.Fatalf("got %v, want the 1h floor", got)
		}
	})

	t.Run("周期较长时取 3 倍", func(t *testing.T) {
		long := int64(3600) // 1 小时，3 倍 = 3 小时，超过下限
		if got := scheduleActivityWindow(&long); got != 3*time.Hour {
			t.Fatalf("got %v, want 3h", got)
		}
	})
}

func TestValidRunStates(t *testing.T) {
	states := ValidRunStates()
	if len(states) != 7 {
		t.Fatalf("got %d states, want 7 (matching river_job_state)", len(states))
	}
	for _, s := range states {
		if !isValidRunState(s) {
			t.Fatalf("%q should be valid", s)
		}
	}
	if isValidRunState("bogus") {
		t.Fatal("an unknown state must not validate")
	}
}
