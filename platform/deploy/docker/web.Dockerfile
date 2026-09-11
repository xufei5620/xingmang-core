# Monorepo root context preserves the ../invoice/web workspace dependency.
FROM node:24.13.0-alpine@sha256:cd6fb7efa6490f039f3471a189214d5f548c11df1ff9e5b181aa49e22c14383e AS builder
RUN corepack enable
WORKDIR /src/platform
COPY platform/ /src/platform/
COPY invoice/web/ /src/invoice/web/
RUN pnpm install --config.verify-deps-before-run=false --frozen-lockfile \
 && pnpm --filter admin-web run build \
 && pnpm --filter solov-invoice-web run build

FROM nginx:1.28-alpine AS web
COPY deploy/unified/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /src/platform/web/apps/admin-web/dist /usr/share/nginx/html
COPY --from=builder /src/invoice/web/dist /usr/share/nginx/invoice
COPY --chmod=0755 platform/deploy/docker/web-app-config.sh /docker-entrypoint.d/40-xm-app-config.sh
RUN mkdir -p /etc/nginx/runtime
EXPOSE 80 8081
