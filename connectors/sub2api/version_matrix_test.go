package sub2api

import "testing"

// XM-SUB2API-0_2-MATRIX：兼容矩阵这个**值本身**要被钉住。
//
// amount_test.go 里那张表钉的是 versionSupported 的**算法**（前缀怎么匹配、
// v 前缀认不认、预发布后缀影响不影响），它用的是局部矩阵，改导出的
// SupportedUpstreamVersions 不会让它变红。而矩阵的值是采集链路的判据：
// 它落后于生产实际跑的版本时，连接器对真实上游一律 Supported=false，
// 没有任何测试会说话。这个文件就是让它说话的地方。

func TestSupportedUpstreamVersionsCoversProduction(t *testing.T) {
	// 生产在 2026-09-05 就地升级到 0.2.1（后台点更新会替换容器内的二进制，
	// 镜像标签不变）。矩阵必须认它，否则采集判据直接判假。
	for _, detected := range []string{"0.2.1", "v0.2.1", "0.2.0", "0.2.9-rc.1", "0.2"} {
		if !versionSupported(detected, SupportedUpstreamVersions) {
			t.Fatalf("矩阵必须支持 %q：生产实际运行的是 0.2.x", detected)
		}
	}
	// 0.1 线仍在矩阵里：回滚到旧二进制时采集不能跟着判假。
	for _, detected := range []string{"0.1.179", "0.1.183", "0.1.133"} {
		if !versionSupported(detected, SupportedUpstreamVersions) {
			t.Fatalf("矩阵不能丢掉 0.1 线：%q 回滚时仍要能采集", detected)
		}
	}
}

// 矩阵是**白名单**，不是「大于等于就行」：没有逐字段核对过源码的次版本
// 必须判假，让 R6（upstream.version.changed）把它说出来，而不是静静地按
// 旧字段解析新响应。
func TestSupportedUpstreamVersionsRejectsUnverifiedLines(t *testing.T) {
	for _, detected := range []string{"0.3.0", "0.0.9", "1.0.0", "1.2.3", "10.1.0", "unknown", ""} {
		if versionSupported(detected, SupportedUpstreamVersions) {
			t.Fatalf("没有核对过源码的版本必须判假：%q", detected)
		}
	}
}

// 矩阵条目写到 major.minor，不写补丁位。写成 "0.2.1" 会让 0.2.2 判假——
// 上游打个补丁我们就停止采集，而补丁版之间字段没变。
func TestSupportedUpstreamVersionsAreMinorLevelEntries(t *testing.T) {
	for _, entry := range SupportedUpstreamVersions {
		dots := 0
		for _, r := range entry {
			if r == '.' {
				dots++
			}
		}
		if dots != 1 {
			t.Fatalf("矩阵条目 %q 应写到 major.minor（恰好一个点），否则前缀匹配会把同线补丁版判假", entry)
		}
	}
}
