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

## 6. 每个 worktree 独立测试库

**背景**：此前所有 worktree/agent 共用同一个 `xm_test` 库。2026-09-02 门禁脚本
第一次导出 `XM_TEST_DATABASE_URL`（此前一直未导出，所有 DB 集成测试从未真正
跑过，见 `docs/handoffs/slices/XM-AUTH-TOTP0.md`），随即在 2026-09-03 暴露出
`internal/platform/credentials` 与 `internal/platform/jobs` 两个包共用同一个
`xm_test` 库时的真实冲突：`credentials` 包的测试往 `core.connector_config` 写
`(sub2api|newapi, staging)` 这两行且跑完不清理，`jobs` 包的同库测试一旦跑在它
后面就会撞上残留行报 `duplicate key value violates unique constraint`（详见
`docs/handoffs/slices/XM-DBTEST-FIX0.md` 的 risks 一节）。这是"跑测顺序/并发
敏感"的耦合，根上是多个 worktree/包共用同一个库；每个 worktree 拿到自己独立
的库就不会再撞。

**用法**（`scripts/dev/worktree-testdb.sh`，POSIX bash，Windows 下用 Git Bash
跑；PowerShell 用户可用同目录的 `worktree-testdb.ps1`，参数一一对应，行为
逐字一致）：

```bash
# 默认动作：库不存在就建、灌迁移到最新版本，打印 export 语句
# （本仓库脚本不靠可执行位分发，一律显式 bash 前缀，同 check-governance.sh）
bash scripts/dev/worktree-testdb.sh
# 直接让当前 shell 生效：
testdb_exports=$(bash scripts/dev/worktree-testdb.sh) || { echo 'test database provisioning failed' >&2; exit 1; }
[[ -n "$testdb_exports" ]] || { echo 'test database environment is empty' >&2; exit 1; }
eval "$testdb_exports"
# 或只取连接串自己赋值（--print-url 只打印 URL，不带 export 前缀）：
testdb_url=$(bash scripts/dev/worktree-testdb.sh --print-url) || { echo 'test database provisioning failed' >&2; exit 1; }
[[ -n "$testdb_url" ]] || { echo 'test database URL is empty' >&2; exit 1; }
export XM_TEST_DATABASE_URL="$testdb_url"

# 列出所有 xm_test_* 测试库及大小
bash scripts/dev/worktree-testdb.sh --list
```

```powershell
$env:XM_TEST_DATABASE_URL = & scripts\dev\worktree-testdb.ps1 -PrintUrl
scripts\dev\worktree-testdb.ps1 -ListDatabases
```

库名由当前 Git worktree 完整路径派生：basename 规整成 `[a-z0-9_]`，加
`xm_test_` 前缀，并始终附加规范路径 SHA-256 的前 16 位十六进制摘要。
可读短名按需截断，总长不超过 63 字节；同 basename、大小写/符号规整相同
的不同路径可区分。迁移工作树位置会派生新库。管理
连接默认是本机 `invoice-test-pg` 容器（`127.0.0.1:55432`，superuser
`postgres`），可用 `--pg-url` 覆盖。

**清理**：任务结束、移除 worktree 前跑一次

```bash
bash scripts/dev/worktree-testdb.sh --drop
```

删除当前 worktree 专属的测试库（含终止其残留连接）；不会碰其他 worktree 的库
或旧的共享 `xm_test` 库。旧版 `xm_test_<basename>` 库不会自动重用、删除或
重命名；保留原库，只有核对所有者和用途后再单独决定是否清理。
