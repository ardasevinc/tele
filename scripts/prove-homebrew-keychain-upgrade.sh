#!/usr/bin/env bash
set -euo pipefail

readonly SIGNING_IDENTITY='Developer ID Application: ARDA SEVINC (J3S8HNBXSU)'
readonly SIGNING_IDENTIFIER='com.ardasevinc.tele'
readonly REQUIREMENT='identifier "com.ardasevinc.tele" and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] /* exists */ and certificate leaf[field.1.2.840.113635.100.6.1.13] /* exists */ and certificate leaf[subject.OU] = J3S8HNBXSU'
readonly PROOF_TAP='tele/keychain-upgrade-proof'
readonly PROOF_FORMULA="$PROOF_TAP/tele-keychain-upgrade-proof"

usage() {
  echo 'usage: scripts/prove-homebrew-keychain-upgrade.sh EVIDENCE_DIRECTORY SOURCE_CONFIG SOURCE_PROFILE SOURCE_VAULT SOURCE_INSTANCE PASSPHRASE_FILE SOURCE_SESSION' >&2
  exit 2
}

[[ $# -eq 7 ]] || usage
evidence=$1
source_config=$2
source_profile=$3
source_vault=$4
source_instance=$5
passphrase_file=$6
source_session=$7

[[ $(uname -s) == Darwin ]] || { echo 'Homebrew Keychain upgrade proof requires Darwin' >&2; exit 1; }
[[ ! -e $evidence ]] || { echo "evidence path already exists: $evidence" >&2; exit 1; }
[[ -f $source_config && -f $source_vault && -f $passphrase_file && -f $source_session ]] || { echo 'source proof inputs are incomplete' >&2; exit 1; }
[[ $(stat -f '%Lp' "$passphrase_file") == 600 ]] || { echo 'passphrase file must have mode 0600' >&2; exit 1; }
for command in brew codesign git go jq security shasum tar uuidgen; do
  command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
done
security find-identity -v -p codesigning | grep -Fq "\"$SIGNING_IDENTITY\"" || { echo 'Developer ID identity is unavailable' >&2; exit 1; }
if brew tap | grep -Fxq "$PROOF_TAP"; then
  echo "temporary proof tap already exists: $PROOF_TAP" >&2
  exit 1
fi

evidence_parent=$(cd "$(dirname "$evidence")" && pwd)
evidence="$evidence_parent/$(basename "$evidence")"
work=$(mktemp -d "${TMPDIR:-/tmp}/tele-homebrew-upgrade.XXXXXX")
target_data="$HOME/.local/share/tele"
target_profile="homebrew-proof-$(uuidgen | tr '[:upper:]' '[:lower:]')"
target_config="$work/config.toml"
target_instance=''
tap_created=false
formula_installed=false
cleanup_required=true

cleanup() {
  if [[ $cleanup_required == true && -n $target_instance && -x $evidence/tele-b ]]; then
    trash "$target_config" >/dev/null 2>&1 || true
    "$evidence/tele-b" --json --config "$target_config" --profile "$target_profile" \
      secrets purge --backend keychain-v1 "$target_instance" --confirm-instance "$target_instance" \
      > "$evidence/purge.json" 2> "$evidence/purge.err" || true
  fi
  trash "$target_data/$target_profile" >/dev/null 2>&1 || true
  if [[ $formula_installed == true ]]; then
    brew uninstall --force "$PROOF_FORMULA" >/dev/null 2>&1 || true
  fi
  if [[ $tap_created == true ]]; then
    brew untap --force "$PROOF_TAP" >/dev/null 2>&1 || true
  fi
  trash "$work" >/dev/null 2>&1 || true
}
trap cleanup EXIT
mkdir -m 0700 "$evidence" "$work/a" "$work/b"

head_commit=$(git rev-parse HEAD)
short_commit=$(git rev-parse --short=12 HEAD)
ldflags_base='-s -w -buildid='
build_fixture() {
  local output=$1 version=$2 commit=$3 package=$4
  CGO_ENABLED=0 GOOS=darwin GOARCH="$(go env GOARCH)" go build -trimpath -buildvcs=false \
    -ldflags "$ldflags_base -X github.com/ardasevinc/tele/internal/buildinfo.Version=$version -X github.com/ardasevinc/tele/internal/buildinfo.Commit=$commit" \
    -o "$output" "$package"
  chmod 0755 "$output"
  codesign --force --sign "$SIGNING_IDENTITY" --identifier "$SIGNING_IDENTIFIER" --options runtime --timestamp "$output"
  codesign --verify --strict -R="$REQUIREMENT" --verbose=2 "$output"
}

build_fixture "$evidence/tele-a" '1.2.2-homebrew-a' "${short_commit}a" ./cmd/tele
build_fixture "$evidence/tele-b" '1.2.2-homebrew-b' "${short_commit}b" ./cmd/tele
build_fixture "$work/live-clone" '1.2.2-homebrew-seed' "${short_commit}s" ./internal/secrets/testdata/keychain-live-clone-fixture
cp "$evidence/tele-a" "$work/a/tele"
cp "$evidence/tele-b" "$work/b/tele"
tar -czf "$work/tele-a.tar.gz" -C "$work/a" tele
tar -czf "$work/tele-b.tar.gz" -C "$work/b" tele
sha_a=$(shasum -a 256 "$work/tele-a.tar.gz" | awk '{print $1}')
sha_b=$(shasum -a 256 "$work/tele-b.tar.gz" | awk '{print $1}')

brew tap-new "$PROOF_TAP" >/dev/null
tap_created=true
tap_repo=$(brew --repo "$PROOF_TAP")
formula_path="$tap_repo/Formula/tele-keychain-upgrade-proof.rb"
render_formula() {
  local version=$1 archive=$2 checksum=$3
  cat > "$formula_path" <<RUBY
class TeleKeychainUpgradeProof < Formula
  desc "Disposable physical Tele Keychain upgrade proof"
  homepage "https://github.com/ardasevinc/tele"
  url "file://$archive"
  version "$version"
  sha256 "$checksum"
  keg_only "physical upgrade proof"

  def install
    bin.install "tele" => "tele-keychain-upgrade-proof"
  end

  test do
    assert_equal "tele-official-build-v1", shell_output("#{bin}/tele-keychain-upgrade-proof internal official-build").strip
  end
end
RUBY
}

render_formula '1.2.2-a' "$work/tele-a.tar.gz" "$sha_a"
brew install "$PROOF_FORMULA" > "$evidence/brew-install.txt"
formula_installed=true
prefix_a=$(brew --prefix "$PROOF_FORMULA")
binary_a="$prefix_a/bin/tele-keychain-upgrade-proof"
[[ $prefix_a == */Cellar/tele-keychain-upgrade-proof/1.2.2-a ]]
cmp "$binary_a" "$evidence/tele-a"
"$binary_a" internal official-build > "$evidence/a-official.txt"

"$binary_a" --json --config "$target_config" --profile "$target_profile" \
  secrets init --backend keychain-v1 > "$evidence/init.json"
target_instance=$(jq -er '.data.instance' "$evidence/init.json")
api_id=$(/opt/homebrew/bin/tele --json --config "$source_config" --profile "$source_profile" config get api-id | jq -er '.data.api_id')
"$binary_a" --json --config "$target_config" --profile "$target_profile" config set api-id "$api_id" > "$evidence/configure.json"
"$work/live-clone" "$source_vault" "$source_profile" "$source_instance" "$passphrase_file" \
  "$source_session" "$target_data" "$target_profile" "$target_instance" > "$evidence/seed.txt"

render_formula '1.2.2-b' "$work/tele-b.tar.gz" "$sha_b"
brew upgrade "$PROOF_FORMULA" > "$evidence/brew-upgrade.txt"
prefix_b=$(brew --prefix "$PROOF_FORMULA")
binary_b="$prefix_b/bin/tele-keychain-upgrade-proof"
[[ $prefix_b == */Cellar/tele-keychain-upgrade-proof/1.2.2-b && $prefix_a != "$prefix_b" ]]
cmp "$binary_b" "$evidence/tele-b"
"$binary_b" internal official-build > "$evidence/b-official.txt"
"$binary_b" --json --read-only --timeout 30s --config "$target_config" --profile "$target_profile" doctor --connect > "$evidence/doctor.json"
jq -e '.ok == true and .data.ok == true' "$evidence/doctor.json" >/dev/null
"$binary_b" --json --read-only --timeout 30s --config "$target_config" --profile "$target_profile" me > "$work/me.json"
jq -e '.ok == true' "$work/me.json" >/dev/null
jq '{ok,command,profile}' "$work/me.json" > "$evidence/read.json"

trash "$target_config"
"$binary_b" --json --config "$target_config" --profile "$target_profile" \
  secrets purge --backend keychain-v1 "$target_instance" --confirm-instance "$target_instance" \
  > "$evidence/purge.json" 2> "$evidence/purge.err"
cleanup_required=false
trash "$target_data/$target_profile" >/dev/null 2>&1 || true

remaining=$(security dump-keychain 2>/dev/null | sed -n "s/.*\"acct\"<blob>=\"\(v1:$target_profile:$target_instance:[^\"]*\)\".*/\1/p" | wc -l | tr -d ' ')
[[ $remaining == 0 ]]
hash_a=$(shasum -a 256 "$evidence/tele-a" | awk '{print $1}')
hash_b=$(shasum -a 256 "$evidence/tele-b" | awk '{print $1}')
cdhash_a=$(codesign -dvvv "$evidence/tele-a" 2>&1 | awk -F= '/^CDHash=/{print $2}')
cdhash_b=$(codesign -dvvv "$evidence/tele-b" 2>&1 | awk -F= '/^CDHash=/{print $2}')
[[ $hash_a != "$hash_b" && $cdhash_a != "$cdhash_b" ]]
jq -n \
  --arg schema tele-homebrew-keychain-upgrade-proof-v1 --arg commit "$head_commit" \
  --arg profile "$target_profile" --arg instance "$target_instance" \
  --arg prefix_a "$prefix_a" --arg prefix_b "$prefix_b" \
  --arg hash_a "$hash_a" --arg hash_b "$hash_b" --arg cdhash_a "$cdhash_a" --arg cdhash_b "$cdhash_b" \
  --arg requirement "$REQUIREMENT" \
  '{schema:$schema,commit:$commit,profile:$profile,instance:$instance,cellar:{a:$prefix_a,b:$prefix_b},binaries:{a:{sha256:$hash_a,cdhash:$cdhash_a},b:{sha256:$hash_b,cdhash:$cdhash_b}},requirement:$requirement,claims:{distinct_cellar_paths:true,installed_bytes_exact:true,a_created_b_read_without_ui:true,b_doctor_connect_passed:true,b_normal_read_passed:true,b_purged_without_ui:true,no_keychain_items_remain:true}}' \
  > "$evidence/receipt.json"
shasum -a 256 "$evidence"/* > "$evidence/SHA256SUMS.generated"

echo "physical Homebrew Keychain upgrade proof complete: $evidence"
