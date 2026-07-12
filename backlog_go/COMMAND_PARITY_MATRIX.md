# backlog_go command parity matrix

This document tracks behavior against the Python and TypeScript reference
implementations for milestone release planning.

## Status legend

- ✅ parity-targeted
- ⚠️ implemented but with noted limitations
- ⚙️ implemented as explicit compatibility stub
- ❌ not implemented in this milestone

## Matrix

| Command | go | parity status |
|---|---|---|
| `init` | ✅ | Fully implemented |
| `list`, `ls` | ✅ | Parity-targeted command output |
| `tree` | ✅ | Parity-targeted |
| `show` | ✅ | Parity-targeted |
| `add` | ✅ | Parity-targeted |
| `add-epic` | ✅ | Parity-targeted |
| `add-milestone` | ✅ | Parity-targeted |
| `add-phase` | ✅ | Parity-targeted |
| `set` | ✅ | Parity-targeted |
| `append` | ✅ | Parity-targeted |
| `update` | ✅ | Parity-targeted |
| `claim` | ✅ | Parity-targeted |
| `done` | ✅ | Parity-targeted |
| `unclaim` | ✅ | Parity-targeted |
| `grab` | ✅ | Parity-targeted |
| `cycle` | ✅ | Parity-targeted |
| `work` | ✅ | Parity-targeted |
| `next` | ✅ | Parity-targeted |
| `preview` | ✅ | Parity-targeted |
| `move` | ✅ | Parity-targeted |
| `sync` | ✅ | Parity-targeted |
| `log` | ✅ | Parity-targeted |
| `dash` | ✅ | Parity-targeted |
| `blocked` | ✅ | Parity-targeted |
| `skip` | ✅ | Implemented and tested |
| `unclaim-stale` | ✅ | Implemented and tested |
| `handoff` | ✅ | Implemented and tested |
| `admin` | ⚙️ | Compatibility stub with explicit guidance |
| `check` | ✅ | Implemented with JSON/text output |
| `search` | ✅ | Implemented with filtering support |
| `blockers` | ✅ | Implemented with root-blocker analysis |
| `why` | ✅ | Implemented with dependency rationale |
| `timeline`, `tl` | ✅ | Implemented |
| `report`, `r` | ✅ | `progress`, `velocity`, `estimate-accuracy` implemented |
| `data` | ✅ | `summary` and `export` implemented |
| `schema` | ✅ | Implemented with JSON/text output |
| `session` | ✅ | `start`, `heartbeat`, `list`, `end`, `clean` implemented |
| `agents` | ✅ | Implemented |
| `skills` | ✅ | `install` implemented for codex/claude/opencode artifacts |
| `idea` | ✅ | Implemented |
| `bug` | ✅ | Implemented |

## Current known limitation

- The Go CLI uses `.backlog` as the primary project directory while still
  supporting `.tasks` fallback for compatibility.
- Legacy aliases not present in Python/TypeScript (`grants`, `sprint`, `unknown`)
  are intentionally excluded from the default command surface.
