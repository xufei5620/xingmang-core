#!/bin/sh
# Operator-invoked metadata-only capture. Credentials remain libpq file references.
set -eu
exec python3 "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/capture_metadata.py" "$@"
