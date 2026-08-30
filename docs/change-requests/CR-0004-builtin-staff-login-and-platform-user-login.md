# CR-0004:星芒自带员工登录 + 开票中心用平台账号登录(2026-08-30,产品负责人拍板)

## 决定(产品负责人原话口径)
1. **星芒后台**是独立的账号密码系统(用户名 + 密码,账号由后台"人员与权限"管理),不用 Keycloak。
2. **开票中心的用户端**用 Sub2API / NewAPI 各自的账号密码登录:用 Sub2API 账号登录看到的就是该账号在
   Sub2API 的开票信息;用 NewAPI 账号登录就是 NewAPI 的。身份 = 平台 + 该平台用户 ID(与 CR-0003 一致)。
3. 理由:Keycloak 那套(跳转、改密、绑 OTP、短令牌)对内部后台与客户端都过于繁琐。

## 影响
- 宪法/ADR-005、ADR-016"身份由统一 IdP 管、平台不存口令"**修订**:星芒自存员工口令(argon2id 哈希),
  开票中心不存口令(仅转发到平台登录接口验证)。
- CR-0001(Keycloak `solov-staff` realm)**作废**;`deploy/keycloak/`、`docs/runbooks/KEYCLOAK-SOLOV-STAFF.md`
  保留为可选路径(XM_AUTH_MODE=oidc 代码不删)。
- 开票中心现有 Keycloak `solov` realm 登录 → 改为平台账号验证;迁移方案在 K:/发票 仓库单独出设计。

## 实施
- XM-LOGIN(星芒):`XM_AUTH_MODE=local`、`core.staff_account/staff_session`(迁移 000021)、
  `/api/v1/auth/{login,logout,me,password}`、`staff.account.*` Action、`cmd/staff-bootstrap`、登录页/改密页/人员与权限页。
- XM-INV-LOGIN(开票):登录页选平台 → 开票后端调用平台登录接口验证(HTTPS,口令不落库不落日志)→ 会话绑定
  (platform, platform_user_id);前置核对 Sub2API/NewAPI 登录接口可程序化调用(Turnstile 需豁免)。

## 安全底线
连续失败锁定、会话 12 小时、限流、登录/改密/账号变更全部审计;口令只以哈希存(星芒)或不存(开票)。
