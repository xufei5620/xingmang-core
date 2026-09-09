# CLAUDE.md — 星芒 monorepo 工作入口

本仓库由两个子系统组成，规则分别在：

- `platform/CLAUDE.md` —— 星芒统一控制平台（红线、Action/Query、CredentialRef、门禁命令）
- `invoice/README.md`、`invoice/RELEASE-READINESS.md`、`invoice/docs/PRODUCTION-RUNBOOK.md` —— 开票系统（发布门禁、签名 tag、runbook；该子系统没有 CLAUDE.md）

改哪个子系统就读哪份；两份里的红线在本仓库同样有效。跨子系统接口变更走 `platform/docs/change-requests/`。
发布链切换到 monorepo 根之前，构建/门禁命令仍在各子目录里执行（`cd platform` / `cd invoice`）。
