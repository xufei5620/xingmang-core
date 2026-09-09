#!/usr/bin/env bash
# Install the Docker-built CPA snapshot lifecycle binary and systemd units.
set -Eeuo pipefail
umask 027

die() { echo "CPA SNAPSHOT INSTALL FAIL: $*" >&2; exit 1; }

[ "$#" -eq 2 ] || die "usage: install-cpa-snapshot.sh <binary> <40-char-commit>"
[ "${EUID:-$(id -u)}" -eq 0 ] || die "must run as root"

binary="$1"
commit="$2"
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "commit must be a lowercase 40-character SHA"
[ -f "$binary" ] && [ ! -L "$binary" ] && [ -x "$binary" ] || die "binary must be a regular executable"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(cd -- "$script_dir/../.." && pwd -P)"
unit_source="$repo_root/deploy/cpa-snapshot"
install_root=/opt/xingmang/cpa-snapshot
state_root=/var/lib/xingmang/cpa-snapshot
systemd_root=/etc/systemd/system

for command_name in awk flock install ln mv readlink rm sed sha256sum systemctl; do
  command -v "$command_name" >/dev/null 2>&1 || die "required command unavailable: $command_name"
done
[ -d /run/lock ] && [ ! -L /run/lock ] || die "/run/lock is unavailable or symlinked"
exec 9>/run/lock/xingmang-cpa-snapshot-install.lock
flock -n 9 || die "another CPA snapshot installation is running"

version_output="$($binary version 2>/dev/null)" || die "cannot read binary build identity"
read -r build_version build_commit extra <<<"$version_output"
[ -n "$build_version" ] && [ "$build_commit" = "$commit" ] && [ -z "${extra:-}" ] \
  || die "binary build commit does not match deployment commit"

binary_sha="$(sha256sum "$binary" | awk '{print $1}')"
[[ "$binary_sha" =~ ^[0-9a-f]{64}$ ]] || die "cannot hash binary"
release_name="${commit:0:12}-${binary_sha:0:12}"
release_dir="$install_root/releases/$release_name"

install -d -o root -g root -m 0755 "$install_root" "$install_root/releases"
install -d -o root -g 10001 -m 0750 "$state_root/published"
install -d -o root -g root -m 0700 "$state_root/staging" "$state_root/history"
install -d -o root -g root -m 0755 "$release_dir"
install -o root -g root -m 0555 "$binary" "$release_dir/cpa-snapshot"
[ "$(sha256sum "$release_dir/cpa-snapshot" | awk '{print $1}')" = "$binary_sha" ] \
  || die "installed binary hash mismatch"
before_generation=""
if [ -e "$state_root/published/usage.sqlite" ]; then
  [ -f "$state_root/published/usage.sqlite" ] && [ ! -L "$state_root/published/usage.sqlite" ] \
    || die "existing published snapshot is unsafe"
  before_json="$($release_dir/cpa-snapshot verify 2>/dev/null)" \
    || die "existing published snapshot does not verify"
  before_generation="$(printf '%s\n' "$before_json" | sed -n 's/.*"generation":"\([0-9a-f]\{32\}\)".*/\1/p')"
  [[ "$before_generation" =~ ^[0-9a-f]{32}$ ]] || die "existing published generation is malformed"
fi

previous_link="$(readlink "$install_root/current" 2>/dev/null || true)"
for unit in xingmang-cpa-snapshot.service xingmang-cpa-snapshot.timer; do
  [ -f "$unit_source/$unit" ] && [ ! -L "$unit_source/$unit" ] || die "missing unit $unit"
done
timer_was_active=0
timer_was_enabled=0
systemctl is-active --quiet xingmang-cpa-snapshot.timer 2>/dev/null && timer_was_active=1 || true
systemctl is-enabled --quiet xingmang-cpa-snapshot.timer 2>/dev/null && timer_was_enabled=1 || true

previous_unit_dir="$release_dir/previous-units"
install -d -o root -g root -m 0700 "$previous_unit_dir"
units_with_previous=""
units_without_previous=""
for unit in xingmang-cpa-snapshot.service xingmang-cpa-snapshot.timer; do
  if [ -e "$systemd_root/$unit" ]; then
    [ -f "$systemd_root/$unit" ] && [ ! -L "$systemd_root/$unit" ] || die "existing unit $unit is unsafe"
    install -o root -g root -m 0600 "$systemd_root/$unit" "$previous_unit_dir/$unit"
    units_with_previous="$units_with_previous $unit"
  else
    units_without_previous="$units_without_previous $unit"
  fi
done

rollback_needed=1
rollback() {
  [ "$rollback_needed" -eq 1 ] || return 0
  if [ -n "$previous_link" ]; then
    rm -f -- "$install_root/current.rollback"
    ln -s "$previous_link" "$install_root/current.rollback"
    mv -Tf "$install_root/current.rollback" "$install_root/current"
  else
    rm -f -- "$install_root/current"
  fi
  for unit in $units_with_previous; do
    install -o root -g root -m 0644 "$previous_unit_dir/$unit" "$systemd_root/$unit" >/dev/null 2>&1 || true
  done
  for unit in $units_without_previous; do
    rm -f -- "$systemd_root/$unit"
  done
  systemctl daemon-reload >/dev/null 2>&1 || true
  [ "$timer_was_enabled" -eq 1 ] && systemctl enable xingmang-cpa-snapshot.timer >/dev/null 2>&1 || true
  [ "$timer_was_active" -eq 1 ] && systemctl start xingmang-cpa-snapshot.timer >/dev/null 2>&1 || true
}
trap rollback EXIT

systemctl disable --now xingmang-cpa-snapshot.timer >/dev/null 2>&1 || true
for unit in xingmang-cpa-snapshot.service xingmang-cpa-snapshot.timer; do
  install -o root -g root -m 0644 "$unit_source/$unit" "$systemd_root/$unit"
done
rm -f -- "$install_root/current.new"
ln -s "releases/$release_name" "$install_root/current.new"
mv -Tf "$install_root/current.new" "$install_root/current"
systemctl daemon-reload
systemctl start xingmang-cpa-snapshot.service
after_json="$("$install_root/current/cpa-snapshot" verify)" || die "new published snapshot does not verify"
after_generation="$(printf '%s\n' "$after_json" | sed -n 's/.*"generation":"\([0-9a-f]\{32\}\)".*/\1/p')"
[[ "$after_generation" =~ ^[0-9a-f]{32}$ ]] || die "new published generation is malformed"
[ -z "$before_generation" ] || [ "$after_generation" != "$before_generation" ] \
  || die "snapshot service did not publish a fresh generation"
if systemctl is-active --quiet xingmang-cpa-snapshot.timer; then
  die "timer started before platform verification"
fi

rollback_needed=0
trap - EXIT
echo "CPA SNAPSHOT INSTALL PASS: commit=$commit artifact_sha256=$binary_sha release=$release_name generation=$after_generation"
