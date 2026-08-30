# 服务器快速上线(不依赖 GitHub;本机 → 服务器 直推)

适用:本机网络/GitHub 不稳定,先把当前 release 部署到 fiberstate 作为线上 staging,之后每次
验收合入后由本机直接推到服务器裸仓库再部署。生产晋级仍走 `deploy.sh prod`/`promote.sh`。

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
cp deploy/compose/.env.example deploy/compose/.env
sed -i "s/^DATABASE_PASSWORD=.*/DATABASE_PASSWORD=$(openssl rand -hex 24)/" deploy/compose/.env
sed -i "s/^XM_FINANCE_FAKE_SEED=.*/XM_FINANCE_FAKE_SEED=true/; s/^XM_REQLOG_MODE=.*/XM_REQLOG_MODE=fake/" deploy/compose/.env
chmod 600 deploy/compose/.env
# WEB_BIND 默认 127.0.0.1:8088,先用 SSH 隧道访问;要对外再走 nginx + Basic Auth 或 Keycloak

# 5) 隔离自检:8088 必须空闲;本栈只用 127.0.0.1:8088 一个宿主端口,自有 compose 网络/卷,不碰宿主 nginx/宝塔/其他项目
ss -ltnp | grep -q ':8088 ' && echo "8088 已被占用,改 .env 的 WEB_PORT" || echo "8088 空闲"

# 6) 首次部署(构建镜像 + 迁移 + 阈值 bootstrap + 演示种子 + 健康探针);nice 让构建给线上项目让路
nice -n 10 deploy/scripts/deploy-local.sh --no-fetch
```

访问:本机 `ssh -p 5620 -N -L 8088:127.0.0.1:8088 root@fiberstate`,浏览器打开 http://127.0.0.1:8088 。

## 每次更新(本机 → 服务器)

```bash
# 本机(验收线合入后):
git push ssh://root@fiberstate:5620/srv/git/xingmang-platform.git release/v0.1-launch
# 服务器:
cd /srv/deploy/xingmang-platform && nice -n 10 deploy/scripts/deploy-local.sh
```

`deploy-local.sh` 会从 origin(服务器裸仓库)刷新 release、要求工作树干净、只重建有变化的服务,
迁移与 bootstrap 幂等;失败即停、不自动回滚、容器与数据卷保留。

## 之后再升级为 DEPLOY0 全流程(可选)

`deploy/scripts/install-git-server.sh --repo /srv/git/xingmang-platform.git --ci-dir /srv/ci --confirm`
会装 receive hooks 并在服务器跑 `scripts/ci-local.sh`(需要服务器有 Go/pnpm/gitleaks 或受控 CI 容器),
届时 push 即触发门禁与 `deploy.sh staging`,生产用 `deploy.sh prod --confirm DEPLOY-PRODUCTION`。

## 与既有项目的隔离(fiberstate 共用时)

- 只占一个宿主端口 `127.0.0.1:8088`(可改 `WEB_PORT`);PostgreSQL 在容器内不映射宿主端口,不与宿主 PG 冲突。
- Compose 项目名 `xingmang-launch`,独立网络与数据卷;不改 nginx/宝塔/UFW,不开公网端口。
- 构建阶段用 `nice -n 10`;服务器 80 vCPU/125 GiB,常驻占用约 1–2 GiB 内存、几十 GB 磁盘。
- 回退/下线:`docker compose -p xingmang-launch -f deploy/compose/launch.yaml --env-file deploy/compose/.env down`(不带 -v 即保留数据)。

## 安全边界

- 凭据只在服务器 `.env`(0600);仓库与 bundle 不含任何凭据。
- staging 使用 dev-header 身份,**不要把 8088 直接暴露公网**;对外前先加 nginx Basic Auth 或切 Keycloak。
- 服务器 root 操作由负责人执行;AI 只提供脚本与命令。
