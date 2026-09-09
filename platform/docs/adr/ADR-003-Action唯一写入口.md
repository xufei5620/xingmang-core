# ADR-003：Action 是所有业务和平台配置写操作的唯一入口

| 项 | 值 |
|---|---|
| 状态 | 已接受 |
| 日期 | 2026-08-26 |
| 来源 | 规格 v2.1 §3 ADR-003（本文件拆分后为唯一权威版本） |

## 背景

写操作分散会导致权限、审计、幂等与补偿不可控。

## 决策

所有读取走 Query；所有产生状态变化的业务写操作和平台配置写操作，无论风险
等级 L0～L4，必须经过 Action。Action 分两级交付：

### Action Core Lite（Foundation-A）

- Action ID 和版本；参数 Schema；Principal；权限检查；Environment；
  事务边界；Request ID；基础审计；统一错误模型；L0/L1 快速执行路径。

### Action Advanced Controls（Foundation-B）

- L2～L4 风险策略；幂等键；写后读取确认；人工审批；Step-up MFA；
  执行计划预览；冷却期和取消窗口；Kill Switch；补偿和人工修复路径；
  高风险环境限制。

## 理由

统一入口是权限、审批、审计、幂等、Kill Switch 生效的前提。

## 边界与后果

### 风险等级

| 等级 | 例子 | 基础控制 |
|---|---|---|
| L0 | 保存个人视图、低影响偏好 | 权限 + 基础审计 |
| L1 | 修改低风险平台配置、确认普通告警 | 权限 + 审计；按需幂等 |
| L2 | 批量配置、启停低风险资源 | 预览 + 幂等 + 写后确认 + 完整审计 |
| L3 | 服务切换、账号批量导入、敏感配置 | 人工批准 + MFA + 冷却 + 补偿 |
| L4 | 退款、生产基础设施高影响动作、开票关键动作 | 双人审批目标；单人阶段 Break-glass |

### 补偿能力声明

每个 Action 必须声明 `compensation_mode: AUTOMATIC | MANUAL | NOT_POSSIBLE`。
不得把"补偿"误写成所有动作都能真正回滚。Action 文档必须区分：自动补偿、
回滚、人工修复、对账纠正、不可逆动作。

### Platform Lifecycle Operation

数据库迁移、Bootstrap、Keycloak Provisioning、备份恢复和获批数据修复不走
普通 Action API，但必须通过版本化脚本、变更单、人工批准、制品校验和独立
审计执行。

### 宪法条款

> 可以降低低风险 Action 的执行成本，但不允许任何业务或平台配置写操作绕过 Action。

## 替代方案

各模块自定义写接口：因治理不可控否决。

## 重评条件

无。

## 相关契约

`contracts/actions/`（Action 定义，示例见规格附录 D）。

## 证据编号

无。
