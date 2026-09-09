# ADR-013：River OSS 只作为后台任务队列

| 项 | 值 |
|---|---|
| 状态 | 已接受 |
| 日期 | 2026-08-26 |
| 来源 | 规格 v2.1 §3 ADR-013（本文件拆分后为唯一权威版本） |

## 背景

River OSS 是 PostgreSQL Job Queue；Workflows/Signals/Timers 属 River Pro
（证据 V1）。把 OSS 版扩展成工作流引擎会形成低配自研 Temporal。

## 决策

River OSS 职责限定为：

- 定时同步；余额和健康探针；告警投递；数据快照；报表生成；普通重试；
  批量任务拆分；超时扫描；周期任务；唯一任务和多队列。

简单审批使用平台数据库显式状态机，审批记录独立落表。River 只负责超时提醒、
重试和后续任务触发。

## 理由

v1 任务形态全部是短任务/定时/重试；长工作流需求出现时应评估专业引擎而非
自研扩展。

## 边界与后果

### Temporal 强制重评条件

开发下列任一能力前必须创建 `ADR-013-TEMPORAL-REVIEW`：

- 等待人工或外部事件数小时至数天；
- 跨系统 Saga/补偿；
- 持久化 Signal、Timer、Callback；
- 执行历史重放；
- 工作流版本治理；
- 长周期 AI Agent 恢复；
- 支付退款；
- 开票异常处理；
- 前端配置发布；
- TG/X 审批发布；
- 多系统批量迁移；
- AI 自动开发维护。

### 禁令

- 不购买 River Pro 来规避 Temporal；
- 不把自研状态机扩展成低配 Temporal。

## 替代方案

Temporal 默认化（原 ADR-008）：已 Superseded。

## 重评条件

见上文 Temporal 强制重评条件清单。

## 相关契约

无。

## 证据编号

V1（River 官方文档：Pro Workflows、Pro Changelog）；V6（Temporal 官方）。
