package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func devHeaders(req *http.Request, scopes string) {
	req.Header.Set("X-Dev-Principal-ID", "staff_alice")
	req.Header.Set("X-Dev-Principal-Type", "HUMAN")
	req.Header.Set("X-Dev-Scopes", scopes)
}

// executePath 是 Action 执行端点的路径。
// 用 /versions/{version}/execute 而非 :execute 后缀——chi 对路径参数后紧跟
// 冒号字面量的解析存在歧义，显式分段更稳。
func executePath(actionID, version string) string {
	return "/api/v1/actions/" + actionID + "/versions/" + version + "/execute"
}

func testRouter(t *testing.T, exec ActionExecutor, services ServiceLister) http.Handler {
	return testRouterWithMetrics(t, exec, services, nil)
}

func testRouterWithMetrics(
	t *testing.T, exec ActionExecutor, services ServiceLister, metrics MetricLister,
) http.Handler {
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
		Kernel:         exec,
		ActionRegistry: action.NewRegistry(),
		Services:       services,
		Metrics:        metrics,
	})
}

// testRouterWithJobs 只装配「后台任务」端点测得到的依赖，其余留空——
// 与 testRouterWithMetrics 同一条纪律（够用就好，不为了齐全而齐全）。
func testRouterWithJobs(t *testing.T, jobs JobsQuerier) http.Handler {
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
		Jobs:           jobs,
	})
}
