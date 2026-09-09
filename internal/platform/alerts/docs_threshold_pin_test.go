package alerts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 三个调好的阈值（迟滞轮数 N、R4 的窗口 W 与阈值 K）在 Go 里确实只有一处
// 定义——把 N 从 3 改成 4，alerts 与 httpapi 的测试全绿，没有任何测试硬写 3。
//
// 但同一个事实在**人读的文档**里还各手抄了一份：
//
//   - docs/modules/alerts/README.md 的规则表与「几个刻意的取舍」一节，
//     是工程师改规则前读的那份；
//   - docs/modules/notify/CATALOG.md 的规则表，是运营收到推送后用来判断
//     「它什么时候会自己好」的那份。
//
// 改了常量不动文档，门禁不会红，两份文档立刻集体说假话——正是 2026-09-08
// 报告 §四点名的「判据是别处事实的一份写死副本，事实变了它不会跟着变，
// 也不会报错，只是安静地给你一个旧答案」。
//
// 仓库里本来就有同型的钉子（rules_test.go 里那条「默认阈值变了——改动请同步
// README」），本片新加的两个数字反而没有。这个文件补上。
//
// **数字全部从常量渲染**，测试里不写字面量；措辞是文档自己的，只钉住那几个
// 数确实以正确的形态出现在正确的位置。

func repoDoc(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{"..", "..", ".."}, parts...)...)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	return string(b)
}

// TestTunedThresholdsAreQuotedInBothDocs：三个阈值必须逐字出现在两份文档里。
func TestTunedThresholdsAreQuotedInBothDocs(t *testing.T) {
	cfg := DefaultRuleConfig()
	n := cfg.SyncFailedHysteresisRounds
	w := cfg.ChronicWindowSamples
	k := cfg.ChronicFailureThreshold
	// 采集周期 300s → 一轮 5 分钟。两个升级时延也是从常量算出来的。
	minutesPerRound := int(cfg.CollectionInterval.Minutes())

	docs := []struct {
		name    string
		path    []string
		phrases []string
	}{
		{
			name: "alerts/README.md",
			path: []string{"docs", "modules", "alerts", "README.md"},
			phrases: []string{
				// R1 迟滞：开与关同一个 N。
				fmt.Sprintf("**连续 %d 轮**采集失败（少于 %d 轮的抖动不报）", n, n),
				fmt.Sprintf("**连续 %d 轮**不再失败", n),
				fmt.Sprintf("N=%d", n),
				// R4 滑动窗口。
				fmt.Sprintf("**最近 %d 条样本里失败 ≥ %d 次**", w, k),
				fmt.Sprintf("回落到 %d 次以下", k),
				fmt.Sprintf("K=%d / W=%d", k, w),
				// 两条 critical 的升级时延（这是替负责人做的取舍，必须写出来）。
				fmt.Sprintf("从 %d 分钟（旧的「连续", n*minutesPerRound),
				fmt.Sprintf("推迟到 %d 分钟（窗口 %d 里凑够 %d 次失败）", k*minutesPerRound, w, k),
				fmt.Sprintf("第 %d 轮（%d 分钟）给出 critical", n, n*minutesPerRound),
			},
		},
		{
			name: "notify/CATALOG.md",
			path: []string{"docs", "modules", "notify", "CATALOG.md"},
			phrases: []string{
				fmt.Sprintf("**连续 %d 轮**采集失败", n),
				fmt.Sprintf("**连续 %d 轮**不再失败", n),
				fmt.Sprintf("最近 %d 轮采集里**失败 ≥ %d 轮**", w, k),
				fmt.Sprintf("回落到 %d 次以下", k),
				// 运营最需要的一句：先到的一定是 R1，R4 晚约半小时。
				fmt.Sprintf("那条 %d 分钟、这条 %d 分钟", n*minutesPerRound, k*minutesPerRound),
			},
		},
	}

	for _, d := range docs {
		t.Run(d.name, func(t *testing.T) {
			text := repoDoc(t, d.path...)
			for _, phrase := range d.phrases {
				if !strings.Contains(text, phrase) {
					t.Errorf("docs/%s 里找不到 %q。\n"+
						"阈值改了就必须同步这份文档——它是%s读的那一份。\n"+
						"当前取值：迟滞 N=%d，R4 窗口 W=%d、阈值 K=%d，采集周期 %s。",
						strings.Join(d.path[1:], "/"), phrase,
						map[string]string{
							"alerts/README.md":  "工程师改规则前",
							"notify/CATALOG.md": "运营收到推送后",
						}[d.name],
						n, w, k, cfg.CollectionInterval)
				}
			}
		})
	}
}

// TestEveryRuleHasARowInBothDocs：**范围从真身发现，不手列**。
//
// 上一条钉的是「数字对不对」，这一条钉的是「有没有漏」：新增一条规则却忘了
// 写进文档，两份表就各少一行，而运营手上那份表的开头写着「规则那一行的
// 取值只有这几个」——一条不在表里的规则会让读的人以为自己看错了。
func TestEveryRuleHasARowInBothDocs(t *testing.T) {
	readme := repoDoc(t, "docs", "modules", "alerts", "README.md")
	catalog := repoDoc(t, "docs", "modules", "notify", "CATALOG.md")

	for _, r := range Rules(DefaultRuleConfig()) {
		row := "| `" + r.Key + "` |"
		if !strings.Contains(readme, row) {
			t.Errorf("规则 %q 在 docs/modules/alerts/README.md 的规则表里没有行", r.Key)
		}
		if !strings.Contains(catalog, row) {
			t.Errorf("规则 %q 在 docs/modules/notify/CATALOG.md 的规则表里没有行", r.Key)
		}
	}
}
