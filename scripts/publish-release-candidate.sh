#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo 'usage: scripts/publish-release-candidate.sh VERSION ARTIFACT_DIRECTORY' >&2
  exit 2
}

[[ $# -eq 2 ]] || usage
version=${1#v}
artifacts=$2
tag="v$version"

[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || usage
[[ -d $artifacts && ! -L $artifacts ]] || { echo 'artifact directory must be a real directory' >&2; exit 1; }
artifacts=$(cd "$artifacts" && pwd)

for command in gh git jq shasum; do
  command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
done

commit=$(git rev-parse HEAD)
[[ -z $(git status --porcelain=v1 --untracked-files=all) ]] || { echo 'publishing requires a clean worktree' >&2; exit 1; }
[[ $(git rev-parse "$tag^{commit}") == "$commit" ]] || { echo "$tag must point at HEAD" >&2; exit 1; }
git fetch origin main "$tag"
[[ $(git rev-parse origin/main) == "$commit" ]] || { echo 'origin/main must equal HEAD' >&2; exit 1; }
remote_tag=$(git ls-remote origin "refs/tags/$tag^{}" | awk 'NR == 1 {print $1}')
if [[ -z $remote_tag ]]; then
  remote_tag=$(git ls-remote origin "refs/tags/$tag" | awk 'NR == 1 {print $1}')
fi
[[ $remote_tag == "$commit" ]] || { echo "origin tag $tag must equal HEAD" >&2; exit 1; }

expected=$(mktemp "${TMPDIR:-/tmp}/tele-release-assets.XXXXXX")
actual=$(mktemp "${TMPDIR:-/tmp}/tele-release-assets.XXXXXX")
trap 'trash "$expected" "$actual" >/dev/null 2>&1 || true' EXIT
printf '%s\n' \
  checksums.txt final-binary-checksums.txt notarization-log.json notarization.json release-manifest.json unsigned-checksums.txt \
  "tele_${version}_darwin_amd64.tar.gz" "tele_${version}_darwin_arm64.tar.gz" \
  "tele_${version}_linux_amd64.tar.gz" "tele_${version}_linux_arm64.tar.gz" | sort > "$expected"
find "$artifacts" -maxdepth 1 -type f -exec basename {} \; | sort > "$actual"
diff -u "$expected" "$actual"
(cd "$artifacts" && shasum -a 256 --check checksums.txt)
jq -e --arg version "$version" --arg commit "$commit" \
  '.schema == "tele-release-v1" and .version == $version and .commit == $commit' \
  "$artifacts/release-manifest.json" >/dev/null

if gh release view "$tag" --repo ardasevinc/tele >/dev/null 2>&1; then
  echo "release already exists: $tag" >&2
  exit 1
fi

notes="docs/releases/$version.md"
[[ -f $notes ]] || { echo "missing authored release notes: $notes" >&2; exit 1; }
if [[ $version == *-* ]]; then prerelease=(--prerelease); else prerelease=(); fi
gh release create "$tag" --repo ardasevinc/tele --verify-tag --draft \
  --title "Tele $version" --notes-file "$notes" "${prerelease[@]}" "$artifacts"/*

remote=$(mktemp -d "${TMPDIR:-/tmp}/tele-release-remote.XXXXXX")
trap 'trash "$expected" "$actual" "$remote" >/dev/null 2>&1 || true' EXIT
gh release download "$tag" --repo ardasevinc/tele --dir "$remote"
local_digest=$(cd "$artifacts" && shasum -a 256 ./* | sort | shasum -a 256 | awk '{print $1}')
remote_digest=$(cd "$remote" && shasum -a 256 ./* | sort | shasum -a 256 | awk '{print $1}')
[[ $local_digest == "$remote_digest" ]] || { echo 'uploaded draft bytes differ from the trusted candidate' >&2; exit 1; }

gh workflow run release.yml --repo ardasevinc/tele --ref "$tag" -f "tag=$tag"
echo "draft $tag uploaded byte-for-byte; release finalizer dispatched"
