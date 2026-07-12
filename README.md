```
  ━━━━─────━━━━━ ╌ ╌ ━━━━━═━═━═━══━━━━━━━━━━━━━═━━━═━━━━━━━═━═━━━══━━━━══━━━━━─╌────╌──╌━
    ▀██▀▀ ██▄▄█ ████▀       ████▄ ▄█▀▀▄ ▄█▀▀▀ █▄▄█▀ ██    ▄█▀▀▄ ▄█▀▀▀ ▄██▀▀
     ██   ██  █ ██▄▄▄       ██▄▄▀ ██▀▀█ ▀█▄▄▄ █▀▀█▄ ██▄▄▄ ▀█▄▄▀ ▀█▄██ ▄▄██▀
    ───────────────────────────────────────────────────────────────────────
      ▀██▀▀ ██▄▄█ ████▀       ████▄ ▄█▀▀▄ ▄█▀▀▀ █▄▄█▀ ██    ▄█▀▀▄ ▄█▀▀▀ ▄██▀▀
       ██   ██  █ ██▄▄▄       ██▄▄▀ ██▀▀█ ▀█▄▄▄ █▀▀█▄ ██▄▄▄ ▀█▄▄▀ ▀█▄██ ▄▄██▀
      ───────────────────────────────────────────────────────────────────────
        ▀██▀▀ ██▄▄█ ████▀       ████▄ ▄█▀▀▄ ▄█▀▀▀ █▄▄█▀ ██    ▄█▀▀▄ ▄█▀▀▀ ▄██▀▀
         ██   ██  █ ██▄▄▄       ▓█▄▄▀ ██▀▀█ ▀█▄▄▄ █▀▀█▄ ██▄▄▓ ▀█▄▓▀ ▀█▄██ ▄▄██▀
        ────────────────────────────────────────────────── ────────────────────
            ·   ░ · · █▓▓▓╎·      ▓▓▓█▒ ░█▀·▄ ▓█▓▀▓ ▓▓▓█▀ █▓    ▄▓▓▓▓ ▄▓▀▓▓ ▄██▀▀
           ░· ·   ·░  █▓▄▓▓ ░     ██▄▓▓ ▓▓▀▓█ ▓█▓▓▓ █▓▓▓▓ ▓▓▄▓▄ ▓█▓▓· ▀▓▄▓▓ ▄▓ ╎▀
           ───╌────────────── ─── ───────── ─ ─ ──·───── ── ─── ───   ──·──── ───
            ·█·▀▓ ▒▓▓▓▓ ▒▓▓▓▓       ▒▒▓▓▓ ▓▒▓▓▓ ▒ ▓▓▓ ▓▓▒▒▒  · ░· ▒ ▓▒▒ ▓▒▓▒  · ··
             ██   ▓▓  ▓ ▓▒▓▓▓       ▓▓▒▓▓  ▒▓▒▓ ▓▒▒▓▓ ▓▓▓▒▒ ░  ░  ▓▓▓▒▓ ░▓▒▒▒ ░  ░░
            ╌── ─  ── ─ ── ───────  ──── ── ── ── ─   ───·─ ──   ───   ─╌╌──── ────
              ·█▒▓▒ ░░·░  ▒▒▒ ╎·      ▒▒▒▓▒ ▒·▒▒▒  ░ ░· ░▒▒▒▒╎▒▓    ░·  ·  ░  ░ ▒▓▒▒▓
                ▓▓   ░ ░░· ▒▓▓▓▒ ░   · ▒▒▒▒▒ ·▓░▒▒       ·░ ·░┃▓▒▒░▒  · ░   ·    ▒▓▒▒
               ── ──  ─── ─  ───  ──────· ── ── ──·─· ── ─ ─   ╌ ─ ──╌── ──── ─ ─╌·─·
                ▓░▒▒▒  · ░  ▒░░▒░       ·░░ ·  ▒·░░ ░░··░  ░·· │ ░    ░░·░░ ░     ░·░░▒
                  ▒▒ ░ ··· ░ ▒▒▒ ░·   ·  ··░ · ░░░░· ░  ░    ░░ │░░░░  ░░ ░· ░···  ▒▒░·▓
                          ░               ·  ·░░ ·    · ··           ░        ·
                           ·             ░░ ░  ░·    ▒ ░  ▒         ░       ░
                           ·              ░··░ ░ ░ ░  ··░          ░        ░
                            ·              · ·   ·      ·          ·       ·
                              ·             ··  ·     ···         ·       ·
```

# The Backlogs

A CLI for hierarchical project backlog management. Phases, milestones, epics, tasks — stored as plain files on disk. Dependency-aware scheduling, multi-agent workflows, and a critical-path engine.

Dual implementation: **Python** (`backlog/`) and **TypeScript/Bun** (`backlog_ts/`), kept in behavioral sync.

## Install

### Prebuilt binary (recommended)

The Go client is the canonical build. Download a binary for your platform from
the [latest release](https://github.com/XertroV/backlog/releases/latest) — targets
are `linux`, `darwin`, `windows` × `amd64`, `arm64`:

```bash
# example: Linux amd64
gh release download --repo XertroV/backlog --pattern 'backlog-linux-amd64' --pattern 'SHA256SUMS'
sha256sum -c SHA256SUMS --ignore-missing        # verify
chmod +x backlog-linux-amd64
mv backlog-linux-amd64 ~/.local/bin/backlog     # somewhere on your PATH
```

Releases are cut by pushing a `vX.Y.Z` tag; see [`RELEASE.md`](RELEASE.md) for the
full procedure.

### Python client (from source)

One-shot installer (Linux/macOS, no sudo required):

```bash
./install.sh
```

This clones the repo into `~/.backlogs/backlogs` by default, creates a virtualenv, installs the Python CLI, and places `backlog` and `bl` in that environment's `bin/` directory.

You can override defaults with:

- `BACKLOG_REPO_URL` (default: `https://github.com/XertroV/tasks.git`)
- `BACKLOG_CLONE_DIR` (must end with `backlogs`)

Local editable (for development):

```bash
python -m venv .venv && source .venv/bin/activate
pip install -e ".[dev]"
```

Create `backlog` and `bl` symlinks in `~/.local/bin`:

```bash
make install-python-with-links
```

If you only want to manage links after install:

```bash
make python-link
make python-unlink
```

User-global (Arch-friendly, offline):

```bash
# system deps (Arch)
sudo pacman -S --needed python-click python-pyyaml python-rich python-networkx python-setuptools

# install
./scripts/install-anywhere.sh
```

After install, `backlog` and `bl` are equivalent entry points. `backlog.py` works as a repo-local wrapper that auto-reexecs into `.venv/bin/python`.

## Quick start

```bash
backlog init --project my-project    # create a .backlog/ tree
backlog grab                         # auto-claim next available task
backlog show                         # inspect current task
backlog done                         # mark complete, see what's unblocked
backlog cycle                        # done + grab next in one step
backlog dash                         # one-screen status overview
```

## How it works

Everything lives in `.backlog/` as nested directories with YAML indexes and Markdown `.todo` files:

```
.backlog/
  index.yaml
  01-phase-name/
    index.yaml
    01-milestone-name/
      index.yaml
      01-epic-name/
        index.yaml
        T001-some-task.todo
```

IDs are hierarchical: `P1` > `P1.M1` > `P1.M1.E1` > `P1.M1.E1.T001`. Task files use YAML frontmatter for metadata (status, estimate, complexity, priority, dependencies, tags, claims).

The engine builds a dependency graph across all tasks, computes a CCPM-style critical path, and uses that to decide what to work on next. It tracks claim ownership, agent heartbeats, and stale-claim detection for multi-agent environments.

## Commands

**Core task operations:**

| Command | What it does |
|---|---|
| `list` | Filter/view tasks (`--available`, `--progress`, `--json`, `--bugs`, `--ideas`) |
| `tree` | Full hierarchical view (`--depth`, `--details`, `--unfinished`) |
| `show [ID...]` | Detailed info (uses current context if no ID) |
| `next` | Next task on the critical path |
| `claim ID` | Claim a specific task |
| `done [ID]` | Complete task, show newly unblocked work |
| `update ID STATUS` | Manual status transition (`--reason` for blocked/rejected/cancelled) |
| `set ID` | Modify task properties (status, priority, complexity, estimate, tags, deps) |
| `append ID [TEXT]` | Append text to a task body (from arguments or piped stdin) |
| `sync` | Recalculate stats and critical path |
| `check` | Consistency checks (missing files, broken deps, cycles, ID integrity) |

**Workflow shortcuts:**

| Command | What it does |
|---|---|
| `grab` | Auto-claim next work (`--single`, `--multi`, sibling batching) |
| `cycle [ID]` | `done` + auto-claim next |
| `work [ID\|--clear]` | Set/show/clear working context |
| `blocked` | Mark blocked (`--reason`) |
| `skip` | Skip current task |
| `handoff` | Transfer to another agent (`--to`, `--notes`) |
| `unclaim` | Release claim |
| `why` | Explain dependency readiness |

**Reporting and analysis:**

| Command | What it does |
|---|---|
| `dash` | One-screen status dashboard |
| `search PATTERN` | Full-text search across tasks |
| `blockers` | Dependency blocker analysis (`--deep`, `--suggest`) |
| `timeline` / `tl` | ASCII Gantt view |
| `report progress` | Progress summary |
| `report velocity` | Velocity over time (`--days N`) |
| `report estimate-accuracy` | Estimate vs actual comparison |

**Project management:**

| Command | What it does |
|---|---|
| `add EPIC_ID` | Add task to an epic |
| `add-epic`, `add-milestone`, `add-phase` | Create higher-level items |
| `move SOURCE_ID --to DEST_ID` | Move task->epic, epic->milestone, or milestone->phase (with renumbering) |
| `bug` | Quick bug report |
| `idea "..."` | Capture a feature idea for later decomposition |
| `init` | Initialize a new `.backlog/` project |
| `migrate` | Move `.tasks/` to `.backlog/` (with symlink compat) |

**Agent tooling:**

| Command | What it does |
|---|---|
| `session start\|heartbeat\|end\|list\|clean` | Agent session tracking |
| `unclaim-stale` | Release stale in-progress claims |
| `agents` | Print AGENTS.md snippets (`--profile short\|medium\|long\|all`) |
| `skills install` | Install planning skills for Codex, Claude, OpenCode |
| `schema` | Show schema details for `.backlog` file formats |
| `data export\|summary` | Data export |

## Common workflows

**Claim-work-complete loop:**

```bash
backlog grab            # pick up next task
backlog show            # read the details
# ... do the work ...
backlog done            # complete it
backlog grab            # next one
```

Or just `backlog cycle` to combine done + grab.

**Pause or hand off:**

```bash
backlog blocked --reason "waiting on API key"
backlog handoff --to agent-2 --notes "impl done, needs tests"
```

**Health check:**

```bash
backlog dash
backlog blockers --deep --suggest
backlog report velocity --days 14
```

## AI agent skills

The `skills install` command exports planning workflows for AI coding agents:

```bash
backlog skills install plan-ingest    # install for codex + claude + opencode
backlog skills install start-tasks    # execution-loop skill
backlog skills install backlog-howto  # backlog manual/overview skill
backlog skills install all --artifact both   # everything
```

Skills land in the standard locations (`.agents/skills/`, `.claude/skills/`, `.config/opencode/skills/`). Use `--scope global` for user-wide install, `--dir ./path` for portable export.

Canonical source for `backlog-howto` is `bl_skills/backlog-howto/SKILL.md`. Refresh generated client constants with:

```bash
python bl_skills/sync_backlog_howto.py
```

## TypeScript implementation

The `backlog_ts/` directory contains a full-parity TypeScript implementation running on Bun:

```bash
cd backlog_ts
bun install
bun run bin/backlog list --available
bun test                              # run tests
bun run parity                        # cross-implementation parity checks
```

## Migration from `tasks`

If you have an existing `.tasks/` directory:

```bash
backlog migrate
```

This moves `.tasks/` to `.backlog/` and creates a compatibility symlink.

## Tests

```bash
# Python
pytest -q

# TypeScript
cd backlog_ts && bun test

# Cross-implementation parity
cd backlog_ts && bun run parity
```

## Context files

| File | Purpose |
|---|---|
| `.backlog/.context.yaml` | Current/sibling/multi-task working context |
| `.backlog/.sessions.yaml` | Active agent heartbeats |
| `.backlog/config.yaml` | Optional overrides (agent defaults, stale thresholds, timeline settings) |
