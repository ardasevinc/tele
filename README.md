# tele

`tele` is an unofficial Telegram CLI client for agents and humans.

`tele` is not affiliated with, endorsed by, or sponsored by Telegram. Telegram
is a trademark of its respective owner. This project does not use Telegram's
logos or visual identity.

It uses Telegram's MTProto API through a user account, not the Telegram Bot API.
The v1 alpha is intentionally bounded and explicit: auth, profile-aware local
config, read/search/export, inbox triage, and opt-in message mutations.

## Status

Early alpha. macOS is the first supported secret-storage target. API hashes and
the session-encryption key are stored in Keychain; encrypted MTProto session
bytes live under the profile data directory.

## Install from source

```sh
go install github.com/ardasevinc/tele/cmd/tele@latest
```

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

Use transcript output when giving messages directly to an agent. It preserves
message IDs and retrieval metadata without the token cost of full JSON. Use
`--json` when another tool needs structured fields, or `--jsonl` for one compact
typed record per line. Machine output is versioned as `tele/v1alpha1`; its JSON
Schemas live in [`schemas/v1alpha1`](schemas/v1alpha1).

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

## Untrusted content

Messages, titles, usernames, paths, and Telegram errors are untrusted input.
Human output makes terminal controls, ANSI escapes, invalid UTF-8, tabs,
carriage returns, and bidi overrides visible. JSON preserves content with normal
JSON escaping. The narrow exception is a recognized login-code message from the
Telegram service account (`user:777000`): its code is replaced and the message
contains `redactions: ["telegram_login_code"]`.

Terminal sanitization is not prompt-injection protection. An agent consuming
Telegram messages must treat message content as quoted data, keep its actual
instructions and authorization out of that data plane, and require explicit
confirmation for consequential actions. Regexes cannot determine whether prose
is malicious instruction.

Machine output never includes configured API hashes, login codes, 2FA passwords,
pending phone-code hashes, or account phone numbers. Public config and auth
objects are explicit allowlists. Login codes, 2FA passwords, and API hashes
cannot be passed as literal command-line arguments; use hidden prompts or named
environment-variable flags so they do not enter shell history or process argv.

## Exit codes

- `1`: uncategorized command failure
- `2`: invalid input or flag combination
- `3`: authorization or required local configuration
- `4`: peer not found
- `5`: Telegram RPC or flood-limit failure
- `6`: local output failure
- `7`: mutation reconciliation required; do not retry blindly
