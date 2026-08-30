package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// paymentsMode 决定"支付与财务"逐笔订单端点(XM-PAY0)用哪个 ReadClient 实现。
//
// 语义与 usersMode 完全一致，但**独立成一份而不是复用**：两个功能各自的
// 默认值由各自的环境变量(XM_PLATFORM_PAYMENTS_MODE / XM_PLATFORM_USERS_MODE)
// 决定——操作员可能想让用户清单先切到 real、订单查询还留在 fake 观察一阵，
// 合用一个开关会剥夺这个灵活性(两条通道读的是同一批上游凭据，但"要不要
// 把这项能力暴露出来"是两个独立的产品决定)。
type paymentsMode string

const (
	paymentsModeFake paymentsMode = "fake"
	paymentsModeReal paymentsMode = "real"
	// paymentsModeOff 完全不挂载订单端点——比端点存在却只回演示数据诚实，
	// 理由与 usersModeOff 相同。
	paymentsModeOff paymentsMode = "off"
)

// parsePaymentsMode 解析 XM_PLATFORM_PAYMENTS_MODE，空串按 fake 处理
// （理由同 parseUsersMode：代价小的一侧选可用性）。
func parsePaymentsMode(s string) (paymentsMode, error) {
	switch mode := paymentsMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return paymentsModeFake, nil
	case paymentsModeFake, paymentsModeReal, paymentsModeOff:
		return mode, nil
	default:
		return "", fmt.Errorf("XM_PLATFORM_PAYMENTS_MODE %q: 只接受 off / fake / real", s)
	}
}

// platformPaymentsDeps 是 buildPlatformPayments 需要的运行时依赖。
//
// Pool 为 nil 时(测试装配)完全按 XM_PLATFORM_PAYMENTS_MODE 的进程级缺省
// 运行，不查 core.connector_config——与 platformUsersDeps 同一条纪律。
type platformPaymentsDeps struct {
	Pool    *pgxpool.Pool
	Secrets secrets.SecretProvider
	Logger  *slog.Logger
	// Sub2APIDefaults / NewAPIDefaults 是没有 core.connector_config 行时的
	// env 缺省，读的是与 cmd/platform-worker 完全相同的一组
	// XM_SUB2API_*/XM_NEWAPI_* 环境变量(loadSub2APIPaymentsDefaults /
	// loadNewAPIPaymentsDefaults)，操作员因此只需要配一套变量，两个进程都吃。
	Sub2APIDefaults jobs.Sub2APIRealConfig
	NewAPIDefaults  jobs.NewAPIRealConfig
}

// buildPlatformPayments 按模式构造逐笔订单查询入口；off 时返回 nil
// （端点不挂载，见 httpapi.Deps.PlatformOrders 的注释）。
//
// fake/real 不是在这里一次性焊死的——返回的 *dynamicPaymentsQuerier 在每次
// ListOrders 调用时才去查 core.connector_config(经 jobs.NewPgConnectorConfigSource
// 的 30s 缓存)，与 dynamicUsersClient 同一条纪律：运营在后台切换接入模式，
// 这个进程不需要重启。
func buildPlatformPayments(mode paymentsMode, deps platformPaymentsDeps) *dynamicPaymentsQuerier {
	if mode == paymentsModeOff {
		return nil
	}
	var configSource jobs.ConnectorConfigSource
	if deps.Pool != nil {
		configSource = jobs.NewPgConnectorConfigSource(deps.Pool)
	}
	return &dynamicPaymentsQuerier{
		defaultMode:     mode,
		configSource:    configSource,
		secrets:         deps.Secrets,
		logger:          deps.Logger,
		sub2apiDefaults: deps.Sub2APIDefaults,
		newapiDefaults:  deps.NewAPIDefaults,
	}
}

// errPlatformPaymentsCapabilityUnavailable 是本轮构造出的客户端不满足
// payments.read.v1(sub2api.PaymentsReadClient / newapi.PaymentsReadClient)时
// 的确定性失败。生产装配(jobs.NewSub2APIClientFactory / NewNewAPIClientFactory
// 经由 sub2api.NewClient / NewFake)返回的客户端始终满足它，这条分支只在
// 上游契约包被换成不兼容实现时才会触发——归 not_supported 而不是 panic。
var errPlatformPaymentsCapabilityUnavailable = errors.New(
	"platformpayments: client does not implement PaymentsReadClient")

// dynamicPaymentsQuerier 实现 httpapi.PlatformOrdersQuerier，按 platform 分派到
// sub2api/newapi 各自的连接器；生效模式/端点/凭据引用推迟到每次调用才解析，
// 见 buildPlatformPayments 的注释。
type dynamicPaymentsQuerier struct {
	defaultMode     paymentsMode
	configSource    jobs.ConnectorConfigSource
	secrets         secrets.SecretProvider
	logger          *slog.Logger
	sub2apiDefaults jobs.Sub2APIRealConfig
	newapiDefaults  jobs.NewAPIRealConfig
}

// ListOrders 实现 httpapi.PlatformOrdersQuerier。
func (q *dynamicPaymentsQuerier) ListOrders(
	ctx context.Context, in httpapi.PlatformOrdersInput,
) (httpapi.PlatformOrdersResult, error) {
	switch in.Platform {
	case jobs.ConnectorPlatformSub2API:
		return q.listSub2API(ctx, in)
	case jobs.ConnectorPlatformNewAPI:
		return q.listNewAPI(ctx, in)
	default:
		// 路由层(ListPlatformOrdersHandler)已经拦过一次，这里是第二道防线：
		// Querier 不该信任调用方传进来的字符串恰好是两个已知值之一。
		return httpapi.PlatformOrdersResult{}, connector.NewError(connector.KindNotSupported,
			"platformpayments.list_orders", fmt.Errorf("平台 %q 不支持订单查询", in.Platform))
	}
}

func (q *dynamicPaymentsQuerier) listSub2API(
	ctx context.Context, in httpapi.PlatformOrdersInput,
) (httpapi.PlatformOrdersResult, error) {
	const op = "platformpayments.sub2api.list_orders"
	mode, cfg, err := q.resolveSub2API(ctx, in.Environment)
	if err != nil {
		return httpapi.PlatformOrdersResult{}, err
	}
	// production 下生效模式仍是 fake：绝不把演示订单当成真实经营数据展示
	// 给财务角色看(宪法 12 条)，与 dynamicUsersClient.ListUsers 同一条闸。
	if mode == jobs.Sub2APIModeFake && in.Environment == "production" {
		return httpapi.PlatformOrdersResult{}, connector.NewError(connector.KindNotSupported,
			op, jobs.ErrConnectorProductionFake)
	}

	// 复用 jobs.NewSub2APIClientFactory：真实模式下的配置校验(missing 清单)、
	// 错误分类(not_supported vs internal)与 worker 同步走的是同一段代码，
	// 这个端点不该有第二份"怎么判定配置不全"的逻辑。
	rc, err := jobs.NewSub2APIClientFactory(mode, cfg)(ctx)
	if err != nil {
		return httpapi.PlatformOrdersResult{}, err
	}
	client, ok := rc.(sub2api.PaymentsReadClient)
	if !ok {
		return httpapi.PlatformOrdersResult{}, connector.NewError(connector.KindNotSupported,
			op, errPlatformPaymentsCapabilityUnavailable)
	}

	page, err := client.ListOrders(ctx, sub2api.OrderFilter{From: in.From, To: in.To, Status: in.Status})
	if err != nil {
		return httpapi.PlatformOrdersResult{}, err
	}
	return sub2APIOrdersResult(page, sub2APIPaymentsSource(mode, cfg)), nil
}

func (q *dynamicPaymentsQuerier) listNewAPI(
	ctx context.Context, in httpapi.PlatformOrdersInput,
) (httpapi.PlatformOrdersResult, error) {
	const op = "platformpayments.newapi.list_orders"
	mode, cfg, err := q.resolveNewAPI(ctx, in.Environment)
	if err != nil {
		return httpapi.PlatformOrdersResult{}, err
	}
	if mode == jobs.NewAPIModeFake && in.Environment == "production" {
		return httpapi.PlatformOrdersResult{}, connector.NewError(connector.KindNotSupported,
			op, jobs.ErrConnectorProductionFake)
	}

	rc, err := jobs.NewNewAPIClientFactory(mode, cfg)(ctx)
	if err != nil {
		return httpapi.PlatformOrdersResult{}, err
	}
	client, ok := rc.(newapi.PaymentsReadClient)
	if !ok {
		return httpapi.PlatformOrdersResult{}, connector.NewError(connector.KindNotSupported,
			op, errPlatformPaymentsCapabilityUnavailable)
	}

	page, err := client.ListOrders(ctx, newapi.OrderFilter{From: in.From, To: in.To, Status: in.Status})
	if err != nil {
		return httpapi.PlatformOrdersResult{}, err
	}
	return newAPIOrdersResult(page, newAPIPaymentsSource(mode, cfg)), nil
}

// resolveSub2API 读一次 core.connector_config(经 30s 缓存)，用行里非空的
// 字段覆盖进程级缺省——与 dynamicUsersClient.resolve 逐字段覆盖同一条纪律。
func (q *dynamicPaymentsQuerier) resolveSub2API(
	ctx context.Context, environment string,
) (jobs.Sub2APIMode, jobs.Sub2APIRealConfig, error) {
	mode := jobs.Sub2APIMode(q.defaultMode)
	cfg := q.sub2apiDefaults
	cfg.Environment = environment
	cfg.Secrets = q.secrets
	if q.configSource == nil {
		return mode, cfg, nil
	}

	row, err := q.configSource.Get(ctx, jobs.ConnectorPlatformSub2API, environment)
	if err != nil {
		// 库读不到：本次按进程级缺省处理，库恢复后自动切回——见
		// dynamicUsersClient.resolve 的同款注释。
		q.logConnectorConfigUnavailable(ctx, jobs.ConnectorPlatformSub2API, environment, err)
		return mode, cfg, nil
	}
	if row == nil {
		return mode, cfg, nil
	}
	parsed, err := jobs.ParseSub2APIMode(row.Mode)
	if err != nil {
		return "", jobs.Sub2APIRealConfig{}, connector.NewError(connector.KindInternal,
			"platformpayments.sub2api.client.mode", err)
	}
	mode = parsed
	if v := strings.TrimSpace(row.Endpoint); v != "" {
		cfg.Endpoint = v
	}
	if hosts := normalizeUsersAllowlist(row.TargetAllowlist); len(hosts) > 0 {
		cfg.TargetAllowlist = hosts
	}
	if v := strings.TrimSpace(row.CredentialRef); v != "" {
		cfg.CredentialRef = v
	}
	return mode, cfg, nil
}

// resolveNewAPI 是 resolveSub2API 的 NewAPI 版本；语义完全相同。
func (q *dynamicPaymentsQuerier) resolveNewAPI(
	ctx context.Context, environment string,
) (jobs.NewAPIMode, jobs.NewAPIRealConfig, error) {
	mode := jobs.NewAPIMode(q.defaultMode)
	cfg := q.newapiDefaults
	cfg.Environment = environment
	cfg.Secrets = q.secrets
	if q.configSource == nil {
		return mode, cfg, nil
	}

	row, err := q.configSource.Get(ctx, jobs.ConnectorPlatformNewAPI, environment)
	if err != nil {
		q.logConnectorConfigUnavailable(ctx, jobs.ConnectorPlatformNewAPI, environment, err)
		return mode, cfg, nil
	}
	if row == nil {
		return mode, cfg, nil
	}
	parsed, err := jobs.ParseNewAPIMode(row.Mode)
	if err != nil {
		return "", jobs.NewAPIRealConfig{}, connector.NewError(connector.KindInternal,
			"platformpayments.newapi.client.mode", err)
	}
	mode = parsed
	if v := strings.TrimSpace(row.Endpoint); v != "" {
		cfg.Endpoint = v
	}
	if hosts := normalizeUsersAllowlist(row.TargetAllowlist); len(hosts) > 0 {
		cfg.TargetAllowlist = hosts
	}
	if v := strings.TrimSpace(row.CredentialRef); v != "" {
		cfg.CredentialRef = v
	}
	return mode, cfg, nil
}

func (q *dynamicPaymentsQuerier) logConnectorConfigUnavailable(
	ctx context.Context, platform, environment string, err error,
) {
	if q.logger == nil {
		return
	}
	q.logger.LogAttrs(ctx, slog.LevelWarn, "platform_payments_connector_config_unavailable",
		slog.String("event", "platform_payments_connector_config_unavailable"),
		slog.String("module", "platform.api.platformpayments"),
		slog.String("platform", platform),
		slog.String("environment", environment),
		slog.String("error_code", "connector_config_unavailable"),
		slog.String("detail", err.Error()),
		slog.String("hint", "本次按 XM_PLATFORM_PAYMENTS_MODE 缺省处理;库恢复后自动切回 core.connector_config"))
}

// sub2APIPaymentsSource / newAPIPaymentsSource 给出这批数据的来源标识
// (data_source)，供前端判断"这是不是演示数据"——与 platformUserPage.DataSource
// 同一条纪律。fake 模式恒定报 "<platform>-fake"，**不依赖运维是否记得把
// InstanceID 配成带 -fake 后缀**：告警面板与用户清单靠的是各自约定俗成的
// 命名习惯，这里直接从模式本身推导，更可靠。
func sub2APIPaymentsSource(mode jobs.Sub2APIMode, cfg jobs.Sub2APIRealConfig) string {
	if mode != jobs.Sub2APIModeReal {
		return "sub2api-fake"
	}
	if id := strings.TrimSpace(cfg.InstanceID); id != "" {
		return id
	}
	return jobs.DefaultSub2APIInstanceID
}

func newAPIPaymentsSource(mode jobs.NewAPIMode, cfg jobs.NewAPIRealConfig) string {
	if mode != jobs.NewAPIModeReal {
		return "newapi-fake"
	}
	if id := strings.TrimSpace(cfg.InstanceID); id != "" {
		return id
	}
	return jobs.DefaultNewAPIInstanceID
}

// sub2APIOrdersResult / newAPIOrdersResult 把连接器的 OrderPage 转成
// httpapi 的平台无关投影——响应体是契约，不是连接器结构体的倒影
// （规格 §18.4，同 platformUserItem 顶部的说明）。
func sub2APIOrdersResult(page sub2api.OrderPage, source string) httpapi.PlatformOrdersResult {
	items := make([]httpapi.PlatformOrderItem, 0, len(page.Items))
	for _, o := range page.Items {
		items = append(items, httpapi.PlatformOrderItem{
			OrderID:          o.OrderID,
			CreatedAt:        o.CreatedAt,
			Status:           o.Status,
			AmountMinorUnits: o.AmountMinorUnits,
			Currency:         o.Currency,
			Method:           o.Method,
			UserRef:          o.UserRef,
			UpstreamOrderRef: o.UpstreamOrderRef,
		})
	}
	stats := make(map[string]httpapi.PlatformOrderStat, len(page.StatsByStatus))
	currency := ""
	for status, s := range page.StatsByStatus {
		stats[status] = httpapi.PlatformOrderStat{Count: s.Count, AmountMinorUnits: s.AmountMinorUnits}
	}
	if len(items) > 0 {
		// 展示币种取第一笔订单的币种：多数窗口内所有订单同币种，
		// 混合币种的窗口本就已经被 IsPartial 标记，这里给个代表值即可，
		// 不是权威汇总(权威汇总是 stats 里按状态各自的 amount)。
		currency = items[0].Currency
	}
	return httpapi.PlatformOrdersResult{
		Items:         items,
		StatsByStatus: stats,
		Currency:      currency,
		Source:        source,
		ObservedAt:    page.ObservedAt,
		Watermark:     page.Watermark,
		IsPartial:     page.IsPartial,
	}
}

func newAPIOrdersResult(page newapi.OrderPage, source string) httpapi.PlatformOrdersResult {
	items := make([]httpapi.PlatformOrderItem, 0, len(page.Items))
	for _, o := range page.Items {
		items = append(items, httpapi.PlatformOrderItem{
			OrderID:          o.OrderID,
			CreatedAt:        o.CreatedAt,
			Status:           o.Status,
			AmountMinorUnits: o.AmountMinorUnits,
			Currency:         o.Currency,
			Method:           o.Method,
			UserRef:          o.UserRef,
			UpstreamOrderRef: o.UpstreamOrderRef,
		})
	}
	stats := make(map[string]httpapi.PlatformOrderStat, len(page.StatsByStatus))
	currency := ""
	for status, s := range page.StatsByStatus {
		stats[status] = httpapi.PlatformOrderStat{Count: s.Count, AmountMinorUnits: s.AmountMinorUnits}
	}
	if len(items) > 0 {
		currency = items[0].Currency
	}
	return httpapi.PlatformOrdersResult{
		Items:         items,
		StatsByStatus: stats,
		Currency:      currency,
		Source:        source,
		ObservedAt:    page.ObservedAt,
		Watermark:     page.Watermark,
		IsPartial:     page.IsPartial,
	}
}

// loadSub2APIPaymentsDefaults / loadNewAPIPaymentsDefaults 读取与
// cmd/platform-worker 完全相同的一组 XM_SUB2API_*/XM_NEWAPI_* 环境变量，
// 作为 core.connector_config 没有行时的进程级缺省。
//
// 两个 main 包互相不能引用对方的未导出函数(cmd/platform-worker 与
// cmd/platform-api 是独立二进制)，重复解析同一组变量名是唯一可行的做法——
// 与 platformusers.go 的 normalizeUsersAllowlist 顶部注释同一条理由。
// Secrets/InstanceID 之外的字段留给调用方(main.go)按环境变量填充。
func loadSub2APIPaymentsDefaults() jobs.Sub2APIRealConfig {
	cfg := jobs.Sub2APIRealConfig{
		Endpoint:        strings.TrimSpace(os.Getenv("XM_SUB2API_ENDPOINT")),
		TargetAllowlist: normalizeUsersAllowlist(strings.Split(os.Getenv("XM_SUB2API_TARGET_ALLOWLIST"), ",")),
		CredentialRef:   strings.TrimSpace(os.Getenv("XM_SUB2API_CREDENTIAL_REF")),
		InstanceID:      strings.TrimSpace(os.Getenv("XM_SUB2API_INSTANCE_ID")),
		Timeout:         10 * time.Second,
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = jobs.DefaultSub2APIInstanceID
	}
	return cfg
}

func loadNewAPIPaymentsDefaults() jobs.NewAPIRealConfig {
	cfg := jobs.NewAPIRealConfig{
		Endpoint:        strings.TrimSpace(os.Getenv("XM_NEWAPI_ENDPOINT")),
		TargetAllowlist: normalizeUsersAllowlist(strings.Split(os.Getenv("XM_NEWAPI_TARGET_ALLOWLIST"), ",")),
		CredentialRef:   strings.TrimSpace(os.Getenv("XM_NEWAPI_CREDENTIAL_REF")),
		UserID:          strings.TrimSpace(os.Getenv("XM_NEWAPI_USER_ID")),
		InstanceID:      strings.TrimSpace(os.Getenv("XM_NEWAPI_INSTANCE_ID")),
		Timeout:         10 * time.Second,
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = jobs.DefaultNewAPIInstanceID
	}
	return cfg
}

// platformPaymentsOrNil 把具体类型转成接口，nil 保持 nil——同一个 Go 坑，
// 见 platformUsersOrNil 的注释。
func platformPaymentsOrNil(q *dynamicPaymentsQuerier) httpapi.PlatformOrdersQuerier {
	if q == nil {
		return nil
	}
	return q
}
