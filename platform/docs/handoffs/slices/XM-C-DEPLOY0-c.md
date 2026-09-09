# XM-C-DEPLOY0-c · origin / GitHub 镜像与流程迁移

## status

READY

## branch / commit / base

- branch: `ai/codex/XM-C-DEPLOY0-c`
- base: `2394730`（XM-C-DEPLOY0-b READY）
- worktree: `K:/星芒统一控制平台/wt-xmDEPLOY0-c`
- implementation commits: `c9e2bca`, `117a922`, `dc45cac`, `5c6e024`

## summary

- `configure-remotes.sh` 提供显式、可回滚的 remote 配置：只在确认后把服务器裸仓库
  设为 `origin`、把 GitHub 设为 `github`；未知 remote、已有 pushurl、userinfo、
  重写规则、符号链接和非受控生产路径均 fail closed；dry-run 不修改 `.git/config`，
  迁移时保留 `branch.*.remote=origin` 跟踪关系。
- `mirror-github.sh` 只允许官方 `github` remote，并验证 `origin` 仍指向服务器，
  执行一次 `git push --mirror github`；不自动重试、不改变 origin，失败输出可追溯
  摘要但不阻塞本地门禁/服务器发布。通知与凭据不进入脚本。
- `deploy/git-hooks/README.md` 说明 hook 安装、green status、main promote 授权锁
  和服务器迁移步骤；AGENTS/CLAUDE/CODEX 常驻交接改为服务器 Handoff 流程，旧 PR
  命令仅保留历史追溯。
- 新增 `docs/runbooks/GIT-WORKFLOW.md`，明确 remote、切片、镜像、审核和恢复边界。

## files_changed

- `deploy/scripts/configure-remotes.sh`
- `deploy/scripts/mirror-github.sh`
- `tests/deploy/deploy0-c-remotes.test.sh`
- `tests/deploy/deploy0-c-mirror.test.sh`
- `deploy/git-hooks/README.md`
- `scripts/guard-governance-files.sh`
- `tests/security/governance-not-hollow.test.sh`
- `AGENTS.md`
- `CLAUDE.md`
- `GEMINI.md`
- `PROJECT-CONSTITUTION.md`
- `docs/handoffs/CODEX-PROMPT.md`
- `docs/handoffs/CODEX-PROJECT-HANDOFF.md`
- `docs/handoffs/CODEX-UI-ALIGNMENT-BRIEF.md`
- `docs/runbooks/GIT-WORKFLOW.md`
- `docs/superpowers/plans/2026-08-29-deploy0-implementation.md`
- `docs/handoffs/slices/XM-C-DEPLOY0-c.md`

## 格 → 数据源 / 证据映射

| 运行时格 | 来源 | 闸门 |
|---|---|---|
| 默认拉取 remote | `origin` → 服务器 bare repo | configure-remotes 显式确认 + 写后核对 |
| 异地备份 remote | `github` → GitHub URL | mirror 只允许 github、单次 push、失败可见 |
| 切片交付状态 | 分支内 `docs/handoffs/slices/XM-*.md` | 本地门禁证据 + 人类验收线 |
| main 保护 | D0-a/b pre-receive + promote | green status、授权锁、非快进拒绝 |

## tests_run

- `D:/Git/bin/bash.exe -n deploy/scripts/configure-remotes.sh deploy/scripts/mirror-github.sh tests/deploy/deploy0-c-remotes.test.sh tests/deploy/deploy0-c-mirror.test.sh` — PASS
- `D:/Git/bin/bash.exe tests/deploy/deploy0-c-remotes.test.sh` — PASS (`DEPLOY0-C-REMOTES-TEST-OK`；含未知 remote、错误 github、pushurl 和 branch tracking 回归)
- `D:/Git/bin/bash.exe tests/deploy/deploy0-c-mirror.test.sh` — PASS (`DEPLOY0-C-MIRROR-TEST-OK`；含单次 push、失败摘要、origin 校验)
- `D:/Git/bin/bash.exe tests/security/governance-not-hollow.test.sh` — PASS
- `go fmt ./...` / `go vet ./...` / `go test -p 1 -count=1 ./...` — PASS（当前 5c6e024 工作树；本片无 Go 源码改动）
- `git diff --name-only 2394730..HEAD -- web` — 空（本片无前端源码改动）；WSL Ubuntu-24.04 archive 检出当前 5c6e024：`pnpm install --frozen-lockfile --ignore-scripts --package-import-method=copy --offline`、`pnpm -r run typecheck`、`pnpm -r run test`、Storybook build、admin-web build — PASS（ui-admin 219 tests；admin-web 881 tests）
- `C:/Users/58439/AppData/Local/Temp/xm-gitleaks-bin/gitleaks.exe git --redact --no-banner --log-opts=2394730..HEAD~2` — PASS（4 implementation commits，no leaks found）
- `git diff --check` — PASS

## tests_not_run

- 未执行真实服务器 remote 切换、SSH 推拉、GitHub mirror、Actions 恢复或 production 部署；
- 未触碰任何 GitHub/服务器凭据；
- 未改变旧 `.github/workflows` 文件的触发器，恢复 Actions 需单独变更记录。

## risks

- `configure-remotes.sh --confirm` 会修改当前 checkout 的 `.git/config`，因此默认只
  读/模拟；执行前必须看 dry-run 输出。脚本拒绝并保留未知 remote，不自动删除。
- GitHub 镜像是异地备份，不是门禁来源；网络失败不会自动重试，需人工再次发起。
  失败日志正文不落盘/不回显，只返回 SHA-256 与行数摘要，避免把远端认证信息带入
  会话；需要详细网络诊断时由服务器管理员在 Git 客户端侧按凭据策略采集。
- 旧设计稿和历史计划仍含 PR 命令，当前活动口径由本文件及
  `docs/runbooks/GIT-WORKFLOW.md` 覆盖。
- `PROJECT-CONSTITUTION.md` 的服务器过渡例外有效至 2026-09-12，届时由产品负责人
  复审并决定恢复/废止 Actions/PR；本片没有延长该期限。

## follow_ups

- 验收线审读并复跑完整门禁后合入 release；产品负责人再按 runbook 执行服务器 remote
  配置和首次镜像。
- D0-c 后续可补服务器端 mirror 定时任务，但必须另立审批并保持单次、可失败不阻塞。
- 按队列继续拆分 LOCAL 中已批准规格的实现切片。
