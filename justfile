set shell := ["zsh", "-eu", "-o", "pipefail", "-c"]

default:
    @just --list

gate:
    just format-check
    just test
    just race
    just vet
    just lint
    just security
    just modules
    just build-all
    just diff-check

format-check:
    @unformatted="$(gofmt -l .)"; if [[ -n "$unformatted" ]]; then print -r -- "$unformatted"; exit 1; fi

test:
    go test ./...

race:
    go test -race ./...

vet:
    go vet ./...

lint:
    staticcheck ./...
    golangci-lint run ./...

security:
    gosec -quiet ./...
    govulncheck ./...

modules:
    go mod verify

build-all:
    go build -o "${TMPDIR:-/tmp}/tele-darwin-arm64" ./cmd/tele
    GOOS=linux GOARCH=amd64 go build -o "${TMPDIR:-/tmp}/tele-linux-amd64" ./cmd/tele
    GOOS=windows GOARCH=amd64 go build -o "${TMPDIR:-/tmp}/tele-windows-amd64.exe" ./cmd/tele

diff-check:
    git diff --check

install:
    #!/usr/bin/env zsh
    set -eu -o pipefail
    target_dir="$(go env GOBIN)"
    if [[ -z "$target_dir" ]]; then target_dir="$(go env GOPATH)/bin"; fi
    target="$target_dir/tele"
    mkdir -p "$target_dir"
    commit="$(git rev-parse --short=12 HEAD)"
    go build -ldflags "-X github.com/ardasevinc/tele/internal/buildinfo.Commit=$commit" -o "$target" ./cmd/tele
    print -r -- "installed: $target"
    "$target" --version
