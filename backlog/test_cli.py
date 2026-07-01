"""Tests for tasks CLI commands."""

import pytest
import json
import yaml
import os
import subprocess
from pathlib import Path
from datetime import datetime, timedelta, timezone
from click.testing import CliRunner
from backlog.cli import cli
from backlog.loader import TaskLoader


@pytest.fixture
def runner():
    """Create a Click CLI test runner."""
    return CliRunner()


@pytest.fixture
def tmp_tasks_dir(tmp_path, monkeypatch):
    """Create a temporary .tasks directory with test data."""
    tasks_dir = tmp_path / ".tasks"
    tasks_dir.mkdir()

    # Create root index.yaml
    root_index = {
        "project": "Test Project",
        "description": "Test project for CLI tests",
        "timeline_weeks": 1,
        "phases": [
            {
                "id": "P1",
                "name": "Test Phase",
                "path": "01-test-phase",
                "status": "in_progress",
            }
        ],
    }
    with open(tasks_dir / "index.yaml", "w") as f:
        yaml.dump(root_index, f)

    # Create phase directory and index
    phase_dir = tasks_dir / "01-test-phase"
    phase_dir.mkdir()

    phase_index = {
        "milestones": [
            {
                "id": "P1.M1",
                "name": "Test Milestone",
                "path": "01-test-milestone",
                "status": "in_progress",
            }
        ],
    }
    with open(phase_dir / "index.yaml", "w") as f:
        yaml.dump(phase_index, f)

    # Create milestone directory and index
    milestone_dir = phase_dir / "01-test-milestone"
    milestone_dir.mkdir()

    milestone_index = {
        "epics": [
            {
                "id": "P1.M1.E1",
                "name": "Test Epic",
                "path": "01-test-epic",
                "status": "in_progress",
            }
        ],
    }
    with open(milestone_dir / "index.yaml", "w") as f:
        yaml.dump(milestone_index, f)

    # Create epic directory and index
    epic_dir = milestone_dir / "01-test-epic"
    epic_dir.mkdir()

    epic_index = {
        "tasks": [],  # Will be populated by create_task_file
    }
    with open(epic_dir / "index.yaml", "w") as f:
        yaml.dump(epic_index, f)

    # Change to temp directory
    monkeypatch.chdir(tmp_path)

    return tmp_path


@pytest.fixture
def tmp_tasks_dir_short_ids(tmp_path, monkeypatch):
    """Create a temporary .tasks directory using short IDs in index files."""
    tasks_dir = tmp_path / ".tasks"
    tasks_dir.mkdir()

    root_index = {
        "project": "Test Project",
        "description": "Test project for CLI tests",
        "timeline_weeks": 1,
        "phases": [
            {
                "id": "P1",
                "name": "Test Phase",
                "path": "01-test-phase",
                "status": "in_progress",
            }
        ],
    }
    with open(tasks_dir / "index.yaml", "w") as f:
        yaml.dump(root_index, f)

    phase_dir = tasks_dir / "01-test-phase"
    phase_dir.mkdir()
    with open(phase_dir / "index.yaml", "w") as f:
        yaml.dump(
            {
                "milestones": [
                    {
                        "id": "M1",
                        "name": "Test Milestone",
                        "path": "01-test-milestone",
                        "status": "in_progress",
                    }
                ]
            },
            f,
        )

    milestone_dir = phase_dir / "01-test-milestone"
    milestone_dir.mkdir()
    with open(milestone_dir / "index.yaml", "w") as f:
        yaml.dump(
            {
                "epics": [
                    {
                        "id": "E1",
                        "name": "Test Epic",
                        "path": "01-test-epic",
                        "status": "in_progress",
                    }
                ]
            },
            f,
        )

    epic_dir = milestone_dir / "01-test-epic"
    epic_dir.mkdir()
    with open(epic_dir / "index.yaml", "w") as f:
        yaml.dump({"tasks": []}, f)

    monkeypatch.chdir(tmp_path)
    return tmp_path


@pytest.fixture
def tmp_tasks_dir_hierarchy_cycle(tmp_path, monkeypatch):
    """Create a temporary .tasks directory with a hierarchy-only dependency cycle."""
    tasks_dir = tmp_path / ".tasks"
    tasks_dir.mkdir()

    root_index = {
        "project": "Test Project",
        "description": "Hierarchy cycle fixture",
        "timeline_weeks": 1,
        "phases": [
            {
                "id": "P1",
                "name": "Phase One",
                "path": "01-phase-one",
                "status": "in_progress",
                "depends_on": ["P2"],
            },
            {
                "id": "P2",
                "name": "Phase Two",
                "path": "02-phase-two",
                "status": "in_progress",
                "depends_on": ["P1"],
            },
        ],
    }
    with open(tasks_dir / "index.yaml", "w") as f:
        yaml.dump(root_index, f)

    for phase_id, phase_path, phase_name in (
        ("P1", "01-phase-one", "Phase One"),
        ("P2", "02-phase-two", "Phase Two"),
    ):
        phase_dir = tasks_dir / phase_path
        phase_dir.mkdir()
        with open(phase_dir / "index.yaml", "w") as f:
            yaml.dump(
                {
                    "milestones": [
                        {
                            "id": "M1",
                            "name": f"{phase_name} Milestone",
                            "path": "01-milestone",
                            "status": "in_progress",
                        }
                    ]
                },
                f,
            )

        milestone_dir = phase_dir / "01-milestone"
        milestone_dir.mkdir()
        with open(milestone_dir / "index.yaml", "w") as f:
            yaml.dump(
                {
                    "epics": [
                        {
                            "id": "E1",
                            "name": f"{phase_name} Epic",
                            "path": "01-epic",
                            "status": "in_progress",
                        }
                    ]
                },
                f,
            )

        epic_dir = milestone_dir / "01-epic"
        epic_dir.mkdir()
        with open(epic_dir / "index.yaml", "w") as f:
            yaml.dump(
                {
                    "tasks": [
                        {
                            "id": "T001",
                            "title": f"{phase_name} Task",
                            "file": "T001-task.todo",
                            "status": "pending",
                            "estimate_hours": 1.0,
                            "complexity": "medium",
                            "priority": "high",
                            "depends_on": [],
                            "tags": [],
                        }
                    ]
                },
                f,
            )

        task_file = epic_dir / "T001-task.todo"
        task_id = f"{phase_id}.M1.E1.T001"
        with open(task_file, "w") as f:
            f.write(
                f"""---
id: {task_id}
title: {phase_name} Task
status: pending
estimate_hours: 1.0
complexity: medium
priority: high
depends_on: []
tags: []
---

# {phase_name} Task
"""
            )

    monkeypatch.chdir(tmp_path)
    return tmp_path


def create_task_file(
    tasks_dir,
    task_id,
    title,
    status="pending",
    claimed_by=None,
    claimed_at=None,
    started_at=None,
    completed_at=None,
    depends_on=None,
):
    """Helper to create a .todo task file."""
    # Parse task_id to determine path (e.g., P1.M1.E1.T001)
    parts = task_id.split(".")
    phase_num = parts[0][1:]  # P1 -> 1
    milestone_num = parts[1][1:]  # M1 -> 1
    epic_num = parts[2][1:]  # E1 -> 1
    task_num = parts[3][1:]  # T001 -> 001

    epic_dir = (
        tasks_dir
        / ".tasks"
        / f"0{phase_num}-test-phase"
        / f"0{milestone_num}-test-milestone"
        / f"0{epic_num}-test-epic"
    )
    epic_dir.mkdir(parents=True, exist_ok=True)

    task_filename = f"T{task_num}-test-task.todo"
    task_file = epic_dir / task_filename
    # File path relative to .tasks/ root
    task_file_relative = task_filename  # Just the filename, loader constructs full path

    frontmatter = {
        "id": task_id,
        "title": title,
        "status": status,
        "estimate_hours": 2.0,
        "complexity": "medium",
        "priority": "high",
        "depends_on": depends_on or [],
        "tags": ["test"],
    }

    if claimed_by:
        frontmatter["claimed_by"] = claimed_by
    if claimed_at:
        frontmatter["claimed_at"] = claimed_at.isoformat()
    if started_at:
        frontmatter["started_at"] = started_at.isoformat()
    elif claimed_at:
        frontmatter["started_at"] = claimed_at.isoformat()
    if completed_at:
        frontmatter["completed_at"] = completed_at.isoformat()

    content = f"""---
{yaml.dump(frontmatter, default_flow_style=False)}---

# {title}

Test task description.

## Requirements

- First requirement
- Second requirement
- Third requirement

## Acceptance Criteria

- Acceptance criterion 1
- Acceptance criterion 2
"""

    with open(task_file, "w") as f:
        f.write(content)

    # Update epic index.yaml with task entry
    epic_index_path = epic_dir / "index.yaml"
    if epic_index_path.exists():
        with open(epic_index_path) as f:
            epic_index = yaml.safe_load(f) or {}
    else:
        epic_index = {}

    if "tasks" not in epic_index:
        epic_index["tasks"] = []

    # Add task to the epic index
    task_entry = {
        "id": task_id,
        "title": title,
        "file": task_file_relative,
        "status": status,
        "estimate_hours": 2.0,
        "complexity": "medium",
        "priority": "high",
    }
    epic_index["tasks"].append(task_entry)

    with open(epic_index_path, "w") as f:
        yaml.dump(epic_index, f)

    return task_file


def create_multi_task_epic(tasks_dir, num_tasks, epic_id="P1.M1.E1"):
    """Create an epic with multiple tasks for testing."""
    tasks = []
    for i in range(1, num_tasks + 1):
        task_id = f"{epic_id}.T{i:03d}"
        create_task_file(tasks_dir, task_id, f"Task {i}")
        tasks.append(task_id)
    return tasks


def create_bug_file(tasks_dir, bug_id, title, status="pending", depends_on=None):
    """Helper to create a .todo bug file and index entry."""
    bugs_dir = tasks_dir / ".tasks" / "bugs"
    bugs_dir.mkdir(parents=True, exist_ok=True)

    bug_filename = f"{bug_id.lower()}-test-bug.todo"
    bug_file = bugs_dir / bug_filename
    bug_depends_on = depends_on or []

    frontmatter = {
        "id": bug_id,
        "title": title,
        "status": status,
        "estimate_hours": 3.0,
        "complexity": "high",
        "priority": "critical",
        "depends_on": bug_depends_on,
        "tags": ["bug"],
    }

    content = f"""---
{yaml.dump(frontmatter, default_flow_style=False)}---

# {title}
"""
    with open(bug_file, "w") as f:
        f.write(content)

    bug_index_path = bugs_dir / "index.yaml"
    bug_index = {"bugs": []}
    if bug_index_path.exists():
        with open(bug_index_path) as f:
            bug_index = yaml.safe_load(f) or {"bugs": []}
    bug_index.setdefault("bugs", []).append({"id": bug_id, "file": bug_filename})

    with open(bug_index_path, "w") as f:
        yaml.dump(bug_index, f)

    return bug_file


def run_git(tasks_dir, *args):
    """Run a git command in a backlog fixture directory."""
    result = subprocess.run(["git", *args], cwd=str(tasks_dir), capture_output=True, text=True)
    if result.returncode != 0:
        message = (result.stdout + result.stderr).strip()
        raise RuntimeError(message or f"git command failed: {' '.join(args)}")
    return (result.stdout or "").strip()


def initialize_test_git_repo(tasks_dir):
    """Initialize a temporary git repo with current fixture files committed."""
    run_git(tasks_dir, "init", "-q")
    run_git(tasks_dir, "config", "user.name", "backlog-tests")
    run_git(tasks_dir, "config", "user.email", "backlog@tests.local")
    run_git(tasks_dir, "add", ".tasks")
    run_git(tasks_dir, "commit", "-m", "baseline")


def git_commit_count(tasks_dir):
    """Return the current commit count for the repo."""
    try:
        return int(run_git(tasks_dir, "rev-list", "--count", "HEAD"))
    except ValueError as exc:
        raise AssertionError(f"invalid git commit count output: {run_git(tasks_dir, 'rev-list', '--count', 'HEAD')}") from exc


class TestClaimCommand:
    """Tests for the claim command."""

    def test_claim_shows_metadata(self, runner, tmp_tasks_dir):
        """claim should display estimate and agent info."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")

        result = runner.invoke(cli, ["claim", "P1.M1.E1.T001", "--agent=test-agent"])

        assert result.exit_code == 0
        assert "Estimate:" in result.output
        assert "2.0 hours" in result.output
        assert "Agent:" in result.output
        assert "test-agent" in result.output

    def test_claim_shows_file_path(self, runner, tmp_tasks_dir):
        """claim should display the .todo file path."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")

        result = runner.invoke(cli, ["claim", "P1.M1.E1.T001", "--agent=test-agent"])

        assert result.exit_code == 0
        assert "File:" in result.output
        assert ".tasks/" in result.output

    def test_claim_shows_todo_contents(self, runner, tmp_tasks_dir):
        """claim should display .todo file contents."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")

        result = runner.invoke(cli, ["claim", "P1.M1.E1.T001", "--agent=test-agent"])

        assert result.exit_code == 0
        # claim dumps raw file content between separator lines
        assert "## Requirements" in result.output
        assert "First requirement" in result.output

    def test_claim_no_content_flag(self, runner, tmp_tasks_dir):
        """--no-content should suppress .todo file contents."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")

        result = runner.invoke(
            cli, ["claim", "P1.M1.E1.T001", "--agent=test-agent", "--no-content"]
        )

        assert result.exit_code == 0
        # Should NOT show file contents
        assert "## Requirements" not in result.output
        # But metadata should still appear
        assert "Estimate:" in result.output
        assert "Agent:" in result.output

    def test_claim_accepts_multiple_task_ids(self, runner, tmp_tasks_dir):
        """claim should accept one or more TASK_ID arguments."""
        task_one = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")
        task_two = create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task Two")

        result = runner.invoke(
            cli,
            [
                "claim",
                "P1.M1.E1.T001",
                "P1.M1.E1.T002",
                "--agent=test-agent",
            ],
        )

        assert result.exit_code == 0
        assert "✓ Claimed: P1.M1.E1.T001 - Task One" in result.output
        assert "✓ Claimed: P1.M1.E1.T002 - Task Two" in result.output
        assert "status: in_progress" in task_one.read_text()
        assert "status: in_progress" in task_two.read_text()

    def test_claim_non_task_id_falls_back_to_show(self, runner, tmp_tasks_dir):
        """claiming non-task IDs should render show output instead."""
        result = runner.invoke(cli, ["claim", "P1", "--agent=test-agent"])

        assert result.exit_code == 0
        assert "Warning: claim only works with task IDs." in result.output
        assert "Showing `backlog show P1` for context." in result.output
        assert "P1" in result.output

    def test_claim_accepts_bug_ids(self, runner, tmp_tasks_dir):
        """claim should work with bug IDs."""
        bug_file = create_bug_file(tmp_tasks_dir, "B001", "Critical Bug")

        result = runner.invoke(cli, ["claim", "B001", "--agent=test-agent"])

        assert result.exit_code == 0
        assert "✓ Claimed: B001 - Critical Bug" in result.output
        assert "status: in_progress" in bug_file.read_text()

    def test_claim_accepts_idea_ids(self, runner, tmp_tasks_dir):
        """claim should work with idea IDs."""
        idea_result = runner.invoke(cli, ["idea", "Add onboarding docs"])
        assert idea_result.exit_code == 0

        ideas_index = yaml.safe_load(
            (tmp_tasks_dir / ".tasks" / "ideas" / "index.yaml").read_text()
        )
        idea_entry = ideas_index["ideas"][0]
        idea_file = tmp_tasks_dir / ".tasks" / "ideas" / idea_entry["file"]

        result = runner.invoke(cli, ["claim", idea_entry["id"], "--agent=test-agent"])
        assert result.exit_code == 0
        assert f"✓ Claimed: {idea_entry['id']} - Add onboarding docs" in result.output
        assert "status: in_progress" in idea_file.read_text()

    def test_claim_warns_when_task_file_missing(self, runner, tmp_tasks_dir):
        """claim should warn when a task entry exists but its file is missing."""
        task_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")
        task_file.unlink()

        result = runner.invoke(cli, ["claim", "P1.M1.E1.T001", "--agent=test-agent"])

        assert result.exit_code != 0
        assert "Task file missing for P1.M1.E1.T001" in result.output
        assert (
            "Cannot claim P1.M1.E1.T001 because the task file is missing."
            in result.output
        )

    def test_claim_auto_commits_when_no_staged_files(self, runner, tmp_tasks_dir):
        """claim should auto-commit task updates when no staged files exist."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")
        initialize_test_git_repo(tmp_tasks_dir)
        initial_commits = git_commit_count(tmp_tasks_dir)

        result = runner.invoke(cli, ["claim", "P1.M1.E1.T001", "--agent=test-agent"])

        assert result.exit_code == 0
        assert "✓ Claimed: P1.M1.E1.T001" in result.output
        assert git_commit_count(tmp_tasks_dir) == initial_commits + 1
        assert run_git(tmp_tasks_dir, "log", "-1", "--pretty=%B") == "bl claim P1.M1.E1.T001: Test Task"
        assert run_git(tmp_tasks_dir, "status", "--short") == ""

    def test_claim_auto_commit_skips_when_staged_files_exist(self, runner, tmp_tasks_dir):
        """claim should skip auto-commit when staged files already exist."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")
        initialize_test_git_repo(tmp_tasks_dir)
        (tmp_tasks_dir / "staged-change.txt").write_text("staged\n")
        run_git(tmp_tasks_dir, "add", "staged-change.txt")

        initial_commits = git_commit_count(tmp_tasks_dir)
        result = runner.invoke(cli, ["claim", "P1.M1.E1.T001", "--agent=test-agent"])

        assert result.exit_code == 0
        assert "✓ Claimed: P1.M1.E1.T001" in result.output
        assert git_commit_count(tmp_tasks_dir) == initial_commits
        assert "A  staged-change.txt" in run_git(tmp_tasks_dir, "status", "--short")


class TestEditCommand:
    """Tests for the edit command."""

    def test_edit_requires_single_task_id(self, runner, tmp_tasks_dir):
        """edit should require exactly one TASK_ID."""
        no_id = runner.invoke(cli, ["edit"])
        assert no_id.exit_code != 0
        assert "edit requires exactly one TASK_ID" in no_id.output

        too_many = runner.invoke(
            cli,
            ["edit", "P1.M1.E1.T001", "P1.M1.E1.T002"],
        )
        assert too_many.exit_code != 0
        assert "edit requires exactly one TASK_ID" in too_many.output

    def test_edit_shows_help(self, runner, tmp_tasks_dir):
        """edit should include command help text."""
        result = runner.invoke(cli, ["edit", "--help"])

        assert result.exit_code == 0
        assert "Usage: backlog edit" in result.output or "Usage: " in result.output
        assert "TASK_ID" in result.output

    def test_edit_non_task_id_falls_back_to_show(self, runner, tmp_tasks_dir):
        """edit should fall back to show for scoped IDs."""
        result = runner.invoke(cli, ["edit", "P1"])

        assert result.exit_code == 0
        assert "Warning: edit only works with task IDs." in result.output
        assert "Showing `backlog show P1` for context." in result.output
        assert "P1" in result.output

    def test_edit_warns_when_task_file_missing(self, runner, tmp_tasks_dir):
        """edit should fail when task file is missing."""
        task_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")
        task_file.unlink()

        result = runner.invoke(cli, ["edit", "P1.M1.E1.T001"])

        assert result.exit_code != 0
        assert (
            "Cannot edit P1.M1.E1.T001 because the task file is missing."
            in result.output
        )

    def test_edit_needs_editor_configured(self, runner, tmp_tasks_dir):
        """edit should fail with a clear message when no editor is configured."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")

        result = runner.invoke(
            cli,
            ["edit", "P1.M1.E1.T001"],
            env={"EDITOR": "", "VISUAL": ""},
        )

        assert result.exit_code != 0
        assert "No editor configured. Set EDITOR or VISUAL." in result.output

    def test_edit_launches_configured_editor(self, runner, tmp_tasks_dir):
        """edit should pass the task file to the configured editor."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")
        editor = tmp_tasks_dir / "editor.sh"
        editor.write_text("#!/bin/sh\necho \"$1\" > editor-called.txt\n")
        editor.chmod(0o755)
        marker = tmp_tasks_dir / "editor-called.txt"

        result = runner.invoke(
            cli,
            ["edit", "P1.M1.E1.T001"],
            env={"VISUAL": str(editor)},
        )

        assert result.exit_code == 0
        assert marker.exists()
        assert "T001" in marker.read_text()

    def test_edit_auto_commit_creates_bl_edit_commit(self, runner, tmp_tasks_dir):
        """edit should auto-commit updated task file when no staged files exist."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")
        editor = tmp_tasks_dir / "editor.sh"
        editor.write_text("#!/bin/sh\necho \"edited\" >> \"$1\"\n")
        editor.chmod(0o755)
        initialize_test_git_repo(tmp_tasks_dir)

        initial_commits = git_commit_count(tmp_tasks_dir)
        result = runner.invoke(
            cli,
            ["edit", "P1.M1.E1.T001"],
            env={"VISUAL": str(editor)},
        )

        assert result.exit_code == 0
        assert git_commit_count(tmp_tasks_dir) == initial_commits + 1
        assert run_git(tmp_tasks_dir, "log", "-1", "--pretty=%B") == "bl edit P1.M1.E1.T001: Test Task"

    def test_edit_auto_commit_skips_when_staged_files_exist(self, runner, tmp_tasks_dir):
        """edit should skip auto-commit when staged files already exist."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")
        editor = tmp_tasks_dir / "editor.sh"
        editor.write_text("#!/bin/sh\necho \"edited\" >> \"$1\"\n")
        editor.chmod(0o755)
        initialize_test_git_repo(tmp_tasks_dir)
        (tmp_tasks_dir / "staged-change.txt").write_text("staged\n")
        run_git(tmp_tasks_dir, "add", "staged-change.txt")

        initial_commits = git_commit_count(tmp_tasks_dir)
        result = runner.invoke(
            cli,
            ["edit", "P1.M1.E1.T001"],
            env={"VISUAL": str(editor)},
        )

        assert result.exit_code == 0
        assert git_commit_count(tmp_tasks_dir) == initial_commits
        assert "A  staged-change.txt" in run_git(tmp_tasks_dir, "status", "--short")


class TestLsCommand:
    """Tests for the ls command."""

    def test_ls_lists_all_phases(self, runner, tmp_tasks_dir):
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")

        result = runner.invoke(cli, ["ls"])

        assert result.exit_code == 0
        assert "P1: Test Phase" in result.output
        assert "0/1 tasks done" in result.output

    def test_ls_root_prints_bug_idea_and_fixed_summaries(self, runner, tmp_tasks_dir):
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")
        create_bug_file(tmp_tasks_dir, "B001", "Root bug")
        result = runner.invoke(cli, ["idea", "capture planning idea"])
        assert result.exit_code == 0
        result = runner.invoke(
            cli,
            [
                "fixed",
                "--title",
                "restore service health check",
                "--at",
                "2026-02-20T12:00:00Z",
                "--body",
                "Added guard against stale session cache.",
            ],
        )
        assert result.exit_code == 0

        result = runner.invoke(cli, ["ls"])

        assert result.exit_code == 0
        assert "Bugs (0/1)" in result.output
        assert "Ideas (0/1)" in result.output
        assert "Fixes (1/1)" in result.output

    def test_ls_phase_lists_milestones(self, runner, tmp_tasks_dir):
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")

        result = runner.invoke(cli, ["ls", "P1"])

        assert result.exit_code == 0
        assert "P1.M1: Test Milestone" in result.output
        assert "0/1 tasks done" in result.output

    def test_ls_milestone_lists_epics(self, runner, tmp_tasks_dir):
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")

        result = runner.invoke(cli, ["ls", "P1.M1"])

        assert result.exit_code == 0
        assert "P1.M1.E1: Test Epic" in result.output
        assert "0/1 tasks done" in result.output

    def test_ls_epic_lists_tasks(self, runner, tmp_tasks_dir):
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task Two")

        result = runner.invoke(cli, ["ls", "P1.M1.E1"])

        assert result.exit_code == 0
        assert "P1.M1.E1.T001: Task One" in result.output
        assert "Task Two" in result.output
        assert "[pending]" in result.output

    def test_ls_task_alias_prints_frontmatter_and_show_hint(self, runner, tmp_tasks_dir):
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")

        result = runner.invoke(cli, ["ls", "P1.M1.E1.T001"])

        assert result.exit_code == 0
        assert "Task: P1.M1.E1.T001 - Task One" in result.output
        assert "id: P1.M1.E1.T001" in result.output
        assert "title: Task One" in result.output
        assert "Body length:" in result.output
        assert "Run 'backlog show P1.M1.E1.T001' for full details." in result.output

    def test_ls_accepts_multiple_scopes_and_fails_strictly(self, runner, tmp_tasks_dir):
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")

        result = runner.invoke(cli, ["ls", "P1", "P1.M1"])
        assert result.exit_code == 0
        assert "P1.M1: Test Milestone" in result.output
        assert "P1.M1.E1: Test Epic" in result.output

        result = runner.invoke(cli, ["ls", "P1", "P9"])
        assert result.exit_code != 0
        assert "Phase not found: P9" in result.output


class TestListCommand:
    """Tests for the list command."""

    def test_list_warns_when_task_file_missing(self, runner, tmp_tasks_dir):
        """list should warn when indexed task files are missing."""
        task_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")
        task_file.unlink()

        result = runner.invoke(cli, ["list"])

        assert result.exit_code == 0
        assert "task file(s) referenced in index are missing" in result.output
        assert "P1.M1.E1.T001" in result.output

    def test_list_marks_bug_on_critical_path(self, runner, tmp_tasks_dir):
        """list should mark bugs that are on critical path."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Short Task")
        create_bug_file(tmp_tasks_dir, "B001", "Critical Bug")

        result = runner.invoke(cli, ["list"])

        assert result.exit_code == 0
        assert "★" in result.output
        assert "B001: Critical Bug" in result.output

    def test_list_fails_on_explicit_dependency_cycle(self, runner, tmp_tasks_dir):
        """list should still fail on task-level dependency cycles."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Task One",
            depends_on=["P1.M1.E1.T002"],
        )
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T002",
            "Task Two",
            depends_on=["P1.M1.E1.T001"],
        )

        result = runner.invoke(cli, ["list"])

        assert result.exit_code != 0
        normalized = " ".join(result.output.split())
        assert "task dependency cycle detected" in normalized
        assert "P1.M1.E1.T001 -> P1.M1.E1.T002 -> P1.M1.E1.T001" in normalized

    def test_list_available_displays_claim_hint(self, runner, tmp_tasks_dir):
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Auto-claim-ready task",
        )

        result = runner.invoke(cli, ["list", "--available"])

        assert result.exit_code == 0
        assert "Use `bl claim TASK_ID` to claim a specific task, or `backlog grab` to auto-claim" in result.output
        assert "the best available task." in result.output

    def test_list_warns_but_succeeds_on_hierarchy_cycle(
        self, runner, tmp_tasks_dir_hierarchy_cycle
    ):
        """list should warn but continue when only hierarchy edges form a cycle."""
        result = runner.invoke(cli, ["list"])

        assert result.exit_code == 0
        assert "Warning:" in result.output
        assert "non-task dependency cycle detected" in result.output
        assert "P1.M1.E1.T001" in result.output
        assert "P2.M1.E1.T001" in result.output


class TestSyncCommand:
    """Tests for the sync command."""

    def test_sync_fails_on_explicit_dependency_cycle(self, runner, tmp_tasks_dir):
        """sync should still fail on task-level dependency cycles."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Task One",
            depends_on=["P1.M1.E1.T002"],
        )
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T002",
            "Task Two",
            depends_on=["P1.M1.E1.T001"],
        )

        result = runner.invoke(cli, ["sync"])

        assert result.exit_code != 0
        normalized = " ".join(result.output.split())
        assert "task dependency cycle detected" in normalized
        assert "P1.M1.E1.T001 -> P1.M1.E1.T002 -> P1.M1.E1.T001" in normalized

    def test_sync_warns_but_succeeds_on_hierarchy_cycle(
        self, runner, tmp_tasks_dir_hierarchy_cycle
    ):
        """sync should warn but continue when only hierarchy edges form a cycle."""
        result = runner.invoke(cli, ["sync"])

        assert result.exit_code == 0
        assert "Warning:" in result.output
        assert "non-task dependency cycle detected" in result.output
        assert "P1.M1.E1.T001" in result.output
        assert "P2.M1.E1.T001" in result.output


class TestGrabCommand:
    """Tests for the grab command."""

    def test_grab_warns_when_task_file_missing(self, runner, tmp_tasks_dir):
        """grab should warn and stop when selected task file is missing."""
        task_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")
        task_file.unlink()

        result = runner.invoke(
            cli, ["grab", "--single", "--agent=test-agent", "--no-content"]
        )

        assert result.exit_code == 0
        assert "Task file missing for P1.M1.E1.T001" in result.output
        assert (
            "Cannot claim P1.M1.E1.T001 because the task file is missing."
            in result.output
        )

    def test_grab_autoselected_bug_fanout_claims_additional_bugs(
        self, runner, tmp_tasks_dir
    ):
        create_bug_file(tmp_tasks_dir, "B001", "Primary Bug")
        create_bug_file(tmp_tasks_dir, "B002", "Second Bug")
        create_bug_file(tmp_tasks_dir, "B003", "Third Bug")

        result = runner.invoke(
            cli, ["grab", "--agent=test-agent", "--no-content", "--single"]
        )

        assert result.exit_code == 0
        assert "B001" in result.output
        assert "B002" in result.output
        assert "B003" in result.output
        assert "parallel" in result.output.lower() or "series" in result.output.lower()

    def test_grab_autoselected_bug_fanout_uses_priority_order(
        self, runner, tmp_tasks_dir
    ):
        create_bug_file(tmp_tasks_dir, "B001", "Primary Bug")
        create_bug_file(tmp_tasks_dir, "B002", "Low Bug")
        create_bug_file(tmp_tasks_dir, "B003", "High Bug")

        b2_file = tmp_tasks_dir / ".tasks" / "bugs" / "b002-test-bug.todo"
        b2_file.write_text(
            b2_file.read_text().replace("priority: critical", "priority: low")
        )

        b3_file = tmp_tasks_dir / ".tasks" / "bugs" / "b003-test-bug.todo"
        b3_file.write_text(
            b3_file.read_text().replace("priority: critical", "priority: high")
        )

        result = runner.invoke(
            cli, ["grab", "--agent=test-agent", "--no-content", "--single"]
        )

        assert result.exit_code == 0
        assert "ADDITIONAL #1: B003" in result.output
        assert "ADDITIONAL #2: B002" in result.output

    def test_grab_auto_commits_when_no_staged_files(self, runner, tmp_tasks_dir):
        """grab should auto-commit claimed task updates when no staged files exist."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task Two")
        initialize_test_git_repo(tmp_tasks_dir)
        initial_commits = git_commit_count(tmp_tasks_dir)

        result = runner.invoke(
            cli, ["grab", "--single", "--agent=test-agent", "--no-content"]
        )

        assert result.exit_code == 0
        assert "P1.M1.E1.T001" in result.output
        assert git_commit_count(tmp_tasks_dir) == initial_commits + 1
        assert run_git(tmp_tasks_dir, "log", "-1", "--pretty=%B") == "bl grab P1.M1.E1.T001: Task One"

    def test_grab_auto_commit_skips_when_staged_files_exist(self, runner, tmp_tasks_dir):
        """grab should skip auto-commit when staged files already exist."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")
        initialize_test_git_repo(tmp_tasks_dir)
        (tmp_tasks_dir / "staged-change.txt").write_text("staged\n")
        run_git(tmp_tasks_dir, "add", "staged-change.txt")

        initial_commits = git_commit_count(tmp_tasks_dir)
        result = runner.invoke(
            cli, ["grab", "--single", "--agent=test-agent", "--no-content"]
        )

        assert result.exit_code == 0
        assert "P1.M1.E1.T001" in result.output
        assert git_commit_count(tmp_tasks_dir) == initial_commits
        assert "A  staged-change.txt" in run_git(tmp_tasks_dir, "status", "--short")


class TestDoneCommand:
    """Tests for the done command."""

    def test_done_saves_duration(self, runner, tmp_tasks_dir):
        """done should save duration_minutes to the task file."""
        # Create and claim a task first
        claimed_at = datetime.now(timezone.utc) - timedelta(minutes=30)
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Test Task",
            status="in_progress",
            claimed_by="test-agent",
            claimed_at=claimed_at,
        )

        result = runner.invoke(cli, ["done", "P1.M1.E1.T001"])

        assert result.exit_code == 0
        assert "Completed" in result.output
        assert "Duration:" in result.output

        # Verify duration was saved to file
        task_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T001-test-task.todo"
        )
        with open(task_file) as f:
            content = f.read()

        assert "duration_minutes:" in content

    def test_done_requires_in_progress_without_force(self, runner, tmp_tasks_dir):
        """done should require an in-progress task unless --force is used."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task", status="pending")

        result = runner.invoke(cli, ["done", "P1.M1.E1.T001"])

        assert result.exit_code == 1
        assert "INVALID_STATUS" in result.output

        task_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T001-test-task.todo"
        )
        assert "status: pending" in task_file.read_text()

    def test_done_force_marks_non_progress_task(self, runner, tmp_tasks_dir):
        """done --force should mark non-in-progress tasks as done."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Test Task",
            status="pending",
            claimed_by="test-agent",
        )

        result = runner.invoke(cli, ["done", "P1.M1.E1.T001", "--force"])

        assert result.exit_code == 0
        assert "Completed" in result.output

        task_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T001-test-task.todo"
        )
        assert "status: done" in task_file.read_text()

    def test_done_supports_multiple_task_ids(self, runner, tmp_tasks_dir):
        """done should complete multiple task IDs in one invocation."""
        bug_file = create_bug_file(
            tmp_tasks_dir,
            "B001",
            "Critical Bug",
            status="in_progress",
        )
        t001 = create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Task One",
            status="in_progress",
            claimed_by="test-agent",
        )
        t002 = create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T002",
            "Task Two",
            status="in_progress",
            claimed_by="test-agent",
        )

        result = runner.invoke(
            cli, ["done", "P1.M1.E1.T001", "P1.M1.E1.T002", "B001"]
        )

        assert result.exit_code == 0
        assert "Completed" in result.output
        assert "P1.M1.E1.T001" in result.output
        assert "P1.M1.E1.T002" in result.output

        assert "status: done" in t001.read_text()
        assert "status: done" in t002.read_text()
        assert "status: done" in bug_file.read_text()

    def test_done_auto_commits_when_no_staged_files(self, runner, tmp_tasks_dir):
        """done should auto-commit task updates when no staged files exist."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Test Task",
            status="in_progress",
            claimed_by="test-agent",
            claimed_at=datetime.now(timezone.utc) - timedelta(minutes=12),
        )
        initialize_test_git_repo(tmp_tasks_dir)
        initial_commits = git_commit_count(tmp_tasks_dir)

        result = runner.invoke(cli, ["done", "P1.M1.E1.T001"])

        assert result.exit_code == 0
        assert "Completed" in result.output
        assert git_commit_count(tmp_tasks_dir) == initial_commits + 1
        assert run_git(tmp_tasks_dir, "log", "-1", "--pretty=%B") == "bl done P1.M1.E1.T001: Test Task"

    def test_done_auto_commit_skips_when_staged_files_exist(self, runner, tmp_tasks_dir):
        """done should skip auto-commit when staged files already exist."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Test Task",
            status="in_progress",
            claimed_by="test-agent",
            claimed_at=datetime.now(timezone.utc) - timedelta(minutes=12),
        )
        initialize_test_git_repo(tmp_tasks_dir)
        (tmp_tasks_dir / "staged-change.txt").write_text("staged\n")
        run_git(tmp_tasks_dir, "add", "staged-change.txt")

        initial_commits = git_commit_count(tmp_tasks_dir)
        result = runner.invoke(cli, ["done", "P1.M1.E1.T001"])

        assert result.exit_code == 0
        assert "Completed" in result.output
        assert git_commit_count(tmp_tasks_dir) == initial_commits
        assert "A  staged-change.txt" in run_git(tmp_tasks_dir, "status", "--short")

    def test_undone_resets_single_task(self, runner, tmp_tasks_dir):
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task", status="done")
        result = runner.invoke(cli, ["undone", "P1.M1.E1.T001"])
        assert result.exit_code == 0

        task_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T001-test-task.todo"
        )
        content = task_file.read_text()
        assert "status: pending" in content

    def test_undone_resets_phase_and_descendants(self, runner, tmp_tasks_dir):
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="done")
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="done")

        root_index = tmp_tasks_dir / ".tasks" / "index.yaml"
        root_index.write_text(root_index.read_text().replace("status: in_progress", "status: done"))

        phase_index = tmp_tasks_dir / ".tasks" / "01-test-phase" / "index.yaml"
        phase_index.write_text(phase_index.read_text().replace("status: in_progress", "status: done"))

        ms_index = (
            tmp_tasks_dir / ".tasks" / "01-test-phase" / "01-test-milestone" / "index.yaml"
        )
        ms_index.write_text(ms_index.read_text().replace("status: in_progress", "status: done"))

        result = runner.invoke(cli, ["undone", "P1"])
        assert result.exit_code == 0

        assert "status: pending" in root_index.read_text()
        assert "status: pending" in phase_index.read_text()
        assert "status: pending" in ms_index.read_text()
        assert "status: pending" in (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T001-test-task.todo"
        ).read_text()
        assert "status: pending" in (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T002-test-task.todo"
        ).read_text()

    def test_undone_auto_commits_when_no_staged_files(self, runner, tmp_tasks_dir):
        """undone should auto-commit reset updates when no staged files exist."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Test Task",
            status="done",
            completed_at=datetime.now(timezone.utc),
        )
        initialize_test_git_repo(tmp_tasks_dir)
        initial_commits = git_commit_count(tmp_tasks_dir)

        result = runner.invoke(cli, ["undone", "P1.M1.E1.T001"])

        assert result.exit_code == 0
        assert "Marked not done" in result.output
        assert git_commit_count(tmp_tasks_dir) == initial_commits + 1
        assert run_git(tmp_tasks_dir, "log", "-1", "--pretty=%B") == "bl undone P1.M1.E1.T001: Test Task"

    def test_undone_auto_commit_skips_when_staged_files_exist(self, runner, tmp_tasks_dir):
        """undone should skip auto-commit when staged files already exist."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Test Task",
            status="done",
            completed_at=datetime.now(timezone.utc),
        )
        initialize_test_git_repo(tmp_tasks_dir)
        (tmp_tasks_dir / "staged-change.txt").write_text("staged\n")
        run_git(tmp_tasks_dir, "add", "staged-change.txt")

        initial_commits = git_commit_count(tmp_tasks_dir)
        result = runner.invoke(cli, ["undone", "P1.M1.E1.T001"])

        assert result.exit_code == 0
        assert "Marked not done" in result.output
        assert git_commit_count(tmp_tasks_dir) == initial_commits
        assert "A  staged-change.txt" in run_git(tmp_tasks_dir, "status", "--short")

    def test_unclaim_auto_commits_when_no_staged_files(self, runner, tmp_tasks_dir):
        """unclaim should auto-commit task resets when no staged files exist."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Task One",
            status="in_progress",
            claimed_by="test-agent",
            claimed_at=datetime.now(timezone.utc) - timedelta(minutes=10),
        )
        initialize_test_git_repo(tmp_tasks_dir)
        initial_commits = git_commit_count(tmp_tasks_dir)

        result = runner.invoke(cli, ["unclaim", "P1.M1.E1.T001"])

        assert result.exit_code == 0
        assert "Unclaimed: P1.M1.E1.T001 - Task One" in result.output
        assert git_commit_count(tmp_tasks_dir) == initial_commits + 1
        assert run_git(tmp_tasks_dir, "log", "-1", "--pretty=%B") == "bl unclaim P1.M1.E1.T001: Task One"

    def test_unclaim_auto_commit_skips_when_staged_files_exist(self, runner, tmp_tasks_dir):
        """unclaim should skip auto-commit when staged files already exist."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Task One",
            status="in_progress",
            claimed_by="test-agent",
            claimed_at=datetime.now(timezone.utc) - timedelta(minutes=10),
        )
        initialize_test_git_repo(tmp_tasks_dir)
        (tmp_tasks_dir / "staged-change.txt").write_text("staged\n")
        run_git(tmp_tasks_dir, "add", "staged-change.txt")

        initial_commits = git_commit_count(tmp_tasks_dir)
        result = runner.invoke(cli, ["unclaim", "P1.M1.E1.T001"])

        assert result.exit_code == 0
        assert "Unclaimed: P1.M1.E1.T001 - Task One" in result.output
        assert git_commit_count(tmp_tasks_dir) == initial_commits
        assert "A  staged-change.txt" in run_git(tmp_tasks_dir, "status", "--short")


class TestNextCommand:
    """Tests for the next command."""

    def test_next_suggests_claim_command(self, runner, tmp_tasks_dir):
        """next should suggest how to claim the task."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")

        result = runner.invoke(cli, ["next"])

        # Should show the next task and suggest claiming it
        if result.exit_code == 0 and "Next task" in result.output:
            assert "grab" in result.output or "claim" in result.output

    def test_next_with_stale_locked_task(self, runner, tmp_tasks_dir):
        """
        Scenario: A task was claimed by an agent that failed.
        - Task is stale (agent not responding)
        - Task is still within lock period (15 min old)
        - All other tasks in the epic/milestone are completed

        Expected: `next` should show "no available tasks" and list the
        in-progress tasks blocking progress.
        """
        # Create a task that is in_progress (claimed 15 minutes ago)
        claimed_at = datetime.now(timezone.utc) - timedelta(minutes=15)
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Locked Test Task",
            status="in_progress",
            claimed_by="failed-agent",
            claimed_at=claimed_at,
        )

        result = runner.invoke(cli, ["next"])

        # Should indicate no available tasks
        assert (
            "No available tasks" in result.output
            or "no available" in result.output.lower()
        )

        # Should show the in-progress task ID and who claimed it
        assert "P1.M1.E1.T001" in result.output
        assert "failed-agent" in result.output

    def test_next_shows_blocking_task_details(self, runner, tmp_tasks_dir):
        """next should show details about in-progress tasks when nothing available."""
        # Create an in-progress task
        claimed_at = datetime.now(timezone.utc) - timedelta(minutes=10)
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Blocking Task",
            status="in_progress",
            claimed_by="some-agent",
            claimed_at=claimed_at,
        )

        result = runner.invoke(cli, ["next"])

        # Should show the blocking task ID
        if "No available tasks" in result.output:
            assert "P1.M1.E1.T001" in result.output
            assert "some-agent" in result.output

    def test_next_can_select_bug(self, runner, tmp_tasks_dir):
        """next should return an available bug when it is highest priority work."""
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Regular Task")
        create_bug_file(tmp_tasks_dir, "B001", "Critical Bug")

        result = runner.invoke(cli, ["next", "--json"])

        assert result.exit_code == 0
        payload = yaml.safe_load(result.output)
        assert payload["id"] == "B001"


class TestPreviewCommand:
    """Tests for the preview command."""

    def test_preview_limits_auxiliary_items_and_includes_grab_suggestions(
        self, runner, tmp_tasks_dir
    ):
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Primary Task")
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Second Task")
        create_task_file(tmp_tasks_dir, "P1.M1.E1.T003", "Third Task")
        create_bug_file(tmp_tasks_dir, "B001", "Critical Bug")
        create_bug_file(tmp_tasks_dir, "B002", "Secondary Bug")

        for i in range(7):
            idea_result = runner.invoke(cli, ["idea", f"future planning idea {i}"])
            assert idea_result.exit_code == 0

        result = runner.invoke(cli, ["preview", "--json"])
        assert result.exit_code == 0
        payload = yaml.safe_load(result.output)

        assert payload["next_available"] == "B001"
        assert len(payload["normal"]) == 1
        assert len(payload["bugs"]) == 2
        assert len(payload["ideas"]) == 5

        normal_by_id = {item["id"]: item for item in payload["normal"]}
        t1 = normal_by_id["P1.M1.E1.T001"]
        assert t1["grab_additional"] == [
            "P1.M1.E1.T002",
            "P1.M1.E1.T003",
        ]

    def test_preview_shows_top_bugs_and_ideas(self, runner, tmp_tasks_dir):
        for i in range(1, 7):
            create_bug_file(tmp_tasks_dir, f"B{i:03d}", f"Bug {i}")

        for i in range(1, 7):
            idea_result = runner.invoke(cli, ["idea", f"planning idea {i}"])
            assert idea_result.exit_code == 0

        result = runner.invoke(cli, ["preview", "--json"])
        assert result.exit_code == 0
        payload = yaml.safe_load(result.output)

        assert len(payload["bugs"]) == 5
        assert len(payload["ideas"]) == 5


class TestLogCommand:
    """Tests for the log command."""

    def test_log_command_returns_json_activity(self, runner, tmp_tasks_dir):
        """log --json should return recent activity entries sorted by timestamp."""
        now = datetime.now(timezone.utc)
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Task One",
            status="done",
            claimed_by="agent-a",
            claimed_at=now - timedelta(hours=3),
            started_at=now - timedelta(hours=2, minutes=30),
            completed_at=now - timedelta(hours=1),
        )
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T002",
            "Task Two",
            status="in_progress",
            claimed_by="agent-b",
            claimed_at=now - timedelta(hours=1, minutes=30),
            started_at=now - timedelta(minutes=45),
        )

        result = runner.invoke(cli, ["log", "--json", "--limit", "4"])

        assert result.exit_code == 0
        payload = json.loads(result.output)
        assert isinstance(payload, list)
        assert payload[0]["task_id"] == "P1.M1.E1.T002"
        assert payload[0]["event"] == "started"
        assert payload[0]["actor"] == "agent-b"
        assert payload[0]["kind"] == "started"
        assert payload[1]["task_id"] == "P1.M1.E1.T001"
        assert payload[1]["event"] == "completed"
        assert payload[1]["actor"] == "agent-a"
        assert payload[1]["kind"] == "completed"
        assert any(item["event"] == "claimed" for item in payload)

    def test_log_command_pretty_output(self, runner, tmp_tasks_dir):
        """log should render an attractive text activity stream."""
        now = datetime.now(timezone.utc)
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Task One",
            status="done",
            claimed_by="agent-a",
            claimed_at=now - timedelta(minutes=40),
            started_at=now - timedelta(hours=1),
            completed_at=now - timedelta(minutes=30),
        )

        result = runner.invoke(cli, ["log", "--limit", "2"])

        assert result.exit_code == 0
        assert "Recent Activity Log" in result.output
        assert "✓" in result.output
        assert "P1.M1.E1.T001" in result.output
        assert "agent-a" in result.output

    def test_log_command_includes_added_task_entries(self, runner, tmp_tasks_dir):
        """log should include added tasks that have no activity timestamps."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Task One",
            status="pending",
        )

        result = runner.invoke(cli, ["log", "--json"])

        assert result.exit_code == 0
        payload = json.loads(result.output)
        assert len(payload) == 1
        entry = payload[0]
        assert entry["task_id"] == "P1.M1.E1.T001"
        assert entry["title"] == "Task One"
        assert entry["event"] == "added"
        assert entry["kind"] == "created"
        assert entry["actor"] is None
        assert "timestamp" in entry

    def test_log_command_filters_only_bugs(self, runner, tmp_tasks_dir):
        """log --bugs should only return bug events."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Task One",
            status="done",
        )
        create_bug_file(tmp_tasks_dir, "B001", "Critical Bug")
        idea_result = runner.invoke(cli, ["idea", "Capture planning idea"])
        assert idea_result.exit_code == 0

        result = runner.invoke(cli, ["log", "--json", "--bugs"])

        assert result.exit_code == 0
        payload = json.loads(result.output)
        assert len(payload) == 1
        assert payload[0]["task_id"] == "B001"
        assert payload[0]["event"] == "added"

    def test_log_command_filters_only_ideas(self, runner, tmp_tasks_dir):
        """log --ideas should only return idea events."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Task One",
            status="in_progress",
        )
        create_bug_file(tmp_tasks_dir, "B001", "Critical Bug")
        idea_result = runner.invoke(cli, ["idea", "Capture next idea"])
        assert idea_result.exit_code == 0

        result = runner.invoke(cli, ["log", "--json", "--ideas"])

        assert result.exit_code == 0
        payload = json.loads(result.output)
        assert len(payload) == 1
        assert payload[0]["task_id"] == "I001"
        assert payload[0]["event"] == "added"

    def test_log_command_filters_bugs_and_ideas_together(self, runner, tmp_tasks_dir):
        """log --bugs --ideas should return only bug and idea events."""
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E1.T001",
            "Task One",
            status="pending",
        )
        create_bug_file(tmp_tasks_dir, "B001", "Critical Bug")
        create_bug_file(tmp_tasks_dir, "B002", "Follow-up Bug")
        idea_result = runner.invoke(cli, ["idea", "Capture planning idea"])
        assert idea_result.exit_code == 0

        result = runner.invoke(cli, ["log", "--json", "--bugs", "--ideas"])

        assert result.exit_code == 0
        payload = json.loads(result.output)
        task_ids = {item["task_id"] for item in payload}
        assert task_ids == {"B001", "B002", "I001"}

class TestCycleCommand:
    """Tests for the cycle command."""

    def test_cycle_auto_grabs_when_epic_not_complete(self, runner, tmp_tasks_dir):
        """cycle should auto-grab next task when completing a task that doesn't complete the epic."""
        # Create 3 tasks in the same epic
        task_ids = create_multi_task_epic(tmp_tasks_dir, 3)

        # Mark first task as in_progress
        task_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T001-test-task.todo"
        )
        with open(task_file, "r") as f:
            content = f.read()
        content = content.replace("status: pending", "status: in_progress")
        content = content.replace("tags:", "claimed_by: test-agent\ntags:")
        with open(task_file, "w") as f:
            f.write(content)

        # Update context to set current task
        context_dir = tmp_tasks_dir / ".tasks"
        context_file = context_dir / ".context.yaml"
        context = {
            "current_task": task_ids[0],
            "agent": "test-agent",
            "started_at": datetime.now(timezone.utc).isoformat(),
        }
        with open(context_file, "w") as f:
            yaml.dump(context, f)

        # Call cycle to complete first task
        result = runner.invoke(cli, ["cycle", "--agent=test-agent"])

        # Assertions
        assert result.exit_code == 0
        assert "Completed" in result.output
        assert task_ids[0] in result.output
        assert "Grabbed" in result.output
        assert task_ids[1] in result.output  # Second task should be auto-claimed

        # Verify second task is now in_progress
        task2_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T002-test-task.todo"
        )
        with open(task2_file, "r") as f:
            task2_content = f.read()
        assert "status: in_progress" in task2_content

    def test_cycle_autoselected_bug_fanout_claims_additional_bugs(
        self, runner, tmp_tasks_dir
    ):
        create_bug_file(
            tmp_tasks_dir,
            "B001",
            "Primary Bug",
            status="in_progress",
        )
        create_bug_file(tmp_tasks_dir, "B002", "Second Bug")
        create_bug_file(tmp_tasks_dir, "B003", "Third Bug")

        b1_file = tmp_tasks_dir / ".tasks" / "bugs" / "b001-test-bug.todo"
        b1_content = b1_file.read_text()
        b1_file.write_text(b1_content.replace("tags:", "claimed_by: test-agent\ntags:"))

        context_file = tmp_tasks_dir / ".tasks" / ".context.yaml"
        context = {
            "current_task": "B001",
            "agent": "test-agent",
            "started_at": datetime.now(timezone.utc).isoformat(),
        }
        with open(context_file, "w") as f:
            yaml.dump(context, f)

        result = runner.invoke(cli, ["cycle", "--agent=test-agent", "--no-content"])

        assert result.exit_code == 0
        assert "Completed" in result.output
        assert "B001" in result.output
        assert "B002" in result.output
        assert "B003" in result.output

    def test_cycle_stops_when_epic_complete(self, runner, tmp_tasks_dir):
        """cycle should stop and not auto-grab when completing the last task in an epic."""
        # Create 2 tasks in the same epic
        task_ids = create_multi_task_epic(tmp_tasks_dir, 2)

        # Mark first task as done
        task1_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T001-test-task.todo"
        )
        with open(task1_file, "r") as f:
            content = f.read()
        content = content.replace("status: pending", "status: done")
        with open(task1_file, "w") as f:
            f.write(content)

        # Mark second task as in_progress
        task2_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T002-test-task.todo"
        )
        with open(task2_file, "r") as f:
            content = f.read()
        content = content.replace("status: pending", "status: in_progress")
        content = content.replace("tags:", "claimed_by: test-agent\ntags:")
        with open(task2_file, "w") as f:
            f.write(content)

        # Update context
        context_dir = tmp_tasks_dir / ".tasks"
        context_file = context_dir / ".context.yaml"
        context = {
            "current_task": task_ids[1],
            "agent": "test-agent",
            "started_at": datetime.now(timezone.utc).isoformat(),
        }
        with open(context_file, "w") as f:
            yaml.dump(context, f)

        # Call cycle to complete the second (last) task
        result = runner.invoke(cli, ["cycle", "--agent=test-agent"])

        # Assertions
        assert result.exit_code == 0
        assert "Completed" in result.output
        assert task_ids[1] in result.output
        assert "EPIC COMPLETE" in result.output
        assert "Review Required" in result.output
        assert "backlog grab" in result.output
        assert "Grabbed" not in result.output  # Should NOT auto-grab

        # Verify context is cleared
        assert not context_file.exists() or not Path(context_file).read_text().strip()

    def test_cycle_stops_when_milestone_complete(self, runner, tmp_tasks_dir):
        """cycle should stop when completing the last task that completes a milestone."""
        # Create one epic with one task (completes both epic and milestone)
        task_ids = create_multi_task_epic(tmp_tasks_dir, 1)

        # Mark the task as in_progress
        task_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T001-test-task.todo"
        )
        with open(task_file, "r") as f:
            content = f.read()
        content = content.replace("status: pending", "status: in_progress")
        content = content.replace("tags:", "claimed_by: test-agent\ntags:")
        with open(task_file, "w") as f:
            f.write(content)

        # Update context
        context_dir = tmp_tasks_dir / ".tasks"
        context_file = context_dir / ".context.yaml"
        context = {
            "current_task": task_ids[0],
            "agent": "test-agent",
            "started_at": datetime.now(timezone.utc).isoformat(),
        }
        with open(context_file, "w") as f:
            yaml.dump(context, f)

        # Call cycle to complete it (completes both epic and milestone)
        result = runner.invoke(cli, ["cycle", "--agent=test-agent"])

        # Assertions
        assert result.exit_code == 0
        assert "Completed" in result.output
        assert task_ids[0] in result.output
        assert "EPIC COMPLETE" in result.output
        assert "MILESTONE COMPLETE" not in result.output
        assert "Review Required" in result.output
        assert "Grabbed" not in result.output  # Should NOT auto-grab

        # Verify context is cleared
        assert not context_file.exists() or not Path(context_file).read_text().strip()

    def test_done_command_handles_completion_status(self, runner, tmp_tasks_dir):
        """done command should still work after updating print_completion_notices to return a value."""
        # Create 2 tasks in an epic
        task_ids = create_multi_task_epic(tmp_tasks_dir, 2)

        # Mark first task as done
        task1_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T001-test-task.todo"
        )
        with open(task1_file, "r") as f:
            content = f.read()
        content = content.replace("status: pending", "status: done")
        with open(task1_file, "w") as f:
            f.write(content)

        # Mark second task as in_progress
        task2_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T002-test-task.todo"
        )
        with open(task2_file, "r") as f:
            content = f.read()
        content = content.replace("status: pending", "status: in_progress")
        content = content.replace("tags:", "claimed_by: test-agent\ntags:")
        with open(task2_file, "w") as f:
            f.write(content)

        # Call done to complete the second (last) task
        result = runner.invoke(cli, ["done", task_ids[1]])

        # Assertions
        assert result.exit_code == 0
        assert "Completed" in result.output
        # Check for epic completion (if applicable)
        if "EPIC COMPLETE" in result.output:
            assert "EPIC COMPLETE" in result.output
        # Should not crash or error

    def test_done_marks_epic_milestone_and_phase_done(self, runner, tmp_tasks_dir):
        """done should mark complete ancestors and lock phase when all tasks are done."""
        # Create 2 tasks in an epic
        task_ids = create_multi_task_epic(tmp_tasks_dir, 2)

        # Mark first task done already
        task1_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T001-test-task.todo"
        )
        with open(task1_file, "r") as f:
            content = f.read()
        content = content.replace("status: pending", "status: done")
        with open(task1_file, "w") as f:
            f.write(content)

        # Mark second task in_progress and claim
        task2_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "01-test-epic"
            / "T002-test-task.todo"
        )
        with open(task2_file, "r") as f:
            content = f.read()
        content = content.replace("status: pending", "status: in_progress")
        content = content.replace("tags:", "claimed_by: test-agent\ntags:")
        with open(task2_file, "w") as f:
            f.write(content)

        result = runner.invoke(cli, ["done", task_ids[1]])
        assert result.exit_code == 0
        assert "Completed" in result.output
        assert "EPIC COMPLETE" in result.output
        assert "MILESTONE COMPLETE" not in result.output
        assert "PHASE COMPLETE" in result.output

        root_index = yaml.safe_load((tmp_tasks_dir / ".tasks" / "index.yaml").read_text())
        phase_entry = next(
            entry for entry in root_index["phases"] if entry["id"] == "P1"
        )
        assert phase_entry["status"] == "done"
        assert phase_entry["locked"] is True

        phase_index = yaml.safe_load((tmp_tasks_dir / ".tasks" / "01-test-phase" / "index.yaml").read_text())
        assert phase_index["status"] == "done"
        assert phase_index["locked"] is True

        milestone_index = yaml.safe_load(
            (tmp_tasks_dir / ".tasks" / "01-test-phase" / "01-test-milestone" / "index.yaml").read_text()
        )
        assert milestone_index["status"] == "done"
        assert any(
            entry["id"] == "P1.M1.E1" and entry["status"] == "done"
            for entry in milestone_index["epics"]
        )

        epic_index = yaml.safe_load(
            (
                tmp_tasks_dir
                / ".tasks"
                / "01-test-phase"
                / "01-test-milestone"
                / "01-test-epic"
                / "index.yaml"
            ).read_text()
        )
        assert epic_index["status"] == "done"

        blocked_add = runner.invoke(cli, ["add", "P1.M1.E1", "--title", "Blocked"])
        assert blocked_add.exit_code != 0
        assert "has been closed and cannot accept new tasks" in blocked_add.output
        assert "create a new epic" in blocked_add.output.lower()

        unlock = runner.invoke(cli, ["unlock", "P1"])
        assert unlock.exit_code == 0
        assert "Unlocked: P1" in unlock.output

        allowed_add = runner.invoke(cli, ["add", "P1.M1.E1", "--title", "Allowed"])
        assert allowed_add.exit_code == 0
        assert "Created task:" in allowed_add.output


    def test_done_shows_milestone_complete_when_multiple_epics(self, runner, tmp_tasks_dir):
        """done should show milestone review prompt when milestone has more than one epic."""
        tasks_root = tmp_tasks_dir / ".tasks"
        milestone_index_path = tasks_root / "01-test-phase" / "01-test-milestone" / "index.yaml"
        milestone_index = yaml.safe_load(milestone_index_path.read_text())
        milestone_index.setdefault("epics", []).append(
            {
                "id": "P1.M1.E2",
                "name": "Second Epic",
                "path": "02-test-epic",
                "status": "in_progress",
            }
        )
        milestone_index_path.write_text(yaml.dump(milestone_index))

        (tasks_root / "01-test-phase" / "01-test-milestone" / "02-test-epic").mkdir(
            parents=True, exist_ok=True
        )
        create_task_file(
            tmp_tasks_dir,
            "P1.M1.E2.T001",
            "Second Epic Task",
            status="pending",
        )

        task2_file = (
            tmp_tasks_dir
            / ".tasks"
            / "01-test-phase"
            / "01-test-milestone"
            / "02-test-epic"
            / "T001-test-task.todo"
        )
        with open(task2_file, "r") as f:
            content = f.read()
        content = content.replace("status: pending", "status: in_progress")
        content = content.replace("tags:", "claimed_by: test-agent\ntags:")
        with open(task2_file, "w") as f:
            f.write(content)

        result = runner.invoke(cli, ["done", "P1.M1.E2.T001"])
        assert result.exit_code == 0
        assert "Completed" in result.output
        assert "EPIC COMPLETE" in result.output
        assert "MILESTONE COMPLETE" in result.output


def test_list_enhanced_shows_milestones(runner, tmp_tasks_dir):
    """Test enhanced list command shows milestones with task counts."""
    # Create two tasks
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="pending")

    result = runner.invoke(cli, ["list"])
    assert result.exit_code == 0
    assert "Test Phase (P1) (0/2 tasks done)" in result.output
    assert "Test Milestone (" in result.output
    assert "M1) (0/2 tasks done)" in result.output


def test_list_all_shows_all_milestones(runner, tmp_tasks_dir):
    """Test list --all shows all milestones without truncation."""
    # Add 7 milestones to test truncation
    phase_dir = tmp_tasks_dir / ".tasks" / "01-test-phase"
    milestones = []
    for i in range(1, 8):
        milestone_dir = phase_dir / f"0{i}-milestone-{i}"
        milestone_dir.mkdir(exist_ok=True)
        milestone_index = {"epics": []}
        with open(milestone_dir / "index.yaml", "w") as f:
            yaml.dump(milestone_index, f)
        milestones.append(
            {
                "id": f"P1.M{i}",
                "name": f"Milestone {i}",
                "path": f"0{i}-milestone-{i}",
                "status": "pending",
            }
        )

    # Update phase index
    phase_index = {"milestones": milestones}
    with open(phase_dir / "index.yaml", "w") as f:
        yaml.dump(phase_index, f)

    # Without --all, should show only 5 and truncate
    result = runner.invoke(cli, ["list"])
    assert result.exit_code == 0
    assert "... and 2 more milestone" in result.output

    # With --all, should show all 7
    result = runner.invoke(cli, ["list", "--all"])
    assert result.exit_code == 0
    assert "Milestone 7" in result.output
    assert "... and 2 more milestone" not in result.output


def test_list_unfinished_filters_completed(runner, tmp_tasks_dir):
    """Test list --unfinished filters out completed items."""
    task1 = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="done")
    task2 = create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="pending")

    result = runner.invoke(cli, ["list", "--unfinished"])
    assert result.exit_code == 0
    # Stats should show actual completion (1/2)
    assert "Test Phase (P1) (1/2 tasks done)" in result.output
    assert "Test Milestone (" in result.output
    assert "M1) (1/2 tasks done)" in result.output


def test_list_json_includes_milestone_metadata(runner, tmp_tasks_dir):
    """Test list --json includes milestone metadata."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="done")

    result = runner.invoke(cli, ["list", "--json"])
    assert result.exit_code == 0

    import json

    data = json.loads(result.output)
    assert "phases" in data
    assert len(data["phases"]) > 0
    assert "milestones" in data["phases"][0]
    assert len(data["phases"][0]["milestones"]) > 0
    assert "stats" in data["phases"][0]["milestones"][0]
    assert data["phases"][0]["milestones"][0]["stats"]["total"] == 2


def test_list_hides_completed_bugs_by_default(runner, tmp_tasks_dir):
    """list should hide completed bugs unless explicitly requested."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_bug_file(tmp_tasks_dir, "B001", "Done Bug", status="done")
    create_bug_file(tmp_tasks_dir, "B002", "Open Bug", status="pending")

    result = runner.invoke(cli, ["list"])
    assert result.exit_code == 0
    assert "B002: Open Bug" in result.output
    assert "B001: Done Bug" not in result.output


def test_list_can_show_completed_bugs_with_flag(runner, tmp_tasks_dir):
    """list --show-completed-aux should include completed bugs."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_bug_file(tmp_tasks_dir, "B001", "Done Bug", status="done")

    result = runner.invoke(cli, ["list", "--show-completed-aux"])
    assert result.exit_code == 0
    assert "B001: Done Bug" in result.output


def test_list_all_implies_show_completed_aux_for_bugs(runner, tmp_tasks_dir):
    """list --all --bugs should include completed bugs."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_bug_file(tmp_tasks_dir, "B001", "Done Bug", status="done")

    result = runner.invoke(cli, ["list", "--all", "--bugs"])
    assert result.exit_code == 0
    assert "B001: Done Bug" in result.output


def test_list_all_implies_show_completed_aux_for_ideas(runner, tmp_tasks_dir):
    """list --all --ideas should include completed ideas."""
    create_task = runner.invoke(cli, ["idea", "capture a planning intake"])
    assert create_task.exit_code == 0

    ideas_index_path = tmp_tasks_dir / ".tasks" / "ideas" / "index.yaml"
    ideas_index = yaml.safe_load(ideas_index_path.read_text())
    idea_file = ideas_index["ideas"][0]["file"]
    idea_path = tmp_tasks_dir / ".tasks" / "ideas" / idea_file
    idea_text = idea_path.read_text().replace("status: pending", "status: done", 1)
    idea_path.write_text(idea_text)

    result = runner.invoke(cli, ["list", "--all", "--ideas"])
    assert result.exit_code == 0
    assert "I001: capture a planning intake" in result.output


def test_list_json_hides_completed_bugs_by_default(runner, tmp_tasks_dir):
    """list --json should exclude completed bugs by default."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_bug_file(tmp_tasks_dir, "B001", "Done Bug", status="done")
    create_bug_file(tmp_tasks_dir, "B002", "Open Bug", status="pending")

    result = runner.invoke(cli, ["list", "--json"])
    assert result.exit_code == 0

    import json

    data = json.loads(result.output)
    bug_ids = {b["id"] for b in data.get("bugs", [])}
    assert "B002" in bug_ids
    assert "B001" not in bug_ids


def test_list_positional_scope_shows_deeper_levels_and_hides_aux(runner, tmp_tasks_dir):
    """list with positional scope should show deeper levels and hide bugs/ideas."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="pending")
    create_bug_file(tmp_tasks_dir, "B001", "Urgent Bug", status="pending")

    result = runner.invoke(cli, ["list", "P1.M1"])
    assert result.exit_code == 0
    assert "Test Epic" in result.output
    assert "P1.M1.E1.T001" in result.output
    assert "B001" not in result.output
    assert "Ideas" not in result.output


def test_list_positional_scope_json_excludes_aux_sections(runner, tmp_tasks_dir):
    """list --json with scope should not include bugs/ideas payload."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_bug_file(tmp_tasks_dir, "B001", "Urgent Bug", status="pending")

    result = runner.invoke(cli, ["list", "P1", "--json"])
    assert result.exit_code == 0

    payload = json.loads(result.output)
    assert payload["phases"]
    assert payload["phases"][0]["id"] == "P1"
    assert payload["bugs"] == []
    assert payload["ideas"] == []
    assert payload["phases"][0]["milestones"][0]["id"].endswith(".M1")


def test_list_missing_scope_query_reports_no_match(runner, tmp_tasks_dir):
    """list with missing scope should report no matching list nodes."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")

    result = runner.invoke(cli, ["list", "P9"])
    assert result.exit_code != 0
    assert "No list nodes found for path query: P9" in result.output


def test_list_accepts_multiple_positional_scopes(runner, tmp_tasks_dir):
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="pending")

    result = runner.invoke(cli, ["list", "P1", "P1.M1"])
    assert result.exit_code == 0
    assert "P1.M1.E1.T001" in result.output
    assert "P1.M1.E1.T002" in result.output


def test_grab_repeated_scope_flags_and_strict_validation(runner, tmp_tasks_dir):
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")

    result = runner.invoke(
        cli,
        ["grab", "--single", "--scope", "P1", "--scope", "P1.M1", "--agent=test-agent", "--no-content"],
    )
    assert result.exit_code == 0
    assert "P1.M1.E1.T001" in result.output

    result = runner.invoke(
        cli,
        ["grab", "--single", "--scope", "P1", "--scope", "P9", "--agent=test-agent", "--no-content"],
    )
    assert result.exit_code != 0
    assert "No list nodes found for path query: P9" in result.output


def test_tree_command_shows_full_hierarchy(runner, tmp_tasks_dir):
    """Test tree command shows full 4-level hierarchy."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="in_progress")

    result = runner.invoke(cli, ["tree"])
    assert result.exit_code == 0
    assert "Test Phase" in result.output
    assert "Test Milestone (0/2)" in result.output
    assert "Test Epic (0/2)" in result.output
    assert "P1.M1.E1.T001" in result.output
    assert "P1.M1.E1.T002" in result.output
    # Check for tree characters
    assert "├──" in result.output or "└──" in result.output


def test_tree_unfinished_filters_completed(runner, tmp_tasks_dir):
    """Test tree --unfinished filters completed work."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="done")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="pending")

    result = runner.invoke(cli, ["tree", "--unfinished"])
    assert result.exit_code == 0
    assert "P1.M1.E1.T001" not in result.output
    assert "P1.M1.E1.T002" in result.output


def test_tree_hides_completed_bugs_by_default(runner, tmp_tasks_dir):
    """tree should hide completed bugs unless explicitly requested."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_bug_file(tmp_tasks_dir, "B001", "Done Bug", status="done")
    create_bug_file(tmp_tasks_dir, "B002", "Open Bug", status="pending")

    result = runner.invoke(cli, ["tree"])
    assert result.exit_code == 0
    assert "B002: Open Bug" in result.output
    assert "B001: Done Bug" not in result.output


def test_tree_can_show_completed_bugs_with_flag(runner, tmp_tasks_dir):
    """tree --show-completed-aux should include completed bugs."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_bug_file(tmp_tasks_dir, "B001", "Done Bug", status="done")

    result = runner.invoke(cli, ["tree", "--show-completed-aux"])
    assert result.exit_code == 0
    assert "B001: Done Bug" in result.output


def test_tree_details_shows_metadata(runner, tmp_tasks_dir):
    """Test tree --details shows metadata."""
    task_file = create_task_file(
        tmp_tasks_dir,
        "P1.M1.E1.T001",
        "Task 1",
        status="in_progress",
        claimed_by="agent-x",
    )

    result = runner.invoke(cli, ["tree", "--details"])
    assert result.exit_code == 0
    assert "@agent-x" in result.output
    assert "h)" in result.output  # estimate hours (format may be 1h or 2.0h)
    # Status is shown via icon [→] for in_progress


def test_tree_depth_limits_expansion(runner, tmp_tasks_dir):
    """Test tree --depth limits expansion correctly."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")

    # Depth 1: Only phases
    result = runner.invoke(cli, ["tree", "--depth", "1"])
    assert result.exit_code == 0
    assert "Test Phase" in result.output
    assert "Test Milestone" not in result.output

    # Depth 2: Phases and milestones
    result = runner.invoke(cli, ["tree", "--depth", "2"])
    assert result.exit_code == 0
    assert "Test Phase" in result.output
    assert "Test Milestone" in result.output
    assert "Test Epic" not in result.output

    # Depth 3: Phases, milestones, and epics
    result = runner.invoke(cli, ["tree", "--depth", "3"])
    assert result.exit_code == 0
    assert "Test Phase" in result.output
    assert "Test Milestone" in result.output
    assert "Test Epic" in result.output
    assert "P1.M1.E1.T001" not in result.output


def test_tree_json_outputs_complete_hierarchy(runner, tmp_tasks_dir):
    """Test tree --json outputs complete hierarchy."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="done")

    result = runner.invoke(cli, ["tree", "--json"])
    assert result.exit_code == 0

    import json

    data = json.loads(result.output)
    assert data["max_depth"] == 4
    assert data["show_details"] is False
    assert data["unfinished_only"] is False
    assert len(data["phases"]) > 0
    assert len(data["phases"][0]["milestones"]) > 0
    assert len(data["phases"][0]["milestones"][0]["epics"]) > 0
    assert len(data["phases"][0]["milestones"][0]["epics"][0]["tasks"]) == 2


def test_tree_path_query_filters_to_matching_nodes(runner, tmp_tasks_dir):
    """Tree path query should keep ancestors and matching descendants."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="pending")

    result = runner.invoke(cli, ["tree", "P1.M1.E1.T002"])
    assert result.exit_code == 0
    assert "Test Phase" in result.output
    assert "Test Milestone" in result.output
    assert "Test Epic" in result.output
    assert "P1.M1.E1.T002" in result.output
    assert "P1.M1.E1.T001" not in result.output


def test_tree_path_query_scoped_text_hides_aux_items(runner, tmp_tasks_dir):
    """Scoped tree output should hide bugs and ideas from aux sections."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="pending")
    create_bug_file(tmp_tasks_dir, "B001", "Root bug", status="pending")
    idea_result = runner.invoke(cli, ["idea", "Root idea"])
    assert idea_result.exit_code == 0

    result = runner.invoke(cli, ["tree", "P1.M1.E1"])
    assert result.exit_code == 0
    assert "Root bug" not in result.output
    assert "Root idea" not in result.output
    assert "Bugs" not in result.output
    assert "Ideas" not in result.output
    assert "P1.M1.E1.T002" in result.output


def test_tree_path_query_with_wildcard_includes_descendants(runner, tmp_tasks_dir):
    """Tree path query should include wildcard descendants."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="pending")

    result = runner.invoke(cli, ["tree", "P1.*"])
    assert result.exit_code == 0
    assert "P1.M1.E1.T001" in result.output
    assert "P1.M1.E1.T002" in result.output


def test_tree_multiple_path_queries_union_scopes(runner, tmp_tasks_dir):
    """Tree path queries should combine matching nodes from multiple queries."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="pending")

    result = runner.invoke(cli, ["tree", "P1.M1.E1.T001", "P1.M1.E1.T002"])
    assert result.exit_code == 0
    assert "P1.M1.E1.T001" in result.output
    assert "P1.M1.E1.T002" in result.output
    assert "No tree nodes found for path query" not in result.output


def test_tree_path_query_no_match_shows_message(runner, tmp_tasks_dir):
    """Tree path query should report no matching nodes when query doesn't match."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")

    result = runner.invoke(cli, ["tree", "P9"])
    assert result.exit_code == 0
    assert "No tree nodes found for path query: P9" in result.output


def test_tree_path_query_no_match_shows_message_with_aux_items(runner, tmp_tasks_dir):
    """No-match path query message should still print when aux items exist."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")
    create_bug_file(tmp_tasks_dir, "B001", "Root bug", status="pending")
    idea_result = runner.invoke(cli, ["idea", "Root idea"])
    assert idea_result.exit_code == 0

    result = runner.invoke(cli, ["tree", "P9"])
    assert result.exit_code == 0
    assert "No tree nodes found for path query: P9" in result.output


def test_tree_path_query_no_match_json_returns_empty_phases(runner, tmp_tasks_dir):
    """JSON output for a missing path query should return an empty phase list."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="pending")

    result = runner.invoke(cli, ["tree", "P9", "--json"])
    assert result.exit_code == 0

    import json

    payload = json.loads(result.output)
    assert payload["phases"] == []


def test_list_and_tree_consistent_task_counts(runner, tmp_tasks_dir):
    """Test list and tree show consistent task counts."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task 1", status="done")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task 2", status="pending")

    list_result = runner.invoke(cli, ["list"])
    tree_result = runner.invoke(cli, ["tree"])

    assert list_result.exit_code == 0
    assert tree_result.exit_code == 0

    # Both should show 1/2 completion
    assert "(1/2" in list_result.output
    assert "(1/2)" in tree_result.output


def test_idea_command_creates_planning_intake_todo(runner, tmp_tasks_dir):
    """idea should create an intake .todo and ideas index entry."""
    result = runner.invoke(
        cli, ["idea", "add integration tests using gpt-oss-120 via groq"]
    )

    assert result.exit_code == 0
    assert "Created idea:" in result.output
    assert "Next:" in result.output
    assert "backlog add-phase" in result.output
    assert "File: .tasks/ideas/" in result.output

    ideas_index_path = tmp_tasks_dir / ".tasks" / "ideas" / "index.yaml"
    assert ideas_index_path.exists()

    ideas_index = yaml.safe_load(ideas_index_path.read_text())
    assert "ideas" in ideas_index
    assert len(ideas_index["ideas"]) == 1
    assert ideas_index["ideas"][0]["id"] == "I001"

    idea_file = ideas_index["ideas"][0]["file"]
    idea_path = tmp_tasks_dir / ".tasks" / "ideas" / idea_file
    assert idea_path.exists()

    content = idea_path.read_text()
    assert "estimate_hours: 10.0" in content
    assert "Run `/plan-task" in content
    assert "tasks add" in content
    assert "tasks bug" in content


def test_idea_command_accepts_body_and_metadata_flags(runner, tmp_tasks_dir):
    result = runner.invoke(
        cli,
        [
            "idea",
            "--title",
            "capture onboarding friction",
            "--estimate",
            "16",
            "--priority",
            "high",
            "--depends-on",
            "I001",
            "--tags",
            "research,ux",
            "--body",
            "Custom idea body content",
            "--simple",
        ],
    )

    assert result.exit_code == 0
    assert "Created idea:" in result.output

    ideas_index_path = tmp_tasks_dir / ".tasks" / "ideas" / "index.yaml"
    ideas_index = yaml.safe_load(ideas_index_path.read_text())
    assert ideas_index["ideas"][0]["id"] == "I001"
    idea_file = tmp_tasks_dir / ".tasks" / "ideas" / ideas_index["ideas"][0]["file"]
    content = idea_file.read_text()

    assert "estimate_hours: 16.0" in content
    assert "priority: high" in content
    assert "depends_on:" in content
    assert "I001" in content
    assert "tags:" in content
    assert "research" in content
    assert "ux" in content
    assert "Custom idea body content" in content
    assert "Created Work Items" not in content


def test_idea_command_rejects_invalid_dependency_id(runner, tmp_tasks_dir):
    result = runner.invoke(
        cli,
        [
            "idea",
            "--title",
            "bad depends on",
            "--depends-on",
            "not a task id",
        ],
    )
    assert result.exit_code != 0
    assert "invalid dependency id" in result.output


def test_add_command_rejects_invalid_dependency_id(runner, tmp_tasks_dir):
    result = runner.invoke(
        cli,
        [
            "add",
            "P1.M1.E1",
            "--title",
            "bad depends on",
            "--depends-on",
            "not a task id",
        ],
    )
    assert result.exit_code != 0
    assert "invalid dependency id" in result.output


def test_add_epic_milestone_and_phase_reject_invalid_dependency_id(runner, tmp_tasks_dir):
    result = runner.invoke(
        cli,
        [
            "add-epic",
            "P1.M1",
            "--title",
            "bad depends on",
            "--depends-on",
            "not a task id",
        ],
    )
    assert result.exit_code != 0
    assert "invalid dependency id" in result.output

    result = runner.invoke(
        cli,
        [
            "add-milestone",
            "P1",
            "--title",
            "bad depends on",
            "--depends-on",
            "not a task id",
        ],
    )
    assert result.exit_code != 0
    assert "invalid dependency id" in result.output

    result = runner.invoke(
        cli,
        [
            "add-phase",
            "--title",
            "bad depends on",
            "--depends-on",
            "not a task id",
        ],
    )
    assert result.exit_code != 0
    assert "invalid dependency id" in result.output


def test_idea_command_rejects_single_word_title(runner, tmp_tasks_dir):
    result = runner.invoke(cli, ["idea", "idea"])
    assert result.exit_code != 0
    assert "at least two words" in result.output


def test_idea_command_increments_idea_ids(runner, tmp_tasks_dir):
    """idea IDs should increment sequentially in .tasks/ideas/."""
    first = runner.invoke(cli, ["idea", "first idea"])
    second = runner.invoke(cli, ["idea", "second idea"])

    assert first.exit_code == 0
    assert second.exit_code == 0

    ideas_index_path = tmp_tasks_dir / ".tasks" / "ideas" / "index.yaml"
    ideas_index = yaml.safe_load(ideas_index_path.read_text())
    ids = [entry["id"] for entry in ideas_index["ideas"]]
    assert ids == ["I001", "I002"]


def test_add_command_auto_commits_when_no_staged_files(runner, tmp_tasks_dir):
    """add should auto-commit the created files when no staged files exist."""
    initialize_test_git_repo(tmp_tasks_dir)
    initial_commits = git_commit_count(tmp_tasks_dir)

    result = runner.invoke(
        cli, ["add", "P1.M1.E1", "--title", "Auto commit task"]
    )
    assert result.exit_code == 0
    assert "Created task: P1.M1.E1.T001" in result.output
    assert "File: .tasks/01-test-phase/01-test-milestone/01-test-epic/T001-auto-commit-task.todo" in result.output

    assert git_commit_count(tmp_tasks_dir) == initial_commits + 1
    assert run_git(tmp_tasks_dir, "log", "-1", "--pretty=%B") == "bl add P1.M1.E1.T001: Auto commit task"
    assert run_git(tmp_tasks_dir, "status", "--short") == ""


def test_add_command_skips_auto_commit_if_staged_files_exist(runner, tmp_tasks_dir):
    """add should not auto-commit when there are staged files before the command."""
    initialize_test_git_repo(tmp_tasks_dir)
    (tmp_tasks_dir / "staged-change.txt").write_text("staged\n")
    run_git(tmp_tasks_dir, "add", "staged-change.txt")

    initial_commits = git_commit_count(tmp_tasks_dir)
    result = runner.invoke(cli, ["add", "P1.M1.E1", "--title", "Should remain staged"])
    assert result.exit_code == 0
    assert "Created task: P1.M1.E1.T001" in result.output

    assert git_commit_count(tmp_tasks_dir) == initial_commits
    assert "A  staged-change.txt" in run_git(tmp_tasks_dir, "status", "--short")


def test_add_command_amends_previous_unpushed_bl_add_commit(runner, tmp_tasks_dir):
    """Subsequent add should amend the previous local bl add commit when possible."""
    initialize_test_git_repo(tmp_tasks_dir)

    first = runner.invoke(cli, ["add", "P1.M1.E1", "--title", "First auto commit task"])
    assert first.exit_code == 0
    assert "Created task: P1.M1.E1.T001" in first.output

    commits_after_first = git_commit_count(tmp_tasks_dir)

    second = runner.invoke(cli, ["add", "P1.M1.E1", "--title", "Second auto commit task"])
    assert second.exit_code == 0
    assert "Created task: P1.M1.E1.T002" in second.output

    assert git_commit_count(tmp_tasks_dir) == commits_after_first
    assert run_git(tmp_tasks_dir, "log", "-1", "--pretty=%B") == "bl add P1.M1.E1.T001: First auto commit task"


def test_bug_command_auto_commits_when_no_staged_files(runner, tmp_tasks_dir):
    """bug should auto-commit created bug files when no staged files exist."""
    initialize_test_git_repo(tmp_tasks_dir)
    initial_commits = git_commit_count(tmp_tasks_dir)

    result = runner.invoke(cli, ["bug", "--title", "Bug auto commit"])
    assert result.exit_code == 0
    assert "Created bug: B001" in result.output

    assert git_commit_count(tmp_tasks_dir) == initial_commits + 1
    assert run_git(tmp_tasks_dir, "log", "-1", "--pretty=%B") == "bl bug B001: Bug auto commit"


def test_bug_command_rejects_invalid_dependency_id(runner, tmp_tasks_dir):
    result = runner.invoke(
        cli,
        [
            "bug",
            "--title",
            "bad depends on",
            "--depends-on",
            "not a task id",
        ],
    )
    assert result.exit_code != 0
    assert "invalid dependency id" in result.output


def test_idea_command_auto_commits_when_no_staged_files(runner, tmp_tasks_dir):
    """idea should auto-commit created idea files when no staged files exist."""
    initialize_test_git_repo(tmp_tasks_dir)
    initial_commits = git_commit_count(tmp_tasks_dir)

    result = runner.invoke(cli, ["idea", "capture planning idea"])
    assert result.exit_code == 0
    assert "Created idea: I001" in result.output

    assert git_commit_count(tmp_tasks_dir) == initial_commits + 1
    assert run_git(tmp_tasks_dir, "log", "-1", "--pretty=%B") == "bl idea I001: capture planning idea"


def test_list_and_next_include_ideas(runner, tmp_tasks_dir):
    """list/next should include idea intake tasks."""
    create = runner.invoke(cli, ["idea", "capture a planning intake"])
    assert create.exit_code == 0

    list_result = runner.invoke(cli, ["list"])
    assert list_result.exit_code == 0
    assert "Ideas" in list_result.output
    assert "★" in list_result.output
    assert "I001: capture a planning intake" in list_result.output

    next_result = runner.invoke(cli, ["next", "--json"])
    assert next_result.exit_code == 0
    payload = yaml.safe_load(next_result.output)
    assert payload["id"] == "I001"


def test_grab_can_claim_idea(runner, tmp_tasks_dir):
    """grab should be able to claim idea intake tasks."""
    create = runner.invoke(cli, ["idea", "iterate on architecture options"])
    assert create.exit_code == 0

    result = runner.invoke(
        cli, ["grab", "--single", "--agent=test-agent", "--no-content"]
    )
    assert result.exit_code == 0
    assert "I001" in result.output

    ideas_index = yaml.safe_load(
        (tmp_tasks_dir / ".tasks" / "ideas" / "index.yaml").read_text()
    )
    idea_path = tmp_tasks_dir / ".tasks" / "ideas" / ideas_index["ideas"][0]["file"]
    idea_content = idea_path.read_text()
    assert "status: in_progress" in idea_content
    assert "claimed_by: test-agent" in idea_content


def test_list_bugs_and_ideas_flags(runner, tmp_tasks_dir):
    bug_result = runner.invoke(cli, ["bug", "--title", "critical bug", "--simple"])
    assert bug_result.exit_code == 0

    idea_result = runner.invoke(cli, ["idea", "future planning idea"])
    assert idea_result.exit_code == 0

    bugs_only = runner.invoke(cli, ["list", "--bugs"])
    assert bugs_only.exit_code == 0
    assert "Bugs" in bugs_only.output
    assert "B001" in bugs_only.output
    assert "Ideas" not in bugs_only.output
    assert "Phase" not in bugs_only.output

    ideas_only = runner.invoke(cli, ["list", "--ideas"])
    assert ideas_only.exit_code == 0
    assert "Ideas" in ideas_only.output
    assert "I001" in ideas_only.output
    assert "Bugs" not in ideas_only.output
    assert "Phase" not in ideas_only.output


def test_list_bugs_and_ideas_short_aliases(runner, tmp_tasks_dir):
    bug_result = runner.invoke(cli, ["bug", "--title", "critical bug", "--simple"])
    assert bug_result.exit_code == 0

    idea_result = runner.invoke(cli, ["idea", "future planning idea"])
    assert idea_result.exit_code == 0

    bugs_only = runner.invoke(cli, ["list", "-b"])
    assert bugs_only.exit_code == 0
    assert "Bugs" in bugs_only.output
    assert "B001" in bugs_only.output
    assert "Ideas" not in bugs_only.output
    assert "Phase" not in bugs_only.output

    ideas_only = runner.invoke(cli, ["list", "-i"])
    assert ideas_only.exit_code == 0
    assert "Ideas" in ideas_only.output
    assert "I001" in ideas_only.output
    assert "Bugs" not in ideas_only.output
    assert "Phase" not in ideas_only.output


def test_list_aux_fallback_title_from_filename(runner, tmp_tasks_dir):
    bug_result = runner.invoke(cli, ["bug", "--title", "critical bug", "--simple"])
    assert bug_result.exit_code == 0

    idea_result = runner.invoke(cli, ["idea", "future planning idea"])
    assert idea_result.exit_code == 0

    bugs_index_path = tmp_tasks_dir / ".tasks" / "bugs" / "index.yaml"
    bugs_index = yaml.safe_load(bugs_index_path.read_text()) or {"bugs": []}
    if bugs_index.get("bugs"):
        bugs_index["bugs"][0].pop("title", None)
    bugs_index_path.write_text(yaml.safe_dump(bugs_index))

    ideas_index_path = tmp_tasks_dir / ".tasks" / "ideas" / "index.yaml"
    ideas_index = yaml.safe_load(ideas_index_path.read_text()) or {"ideas": []}
    if ideas_index.get("ideas"):
        ideas_index["ideas"][0].pop("title", None)
    ideas_index_path.write_text(yaml.safe_dump(ideas_index))

    bug_file = tmp_tasks_dir / ".tasks" / "bugs" / bugs_index["bugs"][0]["file"]
    bug_file.write_text(
        "---\n"
        "id: B001\n"
        "status: pending\n"
        "estimate_hours: 1\n"
        "complexity: medium\n"
        "priority: high\n"
        "depends_on: []\n"
        "tags: []\n"
        "---\n"
    )
    idea_file = tmp_tasks_dir / ".tasks" / "ideas" / ideas_index["ideas"][0]["file"]
    idea_file.write_text(
        "---\n"
        "id: I001\n"
        "status: pending\n"
        "estimate_hours: 1\n"
        "complexity: medium\n"
        "priority: high\n"
        "depends_on: []\n"
        "tags: []\n"
        "---\n"
    )

    result = runner.invoke(cli, ["list"])
    assert result.exit_code == 0
    assert "B001: critical bug" in result.output
    assert "I001: future planning idea" in result.output


def test_set_updates_multiple_fields(runner, tmp_tasks_dir):
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Original")

    result = runner.invoke(
        cli,
        [
            "set",
            "P1.M1.E1.T001",
            "--priority",
            "critical",
            "--complexity",
            "high",
            "--estimate",
            "3.5",
            "--title",
            "Updated Task",
            "--depends-on",
            "B060,P1.M1.E1.T002",
            "--tags",
            "bugfix,urgent",
            "--body",
            "Updated body content",
        ],
    )

    assert result.exit_code == 0
    task_file = (
        tmp_tasks_dir
        / ".tasks"
        / "01-test-phase"
        / "01-test-milestone"
        / "01-test-epic"
        / "T001-test-task.todo"
    )
    content = task_file.read_text()
    assert "title: Updated Task" in content
    assert "priority: critical" in content
    assert "complexity: high" in content
    assert "estimate_hours: 3.5" in content
    assert "- B060" in content
    assert "- P1.M1.E1.T002" in content
    assert "- bugfix" in content
    assert "- urgent" in content
    assert "Updated body content" in content
    assert "## Requirements" not in content


def test_set_rejects_invalid_dependency_id(runner, tmp_tasks_dir):
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Original")
    result = runner.invoke(
        cli,
        [
            "set",
            "P1.M1.E1.T001",
            "--depends-on",
            "not a task id",
        ],
    )
    assert result.exit_code != 0
    assert "invalid dependency id" in result.output


def test_set_requires_at_least_one_property(runner, tmp_tasks_dir):
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Original")
    result = runner.invoke(cli, ["set", "P1.M1.E1.T001"])
    assert result.exit_code != 0
    assert "set requires at least one property flag" in result.output


def test_set_appends_body_when_append_body_flag_set(runner, tmp_tasks_dir):
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Original body")

    result = runner.invoke(
        cli,
        [
            "set",
            "P1.M1.E1.T001",
            "--body",
            "Existing details",
            "--append-body",
        ],
    )
    assert result.exit_code == 0

    task_file = (
        tmp_tasks_dir
        / ".tasks"
        / "01-test-phase"
        / "01-test-milestone"
        / "01-test-epic"
        / "T001-test-task.todo"
    )
    content = task_file.read_text()
    assert "Existing details" in content
    assert "# A" in content


def test_set_auto_commits_when_no_staged_files(runner, tmp_tasks_dir):
    initialize_test_git_repo(tmp_tasks_dir)
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Original body")
    initial_commit_count = git_commit_count(tmp_tasks_dir)

    result = runner.invoke(
        cli,
        [
            "set",
            "P1.M1.E1.T001",
            "--title",
            "Appended Update",
            "--body",
            "Appended body",
            "--append-body",
        ],
    )
    assert result.exit_code == 0
    assert "Updated: P1.M1.E1.T001" in result.output

    assert git_commit_count(tmp_tasks_dir) == initial_commit_count + 1
    assert run_git(tmp_tasks_dir, "log", "-1", "--pretty=%B") == "bl set P1.M1.E1.T001: Appended Update"
    assert run_git(tmp_tasks_dir, "status", "--short") == ""


def test_set_auto_commit_skips_when_staged_files_exist(runner, tmp_tasks_dir):
    initialize_test_git_repo(tmp_tasks_dir)
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Original body")
    (tmp_tasks_dir / "staged-change.txt").write_text("staged\n")
    run_git(tmp_tasks_dir, "add", "staged-change.txt")
    initial_commit_count = git_commit_count(tmp_tasks_dir)

    result = runner.invoke(
        cli,
        [
            "set",
            "P1.M1.E1.T001",
            "--body",
            "Appended body",
            "--append-body",
        ],
    )
    assert result.exit_code == 0
    assert "Updated: P1.M1.E1.T001" in result.output

    assert git_commit_count(tmp_tasks_dir) == initial_commit_count
    assert "A  staged-change.txt" in run_git(tmp_tasks_dir, "status", "--short")


def test_lock_epic_blocks_add_and_unlock_restores_add(runner, tmp_tasks_dir_short_ids):
    lock_result = runner.invoke(cli, ["lock", "P1.M1.E1"])
    assert lock_result.exit_code == 0
    assert "Locked: P1.M1.E1" in lock_result.output

    blocked_add = runner.invoke(cli, ["add", "P1.M1.E1", "--title", "Should Fail"])
    assert blocked_add.exit_code != 0
    assert "has been closed and cannot accept new tasks" in blocked_add.output
    assert "create a new epic" in blocked_add.output.lower()

    unlock_result = runner.invoke(cli, ["unlock", "P1.M1.E1"])
    assert unlock_result.exit_code == 0
    assert "Unlocked: P1.M1.E1" in unlock_result.output

    allowed_add = runner.invoke(cli, ["add", "P1.M1.E1", "--title", "Should Succeed"])
    assert allowed_add.exit_code == 0
    assert "Created task:" in allowed_add.output
    assert "File:" in allowed_add.output
    assert ".tasks/01-test-phase/01-test-milestone/01-test-epic/" in allowed_add.output
    assert "Next:" in allowed_add.output
    assert "backlog show P1.M1.E1.T001" in allowed_add.output
    assert "backlog claim P1.M1.E1.T001" in allowed_add.output


def test_lock_milestone_and_phase_blocks_higher_level_adds(runner, tmp_tasks_dir_short_ids):
    lock_milestone = runner.invoke(cli, ["lock", "P1.M1"])
    assert lock_milestone.exit_code == 0
    blocked_epic_add = runner.invoke(cli, ["add-epic", "P1.M1", "--title", "Should Fail"])
    assert blocked_epic_add.exit_code != 0
    assert "has been closed and cannot accept new epics" in blocked_epic_add.output
    assert "create a new epic" in blocked_epic_add.output.lower()

    lock_phase = runner.invoke(cli, ["lock", "P1"])
    assert lock_phase.exit_code == 0
    blocked_milestone_add = runner.invoke(
        cli, ["add-milestone", "P1", "--title", "Should Fail"]
    )
    assert blocked_milestone_add.exit_code != 0
    assert "has been closed and cannot accept new milestones" in blocked_milestone_add.output


def test_add_commands_accept_optional_descriptions(runner, tmp_tasks_dir):
    """add-phase, add-milestone, and add-epic should persist --description text."""
    phase_description = "Stage rollout and release prep"
    milestone_description = "Prepare release docs and tests"
    epic_description = "Ship user documentation"

    phase_result = runner.invoke(
        cli,
        ["add-phase", "--title", "Second Phase", "--description", phase_description],
    )
    assert phase_result.exit_code == 0
    assert "File: .tasks/02-second-phase/index.yaml" in phase_result.output
    assert "Next:" in phase_result.output
    assert "backlog show P2" in phase_result.output
    assert "backlog add-milestone P2" in phase_result.output
    root_index = yaml.safe_load((tmp_tasks_dir / ".tasks" / "index.yaml").read_text())
    phase_entry = next(entry for entry in root_index["phases"] if entry["id"] == "P2")
    assert phase_entry["description"] == phase_description

    milestone_result = runner.invoke(
        cli,
        ["add-milestone", "P1", "--title", "Second Milestone", "--description", milestone_description],
    )
    assert milestone_result.exit_code == 0
    assert "File: .tasks/01-test-phase/02-second-milestone/index.yaml" in milestone_result.output
    assert "Next:" in milestone_result.output
    assert "  - backlog show P1.M2" in milestone_result.output
    assert "  - backlog add-epic P1.M2" in milestone_result.output

    milestone_id = milestone_result.output.split("Created milestone: ", 1)[1].split("\n", 1)[0].strip()
    milestone_index = yaml.safe_load(
        (tmp_tasks_dir / ".tasks" / "01-test-phase" / "index.yaml").read_text()
    )
    milestone_entry = next(
        entry
        for entry in milestone_index["milestones"]
        if entry.get("description") == milestone_description
    )
    assert milestone_entry["description"] == milestone_description

    epic_result = runner.invoke(
        cli,
        ["add-epic", "P1.M1", "--title", "Second Epic", "--description", epic_description],
    )
    assert epic_result.exit_code == 0
    assert "File: .tasks/01-test-phase/01-test-milestone/02-second-epic/index.yaml" in epic_result.output
    assert "Next:" in epic_result.output
    epic_id = epic_result.output.split("Created epic: ", 1)[1].split("\n", 1)[0].strip()
    assert f"  - backlog show {epic_id}" in epic_result.output
    assert (
        f'  - backlog add {epic_id} --title "<task title>"' in epic_result.output
    )
    epic_index = yaml.safe_load(
        (tmp_tasks_dir / ".tasks" / "01-test-phase" / "01-test-milestone" / "index.yaml").read_text()
    )
    epic_entry = next(
        entry for entry in epic_index["epics"] if entry.get("description") == epic_description
    )
    assert epic_entry["description"] == epic_description


def test_show_idea_pending_displays_instructions(runner, tmp_tasks_dir):
    """show on a pending idea should display an Instructions section."""
    create = runner.invoke(cli, ["idea", "refactor auth module"])
    assert create.exit_code == 0

    result = runner.invoke(cli, ["show", "I001"])
    assert result.exit_code == 0
    assert "Instructions:" in result.output
    assert "Read the idea file at" in result.output
    assert "tasks add-phase" in result.output
    assert "tasks add-milestone" in result.output
    assert "tasks add-epic" in result.output
    assert "tasks add" in result.output
    assert "Created Work Items" in result.output
    assert "Mark this idea as done" in result.output


def test_show_idea_non_pending_hides_instructions(runner, tmp_tasks_dir):
    """show on a non-pending idea should NOT display Instructions."""
    create = runner.invoke(cli, ["idea", "refactor auth module"])
    assert create.exit_code == 0

    # Claim it to change status to in_progress
    claim = runner.invoke(
        cli, ["grab", "--single", "--agent=test-agent", "--no-content"]
    )
    assert claim.exit_code == 0

    result = runner.invoke(cli, ["show", "I001"])
    assert result.exit_code == 0
    assert "Instructions:" not in result.output


def test_show_fixed_task(runner, tmp_tasks_dir):
    """show should display fixed task details when given an F*** ID."""
    fixed = runner.invoke(
        cli,
        [
            "fixed",
            "--title",
            "restore stale auth token",
            "--at",
            "2026-02-20T12:00:00Z",
            "--tags",
            "auth,hotfix",
            "--body",
            "Added regression guard for token refresh.",
        ],
    )
    assert fixed.exit_code == 0
    assert "Created fixed:" in fixed.output
    assert "F001" in fixed.output

    result = runner.invoke(cli, ["show", "F001"])
    assert result.exit_code == 0
    assert "F001" in result.output
    assert "restore stale auth token" in result.output
    assert "status=done" in result.output


def test_show_task_preview_uses_default_line_limit(runner, tmp_tasks_dir):
    """show should display only a preview of task body by default."""
    task_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")
    task_file_relative = task_file.relative_to(tmp_tasks_dir)

    result = runner.invoke(cli, ["show", "P1.M1.E1.T001"])
    assert result.exit_code == 0
    file_contents = task_file.read_text()
    assert (
        f"File stats: {task_file.stat().st_size} bytes, {len(file_contents.splitlines())} lines"
        in result.output
    )
    assert "Body:" in result.output
    assert "# Test Task" in result.output
    assert "- Acceptance criterion 2" not in result.output
    assert "... (2 more lines)" in result.output
    assert "To view the full file, run: cat" in result.output
    assert str(task_file_relative) in result.output


def test_show_task_long_prints_entire_body(runner, tmp_tasks_dir):
    """show --long should print the full task body."""
    task_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")

    result = runner.invoke(cli, ["show", "P1.M1.E1.T001", "--long"])
    assert result.exit_code == 0
    file_contents = task_file.read_text()
    assert (
        f"File stats: {task_file.stat().st_size} bytes, {len(file_contents.splitlines())} lines"
        in result.output
    )
    assert "Body:" in result.output
    assert "# Test Task" in result.output
    assert "- Acceptance criterion 2" in result.output
    assert "... (2 more lines)" not in result.output
    assert "To view the full file, run: cat" not in result.output


def test_show_task_all_prints_entire_task_file(runner, tmp_tasks_dir):
    """show --all should print the complete .todo file including frontmatter."""
    task_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")

    result = runner.invoke(cli, ["show", "P1.M1.E1.T001", "--all"])
    assert result.exit_code == 0
    file_contents = task_file.read_text()
    assert (
        f"File stats: {task_file.stat().st_size} bytes, {len(file_contents.splitlines())} lines"
        in result.output
    )
    assert "Task file:" in result.output
    assert "id: P1.M1.E1.T001" in result.output
    assert "# Test Task" in result.output
    assert "- Acceptance criterion 2" in result.output
    assert "... (2 more lines)" not in result.output
    assert "To view the full file, run: cat" not in result.output


def test_cat_single_task_prints_complete_task_file(runner, tmp_tasks_dir):
    """cat should print the raw .todo task file contents."""
    task_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")

    result = runner.invoke(cli, ["cat", "P1.M1.E1.T001"])

    assert result.exit_code == 0
    assert result.output == task_file.read_text()


def test_cat_multiple_tasks_prints_headers_between_files(runner, tmp_tasks_dir):
    """cat should separate multiple task files with one blank line and a header."""
    first_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "First Task")
    second_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Second Task")

    result = runner.invoke(cli, ["cat", "P1.M1.E1.T001", "P1.M1.E1.T002"])

    expected = (
        first_file.read_text()
        + "\n-------------------- [ P1.M1.E1.T002 ] --------------------\n"
        + second_file.read_text()
    )
    assert result.exit_code == 0
    assert result.output == expected


def test_cat_multiple_tasks_adds_blank_line_after_file_without_trailing_newline(
    runner, tmp_tasks_dir
):
    """cat should keep one blank line before headers even without a final newline."""
    first_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "First Task")
    second_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Second Task")
    first_raw = first_file.read_text().rstrip("\n")
    first_file.write_text(first_raw)

    result = runner.invoke(cli, ["cat", "P1.M1.E1.T001", "P1.M1.E1.T002"])

    expected = (
        first_raw
        + "\n\n-------------------- [ P1.M1.E1.T002 ] --------------------\n"
        + second_file.read_text()
    )
    assert result.exit_code == 0
    assert result.output == expected


def test_cat_prints_malformed_resolved_task_file_verbatim(runner, tmp_tasks_dir):
    """cat should not validate or warn about frontmatter for resolved files."""
    task_file = create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")
    raw = "---\n:\n---\n# Raw\n"
    task_file.write_text(raw)

    result = runner.invoke(cli, ["cat", "P1.M1.E1.T001"])

    assert result.exit_code == 0
    assert result.output == raw


def test_cat_prints_malformed_fixed_task_file_verbatim(runner, tmp_tasks_dir):
    """cat should resolve fixed tasks from the index without parsing their frontmatter."""
    fixes_dir = tmp_tasks_dir / ".tasks" / "fixes" / "2026-02"
    fixes_dir.mkdir(parents=True)
    index_path = tmp_tasks_dir / ".tasks" / "fixes" / "index.yaml"
    index_path.write_text(
        yaml.dump(
            {
                "fixes": [
                    {
                        "id": "F001",
                        "file": "2026-02/F001-ship-patch.todo",
                    }
                ]
            }
        )
    )
    raw = "---\n:\n---\n# Ship patch\nRaw body survives.\n"
    fixed_file = fixes_dir / "F001-ship-patch.todo"
    fixed_file.write_text(raw)

    result = runner.invoke(cli, ["cat", "F001"])

    assert result.exit_code == 0
    assert result.output == raw


def test_cat_task_not_found_shows_tree_hint(runner, tmp_tasks_dir):
    """cat should use show-style not-found guidance for missing tasks."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")

    result = runner.invoke(cli, ["cat", "P1.M1.E1.T999"])

    assert result.exit_code != 0
    assert "Task not found: P1.M1.E1.T999" in result.output
    assert "Tip: Use 'backlog tree P1.M1.E1' to verify available IDs." in result.output


def test_cat_help_shows_usage(runner, tmp_tasks_dir):
    """cat should include command-specific help text."""
    result = runner.invoke(cli, ["cat", "--help"])

    assert result.exit_code == 0
    assert "Usage:" in result.output
    assert "cat" in result.output


def test_show_task_displays_explicit_dependencies(runner, tmp_tasks_dir):
    """show should label explicit dependencies in task output."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Setup task", status="done")
    create_task_file(
        tmp_tasks_dir,
        "P1.M1.E1.T002",
        "Dependent task",
        depends_on=["P1.M1.E1.T001"],
    )

    result = runner.invoke(cli, ["show", "P1.M1.E1.T002"])
    assert result.exit_code == 0
    assert "Explicit dependencies:" in result.output
    assert "✓ P1.M1.E1.T001" in result.output
    assert "Legend: ✓ done | ✗ not done | ? not found" in result.output
    assert "Implicit dependency" not in result.output


def test_show_task_displays_implicit_dependency_when_no_explicit(runner, tmp_tasks_dir):
    """show should label implicit previous-task dependency when explicit deps are absent."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Setup task")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Dependent task", depends_on=[])

    result = runner.invoke(cli, ["show", "P1.M1.E1.T002"])
    assert result.exit_code == 0
    assert "Implicit dependency (previous in epic):" in result.output
    assert "P1.M1.E1.T001" in result.output
    assert "Explicit dependencies:" not in result.output
    assert "Legend: ✓ done | ✗ not done | ? not found" in result.output


def test_show_phase_displays_total_duration(runner, tmp_tasks_dir):
    """show on a phase should include total task duration."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task Two")

    result = runner.invoke(cli, ["show", "P1"])
    assert result.exit_code == 0
    assert "Total Duration:" in result.output
    assert "4.00 hours" in result.output


def test_show_milestone_displays_total_duration(runner, tmp_tasks_dir):
    """show on a milestone should include total task duration."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task Two")

    result = runner.invoke(cli, ["show", "P1.M1"])
    assert result.exit_code == 0
    assert "Total Duration:" in result.output
    assert "4.00 hours" in result.output


def test_show_epic_displays_total_duration(runner, tmp_tasks_dir):
    """show on an epic should include total task duration."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Task One")
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T002", "Task Two")

    result = runner.invoke(cli, ["show", "P1.M1.E1"])
    assert result.exit_code == 0
    assert "Total Duration:" in result.output
    assert "4.00 hours" in result.output


def test_show_phase_not_found_shows_tree_hint(runner, tmp_tasks_dir):
    """show should show a tree hint when a phase is not found."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")

    result = runner.invoke(cli, ["show", "P9"])

    assert result.exit_code != 0
    assert "Phase not found: P9" in result.output
    assert "Tip: Use 'backlog tree' to list available IDs." in result.output


def test_show_task_not_found_in_epic_shows_tree_hint(runner, tmp_tasks_dir):
    """show should show a scoped tree hint for a missing task."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Test Task")

    result = runner.invoke(cli, ["show", "P1.M1.E1.T999"])

    assert result.exit_code != 0
    assert "Task not found: P1.M1.E1.T999" in result.output
    assert "Tip: Use 'backlog tree P1.M1.E1' to verify available IDs." in result.output


def test_move_task_to_different_epic_renumbers_id(runner, tmp_tasks_dir_short_ids):
    """move should relocate a task to destination epic with renumbered ID."""
    tasks_root = tmp_tasks_dir_short_ids / ".tasks"
    target_epic_dir = (
        tasks_root / "01-test-phase" / "01-test-milestone" / "02-target-epic"
    )
    target_epic_dir.mkdir(parents=True, exist_ok=True)

    # Add a second epic under the milestone
    milestone_index_path = (
        tasks_root / "01-test-phase" / "01-test-milestone" / "index.yaml"
    )
    milestone_index = yaml.safe_load(milestone_index_path.read_text())
    milestone_index.setdefault("epics", []).append(
        {
            "id": "E2",
            "name": "Target Epic",
            "path": "02-target-epic",
            "status": "pending",
        }
    )
    milestone_index_path.write_text(yaml.dump(milestone_index))

    # Create destination epic index
    (target_epic_dir / "index.yaml").write_text(
        yaml.dump(
            {
                "id": "P1.M1.E2",
                "name": "Target Epic",
                "tasks": [],
            }
        )
    )

    # Existing task in source epic
    create_task_file(
        tmp_tasks_dir_short_ids, "P1.M1.E1.T001", "Move This Task", status="pending"
    )

    tree_before = TaskLoader().load()
    target_epic_id = None
    for p in tree_before.phases:
        for m in p.milestones:
            for e in m.epics:
                if e.path == "02-target-epic":
                    target_epic_id = e.id
                    break
    assert target_epic_id is not None

    result = runner.invoke(cli, ["move", "P1.M1.E1.T001", "--to", target_epic_id])

    assert result.exit_code == 0
    assert "Moved:" in result.output
    assert "New ID:" in result.output
    assert f"{target_epic_id}.T001" in result.output

    moved_task = target_epic_dir / "T001-move-this-task.todo"
    assert moved_task.exists()
    assert f"id: {target_epic_id}.T001" in moved_task.read_text()


def test_move_epic_to_different_milestone_remaps_descendant_ids(
    runner, tmp_tasks_dir_short_ids
):
    """move should relocate epic and remap child task IDs to new milestone prefix."""
    tasks_root = tmp_tasks_dir_short_ids / ".tasks"
    phase_dir = tasks_root / "01-test-phase"
    target_ms_dir = phase_dir / "02-target-ms"
    target_ms_dir.mkdir(parents=True, exist_ok=True)

    # Add second milestone under phase
    phase_index_path = phase_dir / "index.yaml"
    phase_index = yaml.safe_load(phase_index_path.read_text())
    phase_index.setdefault("milestones", []).append(
        {
            "id": "M2",
            "name": "Target Milestone",
            "path": "02-target-ms",
            "status": "pending",
        }
    )
    phase_index_path.write_text(yaml.dump(phase_index))

    # Create destination milestone index
    (target_ms_dir / "index.yaml").write_text(yaml.dump({"epics": []}))

    create_task_file(tmp_tasks_dir_short_ids, "P1.M1.E1.T001", "A", status="pending")

    tree_before = TaskLoader().load()
    target_ms_id = None
    for p in tree_before.phases:
        for m in p.milestones:
            if m.path == "02-target-ms":
                target_ms_id = m.id
                break
    assert target_ms_id is not None

    result = runner.invoke(cli, ["move", "P1.M1.E1", "--to", target_ms_id])

    assert result.exit_code == 0
    assert "Moved:" in result.output
    assert "New ID:" in result.output
    assert f"{target_ms_id}.E1" in result.output

    moved_task = target_ms_dir / "01-test-epic" / "T001-test-task.todo"
    assert moved_task.exists()
    assert f"id: {target_ms_id}.E1.T001" in moved_task.read_text()


def test_move_rejects_invalid_source_destination_pair(runner, tmp_tasks_dir):
    """move should reject unsupported hierarchy moves."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Invalid Move", status="pending")

    # Task cannot move directly to phase
    result = runner.invoke(cli, ["move", "P1.M1.E1.T001", "--to", "P1"])
    assert result.exit_code != 0
    assert "Invalid move" in result.output


def test_benchmark_command_reports_summary(runner, tmp_tasks_dir):
    """benchmark should show counts for loaded task tree parsing."""
    create_task_file(tmp_tasks_dir, "P1.M1.E1.T001", "Benchmark Task")

    json_result = runner.invoke(cli, ["benchmark", "--json"])
    assert json_result.exit_code == 0
    payload = json.loads(json_result.output)
    summary = payload["summary"]
    assert summary["node_counts"]["phases"] == 1
    assert summary["node_counts"]["milestones"] == 1
    assert summary["node_counts"]["epics"] == 1
    assert summary["task_files_total"] == 1
    assert summary["task_files_found"] == 1
    assert summary["task_files_missing"] == 0
    assert summary["parse_mode"] == "full"
    assert summary["parse_task_body"] is True
    assert summary["index_parse_ms"] >= 0
    assert summary["task_frontmatter_parse_ms"] >= 0
    assert summary["task_body_parse_ms"] >= 0
    assert summary["task_parse_other_ms"] >= 0

    json_no_body = runner.invoke(
        cli,
        ["benchmark", "--mode", "full", "--no-parse-body", "--json"],
    )
    assert json_no_body.exit_code == 0
    payload_no_body = json.loads(json_no_body.output)
    summary_no_body = payload_no_body["summary"]
    assert summary_no_body["parse_mode"] == "full"
    assert summary_no_body["parse_task_body"] is False
    assert summary_no_body["task_body_parse_ms"] == 0

    json_meta = runner.invoke(cli, ["benchmark", "--mode", "metadata", "--json"])
    assert json_meta.exit_code == 0
    metadata_summary = json.loads(json_meta.output)["summary"]
    assert metadata_summary["parse_mode"] == "metadata"
    assert metadata_summary["parse_task_body"] is False

    json_index = runner.invoke(cli, ["benchmark", "--mode", "index", "--json"])
    assert json_index.exit_code == 0
    index_summary = json.loads(json_index.output)["summary"]
    assert index_summary["parse_mode"] == "index"
    assert index_summary["parse_task_body"] is False
    assert index_summary["task_files_total"] == 1
    assert index_summary["task_files_found"] == 1
    assert index_summary["task_files_missing"] == 0

    text_result = runner.invoke(cli, ["benchmark"])
    assert text_result.exit_code == 0
    assert "Task Tree Benchmark" in text_result.output
    assert "Overall parse time" in text_result.output
    assert "Index parse time" in text_result.output
    assert "Task frontmatter parse time" in text_result.output
    assert "Task body parse time" in text_result.output
    assert "Parse mode" in text_result.output
