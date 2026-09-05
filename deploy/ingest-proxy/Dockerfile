ARG NGINX_BASE_IMAGE=nginx:1.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46
FROM ${NGINX_BASE_IMAGE}

RUN apk add --no-cache libcrypto3=3.5.8-r0 libssl3=3.5.8-r0 libuuid=2.42.3-r1 libexpat=2.8.4-r0 \
    && nginx -v 2>&1 | grep -Fx 'nginx version: nginx/1.30.4'
