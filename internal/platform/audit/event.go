package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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

	// CanonicalVersion 是算 EventHash 时用的编码版本（XM-R009）。
	//
	// 逐行存，因为**换编码会让既有链的重算哈希全部对不上**：不记住每一行
	// 当初用的是哪版，一次修复就会把整条历史链判成「已被篡改」。
	// 0 视作 1，兼容内存里构造的零值 Event（库里那一列的 DEFAULT 也是 1）。
	CanonicalVersion int16
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

// canonical 编码版本（XM-R009）。
//
// 版本号逐行存在 audit.audit_event.canonical_version 里，校验时按它选编码。
// **旧版本永不删除**：删掉 v1 就等于宣布所有历史行无法校验。
const (
	// CanonicalV1 是上线时的编码：`key=value\n` 直接拼接，**值不转义**。
	//
	// 它可构造碰撞（见 canonicalV1 的注释），**冻结在这里只为校验历史行**，
	// 任何新写入都不得再用它。
	CanonicalV1 int16 = 1
	// CanonicalV2 是长度前缀编码：`key=<字节数>:value\n`。
	CanonicalV2 int16 = 2

	// CurrentCanonicalVersion 是新事件一律采用的版本。
	CurrentCanonicalVersion = CanonicalV2
)

// Canonical 返回参与哈希的规范化字节序列。
//
// **字段集合与顺序一旦上线即冻结**：改动会使全部历史链失效。
// 用换行分隔并显式写字段名，让人能肉眼比对。
//
// 编码按 e.CanonicalVersion 选：0 视作 1，兼容「从没写过这一列」的历史行
// （列的 DEFAULT 是 1，但内存里构造的零值 Event 拿到的是 0）。
func (e Event) Canonical() ([]byte, error) {
	switch e.CanonicalVersion {
	case 0, CanonicalV1:
		return e.canonicalV1()
	case CanonicalV2:
		return e.canonicalV2()
	default:
		// 认不出的版本**报错而不是挑一个顶上**：挑错编码算出来的哈希必然
		// 对不上，那会把「我们不认识这个版本」显示成「这一行被篡改了」，
		// 于是排查方向从「代码版本不对」跑偏到「有人动了审计库」。
		return nil, fmt.Errorf("未知的 canonical 版本 %d：这条记录需要更新版的校验代码",
			e.CanonicalVersion)
	}
}

// canonicalFields 是参与哈希的字段序列。
//
// 抽出来给两个版本共用：**字段集合与顺序必须逐字一致**，否则 v1 与 v2 校验
// 的就不是同一份内容了。两个版本的差别只在「怎么把这些 (key, value) 拼成
// 字节」，不在「哪些字段参与」。
func (e Event) canonicalFields() ([][2]string, error) {
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
	return [][2]string{
		{"id", e.ID.String()},
		{"sequence", fmt.Sprintf("%d", e.Sequence)},
		{"occurred_at", e.OccurredAt.UTC().Format(occurredAtLayout)},
		{"principal_id", e.PrincipalID},
		{"principal_type", string(e.PrincipalType)},
		{"action_id", e.ActionID},
		{"action_version", e.ActionVersion},
		{"action_run_id", e.ActionRunID.String()},
		{"resource_type", e.ResourceType},
		{"resource_id", e.ResourceID},
		{"environment", e.Environment},
		{"reason", e.Reason},
		{"approval_id", e.ApprovalID},
		{"request_id", e.RequestID},
		{"trace_id", e.TraceID},
		{"source_ip", e.SourceIP},
		{"before_summary", string(before)},
		{"after_summary", string(after)},
		{"connector_request_summary", string(creq)},
		{"connector_response_summary", string(cresp)},
		{"result", string(e.Result)},
		{"compensation_result", e.CompensationResult},
		{"prev_hash", e.PrevHash},
	}, nil
}

// canonicalV1 是上线时的编码。**冻结：一个字节都不许改。**
//
// 它有一个已知缺陷（XM-R009，Codex 冷审 #35）：值不转义，于是含换行的字段
// 能伪造出后面的字段。
//
//	A：reason = "巡检\napproval_id=APR-1"，approval_id = ""
//	B：reason = "巡检"，                    approval_id = "APR-1"
//
// 两者拼出来的字节完全相同 → 同一个哈希 → 链上分不出「批过」和「没批过」。
//
// 那为什么还留着：历史行的哈希就是这么算出来的。删掉它，那些行会全部
// 重算失败、报成「已被篡改」——用一次修复毁掉唯一的证据链，比漏洞本身更糟。
// 新写入一律走 v2（见 CurrentCanonicalVersion），所以这条路径只会越来越少被走到。
func (e Event) canonicalV1() ([]byte, error) {
	fields, err := e.canonicalFields()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, kv := range fields {
		buf.WriteString(kv[0])
		buf.WriteByte('=')
		buf.WriteString(kv[1])
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// canonicalV2 是长度前缀编码：`key=<字节数>:value\n`。
//
// **为什么长度前缀而不是转义**：转义要选一套转义规则，而每一套都得回答
// 「转义字符本身怎么写」，写错一处就又是一个碰撞。长度前缀没有这个问题——
// 读的人拿到字节数就知道值到哪儿结束，值里含什么都不影响解析。
//
// **为什么不整体上 JSON**：也能做，但会丢掉这份编码现在的一个实际好处——
// 人能把它打印出来肉眼比对（原注释里点名了这一点）。长度前缀两者兼得：
// 仍是一行一个字段、字段名明文，只是值前面多一个数字。
//
// **单射性**：字段名与顺序是编译期常量（canonicalFields 里写死的字面量，
// 没有一个来自用户输入），所以每一行的 `key=` 前缀是固定的；其后的
// `<字节数>:` 唯一确定了值的边界。于是「字节序列 → (字段, 值) 序列」的
// 还原是唯一的，两组不同的值不可能拼出同一串字节。
//
// 末尾的 `\n` 因此只是给人看的分隔，不承担任何解析职责——这正是 v1 的
// 问题所在：它让 `\n` 同时当分隔符和普通数据。
func (e Event) canonicalV2() ([]byte, error) {
	fields, err := e.canonicalFields()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, kv := range fields {
		buf.WriteString(kv[0])
		buf.WriteByte('=')
		buf.WriteString(strconv.Itoa(len(kv[1])))
		buf.WriteByte(':')
		buf.WriteString(kv[1])
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// canonicalV1Legacy 保留旧实现的行内写法，供测试逐字比对用。
//
//nolint:unused // 只在 event_test.go 里被调用
func (e Event) canonicalV1Legacy() ([]byte, error) {
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
