FROM registry.access.redhat.com/ubi9/openjdk-21@sha256:cc8a30e9181b0135e6657ca3b824d7b32e4c7f6a664769ef641d4f7031339564 AS source-build

USER root
WORKDIR /work
RUN microdnf install -y git-2.52.0-1.el9.x86_64 gzip-1.12-2.el9_8.x86_64 libicu-67.1-10.el9_6.x86_64 \
    && rpm -q libicu-67.1-10.el9_6.x86_64 \
    && microdnf clean all
ADD --checksum=sha256:5ab3b50c02ecb70cb2a8aece74617da2e74d753176b50886812abbc316300096 https://codeload.github.com/keycloak/keycloak/tar.gz/289376b142480b4d600aca7acb1e4651862ed2a1 /downloads/keycloak-source.tar.gz
ADD --checksum=sha256:80ffca22aed9e8b9713a232f3394fd81d7f20322df75efdb2b047dbd3e3a23bb https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.9.16/apache-maven-3.9.16-bin.tar.gz /downloads/maven.tar.gz
ADD --checksum=sha256:50db8f9559f674a5d9c66f27f3154145928b570c45b4ca119338d33cf0f4ed04 https://repo.maven.apache.org/maven2/io/netty/netty-bom/4.1.137.Final/netty-bom-4.1.137.Final.pom /downloads/netty-bom.pom
COPY source-build/ /source-build/
ENV PATH="/opt/maven/bin:${PATH}"

RUN mkdir -p /opt/maven /work/keycloak /audit /rebuilt/keycloak \
    && tar -xzf /downloads/maven.tar.gz --strip-components=1 -C /opt/maven \
    && tar -xzf /downloads/keycloak-source.tar.gz --strip-components=1 -C /work/keycloak \
    && java /source-build/SourceBuild.java self-test \
    && java /source-build/SourceBuild.java prepare /source-build/inputs.properties /downloads /work/keycloak /audit \
    && java -version > /audit/toolchain.txt 2>&1 \
    && mvn --version >> /audit/toolchain.txt 2>&1 \
    && rpm -qa --qf '%{NAME}-%{VERSION}-%{RELEASE}.%{ARCH}\n' | sort > /audit/builder-rpms.txt \
    && mvn --version | grep -F 'Apache Maven 3.9.16'

WORKDIR /work/keycloak
# The upstream server-only reactor builds the complete distribution, including
# the locked JS/theme build. No distribution profile or JS skip is applied.
RUN --mount=type=cache,id=xm-keycloak-source-289376b1-maven3916-20260910,target=/root/.m2,sharing=locked \
    --mount=type=cache,id=xm-keycloak-js-289376b1-pnpm11-20260910,target=/pnpm-store,sharing=locked \
    export pnpm_config_fetch_timeout=600000 pnpm_config_store_dir=/pnpm-store \
    && mvn -B -ntp -pl quarkus/deployment,quarkus/dist -am -DskipTests clean install \
    && mvn -B -ntp -N org.apache.maven.plugins:maven-help-plugin:3.5.1:effective-pom -Doutput=/audit/effective-root.xml \
    && mvn -B -ntp -f quarkus/server/pom.xml org.apache.maven.plugins:maven-help-plugin:3.5.1:effective-pom -Doutput=/audit/effective-server.xml \
    && mvn -B -ntp -f quarkus/server/pom.xml org.apache.maven.plugins:maven-dependency-plugin:3.8.1:tree '-Dincludes=io.netty:*' -Dverbose -DoutputFile=/audit/netty-dependency-tree.txt \
    && test -s quarkus/dist/target/keycloak-26.7.2.tar.gz \
    && tar -xzf quarkus/dist/target/keycloak-26.7.2.tar.gz --strip-components=1 -C /rebuilt/keycloak \
    && java /source-build/SourceBuild.java verify /source-build/inputs.properties /downloads/netty-bom.pom /audit/effective-root.xml /audit/effective-server.xml /rebuilt/keycloak quarkus/dist/target/keycloak-26.7.2.tar.gz /audit

FROM quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067 AS builder

# COPY overlays a base. Remove its entire old distribution before replacing it
# so none of the original Netty 4.1.136.Final files can survive the rebuild.
USER root
RUN rm -rf /opt/keycloak
COPY --from=source-build --chown=1000:0 /rebuilt/keycloak/ /opt/keycloak/
COPY --from=source-build --chown=1000:0 /audit/ /opt/keycloak/source-build-audit/
COPY --chmod=755 source-build/verify-runtime.sh /usr/local/bin/verify-keycloak-source-build
RUN mkdir -p /opt/keycloak/data && chmod -R g+rwX /opt/keycloak
USER 1000

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
    && ! find /opt/keycloak -type f -name 'com.microsoft.sqlserver.mssql-jdbc-*.jar' -print -quit | grep -q . \
    && /usr/local/bin/verify-keycloak-source-build

FROM quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067
USER root
RUN rm -rf /opt/keycloak
COPY --from=builder --chown=1000:0 /opt/keycloak/ /opt/keycloak/
COPY --chmod=755 source-build/verify-runtime.sh /usr/local/bin/verify-keycloak-source-build
USER 1000
RUN test ! -e /opt/keycloak/bin/client \
    && ! find /opt/keycloak -type f -name 'com.microsoft.sqlserver.mssql-jdbc-*.jar' -print -quit | grep -q . \
    && /usr/local/bin/verify-keycloak-source-build
COPY --chmod=755 entrypoint.sh /opt/keycloak/bin/invoice-entrypoint.sh
COPY --chmod=755 validate-proxy-trust.sh /opt/keycloak/bin/invoice-validate-proxy-trust.sh

ENV KC_DB=postgres \
    KC_HEALTH_ENABLED=true \
    KC_METRICS_ENABLED=true

ENTRYPOINT ["/opt/keycloak/bin/invoice-entrypoint.sh"]
