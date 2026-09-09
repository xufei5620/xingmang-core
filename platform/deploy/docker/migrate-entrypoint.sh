#!/bin/sh
# 一次性迁移容器的入口：把「业务迁移」和「River 迁移」按顺序跑完。
#
# 这是 Platform Lifecycle Operation（宪法 3 条 / ADR-003）：它不走 Action，
# 但受版本化脚本 + 变更单 + 人工批准约束，任何模块都不得把它当写通道。
#
# 为什么两步：
#   1. db/migrations/*.sql 是平台自己的 forward-only 迁移，由 cmd/migrate
#      （golang-migrate）执行。
#   2. River 的表结构由 River 自己的 pinned bundle 管（internal/platform/jobs
#      .Migrate），入口是 `platform-worker -migrate`。db/migrations/river/ 下
#      那份 SQL 是可审阅镜像，**不是**第二条独立迁移线，不能拿 golang-migrate 跑。
# 合在一个容器里跑完，是为了不出现「业务迁移过了、River 没过」的半截状态。
set -eu

: "${DATABASE_URL:?DATABASE_URL is required}"

echo "[migrate] step 1/2: db/migrations (golang-migrate)"
# PGPASSWORD 只作用于这一条命令：cmd/migrate 走 golang-migrate 的 pgx/v5 驱动，
# 它最终调 pgconn.ParseConfig，会读 PGPASSWORD。这样 DSN 里不必内联密码，
# 密码也不会出现在容器的 `ps` 输出或迁移日志里。
#
# 注意**不能**把 PGPASSWORD 导出到整个脚本：第 2 步的 pgdsn 校验会把它当成
# 「密码没走 CredentialRef」而拒绝启动——那个拒绝是对的，别绕过它。
PGPASSWORD="${DATABASE_PASSWORD:-}" \
  XM_DATABASE_URL="$DATABASE_URL" \
  /usr/local/bin/migrate -path /app/db/migrations up

echo "[migrate] step 2/2: River migrations (platform-worker -migrate)"
# 这一步走的是 platform-worker 的正常 DSN 纪律：DATABASE_URL 无密码 +
# DATABASE_PASSWORD_REF/DATABASE_PASSWORD（宪法 7 条）。
/usr/local/bin/platform-worker -migrate

echo "[migrate] done"
