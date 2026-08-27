package shadow

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var reportClock = time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC)

var reportWindow = Window{From: "2026-08-15", To: "2026-08-28"}

// TestArchiveCleanDayIsSmallAndUnambiguous：全绿那天的归档应当一眼可确认。
//
// 只归档没对上的格，所以 clean 那天只有一个头 + 两个空数组——
// 14 天里大多数报告都该长这样，翻起来才不累。
func TestArchiveCleanDay(t *testing.T) {
	r, err := Compare(
		[]Row{row("acc-1", "2026-08-27", KnownCents(100), KnownCents(40))},
		[]Row{row("acc-1", "2026-08-27", KnownCents(100), KnownCents(40))},
		Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := BuildArchive(r, "production", reportWindow, reportClock)

	if a.Verdict != "clean" {
		t.Fatalf("verdict = %q, want clean", a.Verdict)
	}
	if len(a.Rows) != 0 || len(a.Problems) != 0 {
		t.Fatalf("全绿那天不该有明细: %+v", a)
	}
	if a.Version != ArchiveVersion {
		t.Fatalf("缺少结构版本: %+v", a)
	}
	// 容差必须归档：一份「全绿」的报告要能看出它是在什么容差下绿的，
	// 否则 14 天后没人分得清哪几天是放宽旋钮换来的（§9 明确不许先放宽）。
	if a.ToleranceCents != 0 {
		t.Fatalf("tolerance_cents = %d, want 0", a.ToleranceCents)
	}
	if a.Window != reportWindow || a.Environment != "production" {
		t.Fatalf("窗口/环境没带上: %+v", a)
	}
	// 时间戳是 UTC RFC3339（宪法 14 条）。
	if a.GeneratedAt != "2026-08-28T06:00:00Z" {
		t.Fatalf("generated_at = %q", a.GeneratedAt)
	}
	// 空数组必须序列化成 []，不是 null——脚本要能无条件 `.rows[]`。
	var buf bytes.Buffer
	if err := WriteJSON(&buf, a); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"rows": []`) {
		t.Fatalf("空明细应是 []，不是 null:\n%s", buf.String())
	}
}

// TestArchiveNeverWritesZeroForUnknown 是归档侧最要紧的一条纪律。
//
// 未知必须序列化成 JSON null。归档文件会被脚本聚合，一个 0 会被当成
// 「已知的零」加进合计里，而它的真实含义是「没采到」——
// §5.1 在库里管的这条纪律，在文件里同样要管。
func TestArchiveNeverWritesZeroForUnknown(t *testing.T) {
	r, err := Compare(
		[]Row{row("acc-1", "2026-08-27", Unknown(), KnownCents(40))},
		[]Row{row("acc-1", "2026-08-27", KnownCents(100), KnownCents(40))},
		Options{})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, BuildArchive(r, "production", reportWindow, reportClock)); err != nil {
		t.Fatal(err)
	}

	var decoded Archive
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range decoded.Rows {
		if row.Measure != string(MeasureRevenue) {
			continue
		}
		found = true
		if row.PlatformCents != nil {
			t.Fatalf("未知的收入必须是 null，不是 %d——0 会被脚本当成「已知的零」加进合计",
				*row.PlatformCents)
		}
		if row.DiffCents != nil {
			t.Fatalf("一侧未知时不该有差额: %d", *row.DiffCents)
		}
		if row.SoloAICents == nil || *row.SoloAICents != 100 {
			t.Fatalf("已知的那一侧要照常给值: %+v", row)
		}
	}
	if !found {
		t.Fatal("没找到收入那一行，测试没验到东西")
	}
}

// TestArchiveOnlyKeepsUnmatchedRows：对上的格不进归档。
func TestArchiveOnlyKeepsUnmatchedRows(t *testing.T) {
	r, err := Compare(
		[]Row{
			row("ok", "2026-08-27", KnownCents(100), KnownCents(40)),
			row("bad", "2026-08-27", KnownCents(101), KnownCents(40)),
		},
		[]Row{
			row("ok", "2026-08-27", KnownCents(100), KnownCents(40)),
			row("bad", "2026-08-27", KnownCents(100), KnownCents(40)),
		},
		Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := BuildArchive(r, "production", reportWindow, reportClock)
	for _, row := range a.Rows {
		if row.AccountID == "ok" {
			t.Fatalf("对上的格不该进归档: %+v", row)
		}
	}
	// bad 这格：收入差 1 分、毛利跟着差 1 分，成本相同不进。
	if len(a.Rows) != 2 {
		t.Fatalf("应只有收入与毛利两行: %+v", a.Rows)
	}
}

// TestWriteTextLeadsWithVerdict：人类可读报告结论先行。
//
// 每天翻报告的人多数时候只需要第一行。
func TestWriteTextLeadsWithVerdict(t *testing.T) {
	clean, err := Compare(
		[]Row{row("acc-1", "2026-08-27", KnownCents(100), KnownCents(40))},
		[]Row{row("acc-1", "2026-08-27", KnownCents(100), KnownCents(40))},
		Options{})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteText(&buf, clean, "production", reportWindow); err != nil {
		t.Fatal(err)
	}
	first := strings.SplitN(buf.String(), "\n", 2)[0]
	if !strings.Contains(first, "一致") {
		t.Fatalf("第一行就该给结论: %q", first)
	}
	// 全绿时不该输出口径清单——那是出问题时的下一步，平时是噪音。
	if strings.Contains(buf.String(), "下一步") {
		t.Fatalf("全绿不该附排查清单:\n%s", buf.String())
	}
}

// TestWriteTextShowsChecklistAndUnknownRendering：出问题时给出下一步。
//
// §9 要求「先排口径，不要先放宽容差」，而那份清单不该只活在设计稿里——
// 出现差异的那一刻，看报告的人手里往往只有终端。
func TestWriteTextShowsChecklistAndUnknownRendering(t *testing.T) {
	r, err := Compare(
		[]Row{row("acc-1", "2026-08-27", Unknown(), KnownCents(40))},
		[]Row{row("acc-1", "2026-08-27", KnownCents(100), KnownCents(40))},
		Options{})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteText(&buf, r, "production", reportWindow); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.Contains(out, "有差异") {
		t.Fatalf("第一行应给出「有差异」: %s", out)
	}
	// 未知渲染成「未知」而不是 0.00——终端上一个 0.00 会被当成真的零。
	if !strings.Contains(out, "未知") {
		t.Fatalf("未知应渲染成「未知」:\n%s", out)
	}
	if !strings.Contains(out, "下一步") || !strings.Contains(out, DisciplineChecklist[0]) {
		t.Fatalf("有问题时应附 §9 口径清单:\n%s", out)
	}
	if !strings.Contains(out, "不要先放宽容差") {
		t.Fatalf("清单前必须写明「先排口径」:\n%s", out)
	}
}

// TestWriteTextPutsProblemsBeforeDiffs：口径错误排在差异前面。
//
// 两边没在量同一个东西时，下面那些差额都是没有意义的数字，先看这一段
// 才不会白查。
func TestWriteTextPutsProblemsBeforeDiffs(t *testing.T) {
	bad := row("acc-1", "2026-08-27", KnownCents(999), KnownCents(40))
	bad.Currency = "CNY"
	r, err := Compare([]Row{bad},
		[]Row{row("acc-1", "2026-08-27", KnownCents(100), KnownCents(40))}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteText(&buf, r, "production", reportWindow); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	problemAt := strings.Index(out, "口径错误")
	diffAt := strings.Index(out, "未对上的格")
	if problemAt < 0 || diffAt < 0 {
		t.Fatalf("两段都该出现:\n%s", out)
	}
	if problemAt > diffAt {
		t.Fatalf("口径错误应排在差异前面:\n%s", out)
	}
}

// TestFormatCentsIsIntegerOnly：金额渲染全整数，不经 float。
func TestFormatCentsIsIntegerOnly(t *testing.T) {
	cases := map[int64]string{
		0: "0.00", 1: "0.01", 99: "0.99", 100: "1.00",
		12345: "123.45", -1: "-0.01", -12345: "-123.45",
	}
	for cents, want := range cases {
		if got := formatCents(cents); got != want {
			t.Fatalf("formatCents(%d) = %q, want %q", cents, got, want)
		}
	}
}
