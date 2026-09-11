# CR-0010 排除与冻结清单

下列原分支保持原位冻结，不修改、不合入其功能提交。统一代码从完整审计基线重新提取；没有以测试通过替代业务取舍。

| 原分支 | 保留 HEAD | 排除内容 | 业务语义 | 发布流程 | schema |
|---|---|---|---|---|---|
| `ai/codex/XM-CARD-ALERT-SEVERITY-STORE-20260911` | `2c0baf8a` | 告警严重度和静默策略 | 是 | 否 | 否 |
| `ai/codex/XM-CARD-NOTIFICATION-RELIABILITY-20260911` | `3727d423` | 持久通知、回执、同步恢复和分页 | 是 | 是 | 是 |
| `ai/codex/XM-CARD-NOTIFY-OUTBOX-20260911` | `acfce654` | 卡通知 outbox/投递租约 | 是 | 是 | 是 |
| `ai/codex/XM-CARD-RECONCILE-PAGING-20260911` | `5851c6ea` | 分页/重复 ID 协议 | 是 | 否 | 接口约束 |
| `ai/codex/XM-CARD-SYNC-ALERTS-20260911` | `e73d714e` | 同步与告警恢复判断 | 是 | 否 | 接口 |
| `ai/codex/XM-INVOICE-ELIGIBILITY-STABILITY-20260911` | `01c4b16f` | 消费、非现金、迟到与重锚算法 | 是 | 是 | 来源桥/接口 |
| `ai/codex/XM-INVOICE-FUNDING-POLICY-20260911` | `01c6c476` | 管理员正额、套餐、返利核算及0033–0037 | 是 | 是 | 是 |
| `ai/codex/XM-NOTIFICATION-ACK-HARDENING-20260911` | `b9b4d808` | 企微响应严格回执/重试语义 | 是 | 否 | 确认协议 |
| `ai/codex/XM-INVOICE-UNIFIED-SERVICE-20260911` | `84335bf4` | 只提取CR必要实现；其0038邮箱挑战、资金/通知补丁和84335bf4兼容harness均排除 | 混有业务 | 是 | 是 |

PG remediation 不产生任何新提交；`84335bf4` 的兼容验证脚本没有进入新分支。原 logs 保留。两棵 PostgreSQL 使用原官方 digest，原 CVE 记录仅在 F 待负责人拍板，等待官方重建后由负责人另定换 digest。

其余审计辅助分支作为历史证据冻结，完整成果通过 `d4078754` 整体合入，不改任何已有审计提交。main 和签名 tag 保持开工时状态。既有依赖版本不升级；只删除已无调用的旧登录专用依赖、增加本地模块/工作区引用。
