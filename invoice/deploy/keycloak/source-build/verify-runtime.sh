#!/bin/sh
set -eu

# This verifier reads only the public build proof and the installed JARs.
cd /opt/keycloak
test -s source-build-audit/netty-runtime.sha256
test -s source-build-audit/netty-runtime.paths
test -s source-build-audit/source-build.json
test ! -e bin/client
symbolic_links=$(find . -type l -print -quit)
if [ -n "$symbolic_links" ]; then
    echo 'Keycloak distribution contains an unverified symbolic link' >&2
    exit 1
fi
actual_paths_unsorted=$(find . -type f -name 'io.netty.netty-*.jar' -printf '%P\n')
actual_paths=$(printf '%s\n' "$actual_paths_unsorted" | LC_ALL=C sort)
expected_paths=$(cat source-build-audit/netty-runtime.paths)
if [ "$actual_paths" != "$expected_paths" ]; then
    echo 'Keycloak Netty JAR path set differs from the verified distribution' >&2
    exit 1
fi
old_netty_paths=$(find . -type f -name '*netty*4.1.136.Final*.jar' -print -quit)
if [ -n "$old_netty_paths" ]; then
    echo 'Keycloak still contains a Netty 4.1.136.Final JAR' >&2
    exit 1
fi
sha256sum -c source-build-audit/netty-runtime.sha256
