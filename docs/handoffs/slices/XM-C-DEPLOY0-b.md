# XM-C-DEPLOY0-b · 服务器 staging / production 发布闸门

## status

READY

## branch / commit / base

- branch: `ai/codex/XM-C-DEPLOY0-b`
- base: `f982256`（XM-C-DEPLOY0-a READY）
- worktree: `K:/星芒统一控制平台/wt-xmDEPLOY0-b`
- implementation commit: `3036437`
- handoff commit: 待提交

## summary

本片把服务器为中心的部署闭环落成可审阅的脚本和 Compose/反代模板：

- `deploy.sh staging` 只采纳 `refs/heads/release/v0.1-launch`；`prod` 只采纳
  `refs/heads/main`。两档先 fetch exact ref、验证 commit 对象和 CI 的严格单行
  `<sha>.status=green`，再执行 `docker compose config --quiet` → `build --pull=false`
  → `up -d --remove-orphans --wait` → `/healthz` → `/readyz` 有界探针。
- 生产要求 `--confirm DEPLOY-PRODUCTION` 和经过字符/敏感词校验的 `--reason`；
  生产路径、remote、Compose 文件、项目名、探针和 Docker/curl 路径固定在安装约定，
  临时目录只能用显式 `XM_DEPLOY_TEST_MODE=1 --test-mode`，且 test-mode 禁止 prod。
- `promote.sh` 只把 green 的 release 快进推到 main，要求安装器登记的 bare repo、
  `pre-receive` 可执行且哈希与版本化 hook 一致、`denyNonFastForwards`/`denyDeletes`
  已开启。marker 为原子写入的严格 `sha=<40hex>`；安装器开启
  `xm.receive.requirePromoteLock` 时，hook 还验证 promote.sh PID/SHA 授权锁。
- Git/Docker/Compose 控制环境被清理，Docker/Git 使用受控 HOME；审计为 0600 的
  单行 key=value 哈希链（`prev_sha256`/`entry_sha256`），不写 token/password/DSN。
  可选 `--notify-hook` 仅把脱敏环境/ref/SHA/result 通过 stdin 交给受控通知适配器，
  通知失败不改写已完成的部署结果。
- staging 与 production 使用不同 Compose 项目和 web 回环端口；staging profile
  才启用 `001_staging_seed.sql`，production 强制 OIDC、显式采集模式并省略 seed。
  外层 Nginx 模板分别提供 staging Basic Auth/TLS 与 production TLS/OIDC 入口。

## files_changed

- `deploy/scripts/deploy.sh`
- `deploy/scripts/promote.sh`
- `deploy/scripts/install-git-server.sh`
- `deploy/git-hooks/pre-receive`
- `deploy/git-hooks/post-receive`
- `deploy/compose/server-staging.yaml`
- `deploy/compose/server-prod.yaml`
- `deploy/nginx/server-staging.conf.example`
- `deploy/nginx/server-prod.conf.example`
- `tests/deploy/deploy0-b.test.sh`
- `tests/deploy/deploy0-a.test.sh`
- `tests/security/governance-not-hollow.test.sh`
- `scripts/guard-governance-files.sh`
- `docs/runbooks/DEPLOY.md`
- `docs/runbooks/LAUNCH.md`
- `docs/superpowers/plans/2026-08-29-deploy0-implementation.md`

## 格 → 数据源 / 证据映射

| 运行时格 | 来源 | 闸门 |
|---|---|---|
| staging 目标 SHA | `origin/refs/heads/release/v0.1-launch` 的 FETCH_HEAD | exact commit + CI status green |
| production 目标 SHA | `origin/refs/heads/main` 的 FETCH_HEAD | exact commit + CI status green + prod confirm |
| CI 状态 | D0-a post-receive `/srv/ci/<sha>.status` | 单行 `green`、换行、非 symlink |
| main 晋级授权 | bare repo `xm.receive.promoteMarker` + promote lock | pre-receive hook、快进、hook hash |
| Compose 栈 | 版本化 `launch.yaml` + 环境 overlay | `config --quiet` 后才 build/up |
| 健康 | web loopback `/healthz`、`/readyz` | curl 有界重试，两个都通过 |
| 发布审计 | 服务器外部 0600 文件 | key=value hash chain，禁止秘密 |

## tests_run

- `D:/Git/bin/bash.exe -n deploy/scripts/deploy.sh deploy/scripts/promote.sh deploy/git-hooks/pre-receive deploy/scripts/install-git-server.sh tests/deploy/deploy0-b.test.sh` — PASS
- `D:/Git/bin/bash.exe tests/deploy/deploy0-a.test.sh` — PASS (`DEPLOY0-A-TEST-OK`)
- `D:/Git/bin/bash.exe tests/deploy/deploy0-b.test.sh` — PASS (`DEPLOY0-B-TEST-OK`)
- `D:/Git/bin/bash.exe tests/security/governance-not-hollow.test.sh` — PASS
- `go fmt ./...` — PASS
- 清除代理环境后 `go vet ./...` — PASS
- 清除代理环境后 `go test -p 1 -count=1 ./...` — PASS（全部 Go 包）
- Docker Compose staging/prod `config --quiet`（含 profile/生产必填 OIDC 与采集变量）— PASS；production 配置不包含 bootstrap service，staging profile 包含该 service
- WSL Ubuntu-24.04 临时 archive 检出：`pnpm install --frozen-lockfile --ignore-scripts --package-import-method=copy --offline`、`pnpm -r run typecheck`、`pnpm -r run test`、Storybook build、admin-web build — PASS
  - ui-admin 16 文件 / 219 tests；admin-web 47 文件 / 881 tests；保留 Node 22 engine warning 与既有大 chunk warning
- `C:/Users/58439/AppData/Local/Temp/xm-gitleaks-bin/gitleaks.exe git --redact --no-banner --log-opts=HEAD^..HEAD` — PASS（1 commit，no leaks found）
- `git diff --check` — PASS

## tests_not_run

- 未在 fiberstate/OVH 或其它真实服务器执行 root 安装、SSH 推拉、真实 Docker build/up、
  外层 Nginx reload、OIDC 登录或 Telegram 发送。
- 未补齐 D0-a Handoff 提到的 CI 临时 PostgreSQL/缓存编排；该边界仍需独立切片。
- Compose 基础栈仍沿用现有 `DATABASE_PASSWORD` 环境落点；把 Go 服务改成仅读 Docker
  secret 需要独立凭据/运行时变更，不能在本片伪装成已完成。

## risks

- `--test-mode` 是仅本地模拟的显式旁路，脚本拒绝将其用于 prod；生产路径仍由固定
  `/srv/*` 约定和 bare hook/hash 保护。
- 新脚本使用 `/bin/bash -p`，并清理 `BASH_ENV`、动态链接器变量及调用者 PATH；生产
  只使用固定系统 PATH，Compose `.env` 拒绝 `COMPOSE_*`/`DOCKER_*`/`GIT_*` 控制键。
- 安装器开启 `xm.receive.requirePromoteLock` 后，pre-receive 默认强制 promote.sh
  PID/SHA 授权锁；配置缺失或 false 也不会降级，只有显式测试 override 才可运行旧
  marker-only 夹具。
- 正常失败会释放环境/晋级锁；SIGKILL、掉电或进程被强制终止时 EXIT trap 不运行，
  锁会保留并 fail-closed。人工核对进程、日志、marker 后才能删除对应锁。
- 本片的文件审计增加了 hash chain，但尚未把最新 hash 锚定到外部签名存储；业务
  `audit.audit_event` 的数据库外锚定规则仍由既有审计链负责。
- `--notify-hook` 只是安全的通知适配器边界；Telegram CredentialRef/独立 Bot 与
  外部看门狗仍需服务器运维配置，不在仓库中保存 token。

## follow_ups

- 验收线审读本 Handoff、复跑 D0-a/D0-b/Go 全量门禁后合入 release；真实服务器由
  产品负责人按 DEPLOY.md 执行 root 安装和变更单。
- D0-c 处理 origin 切换、GitHub 镜像和旧 PR 文档迁移。
- 独立切片补 CI 临时 PostgreSQL/缓存、外部审计 hash 锚定、OIDC/Keycloak、通知适配器
  与恢复演练。
