package shadow

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// 报告的两种形态：人看的与机器看的。
//
// 两种都要，理由不同：14 天倒计时里**每天**有人翻一次报告决定「今天算不算数」，
// 那个人要的是一眼看懂；而 §22.3 的验收要拿 14 份报告说话，那需要能被脚本
// 聚合、能进 git diff 的结构化文件。
//
// JSON 是归档格式，所以它的字段名一旦发出去就不该改——14 天里前后两份报告
// 得能用同一个脚本读。

// ArchiveVersion 是 JSON 报告的结构版本。
//
// 归档文件要跨 14 天被同一个脚本读，中途改字段必须能被发现——读的人先看这个数。
const ArchiveVersion = 1

// Window 是被对比的业务日区间（闭区间，YYYY-MM-DD）。
type Window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Archive 是 JSON 归档的顶层结构。
//
// 刻意扁平、字段名稳定：它是被 jq/脚本读的，不是被 Go 反序列化的。
type Archive struct {
	Version int    `json:"version"`
	Tool    string `json:"tool"`
	// GeneratedAt 是生成时刻（RFC3339 UTC，宪法 14 条）。
	GeneratedAt string `json:"generated_at"`
	Environment string `json:"environment"`
	Window      Window `json:"window"`
	// ToleranceCents 一并归档：一份「全绿」的报告，必须能看出它是在什么容差下绿的。
	// 不写的话，14 天后没人分得清哪几天是放宽了旋钮换来的（§9 明确不许先放宽）。
	ToleranceCents int64 `json:"tolerance_cents"`
	// Verdict 是这一天的结论：clean / dirty。
	Verdict  string           `json:"verdict"`
	Summary  Summary          `json:"summary"`
	Rows     []ArchiveRow     `json:"rows"`
	Problems []ArchiveProblem `json:"problems"`
}

// ArchiveRow 是归档里的一格。
//
// **只归档不相等的格**（见 BuildArchive）：14 天 × 几十个账号全量写进去，
// 文件会大到没人愿意看，而报告的用途是「今天有什么不对」。
type ArchiveRow struct {
	AccountID string `json:"account_id"`
	Day       string `json:"day"`
	Measure   string `json:"measure"`
	Verdict   string `json:"verdict"`
	// 未知/缺行时这两个字段为 null——**不写 0**，那是本工具最要紧的一条纪律。
	PlatformCents *int64 `json:"platform_cents"`
	SoloAICents   *int64 `json:"soloai_cents"`
	DiffCents     *int64 `json:"diff_cents"`
}

// ArchiveProblem 是归档里的一条口径错误。
type ArchiveProblem struct {
	AccountID string `json:"account_id"`
	Day       string `json:"day"`
	Side      string `json:"side"`
	Reason    string `json:"reason"`
}

// BuildArchive 把报告转成可归档的结构。
//
// 只收**没对上**的格：全量写进去会让一份报告有几千行，而人每天要看的是
// 「今天有什么不对」。全绿那天的报告因此只有一个头 + 空数组——那恰恰是
// 最好读的形态，`"verdict":"clean"` 一眼就能确认。
func BuildArchive(r Report, environment string, window Window, generatedAt time.Time) Archive {
	out := Archive{
		Version:        ArchiveVersion,
		Tool:           "platform-shadow",
		GeneratedAt:    generatedAt.UTC().Format(time.RFC3339),
		Environment:    environment,
		Window:         window,
		ToleranceCents: r.Options.ToleranceCents,
		Verdict:        verdictWord(r.Clean()),
		Summary:        r.Summary,
		Rows:           []ArchiveRow{},
		Problems:       []ArchiveProblem{},
	}
	for _, pair := range r.Pairs {
		for _, m := range pair.Measures() {
			if m.OK() {
				continue
			}
			out.Rows = append(out.Rows, ArchiveRow{
				AccountID:     pair.Key.AccountID,
				Day:           pair.Key.Day,
				Measure:       string(m.Measure),
				Verdict:       string(m.Verdict),
				PlatformCents: centsOrNil(m.Platform),
				SoloAICents:   centsOrNil(m.SoloAI),
				DiffCents:     diffOrNil(m),
			})
		}
	}
	for _, p := range r.Problems {
		out.Problems = append(out.Problems, ArchiveProblem{
			AccountID: p.Key.AccountID, Day: p.Key.Day,
			Side: string(p.Side), Reason: p.Reason,
		})
	}
	return out
}

// centsOrNil 把未知读数写成 JSON null。
//
// **绝不写 0**：归档文件会被脚本聚合，一个 0 会被当成「已知的零」加进合计里，
// 而它的真实含义是「没采到」。这条纪律在 §5.1 里管的是库，在这里管的是文件。
func centsOrNil(a Amount) *int64 {
	if !a.Known {
		return nil
	}
	v := a.Cents
	return &v
}

// diffOrNil 只在两侧都已知时给差额。
func diffOrNil(m MeasureResult) *int64 {
	if !m.Platform.Known || !m.SoloAI.Known {
		return nil
	}
	v := m.DiffCents
	return &v
}

func verdictWord(clean bool) string {
	if clean {
		return "clean"
	}
	return "dirty"
}

// WriteJSON 把归档写成缩进 JSON（带结尾换行）。
//
// 缩进不是为了好看：这些文件要进 git 并被逐日 diff，单行 JSON 的 diff 没法读。
func WriteJSON(w io.Writer, a Archive) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(a)
}

// WriteText 渲染人类可读报告。
//
// 结构是「结论先行」：第一行就是今天算不算数，然后才是统计与明细。
// 每天翻报告的人多数时候只需要第一行。
func WriteText(w io.Writer, r Report, environment string, window Window) error {
	var b strings.Builder

	if r.Clean() {
		fmt.Fprintf(&b, "影子对比：一致 ✓  环境=%s  区间=%s..%s  容差=%d 分\n",
			environment, window.From, window.To, r.Options.ToleranceCents)
	} else {
		fmt.Fprintf(&b, "影子对比：有差异 ✗  环境=%s  区间=%s..%s  容差=%d 分\n",
			environment, window.From, window.To, r.Options.ToleranceCents)
	}

	s := r.Summary
	fmt.Fprintf(&b, "共 %d 格（账号×业务日）：一致 %d，有差异 %d，平台缺行 %d，SoloAI 缺行 %d，未知 %d；口径错误 %d\n",
		s.Pairs, s.Equal, s.Differs, s.MissingOnPlatform, s.MissingOnSoloAI, s.Unknown, s.Problems)

	if s.Problems > 0 {
		// 口径错误排在差异**前面**：两边没在量同一个东西时，下面那些差额
		// 都是没有意义的数字，先看这一段才不会白查。
		b.WriteString("\n口径错误（不是差异——两边没在量同一个东西，先修这个）：\n")
		for _, p := range r.Problems {
			fmt.Fprintf(&b, "  [%s] %s / %s：%s\n", p.Side, p.Key.Day, p.Key.AccountID, p.Reason)
		}
	}

	rows := 0
	for _, pair := range r.Pairs {
		if pair.OK() {
			continue
		}
		if rows == 0 {
			b.WriteString("\n未对上的格：\n")
		}
		rows++
		fmt.Fprintf(&b, "  %s / %s\n", pair.Key.Day, pair.Key.AccountID)
		for _, m := range pair.Measures() {
			if m.OK() {
				continue
			}
			fmt.Fprintf(&b, "      %-8s %-20s 平台=%s  SoloAI=%s  差=%s\n",
				m.Measure, m.Verdict,
				renderAmount(m.Platform), renderAmount(m.SoloAI), renderDiff(m))
		}
		if pair.Platform != nil && pair.Platform.PartialRows > 0 {
			// 「差了一分钱」和「差了一分钱而且有 3 条明细没采到」是两种
			// 不同的排查起点，所以这条要跟着差异一起出现。
			fmt.Fprintf(&b, "      （平台侧 %d/%d 条明细有一侧未采到）\n",
				pair.Platform.PartialRows, pair.Platform.RowCount)
		}
	}

	if !r.Clean() {
		// 报告不止说「不对」，还要说下一步做什么——§9 明确要求先排口径、
		// 不要先放宽容差，而那份清单不该只活在设计稿里。
		b.WriteString("\n下一步（设计稿 §9 的口径清单，**先排口径，不要先放宽容差**）：\n")
		for _, item := range DisciplineChecklist {
			fmt.Fprintf(&b, "  [ ] %s\n", item)
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// DisciplineChecklist 是设计稿 §9 的口径核对清单。
//
// 逐条抄进代码而不是让报告说「见设计稿 §9」：出现差异的那一刻，看报告的人
// 手里往往只有终端。一个要人去翻文档才能用的清单，等于没有清单。
var DisciplineChecklist = []string{
	"业务日两侧都是 CST +08:00？收入与成本共用同一时间权威？",
	"newapi 成本用 type=2（仅 consume）？quota 刻度两侧同为 500000（或同为上游现值）？",
	"sub2api 成本用 /v1/usage 的 today.actual_cost，而不是面板的 trend[].cost？",
	"recharge_ratio 取的是同一时点值？平台的 ratio_snapshot 等于 SoloAI 当时的 recharge_ratio？",
	"未知两侧都跳过（不写 0）？已知 0（today==null）两侧都写 0？",
	"累计口径都是「自上次重置起每日快照之和」，而不是上游的 30 天窗口？",
	"平台四桶行数恒等式成立（分桶没丢行）？",
}

func renderAmount(a Amount) string {
	if !a.Known {
		return "未知"
	}
	return formatCents(a.Cents)
}

func renderDiff(m MeasureResult) string {
	if !m.Platform.Known || !m.SoloAI.Known {
		return "—"
	}
	return formatCents(m.DiffCents)
}

// formatCents 把分渲染成带符号的金额文本，**全整数运算不经 float**。
func formatCents(c int64) string {
	sign := ""
	if c < 0 {
		sign = "-"
		c = -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}
