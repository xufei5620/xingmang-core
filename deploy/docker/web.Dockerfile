# admin-web 的构建与静态托管。
#
# 取舍：在容器里 pnpm 构建，而不是「宿主先 build 再挂载 dist」。
# 理由有三个，按重要性排：
#   1. dist/ 在 .gitignore 里。挂载方案要求每台机器先跑一遍宿主构建，
#      「一键启动」就不成立了，服务器上还得先装 Node 24 + pnpm 11。
#   2. 宿主是 Windows。本仓库的 .npmrc 里那行
#      `package-import-method=copy` 就是为绕开 Defender 扫描 esbuild.exe
#      导致的 EPERM 才加的——把构建放进 Linux 容器，这类宿主特有的坑一次性消失。
#   3. pnpm-lock.yaml + --frozen-lockfile 让构建可复现；宿主 node_modules
#      的实际状态谁也说不准。
# 代价是首次构建慢（要装整个 workspace 的依赖）。可接受：BuildKit 会缓存
# 依赖层，只要 lockfile 不变就不重装。
#
# 版本纪律：node 24 与 VERSIONS.lock 的 node=24.13.0 同线；pnpm 版本由
# 根 package.json 的 packageManager 字段钉死，corepack 照它装。

FROM node:24-alpine AS builder
RUN corepack enable
WORKDIR /src

# 先拷 workspace 骨架 + lockfile：源码改动不会让 pnpm install 层失效。
# pnpm 需要每个 workspace 包的 package.json 才能解析 workspace:* 依赖。
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml .npmrc ./
COPY web/apps/admin-web/package.json      web/apps/admin-web/package.json
COPY web/apps/ui-storybook/package.json   web/apps/ui-storybook/package.json
COPY web/packages/design-tokens/package.json web/packages/design-tokens/package.json
COPY web/packages/ui-admin/package.json      web/packages/ui-admin/package.json
COPY web/packages/ui-primitives/package.json web/packages/ui-primitives/package.json

# --frozen-lockfile：lockfile 与 package.json 不一致就失败，
# 而不是悄悄解析出一组和 CI 不同的版本。
RUN pnpm install --frozen-lockfile

COPY tsconfig.base.json ./
COPY web ./web
RUN pnpm --filter admin-web run build

# ---------- 运行阶段 ----------
FROM nginx:1.28-alpine AS web
COPY deploy/nginx/launch.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /src/web/apps/admin-web/dist /usr/share/nginx/html
EXPOSE 80
