package reqlog_test

// 本文件构造 NewFileClient 的**合成**磁盘固件：自己生成 index.jsonl 与
// <id>.json.gz，不使用任何真实生产数据、不写像真的令牌字符串（占位统一用
// test-only.invalid 之类明显不可用的值）——按 XM-REQLOG-MERGE 交接指示，
// 固件形状对齐 connectors/reqlog 的 Fake 样本矩阵（长对话触发截断、SSE
// 流式、失败请求、令牌映射不到、跨天分页），但 Channel/Upstream/BilledAmount
// 三项刻意**不**填：磁盘记录格式本身没有这三个字段（见
// contracts/connectors/reqlog.read.v1.md §8 第 21/22 项的核对结论），
// 合成固件如实反映这一点，不为了让某条契约测试变绿而编数据。

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/reqlogformat"
)

// fixtureRecord 是构造一条合成记录的输入。
type fixtureRecord struct {
	day, hhmmss string
	seq         int
	source      string
	status      int
	durMs       int64
	ttfbMs      int64
	clientIP    string
	tokenPfx    string
	model       string
	stream      bool
	inTok       int
	outTok      int
	cacheTok    int
	reqBody     string
	respBody    string
	respCType   string
}

func (r fixtureRecord) id() string { return fmt.Sprintf("%s-%06d", r.hhmmss, r.seq) }

func (r fixtureRecord) tsMs(t *testing.T) int64 {
	t.Helper()
	ts, err := time.ParseInLocation("20060102 150405", r.day+" "+r.hhmmss, reqlogformat.CST)
	if err != nil {
		t.Fatalf("解析固件时间戳: %v", err)
	}
	return ts.UnixMilli()
}

// writeFixture 把一条合成记录同时写进 index.jsonl（追加一行）与
// <id>.json.gz（完整明细），布局与 cmd/reqlog-recorder 的写侧逐字段一致。
func writeFixture(t *testing.T, dataDir string, r fixtureRecord) reqlogformat.Record {
	t.Helper()
	dir := filepath.Join(dataDir, r.day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建固件目录: %v", err)
	}

	rec := reqlogformat.Record{
		ID: r.id(), TsMs: r.tsMs(t),
		Time:     time.UnixMilli(r.tsMs(t)).In(reqlogformat.CST).Format("2006-01-02 15:04:05.000"),
		Source:   r.source,
		Method:   "POST",
		Path:     "/v1/chat/completions",
		Status:   r.status,
		DurMs:    r.durMs,
		TtfbMs:   r.ttfbMs,
		ClientIP: r.clientIP,
		UA:       "test-agent/1.0",
		TokenPfx: r.tokenPfx,
		UpReqID:  "req_" + r.id(),
		Model:    r.model,
		Stream:   r.stream,
		ReqSize:  len(r.reqBody),
		// RespSize>0 在本固件里恒成立（所有样本都带非空响应体），
		// 因此 Record.MeasuredTTFB() 恒为 true——"ttfb 未测量"这条边界已经在
		// internal/platform/reqlogformat 的单元测试里单独覆盖过，不必在这里
		// 再制造一次，省得和 testFailedRequestIsReadable 要求的"失败记录也要有
		// 非空响应原文"互相打架。
		RespSize: len(r.respBody),
		InTok:    r.inTok, OutTok: r.outTok, CacheTok: r.cacheTok,
	}

	// 索引行
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal index line: %v", err)
	}
	idxPath := filepath.Join(dir, "index.jsonl")
	f, err := os.OpenFile(idxPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开 index.jsonl: %v", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		t.Fatalf("写 index.jsonl: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭 index.jsonl: %v", err)
	}

	// 明细 gz
	full := reqlogformat.FullRecord{
		Record: rec,
		ReqHeaders: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer test-only.invalid...[masked]",
		},
		ReqBody: r.reqBody,
		RespHeaders: map[string]string{
			"Content-Type": firstNonEmpty(r.respCType, "application/json"),
		},
		RespBody: r.respBody,
	}
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		t.Fatalf("gzip writer: %v", err)
	}
	if err := json.NewEncoder(gz).Encode(full); err != nil {
		t.Fatalf("编码明细: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭 gzip: %v", err)
	}
	gzPath := filepath.Join(dir, rec.ID+".json.gz")
	if err := os.WriteFile(gzPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("写明细文件: %v", err)
	}
	return rec
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func chatBody(model string, stream bool, systemMsg, userMsg string) string {
	b, _ := json.Marshal(map[string]any{
		"model":  model,
		"stream": stream,
		"messages": []map[string]string{
			{"role": "system", "content": systemMsg},
			{"role": "user", "content": userMsg},
		},
	})
	return string(b)
}

func chatReply(text string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-fixture", "object": "chat.completion",
		"choices": []map[string]any{{
			"index": 0, "finish_reason": "stop",
			"message": map[string]string{"role": "assistant", "content": text},
		}},
		"usage": map[string]int{},
	})
	return string(b)
}

func sseReply(chunks ...string) string {
	var b bytes.Buffer
	for _, c := range chunks {
		fmt.Fprintf(&b, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"index\":0}]}\n\n", c)
	}
	b.WriteString("data: {\"choices\":[{\"delta\":{},\"index\":0,\"finish_reason\":\"stop\"}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// longMessageBody 造一段超过契约 MaxMessageBytes（64KiB）的用户输入，
// 用来触发详情页的逐条截断路径（testLongContentIsTruncated 要求样本里
// 必须有至少一条被截断的消息）。
func longMessageBody(model string) string {
	var para bytes.Buffer
	for para.Len() <= 70<<10 {
		para.WriteString("这是一段用于触发截断路径的合成占位文本，不含任何真实用户内容。")
	}
	return chatBody(model, false, "你是测试助手。", para.String())
}

// tokenMapFixture 是合成的 tokenmap.json 内容：一个能解出用户名、一个解出
// 邮箱（触发 MaskEmail 打码路径）、其余 token 前缀故意不登记（触发"映射不到"
// 路径）。全部用 test-only.invalid 之类占位，不写像真的令牌。
func tokenMapFixture() map[string]string {
	return map[string]string{
		// 两个前缀的固件记录都用 source=sub2api（见 buildFixtureDataDir），
		// 后缀必须与之一致——tokenmap 的值形如 "<标识>@<source>"，
		// 后缀对不上时 resolveUsername 会判成"映射不到"（见 file_client.go
		// 的说明），那是刻意的保守失败，不是这里能靠凑巧蒙混过去的东西。
		"sk-test0001invalid00": "fixture-user@sub2api",
		"sk-test0002invalid00": "fixture.user@test-only.invalid@sub2api",
	}
}

func writeTokenMapFixture(t *testing.T, path string) {
	t.Helper()
	b, err := json.Marshal(tokenMapFixture())
	if err != nil {
		t.Fatalf("marshal tokenmap: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("写 tokenmap 固件: %v", err)
	}
}

// buildFixtureDataDir 铺一套跨两天、覆盖长对话截断/SSE 流式/失败请求
// （含 status=0）/令牌映射三态/时间与用户名过滤所需重复值的合成数据集。
// 覆盖面对齐 connectors/reqlog 的 Fake 样本矩阵（fake.go 的注释逐条解释了
// 为什么需要这些形态）。
func buildFixtureDataDir(t *testing.T) (dataDir, tokenMapPath string) {
	t.Helper()
	root := t.TempDir()
	dataDir = filepath.Join(root, "data")
	tokenMapPath = filepath.Join(root, "tokenmap.json")
	writeTokenMapFixture(t, tokenMapPath)

	const day1, day2 = "20260827", "20260828"

	// #1 超长对话：触发逐条截断（sub2api，映射得到用户名）
	writeFixture(t, dataDir, fixtureRecord{
		day: day1, hhmmss: "090000", seq: 1, source: "sub2api",
		status: 200, durMs: 4200, ttfbMs: 310,
		clientIP: "203.0.113.42", tokenPfx: "sk-test0001invalid00", model: "claude-sonnet-4-5",
		inTok: 21000, outTok: 600, cacheTok: 18000,
		reqBody:  longMessageBody("claude-sonnet-4-5"),
		respBody: chatReply("已收到长文档，风险点如下……"),
	})

	// #2 SSE 流式：装配后的最终回复来自事件流，不是 JSON（sub2api）
	writeFixture(t, dataDir, fixtureRecord{
		day: day1, hhmmss: "091500", seq: 2, source: "sub2api",
		status: 200, durMs: 1800, ttfbMs: 90,
		clientIP: "198.51.100.7", tokenPfx: "sk-test0002invalid00", model: "gpt-4o",
		stream: true, inTok: 40, outTok: 12,
		reqBody:   chatBody("gpt-4o", true, "You are terse.", "解释一下向量检索"),
		respBody:  sseReply("向量检索", "按相似度", "返回最近邻结果"),
		respCType: "text/event-stream",
	})

	// #3 失败请求（429），token 计数为 0，但响应原文非空（sub2api）
	writeFixture(t, dataDir, fixtureRecord{
		day: day1, hhmmss: "093000", seq: 3, source: "sub2api",
		status: 429, durMs: 210, ttfbMs: 5,
		clientIP: "192.0.2.181", tokenPfx: "sk-unmapped-token-01", model: "gpt-4o-mini",
		reqBody:  chatBody("gpt-4o-mini", false, "", "ping"),
		respBody: `{"error":{"message":"rate limited","type":"rate_limit_error"}}`,
	})

	// #4 无响应（status=0）：token 计数为 0，响应原文仍非空（timeout 记录）
	// （sub2api——覆盖 testStatsCoverFullFilteredSet 要求的 sawStatusZero）
	writeFixture(t, dataDir, fixtureRecord{
		day: day1, hhmmss: "094500", seq: 4, source: "sub2api",
		status: 0, durMs: 30000, ttfbMs: 0,
		clientIP: "192.0.2.181", tokenPfx: "sk-unmapped-token-01", model: "gpt-4o",
		reqBody:  chatBody("gpt-4o", true, "", "生成一份提纲"),
		respBody: `{"error":{"message":"upstream timeout","type":"server_error"}}`,
	})

	// #5 请求体不是可识别的对话形状（embedding 请求）：MessagesParsed=false
	// （sub2api）
	embedBody := `{"model":"text-embedding-3-large","input":["a","b"]}`
	writeFixture(t, dataDir, fixtureRecord{
		day: day1, hhmmss: "100000", seq: 5, source: "sub2api",
		status: 200, durMs: 88, ttfbMs: 20,
		clientIP: "203.0.113.42", tokenPfx: "sk-test0001invalid00", model: "text-embedding-3-large",
		inTok:    512,
		reqBody:  embedBody,
		respBody: `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.01,0.02]}]}`,
	})

	// #6 newapi 来源的样本（用于跨来源隔离与 both-source 循环类断言）。
	// token 前缀刻意不登记进 tokenmap：登记的两个前缀都固定对应 sub2api
	// 的样本（见 tokenMapFixture 的注释），这条记录混进 newapi 用同一个
	// 前缀会造成"前缀相同、来源不同"的虚假场景，与真实世界不符
	// （newapi/sub2api 是两套独立的令牌空间）。
	writeFixture(t, dataDir, fixtureRecord{
		day: day1, hhmmss: "101500", seq: 6, source: "newapi",
		status: 500, durMs: 15000, ttfbMs: 0,
		clientIP: "198.51.100.7", tokenPfx: "sk-unmapped-token-03", model: "gpt-4o",
		stream:   true,
		reqBody:  chatBody("gpt-4o", true, "", "季度汇报提纲"),
		respBody: `{"error":{"message":"internal error","type":"server_error"}}`,
	})
	writeFixture(t, dataDir, fixtureRecord{
		day: day1, hhmmss: "102000", seq: 7, source: "newapi",
		status: 200, durMs: 640, ttfbMs: 60,
		clientIP: "2001:db8:1234:5678::1", tokenPfx: "sk-unmapped-token-02", model: "gpt-4o-mini",
		inTok: 20, outTok: 8,
		reqBody:  chatBody("gpt-4o-mini", false, "", "hi"),
		respBody: chatReply("hello"),
	})

	// 第二天：批量常规记录，覆盖分页（跨天游标）、按用户名/模型过滤的重复值。
	models := []string{"claude-sonnet-4-5", "gpt-4o", "gpt-4o-mini"}
	for i := range 10 {
		model := models[i%len(models)]
		source := "sub2api"
		if i%4 == 3 {
			source = "newapi"
		}
		status := 200
		if i == 2 {
			status = 500
		}
		// 令牌前缀必须与 source 对应（tokenmap 的后缀是 "@sub2api"）；
		// 失败请求（非 2xx）的用量必须是 0——真实数据里 in/out/cache 三个
		// 字段都来自对**响应体**跑 ExtractUsage，而错误响应体没有 usage
		// 对象，三者恒为 0（不是只有 out 该是 0），伪造非零值会让
		// testFailedRequestIsReadable 这类断言测出一个真实磁盘格式里
		// 不会出现的组合。
		tokenPfx := "sk-test0001invalid00"
		if source == "newapi" {
			tokenPfx = "sk-unmapped-token-03"
		}
		inTok, outTok := 64+i*3, 120+i*7
		if status != 200 {
			inTok, outTok = 0, 0
		}
		writeFixture(t, dataDir, fixtureRecord{
			day: day2, hhmmss: fmt.Sprintf("%02d0000", 8+i%12), seq: 100 + i,
			source: source, status: status,
			durMs: int64(500 + i*50), ttfbMs: int64(50 + i*5),
			clientIP: "203.0.113.42", tokenPfx: tokenPfx, model: model,
			inTok: inTok, outTok: outTok,
			reqBody:  chatBody(model, false, "", fmt.Sprintf("批量样本 #%d", i)),
			respBody: chatReply(fmt.Sprintf("回复 #%d", i)),
		})
	}

	return dataDir, tokenMapPath
}
