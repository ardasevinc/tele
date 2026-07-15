# Release runbook

Releases are built from tags by `.github/workflows/release.yml`. The workflow
validates that the source version equals the tag, the tagged commit is on
`main`, CI passed for that exact commit, two clean builds are byte-identical,
checksums verify, and the Linux binary reports the tagged version and commit.
It then attests every artifact and publishes only after the uploaded asset set
matches the local set.

## Prepare

1. Review the intended diff and dependency changes.
2. Update `internal/buildinfo.Version` without the `v` prefix.
3. Update public documentation and the Obsidian `projects/tele` checkpoint.
4. Run `just gate` and the permitted bounded live smokes.
5. Commit and push `main`.
6. Wait for all jobs in `.github/workflows/ci.yml` to pass for that exact SHA.

## Publish

```sh
version="$(awk -F'"' '/^[[:space:]]*Version = / {print $2}' internal/buildinfo/buildinfo.go)"
commit="$(git rev-parse HEAD)"
test "$(git rev-parse origin/main)" = "$commit"
gh run list --workflow ci.yml --commit "$commit" --limit 1
git tag "v$version"
git push origin "v$version"
gh run watch --exit-status
```

Do not create the GitHub release manually. The tag workflow owns archives,
checksums, attestations, prerelease classification, and publication.

## Verify

```sh
gh release view "v$version" --json url,isDraft,isPrerelease,assets
gh release download "v$version" --pattern checksums.txt --pattern 'tele_*.tar.gz'
gh attestation verify "tele_${version}_darwin_arm64.tar.gz" --repo ardasevinc/tele
```

Verify Homebrew only after the release assets exist and the tap formula has the
new SHA-256 values. Do not publish a formula that points at mutable or missing
assets.

## Rollback

Published release assets are immutable evidence. If a release is bad, document
the defect, mark it clearly in GitHub, fix forward with a new version, and update
the tap. Do not retag or silently replace assets.
