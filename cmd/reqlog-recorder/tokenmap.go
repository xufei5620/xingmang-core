package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// tokenMapLoop 启动时先刷新一次，此后每 r.cfg.TokenMapRefresh 刷新一次
// ——与原型 reqlogger.go 的 `refreshTokenMap(); go func(){ for { sleep(10m);
// refreshTokenMap() } }()` 同一条策略，间隔从硬编码 10 分钟改成可配置。
func (r *Recorder) tokenMapLoop(ctx context.Context) {
	r.refreshTokenMap(ctx)
	ticker := time.NewTicker(r.cfg.TokenMapRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refreshTokenMap(ctx)
		}
	}
}

// refreshTokenMap 从两个上游库只读导出 token 前缀 → 用户名/邮箱映射，
// 写入 r.cfg.TokenMapPath（connectors/reqlog 的文件后端读这份文件解出
// RequestLogSummary.Username，邮箱在那一侧经 MaskEmail 打码后才对外，
// 见 connectors/reqlog/file_client.go 的 resolveUsername）。
//
// ⚠️ 已知风险，本次收编原样保留、不重新设计（见
// docs/handoffs/slices/XM-REQLOG-MERGE.md 的风险清单）：两条
// `docker exec ... psql` 命令绕开了平台的 Connector/CredentialRef 体系
// （ADR-014、ADR-018 的四道只读闸），直接假定本机能 docker exec 进两个
// 特定容器名执行只读 SQL，且导出结果含用户名/邮箱等 PII。这是桌面端原型
// 已经在生产用的既有做法；本次收编的目标是"让它可配置、可测试、磁盘格式
// 兼容"，不是重新设计凭据获取链路——后者需要独立的 ADR/Change Request，
// 不在本任务的时间盒内。
func (r *Recorder) refreshTokenMap(ctx context.Context) {
	m := map[string]string{}

	newapiOut, err := exec.CommandContext(ctx, "docker", "exec", "postgres", "psql",
		"-U", "root", "-d", "new-api", "-t", "-A", "-F", "\t",
		"-c", "select concat('sk-', left(t.key,17)), u.username from tokens t join users u on u.id=t.user_id;",
	).Output()
	if err != nil {
		r.logger.Warn("reqlog_recorder_tokenmap_newapi_query_failed", slog.String("error", err.Error()))
	} else {
		parsePsqlTSV(string(newapiOut), "@newapi", m)
	}

	sub2apiOut, err := exec.CommandContext(ctx, "docker", "exec", "sub2api-mig-postgres", "sh", "-c",
		`psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -t -A -F "	" -c "select left(k.key,20), coalesce(nullif(u.username,''), u.email) from api_keys k join users u on u.id=k.user_id;"`,
	).Output()
	if err != nil {
		r.logger.Warn("reqlog_recorder_tokenmap_sub2api_query_failed", slog.String("error", err.Error()))
	} else {
		parsePsqlTSV(string(sub2apiOut), "@sub2api", m)
	}

	if len(m) == 0 {
		r.logger.Warn("reqlog_recorder_tokenmap_empty_result")
		return
	}

	b, err := json.Marshal(m)
	if err != nil {
		r.logger.Error("reqlog_recorder_tokenmap_marshal_failed", slog.String("error", err.Error()))
		return
	}
	if err := os.WriteFile(r.cfg.TokenMapPath, b, r.cfg.FilePerm); err != nil {
		r.logger.Error("reqlog_recorder_tokenmap_write_failed",
			slog.String("path", r.cfg.TokenMapPath), slog.String("error", err.Error()))
		return
	}
	chownGroup(r.logger, r.cfg.TokenMapPath, r.cfg.GroupID)
	r.logger.Info("reqlog_recorder_tokenmap_refreshed", slog.Int("entries", len(m)))
}

// parsePsqlTSV 解析 `psql -t -A -F "\t"` 的输出（每行"前缀<TAB>标识"），
// 把 "<标识><suffix>" 写进 dst。抽成纯函数是为了不依赖真实 docker/postgres
// 就能单元测试解析逻辑本身——bug 更可能出在这几行字符串处理上，而不是
// exec.Command 那两行参数拼接。
func parsePsqlTSV(output, suffix string, dst map[string]string) {
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(parts) == 2 && parts[0] != "" {
			dst[parts[0]] = parts[1] + suffix
		}
	}
}
