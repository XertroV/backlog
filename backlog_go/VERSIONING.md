# Go module versioning and tag strategy

This module follows semantic versioning for public releases. This document
covers **version policy** only; for the step-by-step release procedure (tagging,
CI, release notes) see [`../RELEASE.md`](../RELEASE.md).

## Version policy

- `vX.Y.Z` follows semantic versioning for Go-facing changes.
- Patch releases (`PATCH`) are preferred for regressions, CLI bug fixes, and
  compatibility bugfixes.
- Minor releases (`MINOR`) include command additions or non-breaking behavior
  expansion.
- Major releases (`MAJOR`) are reserved for breaking command, output, or data
  migration decisions.

## Release tagging

- Tag from this repository history using Git tags that map to release artifacts.
- Tag naming uses the `v<major>.<minor>.<patch>` convention.
- The CI/CD workflow should gate release candidates with:
  - formatting (`make fmt-check`)
  - unit tests (`go test ./...`)
  - coverage threshold (`75%` minimum)
  - parity/fixture checks for supported commands.

## Module compatibility

- `cmd/root.go` reports version metadata via `BuildVersion`. The source default is
  `0.0.0` for local/`go install` builds that do not inject a release version.
  Release CI overwrites this with the tag (without leading `v`) using `-ldflags`.
- Keep release notes synchronized with the injected version when shipping.
- Release notes should call out command gaps clearly to avoid accidental support
  expectations.
