package main

import "testing"

func TestParsePsqlTSVBasic(t *testing.T) {
	out := "sk-abc0000000000001\tzhang.wei\nsk-def0000000000002\tli.na\n"
	dst := map[string]string{}
	parsePsqlTSV(out, "@newapi", dst)
	if dst["sk-abc0000000000001"] != "zhang.wei@newapi" {
		t.Errorf("got %q", dst["sk-abc0000000000001"])
	}
	if dst["sk-def0000000000002"] != "li.na@newapi" {
		t.Errorf("got %q", dst["sk-def0000000000002"])
	}
	if len(dst) != 2 {
		t.Fatalf("len = %d, want 2", len(dst))
	}
}

func TestParsePsqlTSVSkipsBlankAndMalformedLines(t *testing.T) {
	out := "\n" + // 空行
		"sk-onlyoneprefixnocol\n" + // 没有 TAB 分隔的第二列
		"\tno-prefix\n" + // 前缀为空
		"sk-ok0000000000000001\tuser.ok\n" +
		"   \n" // 纯空白行
	dst := map[string]string{}
	parsePsqlTSV(out, "@sub2api", dst)
	if len(dst) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(dst), dst)
	}
	if dst["sk-ok0000000000000001"] != "user.ok@sub2api" {
		t.Errorf("got %+v", dst)
	}
}

func TestParsePsqlTSVMergesAcrossTwoSources(t *testing.T) {
	// refreshTokenMap 对同一个 dst map 依次调用两次（newapi 一次、sub2api
	// 一次），前缀空间不重叠时两边都要保留——这是它们各自独立令牌空间的
	// 直接后果，不是需要额外去重逻辑的地方。
	dst := map[string]string{}
	parsePsqlTSV("sk-newapi000000000001\talice\n", "@newapi", dst)
	parsePsqlTSV("sk-sub2api00000000001\tbob\n", "@sub2api", dst)
	if len(dst) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(dst), dst)
	}
	if dst["sk-newapi000000000001"] != "alice@newapi" {
		t.Errorf("got %+v", dst)
	}
	if dst["sk-sub2api00000000001"] != "bob@sub2api" {
		t.Errorf("got %+v", dst)
	}
}

func TestParsePsqlTSVEmailAsIdentity(t *testing.T) {
	// sub2api 一侧的 SQL 用 coalesce(nullif(username,''), email)：
	// username 为空时标识本身就是邮箱地址。解析函数不关心这一点
	// （原样拼后缀），打码是读侧 connectors/reqlog 的职责
	// （见 file_client.go 的 resolveUsername）。
	dst := map[string]string{}
	parsePsqlTSV("sk-test0000000000001\tuser@example.com\n", "@sub2api", dst)
	if dst["sk-test0000000000001"] != "user@example.com@sub2api" {
		t.Errorf("got %q", dst["sk-test0000000000001"])
	}
}
