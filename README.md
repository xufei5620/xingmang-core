# 星芒（xingmang）

一个仓库、两个子系统，各自保留完整提交历史（经 `git subtree` 并入）：

| 目录 | 子系统 | 生产 | 说明 |
|---|---|---|---|
| `platform/` | 星芒统一控制平台（Go + React 管理端 + PostgreSQL + River） | 验收线 `release/v0.1-launch`，2026-09-09 部署 `3800a8b` | 原仓库 `xingmang-platform` |
| `invoice/` | 开票系统（Go 后端 + 采集代理 + React 用户端/管理端） | `v0.1.0-rc110-signed` = `277063c`，2026-09-09 部署 | 原仓库 `invoice-system` |

各子系统的工作入口、红线与常用命令见 `platform/CLAUDE.md` 与 `invoice/README.md` / `invoice/RELEASE-READINESS.md`；跨子系统的接口变更走 `platform/docs/change-requests/`。

## 与两个原工作仓库的关系（过渡期）

- 两条发布链（平台 `deploy/scripts/deploy-local.sh`、开票 `scripts/verify.ps1` → 镜像门禁 → 签名 tag → bundle → 服务器 stage2）目前仍按「仓库根 = 子系统根」写的，
  切到本仓库为唯一工作仓库前，要把它们改成以 `platform/`、`invoice/` 为根（见 `docs/MONOREPO-MIGRATION.md`）。
- 在那之前，本仓库用 `git subtree pull` 从原仓库同步：
  ```
  git subtree pull --prefix=platform <platform-repo> release/v0.1-launch -m "sync(platform): …"
  git subtree pull --prefix=invoice  <invoice-repo>  <release-branch>    -m "sync(invoice): …"
  ```
