package httpapi

import (
	"context"
	"net/http"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/lifecycle"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 数据库变更（迁移记录）的只读查询（XM-READONLY-QUERIES）。
//
// 「版本与发布 → 数据库变更」那一格此前是蓝图态，页面上写着「缺的是一条新
// Query」。它缺的确实只有读侧：版本化脚本在 db/migrations，库里的
// public.schema_migrations 也一直记着已应用到哪一版。
//
// **权限归 ops.read**（lifecycle.ScopeRead）而不是 registry.read：迁移是
// Platform Lifecycle Operation（宪法 2、3 条），回答的是「这套部署自己处在
// 什么状态」，与心跳、队列积压、控制平面健康同一类运行保障知识面；
// registry.read 那一族说的是「平台管着哪些被管系统」，不是同一件事。
//
// 本端点**只读**。这一页不提供执行迁移或回滚的入口，也不该长出来——那条路
// 走版本化脚本 + 变更单 + 人工批准，不经 Action 通道，更不经 HTTP。

// MigrationReporter 是迁移状态的只读查询能力（*lifecycle.Store 满足）。
type MigrationReporter interface {
	MigrationReport(ctx context.Context) (lifecycle.Report, error)
}

type migrationItem struct {
	Version int64  `json:"version"`
	Name    string `json:"name"`
	Applied bool   `json:"applied"`
	// HasDown 只说明「这一版有没有配套的 down 脚本」，**不是**「可以回滚」。
	// 迁移是 forward-only（规格 §5.7）：down 脚本存在是为了让退路有据可查，
	// 不是一个按钮。前端的列名照这个口径写。
	HasDown bool `json:"has_down"`
}

type migrationsResponse struct {
	// AppliedVersion 是库自己记着的版本号；0 = 一条都没跑过。
	AppliedVersion int64 `json:"applied_version"`
	// Dirty = 上一次迁移跑到一半失败了。这一位比版本号要紧：dirty 的库
	// 既不能继续迁移也不该继续服务，而服务照常起、端点照常回 200。
	Dirty bool `json:"dirty"`
	// Ahead 是「二进制里带着、库里还没应用」的脚本数。> 0 说明镜像更新了
	// 而迁移没跑（或跑失败了），功能会静静地缺一块。
	Ahead int             `json:"ahead"`
	Items []migrationItem `json:"items"`
}

// ListMigrationsHandler 列出版本化迁移脚本及其在本部署的应用状态。
//
// **不按环境过滤**：一个进程只连一个库，这份读数说的就是它自己连着的那个库
// ——拿调用者的环境去筛会造出一个不存在的选择，而端点根本没有第二个库可选。
// 跨环境仍然读不到：另一个环境是另一套部署、另一个进程。
func ListMigrationsHandler(store MigrationReporter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principal.FromContext(r.Context()); !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		report, err := store.MigrationReport(r.Context())
		if err != nil {
			// 读不到就报错，不回一个「版本 0」——见 lifecycle.Store.state
			// 顶部关于 42501 的说明：一句笃定的假话比一个错误难查得多。
			WriteError(w, r, err)
			return
		}
		out := make([]migrationItem, 0, len(report.Items))
		for _, m := range report.Items {
			out = append(out, migrationItem{
				Version: m.Version, Name: m.Name, Applied: m.Applied, HasDown: m.HasDown,
			})
		}
		WriteJSON(w, http.StatusOK, migrationsResponse{
			AppliedVersion: report.Version,
			Dirty:          report.Dirty,
			Ahead:          report.Ahead,
			Items:          out,
		})
	}
}
