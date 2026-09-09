package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/lifecycle"
)

// 「版本与发布 → 数据库变更」那一格的只读端点（XM-READONLY-QUERIES）。
//
// 本文件用假仓储验 handler 段：响应形状、权限、未装配时端点不存在，
// 以及**读不到时报错而不是回一个「版本 0」**。最后那条是本片最要紧的一条：
// 库角色拆分之后 API 身份可能没有 public.schema_migrations 的读权限，
// 那时把错误吞成「一条迁移都没应用」是一句笃定的假话。
//
// 「版本号真的来自库、清单真的来自二进制」是另一段代码，
// 在 migrations_integration_test.go 里用真库验证。

type fakeMigrationReporter struct {
	report lifecycle.Report
	err    error
	calls  int
}

func (f *fakeMigrationReporter) MigrationReport(context.Context) (lifecycle.Report, error) {
	f.calls++
	return f.report, f.err
}

func migrationsRouter(t *testing.T, store MigrationReporter) http.Handler {
	t.Helper()
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
		Migrations:     store,
	})
}

func sampleReport() lifecycle.Report {
	return lifecycle.Report{
		State: lifecycle.State{Version: 2, Dirty: false},
		Items: []lifecycle.Migration{
			{Script: lifecycle.Script{Version: 1, Name: "init_core_registry", HasDown: true}, Applied: true},
			{Script: lifecycle.Script{Version: 2, Name: "init_action", HasDown: true}, Applied: true},
			{Script: lifecycle.Script{Version: 3, Name: "init_audit", HasDown: false}, Applied: false},
		},
		Ahead: 1,
	}
}

type migrationsBody struct {
	AppliedVersion int64 `json:"applied_version"`
	Dirty          bool  `json:"dirty"`
	Ahead          int   `json:"ahead"`
	Items          []struct {
		Version int64  `json:"version"`
		Name    string `json:"name"`
		Applied bool   `json:"applied"`
		HasDown bool   `json:"has_down"`
	} `json:"items"`
}

func decodeMigrations(t *testing.T, body []byte) migrationsBody {
	t.Helper()
	var out migrationsBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("响应解析失败: %v（%s）", err, string(body))
	}
	return out
}

// TestMigrationsEndpointReportsVersionDirtyAndInventory 钉住整份读数的契约。
//
// 三样东西缺一不可，所以三样都断言：
//   - applied_version：库跑到哪一版；
//   - dirty：上一次迁移有没有跑到一半失败（服务照常起，别处看不出来）；
//   - ahead + 逐条 applied：二进制里带着而库里还没跑的有几版。
func TestMigrationsEndpointReportsVersionDirtyAndInventory(t *testing.T) {
	store := &fakeMigrationReporter{report: sampleReport()}
	rec := getAs(t, migrationsRouter(t, store), "/api/v1/ops/migrations", "ops.read")

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeMigrations(t, rec.Body.Bytes())
	if got.AppliedVersion != 2 {
		t.Fatalf("applied_version = %d，期望 2", got.AppliedVersion)
	}
	if got.Dirty {
		t.Fatal("dirty 应为 false")
	}
	if got.Ahead != 1 {
		t.Fatalf("ahead = %d，期望 1", got.Ahead)
	}
	if len(got.Items) != 3 {
		t.Fatalf("items 应有 3 条，实际 %d", len(got.Items))
	}
	if got.Items[0].Version != 1 || got.Items[0].Name != "init_core_registry" ||
		!got.Items[0].Applied || !got.Items[0].HasDown {
		t.Fatalf("第一条 = %+v", got.Items[0])
	}
	// 第三条是「有脚本、还没跑」——applied 与 has_down 都要如实。
	if got.Items[2].Version != 3 || got.Items[2].Applied || got.Items[2].HasDown {
		t.Fatalf("第三条 = %+v，期望 version=3 applied=false has_down=false", got.Items[2])
	}
}

// TestMigrationsEndpointSurfacesDirty：dirty 为真时如实回 true。
//
// 与上一条是对照关系：上一条断言 false，这一条断言 true，同一个字段两个方向
// 都钉住——只钉一个方向的话，一个恒返回该值的实现照样绿。
func TestMigrationsEndpointSurfacesDirty(t *testing.T) {
	report := sampleReport()
	report.Dirty = true
	rec := getAs(t, migrationsRouter(t, &fakeMigrationReporter{report: report}),
		"/api/v1/ops/migrations", "ops.read")

	if got := decodeMigrations(t, rec.Body.Bytes()); !got.Dirty {
		t.Fatalf("dirty 应为 true：%s", rec.Body.String())
	}
}

// TestMigrationsEndpointRequiresOpsRead：权限是 ops.read，不是 registry.read。
//
// 归属是本片一个明确的判断（迁移是平台自身的运行事实，不是资源目录），
// 所以两个方向都钉：registry.read 拿不到，ops.read 拿得到。
func TestMigrationsEndpointRequiresOpsRead(t *testing.T) {
	h := migrationsRouter(t, &fakeMigrationReporter{report: sampleReport()})

	denied := getAs(t, h, "/api/v1/ops/migrations", "registry.read")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("registry.read 应 403，实际 %d：%s", denied.Code, denied.Body.String())
	}
	if got := decodeError(t, denied); got.Message != "缺少权限 ops.read" {
		t.Fatalf("403 文案 = %q，期望「缺少权限 ops.read」", got.Message)
	}

	allowed := getAs(t, h, "/api/v1/ops/migrations", "ops.read")
	if allowed.Code != http.StatusOK {
		t.Fatalf("对照组：ops.read 应 200，实际 %d：%s", allowed.Code, allowed.Body.String())
	}
}

// TestMigrationsReadFailureIsAnErrorNotAZeroVersion 是本文件最要紧的一条。
//
// 库角色拆分（contracts/database/role-policy.v1.json）把 public schema 留给
// River，public.schema_migrations 标着 no-runtime-access——也就是说 API 身份
// 今天没有读它的权限。那种部署上这个端点必须**报错**：回一个
// applied_version=0 会在界面上显示「一条迁移都没应用」，那是一句笃定的假话，
// 比一个 500 难查得多（宪法 12 条：缺数据要说出来）。
//
// 底层细节同时不能泄漏——错误里带着表名与主机地址。
func TestMigrationsReadFailureIsAnErrorNotAZeroVersion(t *testing.T) {
	denied := errors.New(`ERROR: permission denied for table schema_migrations (SQLSTATE 42501) on 10.0.3.14:5432`)
	rec := getAs(t, migrationsRouter(t, &fakeMigrationReporter{err: denied}),
		"/api/v1/ops/migrations", "ops.read")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("读不到时应 500，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 正向断言：响应是标准错误体，且**没有** applied_version 这个字段。
	// 先确认它确实是错误体（正向锚点），再断言那个字段不在——反过来写的话，
	// 一个把 body 写成空串的实现也会让「字段不在」为真。
	if got := decodeError(t, rec); got.Code != "INTERNAL" || got.Message != "服务内部错误" {
		t.Fatalf("错误体 = %+v", got)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if _, present := raw["applied_version"]; present {
		t.Fatalf("读不到时不该回 applied_version：%s", rec.Body.String())
	}
	for _, secret := range []string{"42501", "schema_migrations", "10.0.3.14"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("响应泄漏了底层细节 %q：%s", secret, rec.Body.String())
		}
	}
}

// TestMigrationsRouteAbsentWithoutStore：未装配时端点不存在。
// 缺席型断言，配对照组（装了的那条必须 200）。
func TestMigrationsRouteAbsentWithoutStore(t *testing.T) {
	if got := getAs(t, migrationsRouter(t, nil), "/api/v1/ops/migrations", "ops.read"); got.Code != http.StatusNotFound {
		t.Fatalf("未装配时应 404，实际 %d：%s", got.Code, got.Body.String())
	}
	wired := migrationsRouter(t, &fakeMigrationReporter{report: sampleReport()})
	if got := getAs(t, wired, "/api/v1/ops/migrations", "ops.read"); got.Code != http.StatusOK {
		t.Fatalf("对照组：装了仓储应 200，实际 %d：%s", got.Code, got.Body.String())
	}
}
