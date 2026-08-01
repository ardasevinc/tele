#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: smoke-linux-release.sh BINARY_DIR VERSION COMMIT" >&2
  exit 2
fi

binary_dir="$(realpath "$1")"
version="$2"
commit="$3"
alpine_image="alpine@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce"
smoke_root="$(mktemp -d)"
trap 'rm -r "$smoke_root"' EXIT
mkdir -m 700 "$smoke_root/state"
printf '%s\n' 'release smoke vault passphrase' >"$smoke_root/state/credential"
chmod 600 "$smoke_root/state/credential"

tele_container() {
  docker run --rm --network none --read-only \
    --user "$(id -u):$(id -g)" \
    --env HOME=/state/home \
    --env XDG_CONFIG_HOME=/state/config \
    --env XDG_DATA_HOME=/state/data \
    --env XDG_RUNTIME_DIR=/state/runtime \
    --env TELE_RELEASE_SMOKE_SECRET=TELE_RELEASE_SMOKE_SECRET_7d13 \
    --volume "$binary_dir:/tele:ro" \
    --volume "$smoke_root/state:/state" \
    "$alpine_image" /tele/tele "$@"
}

test "$(tele_container --version)" = "tele version $version ($commit)"
tele_container --json profiles use smoke >"$smoke_root/profiles.json"
tele_container --json config set api-id 123456 >"$smoke_root/api-id.json"
tele_container --json --vault-passphrase-file /state/credential \
  secrets init --backend vault-v1 >"$smoke_root/init.json"
tele_container --json --vault-passphrase-file /state/credential \
  config set api-hash --value-env TELE_RELEASE_SMOKE_SECRET >"$smoke_root/api-hash.json"
tele_container --json --vault-passphrase-file /state/credential \
  secrets migrate --to vault-v1 >"$smoke_root/migrate.json"

source_instance="$(jq -r '.data.source.instance' "$smoke_root/migrate.json")"
test -n "$source_instance"
tele_container --json --vault-passphrase-file /state/credential \
  secrets purge --backend vault-v1 "$source_instance" \
  --confirm-instance "$source_instance" >"$smoke_root/purge.json"

set +e
tele_container --json --vault-passphrase-file /state/credential \
  doctor >"$smoke_root/doctor.json"
doctor_status=$?
set -e
test "$doctor_status" -eq 1
jq -e '.data.purged == true' "$smoke_root/purge.json" >/dev/null
jq -e '.data.checks[] | select(.name == "secret_store" and .status == "pass")' \
  "$smoke_root/doctor.json" >/dev/null
jq -e '.data.checks[] | select(.name == "vault" and .status == "pass")' \
  "$smoke_root/doctor.json" >/dev/null

if grep -R -a -F 'TELE_RELEASE_SMOKE_SECRET_7d13' "$smoke_root"; then
  echo "synthetic secret leaked from the packaged Linux smoke" >&2
  exit 1
fi

mkdir -m 700 "$smoke_root/notty" "$smoke_root/fd" "$smoke_root/pty"
env HOME="$smoke_root/notty/home" \
  XDG_CONFIG_HOME="$smoke_root/notty/config" \
  XDG_DATA_HOME="$smoke_root/notty/data" \
  "$binary_dir/tele" profiles use notty >/dev/null
set +e
env HOME="$smoke_root/notty/home" \
  XDG_CONFIG_HOME="$smoke_root/notty/config" \
  XDG_DATA_HOME="$smoke_root/notty/data" \
  "$binary_dir/tele" secrets init --backend vault-v1 \
  </dev/null >"$smoke_root/notty-output" 2>&1
notty_status=$?
set -e
test "$notty_status" -ne 0
grep -F 'no controlling TTY' "$smoke_root/notty-output" >/dev/null

env HOME="$smoke_root/fd/home" \
  XDG_CONFIG_HOME="$smoke_root/fd/config" \
  XDG_DATA_HOME="$smoke_root/fd/data" \
  "$binary_dir/tele" profiles use fd >/dev/null
env HOME="$smoke_root/fd/home" \
  XDG_CONFIG_HOME="$smoke_root/fd/config" \
  XDG_DATA_HOME="$smoke_root/fd/data" \
  "$binary_dir/tele" --vault-passphrase-fd 3 \
  secrets init --backend vault-v1 3<"$smoke_root/state/credential" >/dev/null

env HOME="$smoke_root/pty/home" \
  XDG_CONFIG_HOME="$smoke_root/pty/config" \
  XDG_DATA_HOME="$smoke_root/pty/data" \
  "$binary_dir/tele" profiles use pty >/dev/null
{
  sleep 0.2
  printf '%s\n%s\n' 'pty smoke passphrase' 'pty smoke passphrase'
} | timeout 10 script -qec \
  "env HOME='$smoke_root/pty/home' XDG_CONFIG_HOME='$smoke_root/pty/config' XDG_DATA_HOME='$smoke_root/pty/data' '$binary_dir/tele' secrets init --backend vault-v1" \
  /dev/null >"$smoke_root/pty-output" 2>&1
grep -F 'initialized vault-v1 for profile pty' "$smoke_root/pty-output" >/dev/null
if grep -F 'pty smoke passphrase' "$smoke_root/pty-output"; then
  echo "PTY passphrase was echoed" >&2
  exit 1
fi
