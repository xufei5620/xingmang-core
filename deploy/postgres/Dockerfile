ARG GO_BUILDER_IMAGE=golang:1.25.13-alpine@sha256:1e0126852075c9c60731c8ba49088448b91f63e2aed97ca9d1a9791622a05946
ARG POSTGRES_BASE_IMAGE=postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2
FROM ${GO_BUILDER_IMAGE} AS gosu-build

ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY gosu-build/go.mod gosu-build/go.sum ./
RUN test "$TARGETOS" = "linux" \
    && test "$TARGETARCH" = "amd64" \
    && go mod download \
    && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -mod=readonly -trimpath -buildvcs=false -ldflags="-s -w" -o /out/gosu github.com/tianon/gosu \
    && /out/gosu --version | grep -Fx '1.19 (go1.25.13 on linux/amd64; gc)'

FROM ${POSTGRES_BASE_IMAGE}

COPY --from=gosu-build /out/gosu /usr/local/bin/gosu
RUN apk add --no-cache libcrypto3=3.5.8-r0 libssl3=3.5.8-r0 \
    && gosu --version | grep -Fx '1.19 (go1.25.13 on linux/amd64; gc)'
