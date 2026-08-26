// Package buildinfo 提供构建元信息，供 platform-api / platform-worker
// 启动日志与 /healthz 输出使用。Version/Commit 由发布流程 -ldflags 注入。
package buildinfo

import "fmt"

var (
	// Version 是语义化版本或 "dev"。
	Version = "dev"
	// Commit 是构建时的 git commit SHA 或 "unknown"。
	Commit = "unknown"
)

// String 返回 "xingmang-platform <version> (<commit>)"。
func String() string {
	return fmt.Sprintf("xingmang-platform %s (%s)", Version, Commit)
}
