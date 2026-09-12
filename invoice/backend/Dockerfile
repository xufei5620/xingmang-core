FROM golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build

WORKDIR /src/invoice/backend
COPY invoice/backend/go.mod invoice/backend/go.sum ./
RUN go mod download
COPY invoice/backend/ ./
RUN go test -mod=readonly ./internal/document ./internal/pdfscanner ./internal/backuparchive
RUN CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-migrate ./cmd/migrate \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-rehearsal-auth-provider ./cmd/rehearsal-auth-provider \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-identity-migrate ./cmd/identity-migrate \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-bootstrap-settings ./cmd/bootstrap-settings \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-bootstrap-sources ./cmd/bootstrap-sources \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-keygen ./cmd/keygen \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-mtlsgen ./cmd/mtlsgen \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-backup-verify ./cmd/backup-verify \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-document-gc ./cmd/document-gc \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-pdf-policy-check ./cmd/pdf-policy-check \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-pdf-scanner ./cmd/pdf-scanner \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-archive-verify ./cmd/archive-verify \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-eligibility-repair ./cmd/eligibility-repair \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-eligibility-shadow ./cmd/eligibility-shadow \
    && CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/invoice-account-bind ./cmd/account-bind

# The offline D seed tool uses the platform's existing Argon2 dependency.
# Its opt-in build tag keeps that dependency out of the invoice module.
FROM build AS rehearsal-build
ARG GOPROXY=https://goproxy.cn,direct
WORKDIR /src/platform
COPY platform/go.mod platform/go.sum ./
RUN GOWORK=off go test -tags rehearsal_tools -mod=readonly -count=1 invoice-system/backend/cmd/rehearsal-seed
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -tags rehearsal_tools -mod=readonly -trimpath -ldflags="-s -w" -o /out/rehearsal-seed invoice-system/backend/cmd/rehearsal-seed

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
COPY --from=build /out/invoice-migrate /out/invoice-rehearsal-auth-provider /out/invoice-identity-migrate /out/invoice-bootstrap-settings /out/invoice-bootstrap-sources /out/invoice-keygen /out/invoice-mtlsgen /out/invoice-backup-verify /out/invoice-document-gc /out/invoice-pdf-policy-check /out/invoice-archive-verify /out/invoice-eligibility-repair /out/invoice-eligibility-shadow /out/invoice-account-bind /usr/local/bin/
COPY --from=rehearsal-build /out/rehearsal-seed /usr/local/bin/rehearsal-seed
COPY invoice/backend/migrations /app/migrations

ENV MIGRATIONS_DIR=/app/migrations
USER 10001:10001

# qpdf and all active-content parsing live only in this no-network sidecar.
FROM scanner-base AS scanner
COPY --from=build /out/invoice-pdf-scanner /usr/local/bin/invoice-pdf-scanner
COPY --from=pdf-policy-gate /qpdf-policy-gate /app/qpdf-policy-gate
USER 10002:10000
ENTRYPOINT ["/usr/local/bin/invoice-pdf-scanner"]

# Keep the scanner final; the unified API is built by platform/deploy/docker/go.Dockerfile.
