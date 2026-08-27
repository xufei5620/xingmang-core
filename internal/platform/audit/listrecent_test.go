package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// evtIn 造一条指定环境的事件。审计链的 sequence 是**全局**的（跨环境连续），
// 所以按环境过滤后序号必然有缺口——这正是 ListRecent 与 List 不能合并的原因。
func evtIn(env, actionID string) audit.Event {
	return audit.Event{
		ID: uuid.New(), OccurredAt: time.Now().UTC(),
		PrincipalID: "staff_alice", PrincipalType: principal.TypeHuman,
		ActionID: actionID, ActionVersion: "1", ActionRunID: uuid.New(),
		ResourceType: "core.service", ResourceID: "sub2api-" + env,
		Environment: env, RequestID: "req-" + uuid.NewString(),
		AfterSummary: map[string]any{"status": "active"},
		Result:       audit.ResultSucceeded,
	}
}

func TestListRecentIsDescendingAndPagesWithoutGaps(t *testing.T) {
	s := audit.NewStore(testPool(t))
	ctx := context.Background()

	const total = 7
	for i := 0; i < total; i++ {
		if _, err := s.Append(ctx, evtIn("development", "registry.service.create")); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}

	// 按 limit=3 一页页往回翻，把游标串起来
	seen := make([]int64, 0, total)
	before := int64(0)
	for page := 0; page < 10; page++ {
		got, err := s.ListRecent(ctx, "development", before, 3)
		if err != nil {
			t.Fatalf("ListRecent(before=%d): %v", before, err)
		}
		if len(got) == 0 {
			break
		}
		for i, e := range got {
			// 页内降序
			if i > 0 && got[i-1].Sequence <= e.Sequence {
				t.Fatalf("页内非降序: %d 之后是 %d", got[i-1].Sequence, e.Sequence)
			}
			seen = append(seen, e.Sequence)
		}
		before = got[len(got)-1].Sequence
	}

	if len(seen) != total {
		t.Fatalf("翻页共读到 %d 条，应为 %d：%v", len(seen), total, seen)
	}
	// 不重不漏：翻完正好是 total..1 的严格降序
	for i, seq := range seen {
		if want := int64(total - i); seq != want {
			t.Fatalf("第 %d 条 sequence = %d，应为 %d（重复或漏读）：%v", i, seq, want, seen)
		}
	}
}

func TestListRecentBeforeSeqIsExclusive(t *testing.T) {
	s := audit.NewStore(testPool(t))
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, evtIn("development", "registry.service.create")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// before_seq=2 必须**不含** 2 本身，否则翻页会重复展示上一页最后一条
	got, err := s.ListRecent(ctx, "development", 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Sequence != 1 {
		t.Fatalf("before_seq=2 应只返回 sequence=1，got %+v", seqsOf(got))
	}

	// 负游标按「从最新开始」处理，不能被当成有效上界而返回空
	got, err = s.ListRecent(ctx, "development", -5, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("负 before_seq 应等价于从最新开始，got %v", seqsOf(got))
	}
}

func TestListRecentFiltersByEnvironment(t *testing.T) {
	s := audit.NewStore(testPool(t))
	ctx := context.Background()

	// 交错写入三个环境，让每个环境的 sequence 都不连续
	envs := []string{"development", "staging", "production", "staging", "development", "staging"}
	for _, env := range envs {
		if _, err := s.Append(ctx, evtIn(env, "registry.service.create")); err != nil {
			t.Fatalf("Append(%s): %v", env, err)
		}
	}

	got, err := s.ListRecent(ctx, "staging", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("staging 应有 3 条，got %v", seqsOf(got))
	}
	for _, e := range got {
		if e.Environment != "staging" {
			// 别的环境的事件泄漏出来 = 跨环境读取，规格 §20.5 明确禁止
			t.Fatalf("返回了 %s 环境的事件 sequence=%d", e.Environment, e.Sequence)
		}
	}
	// 序号在本环境内降序，且确实带缺口（6,4,2）
	if got[0].Sequence != 6 || got[1].Sequence != 4 || got[2].Sequence != 2 {
		t.Fatalf("staging 序号应为 6/4/2，got %v", seqsOf(got))
	}

	// 空环境返回空切片而不是错误
	got, err = s.ListRecent(ctx, "production", 3, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("production 在 sequence<3 内没有事件，got %v", seqsOf(got))
	}
}

func TestListRecentClampsLimit(t *testing.T) {
	s := audit.NewStore(testPool(t))
	ctx := context.Background()

	const total = audit.MaxListLimit + 5
	for i := 0; i < total; i++ {
		if _, err := s.Append(ctx, evtIn("development", "registry.service.create")); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}

	// 超限一律夹到上限：不封顶的话一次请求就能拖走整条链
	got, err := s.ListRecent(ctx, "development", 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != audit.MaxListLimit {
		t.Fatalf("limit=100000 应夹到 %d，got %d", audit.MaxListLimit, len(got))
	}

	// limit<=0 用默认页大小
	got, err = s.ListRecent(ctx, "development", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != audit.DefaultListLimit {
		t.Fatalf("limit=0 应用默认页大小 %d，got %d", audit.DefaultListLimit, len(got))
	}
}

func TestListRecentReturnsHashesUnchanged(t *testing.T) {
	s := audit.NewStore(testPool(t))
	ctx := context.Background()

	appended := make(map[int64]audit.Event)
	for i := 0; i < 3; i++ {
		e, err := s.Append(ctx, evtIn("development", "registry.service.create"))
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		appended[e.Sequence] = e
	}

	got, err := s.ListRecent(ctx, "development", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("应有 3 条，got %d", len(got))
	}
	for _, e := range got {
		want := appended[e.Sequence]
		if e.EventHash != want.EventHash || e.PrevHash != want.PrevHash {
			t.Fatalf("sequence=%d 哈希被改写: got (%s,%s) want (%s,%s)",
				e.Sequence, e.EventHash, e.PrevHash, want.EventHash, want.PrevHash)
		}
		// 读路径不能悄悄改变参与哈希的字段（时间精度、jsonb 数字类型都踩过），
		// 所以在读回的事件上重算一次哈希必须自洽
		computed, err := e.ComputeHash()
		if err != nil {
			t.Fatal(err)
		}
		if computed != e.EventHash {
			t.Fatalf("sequence=%d 读回后重算哈希不符：读路径改变了字段表示", e.Sequence)
		}
	}
}

func seqsOf(events []audit.Event) []int64 {
	out := make([]int64, 0, len(events))
	for _, e := range events {
		out = append(out, e.Sequence)
	}
	return out
}
