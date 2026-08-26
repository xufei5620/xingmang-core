# 平台三个 Go 二进制的统一构建（platform-api / platform-worker / migrate）。
#
# 为什么放在一个文件里：三者共享同一份 go.mod 与几乎全部 internal/ 代码。
# 拆成三个 Dockerfile 会让 BuildKit 各自维护一份模块缓存和编译缓存，
# 首次构建时间 ×3；合成一个 builder 阶段 + 三个瘦运行阶段，
# compose 的三个服务只是选不同的 target，重的那层被复用。
#
# 版本纪律（宪法 §4 / VERSIONS.lock）：镜像必须钉版本，禁止 latest。
# golang:1.27-alpine 内是 go1.27.0，与 VERSIONS.lock 的 toolchain.go 一致。

# ---------- 构建阶段 ----------
FROM golang:1.27-alpine AS builder
WORKDIR /src

# 先只拷依赖清单：源码改动不会让下载层失效
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0：静态链接，运行阶段的 alpine 不需要 libc 兼容层。
# -trimpath：产物里不留构建机的绝对路径。
# -buildvcs=false：.git 不在构建上下文里（见 .dockerignore），版本信息改由
# BUILD_VERSION/BUILD_COMMIT 显式注入——构建产物的版本来源必须是发布流程
# 明确给的，而不是「碰巧构建上下文里有个 .git」（宪法 21 条：可追溯）。
ARG BUILD_VERSION=dev
ARG BUILD_COMMIT=unknown
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X github.com/xufei5620/xingmang-platform/internal/platform/buildinfo.Version=${BUILD_VERSION} -X github.com/xufei5620/xingmang-platform/internal/platform/buildinfo.Commit=${BUILD_COMMIT}" \
      -o /out/ ./cmd/platform-api ./cmd/platform-worker ./cmd/migrate

# ---------- 运行阶段的共同底座 ----------
# alpine 而不是 scratch：出问题时要能 exec 进去 wget/psql 一下。
# 生产收紧到 distroless 时另开 ADR。
FROM alpine:3.22 AS runtime-base
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 -h /app xingmang
WORKDIR /app
USER 10001:10001
ENV TZ=UTC

# ---------- platform-api ----------
FROM runtime-base AS platform-api
COPY --from=builder /out/platform-api /usr/local/bin/platform-api
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
COPY --chown=10001:10001 db/migrations /app/db/migrations
# --chmod 而不是 RUN chmod：Windows 检出的文件没有可执行位，
# 靠 git 保留 mode 在这条链路上不可靠
COPY --chmod=0755 deploy/docker/migrate-entrypoint.sh /usr/local/bin/xm-migrate-all
ENTRYPOINT ["/usr/local/bin/xm-migrate-all"]
