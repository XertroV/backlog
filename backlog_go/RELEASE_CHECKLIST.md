# Go CLI release checklist

> **The canonical release procedure lives in [`../RELEASE.md`](../RELEASE.md).**
> Follow that document to cut a release (tag → babysit CI → curate the GitHub
> release page). This file only lists the Go-specific pre-flight checks it refers
> to.

## Pre-release verification (Go client)

- [ ] Working tree is clean for the intended release scope.
- [ ] `make fmt-check` passes.
- [ ] `go mod tidy` leaves `go.sum` unchanged (committed and tidy).
- [ ] `go test ./... -run '!TestRunParity'` passes.
- [ ] Smoke build works: `go build -o /tmp/backlog . && /tmp/backlog version`.
- [ ] Command parity acceptance checklist reviewed (`PARITY_ACCEPTANCE_CHECKLIST.md`).
- [ ] Known command gaps reviewed (`COMMAND_PARITY_MATRIX.md`).

These mirror the `validate` job in [`../.github/workflows/release.yml`](../.github/workflows/release.yml).
Everything after this — tagging, CI, release notes — is in [`../RELEASE.md`](../RELEASE.md).
