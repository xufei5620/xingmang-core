# CR-0001 新建 Keycloak `solov-staff` 员工 Realm

| 项 | 值 |
|---|---|
| 状态 | 待确认（等 Codex 开票线确认 → auth-admin 执行） |
| 起草日期 | 2026-08-26 |
| 起草方 | 平台线（Claude） |
| 依据 | ADR-005（身份分域）、ADR-016（保留 Keycloak，双 Realm + 变更单）、规格 §4.1 |
| 关联任务 | XM-0007（本变更单）、XM-0008（平台接入 Staff 登录） |

## 发起方

平台线。星芒统一控制平台需要员工身份域才能实现"员工可安全登录"（规格 §22.2
Foundation-A 退出条件第一条）。

## 接收方

- **Codex 开票线**：确认本变更不影响开票系统现有的 `solov` Realm 集成；
- **auth-admin**：实际执行。

## 目标资源

Keycloak 实例（`https://auth.solov.cc`）—— **新增** 一个 Realm 及其下属对象。

## 变更内容

### 1. 新建 Realm `solov-staff`

| 设置项 | 值 | 理由 |
|---|---|---|
| Realm name | `solov-staff` | ADR-005 命名 |
| Display name | 星芒员工 | — |
| Enabled | ON | — |
| **User registration** | **OFF** | ADR-005：员工 Realm 关闭公开注册 |
| Forgot password | OFF | 员工账号由管理员维护，避免邮件找回成为攻击面 |
| Remember me | OFF | — |
| Email as username | OFF | 用显式用户名，便于审计对齐 |
| Login with email | ON | 便利性，不放宽注册 |
| SSL required | `external requests`（生产建议 `all`） | — |
| Access Token Lifespan | 5 分钟 | 短令牌；平台侧会做 Issuer/Audience/ACR 校验 |
| SSO Session Idle | 30 分钟 | 管理后台无操作即登出 |
| SSO Session Max | 8 小时 | 单个工作日上限 |

### 2. 强制 MFA（ADR-005：员工 Realm 强制 MFA/Step-up）

- Authentication → Required Actions：**`Configure OTP` 设为 Default Action + Enabled**
  （新用户首次登录必须绑定 OTP）；
- Authentication → Flows：复制 `browser` 流为 `staff-browser`，把
  **OTP Form 由 `Conditional` 改为 `Required`**，并把该流绑定为 Realm 的 Browser Flow；
- 目的：平台会校验 Token 中的 `acr`/`amr`，未完成 MFA 的会话不允许进入管理后台。

> Step-up MFA（L3/L4 动作前的二次验证）属 Foundation-B（XM-0030），本次**不配置**，
> 但上面的流程设计需为将来加 `acr_values` 条件留出空间——请勿删除 `staff-browser`
> 流的条件子流结构。

### 3. 新建 Client `xingmang-admin-web`

| 设置项 | 值 |
|---|---|
| Client ID | `xingmang-admin-web` |
| Client type | OpenID Connect |
| **Client authentication** | **OFF（public client）** |
| Standard flow | ON |
| Direct access grants | **OFF**（禁止密码直传，强制走浏览器流 + MFA） |
| Implicit flow | OFF |
| Service accounts | OFF |
| **PKCE Challenge Method** | **S256**（Advanced → Proof Key for Code Exchange） |
| Valid redirect URIs | `https://admin-staging.solov.cc/*`<br>`https://admin.solov.cc/*`<br>`http://localhost:5173/*`（本地开发） |
| Valid post logout redirect URIs | 同上 |
| Web origins | `https://admin-staging.solov.cc`<br>`https://admin.solov.cc`<br>`http://localhost:5173` |

### 4. 首个管理员用户（Bootstrap）

- 用户名：由产品负责人指定（建议不用 `admin`）；
- Email verified：ON；
- 首次登录必须完成 `Configure OTP`；
- 密码：临时密码 + Temporary=ON，强制首登修改。

> 这是 Platform Lifecycle Operation（ADR-003 / 规格 §2.4）：不走 Action API，
> 但必须有本变更单 + 人工批准 + 执行记录。

### 5. Realm Role（**仅一个**，边界见下）

- 新建 Realm Role：`staff`（描述：员工身份标记）；
- 分配给上述 bootstrap 用户。

> **平台细粒度权限不进 Keycloak**（ADR-016）。`registry.service.manage` 这类权限
> 由平台数据库管理，不塞进 Token。Keycloak 只回答"这是不是员工、认证强度够不够"。
> 请**不要**在 Realm 里创建 `registry.*` / `ops.*` 之类的角色。

### 6. 导出身份清单（ADR-016 要求）

执行完成后导出现状（含**现有** `solov` Realm 的 Client 清单），交平台线写入
`docs/inventory/identity-inventory.yaml`：

```bash
# 仅导出配置，不含用户与凭据
/opt/keycloak/bin/kc.sh export --dir /tmp/kc-export --users skip
```

需要的字段：每个 Realm 的名称、Client ID 列表、各 Client 的 flow 开关、
Browser Flow 名称、Required Actions。**不要导出任何 Secret 或用户数据**。

## 明确不变

- **`solov` Realm 完全不动**——不改 Client、不改认证流、不改 ACR/MFA 配置、
  不改用户（ADR-016：现有用户 Realm 冻结）；
- 不建立两个 Realm 之间的 Identity Brokering（第一阶段用选项 3：保持隔离）；
- 不修改 Bridge V4 的任何角色、函数或 Agent（ADR-018：开票专属）；
- 不为开票系统创建任何新 Client（开票跨 Realm 方案待开票稳定上线后再定，
  规格 §26 待拍板项 12）；
- 不改 Keycloak 版本与部署方式。

## 影响面

| 对象 | 影响 |
|---|---|
| 开票系统 | **无**。开票用 `solov` Realm，本次不触碰 |
| 现有终端用户 | **无**。`solov` Realm 冻结 |
| Keycloak 实例 | 新增一个 Realm，内存与数据库占用小幅上升（Realm 级开销，非用户级） |
| 平台线 | 解除 XM-0008 阻塞，可实现员工登录 |

**风险**：若误改 `solov` Realm 的 Browser Flow，会影响现有用户登录。
执行前请确认操作对象是 `solov-staff` 而非 `solov`——两个名字只差一个后缀。

## 验证

执行后逐条确认：

1. `https://auth.solov.cc/realms/solov-staff/.well-known/openid-configuration`
   返回 200，且 `issuer` 为 `https://auth.solov.cc/realms/solov-staff`；
2. 匿名访问注册页应**不可用**（User registration = OFF）；
3. bootstrap 用户首次登录：被要求改密码 **且** 被要求绑定 OTP；
4. 绑定 OTP 后再次登录，解析 Access Token 应看到：
   - `iss` = `https://auth.solov.cc/realms/solov-staff`
   - `azp` = `xingmang-admin-web`
   - `acr` / `amr` 体现已完成 OTP
   - `realm_access.roles` 含 `staff`，且**不含**任何 `registry.*`/`ops.*` 角色
5. **`solov` Realm 回归检查**：用一个现有终端用户登录一次，确认流程无变化；
6. 开票系统登录一次，确认无变化（由 Codex 线确认）。

## 回滚

风险低且可逆——本变更只做"新增"：

1. 删除 Realm `solov-staff`（Realm settings → Action → Delete）；
2. 确认 `solov` Realm 未被触碰（对照执行前导出）；
3. 平台侧无需回滚（XM-0008 尚未上线，无依赖）。

**执行前请先导出 `solov` Realm 现状备份**，以便万一误操作可对照恢复：

```bash
/opt/keycloak/bin/kc.sh export --dir /tmp/kc-backup-before-cr0001 --realm solov --users skip
```

## 确认

- 平台线：Claude（起草，2026-08-26）
- 开票线（Codex）：__待确认__（请确认"不影响开票现有集成"）
- 产品负责人批准：__待批准__

## 执行记录

| 项 | 内容 |
|---|---|
| 执行人 | __待填__ |
| 执行时间 | __待填__ |
| Keycloak 版本 | __待填__ |
| 执行前备份路径 | __待填__ |
| 验证结果（6 项逐条） | __待填__ |
| 遇到的偏差 | __待填__ |
| 回滚是否触发 | __待填__ |

> 执行完成后请把本表填好并提 PR 回写本文件（宪法 19 条：决定未回写仓库不算正式决定）。
