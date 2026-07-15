# Invalid-file warning contract (show / claim)

**Status:** Contract defined (P2.M1.E1.T001). Command wiring complete (P2.M1.E1.T002).  
**Clients:** Go (`backlog_go`, canonical), Python (`backlog/`), TypeScript (`backlog_ts/`).  
**Shared fixtures:** [`testdata/invalid-file-diagnostics/`](../../testdata/invalid-file-diagnostics/).

This document is the durable observable contract for how `show` and `claim` report
invalid backlog task files. Implementers and agents should treat the condition codes,
message substrings, claimability rules, and recovery guidance below as the parity
target.

## Scope

| In scope | Out of scope (for this contract) |
| --- | --- |
| Task (and aux-style) `.todo` files referenced by show/claim | Bulk tree repair (`check` may report related issues) |
| Four diagnostic conditions listed below | Silent loader-only errors with no user-facing warning |
| Text UX on stdout (and JSON *shape* if diagnostics are ever exposed) | Changing auto-commit or git policy |

`show` and `claim` do **not** currently take `--json`. Text mode is the primary
observable surface. When a future flag or structured diagnostics channel is added,
use the additive JSON shape in [JSON diagnostics (optional / future)](#json-diagnostics-optional--future).

## Streams and exit behavior

| Surface | Stream | Notes |
| --- | --- | --- |
| `Warning: …` diagnostics | **stdout** (same stream as normal command text) | Matches existing Go/Python/TS `Warning:` styling. Do not bury warnings only on stderr. |
| Blocking claim errors (`Error:` / `Cannot claim…`) | stdout and/or returned error text (implementation-dependent) | Must be greppable; non-zero exit. |
| Readable task body / metadata | stdout | `show` must still print whatever is safely readable after warnings. |

| Command | Condition outcome | Exit |
| --- | --- | --- |
| `show` | Any warning condition | **0** if the ID resolved (or orphan content was shown); non-zero only for hard not-found / invalid path when nothing can be resolved |
| `claim` | Warning-only, still claimable | **0** on success; print all warnings **before** mutation |
| `claim` | Mutation blocked | **non-zero**; print warnings then the blocking error |

Emit **all** applicable warnings for a single target (do not stop after the first),
except that a missing file makes frontmatter/id checks inapplicable.

## Condition codes

Stable machine codes (for logs, tests, and optional JSON):

| Code | Name |
| --- | --- |
| `missing_indexed_file` | Missing indexed file |
| `malformed_frontmatter` | Malformed front matter |
| `id_path_mismatch` | ID / path mismatch |
| `file_absent_from_index` | Resolvable file absent from the index |

---

### 1. `missing_indexed_file`

**Definition:** The loaded tree has a task (or aux) entry whose `file` path does not
exist under the data directory (`.tasks` / `.backlog`).

**Detection:** Resolve `data_dir / task.file` (or absolute equivalent). If the path is
missing (`ENOENT`), this condition applies. Unreadable-but-present files are a
related warning (`Task file for {id} is not readable: …`) but are not this code.

**Required warning substring(s):**

```text
Warning: Task file missing for {TASK_ID}: {PATH}
```

`{PATH}` may be absolute or `data_dir`-relative; tests match on
`Task file missing for {TASK_ID}`.

**show:** Print task metadata from the index, emit the warning, skip body/preview that
requires the file. Prefer still printing next-step hints when the task is pending.

**claim:**

| | |
| --- | --- |
| **May mutate?** | **No** |
| **Blocking error substring** | `Cannot claim {TASK_ID} because the task file is missing.` |

**Recovery (agent-friendly):**

1. `backlog check` — confirm missing-file findings.
2. Restore the `.todo` from VCS, or recreate it under the path listed in the epic
   (or bugs/ideas) `index.yaml`.
3. If the path is wrong, fix the `file:` entry in the epic index, then
   `backlog show {TASK_ID}` to verify.
4. Re-run `backlog claim {TASK_ID}` once the file exists.

**Fixture:** `testdata/invalid-file-diagnostics/cases/missing-indexed-file/`  
**CLI live parity today:** Implemented (Go / Python / TypeScript).

---

### 2. `malformed_frontmatter`

**Definition:** The indexed file exists and is readable, but YAML front matter is not
a valid `---` / mapping / YAML document suitable for task metadata.

**Detection (ordered checks; first match wins for message text):**

1. Missing opening `---` on the first non-empty semantics line (contract: first line
   must be `---`).
2. Missing closing `---` after the opening marker.
3. YAML parse error for the frontmatter block.
4. Parsed document is not a mapping/object.

**Required warning substring(s)** (one of):

```text
Warning: Invalid frontmatter format in {TASK_ID}: missing opening `---` marker.
Warning: Invalid frontmatter format in {TASK_ID}: missing closing `---` marker.
Warning: Invalid task file YAML in {TASK_ID}: {DETAIL}
Warning: Invalid frontmatter in {TASK_ID}: expected a mapping.
```

**show:** Emit warning(s). Still print readable raw/body content when available
(do not hide the file solely because frontmatter is broken).

**claim:**

| | |
| --- | --- |
| **May mutate?** | **Yes** (warn-only) |
| Notes | Index + in-memory task state are authoritative enough to rewrite frontmatter. Claim **must** print the warning(s) before mutation. Successful claim may repair frontmatter as a side effect of save. |

**Recovery:**

1. Open the file: `backlog edit {TASK_ID}` or `cat` the path from `show`.
2. Ensure frontmatter is a YAML mapping between `---` fences with at least
   `id`, `title`, `status` (see schema docs).
3. `backlog show {TASK_ID}` to confirm warnings clear.
4. Or `backlog claim {TASK_ID}` if only claim metadata needs rewriting and the body
   is salvageable.

**Fixture:** `testdata/invalid-file-diagnostics/cases/malformed-frontmatter/`  
**CLI live parity today:** Warnings on show/claim largely implemented; keep message
text stable across clients (T002 closes any remaining gaps).

---

### 3. `id_path_mismatch`

**Definition:** The task is resolved via the **index** (hierarchy + epic/bugs/ideas
index entry). The on-disk frontmatter contains an `id` that, after the same
normalization used for load, does **not** equal the **index-derived full ID**.

**Index-derived full ID:** For normal tasks, the full ID built from parent phase /
milestone / epic path context plus the short `id` in the epic `index.yaml` entry
(e.g. index `T003` under `P1.M1.E1` → `P1.M1.E1.T003`). For bugs/ideas, the index
entry id is the full id.

**Detection:** When validating the resolved file for show/claim, parse frontmatter
`id`. If non-empty and normalized value ≠ index-derived full ID, warn. Lookup for
`show`/`claim` **must** remain addressable by the index-derived ID even when
frontmatter disagrees (index is source of truth for addressing).

**Required warning substring:**

```text
Warning: Frontmatter ID mismatch in {INDEX_DERIVED_ID}: {FRONTMATTER_ID}
```

**show:** Emit warning; still show content and metadata (prefer index-derived ID in
headers so agents address the task consistently).

**claim:**

| | |
| --- | --- |
| **May mutate?** | **Yes** (warn-only) |
| Notes | Claim by **index-derived ID**. Save should prefer writing the index-derived `id` into frontmatter (repair), or at minimum keep index identity stable. |

**Recovery:**

1. `backlog show {INDEX_DERIVED_ID}` — confirm mismatch warning.
2. Edit frontmatter `id:` to match `{INDEX_DERIVED_ID}`, **or** if the file truly
   belongs to another task, move/rename and fix `file:` in the correct index.
3. `backlog check` for broader ID integrity.
4. `backlog claim {INDEX_DERIVED_ID}` once addressing is clear.

**Fixture:** `testdata/invalid-file-diagnostics/cases/id-path-mismatch/`  
**CLI live parity today:** Implemented (Go / Python / TypeScript).

---

### 4. `file_absent_from_index`

**Definition:** A `.todo` file is **resolvable on disk** (under the data directory,
typically beside an epic that would own it, with parseable frontmatter identifying
a task ID), but **no index entry** references that file (and the task ID is not
present in the loaded tree).

**Detection (show / claim by ID):**

1. Standard tree lookup fails for `{TASK_ID}`.
2. Client searches the data dir for a `.todo` whose frontmatter `id` normalizes to
   `{TASK_ID}`, **or** a conventional path under the hierarchy implied by the ID.
3. If such a file exists → this condition (do not treat as a bare “not found” only).

**Required warning substring:**

```text
Warning: Task file found but absent from index for {TASK_ID}: {PATH}
```

Optional follow-up guidance line (recommended):

```text
Next: re-register the file in the epic index (tasks[].file) or run backlog check
```

**show:** Emit warning; if the file is readable, show metadata/body from the file so
agents can recover content. Exit 0 when content was shown.

**claim:**

| | |
| --- | --- |
| **May mutate?** | **No** |
| **Blocking error substring** | `Cannot claim {TASK_ID} because it is not registered in the index.` |

Mutation requires index + file linkage so `save` can update both consistently.

**Recovery:**

1. `backlog show {TASK_ID}` — confirm orphan path and content.
2. Add an index entry under the correct epic `index.yaml`:

   ```yaml
   tasks:
     - id: T00N
       file: T00N-slug.todo
   ```

   (short id + filename relative to the epic directory), **or** re-create via
   `backlog add …` and move body content.
3. `backlog check` / `backlog tree {PARENT}` to verify the task appears.
4. `backlog claim {TASK_ID}`.

**Fixture:** `testdata/invalid-file-diagnostics/cases/file-absent-from-index/`  
**CLI live parity today:** Implemented (Go / Python / TypeScript).

---

## Claimability summary

| Code | claim may mutate? | show still displays content? |
| --- | --- | --- |
| `missing_indexed_file` | **No** | Metadata only (no body) |
| `malformed_frontmatter` | **Yes** (warn first) | Yes (raw/body best-effort) |
| `id_path_mismatch` | **Yes** (warn first; address by index ID) | Yes |
| `file_absent_from_index` | **No** | Yes (from orphan file) |

## Message prefix conventions

- Warnings: `Warning:` (styled, but strip-ANSI tests match the bare word).
- Blocking errors: include `Cannot claim` for claim mutation denials; prefer
  `Error:` where the client already uses that prefix (Python/TS).
- Always include the **task id** and, when known, the **file path**.
- Prefer one actionable next command in prose or a `Next:` block.

## JSON diagnostics (optional / future)

If show/claim grow structured output, emit an additive array without breaking text
mode defaults:

```json
{
  "diagnostics": [
    {
      "code": "missing_indexed_file",
      "level": "warning",
      "task_id": "P1.M1.E1.T001",
      "path": "01-phase/01-ms/01-epic/T001-missing.todo",
      "message": "Task file missing for P1.M1.E1.T001: …",
      "claimable": false,
      "recovery": [
        "backlog check",
        "restore or fix file path in epic index.yaml",
        "backlog claim P1.M1.E1.T001"
      ]
    }
  ]
}
```

`claimable` must match the table above. Until JSON exists, tests assert **text
substrings** from [`testdata/invalid-file-diagnostics/expected/messages.json`](../../testdata/invalid-file-diagnostics/expected/messages.json).

## Parity requirements (T002 checklist)

- [x] Go implements all four conditions in `show` and `claim` per this contract.
- [x] Python and TypeScript match Go message substrings and claimability.
- [x] Lookup remains by **index-derived ID** for `id_path_mismatch`.
- [x] Orphan resolution for `file_absent_from_index` on show; claim blocked with
      the specified error text.
- [x] Focused tests in all three clients pass against the shared fixtures.
- [x] Multiple warnings can appear together when applicable (e.g. mismatch + other
      non-missing issues).

## Related commands

- `backlog check` — tree-wide integrity (missing files, deps, cycles). Complements
  but does not replace show/claim diagnostics.
- `backlog cat` — raw file dump; should not hide content when frontmatter is bad.
- `backlog edit` — already blocks on missing files (same spirit as claim).
