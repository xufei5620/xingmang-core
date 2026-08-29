# XM-C-DEPLOY0-a · 本地 CI 与 Git 接收边界

## status

READY

## branch / commit / base

- branch: `ai/codex/XM-C-DEPLOY0-a`
- implementation commits: `5630fbd`, `dfdb3d0`, `abd23b9`
- base: `release/v0.1-launch` at `75bc7b9`
- worktree: `K:/星芒统一控制平台/wt-xmDEPLOY0-a`

## summary

本片完成服务器为中心工作流的 DEPLOY0-a：本地/服务器共用的四门禁脚本、裸仓库
receive hooks、一次性安装脚本，以及无服务器回归测试。没有连接真实生产服务器，
没有读取或写入任何生产凭据。

- `scripts/ci-local.sh` 固定按 governance → secret-scan → backend → frontend
  顺序执行；每个步骤显式检查返回码，禁止用条件调用吞掉失败；服务器模式要求
  Git 工作树、完整 HEAD、禁止门禁命令替换，并清理外部 Git/扫描配置环境。
- backend 的 sqlc 生成结果覆盖所有 `internal/platform/*/gen` 目录；secret-scan
  使用受控的本次提交增量范围，避免把仓库历史中已有的测试占位误报成新泄露。
- `pre-receive` 只接受 `refs/heads/*` 的非删除、非快进更新；确认目标为裸仓库，
  禁用 replace refs；`main` 必须匹配裸仓库配置的 promote marker。marker 采用
  严格单行 `sha=<commit>` 格式，并以原子锁串行校验与消费，防止并发双放行。
- `post-receive` 读取安装器写入的状态/工作目录配置，校验路径、符号链接和镜像
  名称；每个 SHA 使用独立锁与 `mktemp` 工作目录。CI 检出使用完整 `git clone`
 （保留 `.git`、HEAD、index 和历史），不再使用无法支撑治理/gitleaks/sqlc 的
  `git archive`。
- Docker CI 默认 `--pull=never --network none`，只挂载一次性工作树与服务器登记的
  可信 `ci-local.sh`（路径 + SHA-256 双重校验），并显式传入治理基线。自定义宿主
  执行器仅在 `POST_RECEIVE_ALLOW_CUSTOM_EXECUTOR=1` 测试开关下可用，使用 `env -i`
  和有限 PATH，不能继承服务器凭据或 stdin。
- `install-git-server.sh` 要求 root、绝对且不重叠的路径、显式 `--confirm`；拒绝
  高风险系统用户与符号链接，设置 `receive.denyNonFastForwards`/`denyDeletes`，
  原子安装 hooks，并复制可信 CI 脚本、登记脚本 SHA-256 与 marker/基线配置。递归
  接管范围限制在裸仓库和本安装创建的目录，不 chown 整个 CI 父树。

## files_changed

- `scripts/ci-local.sh`
- `deploy/git-hooks/pre-receive`
- `deploy/git-hooks/post-receive`
- `deploy/scripts/install-git-server.sh`
- `tests/deploy/deploy0-a.test.sh`
- `scripts/guard-governance-files.sh`
- `tests/security/governance-not-hollow.test.sh`
- `docs/superpowers/plans/2026-08-29-deploy0-implementation.md`

## tests_run

- `D:/Git/bin/bash.exe -n scripts/ci-local.sh deploy/git-hooks/pre-receive deploy/git-hooks/post-receive deploy/scripts/install-git-server.sh tests/deploy/deploy0-a.test.sh` — PASS
- `D:/Git/bin/bash.exe tests/deploy/deploy0-a.test.sh` — PASS (`DEPLOY0-A-TEST-OK`；覆盖四门禁失败传播、路径边界、marker 配置/并发、post CI red/cleanup、Git 元数据、可信脚本哈希、Docker 参数与环境隔离)
- `D:/Git/bin/bash.exe tests/security/governance-not-hollow.test.sh` — PASS
- `GOVERNANCE_BASE_REF=origin/main GOVERNANCE_REQUIRE_BASE=0 D:/Git/bin/bash.exe scripts/check-governance.sh` — PASS
- `go fmt ./...` — PASS
- 清除代理环境后 `go vet ./...` — PASS
- 清除代理环境后 `go test -p 1 -count=1 ./...` — PASS
- 临时固定 gitleaks v8.28.0：`gitleaks git --redact --no-banner --log-opts=HEAD^..HEAD` — PASS（本片提交无命中）
- WSL Ubuntu-24.04 全新检出 `abd23b9`：`pnpm install --frozen-lockfile --ignore-scripts --package-import-method=copy --offline`、`pnpm -r run typecheck`、`pnpm -r run test`、Storybook build、admin-web build — PASS
  - design-tokens: 1 文件 / 10 tests
  - ui-primitives: 7 文件 / 16 tests
  - ui-admin: 16 文件 / 219 tests
  - admin-web: 47 文件 / 881 tests
  - 构建保留既有单 chunk >500 kB 的提示，不影响成功
- `git diff --check` — PASS；最终 worktree clean

## tests_not_run

- 未在 fiberstate 或其他真实服务器执行 root 安装、SSH 推拉、Docker CI 镜像运行、
  临时 PostgreSQL/缓存挂载或生产部署。
- Windows 本地 `pnpm install` 受 esbuild `EPERM` 文件锁影响；前端门禁已在 WSL
  的干净临时检出复跑并通过。WSL 使用 Node 22，显示仓库要求 Node >=24 的 engine
  warning，需在服务器按锁定版本复核。
- gitleaks 全历史扫描仍会命中仓库已有测试占位（13 条 generic-api-key）；本片
  采用提交增量扫描并保留该基线债务，后续应由独立安全切片建立精确基线/替换占位，
  不得把真实生产凭据加入 allowlist。

## risks

- 同一 SHA 的锁在进程崩溃后会保留，后续推送会 fail-closed，需要人工确认后清理
  对应 `.lock` 目录；不会自动猜测锁已过期。
- 服务器必须预置与 `xm.ci.scriptSha256` 匹配的可信 CI 脚本和固定镜像；缺失或哈希
  不一致时 post-receive 保持 red，不执行未知脚本。
- `post-receive` 完成后 Git receive 本身可能已经返回成功，部署线必须读取并验证
  `<sha>.status=green`，不能只看 `git push` 返回码。
- 当前切片没有提供临时 PostgreSQL 编排；DEPLOY0-b/服务器 runbook 必须补齐该边界
  后才可把真实服务器 CI 标为可部署。

## follow_ups

- 验收线审读本 Handoff 与 3 个实现提交，在本地 release 集成线上复跑同一组门禁。
- DEPLOY0-b 实现 staging/release、prod/main 的 deploy/promote 脚本、探针、审计和
  受控数据库/缓存编排；不得绕过本片的可信脚本与 status gate。
- DEPLOY0-c 再处理 origin/服务器镜像和文档迁移；GitHub Actions 暂停期间不创建 PR。
- 单独安全切片处理 gitleaks 历史占位的替换/基线方案，并由人工审阅后再改扫描口径。
