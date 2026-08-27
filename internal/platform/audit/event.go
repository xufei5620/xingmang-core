package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// occurredAtLayout 是固定微秒精度的时间格式。
//
// PostgreSQL 的 timestamptz 只到微秒，而 Go 的 time 是纳秒：用 RFC3339Nano
// 会让「写入前算的哈希」与「读回后重算的哈希」对不上，整条链一读就断。
// 固定宽度还能避开 RFC3339Nano 裁剪尾部零的行为差异。
const occurredAtLayout = "2006-01-02T15:04:05.000000Z07:00"

// GenesisHash 是链首事件的 prev_hash。
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// Result 是被审计动作的结果。
type Result string

const (
	ResultSucceeded Result = "succeeded"
	ResultFailed    Result = "failed"
)

// Event 是一条审计事件（字段集合逐字对齐规格 §4.4）。
type Event struct {
	ID       uuid.UUID
	Sequence int64

	OccurredAt time.Time
	// RecordedAt 是库侧写入时刻。**不进哈希**：不同副本可能不同，
	// 让它进链会导致从备份恢复后校验失败。
	RecordedAt time.Time

	PrincipalID   string
	PrincipalType principal.Type
	ActionID      string
	ActionVersion string
	ActionRunID   uuid.UUID
	ResourceType  string
	ResourceID    string
	Environment   string
	Reason        string
	ApprovalID    string
	RequestID     string
	TraceID       string
	SourceIP      string

	BeforeSummary            map[string]any
	AfterSummary             map[string]any
	ConnectorRequestSummary  map[string]any
	ConnectorResponseSummary map[string]any

	Result             Result
	CompensationResult string

	PrevHash  string
	EventHash string
}

// canonicalJSON 把 map 按键排序后序列化，保证确定性。
// Go 的 encoding/json 对 map 已按键排序，但显式走一遍可读且不依赖实现细节。
func canonicalJSON(m map[string]any) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(m[k])
		if err != nil {
			return nil, fmt.Errorf("字段 %q 无法序列化: %w", k, err)
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Canonical 返回参与哈希的规范化字节序列。
//
// **字段集合与顺序一旦上线即冻结**：改动会使全部历史链失效。
// 用换行分隔并显式写字段名，让人能肉眼比对。
func (e Event) Canonical() ([]byte, error) {
	before, err := canonicalJSON(e.BeforeSummary)
	if err != nil {
		return nil, err
	}
	after, err := canonicalJSON(e.AfterSummary)
	if err != nil {
		return nil, err
	}
	creq, err := canonicalJSON(e.ConnectorRequestSummary)
	if err != nil {
		return nil, err
	}
	cresp, err := canonicalJSON(e.ConnectorResponseSummary)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	write := func(k, v string) {
		buf.WriteString(k)
		buf.WriteByte('=')
		buf.WriteString(v)
		buf.WriteByte('\n')
	}
	write("id", e.ID.String())
	write("sequence", fmt.Sprintf("%d", e.Sequence))
	write("occurred_at", e.OccurredAt.UTC().Format(occurredAtLayout))
	write("principal_id", e.PrincipalID)
	write("principal_type", string(e.PrincipalType))
	write("action_id", e.ActionID)
	write("action_version", e.ActionVersion)
	write("action_run_id", e.ActionRunID.String())
	write("resource_type", e.ResourceType)
	write("resource_id", e.ResourceID)
	write("environment", e.Environment)
	write("reason", e.Reason)
	write("approval_id", e.ApprovalID)
	write("request_id", e.RequestID)
	write("trace_id", e.TraceID)
	write("source_ip", e.SourceIP)
	write("before_summary", string(before))
	write("after_summary", string(after))
	write("connector_request_summary", string(creq))
	write("connector_response_summary", string(cresp))
	write("result", string(e.Result))
	write("compensation_result", e.CompensationResult)
	write("prev_hash", e.PrevHash)
	return buf.Bytes(), nil
}

// ComputeHash 计算事件哈希：hex(SHA256(canonical))。
// prev_hash 已包含在 canonical 中，因此链的连接由规范化本身保证。
func (e Event) ComputeHash() (string, error) {
	c, err := e.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:]), nil
}

// normalizeMap 把 map 过一遍 JSON 往返，使其表示与「存进 jsonb 再读回」一致。
//
// 必要性：调用方可能传 int / int64 等 Go 原生类型，而从 jsonb 读回时数字一律
// 是 float64。不做归一化，写入前算的哈希与校验时重算的哈希会不一致——
// 大整数（>2^53）尤其明显。
//
// **这里刻意不用 `json.Decoder.UseNumber()`，与 `internal/platform/ops` 相反**
// （XM-0031）。ops 那边读 value_json 必须保 json.Number，因为它装的是金额的
// minor units，float64 会永久丢精度；本函数的目标不是保精度，而是让写入前的
// 表示与读回后的表示**逐字节相同**——`eventFromRow` 用的是默认
// `json.Unmarshal`（float64），归一化就必须归到同一个表示上，链才校验得过。
// 改成 UseNumber 会让全部历史事件的哈希一次性对不上，等于把整条审计链作废。
//
// 代价是审计摘要里的大整数在链上以 float64 的形态参与哈希，可能丢精度。这被
// 接受，因为审计摘要**不承载金额口径**：它记的是「谁在什么时候改了什么」，
// 金额的权威值在业务表与 ops 指标里。若将来要把金额放进摘要，正确做法是让
// 调用方以 decimal string 写入（字符串不经数字解码，两边表示天然一致），
// 而不是动这里的归一化。
func normalizeMap(m map[string]any) (map[string]any, error) {
	if len(m) == 0 {
		return map[string]any{}, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

// Normalize 返回把四个 summary 归一化后的事件副本，并把 OccurredAt 截断到
// 微秒。Append 在计算哈希前调用它，保证入库表示与校验时的表示完全一致。
func (e Event) Normalize() (Event, error) {
	var err error
	if e.BeforeSummary, err = normalizeMap(e.BeforeSummary); err != nil {
		return Event{}, fmt.Errorf("before_summary: %w", err)
	}
	if e.AfterSummary, err = normalizeMap(e.AfterSummary); err != nil {
		return Event{}, fmt.Errorf("after_summary: %w", err)
	}
	if e.ConnectorRequestSummary, err = normalizeMap(e.ConnectorRequestSummary); err != nil {
		return Event{}, fmt.Errorf("connector_request_summary: %w", err)
	}
	if e.ConnectorResponseSummary, err = normalizeMap(e.ConnectorResponseSummary); err != nil {
		return Event{}, fmt.Errorf("connector_response_summary: %w", err)
	}
	e.OccurredAt = e.OccurredAt.UTC().Truncate(time.Microsecond)
	return e, nil
}
