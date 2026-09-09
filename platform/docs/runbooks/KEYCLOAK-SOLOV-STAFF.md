# 操作卡:导入 solov-staff Realm(CR-0001 执行版,2026-08-30)

执行人:auth-admin(Keycloak 变更红线保留给人)。全程只新增 `solov-staff`,**不碰 `solov` Realm**。

## 1. 备份现状(2 分钟)
Keycloak 容器/主机上:`/opt/keycloak/bin/kc.sh export --dir /tmp/kc-backup-before-cr0001 --realm solov --users skip`

## 2. 导入 Realm(3 分钟)
auth-admin.solov.cc → 左上 Realm 下拉 → **Create realm** → **Resource file** 选择仓库文件
`deploy/keycloak/solov-staff-realm.json` → Create。导入内容:Realm 设置(注册关闭、找回密码关闭、
5 分钟令牌、30 分钟空闲/8 小时上限)、Realm 角色 `staff/admin/credential-admin/auditor/key-metadata-reader`、
Required Action `Configure OTP` 设为默认、Client `xingmang-admin-web`(public、Standard flow、PKCE S256、
回调 `https://console.solov.cc/*` 与 `http://localhost:5173/*`、audience mapper)。

## 3. 强制 MFA 的浏览器流(3 分钟,UI 操作)
Authentication → Flows → `browser` → Duplicate → 名称 `staff-browser` → 在复制的流里把
**OTP Form** 的 requirement 从 Conditional 改为 **Required** → 右上 Action → **Bind flow → Browser flow**。

## 4. 首个管理员(2 分钟)
Users → Add user:用户名由产品负责人指定(不要 `admin`),Email verified ON;
Credentials → Set password,Temporary ON;Role mapping → 分配 `staff` + `admin` + `credential-admin`。

## 5. 验证
- `curl -s https://auth.solov.cc/realms/solov-staff/.well-known/openid-configuration | jq .issuer`
  必须逐字等于 `https://auth.solov.cc/realms/solov-staff`;
- 首次登录被要求改密码 + 绑定 OTP;登录后 Access Token 的 `azp` = `xingmang-admin-web`,
  `realm_access.roles` 含 `staff`/`admin`;
- 用一个现有 `solov` Realm 用户登录一次,确认无变化。

## 6. 平台侧切换(验收线执行)
服务器 `.env`:`XM_AUTH_MODE=oidc`、`XM_OIDC_ISSUER=https://auth.solov.cc/realms/solov-staff`、
`XM_OIDC_AUDIENCE=xingmang-admin-web`、`XM_WEB_AUTH_MODE=oidc`、`XM_WEB_OIDC_ISSUER=<同上>`、
`XM_WEB_OIDC_CLIENT_ID=xingmang-admin-web`,然后 `deploy-local.sh`;nginx 去掉 Basic Auth。

## 与 CR-0001 原稿的差异(规划线修订)
- 角色由"仅 staff"改为五个**粗粒度**角色(staff/admin/credential-admin/auditor/key-metadata-reader),
  与平台 `DefaultRoleScopeMap` 一一对应;仍不在 Keycloak 建任何 `registry.*`/`ops.*` 细粒度角色(ADR-016 不变)。
- 回调地址由 admin(-staging).solov.cc 改为 `console.solov.cc`。
