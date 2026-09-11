#!/usr/bin/env bash
set -Eeuo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
exec "${PYTHON:-python3}" "$script_dir/../rehearsal-unified/lifecycle.py" rollback "$@"
