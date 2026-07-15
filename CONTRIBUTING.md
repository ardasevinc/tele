# Contributing

Small, safety-focused changes are welcome. `tele` is an account-capable MTProto
client, so mutation, auth, persistence, and machine-output changes need stronger
evidence than ordinary CLI polish.

## Development

Requirements:

- Go version declared in `go.mod`
- `just`
- `staticcheck`, `golangci-lint`, `gosec`, and `govulncheck` for the full local
  gate

Run:

```sh
just gate
```

Do not use real credentials or private Telegram content in tests, fixtures,
issues, commits, or CI. Prefer narrow interfaces and synthetic Telegram types to
live MTProto calls. Live mutations are not part of the ordinary contribution
workflow.

Commits use conventional prefixes such as `fix:`, `feat:`, `refactor:`,
`test:`, `docs:`, `build:`, and `ci:`.

## Compatibility

The current alpha may break commands and schemas to fix unsafe or misleading
behavior. Public machine output is versioned under `schemas/`; update schemas,
goldens, and the complete command matrix together when changing it.
