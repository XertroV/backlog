# backlog_go install and runtime examples

## Prerequisites

- Go 1.22+.
- Unix-style shell for examples below.

## Quick install

```bash
cd backlog_go
go install
```

If `go install` writes to a location outside your `PATH`, use:

```bash
export PATH="$HOME/go/bin:$PATH"
```

## Run examples

```bash
cd backlog_go
go run . --help
go run . init --project "Backlog Project"
go run . list --json
go run . show --help
```

## Build and distribution

```bash
cd backlog_go
go build -o backlog .
./backlog init --project "Backlog Project"
./backlog list
```

CI runs from this directory should call:

```bash
go test ./...
make coverage-check
```

## Upgrading an existing install

The Go CLI will print a one-line notice when a newer release is published on
GitHub (cached for 24h, surfaced every 5th invocation). To upgrade in-place:

```bash
backlog upgrade --check     # see current vs latest
backlog upgrade             # download + replace + keep the old binary as *.old.<ts>
backlog upgrade --yes       # skip the confirm prompt
backlog upgrade --version v0.3.0   # pin to a specific tag
```

Falls back to `clankercode/backlog` if `XertroV/backlog` is unreachable, so the
recent repo-move plans won't strand users. Disable the background check in
constrained environments with `BACKLOG_NO_UPDATE_CHECK=1`.

