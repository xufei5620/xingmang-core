package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestParsePsqlTSVBasic(t *testing.T) {
	out := "sk-abc0000000000001\tzhang.wei\t1001\nsk-def0000000000002\tli.na\t1002\n"
	v1 := map[string]string{}
	v2 := map[string]tokenMapV2Entry{}
	parsePsqlTSV(out, "@newapi", "newapi", v1, v2)
	if v1["sk-abc0000000000001"] != "zhang.wei@newapi" {
		t.Errorf("got %q", v1["sk-abc0000000000001"])
	}
	if v1["sk-def0000000000002"] != "li.na@newapi" {
		t.Errorf("got %q", v1["sk-def0000000000002"])
	}
	if len(v1) != 2 {
		t.Fatalf("len(v1) = %d, want 2", len(v1))
	}
	want1 := tokenMapV2Entry{Username: "zhang.wei", Source: "newapi", UserID: "1001"}
	if v2["sk-abc0000000000001"] != want1 {
		t.Errorf("v2 got %+v, want %+v", v2["sk-abc0000000000001"], want1)
	}
	want2 := tokenMapV2Entry{Username: "li.na", Source: "newapi", UserID: "1002"}
	if v2["sk-def0000000000002"] != want2 {
		t.Errorf("v2 got %+v, want %+v", v2["sk-def0000000000002"], want2)
	}
}

func TestParsePsqlTSVSkipsBlankAndMalformedLines(t *testing.T) {
	// 注意：strings.TrimSpace 会先吃掉整行的前导空白（含 TAB），所以一个
	// 前导 TAB 不能用来构造"前缀为空"的输入——它会被 TrimSpace 直接剥掉，
	// 变成"少一列"的另一种残缺形状。两种残缺都该被跳过，这里保留原始
	// 用例覆盖"没有 TAB 分隔的第二列"这一种。
	out := "\n" + // 空行
		"sk-onlyoneprefixnocol\n" + // 没有 TAB 分隔的第二列
		"sk-ok0000000000000001\tuser.ok\t42\n" +
		"   \n" // 纯空白行
	v1 := map[string]string{}
	v2 := map[string]tokenMapV2Entry{}
	parsePsqlTSV(out, "@sub2api", "sub2api", v1, v2)
	if len(v1) != 1 {
		t.Fatalf("len(v1) = %d, want 1: %+v", len(v1), v1)
	}
	if v1["sk-ok0000000000000001"] != "user.ok@sub2api" {
		t.Errorf("got %+v", v1)
	}
	if len(v2) != 1 {
		t.Fatalf("len(v2) = %d, want 1: %+v", len(v2), v2)
	}
	if v2["sk-ok0000000000000001"].UserID != "42" {
		t.Errorf("v2 UserID = %q, want 42", v2["sk-ok0000000000000001"].UserID)
	}
}

func TestParsePsqlTSVMergesAcrossTwoSources(t *testing.T) {
	// refreshTokenMap 对同一对 dst map 依次调用两次（newapi 一次、sub2api
	// 一次），前缀空间不重叠时两边都要保留——这是它们各自独立令牌空间的
	// 直接后果，不是需要额外去重逻辑的地方。
	v1 := map[string]string{}
	v2 := map[string]tokenMapV2Entry{}
	parsePsqlTSV("sk-newapi000000000001\talice\t1\n", "@newapi", "newapi", v1, v2)
	parsePsqlTSV("sk-sub2api00000000001\tbob\t2\n", "@sub2api", "sub2api", v1, v2)
	if len(v1) != 2 {
		t.Fatalf("len(v1) = %d, want 2: %+v", len(v1), v1)
	}
	if v1["sk-newapi000000000001"] != "alice@newapi" {
		t.Errorf("got %+v", v1)
	}
	if v1["sk-sub2api00000000001"] != "bob@sub2api" {
		t.Errorf("got %+v", v1)
	}
	if v2["sk-newapi000000000001"].Source != "newapi" || v2["sk-sub2api00000000001"].Source != "sub2api" {
		t.Errorf("v2 sources got %+v", v2)
	}
}

func TestParsePsqlTSVEmailAsIdentity(t *testing.T) {
	// sub2api 一侧的 SQL 用 coalesce(nullif(username,''), email)：
	// username 为空时标识本身就是邮箱地址。解析函数不关心这一点
	// （原样拼后缀），打码是读侧 connectors/reqlog 的职责
	// （见 file_client.go 的 resolveUsername）。
	v1 := map[string]string{}
	v2 := map[string]tokenMapV2Entry{}
	parsePsqlTSV("sk-test0000000000001\tuser@example.com\t7\n", "@sub2api", "sub2api", v1, v2)
	if v1["sk-test0000000000001"] != "user@example.com@sub2api" {
		t.Errorf("got %q", v1["sk-test0000000000001"])
	}
	if v2["sk-test0000000000001"].Username != "user@example.com" {
		t.Errorf("v2 Username got %q", v2["sk-test0000000000001"].Username)
	}
}

func TestParsePsqlTSVMissingUserIDColumnLeavesV2Empty(t *testing.T) {
	// 上游 ID 这一列在真实导出里恒非空（等值 JOIN 的直接后果，见
	// resolveUserRef 的注释），但解析函数本身不假设这一点：只给两列时
	// （没有第三列）v1 不受影响，v2 侧 UserID 为空字符串——这是"映射不到"
	// 路径的输入,而不是解析错误。
	v1 := map[string]string{}
	v2 := map[string]tokenMapV2Entry{}
	parsePsqlTSV("sk-nouid00000000000001\tno.id.user\n", "@newapi", "newapi", v1, v2)
	if v1["sk-nouid00000000000001"] != "no.id.user@newapi" {
		t.Errorf("v1 got %+v", v1)
	}
	entry, ok := v2["sk-nouid00000000000001"]
	if !ok {
		t.Fatal("v2 应该仍然登记这个前缀（只是 UserID 为空）")
	}
	if entry.UserID != "" {
		t.Errorf("UserID = %q, want empty", entry.UserID)
	}
}

func TestParsePsqlTSVTrimsWhitespaceInUserID(t *testing.T) {
	v1 := map[string]string{}
	v2 := map[string]tokenMapV2Entry{}
	parsePsqlTSV("sk-ws0000000000000001\tspacey.user\t 99 \n", "@sub2api", "sub2api", v1, v2)
	if v2["sk-ws0000000000000001"].UserID != "99" {
		t.Errorf("UserID = %q, want trimmed 99", v2["sk-ws0000000000000001"].UserID)
	}
}

// --- ADR-020 决策·二：机械防护 ---
//
// 这两条测试对源码里的 SQL 字符串字面量（newapiTokenMapQuery /
// sub2apiTokenMapQuery，本包唯一权威副本）做静态断言，不需要真实数据库
// 连接——与 internal/platform/pgreadonly 在运行期拦截真实连接的机制不同，
// 但精神一致（见 ADR-020 决策·二的说明）。

// wordBoundary 把关键字包在单词边界里，防止子串误判
// （比如 "update" 出现在别的标识符内部）。
func wordBoundary(keyword string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(keyword) + `\b`)
}

func TestTokenMapQueriesContainNoWriteKeywords(t *testing.T) {
	// CR-0008 验收标准第 4 条 + ADR-020 决策·二：导出 SQL 不含
	// insert/update/delete/drop/alter/truncate/grant 等写关键字
	// （大小写不敏感）。
	denylist := []string{"insert", "update", "delete", "drop", "alter", "truncate", "grant"}
	for _, q := range []struct{ name, sql string }{
		{"newapi", newapiTokenMapQuery},
		{"sub2api", sub2apiTokenMapQuery},
	} {
		for _, kw := range denylist {
			if wordBoundary(kw).MatchString(q.sql) {
				t.Errorf("%s 查询含写关键字 %q: %s", q.name, kw, q.sql)
			}
		}
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(q.sql)), "select") {
			t.Errorf("%s 查询必须以 select 开头: %s", q.name, q.sql)
		}
	}
}

func TestTokenMapQueriesAreSingleStatementsNoTransaction(t *testing.T) {
	// ADR-020 决策·二："导出路径不含写语句"里隐含的另一半——每条查询必须
	// 是恰好一条以分号收尾的语句，不能靠语句拼接（`; insert ...`）夹带
	// 别的命令；也不应显式开启事务（这条导出路径本身就不该是写事务）。
	for _, q := range []struct{ name, sql string }{
		{"newapi", newapiTokenMapQuery},
		{"sub2api", sub2apiTokenMapQuery},
	} {
		trimmed := strings.TrimSpace(q.sql)
		if n := strings.Count(trimmed, ";"); n != 1 {
			t.Errorf("%s 查询应恰好含一个分号（一条语句），实际 %d 个: %s", q.name, n, q.sql)
		}
		if !strings.HasSuffix(trimmed, ";") {
			t.Errorf("%s 查询应以分号收尾（分号后不该再跟别的命令）: %s", q.name, q.sql)
		}
		lower := strings.ToLower(trimmed)
		for _, kw := range []string{"begin", "start transaction"} {
			if strings.Contains(lower, kw) {
				t.Errorf("%s 查询不该显式开启事务: %s", q.name, q.sql)
			}
		}
	}
}

// tokenMapQueryAllowedIdentifiers 是 ADR-020 决策·一·1 的封闭列举
// （关键字/函数、表名、别名、列名都算在内——凡是能被下面的粗粒度标识符
// 提取器认成"看起来像标识符"的词，都必须显式出现在这张表里）。任何扩大
// 都需要先修 ADR，不得直接在 tokenmap.go 加语句/加表/加列。
var tokenMapQueryAllowedIdentifiers = map[string]bool{
	// SQL 关键字/函数
	"select": true, "from": true, "join": true, "on": true,
	"left": true, "concat": true, "coalesce": true, "nullif": true,
	// 字符串字面量 'sk-' 的字母部分（不是标识符，但粗粒度正则会一并
	// 捕获，显式放行比切换成更复杂的 SQL 解析更省事）。
	"sk": true,
	// 表
	"tokens": true, "users": true, "api_keys": true,
	// 别名
	"t": true, "u": true, "k": true,
	// 列
	"key": true, "user_id": true, "id": true, "username": true, "email": true,
}

var identifierPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

func TestTokenMapQueriesOnlyReferenceAllowlistedIdentifiers(t *testing.T) {
	for _, q := range []struct{ name, sql string }{
		{"newapi", newapiTokenMapQuery},
		{"sub2api", sub2apiTokenMapQuery},
	} {
		for _, tok := range identifierPattern.FindAllString(q.sql, -1) {
			if !tokenMapQueryAllowedIdentifiers[strings.ToLower(tok)] {
				t.Errorf("%s 查询出现了不在 ADR-020 决策·一·1 封闭列举里的标识符 %q: %s",
					q.name, tok, q.sql)
			}
		}
	}
}
