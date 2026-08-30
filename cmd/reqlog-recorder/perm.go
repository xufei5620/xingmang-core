package main

import (
	"log/slog"
	"os"
	"runtime"
)

// chownGroup 尽力把 path 的属组改成 gid，失败只记警告、不致命。
//
// 三条理由缺一都不该让整个记录代理挂掉：
//   - gid<=0 表示运维显式关掉这个功能（见 Config.GroupID 的文档），不是错误；
//   - Windows 上 os.Chown 直接返回"不支持"（本仓库在 Windows 上开发/跑
//     `go test ./...`，生产部署目标是 Linux systemd 服务）——这里的降级
//     是为了让同一份代码在两边都能跑，不是生产要用到的分支；
//   - 就算在 Linux 上，一次 chgrp 失败（比如目标 GID 在这台机器上不存在）
//     也不该让"这一条请求记不下来"变成"记录代理直接退出"——**记录数据**
//     是这个进程唯一不能放弃的职责，属组只是锦上添花的读权限收紧。
//     真正的补救是 docs/runbooks/REQLOG-RECORDER.md 里的一次性
//     `chgrp -R`，不是让进程为了这件事反复重启。
func chownGroup(logger *slog.Logger, path string, gid int) {
	if gid <= 0 {
		return
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chown(path, -1, gid); err != nil {
		logger.Warn("reqlog_recorder_chown_failed",
			slog.String("path", path), slog.Int("gid", gid), slog.String("error", err.Error()))
	}
}
