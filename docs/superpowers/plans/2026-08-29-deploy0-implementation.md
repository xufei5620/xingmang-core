# XM-C-DEPLOY0 · 服务器为中心工作流实施计划

> **For agentic workers:** 每个子片独立分支、独立 Handoff，并在本地完成门禁后才标记 READY。

**Goal:** 用 fiberstate 自托管裸仓库、服务器门禁和受控部署脚本替代 GitHub Actions/PR 作为主要闭环，同时保留 GitHub 仅作镜像。

**Architecture:** DEPLOY0-a 提供本地/服务器共用的 CI 脚本、裸仓库钩子和一次性安装脚本；DEPLOY0-b 提供 staging/prod 部署与 promote 脚本；DEPLOY0-c 提供 origin 切换、GitHub 镜像和文档迁移。脚本默认 fail-closed、无隐式凭据、不会自动执行生产动作。

**Tech Stack:** Bash、POSIX 工具、Docker Compose、Git receive hooks、PowerShell 仅用于 Windows 本地验证。

**Spec:** `docs/superpowers/plans/2026-08-29-server-centric-workflow.md`

## Global Constraints

- GitHub Actions 停摆期间不创建 PR；每片使用 `ai/codex/XM-…` 分支和 `docs/handoffs/slices/XM-….md`。
- GitHub 只作镜像；镜像失败不阻塞本地/服务器验收，且不重复重试。
- 生产部署必须显式指定 `prod`、二次确认并写审计日志；没有确认不得执行。
- CI 容器默认 `--network none`，只挂载受控缓存和临时 PostgreSQL；不得读取生产凭据。
- 真实服务器安装由产品负责人以 root 执行；本地只做脚本静态/模拟验证。

### DEPLOY0-a

**Files:** `scripts/ci-local.sh`, `deploy/git-hooks/pre-receive`, `deploy/git-hooks/post-receive`, `deploy/scripts/install-git-server.sh`, tests and Handoff.

**Acceptance:** 四门禁顺序可复现；post-receive 只接受 release 触发 CI；pre-receive 拒绝非快进和未经 promote 的 main；安装脚本要求 root、显式路径和确认，不打印密钥。

### DEPLOY0-b

**Files:** `deploy/scripts/deploy.sh`, `promote.sh`, server compose/override, Nginx
templates, governance protection, tests and Handoff.

**Acceptance:** staging/release 与 prod/main 分离；构建、健康探针、失败停止和
审计记录可验证；无确认不得生产部署；生产只能使用安装器登记的 repo/status/
Compose/hook 路径，main 晋级必须经过可信 pre-receive 与 promote 授权锁。
Telegram 不在脚本内接收 token，采用可选的受控通知适配器（脱敏 stdin），
凭据解析留在服务器外部 CredentialRef 适配层。

### DEPLOY0-c

**Files:** origin/mirror helper, hook documentation, `AGENTS.md`, `CLAUDE.md`, handoff updates and Handoff.

**Acceptance:** 默认 origin 指向服务器裸仓库，GitHub remote 命名为 github；镜像命令显式、可失败不阻塞；旧 PR 流程文档改为分支 Handoff 流程。
