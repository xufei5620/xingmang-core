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

// tokenMapV2File / tokenMapV2Entry 是 CR-0008 新增并行文件 tokenmap.v2.json
// 的磁盘形状。与 connectors/reqlog（读侧）的同名类型**独立定义**：本包是
// `main` 包，读侧的只读连接器包不能导入它；反过来让只读连接器包导入一个
// cmd/* 二进制也不合适。JSON 形状本身是两边共享的契约（CR-0008「契约变化」
// 一节给出的形状），与 v1 文件（`map[string]string`）今天已经是同一种
// "两边各自按同一份文字契约实现"的关系。
type tokenMapV2File struct {
	SchemaVersion int                        `json:"schema_version"`
	Entries       map[string]tokenMapV2Entry `json:"entries"`
}

type tokenMapV2Entry struct {
	Username string `json:"username"`
	Source   string `json:"source"`
	UserID   string `json:"user_id"`
}

// newapiTokenMapQuery / sub2apiTokenMapQuery 是两条导出 SQL 的**唯一权威
// 副本**——refreshTokenMap 与本包的机械防护测试（tokenmap_guard_test.go，
// ADR-020 决策·二）都引用这两个常量，不各自维护一份字面量。抽成命名常量
// 只是为了让测试能对"源码里的字符串字面量"做静态断言而不必解析
// exec.Command 的参数列表；两条查询本身逐字不变（只是从两处硬编码字面量
// 挪成一处常量）。
//
// ADR-020 决策·一·1 的封闭列举——任何扩大都需要先修 ADR，不得直接在这里
// 加语句/加表/加列：
//   - NewAPI：表 tokens（列 key、user_id）、users（列 id、username），
//     经 users.id = tokens.user_id 关联；
//   - Sub2API：表 api_keys（列 key、user_id）、users（列 id、username、
//     email），经 users.id = api_keys.user_id 关联。
const (
	newapiTokenMapQuery  = "select concat('sk-', left(t.key,17)), u.username, u.id from tokens t join users u on u.id=t.user_id;"
	sub2apiTokenMapQuery = `select left(k.key,20), coalesce(nullif(u.username,''), u.email), u.id from api_keys k join users u on u.id=k.user_id;`
)

// refreshTokenMap 从两个上游库只读导出 token 前缀 → 用户名/邮箱/上游用户 ID
// 映射，写入 r.cfg.TokenMapPath（v1，逐字节保持 CR-0008 之前的格式）与
// r.cfg.TokenMapV2Path（v2，CR-0008 新增并行文件，含上游数字/不透明用户
// ID）。connectors/reqlog 的文件后端读 v1 解出 RequestLogSummary.Username
// （邮箱经 MaskEmail 打码后才对外，见 connectors/reqlog/file_client.go 的
// resolveUsername），可选读 v2 解出 RequestLogSummary.User（见同文件的
// resolveUserRef）。
//
// ⚠️ 已知风险，本次收编原样保留、不重新设计（见
// docs/handoffs/slices/XM-REQLOG-MERGE.md 的风险清单；ADR-020「请求日志
// 记录器的上游库只读接入」记录了这条通道的边界与不变量）：两条
// `docker exec ... psql` 命令绕开了平台的 Connector/CredentialRef 体系
// （ADR-014、ADR-018 的四道只读闸），直接假定本机能 docker exec 进两个
// 特定容器名执行只读 SQL，且导出结果含用户名/邮箱/用户 ID 等 PII。这是
// 桌面端原型已经在生产用的既有做法；CR-0008 在这条既有边界内新增一列
// 已经 JOIN 到的 u.id，不新增语句、不新增连接目标（ADR-020 决策·一·1）。
func (r *Recorder) refreshTokenMap(ctx context.Context) {
	m := map[string]string{}
	v2 := map[string]tokenMapV2Entry{}

	newapiOut, err := exec.CommandContext(ctx, "docker", "exec", "postgres", "psql",
		"-U", "root", "-d", "new-api", "-t", "-A", "-F", "\t",
		"-c", newapiTokenMapQuery,
	).Output()
	if err != nil {
		r.logger.Warn("reqlog_recorder_tokenmap_newapi_query_failed", slog.String("error", err.Error()))
	} else {
		parsePsqlTSV(string(newapiOut), "@newapi", "newapi", m, v2)
	}

	sub2apiOut, err := exec.CommandContext(ctx, "docker", "exec", "sub2api-mig-postgres", "sh", "-c",
		`psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -t -A -F "	" -c "`+sub2apiTokenMapQuery+`"`,
	).Output()
	if err != nil {
		r.logger.Warn("reqlog_recorder_tokenmap_sub2api_query_failed", slog.String("error", err.Error()))
	} else {
		parsePsqlTSV(string(sub2apiOut), "@sub2api", "sub2api", m, v2)
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

	r.writeTokenMapV2(v2)
}

// writeTokenMapV2 写 CR-0008 新增的并行文件 tokenmap.v2.json：与
// tokenmap.json 同一次刷新算出、内容对应，但失败只记警告、不影响
// tokenmap.json 已经成功写盘这件事——v1 文件是这条链路今天唯一被消费的
// 产出，v2 是可选新增，两者的失败必须互不传染（CR-0008「契约变化」：v2
// 缺失或解析失败时消费侧退回今天的行为，不是新错误类别）。
//
// r.cfg.TokenMapV2Path 留空表示不写，与 TokenMapPath 的"留空即不生效"
// 约定一致。与 tokenmap.json 同样不做临时文件+rename 的原子写：本次改动
// 保持与 v1 文件今天的写法完全一致（都是一次 os.WriteFile），不引入 v1
// 没有的新写盘策略。
func (r *Recorder) writeTokenMapV2(v2 map[string]tokenMapV2Entry) {
	if strings.TrimSpace(r.cfg.TokenMapV2Path) == "" {
		return
	}
	b, err := json.Marshal(tokenMapV2File{SchemaVersion: 2, Entries: v2})
	if err != nil {
		r.logger.Error("reqlog_recorder_tokenmap_v2_marshal_failed", slog.String("error", err.Error()))
		return
	}
	if err := os.WriteFile(r.cfg.TokenMapV2Path, b, r.cfg.FilePerm); err != nil {
		r.logger.Error("reqlog_recorder_tokenmap_v2_write_failed",
			slog.String("path", r.cfg.TokenMapV2Path), slog.String("error", err.Error()))
		return
	}
	chownGroup(r.logger, r.cfg.TokenMapV2Path, r.cfg.GroupID)
	r.logger.Info("reqlog_recorder_tokenmap_v2_refreshed", slog.Int("entries", len(v2)))
}

// parsePsqlTSV 解析 `psql -t -A -F "\t"` 的输出（每行"前缀<TAB>标识<TAB>
// 上游ID"），同时填两份目标：
//   - v1Dst（tokenmap.json，格式与 CR-0008 之前逐字节相同）："<标识><suffix>"；
//   - v2Dst（tokenmap.v2.json，CR-0008 新增并行文件）：{username, source, user_id}。
//
// 上游 ID 这一列允许缺失（SplitN 限 3 段，只拿到 2 段时兜底）：v1 侧不受
// 影响，v2 侧对应条目 UserID 为空字符串——消费侧 resolveUserRef 把空
// UserID 按"映射不到"处理，不是新错误类别（CR-0008 变更范围#3）。
//
// 抽成纯函数是为了不依赖真实 docker/postgres 就能单元测试解析逻辑本身
// ——bug 更可能出在这几行字符串处理上，而不是 exec.Command 那两行参数
// 拼接。
func parsePsqlTSV(output, suffix, source string, v1Dst map[string]string, v2Dst map[string]tokenMapV2Entry) {
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 3)
		if len(parts) < 2 || parts[0] == "" {
			continue
		}
		prefix, identity := parts[0], parts[1]
		var userID string
		if len(parts) == 3 {
			userID = strings.TrimSpace(parts[2])
		}
		v1Dst[prefix] = identity + suffix
		v2Dst[prefix] = tokenMapV2Entry{Username: identity, Source: source, UserID: userID}
	}
}
