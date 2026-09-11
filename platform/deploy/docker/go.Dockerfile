# 平台 Go 二进制的统一构建（platform-api / platform-worker / migrate /
# runway-threshold-bootstrap / staff-bootstrap）。
#
# 为什么放在一个文件里：三者共享同一份 go.mod 与几乎全部 internal/ 代码。
# 拆成三个 Dockerfile 会让 BuildKit 各自维护一份模块缓存和编译缓存，
# 首次构建时间 ×3；合成一个 builder 阶段 + 三个瘦运行阶段，
# compose 的三个服务只是选不同的 target，重的那层被复用。
#
# 版本纪律（宪法 §4 / VERSIONS.lock）：镜像必须钉版本，禁止 latest。
# 精确 patch 与 digest 同时锁定，避免同名浮动标签偏离 VERSIONS.lock。

# ---------- 构建阶段 ----------
FROM golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS builder
WORKDIR /src/platform

# 先只拷依赖清单：源码改动不会让下载层失效
COPY platform/go.mod platform/go.sum ./
COPY invoice/backend/go.mod invoice/backend/go.sum /src/invoice/backend/
RUN go mod download

COPY platform/ /src/platform/
COPY invoice/backend/ /src/invoice/backend/

# CGO_ENABLED=0：静态链接，运行阶段的 alpine 不需要 libc 兼容层。
# -trimpath：产物里不留构建机的绝对路径。
# -buildvcs=false：.git 不在构建上下文里（见 .dockerignore），版本信息改由
# BUILD_VERSION/BUILD_COMMIT 显式注入——构建产物的版本来源必须是发布流程
# 明确给的，而不是「碰巧构建上下文里有个 .git」（宪法 21 条：可追溯）。
ARG BUILD_VERSION=dev
ARG BUILD_COMMIT=unknown
ENV CGO_ENABLED=0 GOOS=linux GOWORK=off
RUN go build -mod=readonly -trimpath -buildvcs=false \
      -ldflags "-s -w -X github.com/xufei5620/xingmang-platform/internal/platform/buildinfo.Version=${BUILD_VERSION} -X github.com/xufei5620/xingmang-platform/internal/platform/buildinfo.Commit=${BUILD_COMMIT}" \
      -o /out/ ./cmd/platform-api ./cmd/platform-worker ./cmd/migrate ./cmd/runway-threshold-bootstrap ./cmd/staff-bootstrap ./cmd/cpa-snapshot

# ---------- 运行阶段的共同底座 ----------
# alpine 而不是 scratch：出问题时要能 exec 进去 wget/psql 一下。
# 生产收紧到 distroless 时另开 ADR。
FROM alpine:3.22 AS runtime-base
# /run/xm/secrets 是凭据文件目录的挂载点（XM-CRED0）。先在镜像里建好并交给
# 运行用户：命名卷首次创建时会继承镜像里这个目录的属主，否则 root:root 的
# 挂载点会让以 10001 运行的 platform-api 一个文件都写不进去。
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 10000 scanner-share \
 && adduser -D -u 10001 -h /app xingmang \
 && mkdir -p /run/xm/secrets /data/documents /scanner \
 && chown 10001:10001 /run/xm/secrets /data/documents \
 && chown 10002:10000 /scanner && chmod 0750 /scanner \
 && chmod 0700 /run/xm/secrets
WORKDIR /app
USER 10001:10001
ENV TZ=UTC

# ---------- platform-api ----------
FROM runtime-base AS platform-api
COPY --from=builder /out/platform-api /usr/local/bin/platform-api
COPY invoice/backend/migrations /app/migrations
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/platform-api"]

# ---------- platform-worker ----------
FROM runtime-base AS platform-worker
COPY --from=builder /out/platform-worker /usr/local/bin/platform-worker
ENTRYPOINT ["/usr/local/bin/platform-worker"]

# ---------- migrate（Platform Lifecycle Operation）----------
# 同时带上 platform-worker：River 的迁移入口是 `platform-worker -migrate`
# （internal/platform/jobs.Migrate 用 River 内嵌的 pinned bundle，
# 不是 db/migrations 下那份可审阅镜像）。两步迁移在同一个容器里跑完，
# 避免「业务迁移过了、River 没过」这种半截状态。
FROM runtime-base AS migrate
COPY --from=builder /out/migrate /usr/local/bin/migrate
COPY --from=builder /out/platform-worker /usr/local/bin/platform-worker
COPY --from=builder /out/runway-threshold-bootstrap /usr/local/bin/runway-threshold-bootstrap
# staff-bootstrap（XM-LOGIN）与 runway-threshold-bootstrap 同一条纪律：
# Platform Lifecycle Operation，只在 tools profile 里显式运行一次，不参与
# 常规 `up`，所以放进同一个 migrate 镜像而不是单开一个运行阶段。
COPY --from=builder /out/staff-bootstrap /usr/local/bin/staff-bootstrap
# cpa-snapshot 是宿主机 Platform Lifecycle Operation。部署脚本只从已完成
# migrate 的精确镜像提取这一个静态二进制；服务器不安装 Go/sqlite 工具链。
COPY --from=builder /out/cpa-snapshot /usr/local/bin/cpa-snapshot
COPY --chown=10001:10001 platform/db/migrations /app/db/migrations
# --chmod 而不是 RUN chmod：Windows 检出的文件没有可执行位，
# 靠 git 保留 mode 在这条链路上不可靠
COPY --chmod=0755 platform/deploy/docker/migrate-entrypoint.sh /usr/local/bin/xm-migrate-all
ENTRYPOINT ["/usr/local/bin/xm-migrate-all"]
