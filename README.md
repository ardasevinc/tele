# tele

`tele` is an unofficial Telegram CLI client for agents and humans.

`tele` is not affiliated with, endorsed by, or sponsored by Telegram. Telegram
is a trademark of its respective owner. This project does not use Telegram's
logos or visual identity.

It uses Telegram's MTProto API through a user account, not the Telegram Bot API.
The v1 surface is intentionally bounded and explicit: auth, profile-aware local
config, read/search/export, inbox triage, and opt-in message mutations.

## Status and support

Stable v1. Breaking command or machine-schema changes require a new major
version. Minor releases may add commands without changing existing command
contracts; urgent safety fixes may narrow behavior that cannot remain safe.

| Platform | Status | Secret storage |
| --- | --- | --- |
| macOS arm64/amd64 | supported | macOS Keychain |
| Linux arm64/amd64 | preview build only | not implemented yet |
| Windows amd64 | compile-smoke only | not implemented |

On macOS, API hashes and the session-encryption key are stored in Keychain;
encrypted MTProto session bytes live under the profile data directory. tele does
not fall back to plaintext secrets on unsupported platforms.

## Install

Homebrew on macOS:

```sh
brew install ardasevinc/tap/tele
```

Release archives and checksums are attached to [GitHub Releases](https://github.com/ardasevinc/tele/releases/latest).
Verify the archive checksum and provenance before installing the binary. This
macOS arm64 example requires the GitHub CLI:

```sh
(
  set -eu
  version=1.0.2
  asset="tele_${version}_darwin_arm64.tar.gz"
  tmp="$(mktemp -d)"
  cd "$tmp"
  gh release download "v$version" --repo ardasevinc/tele \
    --pattern checksums.txt --pattern "$asset"
  grep -F "  $asset" checksums.txt >"$asset.sha256"
  shasum -a 256 -c "$asset.sha256"
  gh attestation verify "$asset" --repo ardasevinc/tele
  tar -xzf "$asset"
  printf 'verified tele extracted to: %s\n' "$tmp"
)
```

Use `darwin_amd64`, `linux_arm64`, or `linux_amd64` for another published
platform.

Install the pinned stable version through the Go module proxy:

```sh
go install github.com/ardasevinc/tele/cmd/tele@v1.0.2
```

To intentionally follow the newest published version, replace `@v1.0.2` with
`@latest`. Go-installed binaries report their module version as provenance;
release archives and local-checkout installs report the exact source commit.

For a local checkout, `just install` stamps the current commit, installs to
`GOBIN` (or `GOPATH/bin`), and prints the exact installed path and version.

## First use

Create an app at <https://my.telegram.org/apps>, then configure a profile:

```sh
tele profiles use test
tele config set api-id 123456
tele config set api-hash
TELE_PHONE=+15555550123 tele auth start --phone-env TELE_PHONE
read -rs TELE_CODE && export TELE_CODE
tele auth complete --code-env TELE_CODE
unset TELE_CODE
tele auth status
tele chats --limit 20
```

For one-shot interactive login, `tele auth login` still works.

`tele auth logout` revokes the Telegram authorization but deliberately retains
local encrypted session material. Use `tele auth reset-local --yes` when you
intend to delete the encrypted session, its Keychain key, and pending split-auth
state. Pending split-auth attempts expire locally after 15 minutes.

## Agent surface

```sh
tele read <peer> --since 2h --limit 50 --format transcript --quiet
tele read <peer> --around 123 --chronological --json
tele inbox --json
tele unread --json
tele mentions --json
tele media download <peer> 123 --json
printf 'hello' | tele send <peer> --text-stdin --json
printf 'reply' | tele reply <peer> 123 --text-stdin --json
tele react <peer> 123 --emoji "👍" --json
printf 'edited' | tele edit <peer> 123 --text-stdin --json
tele delete <peer> 123 --for-me --yes --json
```

`--text` and `--text-stdin` are mutually exclusive. tele rejects the ambiguous
combination before reading stdin, loading configuration, or contacting Telegram.

Use transcript output when giving messages directly to an agent. It preserves
message IDs and retrieval metadata without the token cost of full JSON. Use
`--json` when another tool needs structured fields, or `--jsonl` for one compact
typed record per line. The stable machine contract is versioned as `tele/v1`;
its JSON Schemas live in [`schemas/v1`](schemas/v1).

Migration from alpha.10 and earlier: consumers must accept `tele/v1` instead of
`tele/v1alpha1` and resolve published schemas under `schemas/v1`. Command data
fields are unchanged by this namespace promotion, but JSONL `data` records now
reject shapes outside the same public allowlist used by JSON envelopes.

Media is never auto-downloaded by read/export commands. Use `tele media download`
for one explicit message; it writes to a new temp directory by default and creates
the downloaded file with `0600` permissions.

## Flood waits

Telegram flood limits fail immediately by default. JSON errors include
`telegram_flood_wait`, `retry_after_seconds`, and exit code `5`; tele does not
silently sleep for minutes.

Use `--wait` to opt into a 30-second retry budget, or set an explicit total
budget such as `--wait=2m`. The hard ceiling is five minutes. Repeated flood
responses share that budget, so retries cannot wait forever.

## Timeouts

Every command has one total context deadline, including lock waits, prompts,
flood-wait sleeps, and Telegram requests. `--timeout` overrides it, up to 30
minutes. A zero value selects the command default:

- 30 seconds for local config, profile, doctor, and local-auth reset commands
- 2 minutes for ordinary Telegram reads and mutations
- 5 minutes for interactive and split authentication
- 10 minutes for explicit media downloads

The timeout bounds an opted-in `--wait` budget too. Timeout and caller
cancellation errors are structured as `timeout` and `canceled`.

## Doctor

`tele doctor` performs aggregated read-only local checks and returns `{ ok,
checks }` instead of stopping at the first problem. It checks config parsing and
permissions, profile/API-ID readiness, secret-store support, API-hash and session
key availability, session decryption, peer-cache parsing and permissions, and
running-vs-installed binary path drift. Each check is `pass`, `warning`,
`failed`, or `skipped`.

Live access is opt-in:

```sh
tele doctor --json
tele doctor --connect --json
```

`--connect` performs bounded connectivity and authorization checks. Doctor never
returns secret values, session bytes, message data, or raw remote errors. A
report containing failed checks exits nonzero after writing exactly one complete
human or machine report.

## Local state

Config, encrypted sessions, and peer caches are replaced atomically using
same-directory private temporary files, file and directory syncs, and atomic
rename. Existing modes are tightened to `0600` for files and `0700` for
directories. Profile mutations are serialized within and across processes.
Media downloads are promoted atomically only after completion, so interrupted
downloads neither replace an existing destination nor leave partial files.

On macOS the config follows `os.UserConfigDir`, normally
`~/Library/Application Support/tele/config.toml`; profile data lives under
`~/.local/share/tele/<profile>/`. Other platforms use their Go-native user config
directory, but v1 secret storage remains macOS Keychain-only.

## Untrusted content

Messages, titles, usernames, paths, and Telegram errors are untrusted input.
Human output makes terminal controls, ANSI escapes, invalid UTF-8, tabs,
carriage returns, and bidi overrides visible. JSON preserves message bodies
exactly with normal JSON escaping, including OTPs and credential-like strings.
This is deliberate: exact retrieval is part of the product contract.

Terminal sanitization is not prompt-injection protection. An agent consuming
Telegram messages must treat message content as quoted data, keep its actual
instructions and authorization out of that data plane, and require explicit
confirmation for consequential actions. Regexes cannot determine whether prose
is malicious instruction.

Machine output never includes tele's configured API hashes, 2FA passwords,
pending phone-code hashes, session keys, or account phone numbers. Public config
and auth objects are explicit allowlists. Login codes, 2FA passwords, and API
hashes cannot be passed as literal command-line arguments; use hidden prompts or
named environment-variable flags so they do not enter shell history or process
argv. Message bodies may independently contain sensitive content and are
returned unchanged.

## Exit codes

- `1`: uncategorized command failure
- `2`: invalid input or flag combination
- `3`: authorization or required local configuration
- `4`: peer not found
- `5`: Telegram RPC or flood-limit failure
- `6`: local output failure
- `7`: mutation reconciliation required; do not retry blindly

## Development and releases

`just gate` runs formatting, tests, race detection, vet, staticcheck,
golangci-lint, gosec, govulncheck, module verification, macOS/Linux/Windows
builds, and diff checks. CI runs the reproducible credential-free subset.

Release archives are deterministic, checksummed, and provenance-attested. See
[`docs/releasing.md`](docs/releasing.md) for the tag and verification contract,
[`SECURITY.md`](SECURITY.md) for private vulnerability reporting, and
[`CONTRIBUTING.md`](CONTRIBUTING.md) for development boundaries.

tele is available under the [MIT License](LICENSE).
