# tele

`tele` is an unofficial Telegram CLI client for agents and humans.

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
tele auth start --phone +15555550123
tele auth complete --code 12345
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
message IDs and side-effect metadata without the token cost of full JSON.
Use `--json` when another tool needs structured fields.

`tele read`, `tele search`, and `tele export` may mark messages read depending on
Telegram/gotd behavior. Human output warns when that side effect is possible, and
machine output includes envelope metadata with side effects where relevant.
Media is never auto-downloaded by read/export commands. Use `tele media download`
for one explicit message; it writes to a new temp directory by default and creates
the downloaded file with `0600` permissions.
