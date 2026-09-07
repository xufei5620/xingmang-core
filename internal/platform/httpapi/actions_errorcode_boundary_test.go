package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 这个文件补的是 XM-KERNEL-ERRCODE0 留下的**边界缺口**。
//
// 那一片修的是一个真实生产故障：`Kernel.Execute` 无条件把 Handler 的错误
// 改写成 `EXECUTION_FAILED`，于是 assurance/alerts/credentials/finance 各包
// 精心设计的 `INVALID_PARAMS` / `CONFLICT` / `PRECONDITION_FAILED` 全部不可达
// ——真实调用方看到的是 502 加一句通用文案。
//
// 修复只钉在 `action` 包的内核单测里；而这条链**真正要成立的地方是 HTTP 边界**：
//
//	Handler 的域错误 → Kernel 保留 Code/Message → StatusForCode 映射 → safeMessage 输出
//
// 四段里任何一段断掉，调用方就拿不到设计好的答案。而本包在这一片之前
// **一个用真内核的测试都没有**（全是 fakeExecutor），也就是说这四段的接缝
// 从未被一起验证过。fakeExecutor 那些用例证明的是「HTTP 层会照搬 executor
// 给的错误」，不是「内核真的会给出那个错误」。

const boundaryActionID = "assurance.probe.declare"

// boundaryRouter 装一个**真内核**：真注册表、真 Definition、真 Handler。
//
// 与本包其余测试的 fakeExecutor 相反——这里的整个价值就在于不打桩，
// 让错误真的穿过内核那一层。
func boundaryRouter(t *testing.T, handlerErr error) http.Handler {
	t.Helper()
	reg := action.NewRegistry()
	def := action.Definition{
		ID:         boundaryActionID,
		Version:    "1",
		RiskLevel:  action.L1,
		Permission: "registry.service.manage",
		Schema: action.Schema{Fields: []action.Field{
			{Name: "channel_id", Type: action.FieldString, Required: true},
		}},
		Environments:   []string{"development", "staging", "production"},
		PrincipalTypes: []principal.Type{principal.TypeHuman},
	}
	if err := reg.Register(def, func(context.Context, map[string]any) (any, error) {
		if handlerErr != nil {
			return nil, handlerErr
		}
		return map[string]string{"declaration_id": "decl-1"}, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	kernel := action.NewKernel(reg, &boundaryRunStore{}, action.WithLogger(discardLogger()))

	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, Kernel: kernel, ActionRegistry: reg,
	})
}

// boundaryRunStore 吞掉执行记录：本片验证的是错误码，不是审计落库。
type boundaryRunStore struct{ runs []action.Run }

func (s *boundaryRunStore) InsertRun(_ context.Context, r action.Run) error {
	s.runs = append(s.runs, r)
	return nil
}

func postBoundary(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		executePath(boundaryActionID, "1"), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-boundary")
	devHeaders(req, "registry.service.manage")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) ErrorBody {
	t.Helper()
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是标准错误体: %v（%s）", err, rec.Body.String())
	}
	return body.Error
}

// TestHandlerDomainErrorReachesHTTPUnchanged 是这个文件的主张：
// Handler 设计的 Code 与 Message 要**原样**到达调用方，状态码也要跟着走。
//
// 每一行都是一次真实的 HTTP 请求穿过真实内核。
func TestHandlerDomainErrorReachesHTTPUnchanged(t *testing.T) {
	cases := map[string]struct {
		handlerErr error
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		"参数类域错误": {
			handlerErr: action.NewError(action.CodeInvalidParams, `渠道目录里查无此渠道 "chn-9"`, nil),
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_PARAMS",
			wantMsg:    `渠道目录里查无此渠道 "chn-9"`,
		},
		"状态冲突": {
			handlerErr: action.NewError(action.CodeConflict, "该检测声明已存在", nil),
			wantStatus: http.StatusConflict,
			wantCode:   "CONFLICT",
			wantMsg:    "该检测声明已存在",
		},
		"前置条件不满足": {
			handlerErr: action.NewError(action.CodePreconditionFailed, "该平台的连接器尚未启用", nil),
			wantStatus: http.StatusPreconditionFailed,
			wantCode:   "PRECONDITION_FAILED",
			wantMsg:    "该平台的连接器尚未启用",
		},
		"权限类域错误": {
			handlerErr: action.NewError(action.CodePermissionDenied, "缺少权限 assurance.probe.manage", nil),
			wantStatus: http.StatusForbidden,
			wantCode:   "PERMISSION_DENIED",
			wantMsg:    "缺少权限 assurance.probe.manage",
		},
		// 乐观并发的版本冲突（XM-ERRCODE-AUDIT）。assurance 的
		// ErrVersionConflict 与 finance 的 ErrBindingConflict 都映射到它，
		// 都要在边界上变成 409——曾经 assurance 那条是 412，两个包对同一个
		// 概念给出不同状态码，而在内核 bug 期间两者都被改写成 502，看不出来。
		"版本冲突": {
			handlerErr: action.NewError(action.CodeRevisionConflict, "expected_version 与当前版本不一致，请刷新后重试", nil),
			wantStatus: http.StatusConflict,
			wantCode:   "REVISION_CONFLICT",
			wantMsg:    "expected_version 与当前版本不一致，请刷新后重试",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := postBoundary(t, boundaryRouter(t, tc.handlerErr), `{"params":{"channel_id":"chn-9"}}`)
			if rec.Code != tc.wantStatus {
				t.Fatalf("状态码 %d，期望 %d：%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			got := decodeError(t, rec)
			if got.Code != tc.wantCode {
				t.Fatalf("错误码 %q，期望 %q", got.Code, tc.wantCode)
			}
			// 文案也要原样：Handler 写的是「查无此渠道 chn-9」，调用方看到
			// 「action xxx 执行失败」就等于这条设计白做了。
			if got.Message != tc.wantMsg {
				t.Fatalf("文案 %q，期望 %q", got.Message, tc.wantMsg)
			}
			// request_id 要回到响应里，报障时能对上服务端日志（规格 §5.8）。
			if got.RequestID != "req-boundary" {
				t.Fatalf("request_id 没回来：%q", got.RequestID)
			}
		})
	}
}

// TestBareHandlerErrorIsNormalizedAndDoesNotLeak：**反向**的那一半。
//
// 只有域错误该被保留；裸的驱动/底层错误必须归一成 EXECUTION_FAILED 并换成
// 安全文案——否则修完上面那条就会变成「把 SQL 约束名和内网地址一路吐给调用方」。
// 两条一起才是完整的契约。
func TestBareHandlerErrorIsNormalizedAndDoesNotLeak(t *testing.T) {
	leaky := errors.New(`pq: duplicate key value violates unique constraint "probe_declaration_pkey" on 10.0.3.14:5432`)
	rec := postBoundary(t, boundaryRouter(t, leaky), `{"params":{"channel_id":"chn-9"}}`)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("状态码 %d，期望 502：%s", rec.Code, rec.Body.String())
	}
	got := decodeError(t, rec)
	if got.Code != "EXECUTION_FAILED" {
		t.Fatalf("错误码 %q", got.Code)
	}
	for _, secret := range []string{"pq:", "probe_declaration_pkey", "10.0.3.14"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("底层细节泄漏到响应里了（%q）：%s", secret, rec.Body.String())
		}
	}
}

// TestKernelPrechecksReachHTTPWithRightStatus：内核在 Handler 之前的那几道
// 判定，也要在 HTTP 上给出正确的状态码。
//
// 这几条以前只在 action 包里验过。它们与上面那组共用同一条输出路径，
// 一起钉住才能说「错误模型在边界上是完整的」。
func TestKernelPrechecksReachHTTPWithRightStatus(t *testing.T) {
	h := boundaryRouter(t, nil)

	t.Run("参数不合 Schema → 400", func(t *testing.T) {
		// channel_id 必填却没给。这一步在内核里，不在 HTTP 层。
		rec := postBoundary(t, h, `{"params":{}}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Code != "INVALID_PARAMS" {
			t.Fatalf("错误码 %q", got.Code)
		}
	})

	t.Run("未注册的 Action → 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost,
			executePath("registry.service.create", "1"), strings.NewReader(`{"params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		devHeaders(req, "registry.service.manage")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("缺权限 → 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost,
			executePath(boundaryActionID, "1"), strings.NewReader(`{"params":{"channel_id":"c"}}`))
		req.Header.Set("Content-Type", "application/json")
		devHeaders(req, "some.other.scope")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
		}
		// 文案要带 scope 名，前端据此告诉人「缺哪个权限」（api/client.ts 的
		// MISSING_SCOPE_PATTERN 就是按这个形状抠的）。
		if got := decodeError(t, rec); !strings.Contains(got.Message, "registry.service.manage") {
			t.Fatalf("403 文案里应当带缺失的权限名：%q", got.Message)
		}
	})
}

// TestSuccessPathThroughRealKernel 是上面那些错误用例的地基。
//
// 没有它的话，「拿到了期望的错误码」有可能是因为**任何**请求都失败——
// 一条永远 500 的链路能让上面几条里的某些误判成通过。
func TestSuccessPathThroughRealKernel(t *testing.T) {
	rec := postBoundary(t, boundaryRouter(t, nil), `{"params":{"channel_id":"chn-9"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	if body["action_run_id"] == nil || body["action_run_id"] == "" {
		t.Fatalf("成功响应必须带 action_run_id（规格 §5.8）：%s", rec.Body.String())
	}
	if result, _ := body["result"].(map[string]any); result["declaration_id"] != "decl-1" {
		t.Fatalf("Handler 的返回值没带回来：%s", rec.Body.String())
	}
}

// TestActionRunRecordsTheDomainCode：XM-KERNEL-ERRCODE0 修的**第二个**后果。
//
// 那条 bug 不只让调用方看错，还把 `ActionRun.ErrorCode` 一并污染成
// EXECUTION_FAILED——事后按错误码统计分布时全都归到一类里。响应对了不代表
// 记录也对，两者是内核里的两条不同赋值。
func TestActionRunRecordsTheDomainCode(t *testing.T) {
	reg := action.NewRegistry()
	def := action.Definition{
		ID: boundaryActionID, Version: "1", RiskLevel: action.L1,
		Permission: "registry.service.manage",
		Schema: action.Schema{Fields: []action.Field{
			{Name: "channel_id", Type: action.FieldString, Required: true},
		}},
		Environments:   []string{"development"},
		PrincipalTypes: []principal.Type{principal.TypeHuman},
	}
	if err := reg.Register(def, func(context.Context, map[string]any) (any, error) {
		return nil, action.NewError(action.CodeConflict, "该检测声明已存在", nil)
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	store := &boundaryRunStore{}
	kernel := action.NewKernel(reg, store, action.WithLogger(discardLogger()))
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, Kernel: kernel, ActionRegistry: reg,
	})

	if rec := postBoundary(t, h, `{"params":{"channel_id":"chn-9"}}`); rec.Code != http.StatusConflict {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	if len(store.runs) != 1 {
		t.Fatalf("应当写一条执行记录：%+v", store.runs)
	}
	if store.runs[0].ErrorCode != action.CodeConflict {
		t.Fatalf("ActionRun 记的错误码是 %q，期望 CONFLICT——错误码统计会失真",
			store.runs[0].ErrorCode)
	}
}
