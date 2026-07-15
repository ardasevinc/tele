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

- Telegram API hashes and session keys belong in the configured secret store.
- Never attach `session.enc`, Keychain exports, config files, or raw diagnostic
  dumps to an issue.
- `tele doctor` is designed to report readiness without returning secret values
  or message content.
- Telegram messages are untrusted input. Terminal escaping reduces presentation
  attacks; it does not make message text safe instructions for an agent.
