# 上线切换清单(console.solov.cc → 生产态)

顺序:① CRED0 后端/worker/UI 合入并部署(仍 staging)→ ② 后台录入凭据、切 real,验证真实数据 →
③ 用户导入 Keycloak realm(`docs/runbooks/KEYCLOAK-SOLOV-STAFF.md`)→ ④ AUTH1 部署,`XM_WEB_AUTH_MODE=oidc` 验证登录 →
⑤ 切生产 compose 覆盖并去掉 Basic Auth。

## ⑤ 生产切换(服务器 /srv/deploy/xingmang-platform,验收线执行)

`.env` 改动(其余不动):
```
ENVIRONMENT=production
XM_AUTH_MODE=oidc
XM_OIDC_ISSUER=https://auth.solov.cc/realms/solov-staff
XM_OIDC_AUDIENCE=xingmang-admin-web
XM_WEB_AUTH_MODE=oidc
XM_WEB_OIDC_ISSUER=https://auth.solov.cc/realms/solov-staff
XM_WEB_OIDC_CLIENT_ID=xingmang-admin-web
XM_FINANCE_FAKE_SEED=false
XM_SECRET_ROOT=/run/xm/secrets
# 生产要求显式模式;实际接入模式由 core.connector_config(后台"接入模式")决定,这里只是兜底默认
XM_SUB2API_MODE=fake
XM_SUB2API_INSTANCE_ID=sub2api-prod
XM_NEWAPI_MODE=fake
XM_NEWAPI_INSTANCE_ID=newapi-prod
XM_FINANCE_COLLECT_MODE=fake
XM_FINANCE_COLLECT_INSTANCE_ID=finance-prod
```

数据:生产不带演示数据——新建数据库卷(`docker volume create xingmang-launch_postgres-data-prod` 并在 .env/compose 里切换),
凭据文件卷 `xm-secrets` 保留(后台录入的凭据随之保留)。

部署:`cd /srv/deploy/xingmang-platform && nice -n 10 deploy/scripts/deploy-local.sh --override-file deploy/compose/server-prod.yaml`
(演示登记服务在 prod 覆盖下不存在,脚本自动跳过)。

nginx:编辑 `/www/server/panel/vhost/nginx/console.solov.cc.conf` 删除 `auth_basic` 两行,`nginx -t && nginx -s reload`。

验证:`/healthz` 构建号 = 合入 sha;未登录访问 302 到 Keycloak;登录后 `GET /api/v1/credentials` 200;
`scripts/verify-real-mode.sh --platform sub2api --environment production` PASS;页面无"演示数据"横幅、无"开发模式"标签。

回退:`.env` 改回 staging 值,`deploy-local.sh`(不带覆盖文件),nginx 恢复 Basic Auth。
