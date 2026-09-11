#!/usr/bin/env bash
set -Eeuo pipefail
project_root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
failures=0
for component in wrapper operator; do
  for layout in standalone monorepo; do
    for mode in valid wrong-prefix missing-entry tampered; do
      label=$component-$layout-$mode
      [[ -z "${KEYCLOAK_MANIFEST_CASE:-}" || "$KEYCLOAK_MANIFEST_CASE" == "$label" ]] || continue
      release="$temporary/$label"
      if [[ "$layout" == standalone ]]; then relative=source; else relative=source/invoice; fi
      mkdir -p "$release/$relative/deploy/keycloak"
      for name in SOURCE_COMMIT SOURCE_TAG KEYCLOAK_IMAGE SMTP_TRANSPORT "$relative/deploy/keycloak/invite-permanent-master-admin.sh" "$relative/deploy/keycloak/run-permanent-master-admin-maintenance.sh"; do printf 'synthetic public binding\n' >"$release/$name"; done
      (
        cd "$release"
        # Linux manifests use the text marker; MSYS defaults to a binary '*'.
        if [[ "$component" == operator ]]; then sha256sum --text SOURCE_COMMIT SOURCE_TAG KEYCLOAK_IMAGE SMTP_TRANSPORT; fi
        sha256sum --text "$relative/deploy/keycloak/invite-permanent-master-admin.sh"
        [[ "$mode" == missing-entry ]] || sha256sum --text "$relative/deploy/keycloak/run-permanent-master-admin-maintenance.sh"
      ) >"$release/RELEASE-TREE.sha256"
      if [[ "$mode" == wrong-prefix ]]; then sed 's@source/@unexpected/@g' "$release/RELEASE-TREE.sha256" >"$release/wrong"; cp "$release/wrong" "$release/RELEASE-TREE.sha256"; fi
      if [[ "$mode" == tampered ]]; then printf 'changed\n' >>"$release/$relative/deploy/keycloak/invite-permanent-master-admin.sh"; fi
      probe="$release/probe.sh"
      { printf 'set -euo pipefail\ndie() { echo "$*" >&2; exit 1; }\n'
        printf 'RELEASE_ROOT=%q\nSOURCE_RELATIVE_ROOT=%q\n' "$release" "$relative"
        printf 'RELEASE_MANIFEST="$RELEASE_ROOT/RELEASE-TREE.sha256"\nRELEASE_BINDING_MANIFEST=$RELEASE_MANIFEST\nOPERATOR="$RELEASE_ROOT/$SOURCE_RELATIVE_ROOT/deploy/keycloak/invite-permanent-master-admin.sh"\nWRAPPER_PATH="$RELEASE_ROOT/$SOURCE_RELATIVE_ROOT/deploy/keycloak/run-permanent-master-admin-maintenance.sh"\n'
        if [[ "$component" == wrapper ]]; then
          sed -n '/^verify_release_file() {/,/^allowlist_is_safe /{ /^allowlist_is_safe /!p; }' "$project_root/deploy/keycloak/run-permanent-master-admin-maintenance.sh"
        else
          sed -n '/^release_manifest_entries=0/,/^SOURCE_COMMIT=/{ /^SOURCE_COMMIT=/!p; }' "$project_root/deploy/keycloak/invite-permanent-master-admin.sh"
        fi
      } >"$probe"
      if bash "$probe" >"$temporary/output" 2>&1; then actual=0; else actual=$?; fi
      if [[ "$mode" == valid && "$actual" != 0 ]] || [[ "$mode" != valid && "$actual" == 0 ]]; then echo "FAIL $label: exit=$actual" >&2; cat "$temporary/output" >&2; failures=$((failures+1)); fi
    done
  done
done
(( failures == 0 )) || exit 1
echo 'Keycloak signed-manifest path cases passed.'
