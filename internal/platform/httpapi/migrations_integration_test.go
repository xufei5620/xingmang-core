package httpapi

import (
	"net/http"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/lifecycle"
)

// 端到端：真实 public.schema_migrations + 真实 lifecycle.Store + 真实路由。
//
// 单元测试（migrations_test.go）用假仓储证明 handler 会把读数原样搬进响应；
// 这里证明的是另一段——版本号真的来自库，清单真的来自二进制，而且两边对得上。
// 只测前一段的话，一个「SELECT 写错表名」的实现照样能让假仓储那组全绿。

func migrationsIntRouter(t *testing.T) http.Handler {
	t.Helper()
	pool := catalogTestPool(t)
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:         discardLogger(),
		Service:        "platform-api",
		Environment:    "development",
		DB:             fakePinger{},
		Resolver:       res,
		Kernel:         &fakeExecutor{},
		ActionRegistry: action.NewRegistry(),
		Migrations:     lifecycle.NewStore(pool),
	})
}

// TestMigrationsEndpointAgainstRealDatabase：测试库是被 db/migrations 迁到
// 最新的，所以这份读数必须说「全部已应用、没有落后、不脏」。
//
// 判据取自**二进制里的清单**（lifecycle.Inventory）而不是写死一个数字：
// 写死 49 的话，下一片加一条迁移就得改测试，而那次改动最该被这条挡住的
// 恰恰是「加了脚本但清单没跟上」。
func TestMigrationsEndpointAgainstRealDatabase(t *testing.T) {
	h := migrationsIntRouter(t)

	scripts, err := lifecycle.Inventory()
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if len(scripts) == 0 {
		t.Fatal("清单是空的——判据本身失效了")
	}
	latest := scripts[len(scripts)-1].Version

	rec := getAs(t, h, "/api/v1/ops/migrations", "ops.read")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeMigrations(t, rec.Body.Bytes())

	// 版本号来自库。测试库由 scripts/dev/worktree-testdb.sh 迁到最新，
	// 所以它必须等于清单里最后一版——这一条同时挡住两种错：
	// 读错了表（会是 0）、以及清单与磁盘漂开（上一版号对不上）。
	if got.AppliedVersion != latest {
		t.Fatalf("applied_version = %d，测试库应已迁到最新的 %d："+
			"要么读错了表，要么测试库没跑最新迁移（先跑 scripts/dev/worktree-testdb.sh）",
			got.AppliedVersion, latest)
	}
	if got.Dirty {
		t.Fatal("测试库的 dirty 应为 false——为真说明上一次迁移跑到一半失败了")
	}
	if got.Ahead != 0 {
		t.Fatalf("ahead = %d，全部已应用时应为 0", got.Ahead)
	}
	if len(got.Items) != len(scripts) {
		t.Fatalf("items %d 条，清单 %d 条", len(got.Items), len(scripts))
	}
	for _, item := range got.Items {
		if !item.Applied {
			t.Fatalf("版本 %d（%s）被报成未应用，而测试库已迁到 %d",
				item.Version, item.Name, got.AppliedVersion)
		}
	}
	// 第一版的名字来自嵌入的文件名，不是编出来的。
	if got.Items[0].Version != 1 || got.Items[0].Name != "init_core_registry" {
		t.Fatalf("第一条 = %+v", got.Items[0])
	}
}
