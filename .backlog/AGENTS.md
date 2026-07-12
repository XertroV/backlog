# AGENTS.md

## Agent workflow

- Always use `bl` to create and modify backlog tasks.
- Use `bl --help` before acting if you need command-level guidance.
- Run `bl howto` to review workflow expectations.

## Client implementations

`bl` / `backlog` is the **Go** client and the canonical one. The `backlog/`
(Python) and `backlog_ts/` (TypeScript) trees exist **only as parity-test
helpers** — they re-implement the same CLI surface so cross-client parity
tests can verify behaviour against the Go reference. Don't `pip install` the
Python package or `bun install`/`npm install` the TS package in your dev
environment; they are not user-facing tools. If you need to run a parity
test locally, invoke the scripts directly (e.g. `python backlog.py ...` or
`bun run backlog_ts/src/cli.ts ...`) — the CI workflow does the same.
