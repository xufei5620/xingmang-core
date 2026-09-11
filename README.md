# 星芒（xingmang）

一个仓库、两个子系统，各自保留完整提交历史（经 `git subtree` 并入）：

| 目录 | 子系统 | 生产 | 说明 |
|---|---|---|---|
| `platform/` | 星芒统一控制平台（Go + React 管理端 + PostgreSQL + River） | 验收线 `release/v0.1-launch`，2026-09-09 部署 `3800a8b` | 原仓库 `xingmang-platform` |
| `invoice/` | 开票系统（Go 后端 + 采集代理 + React 用户端/管理端） | `v0.1.0-rc110-signed` = `277063c`，2026-09-09 部署 | 原仓库 `invoice-system` |

各子系统的工作入口、红线与常用命令见 `platform/CLAUDE.md` 与 `invoice/README.md` / `invoice/RELEASE-READINESS.md`；跨子系统的接口变更走 `platform/docs/change-requests/`。

## monorepo 源码与生产状态

- 本地候选已适配 monorepo：平台 Git 根与 `platform/` 项目根分开；开票工具从
  `invoice/` 解析项目资产。当前审查/修复仍按任务指定的独立工作树和批准基线执行。
- 源码适配不等于 main 已合并或生产已切换。上表是原生产版本的历史交接快照，
  本地验证、签名演练或镜像加载都不代表新的部署。
- 目录约定与剩余边界见 [迁移说明](docs/MONOREPO-MIGRATION.md)；
  已执行步骤和未上线范围见 [切根交接](docs/handoffs/MONOREPO-CUTOVER.md)。
- 两套历史已通过 subtree 纳入；旧 subtree pull 流程保留在历史记录中，
  不作为当前 monorepo 的自动同步命令。main 集成、服务器真相源及远端目标须负责人确认。
