package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
)

// XM-R009 的兼容性证明：**换编码之后，既有链上的行仍然验得过**。
//
// 这是本次修复最关键的一条。改 canonical 编码的天然后果是「历史行的重算哈希
// 全部对不上」——如果真发生了，一次安全修复会把唯一的证据链判成已被篡改，
// 比原来的漏洞更糟。canonical_version 这一列存在的全部理由就是防这个。
//
// 单元测试证不了它：那里的 Event 是内存里手搓的，版本字段想填几就填几。
// 真正要验的是**一条按 v1 写进库、再从库里读出来的行**能不能通过 VerifyChain，
// 所以这条必须在真库上跑。

// insertLegacyV1Event 绕过 Store.Append，直接按 v1 编码写一条行进库。
//
// 为什么要绕过：Append 现在一律写 v2（这正是修复本身），没有任何生产代码路径
// 还能造出 v1 行。而库里**已经有**这样的行——它们是这次修复之前写的。
// 直接 INSERT 是唯一能复刻那个状态的办法。
func insertLegacyV1Event(t *testing.T, seq int64, prevHash string) audit.Event {
	t.Helper()
	e := audit.Event{
		ID:            uuid.New(),
		Sequence:      seq,
		OccurredAt:    time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		RecordedAt:    time.Now().UTC(),
		PrincipalID:   "staff:legacy",
		PrincipalType: "HUMAN",
		ActionID:      "registry.service.create",
		ActionVersion: "v1",
		ActionRunID:   uuid.New(),
		ResourceType:  "service",
		ResourceID:    "svc-legacy",
		Environment:   "development",
		Reason:        "上线前写下的历史行",
		Result:        audit.ResultSucceeded,
		PrevHash:      prevHash,
		// 关键：按**旧**版本算哈希，模拟这条行是修复之前写的
		CanonicalVersion: audit.CanonicalV1,
	}
	normalized, err := e.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	e = normalized
	hash, err := e.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	e.EventHash = hash
	return e
}

func TestLegacyV1RowsStillVerifyAfterUpgrade(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := audit.NewStore(pool)

	// 1) 直接写两条 v1 行（模拟修复之前就在库里的历史链）
	legacy1 := insertLegacyV1Event(t, 1, audit.GenesisHash)
	legacy2 := insertLegacyV1Event(t, 2, legacy1.EventHash)
	for _, e := range []audit.Event{legacy1, legacy2} {
		_, err := pool.Exec(ctx, `
			INSERT INTO audit.audit_event (
				id, sequence, occurred_at, recorded_at, principal_id, principal_type,
				action_id, action_version, action_run_id, resource_type, resource_id,
				environment, reason, approval_id, request_id, trace_id, source_ip,
				before_summary, after_summary, connector_request_summary,
				connector_response_summary, result, compensation_result,
				prev_hash, event_hash, canonical_version
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'','','','',
				'{}'::jsonb,'{}'::jsonb,'{}'::jsonb,'{}'::jsonb,$14,'',$15,$16,$17)`,
			e.ID, e.Sequence, e.OccurredAt, e.RecordedAt, e.PrincipalID, string(e.PrincipalType),
			e.ActionID, e.ActionVersion, e.ActionRunID, e.ResourceType, e.ResourceID,
			e.Environment, e.Reason, string(e.Result), e.PrevHash, e.EventHash, e.CanonicalVersion)
		if err != nil {
			t.Fatalf("写历史行 sequence=%d 失败: %v", e.Sequence, err)
		}
	}

	// 2) 在它后面用**新**代码追加一条（Append 会写 v2）
	appended, err := store.Append(ctx, evt("registry.service.create"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if appended.CanonicalVersion != audit.CurrentCanonicalVersion {
		t.Fatalf("新写入应当用 v%d，got v%d",
			audit.CurrentCanonicalVersion, appended.CanonicalVersion)
	}
	if appended.PrevHash != legacy2.EventHash {
		t.Fatalf("新行必须接在历史链尾上：prev=%s，历史链尾=%s",
			appended.PrevHash, legacy2.EventHash)
	}

	// 3) 整条链（两条 v1 + 一条 v2）必须验得过。
	//    这就是「混版本链」——迁移之后每一条真实的链都会长这样。
	// ⚠️ 上界必须是真实的链尾。`VerifyChain(ctx, 1, 0)` 查的是
	// `sequence <= 0`——**一行都读不到**，于是它无条件返回 nil，
	// 这条测试会假装通过（cmd/audit-verify 把 0 解释成链尾，Store 不会）。
	tip, _, err := store.Tip(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tip != 3 {
		t.Fatalf("链尾应当是 3（两条历史 + 一条新写），got %d", tip)
	}
	problem, err := store.VerifyChain(ctx, 1, tip)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if problem != nil {
		t.Fatalf("混版本链应当验得过，却报了 sequence=%d %s：%s",
			problem.Sequence, problem.Kind, problem.Detail)
	}
}

// TestTamperedLegacyRowStillDetected：兼容不等于放水。
//
// 「按版本选编码」如果被写成「哪个版本能验过就用哪个」，就等于给攻击者两次
// 机会。这条确认历史行被改动之后照样报 hash_mismatch。
func TestTamperedLegacyRowStillDetected(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := audit.NewStore(pool)

	legacy := insertLegacyV1Event(t, 1, audit.GenesisHash)
	_, err := pool.Exec(ctx, `
		INSERT INTO audit.audit_event (
			id, sequence, occurred_at, recorded_at, principal_id, principal_type,
			action_id, action_version, action_run_id, resource_type, resource_id,
			environment, reason, approval_id, request_id, trace_id, source_ip,
			before_summary, after_summary, connector_request_summary,
			connector_response_summary, result, compensation_result,
			prev_hash, event_hash, canonical_version
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'','','','',
			'{}'::jsonb,'{}'::jsonb,'{}'::jsonb,'{}'::jsonb,$14,'',$15,$16,$17)`,
		legacy.ID, legacy.Sequence, legacy.OccurredAt, legacy.RecordedAt,
		legacy.PrincipalID, string(legacy.PrincipalType), legacy.ActionID, legacy.ActionVersion,
		legacy.ActionRunID, legacy.ResourceType, legacy.ResourceID, legacy.Environment,
		// reason 与算哈希时用的**不一样**：模拟这一行落库后被人改过
		"被改过的理由", string(legacy.Result), legacy.PrevHash, legacy.EventHash,
		legacy.CanonicalVersion)
	if err != nil {
		t.Fatal(err)
	}

	// 同样要给真实上界，理由见上一条用例
	tip, _, err := store.Tip(ctx)
	if err != nil {
		t.Fatal(err)
	}
	problem, err := store.VerifyChain(ctx, 1, tip)
	if err != nil {
		t.Fatal(err)
	}
	if problem == nil || problem.Kind != "hash_mismatch" {
		t.Fatalf("被改过的历史行必须报 hash_mismatch，got %+v", problem)
	}
}
