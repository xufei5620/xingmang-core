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
	write("occurred_at", e.OccurredAt.UTC().Format(time.RFC3339Nano))
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
