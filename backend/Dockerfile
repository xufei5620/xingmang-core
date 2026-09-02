FROM golang:1.25.13-alpine@sha256:1e0126852075c9c60731c8ba49088448b91f63e2aed97ca9d1a9791622a05946 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test -mod=readonly ./internal/document ./internal/pdfscanner ./internal/backuparchive
RUN CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-api ./cmd/api \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-migrate ./cmd/migrate \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-bootstrap-settings ./cmd/bootstrap-settings \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-bootstrap-sources ./cmd/bootstrap-sources \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-keygen ./cmd/keygen \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-mtlsgen ./cmd/mtlsgen \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-backup-verify ./cmd/backup-verify \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-document-gc ./cmd/document-gc \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-oidc-logout-retention ./cmd/oidc-logout-retention \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-pdf-policy-check ./cmd/pdf-policy-check \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-pdf-scanner ./cmd/pdf-scanner \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-archive-verify ./cmd/archive-verify \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-oidc-preflight ./cmd/oidc-preflight \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-eligibility-repair ./cmd/eligibility-repair

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS api-base

RUN apk add --no-cache ca-certificates libcrypto3=3.5.8-r0 libssl3=3.5.8-r0 \
    && addgroup -S -g 10000 scanner-share \
    && addgroup -S -g 10001 invoice \
    && adduser -S -D -H -u 10001 -G invoice -s /sbin/nologin invoice \
    && mkdir -p /data/documents /app /scanner \
    && chown -R invoice:invoice /data/documents /app \
    && chown 10002:scanner-share /scanner \
    && chmod 0750 /scanner

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS scanner-base

RUN apk add --no-cache ca-certificates libcrypto3=3.5.8-r0 libssl3=3.5.8-r0 qpdf=12.3.2-r0 \
    && qpdf --version | grep -F 'qpdf version 12.3.2' \
	&& addgroup -S -g 10000 scanner-share \
	&& adduser -S -D -H -u 10002 -G scanner-share -s /sbin/nologin scanner \
	&& mkdir -p /scanner /scan-tmp /app \
	&& chown -R scanner:scanner-share /scanner /scan-tmp /app \
	&& chmod 0750 /scanner \
	&& chmod 0700 /scan-tmp

FROM scanner-base AS pdf-policy-gate
COPY --from=build /out/invoice-pdf-policy-check /usr/local/bin/invoice-pdf-policy-check
RUN /usr/local/bin/invoice-pdf-policy-check --self-test \
    && printf 'qpdf-policy-ok\n' >/qpdf-policy-gate

FROM api-base AS tools
RUN apk add --no-cache qpdf=12.3.2-r0 \
    && qpdf --version | grep -F 'qpdf version 12.3.2'
COPY --from=build /out/invoice-migrate /out/invoice-bootstrap-settings /out/invoice-bootstrap-sources /out/invoice-keygen /out/invoice-mtlsgen /out/invoice-backup-verify /out/invoice-document-gc /out/invoice-oidc-logout-retention /out/invoice-pdf-policy-check /out/invoice-archive-verify /out/invoice-eligibility-repair /out/invoice-oidc-preflight /usr/local/bin/
COPY migrations /app/migrations

ENV MIGRATIONS_DIR=/app/migrations
USER 10001:10001

# qpdf and all active-content parsing live only in this no-network sidecar.
FROM scanner-base AS scanner
COPY --from=build /out/invoice-pdf-scanner /usr/local/bin/invoice-pdf-scanner
COPY --from=pdf-policy-gate /qpdf-policy-gate /app/qpdf-policy-gate
USER 10002:10000
ENTRYPOINT ["/usr/local/bin/invoice-pdf-scanner"]

# Keep this final so an ordinary `docker build` produces the minimal API image.
FROM api-base AS api
COPY --from=build /out/invoice-api /usr/local/bin/invoice-api
COPY --from=pdf-policy-gate /qpdf-policy-gate /app/qpdf-policy-gate
COPY migrations /app/migrations

ENV MIGRATIONS_DIR=/app/migrations
USER 10001:10001
EXPOSE 8088
ENTRYPOINT ["/usr/local/bin/invoice-api"]
