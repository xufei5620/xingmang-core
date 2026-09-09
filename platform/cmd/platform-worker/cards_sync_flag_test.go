package main

import "testing"

// 流水同步默认**开启**（XM-CARD6，2026-09-05）。
//
// 它原本默认关闭，理由写在代码里：「调用量与卡数成正比，而上游限流阈值未知」。
// 那个未知现在没有了——实测 600 次/分钟/密钥。按 250 张卡、5 分钟一轮算是
// 50 次/分钟，占预算的 8%，而卡状态刷新已经改成批量（250 张卡 3 次调用），
// 恰好把原先被它占掉的那部分预算让了出来。
//
// 保留显式关闭的出口：真撞上限流时，改一个环境变量比回滚一次部署快。
func TestTransactionSyncDefaultsOn(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"未配置即开启", "", true},
		{"显式 false 关闭", "false", false},
		{"显式 true 开启", "true", true},
		{"大小写不敏感", "FALSE", false},
		{"两侧空白不影响", "  false  ", false},
		// 无法识别的值按开启处理：这个开关的两个方向不对称——
		// 多同步一轮流水的代价是几十次调用，少同步的代价是流水长期缺失
		// 而没有任何报错。拼错一个值不该静默换来后者。
		{"拼错的值仍开启", "flase", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := syncTransactionsEnabled(tc.raw); got != tc.want {
				t.Fatalf("syncTransactionsEnabled(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
