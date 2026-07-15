"""Integration tests for major command feature groups not covered elsewhere."""

import json
from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest
import yaml
from click.testing import CliRunner

from backlog.cli import cli


@pytest.fixture
def runner():
    return CliRunner()


def _write_task(
    epic_dir: Path, task_id: str, title: str, frontmatter_overrides: dict
) -> dict:
    """Create a task file and return its index entry."""
    task_short = task_id.split(".")[-1]
    filename = f"{task_short}-task.todo"

    frontmatter = {
        "id": task_id,
        "title": title,
        "status": "pending",
        "estimate_hours": 2.0,
        "complexity": "medium",
        "priority": "medium",
        "depends_on": [],
        "tags": [],
    }
    frontmatter.update(frontmatter_overrides)

    content = (
        "---\n"
        f"{yaml.dump(frontmatter, default_flow_style=False)}"
        "---\n\n"
        f"# {title}\n\n"
        "Task details.\n"
    )
    (epic_dir / filename).write_text(content)

    return {
        "id": task_id,
        "title": title,
        "file": filename,
        "status": frontmatter["status"],
        "estimate_hours": frontmatter["estimate_hours"],
        "complexity": frontmatter["complexity"],
        "priority": frontmatter["priority"],
        "depends_on": frontmatter.get("depends_on", []),
    }


@pytest.fixture
def tmp_feature_tasks_dir(tmp_path, monkeypatch):
    """Create a realistic task tree used to validate major command features."""
    now = datetime.now(timezone.utc)
    tasks_dir = tmp_path / ".tasks"
    tasks_dir.mkdir()

    # Root project index
    (tasks_dir / "index.yaml").write_text(
        yaml.dump(
            {
                "project": "Feature Test Project",
                "description": "Coverage for major command groups",
                "timeline_weeks": 4,
                "phases": [
                    {
                        "id": "P1",
                        "name": "Phase One",
                        "path": "01-phase-one",
                        "status": "in_progress",
                    }
                ],
            }
        )
    )

    # Phase + milestone indexes
    phase_dir = tasks_dir / "01-phase-one"
    phase_dir.mkdir()
    (phase_dir / "index.yaml").write_text(
        yaml.dump(
            {
                "milestones": [
                    {
                        "id": "M1",
                        "name": "Milestone One",
                        "path": "01-milestone-one",
                        "status": "in_progress",
                    }
                ]
            }
        )
    )

    milestone_dir = phase_dir / "01-milestone-one"
    milestone_dir.mkdir()
    (milestone_dir / "index.yaml").write_text(
        yaml.dump(
            {
                "epics": [
                    {
                        "id": "E1",
                        "name": "Core Epic",
                        "path": "01-core-epic",
                        "status": "in_progress",
                    },
                    {
                        "id": "E2",
                        "name": "Feature Epic",
                        "path": "02-feature-epic",
                        "status": "in_progress",
                    },
                ]
            }
        )
    )

    # Epic E1 tasks
    e1_dir = milestone_dir / "01-core-epic"
    e1_dir.mkdir()
    e1_tasks = [
        _write_task(
            e1_dir,
            "P1.M1.E1.T001",
            "Completed Setup Task",
            {
                "status": "done",
                "priority": "medium",
                "tags": ["setup"],
                "started_at": (now - timedelta(days=2, hours=2)).isoformat(),
                "completed_at": (now - timedelta(days=2)).isoformat(),
                "duration_minutes": 120,
            },
        ),
        _write_task(
            e1_dir,
            "P1.M1.E1.T002",
            "In Progress Core Task",
            {
                "status": "in_progress",
                "priority": "high",
                "tags": ["core"],
                "depends_on": ["P1.M1.E1.T001"],
                "claimed_by": "agent-a",
                "claimed_at": (now - timedelta(hours=3)).isoformat(),
                "started_at": (now - timedelta(hours=3)).isoformat(),
            },
        ),
    ]
    (e1_dir / "index.yaml").write_text(yaml.dump({"tasks": e1_tasks}))

    # Epic E2 tasks
    e2_dir = milestone_dir / "02-feature-epic"
    e2_dir.mkdir()
    e2_tasks = [
        _write_task(
            e2_dir,
            "P1.M1.E2.T001",
            "High Priority Feature Task",
            {
                "status": "pending",
                "priority": "high",
                "tags": ["backend", "searchable"],
            },
        ),
        _write_task(
            e2_dir,
            "P1.M1.E2.T002",
            "Waiting Feature Task",
            {
                "status": "pending",
                "priority": "medium",
                "depends_on": ["P1.M1.E1.T002"],
                "tags": ["backend"],
            },
        ),
        _write_task(
            e2_dir,
            "P1.M1.E2.T003",
            "Blocked Feature Task",
            {
                "status": "blocked",
                "priority": "low",
                "tags": ["blocked"],
            },
        ),
    ]
    (e2_dir / "index.yaml").write_text(yaml.dump({"tasks": e2_tasks}))

    # Working context for dash/workflow display commands
    (tasks_dir / ".context.yaml").write_text(
        yaml.dump(
            {
                "current_task": "P1.M1.E1.T002",
                "agent": "agent-a",
                "started_at": (now - timedelta(minutes=40)).isoformat(),
            }
        )
    )

    monkeypatch.chdir(tmp_path)
    return tmp_path


def test_data_export_json_contains_project_tree(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["data", "export", "--format", "json"])
    assert result.exit_code == 0

    payload = json.loads(result.output)
    assert payload["project"] == "Feature Test Project"
    assert len(payload["phases"]) == 1
    assert payload["stats"]["total_tasks"] == 5


def test_data_summary_json_reports_status_counts(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["data", "summary", "--format", "json"])
    assert result.exit_code == 0

    payload = json.loads(result.output)
    assert payload["overall"]["total_tasks"] == 5
    assert payload["overall"]["done"] == 1
    assert payload["overall"]["in_progress"] == 1
    assert payload["overall"]["pending"] == 2
    assert payload["overall"]["blocked"] == 1


def test_sync_writes_stats_to_nested_indexes(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["sync"])
    assert result.exit_code == 0
    assert "Synchronized task tree" in result.output

    root_index = yaml.safe_load((tmp_feature_tasks_dir / ".tasks" / "index.yaml").read_text())
    assert root_index["stats"]["total_tasks"] == 5
    assert root_index["stats"]["done"] == 1
    assert root_index["stats"]["in_progress"] == 1
    assert root_index["stats"]["blocked"] == 1
    assert root_index["stats"]["pending"] == 2

    phase_index = yaml.safe_load(
        (tmp_feature_tasks_dir / ".tasks" / "01-phase-one" / "index.yaml").read_text()
    )
    assert phase_index["stats"]["total_tasks"] == 5
    assert phase_index["stats"]["done"] == 1
    assert phase_index["stats"]["in_progress"] == 1
    assert phase_index["stats"]["blocked"] == 1
    assert phase_index["stats"]["pending"] == 2

    milestone_index = yaml.safe_load(
        (
            tmp_feature_tasks_dir
            / ".tasks"
            / "01-phase-one"
            / "01-milestone-one"
            / "index.yaml"
        ).read_text()
    )
    assert milestone_index["stats"]["total_tasks"] == 5
    assert milestone_index["stats"]["done"] == 1
    assert milestone_index["stats"]["in_progress"] == 1
    assert milestone_index["stats"]["blocked"] == 1
    assert milestone_index["stats"]["pending"] == 2

    epic_one_index = yaml.safe_load(
        (
            tmp_feature_tasks_dir
            / ".tasks"
            / "01-phase-one"
            / "01-milestone-one"
            / "01-core-epic"
            / "index.yaml"
        ).read_text()
    )
    assert epic_one_index["stats"]["total"] == 2
    assert epic_one_index["stats"]["done"] == 1
    assert epic_one_index["stats"]["in_progress"] == 1
    assert epic_one_index["stats"]["blocked"] == 0
    assert epic_one_index["stats"]["pending"] == 0

    epic_two_index = yaml.safe_load(
        (
            tmp_feature_tasks_dir
            / ".tasks"
            / "01-phase-one"
            / "01-milestone-one"
            / "02-feature-epic"
            / "index.yaml"
        ).read_text()
    )
    assert epic_two_index["stats"]["total"] == 3
    assert epic_two_index["stats"]["done"] == 0
    assert epic_two_index["stats"]["in_progress"] == 0
    assert epic_two_index["stats"]["blocked"] == 1
    assert epic_two_index["stats"]["pending"] == 2


def test_bug_accepts_positional_description_as_simple_title(
    runner, tmp_feature_tasks_dir
):
    result = runner.invoke(cli, ["bug", "fix flaky integration test"])
    assert result.exit_code == 0
    assert "Created bug:" in result.output
    assert "File: .tasks/bugs/B001-fix-flaky-integration-test.todo" in result.output
    assert "Next:" in result.output
    assert "backlog show B001" in result.output
    assert "backlog claim B001" in result.output
    assert "IMPORTANT" not in result.output

    bugs_index_path = tmp_feature_tasks_dir / ".tasks" / "bugs" / "index.yaml"
    bugs_index = yaml.safe_load(bugs_index_path.read_text())
    bug_file = bugs_index["bugs"][0]["file"]
    bug_content = (tmp_feature_tasks_dir / ".tasks" / "bugs" / bug_file).read_text()

    assert "title: fix flaky integration test" in bug_content
    assert "## Steps to Reproduce" not in bug_content


def test_bug_rejects_single_word_positional_title(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["bug", "list"])
    assert result.exit_code != 0
    assert "at least two words" in result.output


def test_bug_rejects_single_word_title_flag(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["bug", "--title", "fix"])
    assert result.exit_code != 0
    assert "at least two words" in result.output


def test_fixed_command_captures_metadata(runner, tmp_feature_tasks_dir):
    result = runner.invoke(
        cli,
        [
            "fixed",
            "--title",
            "restore stale auth token",
            "--description",
            "Restore token handling to avoid forced logouts",
            "--at",
            "2026-02-20T12:00:00Z",
            "--tags",
            "auth,hotfix",
            "--body",
            "Added regression guard for token refresh.",
        ],
    )

    assert result.exit_code == 0
    assert "Created fixed:" in result.output

    fixes_index_path = tmp_feature_tasks_dir / ".tasks" / "fixes" / "index.yaml"
    assert fixes_index_path.exists()

    fixes_index = yaml.safe_load(fixes_index_path.read_text())
    assert "fixes" in fixes_index
    assert len(fixes_index["fixes"]) == 1
    assert fixes_index["fixes"][0]["id"] == "F001"

    fix_file = (
        tmp_feature_tasks_dir
        / ".tasks"
        / "fixes"
        / fixes_index["fixes"][0]["file"]
    )
    assert fix_file.exists()
    content = fix_file.read_text()
    fm_text = content.split("---", 2)[1]
    fm = yaml.safe_load(fm_text)
    assert fm["id"] == "F001"
    assert fm["type"] == "fixed"
    assert fm["title"] == "restore stale auth token"
    assert fm["description"] == "Restore token handling to avoid forced logouts"
    assert fm["status"] == "done"
    assert fm["created_at"] == "2026-02-20T12:00:00+00:00"
    assert fm["completed_at"] == "2026-02-20T12:00:00+00:00"
    assert fm["tags"] == ["auth", "hotfix"]
    assert "Added regression guard for token refresh." in content
    assert fixes_index["fixes"][0]["file"] == "2026-02/F001-restore-stale-auth-token.todo"


def test_fixed_rejects_single_word_input(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["fixed", "fix"])
    assert result.exit_code != 0
    assert "at least two words" in result.output


def test_fixed_rejects_single_word_title_flag(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["fixed", "--title", "fix"])
    assert result.exit_code != 0
    assert "at least two words" in result.output


def test_grab_prioritizes_normal_tasks_over_ideas(runner, tmp_feature_tasks_dir):
    idea_result = runner.invoke(cli, ["idea", "collect future refactor idea"])
    assert idea_result.exit_code == 0

    grab_result = runner.invoke(
        cli, ["grab", "--single", "--agent", "agent-priority", "--no-content"]
    )
    assert grab_result.exit_code == 0
    assert "P1.M1.E2.T001" in grab_result.output
    assert "I001" not in grab_result.output


def test_grab_prioritizes_bugs_over_normal_tasks(runner, tmp_feature_tasks_dir):
    bug_result = runner.invoke(
        cli,
        [
            "bug",
            "--title",
            "tiny low bug",
            "--estimate",
            "0.1",
            "--complexity",
            "low",
            "--priority",
            "low",
            "--simple",
        ],
    )
    assert bug_result.exit_code == 0

    grab_result = runner.invoke(
        cli, ["grab", "--single", "--agent", "agent-priority", "--no-content"]
    )
    assert grab_result.exit_code == 0
    assert "B001" in grab_result.output


def test_session_command_lifecycle(runner, tmp_feature_tasks_dir):
    start = runner.invoke(
        cli, ["session", "start", "--agent", "agent-feature", "--task", "P1.M1.E2.T001"]
    )
    assert start.exit_code == 0
    assert "Session started" in start.output

    hb = runner.invoke(
        cli,
        [
            "session",
            "heartbeat",
            "--agent",
            "agent-feature",
            "--progress",
            "running tests",
        ],
    )
    assert hb.exit_code == 0
    assert "Heartbeat updated" in hb.output

    listed = runner.invoke(cli, ["session", "list", "--active"])
    assert listed.exit_code == 0
    assert "agent-feature" in listed.output

    end = runner.invoke(cli, ["session", "end", "--agent", "agent-feature"])
    assert end.exit_code == 0
    assert "Session ended" in end.output


def test_search_finds_task_by_pattern_and_tag_filter(runner, tmp_feature_tasks_dir):
    result = runner.invoke(
        cli, ["search", "High Priority Feature", "--tags", "backend", "--limit", "5"]
    )
    assert result.exit_code == 0
    assert "P1.M1.E2.T001" in result.output


def test_blockers_shows_dependency_chain_and_root_blocker(
    runner, tmp_feature_tasks_dir
):
    result = runner.invoke(cli, ["blockers", "--suggest"])
    assert result.exit_code == 0
    assert "Blocking Chains" in result.output
    assert "P1.M1.E1.T002" in result.output


def test_dash_renders_current_task_and_progress(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["dash"])
    assert result.exit_code == 0
    assert "Current Task" in result.output
    assert "P1.M1.E1.T002" in result.output
    assert "Progress:" in result.output


def test_timeline_renders_ascii_timeline(runner, tmp_feature_tasks_dir):
    result = runner.invoke(
        cli, ["timeline", "--weeks", "4", "--group-by", "phase", "--show-done"]
    )
    assert result.exit_code == 0
    assert "Project Timeline" in result.output
    assert "Legend:" in result.output


def test_tl_alias_matches_timeline_output(runner, tmp_feature_tasks_dir):
    timeline_result = runner.invoke(cli, ["timeline", "--weeks", "4", "--show-done"])
    alias_result = runner.invoke(cli, ["tl", "--weeks", "4", "--show-done"])

    assert timeline_result.exit_code == 0
    assert alias_result.exit_code == 0
    assert "Project Timeline" in alias_result.output
    assert "Legend:" in alias_result.output
    assert alias_result.output == timeline_result.output


def test_timeline_accepts_repeated_scope_flags(runner, tmp_feature_tasks_dir):
    result = runner.invoke(
        cli,
        ["timeline", "--scope", "P1", "--scope", "P1.M1", "--show-done"],
    )
    assert result.exit_code == 0
    assert "Project Timeline" in result.output

    invalid = runner.invoke(cli, ["timeline", "--scope", "P9"])
    assert invalid.exit_code != 0
    assert "No list nodes found for path query: P9" in invalid.output


def test_data_export_accepts_repeated_scope_flags(runner, tmp_feature_tasks_dir):
    result = runner.invoke(
        cli,
        ["data", "export", "--format", "json", "--scope", "P1", "--scope", "P1.M1"],
    )
    assert result.exit_code == 0
    payload = json.loads(result.output)
    assert payload["phases"]

    invalid = runner.invoke(cli, ["data", "export", "--format", "json", "--scope", "P9"])
    assert invalid.exit_code != 0
    assert "No list nodes found for path query: P9" in invalid.output


def test_report_progress_json(runner, tmp_feature_tasks_dir):
    bug_result = runner.invoke(cli, ["bug", "fix flaky integration test"])
    assert bug_result.exit_code == 0
    idea_result = runner.invoke(cli, ["idea", "capture future optimization idea"])
    assert idea_result.exit_code == 0

    result = runner.invoke(cli, ["report", "progress", "--format", "json"])
    assert result.exit_code == 0

    payload = json.loads(result.output)
    assert payload["overall"]["total"] == 5
    assert payload["overall"]["done"] == 1
    assert payload["overall"]["in_progress"] == 1
    assert payload["auxiliary"]["bugs"]["total"] == 1
    assert payload["auxiliary"]["ideas"]["total"] == 1
    assert payload["bugs"][0]["id"] == "B001"
    assert payload["ideas"][0]["id"] == "I001"

    text_result = runner.invoke(cli, ["report", "progress"])
    assert text_result.exit_code == 0
    assert "Auxiliary:" in text_result.output
    assert "All Bugs" in text_result.output
    assert "All Ideas" in text_result.output


def test_report_velocity_json(runner, tmp_feature_tasks_dir):
    result = runner.invoke(
        cli, ["report", "velocity", "--days", "3", "--format", "json"]
    )
    assert result.exit_code == 0

    payload = json.loads(result.output)
    assert payload["days_analyzed"] == 3
    assert len(payload["daily_data"]) == 3
    assert payload["total_completed"] >= 1


def test_velocity_hidden_alias_matches_report_velocity_json(
    runner, tmp_feature_tasks_dir
):
    alias_result = runner.invoke(cli, ["velocity", "--days", "3", "--format", "json"])
    report_result = runner.invoke(
        cli, ["report", "velocity", "--days", "3", "--format", "json"]
    )

    assert alias_result.exit_code == 0
    assert report_result.exit_code == 0

    alias_payload = json.loads(alias_result.output)
    report_payload = json.loads(report_result.output)

    assert alias_payload["days_analyzed"] == report_payload["days_analyzed"]
    assert alias_payload["total_completed"] == report_payload["total_completed"]
    assert alias_payload["daily_data"] == report_payload["daily_data"]


def test_velocity_alias_is_listed_in_help(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["--help"])

    assert result.exit_code == 0
    assert "\n  velocity" in result.output


def test_version_command_outputs_version(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["version"])
    assert result.exit_code == 0
    assert "backlog version 0.0.0" in result.output

    extra_arg_result = runner.invoke(cli, ["version", "P1.M1.E1.T001"])
    assert extra_arg_result.exit_code != 0
    assert "Usage:" in extra_arg_result.output


def test_help_sync_renders_command_help(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["help", "sync"])
    assert result.exit_code == 0
    assert "Usage:" in result.output
    assert "sync [OPTIONS]" in result.output
    assert "Recalculate statistics and critical path." in result.output


def test_no_args_shows_usage_help(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, [])
    assert result.exit_code == 0
    assert "Usage:" in result.output
    assert "Commands:" in result.output


def test_ci_validate_ids_command_accepts_shorthand_ids(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["ci", "validate-ids", "B020", "E1.T02"], mix_stderr=False)
    assert result.exit_code == 0
    assert result.output == ""


def test_ci_validate_ids_command_rejects_invalid_id(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["ci", "validate-ids", "B020", "not-an-id"], mix_stderr=False)
    assert result.exit_code != 0
    assert result.output == ""


def test_timeline_alias_is_shown_inline_in_help(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["--help"])

    assert result.exit_code == 0
    assert "timeline" in result.output
    assert "alias: tl" in result.output
    assert "\n  tl " not in result.output


def test_howto_is_pinned_to_top_of_help(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["--help"])

    assert result.exit_code == 0
    commands_section = result.output.split("Commands:\n", 1)[1]
    first_command_line = next(
        line for line in commands_section.splitlines() if line.startswith("  ")
    )
    assert first_command_line.strip().startswith("howto")


def test_all_commands_have_non_thin_help(runner, tmp_feature_tasks_dir):
    commands = [
        "howto",
        "add",
        "add-epic",
        "add-milestone",
        "add-phase",
        "agents",
        "benchmark",
        "ci",
        "blocked",
        "blockers",
        "bug",
        "cat",
        "check",
        "claim",
        "cycle",
        "dash",
        "data",
        "done",
        "fixed",
        "grab",
        "handoff",
        "idea",
        "init",
        "list",
        "lock",
        "log",
        "ls",
        "migrate",
        "move",
        "next",
        "preview",
        "report",
        "schema",
        "search",
        "session",
        "set",
        "show",
        "skills",
        "skip",
        "sync",
        "timeline",
        "velocity",
        "tree",
        "version",
        "unclaim",
        "unclaim-stale",
        "undone",
        "unlock",
        "update",
        "why",
        "work",
    ]

    for command in commands:
        result = runner.invoke(cli, [command, "--help"])
        assert result.exit_code == 0
        assert "Usage:" in result.output
        assert len(result.output.strip()) > 40


def test_howto_command_outputs_text_and_json(runner, tmp_feature_tasks_dir):
    text_result = runner.invoke(cli, ["howto"])
    assert text_result.exit_code == 0
    assert "# Backlog How-To" in text_result.output
    assert "Core Work Loop" in text_result.output

    json_result = runner.invoke(cli, ["howto", "--json"])
    assert json_result.exit_code == 0
    payload = json.loads(json_result.output)
    assert payload["name"] == "backlog-howto"
    assert isinstance(payload["skill_version"], str)
    assert "# Backlog How-To" in payload["content"]


def test_report_estimate_accuracy_json(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["report", "estimate-accuracy", "--format", "json"])
    assert result.exit_code == 0

    payload = json.loads(result.output)
    assert payload["tasks_analyzed"] >= 1
    assert payload["total_estimated_hours"] > 0
    assert payload["total_actual_hours"] > 0


def test_list_json_priority_filter_stats(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["list", "--json", "--priority", "high"])
    assert result.exit_code == 0

    payload = json.loads(result.output)
    assert payload["filter"]["priority"] == "high"
    assert payload["filtered_stats"]["total_tasks"] == 2
    assert payload["filtered_stats"]["in_progress"] == 1
    assert payload["filtered_stats"]["pending"] == 1


def test_list_json_priority_and_complexity_filters(runner, tmp_feature_tasks_dir):
    result = runner.invoke(
        cli, ["list", "--json", "--priority", "high", "--complexity", "medium"]
    )
    assert result.exit_code == 0

    payload = json.loads(result.output)
    assert payload["filter"]["priority"] == "high"
    assert payload["filter"]["complexity"] == "medium"
    assert payload["filtered_stats"]["total_tasks"] == 2


def test_list_available_priority_filter(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["list", "--available", "--priority", "high"])
    assert result.exit_code == 0

    # Only the pending high-priority task is available in this fixture.
    assert "P1.M1.E2.T001" in result.output
    assert "P1.M1.E2.T002" not in result.output
    assert "P1.M1.E2.T003" not in result.output


def test_list_available_includes_bugs_and_ideas(runner, tmp_feature_tasks_dir):
    """list --available should include bugs and ideas in output."""
    bug = runner.invoke(cli, ["bug", "--title", "critical bug", "--simple"])
    assert bug.exit_code == 0
    idea = runner.invoke(cli, ["idea", "capture a planning intake"])
    assert idea.exit_code == 0

    result = runner.invoke(cli, ["list", "--available"])
    assert result.exit_code == 0
    assert "B001: critical bug" in result.output
    assert "I001: capture a planning intake" in result.output


def test_list_progress_shows_phase_and_milestone_ids(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["list", "--progress"])
    assert result.exit_code == 0

    # Check that phase ID is shown (e.g., "P1:")
    assert "P1:" in result.output
    # Check that milestone ID is shown (e.g., "P1.M1:")
    assert "P1.M1:" in result.output
    # Check the header
    assert "Project Progress" in result.output


def test_list_progress_shows_in_progress_color_segment(runner, tmp_feature_tasks_dir):
    result = runner.invoke(cli, ["list", "--progress"], color=True)
    assert result.exit_code == 0

    # In-progress work should have a distinct bar segment.
    assert "▓" in result.output
