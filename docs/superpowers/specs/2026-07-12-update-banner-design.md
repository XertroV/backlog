# Background Update Detection — Design

Date: 2026-07-12
Scope: Go client only (`backlog_go`). Python and TypeScript parity clients are out of scope for this PR (per user).

## Goal

When the user runs `backlog`, the CLI should:

1. Quietly check whether a newer version of `backlog` is available on GitHub
   Releases for `XertroV/backlog`.
2. Cache that lookup for **1 day** so we don't hammer the GitHub API.
3. On every **5th invocation** where a newer version exists, prepend a single,
   visually distinct yellow line to the start of the CLI's output inviting the
   user to run `backlog upgrade`.
4. Provide a working `backlog upgrade` subcommand that downloads and replaces
   the current binary with the matching release asset.
5. Skip the banner when output is `--json` (so machine consumers stay clean)
   and skip when the user disabled color (the line still appears, but plain).

## Non-Goals

- No parity work for Python or TypeScript clients.
- No automatic, unattended self-upgrade (the user must opt in with
  `backlog upgrade`).
- No migration of pre-existing caches — first run creates a fresh cache file.
- No background goroutine / async fetch: blocking the CLI for ~50–200 ms on
  the first invocation each day is acceptable. We add a short hard timeout
  (2s) and degrade silently on failure.

## Architecture

### New package: `backlog_go/internal/updater`

Single-responsibility module with no dependency on the rest of the runner:

```
updater/
├── cache.go       # load/save state, path resolution
├── semver.go      # tiny semver compare (no external dep)
├── release.go     # GitHub release fetch (stdlib net/http)
├── banner.go      # MaybePrintBanner(state, args) decision logic + formatting
├── upgrade.go     # download + replace (used by the `backlog upgrade` cmd)
└── *_test.go      # unit tests with httptest and t.TempDir
```

State struct (serialized to JSON):

```go
type State struct {
    LastCheckedAt    time.Time `json:"last_checked_at"`
    LatestTag        string    `json:"latest_tag,omitempty"`        // e.g. "v0.2.1"
    LatestVersion    string    `json:"latest_version,omitempty"`    // e.g. "0.2.1"
    SourceRepo       string    `json:"source_repo,omitempty"`       // which repo it came from ("XertroV/backlog" | "clankercode/backlog")
    CheckOK          bool      `json:"check_ok"`                    // last fetch succeeded
    CurrentVersion   string    `json:"current_version,omitempty"`   // the version that produced this state
    InvocationCount  int       `json:"invocation_count"`            // monotonic counter, persists
}
```

### Public API

```go
// Load reads the cached state. Missing/corrupt cache returns zero-value state.
func Load() State

// Save writes state atomically (tmp file + rename) with 0600 perms.
func Save(s State) error

// Check returns possibly-updated state. If the cached state is <24h old,
// it returns as-is. Otherwise it tries to fetch with a 2s timeout; on
// failure it returns the existing state untouched and leaves LastCheckedAt
// unchanged.
func Check(ctx context.Context, currentVersion string) (State, error)

// MaybePrintBanner prints the one-line update banner if all of the following
// are true:
//   - args do NOT include `--json` (caller passes the raw arg slice)
//   - state.CheckOK and LatestVersion > CurrentVersion
//   - state.InvocationCount > 0 && state.InvocationCount % 5 == 0
//
// It also bumps the invocation counter (Save) before returning.
func MaybePrintBanner(state State, args []string, currentVersion string) error
```

### Cache location

Resolution order (first writable wins):
1. `$BACKLOG_UPDATE_CACHE_FILE` (full path override)
2. `$XDG_CACHE_HOME/backlog/update-check.json`
3. `$HOME/.cache/backlog/update-check.json`
4. `$TMPDIR/backlog/update-check.json`

Directory is created with 0700 perms if missing. File is 0600.

### Version comparison

A 30-line semver compare handling `v` prefix and missing patch/minor:

- `0.1.0` < `0.2.0`
- `v1.2.3` == `1.2.3`
- `1.2` is treated as `1.2.0`
- Pre-release suffixes (e.g. `-rc1`) sort before the corresponding release.
- Non-semver tags (e.g. `nightly-...`) are treated as "newer than anything",
  but the banner only fires if the comparison succeeds.

### GitHub fetch

- Endpoints, in **failover order**:
  1. `GET https://api.github.com/repos/XertroV/backlog/releases/latest`
  2. `GET https://api.github.com/repos/clankercode/backlog/releases/latest`
     (used only when #1 returns 404, 5xx, or a network/timeout error; the
     user plans to move the canonical repo there).
- Headers: `Accept: application/vnd.github+json`, `User-Agent: backlog-go/<v>`
- Timeout: 2s per attempt via `http.Client{Timeout: 2s}`; total worst case
  ≈4s (we accept this; first invocation of the day may pay it).
- Parse just `tag_name` from the JSON; ignore everything else.
- The chosen source repo is recorded in `state.SourceRepo` so debugging
  is straightforward (`backlog upgrade --check` prints it).
- On both endpoints failing: keep stale state, do not bump
  `LastCheckedAt`. The banner never fires based on stale data older than
  7 days.
- Hardcoded list lives in one place (`updater.repos = []string{...}`); a
  follow-up could expose it via flag if user feedback demands.

### Banner copy and formatting

Single line, bright yellow (via existing `styleWarning`):

```
⬆ Update available: v0.2.1 (you have v0.1.0) — run `backlog upgrade` to upgrade.
```

- One line: never wrapped/truncated.
- Visually distinct: ANSI bold + yellow (yellow stays plain-text when
  `--no-color` / `NO_COLOR=1` / non-TTY).
- Emoji `⬆` reads well even without color.
- Trimmed to fit ≤100 chars wide terminals by eliding the middle portion
  when terminal width < 100 (extra safety; can be deferred if too much).

### 5th-invocation cadence

Counter increments on every successful `MaybePrintBanner` call (after the
skip checks). Banner only fires when the incremented count is a positive
multiple of 5.

Semantics:

- Run 1 → counter 1, no banner.
- Run 5 → counter 5, banner.
- Run 10 → counter 10, banner.
- Run 100 → counter 100, banner.

Counter resets to 0 when `state.CurrentVersion != currentVersion`, i.e.
after the user actually installed a new version. This prevents the banner
from spamming after upgrade until the *next* release becomes available.

### `backlog upgrade` subcommand

New subcommand tree under `commands`:

```
CmdUpgrade       = "upgrade"
CmdUpgradeAlias  = "self-update"    // also accepted
```

Flags:

- `--check`     — print current vs latest, then exit (no download).
- `--version V` — target a specific tag (default: latest). Useful for
                  downgrading or pinning.
- `--yes` / `-y`— skip the "replace current binary?" prompt when running
                  interactively. Non-interactive runs default to --yes.
- `--help` / `-h`.

Behavior:
1. Reuse `updater.Check` to get the latest version (refreshing 24h cache).
2. Compute target asset name: `backlog-<goos>-<goarch>[.exe]`.
3. If running from a non-Go source build, refuse and print a clear message.
4. Download from the chosen source repo (`state.SourceRepo` if known,
   otherwise try `XertroV/backlog` then `clankercode/backlog`):
   `https://github.com/<owner>/backlog/releases/download/<tag>/<asset>`
   with a 30-second timeout and streaming body (avoid loading whole
   archive into memory).
5. Download `SHA256SUMS` for the same tag, verify the line for our asset.
6. Backup the current binary to `<exe>.old.<unix-ts>` (same dir).
7. Replace the binary atomically: write to `<exe>.new`, fsync, rename.
8. Print `upgraded <old> -> <new>` and exit 0.

Edge cases:
- Current binary path: `os.Executable()`; resolve symlinks.
- File on read-only mount or directory lacking write perms: print clear
  error and a hint to install via `curl ... | sh` (link to README).
- Network failure: exit non-zero with the HTTP status / error message.

### Augmenting `backlog update` help

`CmdUpdate` help (`printUsageForCommand` path) gets an extra paragraph at
the bottom:

> **Tip:** `backlog update` updates a *task*'s status. To upgrade the
> `backlog` CLI itself, run `backlog upgrade`.

The existing task-update usage path is unchanged; this only changes the
no-args help output and the recovery hint.

### Wiring into the runner

Single insertion in `Run()` immediately after color-flag parsing and before
command dispatch:

```go
if !isUpgradeSelfUpdateCheckDisabled(args) {
    if state, err := updater.Check(context.Background(), root.Version()); err == nil {
        _ = updater.MaybePrintBanner(state, args, root.Version())
    }
}
```

`isUpgradeSelfUpdateCheckDisabled` returns true for `version`, `-v`, `--version`,
`help`, `-h`, `--help`, and the no-args invocation (the help banner already
makes a banner redundant). All other commands trigger the check.

## Testing Strategy

Unit tests under `backlog_go/internal/updater/`:

- `cache_test.go`: round-trip Save/Load; missing file; corrupt JSON; atomic
  write leaves no temp file on success.
- `semver_test.go`: cases for the matrix above (with/without `v`, missing
  patch/minor, pre-release, malformed).
- `release_test.go`: httptest server returning a fake release; 404 path;
  timeout path; malformed JSON path; verify state is left untouched on
  error.
- `banner_test.go`: skip-on-`--json`; cadence (counter == 5 → emits);
  version-not-greater path; counter reset on `CurrentVersion` change.
- `upgrade_test.go`: backup creation; failure modes (no asset, bad
  checksum); `os.Executable` path mocked via package var.

Integration: smoke test the wired-in banner using `httptest` plus a stub
`root.Version()` override is overkill — the unit-level coverage above plus
a manual smoke (`backlog grab`) is sufficient.

Mechanical checks before merge:

```bash
cd backlog_go && make fmt-check
go mod tidy && git diff --exit-code go.sum
go test ./... -run '!TestRunParity'
go build -o /tmp/backlog-smoke . && /tmp/backlog-smoke upgrade --check || true
```

## Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| Primary repo moves or 404s | Failover to `clankercode/backlog`; state records which repo served the latest tag; `backlog upgrade --check` prints it. |
| Self-replacing a running binary fails on Windows (locked .exe) | Future PR; out of scope here. The CLI prints a Windows-friendly error for now. |
| User upgrades while another concurrent `backlog` is in flight | Worst case is "other invocations get the old binary until they restart". Acceptable for a CLI. |
| Cache file is shared across machines (CI vs laptop) | Cache is per `$HOME`; CI typically has ephemeral `$HOME`. Acceptable. |
| User's first run with no internet | `Check` errors, no state change, no banner. |
| Banner breaks `--json` parsers | `MaybePrintBanner` inspects `args` for `--json` and silently skips. |
| `backlog upgrade` races on partial download | Stream to `<exe>.new`; only `rename` over the live binary after fsync. |

## Rollout

- Land on master via the user's automerge instruction (FF-only after
  squash on the feature branch in `.worktrees/feat-update-banner`).
- No release needed for the banner to surface — users pick it up on their
  next `backlog` invocation after pulling.
- Document `backlog upgrade` in `README.md` and `backlog_go/INSTALL.md`
  alongside the existing `gh release download` snippet so users have a
  fallback path.
- Track the canonical repo location in a single `var defaultRepos = []string{
    "XertroV/backlog",
    "clankercode/backlog",
  }` so future moves are one-line changes.
