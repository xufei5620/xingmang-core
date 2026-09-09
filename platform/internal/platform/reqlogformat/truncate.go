package reqlogformat

import "fmt"

// Truncate 按**字节**截断 s：超过 n 字节时截到 n 并附一段人可读的
// "共 X 字节"说明。
//
// 这是写侧预览/工具调用参数摘要用的截断（原样照搬桌面端原型 viewer.go 的
// `trunc`），**不是**契约层 TruncateUTF8（connectors/reqlog/redact.go）：
// 两者截断口径不同——本函数按字节硬切（可能切开一个多字节字符，只用于
// 内部拼装的提示串，不直接展示给终端用户的对话正文），也会附加一段可见的
// 后缀说明；TruncateUTF8 是按 UTF-8 字符边界回退、不拼说明文字、且返回
// 截断标记供契约类型使用。两者服务不同的读者：这个函数产出的字符串会被
// 嵌进 ExtractFinalText/ParseTurns 拼好的对话文本里再整体走一次
// TruncateUTF8，所以这里切坏一个字符不会导致最终展示出 U+FFFD——外层
// 还有一次按字符边界的截断兜底。
func Truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + fmt.Sprintf(" ...(共%d字节)", len(s))
	}
	return s
}
