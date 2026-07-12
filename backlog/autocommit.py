"""Shared auto-commit helpers for CLI commands.

Commands can wrap their write-path operations with this module so that new files
are automatically committed when there are no staged git changes.
"""

from __future__ import annotations

import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Dict, Optional


def _run_git(args: list[str], cwd: Optional[Path | str] = None) -> str:
    process = subprocess.run(
        ["git", *args],
        cwd=str(cwd) if cwd is not None else None,
        capture_output=True,
        text=True,
    )
    output = (process.stdout or "") + (process.stderr or "")
    if process.returncode != 0:
        message = (output.strip() or "git command failed").strip()
        raise RuntimeError(message)
    return (process.stdout or "").rstrip("\r\n")


def _git_status_snapshot(cwd: Optional[Path | str] = None) -> Dict[str, str]:
    status_text = _run_git(["status", "--porcelain=v1"], cwd)
    status: Dict[str, str] = {}
    for line in status_text.splitlines():
        if len(line) < 3:
            continue
        state = line[0:2]
        path = line[3:].rstrip("\r")
        if not path:
            continue
        if state.startswith("R"):
            parts = path.split(" -> ", 1)
            if len(parts) == 2:
                status[parts[1]] = state
                continue
        status[path] = state
    return status


def _has_staged_changes(status: Dict[str, str]) -> bool:
    for state in status.values():
        if state and state[0] not in [" ", "?"]:
            return True
    return False


def _changed_tracked_paths(before: Dict[str, str], after: Dict[str, str]) -> list[str]:
    paths = set(after.keys())
    for path in before:
        if path not in after or before[path] != after[path]:
            paths.add(path)
    for path in before:
        if path not in after:
            paths.add(path)
    return sorted(paths)


def _normalize_message_fragment(value: Optional[str]) -> str:
    if not value:
        return ""
    return " ".join((value or "").replace("\r", " ").replace("\n", " ").split())


def _auto_commit_message(command: str, metadata: tuple[str, str] | None = None) -> str:
    prefix = "backlog " + command
    if command == "add":
        prefix = "bl add"
    elif command == "edit":
        prefix = "bl edit"
    elif command == "bug":
        prefix = "bl bug"
    elif command == "idea":
        prefix = "bl idea"
    elif command == "claim":
        prefix = "bl claim"
    elif command == "grab":
        prefix = "bl grab"
    elif command == "done":
        prefix = "bl done"
    elif command == "unclaim":
        prefix = "bl unclaim"
    elif command == "undone":
        prefix = "bl undone"
    elif command == "set":
        prefix = "bl set"
    elif command == "append":
        prefix = "bl append"

    if metadata is None:
        return prefix

    item_id, item_title = metadata
    item_id = _normalize_message_fragment(item_id)
    item_title = _normalize_message_fragment(item_title)
    if item_id and item_title:
        return f"{prefix} {item_id}: {item_title}"
    if item_id:
        return f"{prefix} {item_id}"
    if item_title:
        return f"{prefix} {item_title}"
    return prefix


def _is_previous_commit_from_bl_add(cwd: Optional[Path | str] = None) -> bool:
    message = _run_git(["log", "-1", "--pretty=%B"], cwd).strip()
    if not message:
        return False
    return message.split("\n", 1)[0].startswith("bl add")


def _is_previous_commit_unpushed(cwd: Optional[Path | str] = None) -> bool:
    try:
        _run_git(["rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"], cwd)
    except Exception:
        return True

    ahead_raw = _run_git(["rev-list", "--count", "@{u}..HEAD"], cwd).strip()
    try:
        return int(ahead_raw) > 0
    except ValueError:
        return False


def _git_add(paths: list[str], cwd: Optional[Path | str] = None) -> None:
    if not paths:
        return
    _run_git(["add", "--", *paths], cwd)


def _git_commit(message: str, cwd: Optional[Path | str] = None) -> None:
    _run_git(["commit", "-m", message], cwd)


def _git_amend_no_edit(cwd: Optional[Path | str] = None) -> None:
    _run_git(["commit", "--amend", "--no-edit"], cwd)


@dataclass
class _AutoCommitContext:
    has_staged: bool
    pre_status: Dict[str, str]
    can_amend_bl_add: bool
    prev_add_unpushed: bool


def _capture_auto_commit_context(cwd: Optional[Path | str] = None) -> _AutoCommitContext:
    pre_status = _git_status_snapshot(cwd)
    context = _AutoCommitContext(
        has_staged=_has_staged_changes(pre_status),
        pre_status=pre_status,
        can_amend_bl_add=False,
        prev_add_unpushed=False,
    )
    if context.has_staged:
        return context
    context.can_amend_bl_add = _is_previous_commit_from_bl_add(cwd)
    context.prev_add_unpushed = _is_previous_commit_unpushed(cwd)
    return context


def _execute_auto_commit(
    command: str,
    context: _AutoCommitContext,
    metadata: tuple[str, str] | None = None,
    cwd: Optional[Path | str] = None,
) -> None:
    post_status = _git_status_snapshot(cwd)
    changed_files = _changed_tracked_paths(context.pre_status, post_status)
    if not changed_files:
        return

    _git_add(changed_files, cwd)

    if command == "add" and context.can_amend_bl_add and context.prev_add_unpushed:
        try:
            _git_amend_no_edit(cwd)
            return
        except Exception:
            pass

    _git_commit(_auto_commit_message(command, metadata), cwd)


def run_with_auto_commit(
    command: str,
    action: Callable[[], tuple[str, str] | None],
    warn: Callable[[str], None] | None = None,
    cwd: Optional[Path | str] = None,
) -> None:
    """Run a command and auto-commit changed tracked files when safe.

    If we are not in a git repo or git commands fail, the command still runs.
    If there are staged changes before the command runs, we skip auto-commit.
    """

    context: _AutoCommitContext | None
    try:
        context = _capture_auto_commit_context(cwd)
    except Exception:
        context = None

    metadata = action()

    if context is None or context.has_staged:
        return

    try:
        _execute_auto_commit(command, context, metadata, cwd)
    except Exception as err:
        if warn is not None:
            warn(f"Auto-commit skipped: {err}")
