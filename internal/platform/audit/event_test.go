package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func sampleEvent() Event {
	at := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	return Event{
		ID:            uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Sequence:      1,
		OccurredAt:    at,
		RecordedAt:    at,
		PrincipalID:   "staff_alice",
		PrincipalType: principal.TypeHuman,
		ActionID:      "registry.service.create",
		ActionVersion: "1",
		ActionRunID:   uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		ResourceType:  "core.service",
		ResourceID:    "sub2api-prod",
		Environment:   "production",
		RequestID:     "req-1",
		AfterSummary:  map[string]any{"status": "active"},
		Result:        ResultSucceeded,
		PrevHash:      GenesisHash,
	}
}

func TestComputeHashIsDeterministic(t *testing.T) {
	e := sampleEvent()
	h1, err := e.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	h2, err := e.ComputeHash()
	if err != nil || h1 != h2 {
		t.Fatalf("同一事件两次哈希不一致: %s vs %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("哈希长度 = %d, want 64", len(h1))
	}
	if strings.ToLower(h1) != h1 {
		t.Fatal("哈希应为小写十六进制")
	}
}

func TestComputeHashIgnoresMapOrder(t *testing.T) {
	// map 迭代顺序随机；规范化必须排序键，否则同一事件会产生不同哈希，
	// 整条链在重启后就对不上了
	a := sampleEvent()
	a.AfterSummary = map[string]any{"x": 1, "y": 2, "z": 3}
	b := sampleEvent()
	b.AfterSummary = map[string]any{"z": 3, "y": 2, "x": 1}

	ha, _ := a.ComputeHash()
	hb, _ := b.ComputeHash()
	if ha != hb {
		t.Fatalf("map 键顺序影响了哈希: %s vs %s", ha, hb)
	}
}

func TestComputeHashChangesWithEveryMeaningfulField(t *testing.T) {
	base, _ := sampleEvent().ComputeHash()
	for name, mutate := range map[string]func(*Event){
		"PrincipalID":   func(e *Event) { e.PrincipalID = "staff_bob" },
		"ActionID":      func(e *Event) { e.ActionID = "registry.service.observe" },
		"ActionRunID":   func(e *Event) { e.ActionRunID = uuid.New() },
		"ResourceID":    func(e *Event) { e.ResourceID = "other" },
		"Environment":   func(e *Event) { e.Environment = "staging" },
		"Result":        func(e *Event) { e.Result = ResultFailed },
		"AfterSummary":  func(e *Event) { e.AfterSummary = map[string]any{"status": "retired"} },
		"BeforeSummary": func(e *Event) { e.BeforeSummary = map[string]any{"status": "active"} },
		"OccurredAt":    func(e *Event) { e.OccurredAt = e.OccurredAt.Add(time.Second) },
		"Sequence":      func(e *Event) { e.Sequence = 2 },
		"PrevHash":      func(e *Event) { e.PrevHash = strings.Repeat("a", 64) },
		"Reason":        func(e *Event) { e.Reason = "改了理由" },
		"SourceIP":      func(e *Event) { e.SourceIP = "10.0.0.1" },
	} {
		e := sampleEvent()
		mutate(&e)
		h, err := e.ComputeHash()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if h == base {
			t.Fatalf("%s 改动后哈希未变——该字段没进链，篡改将无法被发现", name)
		}
	}
}

func TestComputeHashExcludesRecordedAt(t *testing.T) {
	// recorded_at 是库侧写入时刻，不同副本可能不同；它不该进链，
	// 否则从备份恢复后链会失效
	a := sampleEvent()
	b := sampleEvent()
	b.RecordedAt = b.RecordedAt.Add(5 * time.Minute)
	ha, _ := a.ComputeHash()
	hb, _ := b.ComputeHash()
	if ha != hb {
		t.Fatal("recorded_at 不应进入哈希")
	}
}
