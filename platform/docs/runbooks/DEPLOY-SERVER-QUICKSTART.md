# 服务器快速上线(不依赖 GitHub;本机 → 服务器 直推)

适用:本机网络/GitHub 不稳定,先把当前 release 部署到 fiberstate 作为线上 staging,之后每次
验收合入后由本机直接推到服务器裸仓库再部署。生产晋级仍走 `deploy.sh prod`/`promote.sh`。

当前命令按已获批准的 monorepo checkout 编写：Git 顶层下的项目目录为 `platform/`。
服务器 bare repo 的历史衔接、现有配置迁移和真实部署仍需负责人批准；本文命令不自动完成这些前置。

## 一次性(服务器,root)

```bash
# 1) 裸仓库(以后本机直接 push 到这里;deploy-local.sh 允许的 origin 之一)
mkdir -p /srv/git /srv/deploy
git init --bare /srv/git/xingmang-platform.git

# 2) 用本机传上来的 bundle 灌入首版(bundle 由验收线生成:xingmang-release.bundle)
git -C /srv/git/xingmang-platform.git fetch /root/xingmang-release.bundle release/v0.1-launch:release/v0.1-launch

# 3) 部署检出(目录名必须是 xingmang-platform,origin 必须是 /srv/git/xingmang-platform.git)
git clone -b release/v0.1-launch /srv/git/xingmang-platform.git /srv/deploy/xingmang-platform
cd /srv/deploy/xingmang-platform

# 4) 环境文件(不入库;至少改 DATABASE_PASSWORD,其余先用 fake 默认)
cp platform/deploy/compose/.env.example platform/deploy/compose/.env
sed -i "s/^DATABASE_PASSWORD=.*/DATABASE_PASSWORD=$(openssl rand -hex 24)/" platform/deploy/compose/.env
sed -i "s/^XM_FINANCE_FAKE_SEED=.*/XM_FINANCE_FAKE_SEED=true/; s/^XM_REQLOG_MODE=.*/XM_REQLOG_MODE=fake/" platform/deploy/compose/.env
chmod 600 platform/deploy/compose/.env
# WEB_BIND 默认 127.0.0.1:8088,先用 SSH 隧道访问;要对外再走 nginx + Basic Auth 或 Keycloak

# 5) 隔离自检:8088 必须空闲;本栈只用 127.0.0.1:8088 一个宿主端口,自有 compose 网络/卷,不碰宿主 nginx/宝塔/其他项目
ss -ltnp | grep -q ':8088 ' && echo "8088 已被占用,改 .env 的 WEB_PORT" || echo "8088 空闲"

# 6) 首次部署(构建镜像 + 迁移 + 阈值 bootstrap + 演示种子 + 健康探针);nice 让构建给线上项目让路
nice -n 10 platform/deploy/scripts/deploy-local.sh --no-fetch
```

访问:本机 `ssh -p 5620 -N -L 8088:127.0.0.1:8088 root@fiberstate`,浏览器打开 http://127.0.0.1:8088 。

## 每次更新(本机 → 服务器)

```bash
# 本机(验收线合入后):
git push ssh://root@fiberstate:5620/srv/git/xingmang-platform.git release/v0.1-launch
# 服务器:
cd /srv/deploy/xingmang-platform && nice -n 10 platform/deploy/scripts/deploy-local.sh
```

`deploy-local.sh` 会从 origin(服务器裸仓库)刷新 release、要求工作树干净、只重建有变化的服务,
迁移与 bootstrap 幂等;失败即停、不自动回滚、容器与数据卷保留。

自我更新安全性(XM-DEPLOY-SELFUPDATE0):脚本 fast-forward 自己所在的 checkout 时,
新拉取的提交也会改写 `deploy-local.sh` 自身。脚本内部把全部逻辑包在一个函数里、只在
文件最后一行调用,保证 bash 已经把整份脚本读完再开始执行,运行中途磁盘文件被改写(不管是
这次 self-update 自己触发的,还是操作员在另一个终端并发 `git pull`)都不会让当前这次运行
读到新旧混杂的字节。self-update 成功快进后,脚本会以同样的参数自动 `exec` 一次刚更新的
自身(内部用 `XM_DEPLOY_LOCAL_REEXEC=1` 保证只发生一次),确保真正跑起来的是新版本脚本,
不需要操作员按老办法"先手动 fast-forward、再运行"。

**退出码 3**:fetch 已经成功、但本地 checkout 有 upstream 没有的提交、无法自动
fast-forward(即真正分叉,不是单纯落后)时,脚本会打印精确的手动修复指令并以退出码 3
停止——不会尝试合并或强推。收到退出码 3 时,按提示手动执行
`git fetch origin release/v0.1-launch && git merge --ff-only FETCH_HEAD` 核实/解决分叉后
重新运行本脚本。fetch 本身失败(网络/镜像不可达)不算这个退出码,仍走既有的"有精确匹配
SHA 才放行"fail-closed 路径(退出码 1)。

**cpa-observations 等待预算(XM-DEPLOY-CPAWAIT0)**:production file 模式下,`deploy-local.sh`
在 API/worker 启动后会等待新部署对应的四条同一 generation 的成功 CPA 观测落库
(`cpa.requests.daily`/`cpa.cost.daily`/`cpa.keys.usage`/`cpa.accounts.health`)。等待预算 =
worker 自己的 cpa_sync 周期(读同一个 `XM_CPA_SYNC_INTERVAL`、同一解析规则和默认值,未设置
时用 worker 侧默认 300s)+60s 安全边际,不再是固定的~24s 窗口——worker 容器刚重建时,即使
周期任务会在启动时尝试同步一次,也没有一个可靠的亚分钟级上界能保证这次尝试已经落库完成,
唯一确定的上界是"最迟不超过下一个正常周期节拍"。曾经因为这个窗口太短,两次生产部署在
观测值已经正确产生之后才被误判失败。

每 10s 轮询一次,至多每 60s 打印一行进度(`cpa-observations=waiting elapsed=…s
expected_generation=…`)。预算耗尽仍未匹配时,失败行会把期望的状态元组和最后一次观测到的
状态元组一起打出来(`expected=... observed=...`),用来判断是旧 generation 还在、部分到齐
还是完全没有。

## 之后再升级为 DEPLOY0 全流程(可选)

`platform/deploy/scripts/install-git-server.sh --repo /srv/git/xingmang-platform.git --ci-dir /srv/ci --confirm`
会装 receive hooks 并在服务器跑 `platform/scripts/ci-local.sh`(需要服务器有 Go/pnpm/gitleaks 或受控 CI 容器),
届时 push 即触发门禁与 `deploy.sh staging`,生产用 `deploy.sh prod --confirm DEPLOY-PRODUCTION`。

## 与既有项目的隔离(fiberstate 共用时)

- 只占一个宿主端口 `127.0.0.1:8088`(可改 `WEB_PORT`);PostgreSQL 在容器内不映射宿主端口,不与宿主 PG 冲突。
- Compose 项目名 `xingmang-launch`,独立网络与数据卷;不改 nginx/宝塔/UFW,不开公网端口。
- 构建阶段用 `nice -n 10`;服务器 80 vCPU/125 GiB,常驻占用约 1–2 GiB 内存、几十 GB 磁盘。
- 回退/下线:`docker compose -p xingmang-launch -f platform/deploy/compose/launch.yaml --env-file platform/deploy/compose/.env down`(不带 -v 即保留数据)。

## 安全边界

- 凭据只在服务器 `.env`(0600);仓库与 bundle 不含任何凭据。
- staging 使用 dev-header 身份,**不要把 8088 直接暴露公网**;对外前先加 nginx Basic Auth 或切 Keycloak。
- 服务器 root 操作由负责人执行;AI 只提供脚本与命令。

## 域名访问(2026-08-30 已生效)

- `https://console.solov.cc` → Cloudflare(橙云)→ fiberstate nginx vhost
  `/www/server/panel/vhost/nginx/console.solov.cc.conf` → `127.0.0.1:8088`。
- 证书:acme.sh DNS-01(`~/.acme.sh/console.solov.cc_ecc`,`--install-cert` 到
  `/www/server/panel/vhost/cert/console.solov.cc/`,自动续期并 reload)。
- 访问控制:HTTP Basic Auth,用户 `xingmang`,口令文件 `/www/server/nginx/conf/auth/xingmang.htpasswd`
  (root:www 640;不要放在 `/www/server/panel/vhost/nginx/` 下,nginx worker 读不到会 500)。
  改口令:`printf "xingmang:%s
" "$(openssl passwd -apr1 '<新口令>')" > /www/server/nginx/conf/auth/xingmang.htpasswd`。
- 这是临时门禁;切 Keycloak(XM_AUTH_MODE=oidc)后删除 `auth_basic` 两行即可。
