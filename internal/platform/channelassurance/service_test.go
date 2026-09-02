package channelassurance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// fakeReader 是 Reader 的测试替身：记录最后一次调用的入参，返回预设结果或
// 预设错误，不碰任何文件系统——本包的测试只关心 Service 这一层的平台解析、
// 时钟注入与错误翻译，聚合算法本身已经在 connectors/reqlog 测过。
type fakeReader struct {
	windowResult  reqlog.AssuranceResult
	windowErr     error
	historyResult []reqlog.AssuranceResult
	historyErr    error

	gotSource string
	gotPreset reqlog.AssuranceWindowPreset
	gotNow    time.Time
	gotN      int
}

func (f *fakeReader) WindowAssurance(_ context.Context, source string, preset reqlog.AssuranceWindowPreset, now time.Time) (reqlog.AssuranceResult, error) {
	f.gotSource, f.gotPreset, f.gotNow = source, preset, now
	return f.windowResult, f.windowErr
}

func (f *fakeReader) HistoryDays(_ context.Context, source string, at time.Time, n int) ([]reqlog.AssuranceResult, error) {
	f.gotSource, f.gotNow, f.gotN = source, at, n
	return f.historyResult, f.historyErr
}

func TestNewServiceRejectsNilReader(t *testing.T) {
	if _, err := NewService(nil); err == nil {
		t.Fatal("reader 为 nil 时应该报错")
	}
}

func TestServiceOverviewResolvesPlatformAndClock(t *testing.T) {
	fixedNow := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	reader := &fakeReader{windowResult: reqlog.AssuranceResult{Source: "sub2api", RequestCount: 42}}
	svc, err := NewService(reader, WithClock(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.Overview(context.Background(), "sub2api", reqlog.AssuranceWindow1h)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestCount != 42 {
		t.Fatalf("RequestCount = %d, want 42（应原样透传读取器结果）", result.RequestCount)
	}
	if reader.gotSource != "sub2api" {
		t.Fatalf("传给 reader 的 source = %q, want sub2api", reader.gotSource)
	}
	if reader.gotPreset != reqlog.AssuranceWindow1h {
		t.Fatalf("传给 reader 的 preset = %q, want 1h", reader.gotPreset)
	}
	if !reader.gotNow.Equal(fixedNow) {
		t.Fatalf("传给 reader 的 now = %v, want %v（时钟必须可注入且被 UTC 化后透传）", reader.gotNow, fixedNow)
	}
}

// TestServiceOverviewUnknownPlatformIsNotRegistered 核对未知/不在 reqlog
// 覆盖范围内的平台（例如 cpa）返回 404 语义的 NotRegistered，且**不调用**
// 读取器——404 应该在到达文件系统之前就短路。
func TestServiceOverviewUnknownPlatformIsNotRegistered(t *testing.T) {
	reader := &fakeReader{}
	svc, err := NewService(reader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Overview(context.Background(), "cpa", reqlog.AssuranceWindow24h)
	if err == nil {
		t.Fatal("cpa 不在 reqlog 覆盖范围内，应该报错")
	}
	var actionErr *action.Error
	if !errors.As(err, &actionErr) {
		t.Fatalf("错误类型 = %T, want *action.Error", err)
	}
	if actionErr.Code != action.CodeNotRegistered {
		t.Fatalf("Code = %q, want %q", actionErr.Code, action.CodeNotRegistered)
	}
	if reader.gotSource != "" {
		t.Fatal("平台解析失败时不该调用 reader")
	}
}

func TestServiceOverviewTranslatesReaderError(t *testing.T) {
	reader := &fakeReader{windowErr: context.Canceled}
	svc, err := NewService(reader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Overview(context.Background(), "sub2api", reqlog.AssuranceWindow15m)
	if err == nil {
		t.Fatal("reader 返回错误时 Overview 应该报错")
	}
	var actionErr *action.Error
	if !errors.As(err, &actionErr) {
		t.Fatalf("错误类型 = %T, want *action.Error", err)
	}
	if actionErr.Code != action.CodeExecutionFailed {
		t.Fatalf("Code = %q, want %q", actionErr.Code, action.CodeExecutionFailed)
	}
}

func TestServiceHistoryUsesFixedWindow(t *testing.T) {
	reader := &fakeReader{historyResult: make([]reqlog.AssuranceResult, HistoryWindowDays)}
	svc, err := NewService(reader)
	if err != nil {
		t.Fatal(err)
	}
	days, err := svc.History(context.Background(), "newapi")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != HistoryWindowDays {
		t.Fatalf("len(days) = %d, want %d", len(days), HistoryWindowDays)
	}
	if reader.gotN != HistoryWindowDays {
		t.Fatalf("传给 reader 的天数 = %d, want %d（历史记录必须钉死 7 天，不能被参数化成任意跨度）", reader.gotN, HistoryWindowDays)
	}
	if reader.gotSource != "newapi" {
		t.Fatalf("传给 reader 的 source = %q, want newapi", reader.gotSource)
	}
}

func TestServiceHistoryUnknownPlatform(t *testing.T) {
	svc, err := NewService(&fakeReader{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.History(context.Background(), "invoice"); err == nil {
		t.Fatal("invoice 不在 reqlog 覆盖范围内，History 应该报错")
	}
}
