#!/usr/bin/env bash
set -euo pipefail

readonly SIGNING_IDENTITY='Developer ID Application: ARDA SEVINC (J3S8HNBXSU)'
readonly SIGNING_IDENTIFIER='com.ardasevinc.tele'
readonly REQUIREMENT='identifier "com.ardasevinc.tele" and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] /* exists */ and certificate leaf[field.1.2.840.113635.100.6.1.13] /* exists */ and certificate leaf[subject.OU] = J3S8HNBXSU'

[[ $# -eq 1 ]] || { echo 'usage: scripts/prove-keychain-upgrade.sh EVIDENCE_DIRECTORY' >&2; exit 2; }
evidence=$1
[[ $(uname -s) == Darwin ]] || { echo 'Keychain upgrade proof requires Darwin' >&2; exit 1; }
[[ ! -e $evidence ]] || { echo "evidence path already exists: $evidence" >&2; exit 1; }
evidence_parent=$(cd "$(dirname "$evidence")" && pwd)
evidence="$evidence_parent/$(basename "$evidence")"

for command in codesign git go jq security shasum uuidgen; do
  command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
done
security find-identity -v -p codesigning | grep -Fq "\"$SIGNING_IDENTITY\"" || { echo 'Developer ID identity is unavailable' >&2; exit 1; }

work=$(mktemp -d "${TMPDIR:-/tmp}/tele-keychain-upgrade.XXXXXX")
trap 'trash "$work" >/dev/null 2>&1 || true' EXIT
mkdir -m 0700 "$evidence" "$work/data"
profile="upgrade-proof-$(uuidgen | tr '[:upper:]' '[:lower:]')"
instance=$(uuidgen | tr '[:upper:]' '[:lower:]')

build_fixture() {
  local name=$1 build_id=$2
  CGO_ENABLED=0 GOOS=darwin GOARCH="$(go env GOARCH)" go build -trimpath -buildvcs=false \
    -ldflags "-s -w -buildid= -X main.buildID=$build_id" \
    -o "$evidence/$name" ./internal/secrets/testdata/keychain-upgrade-fixture
  chmod 0755 "$evidence/$name"
}

verify_official() {
  local binary=$1
  codesign --verify --strict -R="$REQUIREMENT" --verbose=2 "$binary"
  [[ $(codesign -dr - "$binary" 2>&1 | sed -n 's/^designated => //p') == "$REQUIREMENT" ]]
}

build_fixture tele-a "A-$(git rev-parse --short=12 HEAD)"
build_fixture tele-b "B-$(git rev-parse --short=12 HEAD)"
build_fixture tele-negative "negative-$(git rev-parse --short=12 HEAD)"

codesign --force --sign "$SIGNING_IDENTITY" --identifier "$SIGNING_IDENTIFIER" --options runtime --timestamp "$evidence/tele-a"
codesign --force --sign "$SIGNING_IDENTITY" --identifier "$SIGNING_IDENTIFIER" --options runtime --timestamp "$evidence/tele-b"
codesign --force --sign - --identifier "$SIGNING_IDENTIFIER" "$evidence/tele-negative"
verify_official "$evidence/tele-a"
verify_official "$evidence/tele-b"

hash_a=$(shasum -a 256 "$evidence/tele-a" | awk '{print $1}')
hash_b=$(shasum -a 256 "$evidence/tele-b" | awk '{print $1}')
cdhash_a=$(codesign -dvvv "$evidence/tele-a" 2>&1 | awk -F= '/^CDHash=/{print $2}')
cdhash_b=$(codesign -dvvv "$evidence/tele-b" 2>&1 | awk -F= '/^CDHash=/{print $2}')
requirement_a=$(codesign -dr - "$evidence/tele-a" 2>&1 | sed -n 's/^designated => //p')
requirement_b=$(codesign -dr - "$evidence/tele-b" 2>&1 | sed -n 's/^designated => //p')
[[ $hash_a != "$hash_b" && $cdhash_a != "$cdhash_b" && $requirement_a == "$requirement_b" ]]

cleanup_required=true
cleanup_instance() {
  if [[ $cleanup_required == true ]]; then
    "$evidence/tele-b" purge "$work/data" "$profile" "$instance" > "$evidence/purge.txt" 2> "$evidence/purge.err" || true
  fi
}
trap 'cleanup_instance; trash "$work" >/dev/null 2>&1 || true' EXIT

"$evidence/tele-a" create "$work/data" "$profile" "$instance" > "$evidence/create.txt" 2> "$evidence/create.err"
"$evidence/tele-b" read "$work/data" "$profile" "$instance" > "$evidence/read.txt" 2> "$evidence/read.err"
"$evidence/tele-negative" read-negative "$work/data" "$profile" "$instance" > "$evidence/negative.txt" 2> "$evidence/negative.err"
"$evidence/tele-b" purge "$work/data" "$profile" "$instance" > "$evidence/purge.txt" 2> "$evidence/purge.err"
cleanup_required=false

jq -n \
  --arg schema tele-keychain-upgrade-proof-v1 \
  --arg commit "$(git rev-parse HEAD)" \
  --arg profile "$profile" \
  --arg instance "$instance" \
  --arg hash_a "$hash_a" --arg hash_b "$hash_b" \
  --arg cdhash_a "$cdhash_a" --arg cdhash_b "$cdhash_b" \
  --arg requirement "$requirement_a" \
  '{schema:$schema,commit:$commit,profile:$profile,instance:$instance,binaries:{a:{sha256:$hash_a,cdhash:$cdhash_a},b:{sha256:$hash_b,cdhash:$cdhash_b}},requirement:$requirement,claims:{different_bytes:true,different_cdhashes:true,identical_designated_requirements:true,a_created_b_read_without_ui:true,different_signer_denied_without_ui:true,instance_purged:true}}' \
  > "$evidence/receipt.json"

echo "physical Keychain A-to-B proof complete: $evidence"
