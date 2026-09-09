package assurance

import (
	"fmt"
	"slices"
	"strings"
)

// PromptTemplateKeys 是设计稿 §1.2.1 固定的四个探测任务模板。
//
// 模板集合本身固化在代码里（非数据库可配置项）：新增模板需要新的切片而不是
// 运行时配置，避免探测的"考什么题"变成一个不受控的输入面（ADR-019 决策·五）。
var PromptTemplateKeys = []string{
	TemplateModelFingerprint,
	TemplateBenchmarkSet,
	TemplateContextLength,
	TemplateMinViableRequest,
}

const (
	TemplateModelFingerprint = "model_fingerprint"
	TemplateBenchmarkSet     = "benchmark_set"
	TemplateContextLength    = "context_length"
	TemplateMinViableRequest = "min_viable_request"
)

// ValidPromptTemplateKey 校验模板 key 是否是固定枚举之一。
func ValidPromptTemplateKey(key string) bool {
	return slices.Contains(PromptTemplateKeys, key)
}

// ProbeMessage 是探测请求里的一条对话消息（OpenAI 兼容 chat/completions 形状）。
type ProbeMessage struct {
	Role    string
	Content string
}

// contextLengthFixedInput 是"上下文长度"模板的固定长填充文本，末尾带一段
// 唯一的、要求模型复述的标记片段。长度与内容都固定在代码里——探测的"考
// 什么题"不是运行时输入。
const contextLengthTailMarker = "XM-ASSURE1-PROBE-TAIL-7f3c9a2e"

var contextLengthFixedInput = strings.Repeat(
	"星芒统一控制平台的渠道保障探测使用固定的、代码内置的输入内容，不接受自由文本。", 20,
) + contextLengthTailMarker

// benchmarkQuestion 是"基准题集"模板的一道固定题目。
type benchmarkQuestion struct {
	Prompt string
	Answer string // 期望答案里必须出现的子串（大小写不敏感）
}

// benchmarkQuestions 是固定的 3 道题（设计稿 §1.2.1："固定小题集（≤3 题）"）。
// 题目本身刻意选择答案唯一、可用子串精确匹配判分的形式，避免"语义正确性
// 判断"（§7.5 明确禁止）。
var benchmarkQuestions = []benchmarkQuestion{
	{Prompt: "只回答一个阿拉伯数字：7 加 5 等于多少？", Answer: "12"},
	{Prompt: "只回答一个单词：法国的首都是哪座城市（用英文回答）？", Answer: "paris"},
	{Prompt: "只回答一个阿拉伯数字：3 的 3 次方是多少？", Answer: "27"},
}

// BenchmarkQuestionCount 是基准题集的固定题目数。
var BenchmarkQuestionCount = len(benchmarkQuestions)

// BuildMessages 按模板 key 构造发给被探测模型的固定消息序列。
//
// 不接受任何来自声明的自由文本——这里的每一个字符串都是本文件里的常量，
// 唯一的运行时输入是 model（用于 model_fingerprint 的期望片段查表）。
func BuildMessages(templateKey string) ([]ProbeMessage, error) {
	switch templateKey {
	case TemplateModelFingerprint:
		return []ProbeMessage{{
			Role: "user",
			Content: "请只回答你的模型名称或标识符本身，不要包含任何其他文字、" +
				"标点或解释。",
		}}, nil
	case TemplateBenchmarkSet:
		var b strings.Builder
		b.WriteString("请依次回答以下题目，每题一行，只写答案本身，不要重复题干、" +
			"不要添加解释：\n")
		for i, q := range benchmarkQuestions {
			fmt.Fprintf(&b, "%d. %s\n", i+1, q.Prompt)
		}
		return []ProbeMessage{{Role: "user", Content: b.String()}}, nil
	case TemplateContextLength:
		return []ProbeMessage{{
			Role: "user",
			Content: contextLengthFixedInput +
				"\n\n以上文本到此结束。请只回答这段文本最后 32 个字符，不要包含其他内容。",
		}}, nil
	case TemplateMinViableRequest:
		return []ProbeMessage{{Role: "user", Content: "ping"}}, nil
	default:
		return nil, fmt.Errorf("未知的 prompt_template_key %q: %w", templateKey, ErrInvalidInput)
	}
}

// modelFingerprintHints 是"这个模型自我标识应该含有的片段"的小型对照表
// （设计稿 §1.2.1："每个模型固定一条已知探针问题+预期片段，随实现附一份
// 小型对照表，非用户可编辑"）。按厂商前缀匹配，未命中的模型退化为用声明的
// model 字符串本身作为期望片段——这是初版小表，后续可按需扩展，不需要
// 因为某个新模型名不在表里就拒绝声明。
var modelFingerprintHints = []struct {
	prefix string
	hint   string
}{
	{"claude", "claude"},
	{"gpt", "gpt"},
	{"gemini", "gemini"},
	{"grok", "grok"},
	{"kimi", "kimi"},
	{"glm", "glm"},
	{"chatglm", "glm"},
	{"deepseek", "deepseek"},
}

// expectedFingerprintFragment 返回 model_fingerprint 模板对某个模型的期望片段。
func expectedFingerprintFragment(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	for _, h := range modelFingerprintHints {
		if strings.Contains(lower, h.prefix) {
			return h.hint
		}
	}
	return lower
}

// AssessInput 是一次断言判定的输入。
type AssessInput struct {
	TemplateKey string
	Model       string
	Text        string
	Expected    ExpectedShape
	// PreviousScore 仅 benchmark_set 用：上一次非 refused 批次里同一
	// (channel_id, model) 的基准题得分；nil 表示没有可比较的历史。
	PreviousScore *int
}

// AssessOutput 是一次断言判定的结果。
type AssessOutput struct {
	// Status 只会是 ResultStatusOK 或 ResultStatusDegraded——failed/timeout
	// 由 Job 在拿到响应之前的传输层错误里判定，走不到这里（见
	// jobs/assurance_probe.go probeOneTarget 的注释）。
	Status  string
	Verdict string
	// Score 仅 benchmark_set 非 nil，供下一次探测取"上一次分数"比较。
	Score *int
}

// benchmarkScorePrefix / benchmarkScoreFormat 让 Score 能从 Verdict 文本里
// 可靠地重新解析出来——probe_result 表没有单独的"分数"列（设计稿 §1.4 的
// 冻结字段列表里没有），而"与上一次分数比较"这条断言规则需要读到上一次的
// 分数，因此约定 Verdict 必须以这个固定前缀开头，Store.PreviousBenchmarkScore
// 解析时按这个格式重新提取分数。
const benchmarkScorePrefix = "基准题集"

func formatBenchmarkVerdict(correct int, trend string) string {
	return fmt.Sprintf("%s %d/%d 分（%s）", benchmarkScorePrefix, correct, BenchmarkQuestionCount, trend)
}

// ParseBenchmarkScore 从一条历史 Verdict 文本里解析出基准题得分；解析不出
// （不是本模板产出的 Verdict，或格式意外改变）时返回 (0, false)。
func ParseBenchmarkScore(verdict string) (int, bool) {
	if !strings.HasPrefix(verdict, benchmarkScorePrefix+" ") {
		return 0, false
	}
	rest := strings.TrimPrefix(verdict, benchmarkScorePrefix+" ")
	var correct, total int
	if n, err := fmt.Sscanf(rest, "%d/%d", &correct, &total); n != 2 || err != nil {
		return 0, false
	}
	if total != BenchmarkQuestionCount {
		return 0, false
	}
	return correct, true
}

// mustContainOK 检查 expected_shape.must_contain 里的每个子串是否都出现在
// text 里（区分大小写——由声明人显式写下的子串，按字面匹配）。
func mustContainOK(text string, mustContain []string) bool {
	for _, s := range mustContain {
		if s == "" {
			continue
		}
		if !strings.Contains(text, s) {
			return false
		}
	}
	return true
}

// shapeOK 检查响应文本的长度是否落在 expected_shape 声明的区间内。
func shapeOK(text string, expected ExpectedShape) bool {
	n := len([]rune(text))
	if expected.MinLength > 0 && n < expected.MinLength {
		return false
	}
	if expected.MaxLength > 0 && n > expected.MaxLength {
		return false
	}
	return true
}

// Assess 判定一次探测响应的结果。text 已经是探测客户端解出的纯文本（从不是
// 原始 HTTP body，见 fakeclient.go/realclient.go 的 ProbeResponse.Text）。
func Assess(in AssessInput) (AssessOutput, error) {
	shapePass := shapeOK(in.Text, in.Expected) && mustContainOK(in.Text, in.Expected.MustContain)

	switch in.TemplateKey {
	case TemplateModelFingerprint:
		fragment := expectedFingerprintFragment(in.Model)
		matched := fragment != "" && strings.Contains(strings.ToLower(in.Text), fragment)
		if shapePass && matched {
			return AssessOutput{Status: ResultStatusOK, Verdict: "模型自称与声明一致"}, nil
		}
		if !matched {
			return AssessOutput{Status: ResultStatusDegraded, Verdict: "模型自称与声明不符"}, nil
		}
		return AssessOutput{Status: ResultStatusDegraded, Verdict: "响应形状不符合声明的长度/必含片段"}, nil

	case TemplateBenchmarkSet:
		correct := countBenchmarkCorrect(in.Text)
		score := correct
		var trend string
		status := ResultStatusOK
		switch {
		case correct == 0:
			status, trend = ResultStatusDegraded, "全部未命中"
		case in.PreviousScore == nil:
			trend = "无历史可比"
		case correct < *in.PreviousScore-1:
			status, trend = ResultStatusDegraded, fmt.Sprintf("较上次下降（上次 %d/%d）", *in.PreviousScore, BenchmarkQuestionCount)
		case correct < *in.PreviousScore:
			trend = fmt.Sprintf("较上次略降（上次 %d/%d）", *in.PreviousScore, BenchmarkQuestionCount)
		default:
			trend = "与上次持平或提升"
		}
		if !shapePass && status == ResultStatusOK {
			status = ResultStatusDegraded
		}
		return AssessOutput{Status: status, Verdict: formatBenchmarkVerdict(correct, trend), Score: &score}, nil

	case TemplateContextLength:
		tail := lastRunes(contextLengthFixedInput, 32)
		if strings.Contains(in.Text, tail) && shapePass {
			return AssessOutput{Status: ResultStatusOK, Verdict: "命中长上下文尾部片段"}, nil
		}
		return AssessOutput{Status: ResultStatusDegraded, Verdict: "未命中长上下文尾部片段"}, nil

	case TemplateMinViableRequest:
		if strings.TrimSpace(in.Text) != "" && shapePass {
			return AssessOutput{Status: ResultStatusOK, Verdict: "最小可用请求成功"}, nil
		}
		return AssessOutput{Status: ResultStatusDegraded, Verdict: "最小可用请求返回为空或形状异常"}, nil

	default:
		return AssessOutput{}, fmt.Errorf("未知的 prompt_template_key %q: %w", in.TemplateKey, ErrInvalidInput)
	}
}

// countBenchmarkCorrect 统计响应文本里命中了几道基准题的期望答案子串。
//
// 判分方式是"响应整体是否包含每题答案子串"而不是按行对齐解析——探测响应
// 的格式不受我们控制（哪怕 Prompt 已经要求"每题一行"），按行严格对齐解析
// 一旦模型多加了空行就会全错，比对全文做子串命中更稳健，且仍然是
// "浅层断言"（不判断解题过程，只看答案字面是否出现）。
func countBenchmarkCorrect(text string) int {
	lower := strings.ToLower(text)
	correct := 0
	for _, q := range benchmarkQuestions {
		if strings.Contains(lower, strings.ToLower(q.Answer)) {
			correct++
		}
	}
	return correct
}

// lastRunes 返回字符串按 rune 计的最后 n 个字符（用于长上下文尾片段比较，
// 避免按字节切分把多字节字符切碎）。
func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
