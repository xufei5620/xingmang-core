# 架构基线 v2.1（索引）

本文件是 ADR 索引，不是规范正文；每条决策以 `docs/adr/` 独立文件为唯一权威。
原始总规格存档于 `docs/evidence/`（追溯用，不具规范效力，规格 §0.1）。

## 一句话架构原则

> 后台统一，身份分域，权限统一，动作统一，数据分治，接口集成；
> 控制平台不进入用户实时请求路径、不拥有第三方业务真相、不直接写第三方业务原表。

## ADR 索引

| 编号 | 标题 | 状态 |
|---|---|---|
| [ADR-001](../adr/ADR-001-独立平台仓库.md) | 新建独立平台仓库 | 已接受 |
| [ADR-002](../adr/ADR-002-模块化单体优先.md) | 模块化单体优先 | 已接受 |
| [ADR-003](../adr/ADR-003-Action唯一写入口.md) | Action 是所有写操作唯一入口 | 已接受 |
| [ADR-004](../adr/ADR-004-Connector隔离第三方差异.md) | Connector 隔离第三方差异 | 已接受 |
| [ADR-005](../adr/ADR-005-身份分域.md) | 用户、员工和机器身份分域 | 已接受 |
| [ADR-006](../adr/ADR-006-财务体验统一领域不合并.md) | 财务体验统一，领域不合并 | 已接受 |
| [ADR-007](../adr/ADR-007-多前端统一协议ConfigEdge后置.md) | 多前端统一协议，Config Edge 后置 | 已接受 |
| [ADR-008](../adr/ADR-008-Temporal默认决策被取代.md) | 原 Temporal 默认决策 | Superseded |
| [ADR-009](../adr/ADR-009-AI复用Action不设后门.md) | AI 复用 Action，不设后门 | 已接受 |
| [ADR-010](../adr/ADR-010-模型真实性概率审计.md) | 模型真实性采用概率审计 | 已接受 |
| [ADR-011](../adr/ADR-011-控制平面与数据平面边界.md) | 控制平面与数据平面边界 | 已接受 |
| [ADR-012](../adr/ADR-012-桌面端治理废止.md) | 桌面端治理废止 | 废止 |
| [ADR-013](../adr/ADR-013-River只作后台任务队列.md) | River OSS 只作后台任务队列 | 已接受 |
| [ADR-014](../adr/ADR-014-SecretProvider与CredentialRef.md) | SecretProvider/CredentialRef 第一天启用 | 已接受 |
| [ADR-015](../adr/ADR-015-Tailscale网络与命令信封.md) | Tailscale 网络层 + 命令信封鉴权 | 已接受 |
| [ADR-016](../adr/ADR-016-保留Keycloak双Realm.md) | 保留 Keycloak，双 Realm + 变更单 | 已接受 |
| [ADR-017](../adr/ADR-017-Sub2API战略去留不阻塞.md) | Sub2API 战略去留不阻塞平台建设 | 待决策 |
| [ADR-018](../adr/ADR-018-开票与平台数据通道隔离.md) | 开票和平台数据通道隔离 | 已接受 |
| [ADR-019](../adr/ADR-019-渠道主动探测通道.md) | 渠道主动探测通道 | 已接受 |
| [ADR-020](../adr/ADR-020-请求日志记录器的上游库只读接入.md) | 请求日志记录器的上游库只读接入 | 提议 |

## 里程碑主线（规格 §22.1）

开工门槛 P-0~P-6 → Foundation-A → M1 → M1.5 → Foundation-B →
M2/M3/M4（A 读先行、B 写依赖 Foundation-B）→ 开票二阶段 → 后置能力。

## 跨切片路线索引

单条变更单涉及多个有先后依赖的切片时，其执行排序记录在 `docs/roadmap/`，本节只做索引，不重复内容：

| 变更单 | 路线文件 |
|---|---|
| [CR-0006](../change-requests/CR-0006-console-auth-for-invoice-admin.md) | [CR-0006-console-auth-slices.md](../roadmap/CR-0006-console-auth-slices.md) |
