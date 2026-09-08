package lifecycle_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/lifecycle"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// 本文件验证的是**嵌进二进制的那份清单**与仓库磁盘上的那一份一致。
//
// 为什么值得单独测：go:embed 的模式写错了不会报错，只会少嵌几个文件——
// 「migrations/*.sql」改成「migrations/*.up.sql」照样编译通过，然后界面上
// 每一版的「有回滚脚本」全部变成否，而没有任何东西会红。所以判据必须取自
// **磁盘**，不能取自嵌进来的那份自己。

var scriptNamePattern = regexp.MustCompile(`^([0-9]+)_(.+)\.(up|down)\.sql$`)

// diskScripts 从磁盘上的 db/migrations 读出版本 → 是否有 down 脚本。
func diskScripts(t *testing.T) map[int64]bool {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "db", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取 %s: %v", dir, err)
	}
	out := map[int64]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := scriptNamePattern.FindStringSubmatch(e.Name())
		if m == nil {
			t.Fatalf("db/migrations 下出现认不出的文件名 %q", e.Name())
		}
		v, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			t.Fatalf("版本号解析失败 %q: %v", e.Name(), err)
		}
		if _, ok := out[v]; !ok {
			out[v] = false
		}
		if m[3] == "down" {
			out[v] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("磁盘上一个迁移脚本都没读到——判据本身失效了")
	}
	return out
}

// TestInventoryMatchesTheScriptsOnDisk：嵌进来的清单必须与磁盘逐版本对得上。
//
// 三条一起断言，缺一条都留得下一个真实的失败形态：
//   - 版本集合相同 → 嵌漏了整版会红；
//   - 每一版的 HasDown 相同 → 只嵌 up 会红（这条正是模式写错时的症状）；
//   - 名字非空且升序 → 解析把名字吃掉、或者顺序乱了会红。
func TestInventoryMatchesTheScriptsOnDisk(t *testing.T) {
	want := diskScripts(t)

	got, err := lifecycle.Inventory()
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("嵌入清单 %d 版，磁盘上 %d 版", len(got), len(want))
	}

	var prev int64
	for i, s := range got {
		wantDown, ok := want[s.Version]
		if !ok {
			t.Fatalf("嵌入清单里的版本 %d 在磁盘上不存在", s.Version)
		}
		if s.HasDown != wantDown {
			t.Fatalf("版本 %d 的 has_down = %v，磁盘上是 %v", s.Version, s.HasDown, wantDown)
		}
		if s.Name == "" {
			t.Fatalf("版本 %d 的名字是空串", s.Version)
		}
		if i > 0 && s.Version <= prev {
			t.Fatalf("清单没有按版本升序：第 %d 项是 %d，上一项是 %d", i, s.Version, prev)
		}
		prev = s.Version
	}
}

// TestInventoryKeepsTheFirstMigrationIdentifiable 钉住第一版的**名字**。
//
// 上一条比的是「与磁盘一致」，两边同时错的话它照样绿（比如解析把名字统一
// 截成空串——那条已经被上面挡了，但比如统一取错一段就不会）。这里钉一个
// 与实现无关的已知事实：000001 叫 init_core_registry，且带 down 脚本。
func TestInventoryKeepsTheFirstMigrationIdentifiable(t *testing.T) {
	got, err := lifecycle.Inventory()
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("清单是空的")
	}
	first := got[0]
	if first.Version != 1 {
		t.Fatalf("第一版是 %d，期望 1", first.Version)
	}
	if first.Name != "init_core_registry" {
		t.Fatalf("第一版名字 = %q，期望 init_core_registry", first.Name)
	}
	if !first.HasDown {
		t.Fatal("000001 有 down 脚本，has_down 却是 false")
	}
}

// TestScopeReadIsOpsRead：本包声明的读权限必须与 ops 包那一个**逐字相同**。
//
// 两处各写一个字符串常量迟早漂开，而漂开的症状是「界面 403、权限表里查得到
// 那个 scope」——最难查的一类。同 alerts/consistency_test.go 的做法。
func TestScopeReadIsOpsRead(t *testing.T) {
	if lifecycle.ScopeRead != ops.ScopeRead {
		t.Fatalf("lifecycle.ScopeRead = %q，与 ops.ScopeRead = %q 不一致；"+
			"迁移状态按设计复用 ops.read，两处必须同源",
			lifecycle.ScopeRead, ops.ScopeRead)
	}
	if lifecycle.ScopeRead != "ops.read" {
		t.Fatalf("lifecycle.ScopeRead = %q，期望字面量 ops.read", lifecycle.ScopeRead)
	}
}
