package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/buildinfo"
)

// Pinger 是健康检查所需的最小数据库能力（*pgxpool.Pool 天然满足）。
type Pinger interface {
	Ping(ctx context.Context) error
}

// HealthHandler 是存活探针：只回答「进程还在」，不查任何依赖。
//
// 与 /readyz 分开是因为语义不同：存活失败会导致进程被重启，
// 数据库抖动不该触发重启。
func HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"build":  buildinfo.String(),
		})
	}
}

// ReadyHandler 是就绪探针：数据库不可达时返回 503，让反代摘掉本实例。
func ReadyHandler(db Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			// 根因只进日志：探针响应是公开面，不能泄漏内网地址（规格 §18.4）
			LoggerFrom(ctx).ErrorContext(ctx, "readiness_failed",
				slog.String("module", "httpapi"),
				slog.String("error_code", "database_unreachable"),
				slog.Any("err", err))
			WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status":     "unavailable",
				"dependency": "database",
			})
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"status": "ready"})
	}
}
