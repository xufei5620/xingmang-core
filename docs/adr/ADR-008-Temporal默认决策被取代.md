# ADR-008：原 Temporal 默认决策被 ADR-013 取代

| 项 | 值 |
|---|---|
| 状态 | Superseded（被 [ADR-013](ADR-013-River只作后台任务队列.md) 取代） |
| 日期 | 2026-08-26 |
| 来源 | 规格 v2.1 §3 ADR-008（本文件拆分后为唯一权威版本） |

## 背景

早期版本曾默认"所有持久化 Workflow 使用 Temporal"。

## 决策

原"所有持久化 Workflow 使用 Temporal"的默认方案不再作为 v1 基线。
Temporal 根据 ADR-013 的能力触发条件重新评估。

## 理由

v1 范围内没有强持久工作流需求；默认引入 Temporal 的运维成本不成比例。

## 边界与后果

见 ADR-013 的 Temporal 强制重评条件。

## 替代方案

维持 Temporal 默认：被 ADR-013 否决。

## 重评条件

见 ADR-013。

## 相关契约

无。

## 证据编号

V6（Temporal 官方 Approval Pattern 与 Document Approvals）。
