# Releasing `backlog`

This document is the **canonical release procedure**. It is written for an agent
(or human) to follow end-to-end, including babysitting CI and curating the
GitHub release page.

## What a release is

- The **Go client (`backlog_go`) is canonical.** Releases ship pre-built Go
  binaries for six targets: `linux`, `darwin`, `windows` × `amd64`, `arm64`.
- A release is triggered entirely by **pushing a `vX.Y.Z` git tag** to `origin`.
  The [`.github/workflows/release.yml`](.github/workflows/release.yml) workflow
  does the rest: validate → build → changelog → create the GitHub Release with
  all binaries + a `SHA256SUMS` file attached.
- Separately, [`ci-master-build.yml`](.github/workflows/ci-master-build.yml)
  keeps a rolling `latest` **pre-release** built from `master`. That is *not* a
  version release — ignore it for tagged releases; production users consume
  `vX.Y.Z` releases only.

Repo: `XertroV/tasks`. Tags and releases are named `vX.Y.Z`; the binary itself
reports the version **without** the `v` prefix (e.g. `backlog version 0.2.0`).

## Versioning policy

Semantic versioning. See [`backlog_go/VERSIONING.md`](backlog_go/VERSIONING.md)
for the full policy. In short:

- **PATCH** (`vX.Y.Z+1`) — bug fixes, regressions, compatibility fixes.
- **MINOR** (`vX.Y+1.0`) — new commands/flags, non-breaking behaviour additions.
- **MAJOR** (`vX+1.0.0`) — breaking command, output, or data-migration changes.

Pick the bump by diffing against the previous release (`git log <prev-tag>..HEAD`).

## Prerequisites

- `gh` authenticated (`gh auth status`) with push access to `XertroV/tasks`.
- Local `master` up to date with `origin/master`.
- A clean working tree (`git status`).

---

## Procedure

### 1. Confirm master is release-ready

Run from the repo root, targeting the Go client:

```bash
git checkout master && git pull --ff-only
cd backlog_go
make fmt-check          # gofmt is clean
go mod tidy && git diff --exit-code go.sum   # go.sum committed & tidy
go test ./... -run '!TestRunParity'          # unit tests (parity tests need bun/python)
go build -o /tmp/backlog-smoke . && /tmp/backlog-smoke version   # smoke build
cd ..
```

These mirror the `validate` job in `release.yml`. If any fail, fix and merge to
`master` **before** tagging — the workflow runs against the tagged commit, so the
fix must be in history the tag points at.

### 2. Choose the version and review the changelog window

```bash
PREV=$(git describe --tags --abbrev=0 --match 'v[0-9]*')   # e.g. v0.1.0
echo "Previous release: $PREV"
git log --no-merges --pretty=format:'- %s (%h)' "$PREV..HEAD"
```

Read that log and decide the semver bump. Note anything user-facing — you will
fold it into the release page in step 6.

### 3. Create and push the tag

Use an **annotated** tag. Set `VER` to the chosen version:

```bash
VER=v0.2.0
git tag -a "$VER" -m "backlog $VER"
git push origin "$VER"
```

Pushing the tag is what triggers `release.yml`. (Never reuse or move an existing
tag — see Rollback below.)

### 4. Babysit CI

Watch the run to completion — do not walk away until the release exists:

```bash
gh run watch "$(gh run list --workflow=release.yml --limit=1 --json databaseId --jq '.[0].databaseId')" --exit-status
```

The workflow runs four job stages:

| Job         | What it does                                              |
|-------------|-----------------------------------------------------------|
| `validate`  | tidy / fmt-check / `go test` (must pass or nothing ships) |
| `build`     | cross-compiles all six binaries (matrix)                  |
| `changelog` | generates release notes since the previous `v*` tag       |
| `release`   | flattens binaries, writes `SHA256SUMS`, creates the Release|

If a job **fails**, inspect it and fix root cause:

```bash
gh run view <run-id> --log-failed
```

Common failures and fixes (apply on `master`, then re-tag per Rollback):
- **fmt-check** — run `cd backlog_go && gofmt -w .`, commit.
- **go.sum diff** — run `go mod tidy`, commit the updated `go.sum`.
- **test failure** — fix the test/code on `master`.

Typical total runtime is ~1–3 minutes.

### 5. Verify the published release

```bash
gh release view "$VER"
```

Confirm **all seven assets** are attached: six `backlog-<os>-<arch>[.exe]`
binaries plus `SHA256SUMS`. Spot-check one binary end-to-end:

```bash
cd $(mktemp -d)
gh release download "$VER" --pattern 'backlog-linux-amd64' --pattern 'SHA256SUMS'
sha256sum -c SHA256SUMS --ignore-missing     # must report: backlog-linux-amd64: OK
chmod +x backlog-linux-amd64
./backlog-linux-amd64 version                # must print the version WITHOUT the leading v
cd - >/dev/null
```

The reported version must equal `$VER` with the `v` stripped (e.g. `0.2.0`).

### 6. Curate the release page

The workflow auto-fills the body with a raw commit list. **Improve it** into a
human-readable summary covering *all* changes since the previous release:

1. Read the auto-generated notes: `gh release view "$VER"`.
2. Draft a summary grouped by **Added / Changed / Fixed** (drop noise like
   "fix typo", "wip"). Use the `git log "$PREV..$VER"` from step 2 as the source
   of truth, not just the commit subjects.
3. Update the release, preserving the auto-generated Downloads section:

```bash
# Edit notes.md to hold the final curated body, then:
gh release edit "$VER" --notes-file notes.md
```

Keep the `### Downloads` / `SHA256SUMS` verification block from the auto-notes so
users still get install + checksum instructions.

### 7. Post-release

- If install docs reference a specific version, update them
  (`README.md`, `backlog_go/INSTALL.md`).
- Announce/hand off as appropriate.

---

## Rollback / fixing a bad release

Tags are immutable by convention — do **not** move a published tag. If a release
is broken:

**Preferred — ship a new patch.** Fix on `master`, then tag `vX.Y.Z+1`. Cheap,
auditable, no history rewriting.

**If the tag must be reclaimed** (e.g. tagged the wrong commit and no one has
pulled it):

```bash
gh release delete "$VER" --yes        # remove the GitHub Release
git push origin :refs/tags/"$VER"     # delete the remote tag
git tag -d "$VER"                     # delete the local tag
# ...fix, then redo from step 3.
```

Only reclaim a tag if you are certain it has not been consumed downstream.

---

## Quick reference

```bash
# cut a release (after master is green and merged)
VER=v0.2.0
git tag -a "$VER" -m "backlog $VER" && git push origin "$VER"
gh run watch "$(gh run list --workflow=release.yml --limit=1 --json databaseId --jq '.[0].databaseId')" --exit-status
gh release view "$VER"
# ...then curate the notes (step 6) and verify a binary (step 5).
```
