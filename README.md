# tele

`tele` is an unofficial Telegram CLI client for agents and humans.

`tele` is not affiliated with, endorsed by, or sponsored by Telegram. Telegram
is a trademark of its respective owner. This project does not use Telegram's
logos or visual identity.

It primarily uses Telegram's MTProto API through a user account. The managed-bot
factory additionally uses the official Bot API through an explicitly configured
manager bot. The v1 surface is intentionally bounded and explicit: auth,
profile-aware local config, read/search/export, inbox triage, opt-in message
mutations, and managed-bot creation and token custody.

## Status and support

Stable v1. Breaking command or machine-schema changes require a new major
version. Minor releases may add commands without changing existing command
contracts; urgent safety fixes may narrow behavior that cannot remain safe.

| Platform | Status | Secret storage |
| --- | --- | --- |
| macOS arm64/amd64 | supported | official Developer ID release: native Keychain or portable vault; other builds: portable vault |
| Linux arm64/amd64 | supported | Secret Service or portable vault |
| Windows amd64 | compile-smoke only | not implemented |

API hashes, session-encryption keys, manager-bot credentials, and managed
child-bot tokens are stored in the selected profile backend. Encrypted MTProto
session bytes live under the profile data directory. Backend choice is explicit
and sticky; tele never silently falls back to plaintext or another backend.

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
  version=1.2.1
  asset="tele_${version}_darwin_arm64.tar.gz"
  tmp="$(mktemp -d)"
  cd "$tmp"
  gh release download "v$version" --repo ardasevinc/tele \
    --pattern checksums.txt --pattern "$asset"
  gh attestation verify checksums.txt --repo ardasevinc/tele
  gh attestation verify "$asset" --repo ardasevinc/tele
  grep -F "  $asset" checksums.txt >"$asset.sha256"
  shasum -a 256 -c "$asset.sha256"
  tar -xzf "$asset"
  printf 'verified tele extracted to: %s\n' "$tmp"
)
```

Use `darwin_amd64`, `linux_arm64`, or `linux_amd64` for another published
platform.

Install the pinned stable version through the Go module proxy:

```sh
go install github.com/ardasevinc/tele/cmd/tele@v1.2.1
```

To intentionally follow the newest published version, replace `@v1.2.1` with
`@latest`. Go-installed binaries report their module version as provenance;
release archives and local-checkout installs report the exact source commit.

For a local checkout, `just install` stamps the current commit, installs to
`GOBIN` (or `GOPATH/bin`), and prints the exact installed path and version.
Go-installed and local-checkout binaries are not Developer ID-signed official
macOS builds, so they use `vault-v1` and cannot open or create production
`keychain-v1` state.

## First use

Create an app at <https://my.telegram.org/apps>, then configure a profile:

```sh
tele --profile test config set api-id 123456
tele --profile test secrets init --backend vault-v1
tele profiles use test
tele --profile test config set api-hash
TELE_PHONE=+15555550123 tele --profile test auth start --phone-env TELE_PHONE
read -rs TELE_CODE && export TELE_CODE
tele --profile test auth complete --code-env TELE_CODE
unset TELE_CODE
tele --profile test auth status
tele --profile test chats --limit 20
```

The portable `vault-v1` backend works on every supported macOS and Linux
target. Interactive use prompts twice on the controlling TTY. Automation must
provide a protected regular file with `--vault-passphrase-file` or an inherited
descriptor numbered 3 or higher with `--vault-passphrase-fd`; passphrases are
never accepted through argv or environment variables.

Linux desktops with a running, unlocked Secret Service provider may instead
initialize `secret-service-v1`. Official Developer ID-signed macOS release
builds may choose `keychain-v1`; Tele verifies its own pinned identifier, Team
ID, Developer ID chain, and signing requirement through Security.framework
before creating or opening that backend. Ad-hoc, source, and `go install`
builds must use `vault-v1`. If a provider is missing, headless, locked, or the
running build is not eligible, tele returns a typed backend error rather than
switching storage behind your back.

For one-shot interactive login, `tele auth login` still works.

`tele auth logout` revokes the Telegram authorization but deliberately retains
local encrypted session material. Use `tele auth reset-local --yes` when you
intend to delete the encrypted session, its Keychain key, and pending split-auth
state. Pending split-auth attempts expire locally after 15 minutes.

## Managed bot factory

Telegram's Bot Management Mode lets a dedicated manager bot control bots that
remain owned by the authenticated user. tele uses the user session to check
usernames and create bots, then uses the manager bot only to retrieve or replace
managed child tokens.

Enable Bot Management Mode for a dedicated bot in `@BotFather`, then configure
its token without placing it in argv:

```sh
tele bots manager configure @ManagerBot
# or, for automation:
printf '%s\n' "$TELE_MANAGER_TOKEN" |
  tele bots manager configure @ManagerBot --token-stdin
tele bots manager status
```

Create and inspect a managed bot:

```sh
tele bots username check ExampleWorkerBot
tele bots create ExampleWorkerBot --name "Example Worker"
tele bots reconcile
tele bots reconcile --import @ExampleWorkerBot
tele bots list
tele bots inspect @ExampleWorkerBot
```

`bots reconcile` reads Telegram's owned-bot catalog and compares it with local
state. Remote-only bots are proposed, never silently added. Repeat `--import`
to accept specific bots controlled by the configured manager; their current
tokens are retrieved without rotation. Local-only entries become tombstones,
not deletions. Bots controlled by another manager remain discoverable but are
not assigned fabricated manager or token state. Telegram exposes no matching
remote deletion operation here, so Tele does not claim one.

`bots list` reads the reconciled local inventory. It lives at
the profile's data path, normally
`$XDG_DATA_HOME/tele/<profile>/bots.json` on Linux and
`~/.local/share/tele/<profile>/bots.json` on macOS. It uses private atomic
writes and contains bot identity and reconciliation metadata but no tokens.

Synchronize the current remote token non-destructively, or explicitly rotate it:

```sh
tele bots token sync @ExampleWorkerBot
tele bots token rotate @ExampleWorkerBot --yes
```

Ordinary output never returns manager or child tokens. Creation stores a durable
inventory receipt before requesting the child token, then escrows the token in
the selected secret backend. Post-dispatch ambiguity and failures after confirmed creation or
rotation exit with code `7` and a reconciliation handle. Do not retry those
operations blindly; use inventory inspection or non-destructive token sync
first.

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
key availability, vault structure and catalog authority, retained migration
receipts, session decryption, peer-cache parsing and permissions, and
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
`~/.local/share/tele/<profile>/`. Linux follows the XDG Base Directory contract:
config under `$XDG_CONFIG_HOME/tele` and profile data under
`$XDG_DATA_HOME/tele`, with the standard `~/.config` and `~/.local/share`
defaults. Existing legacy Linux state is preserved when unambiguous; conflicting
legacy and XDG copies are rejected for explicit reconciliation.

## Updates

`tele update --check` queries GitHub's latest immutable stable release and
reports the running version, source provenance, resolved executable, detected
install manager, and an exact recommendation. Add `--json` for the stable
machine result. This command is read-only.

`tele update --yes` mutates only an unambiguous, writable Go installation and
pins the exact release tag. It retains a same-directory rollback executable,
requires the candidate to parse the current config and selected vault format,
verifies its exact module version, and restores the prior executable if either
check fails. Homebrew installations receive `brew upgrade tele`. Standalone
archives remain check-only until tele can natively verify GitHub attestations,
so the reported manual command verifies both `checksums.txt` and the selected
archive before checking its digest. Development, dirty, prerelease,
unknown-provenance, and unsupported-platform builds are always check-only.

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
pending phone-code hashes, session keys, manager-bot tokens, managed child-bot
tokens, or account phone numbers. Public config, auth, and managed-bot objects
are explicit allowlists. Login codes, 2FA passwords, API hashes, and manager
tokens cannot be passed as literal command-line arguments; use hidden prompts,
stdin, or named environment-variable flags so they do not enter shell history
or process argv. Message bodies may independently contain sensitive content and
are returned unchanged.

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
golangci-lint, gosec, govulncheck, module verification, supported macOS/Linux
builds, a Windows compile smoke, and diff checks. CI adds real Secret Service
and Keychain lifecycle tests plus packaged static-Linux runtime smoke tests.

Release archives are deterministic, checksummed, and provenance-attested. See
[`docs/releasing.md`](docs/releasing.md) for the tag and verification contract,
[`SECURITY.md`](SECURITY.md) for private vulnerability reporting, and
[`CONTRIBUTING.md`](CONTRIBUTING.md) for development boundaries.

tele is available under the [MIT License](LICENSE).
