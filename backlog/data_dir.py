"""Data directory detection and migration for The Backlogs."""

import os
import sys
import shutil
from pathlib import Path
from typing import Optional, Tuple

BACKLOG_DIR = ".backlog"
TASKS_DIR = ".tasks"

MIGRATION_COMMENT = (
    "<!-- CLI migrated: 'tasks' → 'backlog' (alias 'bl' also works). -->\n"
)

KNOWN_COMMANDS = [
    "list",
    "ls",
    "show",
    "next",
    "claim",
    "grab",
    "done",
    "cycle",
    "work",
    "update",
    "sync",
    "check",
    "unclaim-stale",
    "add",
    "add-epic",
    "add-milestone",
    "add-phase",
    "move",
    "idea",
    "bug",
    "fixed",
    "blocked",
    "skip",
    "unclaim",
    "handoff",
    "why",
    "dash",
    "search",
    "blockers",
    "timeline",
    "tl",
    "session",
    "report",
    "data",
    "edit",
    "schema",
    "skills",
    "migrate",
]

BACKLOG_AGENTS_BOOTSTRAP = """# AGENTS.md

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
"""


def _ensure_backlog_agents(data_dir: Path) -> None:
    """Ensure .backlog/AGENTS.md and .backlog/CLAUDE.md exist with the expected contents."""
    if not data_dir.exists():
        return

    agents_path = data_dir / "AGENTS.md"
    if not agents_path.exists():
        agents_path.write_text(BACKLOG_AGENTS_BOOTSTRAP)

    claude_path = data_dir / "CLAUDE.md"
    needs_link = True
    if claude_path.exists():
        if claude_path.is_symlink():
            try:
                needs_link = claude_path.resolve() != agents_path.resolve()
            except OSError:
                needs_link = True
        else:
            needs_link = True

    if needs_link:
        if claude_path.exists() or claude_path.is_symlink():
            if claude_path.is_dir() and not claude_path.is_symlink():
                shutil.rmtree(claude_path)
            else:
                claude_path.unlink()
        os.symlink("AGENTS.md", claude_path)


def get_data_dir() -> Path:
    """Get the data directory path, preferring .backlog over .tasks.

    Searches up parent directories to support running from nested folders.
    """
    current = Path.cwd()
    first_tasks = None
    while True:
        backlog_path = current / BACKLOG_DIR
        if backlog_path.exists():
            _ensure_backlog_agents(backlog_path)
            return backlog_path

        if first_tasks is None:
            tasks_path = current / TASKS_DIR
            if tasks_path.exists():
                first_tasks = tasks_path

        parent = current.parent
        if parent == current:
            break
        current = parent

    if first_tasks is not None:
        return first_tasks

    raise FileNotFoundError(
        f"No data directory found. Expected {BACKLOG_DIR}/ or {TASKS_DIR}/"
    )


def get_data_dir_name() -> str:
    """Get the name of the data directory (.backlog or .tasks)."""
    return get_data_dir().name


def needs_migration() -> bool:
    """Check if migration from .tasks to .backlog is needed."""
    return Path(TASKS_DIR).exists() and not Path(BACKLOG_DIR).exists()


def is_symlink_to(path: Path, target: Path) -> bool:
    """Check if path is a symlink pointing to target."""
    if not path.is_symlink():
        return False
    try:
        return os.readlink(path) == str(target) or path.resolve() == target.resolve()
    except OSError:
        return False


def is_interactive() -> bool:
    """Check if we're running in an interactive TTY."""
    return sys.stdin.isatty() and sys.stdout.isatty()


def update_md_file(file_path: Path) -> bool:
    """Update a markdown file, replacing 'tasks' CLI references with 'backlog'.

    Returns True if file was updated, False if skipped or no changes needed.
    """
    if not file_path.exists():
        return False

    try:
        content = file_path.read_text()
    except Exception:
        return False

    if MIGRATION_COMMENT in content:
        return False

    lines = content.split("\n")
    new_lines = []

    for line in lines:
        new_line = line

        for cmd in KNOWN_COMMANDS:
            new_line = new_line.replace(f"`tasks {cmd}", f"`backlog {cmd}")
            new_line = new_line.replace(f"    tasks {cmd}", f"    backlog {cmd}")
            new_line = new_line.replace(f"- tasks {cmd}", f"- backlog {cmd}")

        new_line = new_line.replace("`tasks --", "`backlog --")
        new_line = new_line.replace("`tasks [", "`backlog [")
        new_line = new_line.replace("python -m tasks", "python -m backlog")
        new_line = new_line.replace("./tasks.py", "./backlog.py")
        new_line = new_line.replace("`tasks/`", "`backlog/`")
        new_line = new_line.replace('"tasks/', '"backlog/')

        new_lines.append(new_line)

    new_content = "\n".join(new_lines)

    if new_content != content:
        new_content = MIGRATION_COMMENT + new_content
        file_path.write_text(new_content)
        return True

    return False


def migrate_data_dir(
    create_symlink: bool = True, force: bool = False
) -> Tuple[bool, str]:
    """Migrate .tasks/ to .backlog/ with optional symlink.

    Returns (success, message).
    """
    tasks_path = Path(TASKS_DIR)
    backlog_path = Path(BACKLOG_DIR)

    if backlog_path.exists():
        if is_symlink_to(tasks_path, backlog_path):
            try:
                _ensure_backlog_agents(backlog_path)
            except OSError as exc:
                return False, f"Failed to initialize AGENTS files: {exc}"
            return True, "Already migrated (.tasks is symlink to .backlog)"
        if tasks_path.exists() and not tasks_path.is_symlink():
            if not force:
                return (
                    False,
                    "Both .tasks/ and .backlog/ exist. Use --force to proceed.",
                )
            try:
                _ensure_backlog_agents(backlog_path)
            except OSError as exc:
                return False, f"Failed to initialize AGENTS files: {exc}"
            return True, "Both directories exist (force mode - using .backlog/)"
        try:
            _ensure_backlog_agents(backlog_path)
        except OSError as exc:
            return False, f"Failed to initialize AGENTS files: {exc}"
        return True, "Already migrated (.backlog/ exists)"

    if not tasks_path.exists():
        return False, "No .tasks/ directory found to migrate"

    try:
        tasks_path.rename(backlog_path)
    except OSError as e:
        return False, f"Failed to rename .tasks/ to .backlog/: {e}"

    if create_symlink:
        try:
            os.symlink(BACKLOG_DIR, tasks_path)
        except OSError as e:
            return False, f"Migrated but failed to create symlink: {e}"

    md_files = [
        Path("AGENTS.md"),
        Path("CLAUDE.md"),
    ]

    md_files.extend(Path(".").glob("*.md"))

    updated_files = []
    for md_file in md_files:
        if md_file.is_file() and md_file.name not in ["README.md", "PARITY_DIFFS.md"]:
            if update_md_file(md_file):
                updated_files.append(md_file.name)

    try:
        _ensure_backlog_agents(backlog_path)
    except OSError as exc:
        return False, f"Failed to initialize AGENTS files: {exc}"

    msg = f"Migrated .tasks/ → .backlog/"
    if create_symlink:
        msg += " (with symlink)"
    if updated_files:
        msg += f"\nUpdated doc files: {', '.join(updated_files)}"

    return True, msg


def check_and_prompt_migration() -> Optional[str]:
    """Check if migration is needed and prompt if interactive.

    Returns None if no action needed, or a message if migration was done or recommended.
    """
    if not needs_migration():
        return None

    if is_interactive():
        print(
            f"Found {TASKS_DIR}/ directory. Migrate to {BACKLOG_DIR}/? [Y/n] ",
            end="",
            flush=True,
        )
        try:
            response = input().strip().lower()
            if response in ("", "y", "yes"):
                success, msg = migrate_data_dir(create_symlink=True)
                if success:
                    return msg
                return f"Migration failed: {msg}"
        except (EOFError, KeyboardInterrupt):
            print()
            return None
    else:
        return f"Warning: {TASKS_DIR}/ found. Run 'backlog migrate' to migrate to {BACKLOG_DIR}/"

    return None
