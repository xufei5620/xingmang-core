package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/requestlog"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// reqlogTokenEnvVar 是 reqlog 控制台 Basic Auth 凭据在 env Provider 下的落点。
//
// Connector **不认识**这个名字：它只拿到 secret://<scope>/<name> 形式的引用，
// 由这里的登记表决定去哪儿取（ADR-014）。将来换 SOPS/Vault，改的只有本文件。
//
// 值的形态是 `用户名:口令` 整串——理由见 reqlog.applyBasicAuth 的注释：
// 把用户名单独放进配置，等于让凭据的一半有了一个不受 CredentialRef 管辖的落点。
const reqlogTokenEnvVar = "XM_REQLOG_TOKEN"

// reqlogMode 决定「请求详情」用哪个 ReadClient 实现。
type reqlogMode string

const (
	// reqlogModeFake 用 reqlog.NewFake：控制台 API 形状核实之前唯一走得通的模式。
	reqlogModeFake reqlogMode = "fake"
	// reqlogModeReal 走真实只读客户端骨架；数据方法目前一律 not_supported。
	reqlogModeReal reqlogMode = "real"
	// reqlogModeOff 完全不挂载「请求」两个端点。
	//
	// 比 fake 多出来的这一档是必要的：一个没部署 reqlog 的环境，
	// 端点**不存在**（404）比端点存在却只回演示数据诚实得多——
	// 后者会让前端把演示对话渲染成真实用户的问答。
	reqlogModeOff reqlogMode = "off"
)

// parseReqlogMode 解析模式，空串按 off 处理。
//
// 默认 off 而不是 fake：这条通道读的是**用户与模型的完整对话**，
// 而 fake 模式会把一批编出来的对话摆进一个长得像真的详情页。别的连接器默认
// fake 的代价是「看板上几个数字是假的」，这里的代价是「有人以为自己在看
// 真实用户问了什么」——默认值必须选那个更难出错的方向。
//
// staging 要演示就显式配 XM_REQLOG_MODE=fake，那是一次有意的选择。
func parseReqlogMode(s string) (reqlogMode, error) {
	switch mode := reqlogMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return reqlogModeOff, nil
	case reqlogModeFake, reqlogModeReal, reqlogModeOff:
		return mode, nil
	default:
		return "", fmt.Errorf("XM_REQLOG_MODE %q: 只接受 off / fake / real", s)
	}
}

// reqlogConfig 是「请求详情」这条链路的配置。
type reqlogConfig struct {
	Mode reqlogMode
	// 以下三项只在 real 模式用到。**只读进配置、不解析**：
	// 凭据只经 CredentialRef，明文由 SecretProvider 在拼 Authorization 头的
	// 那一瞬才出现（ADR-014、宪法 7 条）。
	Endpoint        string
	TargetAllowlist []string
	CredentialRef   string
	Timeout         time.Duration
}

// defaultReqlogTimeout 是单次控制台读取的超时。
//
// 比 sub2api 的 15s 宽一点：这条通道要取回完整 SSE 流，单条记录可以到几十 MB，
// 而它又不在任何热路径上——一次由人点击触发的详情读取，多等几秒好过读不回来。
const defaultReqlogTimeout = 30 * time.Second

func reqlogConfigFromEnv(getenv func(string) string) (reqlogConfig, error) {
	mode, err := parseReqlogMode(getenv("XM_REQLOG_MODE"))
	if err != nil {
		return reqlogConfig{}, err
	}
	c := reqlogConfig{
		Mode:            mode,
		Endpoint:        strings.TrimSpace(getenv("XM_REQLOG_ENDPOINT")),
		TargetAllowlist: parseHostAllowlist(getenv("XM_REQLOG_TARGET_ALLOWLIST")),
		CredentialRef:   strings.TrimSpace(getenv("XM_REQLOG_CREDENTIAL_REF")),
		Timeout:         defaultReqlogTimeout,
	}
	if v := strings.TrimSpace(getenv("XM_REQLOG_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return reqlogConfig{}, fmt.Errorf("XM_REQLOG_TIMEOUT: %w", err)
		}
		if d <= 0 {
			return reqlogConfig{}, fmt.Errorf("XM_REQLOG_TIMEOUT 必须为正, got %s", v)
		}
		c.Timeout = d
	}
	return c, nil
}

// parseHostAllowlist 把逗号分隔的主机清单拆成精确匹配用的切片。
//
// 只做拆分、去空白、转小写——**不做**任何补全或推断（比如从 endpoint 猜一个
// 主机塞进去）。allowlist 的全部价值就在于它是人显式写下的那一份，
// 系统替人填进去的那一项等于没有。与 cmd/platform-worker 里的同名函数一致。
func parseHostAllowlist(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		host := strings.ToLower(strings.TrimSpace(part))
		if host != "" {
			out = append(out, host)
		}
	}
	return out
}

// requestLogsOrNil 把「没启用」翻译成一个**真正为 nil 的接口值**。
//
// 直接把 `(*requestlog.Service)(nil)` 赋给接口字段，得到的接口**不是 nil**
// （它带着类型信息），于是 router 里的 `d.RequestLogs != nil` 为真，端点照挂，
// 第一次调用就 panic。这是 Go 里最经典的一个坑，而它在这里的后果是
// 「本该 404 的端点变成 500」——正是我们特意区分开的那两种状态。
func requestLogsOrNil(s *requestlog.Service) httpapi.RequestLogQuerier {
	if s == nil {
		return nil
	}
	return s
}

// newRequestLogService 按配置装配「请求详情」查询入口。
//
// 返回 (nil, nil) 表示这条链路没有启用——路由因此不挂载那两个端点。
// 「没启用」不是错误：reqlog 是一个外挂系统，没部署它的环境照样该起得来。
//
// 生产禁 fake：与 worker 那边对 Sub2API 的处理同一条纪律，但理由更硬一档。
// 那边的 fake 会让看板上几个数字是假的；这里的 fake 会让一个标着真实用户名的
// 详情页显示一段编出来的对话——有人会拿它去回复客诉、去做风控判断。
func newRequestLogService(
	cfg reqlogConfig, environment string, sink *audit.Store, logger *slog.Logger,
) (*requestlog.Service, error) {
	switch cfg.Mode {
	case reqlogModeOff:
		logger.Info("reqlog_disabled",
			slog.String("module", "platform.api"),
			slog.String("detail", "XM_REQLOG_MODE 未配置或为 off：请求详情端点不挂载"))
		return nil, nil

	case reqlogModeFake:
		if environment == "production" {
			return nil, fmt.Errorf(
				"XM_REQLOG_MODE=fake 不允许在生产环境使用：请求详情页会把编造的对话" +
					"显示成真实用户的问答。生产请配 real，或显式设为 off")
		}
		logger.Warn("reqlog_fake_mode",
			slog.String("module", "platform.api"),
			slog.String("detail", "请求详情返回演示数据，不是真实用户对话"))
		return requestlog.NewService(reqlog.NewFake(reqlog.FakeOptions{}), sink)

	case reqlogModeReal:
		client, err := newReqlogClient(cfg, environment, logger)
		if err != nil {
			return nil, err
		}
		return requestlog.NewService(client, sink)

	default:
		return nil, fmt.Errorf("未知的 reqlog 模式 %q", cfg.Mode)
	}
}

// newReqlogClient 构造真实只读客户端。
//
// ⚠️ 客户端今天读不出数据：控制台 API 形状未核实，数据方法返回 not_supported
// （见 connectors/reqlog/client.go）。仍然把它接起来而不是也走 off，理由是
// **配置链路要能被验证**：endpoint 拼错、allowlist 漏填、凭据引用格式不对，
// 这些在启动时就该被发现，而不是等真实 API 补上那天才一起暴露。
//
// 配置不全时**启动即拒**（与 worker 那边「写一条 SyncFailed 观测继续跑」相反）：
// 那边是后台采集，一条通道配错不该拖垮心跳；这里是一个由人点击触发的只读端点，
// 让它带着半套配置起来，症状会是「点进去报 502，查半天发现 allowlist 是空的」。
func newReqlogClient(cfg reqlogConfig, environment string, logger *slog.Logger) (requestlog.Client, error) {
	if cfg.Endpoint == "" || len(cfg.TargetAllowlist) == 0 || cfg.CredentialRef == "" {
		return nil, fmt.Errorf(
			"XM_REQLOG_MODE=real 需要 XM_REQLOG_ENDPOINT / XM_REQLOG_TARGET_ALLOWLIST / " +
				"XM_REQLOG_CREDENTIAL_REF 三项齐备")
	}
	provider, err := reqlogSecretsFromEnv(cfg.CredentialRef, environment, logger)
	if err != nil {
		return nil, err
	}
	client, err := reqlog.NewClient(connector.Config{
		ServiceInstanceID: "reqlog-" + environment,
		Environment:       environment,
		Endpoint:          cfg.Endpoint,
		CredentialRef:     cfg.CredentialRef,
		TargetAllowlist:   cfg.TargetAllowlist,
		Timeout:           cfg.Timeout,
	}, provider)
	if err != nil {
		return nil, err
	}
	logger.Warn("reqlog_real_mode_incomplete",
		slog.String("module", "platform.api"),
		slog.String("detail", "reqlog 控制台 API 形状未核实：请求详情端点会返回 501（XM-0039）"))
	return client, nil
}

// reqlogSecretsFromEnv 装配 Basic Auth 凭据的 Provider：
// 显式登记的 EnvProvider + 每次读取都留审计的 Audited 装饰器。
func reqlogSecretsFromEnv(
	refText, environment string, logger *slog.Logger,
) (secrets.SecretProvider, error) {
	ref, err := secrets.ParseCredentialRef(refText)
	if err != nil {
		return nil, fmt.Errorf("XM_REQLOG_CREDENTIAL_REF: %w", err)
	}
	provider, err := secrets.NewEnvProvider(
		map[string]string{ref.String(): reqlogTokenEnvVar},
	)
	if err != nil {
		return nil, err
	}
	// 审计装饰器包在外面：每次解析（无论成败）都留一条不含明文的记录，
	// 「这个只读账号什么时候被谁用过」才查得出来（规格 §4.5）
	return secrets.NewAudited(provider, secrets.NewSlogRecorder(logger), environment), nil
}
