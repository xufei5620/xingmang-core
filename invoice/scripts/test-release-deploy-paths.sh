#!/usr/bin/env bash
set -Eeuo pipefail
project_root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
failures=0
asset_names=(docker-compose.prod.yml docker-compose.idp.yml docker-compose.sources.yml postgres/apply-permissions.sh postgres/harden-runtime-role.sql postgres/010-invoice-roles.sh keycloak/010-keycloak-app-role.sh clamav-healthcheck.sh nginx/ingest-mtls.conf)
for component in roll-forward wrapper operator; do
  for layout in standalone monorepo ambiguous wrong noncanonical "${asset_names[@]/#/missing-}" "${asset_names[@]/#/directory-}" "${asset_names[@]/#/symlink-}"; do
    if [[ "$layout" != standalone && "$layout" != monorepo && "$layout" != ambiguous && "$layout" != wrong && "$component" != roll-forward ]]; then continue; fi
    label=$component-$layout
    [[ -z "${RELEASE_PATH_CASE:-}" || "$RELEASE_PATH_CASE" == "$label" ]] || continue
    release="$temporary/$label/release"
    case "$layout" in standalone) relative=source;; wrong) relative=unexpected;; *) relative=source/invoice;; esac
    deploy="$release/$relative/deploy"
    mkdir -p "$deploy/keycloak" "$deploy/postgres" "$deploy/nginx" "$release/source"
    if [[ "$layout" == ambiguous ]]; then mkdir -p "$release/source/deploy"; fi
    printf 'fixture only\n' >"$release/.env.production"
    printf 'fixture public asset\n' >"$release/asset-target"
    for name in "${asset_names[@]}"; do
      [[ "$layout" != "missing-$name" ]] || continue
      if [[ "$layout" == "directory-$name" ]]; then
        mkdir -p "$deploy/$name"
      elif [[ "$layout" == "symlink-$name" ]]; then
        # MSYS .lnk symlinks need no Windows symlink privilege; Bash -L and -f
        # still observe a real symbolic link. Native Unix ln ignores MSYS.
        MSYS=winsymlinks:lnk ln -s "$release/asset-target" "$deploy/$name"
        [[ -L "$deploy/$name" && -f "$deploy/$name" ]] || { echo 'fixture symlink creation failed' >&2; exit 2; }
      else
        printf 'fixture only\n' >"$deploy/$name"
      fi
    done
    if [[ "$component" == roll-forward ]]; then
      probe="$deploy/path-probe.sh"
      { printf 'set -euo pipefail\nroot=%q\n' "$release"
        if [[ "$layout" == noncanonical ]]; then printf 'realpath() { printf "/fixture/escaped\\n"; }\n'; fi
        sed -n '/^env_file=$root/,/^tag=$(sed /{ /^tag=$(sed /!p; }' "$project_root/deploy/roll-forward.sh"
        printf 'printf "resolved=%%s\\n" "$deploy_dir"\n'
      } >"$probe"
    else
      if [[ "$component" == wrapper ]]; then filename=run-permanent-master-admin-maintenance.sh; first='^readonly WRAPPER_PATH='; last='^readonly OPERATOR='; else filename=invite-permanent-master-admin.sh; first='^readonly OPERATOR_PATH='; last='^readonly SOURCE_COMMIT_FILE='; fi
      probe="$deploy/keycloak/$filename"
      { printf 'set -euo pipefail\ndie() { echo "$*" >&2; exit 1; }\n'
        sed -n "/$first/,/$last/{ /$last/!p; }" "$project_root/deploy/keycloak/$filename"
        printf 'printf "resolved=%%s\\n" "$RELEASE_ROOT"\n'
      } >"$probe"
    fi
    if bash "$probe" >"$temporary/output" 2>&1; then actual=0; else actual=$?; fi
    if [[ "$layout" == standalone || "$layout" == monorepo ]]; then expected=0; else expected=1; fi
    if [[ "$expected" == 0 && "$actual" != 0 ]] || [[ "$expected" != 0 && "$actual" == 0 ]]; then echo "FAIL $label: exit=$actual expected=$expected" >&2; failures=$((failures+1)); fi
    if [[ "$expected" == 0 && "$actual" == 0 ]]; then
      if [[ "$component" == roll-forward ]]; then expected_path=$deploy; else expected_path=$release; fi
      if [[ "$(cat "$temporary/output")" != "resolved=$expected_path" ]]; then echo "FAIL $label: wrong resolved root" >&2; failures=$((failures+1)); fi
    fi
  done
done
(( failures == 0 )) || exit 1
echo 'Release deployment path cases passed.'
