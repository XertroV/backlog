---
name: release
description: Guide for creating backlog releases -- version tagging, CI workflows, and release process.
---

# Release Skill

Covers how to create and manage releases for the backlog Go CLI.

## Quick Reference

- **Version-tag release:** `git tag v0.1.0 && git push origin v0.1.0` -- triggers `.github/workflows/release.yml`
- **Latest dev builds:** every push to master auto-builds binaries via `.github/workflows/ci-master-build.yml`
- **View releases:** https://github.com/XertroV/tasks/releases

## Release Workflows

### 1. Version Release (release.yml)

Triggered by pushing a `v*` tag. This is the production release workflow.

Steps:
1. Ensure master is clean and tests pass locally.
2. Tag the release:
     git tag v0.2.0
     git push origin v0.2.0
3. CI automatically:
   - Runs validation (tidy, fmt, tests, 75% coverage gate)
   - Cross-compiles binaries for linux/darwin/windows (amd64 + arm64)
   - Injects version into binary via ldflags
   - Generates changelog since the previous version tag
   - Creates a GitHub Release with all binaries attached

### 2. Latest Build (ci-master-build.yml)

Triggered automatically on every push to master (when backlog_go/ or the workflow changes).

- Builds all platform binaries with version `latest+<commit-short-sha>`
- Creates/updates a rolling `latest` pre-release on GitHub
- Includes changelog: commits since last version tag + recent 20 commits
- Intended for development/testing, not production use

## Versioning Policy

Follows semver (see backlog_go/VERSIONING.md):

- **Patch** (v0.1.X): bugfixes, regressions
- **Minor** (v0.X.0): new commands, non-breaking features
- **Major** (vX.0.0): breaking changes

The version is injected at build time via:
    -ldflags "-X github.com/XertroV/tasks/backlog_go/cmd.BuildVersion=<version>"

## Manual Pre-release Checklist

Before tagging a release:

1. Clean working tree: git status
2. Run all tests: make test
3. Format check: cd backlog_go && make fmt-check
4. Coverage gate: cd backlog_go && make coverage-check
5. Smoke test the binary: ./backlog-go --help
6. Verify parity: make parity (if applicable)

## Binary Platforms

| OS      | Architecture | Binary name pattern            |
|---------|-------------|-------------------------------|
| Linux   | amd64       | backlog-linux-amd64            |
| Linux   | arm64       | backlog-linux-arm64            |
| macOS   | amd64       | backlog-darwin-amd64           |
| macOS   | arm64       | backlog-darwin-arm64           |
| Windows | amd64       | backlog-windows-amd64.exe      |
| Windows | arm64       | backlog-windows-arm64.exe      |

## Troubleshooting

- **CI fails on validate step:** check fmt, tests, or coverage locally first
- **Missing binaries in release:** check the build matrix in release.yml
- **Wrong version in binary:** ensure the tag name matches v* pattern
- **Changelog is empty:** verify the previous tag exists (git tag --list 'v*')
