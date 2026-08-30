package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/xufei5620/xingmang-platform/connectors/cpa"
	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
)

// cpaMode 决定 CPA「用户管理」逐 key 用量端点用哪个实现（XM-CPA0）。
//
// 与 reqlogMode 同一条纪律，但没有 fake/real 两档：CPA 没有可以伪造响应的
// HTTP 契约——数据源是本机只读挂载的 cpa-manager-plus SQLite 文件
// （connectors/cpa 的包文档），一个"假装读到了"的模式在这里没有意义。
// 要么读真实文件（file），要么不读（off）。与 worker 侧 jobs.CPAMode 同一套
// 两态词汇，但本文件不导入 jobs 包，而是照 reqlogMode 的先例本地重复一份——
// cmd/platform-api 与 cmd/platform-worker 是两个独立的 main 包，各自维护
// 同一套小型解析逻辑，而不是让 API 进程去依赖 worker 专属的 River/Config 包。
type cpaMode string

const (
	cpaModeOff  cpaMode = "off"
	cpaModeFile cpaMode = "file"
)

// parseCPAMode 解析模式，空串按 off 处理。
//
// 默认 off：没配 XM_CPA_MODE 的环境应当保持关闭，而不是每次请求都对着一个
// 不存在的挂载路径报错——与 jobs.ParseCPAMode（worker 侧）同一条理由。
func parseCPAMode(s string) (cpaMode, error) {
	switch mode := cpaMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return cpaModeOff, nil
	case cpaModeOff, cpaModeFile:
		return mode, nil
	default:
		return "", fmt.Errorf("XM_CPA_MODE %q: 只接受 off 或 file", s)
	}
}

// cpaConfig 是 CPA 逐 key 用量端点的配置。
//
// 只影响 GET /api/v1/platforms/cpa/keys——概览与渠道保障两页读的是
// /metrics（cpa.requests.daily 等四条观测），由 internal/platform/jobs.
// CPASyncWorker 周期写入，装配在 cmd/platform-worker，不经本文件。
type cpaConfig struct {
	Mode cpaMode
	// DataDir / FileName 是只读挂载路径，不经 CredentialRef：这是一条挂载
	// 路径，不是向第三方系统认证的凭据（与 reqlogConfig.DataDir 同一条纪律，
	// 见 connectors/cpa.FileConfig 的文档）。
	DataDir  string
	FileName string
}

func cpaConfigFromEnv(getenv func(string) string) (cpaConfig, error) {
	mode, err := parseCPAMode(getenv("XM_CPA_MODE"))
	if err != nil {
		return cpaConfig{}, err
	}
	return cpaConfig{
		Mode:     mode,
		DataDir:  strings.TrimSpace(getenv("XM_CPA_DATA_DIR")),
		FileName: strings.TrimSpace(getenv("XM_CPA_FILE_NAME")),
	}, nil
}

// newCPAKeysQuerier 按配置装配逐 key 用量查询入口。
//
// 返回 (nil, nil) 表示这条链路没有启用——路由因此不挂载
// /platforms/cpa/keys（httpapi.Deps.CPAKeys 的同一条纪律：端点不存在
// 比端点存在却一调就 500 诚实）。
//
// 配置不全时**启动即拒**，与 reqlog file 模式同一条纪律（newReqlogFileClient
// 的注释）：这是一个由人点开「用户管理」页签触发的只读端点，带着半套配置
// 起来的症状是"点进去报错，查半天发现挂载路径是空的"，不如启动时就说清楚。
func newCPAKeysQuerier(cfg cpaConfig, logger *slog.Logger) (httpapi.CPAKeysQuerier, error) {
	switch cfg.Mode {
	case cpaModeOff:
		logger.Info("cpa_disabled",
			slog.String("module", "platform.api"),
			slog.String("detail", "XM_CPA_MODE 未配置或为 off：CPA 用户管理端点不挂载"))
		return nil, nil

	case cpaModeFile:
		if strings.TrimSpace(cfg.DataDir) == "" {
			return nil, fmt.Errorf("XM_CPA_MODE=file 需要 XM_CPA_DATA_DIR 非空")
		}
		client, err := cpa.NewFileClient(cpa.FileConfig{
			DataDir: cfg.DataDir, FileName: cfg.FileName, Logger: logger,
		})
		if err != nil {
			return nil, err
		}
		return cpaKeysOrNil(client), nil

	default:
		return nil, fmt.Errorf("未知的 CPA 模式 %q", cfg.Mode)
	}
}

// cpaKeysOrNil 把 cpa.ReadClient 转成 httpapi.CPAKeysQuerier 接口值，nil 保持
// nil——同 requestLogsOrNil 挡的那个 Go 经典坑：直接把一个带类型信息的 nil
// 具体值塞进接口字段，会让 `d.CPAKeys != nil` 恒为真，端点照挂、一调就 panic。
func cpaKeysOrNil(c cpa.ReadClient) httpapi.CPAKeysQuerier {
	if c == nil {
		return nil
	}
	return c
}
