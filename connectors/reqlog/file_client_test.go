package reqlog_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/connectors/reqlog/contracttest"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// faultInjectingFileClient 把 contracttest.Factory 要求的 FakeOptions 故障
// 注入语义（Latency/FailWith/Unhealthy/UnsupportedVersion/Partial/
// MissingCapabilities/ObservedAge）套在一个真实的 NewFileClient 之上：
// file_client.go 不认识、也不该认识这些"测试专用旋钮"——注入逻辑只活在
// 测试里，这样 contracttest.RunSuite 既能跑通故障路径断言，又不会把
// 测试专用分支泄漏进生产实现（与 fakeClient 自己实现这些选项是两回事：
// 这里包的是一个真实读磁盘的客户端）。
type faultInjectingFileClient struct {
	inner reqlog.ReadClient
	opts  reqlog.FakeOptions
}

func (f *faultInjectingFileClient) wait(ctx context.Context) error {
	if f.opts.Latency <= 0 {
		return ctx.Err()
	}
	select {
	case <-time.After(f.opts.Latency):
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *faultInjectingFileClient) Version(ctx context.Context) (connector.VersionInfo, error) {
	if err := f.wait(ctx); err != nil {
		return connector.VersionInfo{}, connector.NewError(connector.KindUnavailable, "reqlog.version", err)
	}
	if f.opts.UnsupportedVersion {
		return connector.VersionInfo{
			Detected: "file/1-unsupported-fixture", Fingerprint: "file/1-unsupported-fixture",
			Supported: false, DetectedAt: time.Now().UTC(),
		}, nil
	}
	return f.inner.Version(ctx)
}

func (f *faultInjectingFileClient) Health(ctx context.Context) (connector.HealthResult, error) {
	if err := f.wait(ctx); err != nil {
		return connector.HealthResult{}, connector.NewError(connector.KindUnavailable, "reqlog.health", err)
	}
	if f.opts.Unhealthy {
		return connector.HealthResult{
			Healthy: false, CheckedAt: time.Now().UTC(),
			LatencyMS: f.opts.Latency.Milliseconds(), ErrorKind: connector.KindUnavailable,
			Detail: "injected by faultInjectingFileClient",
		}, nil
	}
	return f.inner.Health(ctx)
}

func (f *faultInjectingFileClient) Capabilities(ctx context.Context) ([]registry.Capability, error) {
	if err := f.wait(ctx); err != nil {
		return nil, connector.NewError(connector.KindUnavailable, "reqlog.capabilities", err)
	}
	caps, err := f.inner.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	if len(f.opts.MissingCapabilities) == 0 {
		return caps, nil
	}
	missing := make(map[registry.Capability]struct{}, len(f.opts.MissingCapabilities))
	for _, c := range f.opts.MissingCapabilities {
		missing[c] = struct{}{}
	}
	out := make([]registry.Capability, 0, len(caps))
	for _, c := range caps {
		if _, gone := missing[c]; !gone {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *faultInjectingFileClient) ListRequests(ctx context.Context, filter reqlog.ListFilter) (reqlog.RequestLogPage, error) {
	const op = "reqlog.requests.read"
	if err := f.wait(ctx); err != nil {
		return reqlog.RequestLogPage{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	if err := reqlog.ValidateFilter(filter); err != nil {
		return reqlog.RequestLogPage{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	if f.opts.FailWith != "" {
		return reqlog.RequestLogPage{}, connector.NewError(f.opts.FailWith, op, nil)
	}
	page, err := f.inner.ListRequests(ctx, filter)
	if err != nil {
		return page, err
	}
	if f.opts.Partial {
		page.IsPartial = true
	}
	if f.opts.ObservedAge > 0 {
		page.ObservedAt = page.ObservedAt.Add(-f.opts.ObservedAge)
	}
	return page, nil
}

func (f *faultInjectingFileClient) RequestContent(ctx context.Context, source, id string) (reqlog.RequestLogContent, error) {
	const op = "reqlog.request.content_read"
	if err := f.wait(ctx); err != nil {
		return reqlog.RequestLogContent{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	if _, err := reqlog.ParseSource(source); err != nil {
		return reqlog.RequestLogContent{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	if f.opts.FailWith != "" {
		return reqlog.RequestLogContent{}, connector.NewError(f.opts.FailWith, op, nil)
	}
	content, err := f.inner.RequestContent(ctx, source, id)
	if err != nil {
		return content, err
	}
	if f.opts.Partial {
		content.IsPartial = true
	}
	if f.opts.ObservedAge > 0 {
		content.ObservedAt = content.ObservedAt.Add(-f.opts.ObservedAge)
	}
	return content, nil
}

func newFileClientFactory(t *testing.T, dataDir, tokenMapPath string) contracttest.Factory {
	t.Helper()
	return func(opts reqlog.FakeOptions) reqlog.ReadClient {
		now := time.Now
		if opts.Now != nil {
			now = opts.Now
		}
		inner, err := reqlog.NewFileClient(reqlog.FileConfig{
			DataDir: dataDir, TokenMapPath: tokenMapPath, Now: now,
		})
		if err != nil {
			t.Fatalf("NewFileClient: %v", err)
		}
		return &faultInjectingFileClient{inner: inner, opts: opts}
	}
}

// TestFileClientSatisfiesContract 跑 connectors/reqlog/contracttest 套件
// （XM-REQLOG-MERGE 第 2 步的合规判据），全部子测试必须通过。
//
// "渠道上游与计费保留未知和已知零"（testRoutingAndBillingMetadata）这一项
// 不是靠编数据过关：reqlog 磁盘记录格式本身不含 channel/upstream/
// billed_amount 字段——逐字段核对自桌面端原型 reqlogger.go 的
// Record/FullRecord 结构体，两者都没有这三个字段（核对结论见
// contracts/connectors/reqlog.read.v1.md §10.1 第 21、22 项）。文件后端
// 如实反映这一点的方式是**不声明** reqlog.CapabilityRoutingRead /
// reqlog.CapabilityBillingRead 这两项可选能力（见 file_client.go 的
// Capabilities()），contracttest 的这条子测试据此改为断言"没声明就该恒为
// 未知"，而不是要求每个后端都能凑出磁盘上从未有过的数据——细节见
// contracttest/suite.go 里 testRoutingAndBillingMetadata 的文档注释。
// 本测试文件之外的 TestFileClientXxx 覆盖这条子测试没有覆盖到的文件后端
// 专属行为（跨天分页、令牌邮箱打码、坏索引行容错等）。
func TestFileClientSatisfiesContract(t *testing.T) {
	dataDir, tokenMapPath := buildFixtureDataDir(t)
	contracttest.RunSuite(t, newFileClientFactory(t, dataDir, tokenMapPath))
}

func mustFileClient(t *testing.T, cfg reqlog.FileConfig) reqlog.ReadClient {
	t.Helper()
	c, err := reqlog.NewFileClient(cfg)
	if err != nil {
		t.Fatalf("NewFileClient: %v", err)
	}
	return c
}

func TestFileClientCrossDayPagination(t *testing.T) {
	dataDir, tokenMapPath := buildFixtureDataDir(t)
	c := mustFileClient(t, reqlog.FileConfig{DataDir: dataDir, TokenMapPath: tokenMapPath})
	ctx := context.Background()

	seenDays := map[string]bool{}
	seenIDs := map[string]int{}
	var order []string
	cursor := ""
	var prevOccurred time.Time
	first := true
	for range 100 {
		page, err := c.ListRequests(ctx, reqlog.ListFilter{
			Source: reqlog.SourceSub2API, Limit: 2, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("ListRequests: %v", err)
		}
		for _, item := range page.Items {
			seenIDs[item.ID]++
			order = append(order, item.ID)
			// id 形如 YYYYMMDD-HHMMSS-NNNNNN：前 8 位就是落盘目录名
			if len(item.ID) >= 8 {
				seenDays[item.ID[:8]] = true
			}
			if !first && item.OccurredAt.After(prevOccurred) {
				t.Fatalf("翻页未保持时间倒序：%s 晚于前一条 %s", item.OccurredAt, prevOccurred)
			}
			prevOccurred = item.OccurredAt
			first = false
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seenDays) < 2 {
		t.Fatalf("分页应跨过至少两个日期目录，实际只见到 %v", seenDays)
	}
	for id, n := range seenIDs {
		if n != 1 {
			t.Fatalf("记录 %s 在跨天翻页中出现了 %d 次", id, n)
		}
	}
	if len(order) < 8 {
		t.Fatalf("固件里 sub2api 记录应该有不少于 8 条，翻页只翻出 %d 条", len(order))
	}
}

func TestResolveUsernameAndEmailMasking(t *testing.T) {
	dataDir, tokenMapPath := buildFixtureDataDir(t)
	c := mustFileClient(t, reqlog.FileConfig{DataDir: dataDir, TokenMapPath: tokenMapPath})
	ctx := context.Background()

	page, err := c.ListRequests(ctx, reqlog.ListFilter{Source: reqlog.SourceSub2API, Limit: reqlog.MaxListLimit})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	byPrefix := map[string]reqlog.RequestLogSummary{}
	for _, item := range page.Items {
		if _, exists := byPrefix[item.TokenPrefix]; !exists {
			byPrefix[item.TokenPrefix] = item
		}
	}

	plain, ok := byPrefix["sk-test0001invalid00"]
	if !ok {
		t.Fatal("固件里应该有 sk-test0001invalid00 前缀的记录")
	}
	if plain.Username != "fixture-user" {
		t.Fatalf("纯用户名应原样透出：got %q, want %q", plain.Username, "fixture-user")
	}

	emailish, ok := byPrefix["sk-test0002invalid00"]
	if !ok {
		t.Fatal("固件里应该有 sk-test0002invalid00 前缀的记录")
	}
	wantMasked := platformusers.MaskEmail("fixture.user@test-only.invalid")
	if emailish.Username != wantMasked {
		t.Fatalf("像邮箱的标识应经 MaskEmail 打码：got %q, want %q", emailish.Username, wantMasked)
	}
	if emailish.Username == "fixture.user@test-only.invalid" {
		t.Fatal("邮箱不能明文出现在连接器的输出里")
	}

	unmapped, ok := byPrefix["sk-unmapped-token-01"]
	if !ok {
		t.Fatal("固件里应该有未登记的 token 前缀样本")
	}
	if unmapped.Username != "" {
		t.Fatalf("未登记的 token 前缀应显示为「映射不到」（空串），不该用 token_prefix 顶替：got %q", unmapped.Username)
	}
}

func TestRequestContentRejectsMalformedID(t *testing.T) {
	dataDir, tokenMapPath := buildFixtureDataDir(t)
	c := mustFileClient(t, reqlog.FileConfig{DataDir: dataDir, TokenMapPath: tokenMapPath})
	ctx := context.Background()

	for _, bad := range []string{
		"not-an-id",
		"../../../etc/passwd",
		"",
		"   ",
		"20260827-090000-000001-extra",
		"2026082-090000-000001", // 天数位数不对
		"20260827/090000-000001",
	} {
		if _, err := c.RequestContent(ctx, reqlog.SourceSub2API, bad); !errors.Is(err, reqlog.ErrNotFound) {
			t.Errorf("id=%q 应判为 ErrNotFound, got %v", bad, err)
		}
	}
}

func TestRequestContentContentTypeFromHeaders(t *testing.T) {
	dataDir, tokenMapPath := buildFixtureDataDir(t)
	c := mustFileClient(t, reqlog.FileConfig{DataDir: dataDir, TokenMapPath: tokenMapPath})
	ctx := context.Background()

	page, err := c.ListRequests(ctx, reqlog.ListFilter{Source: reqlog.SourceSub2API, Limit: reqlog.MaxListLimit})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	var streamItem *reqlog.RequestLogSummary
	for i := range page.Items {
		if page.Items[i].Stream && page.Items[i].Status == 200 {
			streamItem = &page.Items[i]
			break
		}
	}
	if streamItem == nil {
		t.Fatal("固件里应该有一条成功的流式记录")
	}
	content, err := c.RequestContent(ctx, streamItem.Source, streamItem.ID)
	if err != nil {
		t.Fatalf("RequestContent: %v", err)
	}
	if content.RawRequest.ContentType != "application/json" {
		t.Errorf("RawRequest.ContentType = %q, want application/json", content.RawRequest.ContentType)
	}
	if content.RawResponse.ContentType != "text/event-stream" {
		t.Errorf("RawResponse.ContentType = %q, want text/event-stream", content.RawResponse.ContentType)
	}
}

func TestHealthReportsUnhealthyForMissingDataDir(t *testing.T) {
	c := mustFileClient(t, reqlog.FileConfig{DataDir: filepath.Join(t.TempDir(), "does-not-exist")})
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health 不该返回 error（结构化不健康 != 调用失败）: %v", err)
	}
	if h.Healthy {
		t.Fatal("数据目录不存在应判为不健康")
	}
	if h.ErrorKind != connector.KindUnavailable {
		t.Fatalf("ErrorKind = %q, want unavailable", h.ErrorKind)
	}
}

func TestHealthHealthyForEmptyDataDir(t *testing.T) {
	// 目录存在但还没有任何一天的数据（记录代理刚起）：健康，不是"坏了"。
	empty := t.TempDir()
	c := mustFileClient(t, reqlog.FileConfig{DataDir: empty})
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !h.Healthy {
		t.Fatalf("空目录应判为健康（没有数据不等于坏了）: %+v", h)
	}
}

func TestListRequestsSkipsMalformedIndexLines(t *testing.T) {
	dataDir, tokenMapPath := buildFixtureDataDir(t)
	idxPath := filepath.Join(dataDir, "20260827", "index.jsonl")
	f, err := os.OpenFile(idxPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开索引文件追加坏行: %v", err)
	}
	if _, err := f.WriteString("{this is not valid json\n"); err != nil {
		t.Fatalf("写坏行: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭: %v", err)
	}

	c := mustFileClient(t, reqlog.FileConfig{DataDir: dataDir, TokenMapPath: tokenMapPath})
	page, err := c.ListRequests(context.Background(), reqlog.ListFilter{
		Source: reqlog.SourceSub2API, Limit: reqlog.MaxListLimit,
	})
	if err != nil {
		t.Fatalf("一行坏数据不该拖垮整页: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatal("坏行之外的正常记录应该照常返回")
	}
}

func TestMissingTokenMapIsFailSoft(t *testing.T) {
	dataDir, _ := buildFixtureDataDir(t)
	c := mustFileClient(t, reqlog.FileConfig{
		DataDir: dataDir, TokenMapPath: filepath.Join(t.TempDir(), "no-such-tokenmap.json"),
	})
	page, err := c.ListRequests(context.Background(), reqlog.ListFilter{
		Source: reqlog.SourceSub2API, Limit: reqlog.MaxListLimit,
	})
	if err != nil {
		t.Fatalf("tokenmap 缺失不该让列表报错: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatal("即便令牌映射不可用，记录本身仍应可读")
	}
	for _, item := range page.Items {
		if item.Username != "" {
			t.Fatalf("tokenmap 不可用时 Username 必须全为空串，got %q（记录 %s）", item.Username, item.ID)
		}
	}
}

func TestNewFileClientRejectsEmptyDataDir(t *testing.T) {
	if _, err := reqlog.NewFileClient(reqlog.FileConfig{}); err == nil {
		t.Fatal("DataDir 为空应在构造期就被拒绝")
	}
}
