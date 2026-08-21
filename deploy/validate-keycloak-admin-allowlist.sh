#!/usr/bin/env bash
set -euo pipefail

path="${1:?usage: validate-keycloak-admin-allowlist.sh PATH}"
test -f "$path" && test ! -L "$path" && test -s "$path"

python3 - "$path" <<'PY'
import ipaddress
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
rules = []
for number, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
    line = raw.split("#", 1)[0].strip()
    if not line:
        continue
    rules.append((number, line))

if not rules or rules[-1][1] != "deny all;":
    raise SystemExit("allowlist must end with exactly 'deny all;'")
if sum(line == "deny all;" for _, line in rules) != 1:
    raise SystemExit("allowlist must contain exactly one 'deny all;' rule")

addresses = []
for number, line in rules[:-1]:
    match = re.fullmatch(r"allow\s+([^;\s]+);", line)
    if not match:
        raise SystemExit(f"line {number}: only allow HOST_CIDR; is permitted")
    try:
        network = ipaddress.ip_network(match.group(1), strict=True)
    except ValueError as exc:
        raise SystemExit(f"line {number}: invalid canonical CIDR: {exc}") from exc
    if network.prefixlen != network.max_prefixlen:
        raise SystemExit(f"line {number}: allow entry must be an exact host route")
    addresses.append(str(network))

if len(addresses) < 2:
    raise SystemExit("allowlist requires normal-admin and independent break-glass host routes")
if len(addresses) != len(set(addresses)):
    raise SystemExit("allowlist contains a duplicate host route")
PY

printf 'Keycloak admin allowlist contains only exact host routes and deny all\n'
