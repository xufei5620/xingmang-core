# Keycloak 26.7.2 source rebuild

This image rebuilds the complete official Keycloak distribution with Netty BOM
`4.1.137.Final` imported first in the root POM. The BOM retains its independently
versioned `netty-tcnative*` family at `2.0.81.Final`. It does not replace individual
JARs in a previously augmented distribution or change vulnerability exemptions.

`inputs.properties` records the reviewed source commit, archive hashes, builder
and runtime image digests, Maven version, and BOM identity. The three downloads
also have literal Docker `ADD --checksum=sha256:` pins. The Java helper verifies
the downloaded bytes against the lock before it patches the source POM.

The source stage also installs the exact UBI prerequisite
`libicu-67.1-10.el9_6.x86_64` and asserts it with `rpm -q` before Maven runs.
The admin-client generator invokes native Kiota v1.32.4, whose .NET runtime
requires ICU. Without it Kiota fails fast, but the npm wrapper can still report
success; the missing generated client then causes TypeScript TS2307 at the
`adminClient.js` import. Installing ICU fixes that build prerequisite without changing Node,
pnpm, TypeScript, Keycloak source or any type-checking step. The exact installed
RPM inventory is retained in `builder-rpms.txt` alongside the build proof.

The patch inserts exactly one BOM import before Infinispan and Quarkus, retaining
all original POM bytes. `source-pom.before.xml`, `source-pom.after.xml`, and the
insertion fragment `source-pom.patch` retain the complete change for comparison.
The standard upstream server reactor builds JS/themes and the final distribution
archive:

```sh
mvn -B -ntp -pl quarkus/deployment,quarkus/dist -am -DskipTests clean install
```

Task-specific BuildKit caches retain the Maven repository and pnpm package store
across interrupted downloads. The build command sets pnpm's HTTP timeout to
600 seconds through command-scoped `pnpm_config_*` variables; it does not change
host settings or the runtime image environment. The complete clean reactor and
frozen-lockfile install still run. Source working directories and Wireit task
state are not shared through these caches, preventing failed generation state
from being restored into a fresh source build.

The effective root/server POMs must match all 55 locked BOM coordinates, including
type and classifier. The resolved Netty dependency tree must match those versions;
inactive requests marked `omitted` are excluded. Every distributed JAR is opened
to inspect any `META-INF/maven/io.netty/*/pom.properties`. Unknown coordinates,
incorrect versions, missing metadata on named Netty JARs, missing runtime
`netty-handler`, unaccounted paths, and symbolic links stop the build.

Both subsequent image stages delete the fixed `/opt/keycloak` directory as root
before copying the complete rebuilt distribution. This removes the old base
JARs that Docker's overlay copy would otherwise retain. The builder preserves
the original PostgreSQL optimization and unused admin CLI / SQL Server driver
pruning. Both stages restore UID 1000, group 0. The final image contains the
runtime distribution and public audit proof; the source, Maven installation,
and build JDK stay in the source stage.

Public proof is installed at `/opt/keycloak/source-build-audit/`:

- `source-build.json`: input identities and hashes of the distribution and proof.
- `inputs.properties`, `source-inputs.sha256`, `toolchain.txt`: pinned inputs and
  the measured Java/Maven versions.
- `source-pom.before.xml`, `source-pom.after.xml`, `source-pom.patch`: exact POM
  bytes and the inserted XML fragment.
- `netty-bom.pom`, `netty-managed.tsv`, `effective-root.xml`, `effective-server.xml`,
  `netty-dependency-tree.txt`: dependency management and resolution evidence.
- `distribution.sha256`, `netty-jars.json`: complete distribution archive hash
  and all inspected Netty metadata, with retained-runtime flags.
- `netty-runtime.sha256`, `netty-runtime.paths`: hashes and the exact retained
  Netty JAR path set. Removed CLI contents are excluded from these runtime lists.

`/usr/local/bin/verify-keycloak-source-build` compares the installed Netty path
set, rejects symbolic links and old 4.1.136.Final filenames, then verifies every
listed JAR hash. It runs after optimization/pruning and again in the final stage.
It can also be called with an isolated image command to inspect build integrity;
it does not start Keycloak or contact a database.

The dependency-free helper can be checked with a JDK 21 source-mode invocation:

```sh
java source-build/SourceBuild.java self-test
```

The self-test covers first-priority insertion, preservation of original POM bytes,
duplicate rejection, effective versions and classifiers, resolved versions, and
actual JAR metadata versions. The `prepare` and `verify` argument contracts are
documented in the helper's argument checks and used literally by the Dockerfile.

These pins make the build inputs and procedure repeatable; they do not claim
byte-identical output archives because upstream archive timestamps and build
metadata are not normalized. A successful helper or image build is not a
vulnerability-policy pass or production deployment: the existing local image
gate and isolated functional checks must still run without weakened exemptions.

Official references:

- [Keycloak build instructions at the pinned commit](https://github.com/keycloak/keycloak/blob/289376b142480b4d600aca7acb1e4651862ed2a1/docs/building.md)
- [Keycloak root POM at the pinned commit](https://github.com/keycloak/keycloak/blob/289376b142480b4d600aca7acb1e4651862ed2a1/pom.xml)
- [Keycloak container permissions](https://github.com/keycloak/keycloak/blob/289376b142480b4d600aca7acb1e4651862ed2a1/quarkus/container/Dockerfile)
- [Netty BOM 4.1.137.Final](https://repo.maven.apache.org/maven2/io/netty/netty-bom/4.1.137.Final/netty-bom-4.1.137.Final.pom)
