#!/usr/bin/env bash
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
dockerfile="$repo_root/deploy/docker/go.Dockerfile"
compose="$repo_root/deploy/compose/server-prod.yaml"
deploy_local="$repo_root/deploy/scripts/deploy-local.sh"
installer="$repo_root/deploy/scripts/install-cpa-snapshot.sh"
service="$repo_root/deploy/cpa-snapshot/xingmang-cpa-snapshot.service"
timer="$repo_root/deploy/cpa-snapshot/xingmang-cpa-snapshot.timer"
fail=0

ok() { printf 'ok - %s\n' "$1"; }
bad() { printf 'not ok - %s\n' "$1" >&2; fail=1; }
has() { grep -Fq -- "$2" "$1" 2>/dev/null; }
count() { grep -Fc -- "$2" "$1" 2>/dev/null || true; }

for file in "$service" "$timer"; do
  [ -f "$file" ] && ok "$(basename "$file") exists" || bad "$(basename "$file") exists"
done

has "$dockerfile" './cmd/cpa-snapshot' &&
  has "$dockerfile" '/out/cpa-snapshot' &&
  has "$dockerfile" 'COPY --from=builder /out/cpa-snapshot /usr/local/bin/cpa-snapshot' \
  && ok 'migrate image carries cpa-snapshot' || bad 'migrate image carries cpa-snapshot'

if has "$compose" '/root/cpa-stack/cpam-data'; then
  bad 'API/worker compose never mounts live cpam-data'
else
  ok 'API/worker compose never mounts live cpam-data'
fi
[ "$(count "$compose" 'source: /var/lib/xingmang/cpa-snapshot/published')" = 2 ] \
  && ok 'two consumers use the fixed published directory' || bad 'two consumers use the fixed published directory'
[ "$(count "$compose" 'target: /var/lib/xm/cpa')" = 2 ] \
  && ok 'two consumers use the fixed container directory' || bad 'two consumers use the fixed container directory'
[ "$(count "$compose" 'read_only: true')" -ge 2 ] \
  && ok 'snapshot binds are read-only' || bad 'snapshot binds are read-only'
[ "$(count "$compose" 'create_host_path: false')" -ge 2 ] \
  && ok 'snapshot binds cannot auto-create an empty source' || bad 'snapshot binds cannot auto-create an empty source'
for consumer in platform-api platform-worker; do
  consumer_block="$(awk -v service="$consumer" '
    $0 == "  " service ":" { inside=1; next }
    inside && $0 ~ /^  [a-zA-Z0-9_-]+:$/ { exit }
    inside { print }
  ' "$compose")"
  if printf '%s\n' "$consumer_block" | grep -Fq 'source: /var/lib/xingmang/cpa-snapshot/published' &&
     printf '%s\n' "$consumer_block" | grep -Fq 'target: /var/lib/xm/cpa' &&
     printf '%s\n' "$consumer_block" | grep -Fq 'read_only: true' &&
     printf '%s\n' "$consumer_block" | grep -Fq 'create_host_path: false'; then
    ok "$consumer binds one complete read-only CPA snapshot volume"
  else
    bad "$consumer binds one complete read-only CPA snapshot volume"
  fi
done

if [ -f "$service" ]; then
  for required in 'Type=oneshot' 'ExecStart=/opt/xingmang/cpa-snapshot/current/cpa-snapshot publish' \
    'User=root' 'Group=root' 'PrivateNetwork=yes' 'NoNewPrivileges=yes' \
    'PrivateDevices=yes' 'ProtectSystem=strict' 'ProtectHome=read-only' \
    'InaccessiblePaths=/root/cpa-stack/cpa/auths /root/cpa-stack/cpa/config.yaml' \
    'InaccessiblePaths=-/run/docker.sock -/var/run/docker.sock -/run/containerd/containerd.sock -/run/systemd/private' \
    'CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER' \
    'RestrictNamespaces=yes' 'RestrictAddressFamilies=AF_UNIX' \
    'Nice=10' 'TimeoutStartSec=5min'; do
    has "$service" "$required" || bad "service missing $required"
  done
  if grep -Eq '^ExecStart=.*docker|^ReadWritePaths=.*docker\.sock' "$service"; then
    bad 'snapshot service must not receive Docker control'
  else
    ok 'snapshot service has no Docker control'
  fi
fi

if [ -f "$timer" ]; then
  # OnCalendar 是 2026-09-08 事故后加的墙钟兜底：只靠单调触发的定时器一旦过期就
  # 再也不会自己醒来（生产停摆了七天没人知道）。它必须一直在，缺了就是把闸拆了。
  for required in 'OnBootSec=2min' 'OnUnitInactiveSec=5min' 'OnCalendar=*:0/5' 'RandomizedDelaySec=15s' 'Persistent=true'; do
    has "$timer" "$required" || bad "timer missing $required"
  done
fi

has "$deploy_local" 'phase="cpa-snapshot"' &&
  has "$deploy_local" 'install-cpa-snapshot.sh' &&
  has "$installer" 'systemctl start xingmang-cpa-snapshot.service' &&
  has "$deploy_local" 'systemctl enable --now xingmang-cpa-snapshot.timer' &&
  python3 "$repo_root/tests/security/cpa-snapshot-install-order.test.py" "$deploy_local" "$BASH" "$(command -v grep)" "$(command -v tail)" \
  && ok 'deploy-local installs first generation before app startup' || bad 'deploy-local installs first generation before app startup'
has "$deploy_local" 'CPA file mode requires migrate in build service set' \
  && ok 'CPA file mode cannot reuse an unselected stale migrate image' || bad 'CPA file mode cannot reuse an unselected stale migrate image'
has "$deploy_local" 'phase="cpa-consumer"' &&
  has "$deploy_local" '/var/lib/xingmang/cpa-snapshot/published|false' &&
  has "$deploy_local" 'test ! -e /var/lib/xm/cpa/usage.sqlite-wal' \
  && ok 'deploy-local verifies the runtime consumer mount and standalone file' || bad 'deploy-local verifies the runtime consumer mount and standalone file'
preflight_line="$(grep -nF 'phase="cpa-consumer-preflight"' "$deploy_local" | head -n 1 | cut -d: -f1)"
up_line="$(grep -nF 'phase="up-app"' "$deploy_local" | head -n 1 | cut -d: -f1)"
if [ -n "$preflight_line" ] && [ -n "$up_line" ] && [ "$preflight_line" -lt "$up_line" ]; then
  ok 'consumer preflight runs before replacing app containers'
else
  bad 'consumer preflight runs before replacing app containers'
fi
has "$deploy_local" 'phase="cpa-observations"' &&
  has "$deploy_local" 'FROM ops.metric_observation' &&
  has "$deploy_local" "value_json->>'run_at'" \
  && ok 'deploy-local gates on four same-generation CPA observations and run_at' || bad 'deploy-local gates on four same-generation CPA observations and run_at'
has "$deploy_local" "sed 's/[[:space:]]#.*$//'" \
  && ok 'deploy-local parses unquoted dotenv inline comments like Compose' || bad 'deploy-local parses unquoted dotenv inline comments like Compose'

if rg -n 'immutable=1' "$repo_root/connectors/cpa" "$compose" 2>/dev/null >/dev/null ||
   grep -Eq '^[[:space:]-]*(source:[[:space:]]*)?.*(cpam-data|/cpa/auths|config\.yaml).*:/var/lib/xm/cpa' "$compose"; then
  bad 'runtime wiring contains a forbidden CPA access/bypass'
else
  ok 'runtime wiring has no immutable or credential access bypass'
fi

exit "$fail"
