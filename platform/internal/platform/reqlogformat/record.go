package reqlogformat

import (
	"regexp"
	"time"
)

// CST 是记录代理落盘时用的时区（UTC+8）。
//
// 为什么磁盘格式里有时区而不是全 UTC（与宪法 14 条"时间库内 UTC"看似冲突）：
// 这不是新决定——它是桌面端原型 reqlogger.go 已经在生产写了的既有格式
// （按天分目录、`Record.Time` 字段都按 CST 计算），XM-REQLOG-MERGE 的任务是
// "记录格式与 index.jsonl 逐字段保持兼容"，不是重新设计它。真正进宪法 14 条
// 管辖的是**平台侧**：连接器（connectors/reqlog 的 NewFileClient）从 TsMs
// 反解出 OccurredAt 时**必须**转成 UTC 再交给契约层（RequestLogSummary.
// OccurredAt 的 Location() 必须是 time.UTC，contracttest 断言了这一点）。
// CST 只活在"按哪个时区的日历日分目录"这一件事上。
var CST = time.FixedZone("CST", 8*3600)

// DayDir 按 CST 日历日把时间戳格式化成落盘用的目录名（YYYYMMDD）。
//
// 与 reqlogger.go 原实现的 `time.UnixMilli(fr.TsMs).In(cst).Format("20060102")`
// 逐字一致——这是磁盘布局的一部分，改了写侧的这一行，读侧的目录扫描就找不到
// 文件，症状是"数据明明在，列表却是空的"。
func DayDir(t time.Time) string {
	return t.In(CST).Format("20060102")
}

// DayDirPattern 匹配落盘的按天目录名。
var DayDirPattern = regexp.MustCompile(`^[0-9]{8}$`)

// RecordIDPattern 匹配单条记录的 id（不含目录）：HHMMSS-NNNNNN。
//
// 与原实现 `fmt.Sprintf("%s-%06d", start.In(cst).Format("150405"), seq...)`
// 一致：六位时间 + 六位序号，来自跨 newapi/sub2api 两个来源共享的单个原子
// 计数器（见 cmd/reqlog-recorder 的 Recorder.seq），因此在同一个日历日目录
// 内，不论来源都不会重号。
var RecordIDPattern = regexp.MustCompile(`^[0-9]{6}-[0-9]{6}$`)

// Record 是索引行（index.jsonl 单行）与详情记录共用的元数据部分。
//
// **字段与 JSON 标签必须与桌面端原型 reqlogger.go 的同名类型逐字段保持一致**
// ——这是老数据能否被新代码读出来的唯一保证。改动任何一个 json 标签都是一次
// 破坏磁盘格式兼容性的变更，不得在本次收编中顺手做。
type Record struct {
	ID       string `json:"id"`
	TsMs     int64  `json:"ts_ms"`
	Time     string `json:"time"`
	Source   string `json:"source"` // newapi | sub2api
	Method   string `json:"method"`
	Path     string `json:"path"`
	Status   int    `json:"status"`
	DurMs    int64  `json:"dur_ms"`
	TtfbMs   int64  `json:"ttfb_ms"`
	ClientIP string `json:"client_ip"`
	UA       string `json:"ua"`
	TokenPfx string `json:"token_prefix"`
	UpReqID  string `json:"upstream_request_id"` // X-Oneapi-Request-Id / X-Request-Id
	RespID   string `json:"resp_body_id"`        // resp_/msg_/chatcmpl-
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	ReqSize  int    `json:"req_size"`
	RespSize int    `json:"resp_size"`
	// Truncated 表示磁盘 gz 明细里的 req_body/resp_body 已被写侧截断
	// （原始上限 64MB/256MB，见 cmd/reqlog-recorder 的 Config）。
	// 与契约层 MaxRawPayloadBytes（512KiB）是两回事：那是读侧再截一次给前端。
	Truncated bool   `json:"truncated,omitempty"`
	EndNote   string `json:"end_note,omitempty"`
	Preview   string `json:"preview,omitempty"`
	InTok     int    `json:"in_tok,omitempty"`
	OutTok    int    `json:"out_tok,omitempty"`
	CacheTok  int    `json:"cache_tok,omitempty"`
}

// FullRecord 是单条记录的完整落盘形态（<id>.json.gz 的内容）。
type FullRecord struct {
	Record
	ReqHeaders  map[string]string `json:"req_headers"`
	ReqBody     string            `json:"req_body"`
	RespHeaders map[string]string `json:"resp_headers"`
	RespBody    string            `json:"resp_body"`
}

// MeasuredTTFB 判定 Record.TtfbMs 是不是一次真实测量，而不是"从未测过"的
// Go 零值。
//
// 磁盘格式本身**不区分**这两种情况：写侧只在响应体读到第一个字节时才写
// TtfbMs（见 cmd/reqlog-recorder 的 teeBody.Read），如果响应体从头到尾一个
// 字节都没读到，TtfbMs 就停留在 Go 的零值 0——这与"首字节刚好 0 毫秒到达"
// （缓存命中，契约样本 #6 特意覆盖的场景）在磁盘上长得一模一样。
//
// 用 RespSize 消歧：first 只会在 `n>0` 的那次 Read 时被置位，而 RespSize 是
// 同一个缓冲区的最终长度——RespSize>0 就意味着 first 曾被置位过，TtfbMs
// 因此是一次真实测量（哪怕测量值恰好是 0）；RespSize==0 则 TtfbMs 从未被
// 写过，只是 Go 零值。这个推导只依赖已经落盘的字段，不依赖任何新增字段，
// 因此对老数据同样成立。
func (r Record) MeasuredTTFB() bool {
	return r.RespSize > 0
}
