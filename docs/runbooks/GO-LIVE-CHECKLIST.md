# 上线切换清单(console.solov.cc → 生产态)

顺序:① CRED0 后端/worker/UI 合入并部署(仍 staging)→ ② 后台录入凭据、切 real,验证真实数据 →
③ 用户导入 Keycloak realm(`docs/runbooks/KEYCLOAK-SOLOV-STAFF.md`)→ ④ AUTH1 部署,`XM_WEB_AUTH_MODE=oidc` 验证登录 →
⑤ 切生产 compose 覆盖并去掉 Basic Auth。

## ⑤ 生产切换 —— 已于 2026-08-30 执行(CR-0004 之后的实际流程,取代上面的 Keycloak 版本)

前提:③④ 已被 CR-0004 取代——星芒后台用自带账号登录(XM-LOGIN,`XM_AUTH_MODE=local`),不再需要 Keycloak。

`.env` 改动(服务器 `/srv/deploy/xingmang-platform/deploy/compose/.env`,改前先 `cp -p .env .env.bak-staging-<时间>`):
```
ENVIRONMENT=production
XM_AUTH_MODE=local
XM_WEB_AUTH_MODE=local
XM_FINANCE_FAKE_SEED=false          # true 会让 platform-api 在生产拒绝启动
XM_REQLOG_MODE=off                  # fake 在生产被 API 拒绝;real 客户端未交付
XM_PLATFORM_USERS_MODE=off          # 默认 fake 会把样本客户摆进生产页面;real 未交付
# 生产要求显式模式;真实接入模式由 core.connector_config(后台"设置→凭据→接入模式")按环境决定,这里只是兜底
XM_SUB2API_MODE=fake
XM_SUB2API_INSTANCE_ID=sub2api-prod
XM_NEWAPI_MODE=fake
XM_NEWAPI_INSTANCE_ID=newapi-prod
XM_FINANCE_COLLECT_MODE=fake
XM_FINANCE_COLLECT_ENABLED=false    # fake 成本观测在生产没有演示标记,禁止产出;REAL0-b/c 交付后改 real
XM_FINANCE_COLLECT_INSTANCE_ID=finance-prod
BUILD_VERSION=production
```

数据:**保留**原数据库卷 `xingmang-launch_postgres18-data`(不再新建卷)。理由:凭据引用、接入模式、指标、
告警、成本登记簿都带 `environment` 列,staging 的演示行在 production 视图里不可见;`core.staff_account`
不分环境,管理员账号与已改的密码原样保留;审计链不断。代价:后台"设置→凭据"里的凭据登记与接入模式是
按环境存的,切到 production 后要**重新粘贴一次 token 并把两张接入模式卡切回 real**(文件卷 `xm-secrets`
里的明文没丢,重新粘贴只是覆盖同一个文件并补上 production 的登记行)。

部署(每次生产部署都要带覆盖文件且必须是绝对路径,否则会回到 launch.yaml 的 staging 默认):
```
cd /srv/deploy/xingmang-platform && nice -n 10 bash deploy/scripts/deploy-local.sh --override-file /srv/deploy/xingmang-platform/deploy/compose/server-prod.yaml
```
演示登记服务 `bootstrap` 在 prod 覆盖下属于 staging profile,脚本打印 `bootstrap=skipped reason=service-not-in-profile`;
`runway-threshold-bootstrap` 照常跑,会给 production 环境登记默认可用天数阈值。

验证:`/healthz` 里 `environment=production`、构建号 = 合入 sha;未登录 `GET /api/v1/auth/me` → 403;
`/app-config.js` 里 `authMode:"local"`;worker 日志 `connector_config_applied … environment=production`;
页面无"演示数据"横幅、无"开发模式"标签;"请求详情/用户清单"显示未接入(端点未挂载)。

回退:`cp -p .env.bak-staging-<时间> .env`,`deploy-local.sh`(不带覆盖文件)。
