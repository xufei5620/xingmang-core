package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestHealthzAlwaysOK(t *testing.T) {
	rec := httptest.NewRecorder()
	HealthHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("不是合法 JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body = %v", body)
	}
	if body["build"] == "" || body["build"] == nil {
		t.Fatal("应含 build 信息")
	}
}

func TestReadyzReflectsDatabase(t *testing.T) {
	rec := httptest.NewRecorder()
	ReadyHandler(fakePinger{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("数据库正常应 200, got %d", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	ReadyHandler(fakePinger{err: errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")}).
		ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("数据库故障应 503, got %d", rec2.Code)
	}
	// 探针响应是公开面，不能泄漏内网地址
	if strings.Contains(rec2.Body.String(), "10.0.0.5") {
		t.Fatalf("探针泄漏内网地址: %s", rec2.Body.String())
	}
}
