# Runbook：服务器为中心的发布与部署（XM-C-DEPLOY0-b）

本 runbook 描述服务器上的 staging 发布、production 晋级与部署脚本边界。
它只覆盖脚本和 Compose 编排；真实服务器安装、密钥配置、TLS 证书、OIDC
Realm、备份恢复演练仍需产品负责人按对应变更单执行。

## 1. 发布链

```
ai/codex/XM-* → release/v0.1-launch
                       │ post-receive CI
                       └── /srv/ci/<sha>.status = green
                                  │
                                  ├── deploy.sh staging
                                  └── promote.sh --confirm ... → main → deploy.sh prod
```

`git push` 返回成功不代表门禁通过；部署脚本始终读取目标提交对应的
`<40 位 SHA>.status`，只有严格单行 `green` 才会继续。`pending`、`red`、缺失、
多行或其它内容一律停止。

## 2. staging

在服务器的受控部署 checkout 中运行：

```bash
deploy/scripts/deploy.sh staging --reason "shared acceptance"
```

脚本固定采纳 `refs/heads/release/v0.1-launch`，使用：

- `deploy/compose/launch.yaml`
- `deploy/compose/server-staging.yaml`
- Compose 项目名 `xingmang-staging`
- web 固定回环发布口 `127.0.0.1:18088`

staging 覆盖通过 `staging` profile 启用 `001_staging_seed.sql`。种子数据只用于
联调，页面必须显示 Fake/演示来源；不能把这个 profile 用于 production。
外部访问由宿主 Nginx 终止 TLS 并加 Basic Auth，模板见
`deploy/nginx/server-staging.conf.example`。`.htpasswd`、证书和私钥必须在仓库
外部由服务器密钥管理提供。

## 3. production 晋级与部署

生产晋级分两步，必须由产品负责人在服务器上执行。以下为 DEPLOY0 流程，
其探针和对应 Nginx 模板固定使用 `127.0.0.1:18089`。操作前须由负责人确认
该流程的获批 env 已显式配置 `WEB_PORT=18089`；不能直接套用 example 的 8088。
`deploy-local.sh` 所用的 8088 与 Compose 现有默认保持不变，本节不授权迁移
生产端口或修改 Nginx。

命令从已获批并完成 monorepo 真相源切换的 checkout 的 `platform/` 目录执行。
需要已有 `jq`；先只读解析同一组 base/override/env 的实际 Compose 模型，仅
验证 web 的端口映射，不打印模型或 env 内容。解析失败或端口不匹配立即停止，
不会进入晋级/部署；由负责人排查配置，不能删除此检查继续执行：

```bash
(
  set -euo pipefail
  docker compose -p xingmang-prod -f deploy/compose/launch.yaml \
    -f deploy/compose/server-prod.yaml --env-file deploy/compose/.env \
    config --format json |
    jq -e '.services.web.ports as $ports |
      ($ports | length) == 1 and
      $ports[0].host_ip == "127.0.0.1" and
      ($ports[0].published | tostring) == "18089" and
      $ports[0].target == 80' >/dev/null || {
        echo "DEPLOY0 port preflight failed; require approved 127.0.0.1:18089 -> web:80" >&2
        exit 1
      }
  deploy/scripts/promote.sh --confirm PROMOTE-PRODUCTION \
    --reason "release approval XM-C-DEPLOY0-b"
  deploy/scripts/deploy.sh prod --confirm DEPLOY-PRODUCTION \
    --reason "production rollout XM-C-DEPLOY0-b"
)
```

两个脚本都要求显式确认和非空原因；非交互环境缺少确认会直接拒绝。晋级脚本
只允许把已经 `green` 的 release 提交推到 `refs/heads/main`，通过裸仓库
`pre-receive` 的 marker 闸门，不调用 `git update-ref`，不接受强推或删除。

production Compose 叠加 `server-prod.yaml`，项目名为 `xingmang-prod`，并且：

- API 强制 `ENVIRONMENT=production`；按 CR-0004 当前默认
  `XM_AUTH_MODE=local` / `XM_WEB_AUTH_MODE=local`，两侧必须成对。仍可显式
  同时切回 `oidc`，此时 issuer/audience 缺一不可；`dev-header` 永远拒绝；
- Sub2API、NewAPI、财务采集必须显式选择 `real`，或显式关闭相应同步；不能
  继承 fake 默认值；
- `XM_FINANCE_FAKE_SEED=false`；staging bootstrap profile 不启用，绝不写入
  `001_staging_seed.sql`；
- web 只绑定回环 `127.0.0.1:18089`，外层 Nginx 负责 TLS，模板见
  `deploy/nginx/server-prod.conf.example`；Authorization Bearer 原样转发，
  不在 Nginx 写入固定 token。

生产脚本会清理 `BASH_ENV`、动态链接器、Git、Docker、Compose 控制变量，并以
`/bin/bash -p` 和固定系统 `PATH` 启动；不要通过 shell profile、全局 Git 配置或
Docker context 改写发布目标。Compose `.env` 也禁止出现 `COMPOSE_*`、`DOCKER_*`
或 `GIT_*` 控制键。

安装器登记的 `xm.receive.requirePromoteLock=true` 会让裸仓库 hook 默认只接受
仍在运行的 `promote.sh` 授权锁（PID + 目标 SHA）；配置缺失或改成 false 不会
退回 marker-only。旧服务器需重新运行安装器完成这项迁移。

生产部署失败时只写失败审计并停止，不自动 `down -v`、不伪造 green、也不自动
回滚到某个猜测的旧版本。回滚必须指定已批准的确切 SHA/制品并重新走人工确认。

## 4. 审计与路径

脚本将每次进入 fetch 后的部署/晋级尝试以单行 `key=value` 追加到受控审计文件，至少包含 UTC 时间、
操作者、环境、ref、完整 SHA、Compose 项目、结果和经过校验的 reason；不会写入
密码、token、DSN、环境文件内容或完整构建日志。审计文件只允许绝对路径、拒绝
符号链接，权限为 0600，并以锁和原子临时文件防止并发交错。

默认服务器目录由安装器约定：裸仓库 `/srv/git/xingmang-platform.git`、CI
状态 `/srv/ci`、部署 checkout `/srv/deploy/xingmang-platform`、审计
`/srv/audit/xingmang-deploy.log`。本地测试使用临时目录和 fake Docker/curl，
不会连接真实服务器。

脚本的正常失败会释放环境锁；如果进程被 SIGKILL/主机掉电中断，锁会保留以
fail-closed。人工核对日志和进程后，可删除对应的 `.deploy-<env>-<sha>.lock`
或 `.promote.lock`，再重试；禁止在未核对时批量清理锁。

## 5. 尚未包含的边界

- 真实服务器 root 安装与 SSH 推拉；
- CI 的临时 PostgreSQL/缓存容器（D0-a 已明确需另行补齐的受控编排）；
- Keycloak/OIDC Realm 配置、TLS/Basic Auth 密钥与外部看门狗；
- Telegram 的具体凭据解析与 Bot 配置。脚本可通过 `--notify-hook` 调用已登记的
  受控通知适配器，只把 environment/ref/sha/result 这组脱敏 key=value 通过 stdin
  交给它；适配器应自行经 CredentialRef 解析 Telegram/Webhook 凭据，不能把 token
  放进环境文件、审计或命令行。未配置适配器时只写审计，不影响部署结果。
