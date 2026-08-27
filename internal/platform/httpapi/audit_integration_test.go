package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 端到端验证：真实审计库 + 真实路由。
//
// 单元测试（audit_test.go）用 fake 证明了 handler 会输出什么；这里证明的是
// 另一件事——真实的哈希链读回来之后，**哈希字段原样到达前端**，且环境过滤
// 与倒序分页在真实 SQL 上成立。审计有一类只在集成层暴露的 bug（时间精度、
// jsonb 类型漂移），XM-0011 已经踩过两次。

func auditTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	// audit_event 有 append-only 规则，但 TRUNCATE 不受规则限制
	if _, err := pool.Exec(ctx, "TRUNCATE audit.chain_root, audit.audit_event"); err != nil {
		t.Fatalf("清空审计表失败: %v", err)
	}
	return pool
}

func appendEvent(t *testing.T, s *audit.Store, env string) audit.Event {
	t.Helper()
	e, err := s.Append(context.Background(), audit.Event{
		ID: uuid.New(), PrincipalID: "staff_alice", PrincipalType: principal.TypeHuman,
		ActionID: "registry.service.create", ActionVersion: "1", ActionRunID: uuid.New(),
		ResourceType: "core.service", ResourceID: "sub2api-" + env,
		Environment: env, RequestID: "req-" + uuid.NewString(),
		// 刻意放一个数字：jsonb 读回是 float64，归一化没做对的话链会断
		AfterSummary: map[string]any{"replicas": 3},
		Result:       audit.ResultSucceeded,
	})
	if err != nil {
		t.Fatalf("Append(%s): %v", env, err)
	}
	return e
}

func TestAuditEventsEndpointAgainstRealChain(t *testing.T) {
	store := audit.NewStore(auditTestPool(t))
	// Principal 环境是 development（见 auditRouter）；production 的事件
	// 必须一条都看不见
	envs := []string{"development", "production", "development", "production", "development"}
	appended := make(map[int64]audit.Event, len(envs))
	for _, env := range envs {
		e := appendEvent(t, store, env)
		appended[e.Sequence] = e
	}
	h := auditRouter(t, store)

	// --- 无 audit.read → 403（真库路径同样受权限约束）---
	if rec := getAudit(t, h, "", "ops.read"); rec.Code != http.StatusForbidden {
		t.Fatalf("无 audit.read 应 403, got %d", rec.Code)
	}

	// --- 倒序分页：limit=2 逐页翻，串起来不重不漏 ---
	var seen []int64
	before := int64(0)
	for page := 0; page < 10; page++ {
		q := "?limit=2"
		if before > 0 {
			q += "&before_seq=" + strconv.FormatInt(before, 10)
		}
		rec := getAudit(t, h, q, audit.ScopeRead)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		var got auditPageBody
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
		}
		for i, it := range got.Items {
			if i > 0 && got.Items[i-1].Sequence <= it.Sequence {
				t.Fatalf("页内非降序: %v", got.Items)
			}
			// --- 环境过滤：别的环境的事件不可见 ---
			if it.Environment != "development" {
				t.Fatalf("看到了 %s 环境的事件 sequence=%d（跨环境读取）",
					it.Environment, it.Sequence)
			}
			// --- 哈希字段原样返回 ---
			want := appended[it.Sequence]
			if it.EventHash != want.EventHash || it.PrevHash != want.PrevHash {
				t.Fatalf("sequence=%d 哈希被改写: got (%s,%s) want (%s,%s)",
					it.Sequence, it.EventHash, it.PrevHash, want.EventHash, want.PrevHash)
			}
			if len(it.EventHash) != 64 || len(it.PrevHash) != 64 {
				t.Fatalf("哈希长度非 64: %+v", it)
			}
			// 摘要非空时必须是对象而不是 null（float64 往返也在这里验）
			if it.AfterSummary["replicas"] != float64(3) {
				t.Fatalf("after_summary 内容不符: %+v", it.AfterSummary)
			}
			seen = append(seen, it.Sequence)
		}
		if got.NextBefore == 0 {
			break
		}
		before = got.NextBefore
	}

	// development 的事件是 sequence 1、3、5，倒序即 5、3、1
	want := []int64{5, 3, 1}
	if len(seen) != len(want) {
		t.Fatalf("翻页共读到 %v，应为 %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("翻页结果 %v，应为 %v（重复或漏读）", seen, want)
		}
	}
}

func TestAuditEventsEndpointCapsLimitAgainstRealChain(t *testing.T) {
	store := audit.NewStore(auditTestPool(t))
	for i := 0; i < audit.MaxListLimit+3; i++ {
		appendEvent(t, store, "development")
	}
	h := auditRouter(t, store)

	rec := getAudit(t, h, "?limit=100000", audit.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got auditPageBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != audit.MaxListLimit {
		t.Fatalf("limit=100000 应夹到 %d 条，got %d", audit.MaxListLimit, len(got.Items))
	}
	if got.NextBefore == 0 {
		t.Fatalf("满页应给出 next_before 供继续翻页: %d", got.NextBefore)
	}
}
