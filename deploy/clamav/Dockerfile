ARG CLAMAV_BASE_IMAGE=clamav/clamav:1.4.5@sha256:4de20bd9ab45a4b763c5412b769217ef5082572ebc8a63aff1a77943419e5dd8
FROM ${CLAMAV_BASE_IMAGE}

RUN apk add --no-cache libcrypto3=3.5.8-r0 libssl3=3.5.8-r0 \
    && clamd --version | grep -F 'ClamAV 1.4.5'
