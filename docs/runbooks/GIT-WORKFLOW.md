# Runbook：服务器中心 Git 工作流（XM-C-DEPLOY0-c）

本文件是 GitHub Actions 停摆期间的当前操作口径。服务器裸仓库承载开发、门禁
和发布闭环；GitHub 只保存镜像，不承担放行判断。

## 1. Remote 约定

每个本地 checkout 最终应有两条 remote：

| 名称 | 用途 | 是否作为门禁依据 |
|---|---|---|
| `origin` | 服务器 fiberstate 裸仓库（默认 `/srv/git/xingmang-platform.git`） | 是 |
| `github` | `github.com/xufei5620/xingmang-platform.git` 异地镜像 | 否 |

不要手工删除未知 remote 或覆盖已有 `pushurl`。先用 dry-run 检查：

```bash
deploy/scripts/configure-remotes.sh \
  --server-url /srv/git/xingmang-platform.git \
  --github-url git@github.com:xufei5620/xingmang-platform.git \
  --dry-run
```

确认输出中的现有 origin 确实是旧 GitHub 地址后，再执行一次显式写入：

```bash
deploy/scripts/configure-remotes.sh \
  --server-url /srv/git/xingmang-platform.git \
  --github-url git@github.com:xufei5620/xingmang-platform.git \
  --confirm CONFIGURE-REMOTES
```

脚本只改 `.git/config`，不 push、不改分支；确认 token 不是密码。临时测试目录
必须同时显式 `--test-mode` 和 `XM_DEPLOY_TEST_MODE=1`。

## 2. 切片交付

1. 从最新 `origin/release/v0.1-launch` 创建 `ai/codex/XM-…`（或对应工具前缀）
   独立 worktree；
2. 在分支内提交 `docs/handoffs/slices/XM-….md`，记录 status、commit、变更、
   门禁、未运行项、风险和 follow-up；
3. 本地运行适用的 Go/前端/治理/安全门禁，全部有证据后把 Handoff 标为 READY；
4. 把分支推送到服务器 `origin`，由验收线审读后合入 release；不创建 PR 作为
   过渡期门禁，不自动推 main/生产；
5. 服务器 `post-receive` 会为 release SHA 写 `/srv/ci/<sha>.status`。部署只接受
   严格单行 `green`，push 返回成功本身不等于通过。

## 3. GitHub 镜像

合入或发布后，由授权操作者显式运行一次：

```bash
deploy/scripts/mirror-github.sh --reason "release mirror XM-…"
```

镜像脚本只允许 `github` remote，执行一次 `git push --mirror github`，不自动重试。
镜像失败会返回非零并写明原因，但不改变服务器门禁、release 合入或本地部署结果；
修复网络后由人再次发起一次明确的镜像操作。

## 4. 发布与审计

服务器发布使用 `deploy/scripts/deploy.sh staging|prod`，晋级 main 使用
`deploy/scripts/promote.sh`；具体生产确认和 Compose 档见 `DEPLOY.md`。所有生产
动作必须由产品负责人提供二次确认、reason 和审计记录，AI 不自动执行。

## 5. GitHub Actions / PR 的历史边界

`.github/workflows/` 与旧设计稿中的 PR 命令保留用于历史追溯和未来恢复，不是当前
放行条件。任何要恢复 Actions、ruleset、PR 必选检查或更换服务器 origin 的动作，
都应先写新的变更记录并由产品负责人批准；不要在切片中偷偷切回双轨。
