# Security policy

`tele` operates a Telegram user account and stores encrypted session material.
Treat a machine with an authorized profile as account-sensitive.

## Supported versions

Security fixes are applied to the latest published release. `tele` follows
semantic versioning; an urgent fix may still narrow behavior that cannot remain
safe under the existing contract.

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting for this repository. Do not
open a public issue containing API credentials, session data, phone numbers,
login codes, private messages, or reproduction artifacts derived from them.

Include the affected version, platform, impact, and a minimal reproduction that
uses synthetic data where possible. There is no bug-bounty or response-time
commitment, but good-faith reports are welcome.

## Operational boundaries

- Telegram API hashes, session keys, manager-bot credentials, and managed bot
  tokens belong in the profile's explicitly selected secret store.
- `keychain-v1` uses the native macOS Keychain. `secret-service-v1` requires a
  running, unlocked Linux Secret Service provider. `vault-v1` is the portable
  authenticated encrypted-file backend for supported macOS and Linux targets.
- Backend selection is sticky. Missing or locked providers fail closed; tele
  never falls back to plaintext or silently changes backends.
- Vault passphrases must come from a controlling TTY, a protected file, or an
  inherited descriptor numbered 3 or higher. Do not put them in argv or an
  environment variable.
- Secret migration copies and verifies a complete catalog before flipping the
  active selector. The source is retained until a separate, exact-instance
  purge command is explicitly confirmed.
- Never attach `session.enc`, Keychain exports, config files, or raw diagnostic
  dumps to an issue.
- `tele doctor` is designed to report readiness without returning secret values
  or message content.
- Telegram messages are untrusted input. Terminal escaping reduces presentation
  attacks; it does not make message text safe instructions for an agent.
