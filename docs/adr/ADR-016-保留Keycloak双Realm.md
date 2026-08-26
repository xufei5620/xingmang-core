# ADR-016：保留 Keycloak，使用双 Realm 和变更单机制

| 项 | 值 |
|---|---|
| 状态 | 已接受 |
| 日期 | 2026-08-26 |
| 来源 | 规格 v2.1 §3 ADR-016（本文件拆分后为唯一权威版本） |

## 背景（事实边界）

Keycloak 基础设施和开票 Staging 已部署；现有 `solov` Realm 的 Client、
认证流和 ACR/MFA 配置正在使用；开票系统尚未完成正式公网生产上线。

现有 Realm 的准确 Client 数量和状态必须以导出后的
`docs/inventory/identity-inventory.yaml` 为准，不在架构正文写死。

## 决策

- 保留 Keycloak；
- `solov` 用户 Realm 冻结；
- 新建 `solov-staff` 员工 Realm；
- 员工 Realm 关闭公开注册，强制 MFA/Step-up；
- 平台细粒度业务权限主要由平台数据库管理，不把全部权限塞进 Keycloak Token；
- Keycloak 负责身份、认证强度、会话和 Client 信任。

## 理由

已有部署与配置资产；身份组件替换成本远高于治理成本。

## 边界与后果

### 变更单机制

所有 Keycloak 变更：

```text
平台线起草 docs/change-requests/CR-xxxx-*.md 或 GitHub Issue
→ Codex 开票线确认
→ auth-admin 执行
→ 回写执行结果和回滚结果
```

`K:\星芒` 仅为同步副本。

### 开票跨 Realm 三个选项

1. 直接信任：开票后端接受 `solov-staff` Issuer，并在该 Realm 注册专用 Client；
2. 身份代理：两个 Realm 建立受控 Identity Brokering；
3. 保持隔离：平台使用 ServicePrincipal 调用只读 API，开票原生后台独立登录。

第一阶段使用选项 3。开票稳定上线后由 Codex 线在 1 和 2 中选择。

### 升级

身份组件出现严重安全补丁时，7 天内评估和部署，不等待季度升级窗口。

## 替代方案

更换身份组件（如 Ory/Zitadel）：因既有资产与迁移风险否决。

## 重评条件

开票正式上线后在选项 1/2 中选择（规格 §26 待拍板项 12）。

## 相关契约

`docs/change-requests/`；开票只读契约见规格附录 H。

## 证据编号

V8（Keycloak Identity Brokering）。
