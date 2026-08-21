ARG KEYCLOAK_BASE_IMAGE=quay.io/keycloak/keycloak:26.7.2@sha256:6efbadc00f0ed0237610becf11f4101b9c3ad8edf08a5b70c97aa4154ed436ec
FROM ${KEYCLOAK_BASE_IMAGE} AS builder

ENV KC_DB=postgres \
    KC_HEALTH_ENABLED=true \
    KC_METRICS_ENABLED=true

RUN /opt/keycloak/bin/kc.sh build \
    # The server never invokes the standalone admin CLI. Removing it also
    # prevents client-only libraries from entering the production SBOM. \
    && rm -rf /opt/keycloak/bin/client \
    # KC_DB=postgres makes the SQL Server JDBC driver unreachable. Keep the
    # Quarkus extension metadata intact, but remove the unused vendor driver. \
    && rm -f /opt/keycloak/lib/lib/main/com.microsoft.sqlserver.mssql-jdbc-*.jar \
    && test ! -e /opt/keycloak/bin/client \
    && ! find /opt/keycloak -type f -name 'com.microsoft.sqlserver.mssql-jdbc-*.jar' -print -quit | grep -q .

FROM ${KEYCLOAK_BASE_IMAGE}
COPY --from=builder /opt/keycloak/ /opt/keycloak/
# COPY overlays the original base filesystem and does not delete files removed
# in the builder. Repeat the least-privilege pruning in the final stage.
RUN rm -rf /opt/keycloak/bin/client \
    && rm -f /opt/keycloak/lib/lib/main/com.microsoft.sqlserver.mssql-jdbc-*.jar \
    && test ! -e /opt/keycloak/bin/client \
    && ! find /opt/keycloak -type f -name 'com.microsoft.sqlserver.mssql-jdbc-*.jar' -print -quit | grep -q .
COPY --chmod=755 entrypoint.sh /opt/keycloak/bin/invoice-entrypoint.sh
COPY --chmod=755 validate-proxy-trust.sh /opt/keycloak/bin/invoice-validate-proxy-trust.sh

ENV KC_DB=postgres \
    KC_HEALTH_ENABLED=true \
    KC_METRICS_ENABLED=true

ENTRYPOINT ["/opt/keycloak/bin/invoice-entrypoint.sh"]
