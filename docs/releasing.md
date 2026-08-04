# Release runbook

Tele separates reproducible unsigned inputs from final macOS distribution
bytes. CI never receives Apple credentials. A trusted Mac builds the exact
source twice, signs and notarizes both Darwin binaries, and uploads a closed
draft release. GitHub independently reproduces the unsigned inputs, validates
the final Linux and Darwin bytes, attests every draft asset, and publishes only
if the asset set has not changed.

Secure timestamps make independently signed binaries nondeterministic. The
unsigned checksums are reproducibility evidence; `checksums.txt`, GitHub
attestations, and Homebrew cover the final signed archives.

## Prepare

1. Review the intended diff and dependency changes.
2. Update `internal/buildinfo.Version` without the `v` prefix.
3. Add `docs/releases/<version>.md` and update public documentation and the
   Obsidian `projects/tele` checkpoint.
4. Run `just gate` and the permitted bounded live smokes.
5. Commit and push `main`.
6. Wait for all jobs in `.github/workflows/ci.yml` to pass for that exact SHA.
7. Tag that SHA and push the tag.

## Build, sign, and notarize on the trusted Mac

The Mac must have the Developer ID Application identity in Keychain and an
authenticated default `asc` team profile. The script never reads a `.p12` or
places signing credentials in repository or GitHub secrets.

```sh
version="$(awk -F'"' '/^[[:space:]]*Version = / {print $2}' internal/buildinfo/buildinfo.go)"
commit="$(git rev-parse HEAD)"
git tag "v$version"
git push origin main "v$version"
gh run list --workflow ci.yml --commit "$commit" --limit 1

candidate="$(mktemp -d "/private/tmp/tele-$version-candidate.XXXXXX")/dist"
scripts/release-macos.sh "$version" "$commit" "$candidate"
```

`release-macos.sh` fails unless the checkout is clean and exact. It:

1. builds all four targets twice and compares both raw binaries and archives;
2. signs the two thin Darwin binaries with identifier `com.ardasevinc.tele`,
   hardened runtime, and a secure timestamp;
3. verifies the Developer ID chain, Team ID `J3S8HNBXSU`, pinned designated
   requirement, and official-build marker;
4. submits both exact binaries together through `asc notarization submit
   --wait`, requires `Accepted` with zero issues, and checks each online ticket;
5. packages only the final signed bytes, proves Linux archives still match the
   reproducible inputs, and emits public receipts and checksums.

A raw CLI ZIP cannot carry a stapled ticket. Tele relies on Apple's online
ticket for connected distribution; the physical acceptance matrix also tests a
naturally quarantined download through Gatekeeper.

## Upload the closed draft and finalize

Inspect the candidate directory before upload, then:

```sh
scripts/publish-release-candidate.sh "$version" "$candidate"
```

The publisher requires the local tag, remote tag, `origin/main`, and `HEAD` to
match. It creates one draft release, uploads the exact closed asset set,
downloads it again, compares the whole-set digest, and dispatches `release.yml`
at the tag.

The credential-free GitHub finalizer independently:

- reproduces unsigned binaries twice and matches `unsigned-checksums.txt`;
- requires final Linux archives to be byte-identical to the reproducible build;
- checks the Developer ID signature, runtime, timestamp, pinned requirement,
  online notarization ticket, exact final-binary hashes, and execution of the
  runner's native Darwin archive;
- requires both runners and the publication job to download the same complete
  artifact-set digest;
- attests every final asset, compares GitHub's server-side asset digests, and
  only then publishes the unchanged draft.

Do not edit, replace, or add draft assets. If the finalizer fails, preserve the
receipts, delete the draft only as a separate explicit recovery action, and
start again from a new clean candidate.

## Verify

```sh
gh release view "v$version" --json url,isDraft,isPrerelease,isImmutable,assets
gh release download "v$version" --pattern checksums.txt --pattern 'tele_*.tar.gz'
gh attestation verify checksums.txt --repo ardasevinc/tele
gh attestation verify "tele_${version}_darwin_arm64.tar.gz" --repo ardasevinc/tele
tar -xzf "tele_${version}_darwin_arm64.tar.gz"
codesign --verify --strict --check-notarization -R=notarized --verbose=2 tele
tele update --check --json
```

Update Homebrew only from the immutable published `checksums.txt`. Verify the
Cellar executable hash equals the executable inside the published archive and
repeat the signature, requirement, and notarization checks on that installed
path. Also verify Linux amd64/arm64 packages, a pinned proxy-only `go install`,
and all repository/vault/public surfaces before closure.

On macOS, `go install` builds are deliberately update-check-only because they
cannot satisfy the official Developer ID policy. Homebrew owns automatic
official upgrades. Linux Go installations retain the pinned rollback-safe
updater.

## Rollback

GitHub-enforced release immutability applies to releases published after it was
enabled on 2026-07-15. Treat every published release as append-only evidence. If
a release is bad, document the defect, mark it clearly in GitHub, fix forward
with a new version, and update the tap. Do not retag or silently replace assets.
