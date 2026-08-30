# 验收日志(验收线追加;Codex 每片开工前/完成后读最新几行)

2026-08-29T16:27Z MERGED 2ab6761 XM-B003a-impl XM-C-MAP0-impl XM-C-USER0-impl XM-C-CREDALERT-impl XM-C004-impl XM-C-AUDITUI-impl XM-LOCAL-detail-impl XM-SUPPLY-detail-impl XM-LOCAL-controls-impl XM-ROUTE-integration-impl XM-DEPLOY-LOCAL(含迁移000015/000016,请执行 deploy-local.sh 并回报 DEPLOYED)
2026-08-29T16:54Z NOTE XM-C004-newapi-finance 为早期版本(14:27),已被合入的 XM-C004-impl 覆盖,不再合入;XM-C-RUNWAY0-impl 仍标 BLOCKED——审批已在冲刺指令 7.2 给出,请按 release 重算编号(000017)后标 READY
2026-08-29T18:30Z MERGED 809eec2 XM-C-RUNWAY0-impl-v2(含迁移000017+runway-threshold-bootstrap tools 服务,请执行 deploy-local.sh 并回报 DEPLOYED;NOTE:本片顺带带入 tools/local-progress 与 scripts/update-local-progress.ps1,未在 Handoff files_changed 声明,已接受为开发工具,下次请声明;XM-C-USER0-v2 见 IN_REVIEW,标 READY 后即验收)
2026-08-30T05:16Z MERGED b94ea5b XM-C-USER0-v2(DailyUsage/Key 元数据只读,scope platform.user_keys.read 按 KEY_SCOPE_APPROVAL 仅 key-metadata-reader 角色;无迁移,请 deploy-local.sh 重建 api/web 并回报 DEPLOYED;下一片按队列:REAL0-d verify-real-mode.sh → REAL0-a SecretProvider → CR-0003 开票按平台隔离 → RL0/DS0/R210/R215/DBR0/AUD2)
