#!/usr/bin/env bash
set -euo pipefail

readonly SIGNING_IDENTITY='Developer ID Application: ARDA SEVINC (J3S8HNBXSU)'
readonly SIGNING_IDENTIFIER='com.ardasevinc.tele'
readonly TEAM_ID='J3S8HNBXSU'
readonly REQUIREMENT='identifier "com.ardasevinc.tele" and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] /* exists */ and certificate leaf[field.1.2.840.113635.100.6.1.13] /* exists */ and certificate leaf[subject.OU] = J3S8HNBXSU'

usage() {
  echo 'usage: scripts/release-macos.sh VERSION COMMIT OUTPUT_DIRECTORY' >&2
  exit 2
}

[[ $# -eq 3 ]] || usage
version=${1#v}
commit=$2
output=$3

[[ $(uname -s) == Darwin ]] || { echo 'trusted macOS release must run on Darwin' >&2; exit 1; }
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || usage
[[ $commit =~ ^[0-9a-f]{7,40}$ ]] || usage
[[ ! -e $output ]] || { echo "output already exists: $output" >&2; exit 1; }
output_parent=$(cd "$(dirname "$output")" && pwd)
output="$output_parent/$(basename "$output")"

for command in asc codesign curl ditto file git go jq security shasum; do
  command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
done

[[ $(git rev-parse HEAD) == "$commit" ]] || { echo 'commit must equal the checked-out HEAD' >&2; exit 1; }
[[ -z $(git status --porcelain=v1 --untracked-files=all) ]] || { echo 'trusted release requires a clean worktree' >&2; exit 1; }
source_version=$(awk -F'"' '/^[[:space:]]*Version = / {print $2}' internal/buildinfo/buildinfo.go)
[[ $source_version == "$version" ]] || { echo "source version $source_version does not match $version" >&2; exit 1; }
security find-identity -v -p codesigning | grep -Fq "\"$SIGNING_IDENTITY\"" || { echo 'Developer ID identity is unavailable' >&2; exit 1; }

work=$(mktemp -d "${TMPDIR:-/tmp}/tele-release-macos.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir -m 0755 "$output"

echo 'building reproducible unsigned inputs twice'
GOCACHE="$work/gocache-a" go run ./scripts/release \
  --version "$version" --commit "$commit" \
  --output "$work/unsigned-archives-a" --binary-output "$work/unsigned-a"
GOCACHE="$work/gocache-b" go run ./scripts/release \
  --version "$version" --commit "$commit" \
  --output "$work/unsigned-archives-b" --binary-output "$work/unsigned-b"
diff -r "$work/unsigned-a" "$work/unsigned-b"
diff -r "$work/unsigned-archives-a" "$work/unsigned-archives-b"

mkdir -m 0755 "$work/final-binaries"
for platform in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  cp "$work/unsigned-a/tele_$platform" "$work/final-binaries/tele_$platform"
  chmod 0755 "$work/final-binaries/tele_$platform"
done
(
  cd "$work/unsigned-a"
  shasum -a 256 tele_* | sort > "$output/unsigned-checksums.txt"
)

verify_signed_binary() {
  local binary=$1 details requirement
  codesign --verify --strict --verbose=2 "$binary"
  codesign --verify --strict --check-notarization -R=notarized --verbose=2 "$binary"
  codesign --verify --strict -R="$REQUIREMENT" --verbose=2 "$binary"
  details=$(codesign -dvvv "$binary" 2>&1)
  grep -Fq "Identifier=$SIGNING_IDENTIFIER" <<<"$details"
  grep -Fq "TeamIdentifier=$TEAM_ID" <<<"$details"
  grep -Fq "Authority=$SIGNING_IDENTITY" <<<"$details"
  grep -Fq 'Runtime Version=' <<<"$details"
  grep -Fq 'Timestamp=' <<<"$details"
  requirement=$(codesign -dr - "$binary" 2>&1 | sed -n 's/^designated => //p')
  [[ $requirement == "$REQUIREMENT" ]] || { echo "unexpected designated requirement for $binary" >&2; exit 1; }
  [[ $("$binary" internal official-build) == tele-official-build-v1 ]]
  [[ $("$binary" --version) == "tele version $version ($commit)" ]]
}

echo 'signing final Darwin binaries'
for arch in amd64 arm64; do
  binary="$work/final-binaries/tele_darwin_$arch"
  codesign --force --sign "$SIGNING_IDENTITY" --identifier "$SIGNING_IDENTIFIER" \
    --options runtime --timestamp "$binary"
  codesign --verify --strict --verbose=2 "$binary"
done

mkdir -m 0755 "$work/notarization"
cp "$work/final-binaries/tele_darwin_amd64" "$work/notarization/tele_darwin_amd64"
cp "$work/final-binaries/tele_darwin_arm64" "$work/notarization/tele_darwin_arm64"
ditto --norsrc -c -k "$work/notarization" "$work/tele-notarization.zip"

echo 'submitting exact signed binaries to Apple'
asc notarization submit --file "$work/tele-notarization.zip" --wait --timeout 1h --output json > "$output/notarization.json"
submission_id=$(jq -er '.data.id' "$output/notarization.json")
[[ $(jq -er '.data.attributes.status' "$output/notarization.json") == Accepted ]]
asc notarization log --id "$submission_id" --output json > "$work/notarization-log-url.json"
log_url=$(jq -er '.data.attributes.developerLogUrl' "$work/notarization-log-url.json")
curl --fail --silent --show-error "$log_url" > "$output/notarization-log.json"
jq -e '.status == "Accepted" and (.issues | length) == 0' "$output/notarization-log.json" >/dev/null

echo 'verifying Apple ticket against each exact binary'
for arch in amd64 arm64; do
  verify_signed_binary "$work/final-binaries/tele_darwin_$arch"
done

echo 'packaging final signed bytes'
go run ./scripts/release --version "$version" --commit "$commit" \
  --output "$output" --binary-input "$work/final-binaries"
cmp "$output/tele_${version}_linux_amd64.tar.gz" "$work/unsigned-archives-a/tele_${version}_linux_amd64.tar.gz"
cmp "$output/tele_${version}_linux_arm64.tar.gz" "$work/unsigned-archives-a/tele_${version}_linux_arm64.tar.gz"

(
  cd "$work/final-binaries"
  shasum -a 256 tele_* | sort > "$output/final-binary-checksums.txt"
)
jq -n \
  --arg schema 'tele-release-v1' \
  --arg version "$version" \
  --arg commit "$commit" \
  --arg submission_id "$submission_id" \
  --arg identifier "$SIGNING_IDENTIFIER" \
  --arg team_id "$TEAM_ID" \
  --arg requirement "$REQUIREMENT" \
  '{schema:$schema,version:$version,commit:$commit,notarization_submission_id:$submission_id,signing:{identifier:$identifier,team_id:$team_id,requirement:$requirement},claims:{unsigned_inputs_reproducible:true,darwin_final_bytes_signed:true,darwin_final_bytes_notarized:true,linux_final_bytes_match_reproducible_inputs:true}}' \
  > "$output/release-manifest.json"

echo "trusted release candidate ready: $output"
