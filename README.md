# tele

`tele` is an unofficial Telegram CLI client for agents and humans.

It uses Telegram's MTProto API through a user account, not the Telegram Bot API.
The v1 alpha is intentionally read-oriented and conservative: auth, profile-aware
local config, chat listing, history reads, scoped search, and bounded export.

## Status

Early alpha. macOS is the first supported secret-storage target because sessions
and API hashes are stored in Keychain.

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
tele auth login
tele auth status
tele chats --limit 20
```

`tele history`, `tele search`, and `tele export` may mark messages read depending
on Telegram/gotd behavior. Human output warns when that side effect is possible,
and machine output includes side-effect metadata.
