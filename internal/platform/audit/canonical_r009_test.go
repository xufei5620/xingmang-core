package audit

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// XM-R009：canonical 编码的碰撞与版本兼容（Codex 冷审 #35，Issue #75）。

// r009Base 是两个用例共用的骨架。字段固定，好让差异只来自被测的那两个字段。
func r009Base() Event {
	return Event{
		ID:            uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		Sequence:      7,
		OccurredAt:    time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC),
		PrincipalID:   "staff:operator-01",
		PrincipalType: principal.TypeHuman,
		ActionID:      "channel.disable",
		ActionVersion: "v1",
		ActionRunID:   uuid.MustParse("22222222-2222-4222-8222-222222222222"),
		ResourceType:  "channel",
		ResourceID:    "ch-1",
		Environment:   "production",
		Result:        ResultSucceeded,
		PrevHash:      GenesisHash,
	}
}

// forgedPair 返回一对**语义不同却在 v1 下同哈希**的事件。
//
// 碰撞的原理是 v1 **没有绑定字段边界**。它把相邻两个字段拼成
//
//	"reason=" + R + "\napproval_id=" + P + "\n"
//
// 而分隔用的 `\n` 与 `=` 在 R、P 里都是允许出现的普通字符。于是只要把
// R 与 P 之间的「切点」往后挪，两组不同的 (R, P) 会拼出完全相同的字节：
//
//	A: R = "例行巡检\napproval_id=APR-1"  P = ""                  声称**没有**审批
//	B: R = "例行巡检"                     P = "APR-1\napproval_id=" 声称**有**审批号
//
// 两者都拼成 `reason=例行巡检\napproval_id=APR-1\napproval_id=\n`。
//
// 安全上的意思很直接：一条「未经审批」的记录与一条「有审批号」的记录在链上
// 是同一个哈希——链答不出这个操作到底批没批过。而 reason 是人填的自由文本，
// 写它的人对内容有完全的控制权。
func forgedPair() (Event, Event) {
	// 声称没有审批，把审批号藏进 reason
	noApproval := r009Base()
	noApproval.Reason = "例行巡检\napproval_id=APR-1"
	noApproval.ApprovalID = ""

	// 声称有审批号，reason 干净
	withApproval := r009Base()
	withApproval.Reason = "例行巡检"
	withApproval.ApprovalID = "APR-1\napproval_id="
	return noApproval, withApproval
}

// TestCanonicalV1IsCollidable 证明**漏洞真的存在**。
//
// 一条测试如果只验「修好了」，无法说明当初修的是什么；这一条把缺陷本身钉下来，
// 于是「为什么要加 canonical_version 这一列」在代码里有据可查。
func TestCanonicalV1IsCollidable(t *testing.T) {
	forged, honest := forgedPair()
	forged.CanonicalVersion = CanonicalV1
	honest.CanonicalVersion = CanonicalV1

	forgedHash, err := forged.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	honestHash, err := honest.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	if forgedHash != honestHash {
		t.Fatalf("v1 应当可碰撞（这是 XM-R009 的前提）：\n伪造=%s\n真实=%s", forgedHash, honestHash)
	}
}

// TestCanonicalV2ResistsTheForgery 是修复本体：同一对事件在 v2 下哈希不同。
func TestCanonicalV2ResistsTheForgery(t *testing.T) {
	forged, honest := forgedPair()
	forged.CanonicalVersion = CanonicalV2
	honest.CanonicalVersion = CanonicalV2

	forgedHash, err := forged.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	honestHash, err := honest.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	if forgedHash == honestHash {
		t.Fatalf("v2 必须区分这两条事件，got 同一个哈希 %s", forgedHash)
	}
}

// TestCanonicalV2SeparatorsCarryNoMeaning 把「分隔符不再承担解析职责」测宽一点。
//
// 换行只是最容易想到的那一个。等号、冒号、以及长度前缀本身的数字，同样是
// v2 格式里出现的字节——值里含它们时都不能造成歧义。
func TestCanonicalV2SeparatorsCarryNoMeaning(t *testing.T) {
	seen := map[string]string{}
	for _, reason := range []string{
		"a",
		"a\napproval_id=x",
		"a=b",
		"a:b",
		"3:abc",              // 长得像长度前缀
		"\napproval_id=5:hi", // 换行 + 等号 + 长度前缀一起来
		"",
	} {
		e := r009Base()
		e.CanonicalVersion = CanonicalV2
		e.Reason = reason
		h, err := e.ComputeHash()
		if err != nil {
			t.Fatal(err)
		}
		if prev, dup := seen[h]; dup {
			t.Fatalf("v2 出现碰撞：reason=%q 与 reason=%q 同哈希", reason, prev)
		}
		seen[h] = reason
	}
}

// TestCanonicalV1BytesUnchangedByRefactor 是**历史链不失效**的保证。
//
// 本次把 v1 的行内拼接重构成了 canonicalFields + 循环。只要重构改动了哪怕一个
// 字节，库里每一条历史行的重算哈希都会对不上、整条链报「已被篡改」——那是一次
// 用修复毁掉证据链的事故。所以拿保留下来的旧实现逐字对一遍。
//
// 用例覆盖含分隔符的值：重构如果把某个 write 调用漏了或换了顺序，
// 恰恰是这些值最容易暴露。
func TestCanonicalV1BytesUnchangedByRefactor(t *testing.T) {
	for _, reason := range []string{"", "普通理由", "a\nb=c", "带=号", "带:号"} {
		e := r009Base()
		e.CanonicalVersion = CanonicalV1
		e.Reason = reason
		e.BeforeSummary = map[string]any{"enabled": true, "n": 3}
		e.AfterSummary = map[string]any{"enabled": false}

		got, err := e.canonicalV1()
		if err != nil {
			t.Fatal(err)
		}
		want, err := e.canonicalV1Legacy()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("v1 编码被重构改变了（历史链会全部失效）\nreason=%q\ngot:\n%s\nwant:\n%s",
				reason, got, want)
		}
	}
}

// TestZeroVersionMeansV1：内存里构造的零值 Event 按 v1 算。
//
// 库里那一列的 DEFAULT 是 1，但 Go 结构体的零值是 0。两者要指向同一种编码，
// 否则「从库里读出来的历史行」与「测试里手搓的同一条事件」会算出不同的哈希。
func TestZeroVersionMeansV1(t *testing.T) {
	e := r009Base()
	e.Reason = "x"

	zero := e
	zero.CanonicalVersion = 0
	one := e
	one.CanonicalVersion = CanonicalV1

	zeroBytes, err := zero.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	oneBytes, err := one.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(zeroBytes) != string(oneBytes) {
		t.Fatal("零值版本必须等价于 v1")
	}
}

// TestUnknownVersionErrors：认不出的版本要报错，不能挑一个编码顶上。
//
// 顶上算出来的哈希必然对不上，于是「代码版本太旧」会显示成「这一行被篡改了」
// ——排查方向从升级代码跑偏到查有没有人动了审计库。
func TestUnknownVersionErrors(t *testing.T) {
	e := r009Base()
	e.CanonicalVersion = 99
	if _, err := e.Canonical(); err == nil {
		t.Fatal("未知版本必须报错")
	}
}
