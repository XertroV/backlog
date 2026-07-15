# Invalid-file diagnostics fixtures

Shared parity fixtures for the show/claim invalid-file warning contract.

- **Contract:** [`docs/diagnostics/invalid-file-warning-contract.md`](../../docs/diagnostics/invalid-file-warning-contract.md)
- **Expected message catalog:** [`expected/messages.json`](expected/messages.json)
- **Cases:** [`cases/`](cases/)

## Layout

Each case is a complete mini backlog root (run CLI with `cwd` set to the case
directory; data dir is `.tasks/`):

| Case directory | Condition code | Intent |
| --- | --- | --- |
| `cases/missing-indexed-file` | `missing_indexed_file` | Index points at a non-existent `.todo` |
| `cases/malformed-frontmatter` | `malformed_frontmatter` | File exists; opening `---` missing |
| `cases/id-path-mismatch` | `id_path_mismatch` | Index short id `T001`; frontmatter `id` is wrong |
| `cases/file-absent-from-index` | `file_absent_from_index` | Orphan `.todo` on disk, not listed in epic index |
| `cases/healthy-control` | _(none)_ | Valid control for false-positive checks |

## Hierarchy (all cases)

```text
.tasks/
  index.yaml                 # P1 → 01-phase
  01-phase/index.yaml        # M1 → 01-ms
  01-phase/01-ms/index.yaml  # E1 → 01-epic
  01-phase/01-ms/01-epic/
    index.yaml
    *.todo
```

Primary task id used by most cases: **`P1.M1.E1.T001`**.

## Usage in tests

1. Copy the case tree into a temp directory (recommended) **or** use a read-only
   cwd only for pure `show` checks.
2. Point the client at that root (`chdir` / process cwd). Prefer not mutating the
   committed fixture tree in-place.
3. Assert substrings from `expected/messages.json` (`warning_substrings`,
   `claim_error_substrings`).
4. Respect `claim.may_mutate` and `implementation.cli_assertions` flags so CI stays
   green until T002 lands remaining behavior.

## Regenerating / editing

Keep fixtures minimal. When changing a message string, update:

1. `docs/diagnostics/invalid-file-warning-contract.md`
2. `expected/messages.json`
3. Client implementations (T002) and focused tests
