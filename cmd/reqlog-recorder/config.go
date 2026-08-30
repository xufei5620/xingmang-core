package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// 原值默认——与桌面端原型 reqlogger.go 的硬编码常量逐项对应（同名常量：
// dataDir/authFile/tokenMapFile/retainDays/maxReqBody/maxRespBody，以及
// main() 里写死的两个监听地址与两个上游地址）。不传任何 flag/环境变量时，
// reqlog-recorder 的落盘位置、监听端口、上游目标、保留期、抓包上限与老版本
// 完全一致——这是"收编"而不是"重写"的前提：生产上已经在跑的行为不因为
// 搬进仓库而改变。
const (
	DefaultDataDir         = "/root/reqlog/data"
	DefaultListenNewAPI    = "127.0.0.1:9301"
	DefaultUpstreamNewAPI  = "http://127.0.0.1:3000"
	DefaultListenSub2API   = "127.0.0.1:9302"
	DefaultUpstreamSub2API = "http://127.0.0.1:8081"
	DefaultTokenMapPath    = "/root/reqlog/tokenmap.json"
	DefaultRetentionDays   = 30
	DefaultMaxReqBody      = 64 << 20  // 64MB，原值 maxReqBody
	DefaultMaxRespBody     = 256 << 20 // 256MB，原值 maxRespBody
	DefaultTokenMapRefresh = 10 * time.Minute

	// 以下三项是**新增默认**，不是"原值"：老版本硬编码目录 0700、文件 0600，
	// 只有 root 能读。新默认让平台容器（UID 10001，只读挂载）能够读到数据，
	// 不必再靠"平台连 root 的东西"这种更危险的方式。改这三项不影响谁能
	// **写**（仍然只有 root，本进程仍以 User=root 运行），只放宽同组的读权限。
	DefaultDirPerm  os.FileMode = 0o750
	DefaultFilePerm os.FileMode = 0o640
	// DefaultGroupID 是星芒平台容器的运行 UID（deploy/docker/go.Dockerfile
	// 里 `adduser -D -u 10001 ... xingmang`），这里借用同一个数字作为 GID：
	// 只读挂载卷是按 GID 判权限的，让新建的目录/文件同组即可，不需要新增
	// 一个专门的 reqlog 用户组。
	DefaultGroupID = 10001
)

// Config 是 reqlog-recorder 的运行配置。
type Config struct {
	DataDir         string
	ListenNewAPI    string
	UpstreamNewAPI  string
	ListenSub2API   string
	UpstreamSub2API string
	TokenMapPath    string
	RetentionDays   int
	MaxReqBody      int64
	MaxRespBody     int64
	DirPerm         os.FileMode
	FilePerm        os.FileMode
	GroupID         int
	TokenMapRefresh time.Duration
}

func (c Config) validate() error {
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("data-dir 不能为空")
	}
	if strings.TrimSpace(c.ListenNewAPI) == "" || strings.TrimSpace(c.ListenSub2API) == "" {
		return fmt.Errorf("listen-newapi / listen-sub2api 不能为空")
	}
	if strings.TrimSpace(c.UpstreamNewAPI) == "" || strings.TrimSpace(c.UpstreamSub2API) == "" {
		return fmt.Errorf("upstream-newapi / upstream-sub2api 不能为空")
	}
	if c.RetentionDays <= 0 {
		return fmt.Errorf("retention-days 必须为正")
	}
	if c.MaxReqBody <= 0 || c.MaxRespBody <= 0 {
		return fmt.Errorf("max-req-body / max-resp-body 必须为正")
	}
	if c.DirPerm&^0o777 != 0 || c.FilePerm&^0o777 != 0 {
		return fmt.Errorf("dir-perm / file-perm 必须是 0..0777 之间的权限位")
	}
	if c.GroupID < 0 {
		return fmt.Errorf("group-id 不能为负")
	}
	if c.TokenMapRefresh <= 0 {
		return fmt.Errorf("tokenmap-refresh 必须为正")
	}
	return nil
}

// envOrDefault 系列：flag 的默认值取自环境变量，环境变量没设时退回硬编码
// 原值——"flag 或环境变量，带原值默认"三者在这里是同一条链：命令行 flag
// 优先，其次环境变量，最后原值。这是本包里唯一读 os.Getenv 风格输入的地方，
// 其余代码只吃解析好的 Config。
func envOrDefault(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

func envOrDefaultInt(getenv func(string) string, key string, def int) int {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envOrDefaultInt64(getenv func(string) string, key string, def int64) int64 {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func envOrDefaultDuration(getenv func(string) string, key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// parseFileMode 把八进制权限字符串（"0750"、"750"、"0o750" 都接受）解析成
// os.FileMode。
func parseFileMode(s string, def os.FileMode) (os.FileMode, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	s = strings.TrimPrefix(s, "0o")
	s = strings.TrimPrefix(s, "0O")
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("权限位 %q 不是合法的八进制数: %w", s, err)
	}
	return os.FileMode(n), nil
}

func envOrDefaultFileMode(getenv func(string) string, key string, def os.FileMode) (os.FileMode, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def, nil
	}
	return parseFileMode(v, def)
}

// ParseConfig 解析命令行 flag，flag 的默认值来自对应环境变量（没设就用
// 原值/新默认）。
//
// 环境变量命名统一加 `XM_REQLOG_RECORDER_` 前缀，与 cmd/platform-api 那边
// 消费方用的 `XM_REQLOG_*`（XM_REQLOG_MODE/XM_REQLOG_DATA_DIR/…）刻意分开
// ——两个是不同进程的配置，前缀相同容易让人以为改一个就影响另一个。
// 唯一例外是 `XM_REQLOG_GID`（无 `_RECORDER_` 中缀）：这是交接指示里
// 显式点名的变量，与之对齐。
func ParseConfig(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("reqlog-recorder", flag.ContinueOnError)

	cfg := Config{}
	fs.StringVar(&cfg.DataDir, "data-dir",
		envOrDefault(getenv, "XM_REQLOG_RECORDER_DATA_DIR", DefaultDataDir),
		"落盘数据根目录（按天分子目录）")
	fs.StringVar(&cfg.ListenNewAPI, "listen-newapi",
		envOrDefault(getenv, "XM_REQLOG_RECORDER_LISTEN_NEWAPI", DefaultListenNewAPI),
		"newapi 代理监听地址")
	fs.StringVar(&cfg.UpstreamNewAPI, "upstream-newapi",
		envOrDefault(getenv, "XM_REQLOG_RECORDER_UPSTREAM_NEWAPI", DefaultUpstreamNewAPI),
		"newapi 上游地址")
	fs.StringVar(&cfg.ListenSub2API, "listen-sub2api",
		envOrDefault(getenv, "XM_REQLOG_RECORDER_LISTEN_SUB2API", DefaultListenSub2API),
		"sub2api 代理监听地址")
	fs.StringVar(&cfg.UpstreamSub2API, "upstream-sub2api",
		envOrDefault(getenv, "XM_REQLOG_RECORDER_UPSTREAM_SUB2API", DefaultUpstreamSub2API),
		"sub2api 上游地址")
	fs.StringVar(&cfg.TokenMapPath, "tokenmap-path",
		envOrDefault(getenv, "XM_REQLOG_RECORDER_TOKENMAP", DefaultTokenMapPath),
		"令牌前缀→用户名映射文件路径")
	fs.IntVar(&cfg.RetentionDays, "retention-days",
		envOrDefaultInt(getenv, "XM_REQLOG_RECORDER_RETENTION_DAYS", DefaultRetentionDays),
		"保留天数（按 CST 日历日清理）")
	fs.Int64Var(&cfg.MaxReqBody, "max-req-body",
		envOrDefaultInt64(getenv, "XM_REQLOG_RECORDER_MAX_REQ_BODY", DefaultMaxReqBody),
		"单条请求体抓取上限（字节）")
	fs.Int64Var(&cfg.MaxRespBody, "max-resp-body",
		envOrDefaultInt64(getenv, "XM_REQLOG_RECORDER_MAX_RESP_BODY", DefaultMaxRespBody),
		"单条响应体抓取上限（字节）")
	fs.IntVar(&cfg.GroupID, "group-id",
		envOrDefaultInt(getenv, "XM_REQLOG_GID", DefaultGroupID),
		"新建目录/文件的属组 GID（0 表示不做 chgrp）")
	fs.DurationVar(&cfg.TokenMapRefresh, "tokenmap-refresh",
		envOrDefaultDuration(getenv, "XM_REQLOG_RECORDER_TOKENMAP_REFRESH", DefaultTokenMapRefresh),
		"令牌映射刷新间隔")

	dirPermDefault, err := envOrDefaultFileMode(getenv, "XM_REQLOG_RECORDER_DIR_PERM", DefaultDirPerm)
	if err != nil {
		return Config{}, err
	}
	filePermDefault, err := envOrDefaultFileMode(getenv, "XM_REQLOG_RECORDER_FILE_PERM", DefaultFilePerm)
	if err != nil {
		return Config{}, err
	}
	dirPermFlag := fs.String("dir-perm", fmt.Sprintf("%04o", dirPermDefault), "新建目录权限位（八进制）")
	filePermFlag := fs.String("file-perm", fmt.Sprintf("%04o", filePermDefault), "新建文件权限位（八进制）")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if cfg.DirPerm, err = parseFileMode(*dirPermFlag, DefaultDirPerm); err != nil {
		return Config{}, fmt.Errorf("--dir-perm: %w", err)
	}
	if cfg.FilePerm, err = parseFileMode(*filePermFlag, DefaultFilePerm); err != nil {
		return Config{}, fmt.Errorf("--file-perm: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
