"""CLI commands for backlog management."""

import copy
import os
import click
import json
import subprocess
import yaml
from pathlib import Path
from types import SimpleNamespace
from builtins import next as builtin_next
from datetime import datetime, timezone
from rich.console import Console

from .models import PathQuery, Status, TaskPath, Complexity, Priority
from .loader import TaskLoader
from .critical_path import CriticalPathCalculator
from .time_utils import utc_now, to_utc
from .status import (
    claim_task,
    complete_task,
    update_status,
    StatusError,
    check_stale_claims,
)
from .data_dir import get_data_dir_name
from .helpers import (
    get_current_task_id,
    set_current_task,
    get_all_current_tasks,
    set_multi_task_context,
    get_all_tasks,
    is_bug_id,
    is_task_id,
    is_idea_id,
    is_fixed_id,
    task_file_path,
    is_task_file_missing,
    find_missing_task_files,
    find_newly_unblocked,
    make_progress_bar,
    print_completion_notices,
    format_phase_path,
    format_milestone_path,
    format_epic_path,
    filter_tree_by_path_query,
    parse_dependency_ids,
)
from .autocommit import run_with_auto_commit
from .commands.generated_backlog_howto import (
    BACKLOG_HOWTO_SKILL_MD,
    BACKLOG_HOWTO_SKILL_VERSION,
)


console = Console()

PREVIEW_DISPLAY_LIMIT = 5
PREVIEW_GRAB_FOLLOW_COUNT = 4
PREVIEW_AUX_LIMIT = 5
PREVIEW_BUG_FANOUT_COUNT = 2
BACKLOG_VERSION = "0.1.0"

AGENTS_SNIPPETS = {
    "short": """# AGENTS.md (Short)

# Work Loop & Task Backlog

## Task Workflow
- Use `backlog grab` to claim work, then `backlog done` or `backlog cycle`.
- If a command fails to parse args/usage, run exactly one recovery command: `backlog cycle`.
- For explicit task IDs, use `backlog claim <TASK_ID> [TASK_ID ...]`.
- Prefer critical-path work, then `critical > high > medium > low` priority.
- If blocked, run `backlog blocked --reason "<why>"` and handoff quickly.
- Keep each change scoped to one task; update status as soon as state changes.
- Before done: run targeted tests for changed code.
- For more see `backlog --help`.
""",
    "medium": """# AGENTS.md (Medium)

# Work Loop & Task Backlog

## Defaults
- Claim with `backlog grab` (or `backlog grab --single` for focused work).
- Use `backlog claim <TASK_ID> [TASK_ID ...]` when task IDs are provided.
- If command argument parsing fails, run `backlog cycle` once to recover.
- CLI selection order is: critical-path first, then task priority.
- Use `backlog work <id>` when switching context; use `backlog show` to review details.

## Execution Loop
1. `backlog grab` and read the task file.
2. Implement in small commits and keep diff narrow.
3. Run focused tests early, then broader tests before completion.
4. Finish with `backlog done` (or `backlog cycle` to continue immediately).

## Coordination
- Use `backlog handoff --to <agent> --notes "<context>"` for ownership transfer.
- Use `backlog blockers --suggest` and `backlog why <task-id>` when sequencing is unclear.
- Run `backlog dash` and `backlog report progress` for health checks.
""",
    "long": """# AGENTS.md (Long)

# Work Loop & Task Backlog

## Operating Model
- Default command: `backlog`. Use local `.backlog/` state as source of truth.
- Selection strategy: critical-path first, then `critical > high > medium > low`.
- Treat task files as contracts: requirements + acceptance criteria drive scope.

## Standard Loop
1. Claim:
    - `backlog grab` for normal flow.
    - `backlog grab --single` for strict focus.
    - `backlog claim <TASK_ID> [TASK_ID ...]` for explicit IDs.
    - If a command fails with parsing/usage errors, run `backlog cycle` once.
2. Inspect:
   - `backlog show` for current task details.
   - `backlog why <task-id>` to inspect dependency readiness.
3. Implement:
   - Keep commits and PRs small and task-scoped.
   - Add or update tests with each behavior change.
4. Validate:
   - Run targeted tests first, full suite before completion if feasible.
5. Resolve:
   - `backlog done` when complete.
   - `backlog cycle` when moving directly to next claim.

## Multi-Agent Defaults
- Use handoff when parallelism helps:
  - `backlog handoff --to <agent> --notes "<state + next steps>"`
- If blocked:
  - `backlog blocked --reason "<root cause>"`
  - `backlog blocked --reason "<root cause>" --grab` (optional)
  - Unblock owner or dependency explicitly.
- For triage:
  - `backlog blockers --deep --suggest`
  - `backlog search "<pattern>" --status=pending`

## Quality Gates
- Ensure behavior is covered by tests.
- Prefer deterministic, fast tests.
- Do not mark done with unresolved blockers, hidden assumptions, or failing tests.
""",
}


class BacklogGroup(click.Group):
    """Click group with agent how-to command pinned at top of help output."""

    def list_commands(self, ctx):
        commands = super().list_commands(ctx)
        if "howto" in commands:
            commands.remove("howto")
            return ["howto", *commands]
        return commands


def load_config():
    """Load configuration."""
    from .data_dir import BACKLOG_DIR, TASKS_DIR

    defaults = {
        "agent": {"default_agent": "cli-user", "auto_claim_after_done": False},
        "session": {"heartbeat_timeout_minutes": 15},
        "stale_claim": {"warn_after_minutes": 60, "error_after_minutes": 120},
        "complexity_multipliers": {
            "low": 1.0,
            "medium": 1.25,
            "high": 1.5,
            "critical": 2.0,
        },
        "display": {"progress_bar_style": "unicode"},
        "timeline": {"default_weeks": 8, "hours_per_week": 40},
    }

    backlog_config = Path(BACKLOG_DIR) / "config.yaml"
    tasks_config = Path(TASKS_DIR) / "config.yaml"
    if backlog_config.exists():
        config_path = backlog_config
    elif tasks_config.exists():
        config_path = tasks_config
    else:
        config_path = None

    if config_path and config_path.exists():
        with open(config_path) as f:
            loaded = yaml.safe_load(f) or {}
        for key, value in defaults.items():
            if key not in loaded:
                loaded[key] = value
            elif isinstance(value, dict):
                for k, v in value.items():
                    if k not in loaded[key]:
                        loaded[key][k] = v
        return loaded

    return defaults


def get_default_agent():
    """Get the default agent ID from config."""
    config = load_config()
    return config.get("agent", {}).get("default_agent", "cli-user")


def _warn_missing_task_file(task) -> bool:
    """Warn when a task references a missing .todo file."""
    from .data_dir import get_data_dir_name

    if not task or not is_task_file_missing(task):
        return False

    console.print(
        f"[yellow]Warning:[/] Task file missing for {task.id}: {get_data_dir_name()}/{task.file}"
    )
    return True


def _warn_task_file_issues(task) -> bool:
    """Warn for task file parse or id-mismatch issues."""
    from .data_dir import get_data_dir_name

    task_file = task_file_path(task)
    if not task_file.exists():
        console.print(
            f"[yellow]Warning:[/] Task file missing for {task.id}: {get_data_dir_name()}/{task.file}"
        )
        return True

    try:
        content = task_file.read_text()
    except OSError as exc:
        console.print(
            f"[yellow]Warning:[/] Task file for {task.id} is not readable: {exc}"
        )
        return False

    lines = content.splitlines()
    if not lines or lines[0].strip() != "---":
        console.print(
            f"[yellow]Warning:[/] Invalid frontmatter format in {task.id}: missing opening `---` marker."
        )
        return False

    end_index = -1
    for idx in range(1, len(lines)):
        if lines[idx].strip() == "---":
            end_index = idx
            break

    if end_index < 0:
        console.print(
            f"[yellow]Warning:[/] Invalid frontmatter format in {task.id}: missing closing `---` marker."
        )
        return False

    frontmatter_raw = "\n".join(lines[1:end_index]).strip()
    if not frontmatter_raw:
        return False

    try:
        frontmatter = yaml.safe_load(frontmatter_raw)
    except Exception as exc:
        console.print(f"[yellow]Warning:[/] Invalid task file YAML in {task.id}: {exc}")
        return False

    if not isinstance(frontmatter, dict):
        console.print(
            f"[yellow]Warning:[/] Invalid frontmatter in {task.id}: expected a mapping."
        )
        return False

    file_id = str(frontmatter.get("id", "")).strip()
    if file_id and file_id != str(task.id):
        console.print(
            f"[yellow]Warning:[/] Frontmatter ID mismatch in {task.id}: {file_id}"
        )
    return False


SHOW_TASK_PREVIEW_LINES = 12


def _warn_missing_task_files(tree, limit: int = 5) -> int:
    """Warn when list output includes tasks with missing files."""
    from .data_dir import get_data_dir_name

    missing_tasks = find_missing_task_files(tree)
    if not missing_tasks:
        return 0

    console.print(
        f"[yellow]Warning:[/] {len(missing_tasks)} task file(s) referenced in index are missing."
    )
    for task in missing_tasks[:limit]:
        console.print(f"  - {task.id}: {get_data_dir_name()}/{task.file}")
    if len(missing_tasks) > limit:
        console.print(f"  ... and {len(missing_tasks) - limit} more")
    console.print()
    return len(missing_tasks)


def _warn_non_task_cycle(calc: CriticalPathCalculator, suppress: bool = False) -> None:
    """Warn when the full hierarchy graph contains a cycle but explicit deps do not."""
    if suppress:
        return

    cycle = calc.find_cycle()
    if cycle:
        console.print(
            f"[yellow]Warning:[/] non-task dependency cycle detected: {' -> '.join(cycle)}"
        )


def _require_at_least_two_words(text: str, context: str) -> None:
    if len(text.split()) < 2:
        raise ValueError(f"{context} requires at least two words")


def _format_relative_age(timestamp) -> str:
    """Format a timestamp as a short relative age string."""
    delta = utc_now() - to_utc(timestamp)
    seconds = int(delta.total_seconds())
    if seconds < 60:
        return f"{seconds}s ago"
    minutes = seconds // 60
    if minutes < 60:
        return f"{minutes}m ago"
    hours = minutes // 60
    if hours < 24:
        return f"{hours}h ago"
    days = hours // 24
    if days < 7:
        return f"{days}d ago"
    return f"{days // 7}w ago"


def _activity_events(tree):
    """Build a timeline of activity events from task metadata."""
    from pathlib import Path

    events = []
    data_dir = Path(get_data_dir_name())

    for task in get_all_tasks(tree):
        task_file = data_dir / task.file

        if task.completed_at:
            events.append(
                {
                    "task_id": task.id,
                    "title": task.title,
                    "event": "completed",
                    "timestamp": task.completed_at,
                    "actor": task.claimed_by,
                }
            )
        if task.started_at:
            events.append(
                {
                    "task_id": task.id,
                    "title": task.title,
                    "event": "started",
                    "timestamp": task.started_at,
                    "actor": task.claimed_by,
                }
            )
        if task.claimed_at:
            events.append(
                {
                    "task_id": task.id,
                    "title": task.title,
                    "event": "claimed",
                    "timestamp": task.claimed_at,
                    "actor": task.claimed_by,
                }
            )
        if (
            not task.claimed_at
            and not task.started_at
            and not task.completed_at
            and task_file.exists()
        ):
            events.append(
                {
                    "task_id": task.id,
                    "title": task.title,
                    "event": "added",
                    "timestamp": datetime.fromtimestamp(task_file.stat().st_mtime, tz=timezone.utc),
                    "actor": None,
                }
            )

    event_order = {"added": 0, "claimed": 1, "started": 2, "completed": 3}
    events.sort(
        key=lambda item: (to_utc(item["timestamp"]), event_order[item["event"]]),
        reverse=True,
    )
    return events


def _activity_icon(event_name: str) -> str:
    """Return a rich icon for activity entries."""
    if event_name == "completed":
        return "[green]✓[/]"
    if event_name == "started":
        return "[blue]▶[/]"
    if event_name == "claimed":
        return "[yellow]✎[/]"
    return "[magenta]✚[/]"


def _activity_kind(event_name: str) -> str:
    """Classify an event for user-facing readability."""
    if event_name == "added":
        return "created"
    return event_name


def _format_ms(ms: float) -> str:
    """Format millisecond durations for CLI output."""
    return f"{ms:.2f}ms"


def _print_benchmark(benchmark: dict, top_n: int, output_json: bool) -> None:
    """Render benchmark statistics for the user."""
    files = benchmark.get("files", {})
    counts = benchmark.get("counts", {})
    total_files = files.get("total", 0)
    by_type = files.get("by_type", {})
    by_type_ms = files.get("by_type_ms", {})
    task_total = counts.get("tasks", 0)
    missing_task_files = benchmark.get("missing_task_files", 0)
    parsed_task_files = task_total - missing_task_files
    parse_mode = benchmark.get("parse_mode", "full")
    parse_body = benchmark.get("parse_task_body", True)
    index_parse_ms = benchmark.get("index_parse_ms", 0.0)
    task_frontmatter_parse_ms = benchmark.get("task_frontmatter_parse_ms", 0.0)
    task_body_parse_ms = benchmark.get("task_body_parse_ms", 0.0)
    other_ms = max(
        0.0,
        benchmark.get("overall_ms", 0.0)
        - index_parse_ms
        - task_frontmatter_parse_ms
        - task_body_parse_ms,
    )

    slowest_phases = sorted(
        benchmark.get("phase_timings", []), key=lambda t: t.get("ms", 0.0), reverse=True
    )[:top_n]
    slowest_milestones = sorted(
        benchmark.get("milestone_timings", []),
        key=lambda t: t.get("ms", 0.0),
        reverse=True,
    )[:top_n]
    slowest_epics = sorted(
        benchmark.get("epic_timings", []), key=lambda t: t.get("ms", 0.0), reverse=True
    )[:top_n]

    if output_json:
        payload = {
            **benchmark,
            "summary": {
                "overall_ms": benchmark.get("overall_ms", 0.0),
                "files_parsed": total_files,
                "task_files_total": task_total,
                "task_files_found": parsed_task_files,
                "task_files_missing": missing_task_files,
                "index_parse_ms": index_parse_ms,
                "task_frontmatter_parse_ms": task_frontmatter_parse_ms,
                "task_body_parse_ms": task_body_parse_ms,
                "task_parse_other_ms": other_ms,
                "parse_mode": parse_mode,
                "parse_task_body": parse_body,
                "node_counts": {
                    "phases": counts.get("phases", 0),
                    "milestones": counts.get("milestones", 0),
                    "epics": counts.get("epics", 0),
                },
            },
            "slowest": {
                "phases": slowest_phases,
                "milestones": slowest_milestones,
                "epics": slowest_epics,
            },
        }
        console.print(json.dumps(payload, indent=2))
        return

    console.print("\n[bold cyan]Task Tree Benchmark[/]\n")
    console.print(
        f"Overall parse time: [yellow]{_format_ms(benchmark.get('overall_ms', 0.0))}[/]"
    )
    console.print(f"Parse mode: [yellow]{parse_mode}[/]")
    console.print(f"Task body parsing: [yellow]{'enabled' if parse_body else 'disabled'}[/]")
    console.print(
        f"Index parse time: [yellow]{_format_ms(index_parse_ms)}[/]"
    )
    console.print(
        f"Task frontmatter parse time: [yellow]{_format_ms(task_frontmatter_parse_ms)}[/]"
    )
    console.print(f"Task body parse time: [yellow]{_format_ms(task_body_parse_ms)}[/]")
    console.print(f"Other parse time: [yellow]{_format_ms(other_ms)}[/]")
    console.print(f"Total files parsed: [yellow]{total_files}[/]")
    console.print(
        f"Task files (leaves): [yellow]{task_total}[/] "
        f"([green]{parsed_task_files}[/] found, [red]{missing_task_files}[/] missing)"
    )
    console.print(f"Phases parsed: [yellow]{counts.get('phases', 0)}[/]")
    console.print(f"Milestones parsed: [yellow]{counts.get('milestones', 0)}[/]")
    console.print(f"Epics parsed: [yellow]{counts.get('epics', 0)}[/]")
    console.print()

    console.print("[bold]Files by type[/]")
    for file_type in sorted(by_type):
        count = by_type[file_type]
        ms_total = by_type_ms.get(file_type, 0.0)
        if count:
            console.print(
                f"  {file_type}: [yellow]{count}[/] files ({_format_ms(ms_total)})"
            )

    if slowest_phases:
        console.print("\n[bold]Slowest phases[/]")
        for item in slowest_phases:
            console.print(
                f"  {item['id']} ({item['path']}): {_format_ms(item['ms'])}"
            )

    if slowest_milestones:
        console.print("\n[bold]Slowest milestones[/]")
        for item in slowest_milestones:
            console.print(
                f"  {item['id']} ({item['path']}): {_format_ms(item['ms'])}"
            )

    if slowest_epics:
        console.print("\n[bold]Slowest epics[/]")
        for item in slowest_epics:
            console.print(
                f"  {item['id']} ({item['path']}): {_format_ms(item['ms'])}"
            )

    console.print()


@click.group(invoke_without_command=True, cls=BacklogGroup)
@click.version_option(version=BACKLOG_VERSION)
@click.pass_context
def cli(ctx):
    """The Backlogs - CLI for project backlog management."""
    if ctx.invoked_subcommand is None:
        import sys
        import os
        import shutil

        if sys.stdout.isatty():
            from .logo import render_stack2

            term_width = shutil.get_terminal_size((80, 24)).columns
            lines = render_stack2(width=term_width, seed=3)
            use_color = "NO_COLOR" not in os.environ
            for line in lines:
                if not use_color:
                    from .logo import _strip_ansi

                    line = _strip_ansi(line)
                print(line)
        print()
        click.echo(ctx.get_help())


@cli.command()
def version():
    """Show CLI version."""
    click.echo(f"backlog version {BACKLOG_VERSION}")


HELP_COMMAND_ALIASES = {"ls": "list", "r": "report", "tl": "timeline"}


@cli.command(name="help")
@click.argument("command_name", required=False)
@click.pass_context
def help_command(ctx, command_name):
    """Show command overview and command-specific guidance."""
    parent_ctx = ctx.parent or ctx

    if command_name is None:
        click.echo(parent_ctx.get_help())
        return

    command_name = HELP_COMMAND_ALIASES.get(command_name, command_name)
    command = parent_ctx.command.get_command(parent_ctx, command_name)
    if command is None:
        raise click.ClickException(f"Unknown command: {command_name}")

    with click.Context(command, info_name=command_name, parent=parent_ctx) as sub_ctx:
        click.echo(command.get_help(sub_ctx))


@cli.group()
def ci():
    """CI-focused helpers for automation workflows."""


@ci.command("validate-ids")
@click.argument("task_ids", nargs=-1)
def validate_ids(task_ids):
    """Validate task IDs for automation tooling."""
    if not task_ids:
        raise click.UsageError("validate-ids requires at least one TASK_ID")

    invalid_ids = [task_id for task_id in task_ids if not is_task_id(task_id)]
    if invalid_ids:
        raise click.ClickException(f"Invalid task ID: {invalid_ids[0]}")


@cli.command()
@click.option("--json", "output_json", is_flag=True, help="Output as JSON")
def howto(output_json):
    """Show the agent how to use backlog effectively."""
    if output_json:
        click.echo(
            json.dumps(
                {
                    "name": "backlog-howto",
                    "skill_version": BACKLOG_HOWTO_SKILL_VERSION,
                    "content": BACKLOG_HOWTO_SKILL_MD,
                },
                indent=2,
            )
        )
        return
    click.echo(BACKLOG_HOWTO_SKILL_MD)


@cli.command()
@click.option("--json", "output_json", is_flag=True, help="Output as JSON")
@click.option(
    "--top",
    default=10,
    type=click.IntRange(min=1),
    help="Number of slowest items to show",
)
@click.option(
    "--mode",
    type=click.Choice(["full", "metadata", "index"]),
    default="full",
    help="Load mode used for task-level parsing",
)
@click.option(
    "--parse-body/--no-parse-body",
    "parse_task_body",
    default=True,
    help="Parse task body content (full mode).",
)
def benchmark(output_json, top, mode, parse_task_body):
    """Show benchmark timings for loading the full task tree."""
    try:
        loader = TaskLoader()
        _, benchmark = loader.load_with_benchmark(
            mode=mode, parse_task_body=parse_task_body
        )
        _print_benchmark(benchmark, top, output_json)
    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


# ============================================================================
# Init Command
# ============================================================================


@cli.command()
@click.option("--project", "-p", required=True, help="Project name")
@click.option("--description", "-d", default="", help="Project description")
@click.option("--timeline-weeks", "-w", default=0, type=int, help="Timeline in weeks")
def init(project, description, timeline_weeks):
    """Initialize a new .backlog project."""
    index_path = Path(".backlog/index.yaml")
    if index_path.exists():
        raise click.ClickException("Already initialized (.backlog/index.yaml exists)")

    Path(".backlog").mkdir(parents=True, exist_ok=True)
    data = {
        "project": project,
        "description": description,
        "timeline_weeks": timeline_weeks,
        "phases": [],
    }
    with open(index_path, "w") as f:
        yaml.dump(data, f, default_flow_style=False)
    click.echo(f'Initialized project "{project}" in .backlog/')


# ============================================================================
# Log Command
# ============================================================================


@cli.command()
@click.option(
    "--limit",
    default=20,
    type=click.IntRange(min=1),
    help="Maximum number of log entries to show",
)
@click.option("--json", "output_json", is_flag=True, help="Output as JSON")
@click.option("-b", "--bugs", is_flag=True, help="Show only bug tasks")
@click.option("-i", "--ideas", is_flag=True, help="Show only idea tasks")
def log(limit, output_json, bugs, ideas):
    """Show recent activity for tasks in a git-log style list."""
    try:
        loader = TaskLoader()
        include_normal = not (bugs or ideas)
        include_bugs = bugs or include_normal
        include_ideas = ideas or include_normal
        tree = loader.load("metadata", include_bugs=include_bugs, include_ideas=include_ideas)
        events = [event for event in _activity_events(tree) if (
            (is_bug_id(event["task_id"]) and include_bugs and not include_normal) or
            (is_idea_id(event["task_id"]) and include_ideas and not include_normal) or
            ((not is_bug_id(event["task_id"]) and not is_idea_id(event["task_id"]) and include_normal))
        )][:limit]

        if output_json:
            json_out = []
            for event in events:
                ts = event["timestamp"]
                json_out.append(
                    {
                        "task_id": event["task_id"],
                        "title": event["title"],
                        "event": event["event"],
                        "kind": _activity_kind(event["event"]),
                        "timestamp": ts.isoformat(),
                        "actor": event["actor"],
                    }
                )
            click.echo(json.dumps(json_out, indent=2))
            return

        if not events:
            console.print("[yellow]No recent activity found.[/]")
            return

        console.print("\n[bold cyan]Recent Activity Log[/]\n")
        for event in events:
            age = _format_relative_age(event["timestamp"])
            actor = f" ({event['actor']})" if event["actor"] else ""
            kind = _activity_kind(event["event"])
            console.print(
                f"{_activity_icon(event['event'])} [{kind}] [{event['event']}] {event['task_id']}{actor}"
            )
            console.print(f"  {event['title']}")
            console.print(f"  {event['timestamp'].isoformat()} ({age})\n")
    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


# ============================================================================
# List Command
# ============================================================================


def _merge_scoped_phases(scoped_phase_sets):
    merged = {}
    phase_order = []
    for phases in scoped_phase_sets:
        for phase in phases:
            if phase.id not in merged:
                merged[phase.id] = copy.deepcopy(phase)
                phase_order.append(phase.id)
                continue
            phase_out = merged[phase.id]
            milestone_map = {m.id: m for m in phase_out.milestones}
            for milestone in phase.milestones:
                if milestone.id not in milestone_map:
                    phase_out.milestones.append(copy.deepcopy(milestone))
                    milestone_map[milestone.id] = phase_out.milestones[-1]
                    continue
                milestone_out = milestone_map[milestone.id]
                epic_map = {e.id: e for e in milestone_out.epics}
                for epic in milestone.epics:
                    if epic.id not in epic_map:
                        milestone_out.epics.append(copy.deepcopy(epic))
                        epic_map[epic.id] = milestone_out.epics[-1]
                        continue
                    epic_out = epic_map[epic.id]
                    task_ids = {t.id for t in epic_out.tasks}
                    for task in epic.tasks:
                        if task.id not in task_ids:
                            epic_out.tasks.append(copy.deepcopy(task))
                            task_ids.add(task.id)
    return [merged[phase_id] for phase_id in phase_order]


def _resolve_list_scope(tree, raw_scope):
    scope_id = raw_scope
    scope_query = PathQuery.parse(scope_id)
    scoped_phases = filter_tree_by_path_query(tree, scope_query)
    if not scoped_phases and scope_id:
        fallback = (
            tree.find_phase(scope_id)
            or tree.find_milestone(scope_id)
            or tree.find_epic(scope_id)
        )
        if fallback:
            scope_id = fallback.id
            scope_query = PathQuery.parse(scope_id)
            scoped_phases = filter_tree_by_path_query(tree, scope_query)
    if not scoped_phases:
        raise ValueError(f"No list nodes found for path query: {raw_scope}")
    try:
        depth = TaskPath.parse(scope_id).depth
    except ValueError:
        depth = len(scope_query.segments)
    return scoped_phases, depth


@cli.command()
@click.option("--status", help="Filter by status (comma-separated)")
@click.option("--phase", help="Filter by phase ID")
@click.option("--milestone", help="Filter by milestone ID (e.g., M1, M2)")
@click.option("--epic", help="Filter by epic ID")
@click.option("--critical", is_flag=True, help="Show only critical path tasks")
@click.option("-a", "--available", is_flag=True, help="Show all unblocked/available tasks")
@click.option(
    "--complexity",
    type=click.Choice(["low", "medium", "high", "critical"]),
    help="Filter by complexity level",
)
@click.option(
    "--priority",
    type=click.Choice(["low", "medium", "high", "critical"]),
    help="Filter by priority level",
)
@click.option("--progress", "show_progress", is_flag=True, help="Show progress bars")
@click.option("--json", "output_json", is_flag=True, help="Output as JSON")
@click.option("--all", "show_all", is_flag=True, help="Show all milestones (no limit)")
@click.option("--unfinished", is_flag=True, help="Show only unfinished items")
@click.option("-b", "--bugs", is_flag=True, help="Show only bug tasks")
@click.option("-i", "--ideas", is_flag=True, help="Show only idea tasks")
@click.option(
    "--show-completed-aux",
    is_flag=True,
    help="Include completed/cancelled/rejected bugs and ideas",
)
@click.argument("scopes", nargs=-1)
def list(
    status,
    phase,
    milestone,
    epic,
    critical,
    available,
    complexity,
    priority,
    show_progress,
    output_json,
    show_all,
    unfinished,
    bugs,
    ideas,
    show_completed_aux,
    scopes,
):
    """List tasks with filtering options."""
    try:
        loader = TaskLoader()
        include_normal = not bugs and not ideas
        include_bugs = bugs or (not bugs and not ideas)
        include_ideas = ideas or (not bugs and not ideas)
        scope_inputs = [*scopes]
        scoped = bool(scope_inputs or phase or milestone or epic)
        if scoped:
            include_normal = True
            include_bugs = False
            include_ideas = False
        tree = loader.load(
            "metadata",
            include_bugs=include_bugs,
            include_ideas=include_ideas,
        )
        config = load_config()

        calc = CriticalPathCalculator(tree, config["complexity_multipliers"])
        critical_path, next_available = calc.calculate(explicit_only=True)
        tree.critical_path = critical_path
        tree.next_available = next_available
        if not output_json:
            _warn_missing_task_files(tree)
            _warn_non_task_cycle(calc)

        scope_query = None
        scoped_phases = None
        scoped_depth = None
        scope_task_ids = None

        if scoped:
            scoped_phase_sets = []
            scoped_depths = []
            if phase:
                phase_obj = tree.find_phase(phase)
                if not phase_obj:
                    raise ValueError(f"Phase not found: {phase}")
                scoped_item_phases, depth = _resolve_list_scope(tree, phase_obj.id)
                scoped_phase_sets.append(scoped_item_phases)
                scoped_depths.append(depth)
            elif milestone:
                milestone_obj = tree.find_milestone(milestone)
                if not milestone_obj:
                    raise ValueError(f"Milestone not found: {milestone}")
                scoped_item_phases, depth = _resolve_list_scope(tree, milestone_obj.id)
                scoped_phase_sets.append(scoped_item_phases)
                scoped_depths.append(depth)
            elif epic:
                epic_obj = tree.find_epic(epic)
                if not epic_obj:
                    raise ValueError(f"Epic not found: {epic}")
                scoped_item_phases, depth = _resolve_list_scope(tree, epic_obj.id)
                scoped_phase_sets.append(scoped_item_phases)
                scoped_depths.append(depth)
            else:
                for raw_scope in scope_inputs:
                    scoped_item_phases, depth = _resolve_list_scope(tree, raw_scope)
                    scoped_phase_sets.append(scoped_item_phases)
                    scoped_depths.append(depth)

            scope_query = None
            scoped_phases = _merge_scoped_phases(scoped_phase_sets)
            scoped_depth = max(scoped_depths) if scoped_depths else None
            scope_task_ids = {
                t.id
                for p in scoped_phases
                for m in p.milestones
                for e in m.epics
                for t in e.tasks
            }

        effective_show_completed_aux = show_completed_aux or (show_all and (bugs or ideas))
        if scoped:
            effective_show_completed_aux = False

        # Handle --progress flag
        if show_progress:
            _list_with_progress(
                tree,
                critical_path,
                complexity,
                priority,
                unfinished,
                effective_show_completed_aux,
                include_normal,
                include_bugs,
                include_ideas,
                scoped_phases,
            )
            return

        # Handle --available flag
        if available:
            all_available = calc.find_all_available()
            if scope_task_ids is not None:
                all_available = [t for t in all_available if t in scope_task_ids]
            _list_available(
                tree,
                all_available,
                critical_path,
                output_json,
                complexity,
                priority,
                include_normal,
                include_bugs,
                include_ideas,
                scoped_phases,
            )
            return

        # Default list view
        if output_json:
            _list_json(
                tree,
                critical_path,
                next_available,
                complexity,
                priority,
                show_all,
                unfinished,
                effective_show_completed_aux,
                include_normal,
                include_bugs,
                include_ideas,
                scoped_phases=scoped_phases,
                scope_query=scope_query,
            )
        else:
            _list_text(
                tree,
                critical_path,
                complexity,
                priority,
                show_all,
                unfinished,
                effective_show_completed_aux,
                include_normal,
                include_bugs,
                include_ideas,
                scoped_phases=scoped_phases,
                scope_query=scope_query,
                scoped_depth=(
                    min(4, scoped_depth + 2) if scoped and scoped_depth else None
                ),
            )

    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


def _task_matches_filters(task, complexity=None, priority=None):
    """Return True when a task matches complexity/priority filters."""
    if complexity and task.complexity.value != complexity:
        return False
    if priority and task.priority.value != priority:
        return False
    return True


def _is_unfinished(status):
    """Check if a status represents unfinished work."""
    return status not in (Status.DONE, Status.CANCELLED, Status.REJECTED)


def _filter_unfinished_tasks(tasks):
    """Filter tasks to only unfinished ones."""
    return [t for t in tasks if _is_unfinished(t.status)]


def _find_list_task(tree, task_id):
    """Resolve normal/auxiliary task IDs for list/available rendering."""
    if task_id.startswith("B"):
        for bug in getattr(tree, "bugs", []):
            if bug.id == task_id:
                return bug
    elif task_id.startswith("I"):
        for idea in getattr(tree, "ideas", []):
            if idea.id == task_id:
                return idea
    return tree.find_task(task_id)


def _preview_grab_candidates(tree, calc, primary_task):
    """Compute what `backlog grab` would claim after this primary task."""
    if not primary_task:
        return []

    if is_bug_id(primary_task.id):
        candidate_ids = calc.find_additional_bugs(
            primary_task, count=PREVIEW_BUG_FANOUT_COUNT
        )
    else:
        candidate_ids = calc.find_sibling_tasks(
            primary_task, count=PREVIEW_GRAB_FOLLOW_COUNT
        )

    candidates = []
    for task_id in candidate_ids:
        task = tree.find_task(task_id)
        if not task or is_task_file_missing(task):
            continue
        if task in candidates:
            continue
        candidates.append(task)

    return candidates


def _preview_task_payload(task, critical_path, calc, tree, output_json=False):
    """Build output payload for a preview row."""
    item = {
        "id": task.id,
        "title": task.title,
        "status": task.status.value,
        "file": task.file,
        "file_exists": not is_task_file_missing(task),
        "estimate_hours": task.estimate_hours,
        "complexity": task.complexity.value,
        "priority": task.priority.value,
        "on_critical_path": task.id in critical_path,
        "grab_additional": [t.id for t in _preview_grab_candidates(tree, calc, task)],
    }
    if output_json:
        return item
    item["path"] = f".tasks/{task.file}"
    return item


def _include_aux_item(status, unfinished=False, show_completed_aux=False):
    """Check whether auxiliary list items (bugs/ideas) should be shown."""
    if unfinished:
        return _is_unfinished(status)
    if show_completed_aux:
        return True
    return _is_unfinished(status)


def _has_unfinished_tasks(epic):
    """Check if epic has any unfinished tasks."""
    return any(_is_unfinished(t.status) for t in epic.tasks)


def _has_unfinished_epics(milestone):
    """Check if milestone has any unfinished epics."""
    return any(_has_unfinished_tasks(e) for e in milestone.epics)


def _has_unfinished_milestones(phase):
    """Check if phase has any unfinished milestones."""
    return any(_has_unfinished_epics(m) for m in phase.milestones)


def _calculate_task_stats(tasks):
    """Calculate task statistics."""
    return {
        "done": sum(1 for t in tasks if t.status == Status.DONE),
        "total": len(tasks),
    }


def _get_epic_stats(epic):
    """Get statistics for an epic."""
    return _calculate_task_stats(epic.tasks)


def _get_milestone_stats(milestone):
    """Get statistics for a milestone."""
    tasks = [t for e in milestone.epics for t in e.tasks]
    return _calculate_task_stats(tasks)


def _get_phase_stats(phase):
    """Get statistics for a phase."""
    tasks = [t for m in phase.milestones for e in m.epics for t in e.tasks]
    return _calculate_task_stats(tasks)


def _get_status_icon(status):
    """Get a colored status icon for display."""
    if status == Status.DONE:
        return "[green][✓][/]"
    elif status == Status.IN_PROGRESS:
        return "[yellow][→][/]"
    elif status == Status.PENDING:
        return "[ ]"
    elif status == Status.BLOCKED:
        return "[red][✗][/]"
    else:  # CANCELLED or REJECTED
        return "[dim][X][/]"


def _show_filter_banner(complexity=None, priority=None):
    """Display active list filters."""
    parts = []
    if complexity:
        parts.append(f"complexity: {complexity}")
    if priority:
        parts.append(f"priority: {priority}")
    if parts:
        console.print(f"[dim]Filtering by {'; '.join(parts)}[/]\n")


def _build_progress_bar(done: int, in_progress: int, total: int, width: int = 20) -> str:
    if total == 0:
        return "[dim]" + "░" * width + "[/]"

    safe_done = max(done, 0)
    safe_in_progress = max(in_progress, 0)
    safe_in_progress = min(safe_in_progress, max(total - safe_done, 0))

    done_width = min(width, round((safe_done / total) * width))
    in_progress_width = min(
        width - done_width,
        round(((safe_done + safe_in_progress) / total) * width) - done_width,
    )
    pending_width = max(width - done_width - in_progress_width, 0)

    done_bar = "█" * done_width
    in_progress_bar = "▓" * in_progress_width
    pending_bar = "░" * pending_width

    return (
        f"[green]{done_bar}[/][yellow]{in_progress_bar}[/][dim]{pending_bar}[/]"
    )


def _list_with_progress(
    tree,
    critical_path,
    complexity=None,
    priority=None,
    unfinished=False,
    show_completed_aux=False,
    include_normal=True,
    include_bugs=True,
    include_ideas=True,
    scoped_phases=None,
):
    """Show list with progress bars."""
    console.print("\n[bold cyan]Project Progress[/]\n")
    _show_filter_banner(complexity, priority)

    phases_to_show = (
        scoped_phases
        if scoped_phases is not None
        else tree.phases if include_normal else []
    )
    if unfinished:
        phases_to_show = [p for p in phases_to_show if _has_unfinished_milestones(p)]

    completed_phases = []
    for phase in phases_to_show:
        # Filter tasks when filters are specified
        if complexity or priority:
            all_tasks = []
            for m in phase.milestones:
                for e in m.epics:
                    all_tasks.extend(
                        [
                            t
                            for t in e.tasks
                            if _task_matches_filters(t, complexity, priority)
                        ]
                    )

            if not all_tasks:
                continue

            # Recalculate stats for filtered tasks
            done = sum(1 for t in all_tasks if t.status.value == "done")
            total = len(all_tasks)
            in_progress = sum(1 for t in all_tasks if t.status.value == "in_progress")
            blocked = sum(1 for t in all_tasks if t.status.value == "blocked")
        else:
            p_stats = phase.stats
            done = p_stats["done"]
            total = p_stats["total_tasks"]
            in_progress = p_stats["in_progress"]
            blocked = p_stats["blocked"]

        pct = (done / total * 100) if total > 0 else 0

        if pct == 100:
            completed_phases.append((phase.id, phase.name, total))
            continue

        bar = _build_progress_bar(done, in_progress, total)

        if in_progress > 0:
            indicator = "[yellow]→[/]"
        elif blocked > 0:
            indicator = "[red]🔒[/]"
        else:
            indicator = "[ ]"

        console.print(f"{indicator} [bold]{phase.id}: {phase.name}[/]")
        console.print(f"    {bar} {pct:5.1f}% ({done}/{total})")

        # Show milestones if phase is in progress
        for m in phase.milestones:
            # Filter milestone tasks when filters are specified
            if complexity or priority:
                milestone_tasks = []
                for e in m.epics:
                    milestone_tasks.extend(
                        [
                            t
                            for t in e.tasks
                            if _task_matches_filters(t, complexity, priority)
                        ]
                    )

                if not milestone_tasks:
                    continue

                m_done = sum(1 for t in milestone_tasks if t.status.value == "done")
                m_total = len(milestone_tasks)
                m_in_progress = sum(
                    1 for t in milestone_tasks if t.status.value == "in_progress"
                )
            else:
                m_stats = m.stats
                m_done = m_stats["done"]
                m_total = m_stats["total_tasks"]
                m_in_progress = m_stats["in_progress"]

            m_pct = (m_done / m_total * 100) if m_total > 0 else 0

            if m_pct == 100:
                continue

            m_bar = _build_progress_bar(m_done, m_in_progress, m_total, width=15)

            if m_in_progress > 0:
                m_ind = "[yellow]→[/]"
            else:
                m_ind = "○"

            console.print(f"    {m_ind} {m.id}: {m_bar} {m_pct:4.0f}%")

        console.print()

    # Show bugs summary in progress view
    bugs = (
        [
            b
            for b in getattr(tree, "bugs", [])
            if _include_aux_item(b.status, unfinished, show_completed_aux)
        ]
        if include_bugs
        else []
    )
    if bugs:
        bugs_done = sum(1 for b in bugs if b.status == Status.DONE)
        bugs_total = len(bugs)
        bug_pct = (bugs_done / bugs_total * 100) if bugs_total > 0 else 0
        bug_bar = make_progress_bar(bugs_done, bugs_total)
        indicator = "[green]✓[/]" if bug_pct == 100 else "🐛"
        console.print(f"{indicator} [bold]Bugs[/]")
        console.print(f"    {bug_bar} {bug_pct:5.1f}% ({bugs_done}/{bugs_total})")
        console.print()

    ideas = (
        [
            i
            for i in getattr(tree, "ideas", [])
            if _include_aux_item(i.status, unfinished, show_completed_aux)
        ]
        if include_ideas
        else []
    )
    if ideas:
        ideas_done = sum(1 for i in ideas if i.status == Status.DONE)
        ideas_total = len(ideas)
        idea_pct = (ideas_done / ideas_total * 100) if ideas_total > 0 else 0
        idea_bar = make_progress_bar(ideas_done, ideas_total)
        indicator = "[green]✓[/]" if idea_pct == 100 else "💡"
        console.print(f"{indicator} [bold]Ideas[/]")
        console.print(f"    {idea_bar} {idea_pct:5.1f}% ({ideas_done}/{ideas_total})")
        console.print()

    if completed_phases:
        completed_str = ", ".join(
            f"{pid} ({total})" for pid, pname, total in completed_phases
        )
        console.print(f"[green]✓ Completed:[/] {completed_str}")
        console.print()


def _show_milestone_detail(tree, m, critical_path, complexity=None, priority=None):
    """Show milestone detail view."""
    console.print(f"\n[bold cyan]Milestone:[/] {m.id} - {m.name}\n")
    console.print(f"[bold]Status:[/] {m.status.value}")
    console.print(f"[bold]Estimate:[/] {m.estimate_hours} hours")
    console.print(f"[bold]Complexity:[/] {m.complexity.value}")

    _show_filter_banner(complexity, priority)

    # Calculate stats with optional filters
    if complexity or priority:
        all_tasks = []
        for e in m.epics:
            all_tasks.extend(
                [t for t in e.tasks if _task_matches_filters(t, complexity, priority)]
            )

        done = sum(1 for t in all_tasks if t.status.value == "done")
        total = len(all_tasks)
        in_progress = sum(1 for t in all_tasks if t.status.value == "in_progress")
        blocked = sum(1 for t in all_tasks if t.status.value == "blocked")
    else:
        m_stats = m.stats
        done = m_stats["done"]
        total = m_stats["total_tasks"]
        in_progress = m_stats["in_progress"]
        blocked = m_stats["blocked"]

    console.print(f"[bold]Progress:[/] {done}/{total} tasks done")
    if in_progress:
        console.print(f"[bold]In Progress:[/] {in_progress} tasks")
    if blocked:
        console.print(f"[bold yellow]Blocked:[/] {blocked} tasks")

    console.print(f"\n[bold]Epics:[/]")
    for e in m.epics:
        # Filter tasks when filters are specified
        if complexity or priority:
            filtered_tasks = [
                t for t in e.tasks if _task_matches_filters(t, complexity, priority)
            ]
            if not filtered_tasks:
                continue
            e_done = sum(1 for t in filtered_tasks if t.status.value == "done")
            e_total = len(filtered_tasks)
        else:
            filtered_tasks = e.tasks
            e_stats = e.stats
            e_done = e_stats["done"]
            e_total = e_stats["total"]

        status_icon = "✓" if e_done == e_total else "○"
        console.print(
            f"\n  [{status_icon}] [bold]{e.name}[/] ({e_done}/{e_total} tasks)"
        )
        console.print(f"      Path: {format_epic_path(tree, e)}")

        for t in filtered_tasks:
            status_icons = {
                "done": "[green]✓[/]",
                "in_progress": "[yellow]→[/]",
                "pending": "[ ]",
                "blocked": "[red]✗[/]",
            }
            icon = status_icons.get(t.status.value, "?")
            console.print(f"      {icon} {t.id}: {t.title} ({t.estimate_hours}h)")

    console.print()


def _list_available(
    tree,
    all_available,
    critical_path,
    output_json,
    complexity=None,
    priority=None,
    include_normal=True,
    include_bugs=True,
    include_ideas=True,
    scoped_phases=None,
):
    """List available tasks with optional complexity/priority filtering."""
    scope_phases = (
        {p.id for p in scoped_phases} if scoped_phases is not None else None
    )
    resolved_available = []
    for task_id in all_available:
        task = _find_list_task(tree, task_id)
        if not task:
            continue
        if not _task_matches_filters(task, complexity, priority):
            continue
        if scope_phases is not None:
            try:
                task_phase = TaskPath.parse(task_id).phase
            except ValueError:
                continue
            if task_phase not in scope_phases:
                continue
        if (
            (task_id.startswith("B") and include_bugs)
            or (task_id.startswith("I") and include_ideas)
            or (not task_id.startswith("B") and not task_id.startswith("I") and include_normal)
        ):
            resolved_available.append(task)

    if not resolved_available:
        if complexity or priority:
            filters = []
            if complexity:
                filters.append(f"complexity={complexity}")
            if priority:
                filters.append(f"priority={priority}")
            console.print(
                f"[yellow]No available tasks found with filters: {', '.join(filters)}[/]"
            )
        else:
            console.print("[yellow]No available tasks found.[/]")
        return

    console.print(f"\n[bold green]Available Tasks ({len(resolved_available)}):[/]\n")
    _show_filter_banner(complexity, priority)

    if output_json:
        output = []
        for task in resolved_available:
            output.append(
                {
                    "id": task.id,
                    "title": task.title,
                    "estimate_hours": task.estimate_hours,
                    "complexity": task.complexity.value,
                    "priority": task.priority.value,
                    "on_critical_path": task.id in critical_path,
                }
            )
        click.echo(json.dumps(output, indent=2))
        return

    by_phase = {}
    bugs = []
    ideas = []

    for task in resolved_available:
        if task.id.startswith("B"):
            bugs.append(task)
            continue
        if task.id.startswith("I"):
            ideas.append(task)
            continue
        task_path = TaskPath.parse(task.id)
        if task_path.phase not in by_phase:
            by_phase[task_path.phase] = []
        by_phase[task_path.phase].append(task)

    for phase_id, tasks in sorted(by_phase.items()):
        p = tree.find_phase(phase_id)
        if p:
            console.print(f"\n[bold cyan]{p.name}[/] ({len(tasks)} available)")
            for t in tasks:
                crit_marker = "[yellow]★[/] " if t.id in critical_path else "  "
                console.print(f"  {crit_marker}[bold]{t.id}:[/] {t.title}")
                console.print(f"     {t.estimate_hours}h, {t.complexity.value}")

    if bugs:
        console.print(f"\n[bold cyan]Bugs ({len(bugs)} available)[/]")
        for task in bugs:
            crit_marker = "[yellow]★[/] " if task.id in critical_path else "  "
            console.print(f"  {crit_marker}[bold]{task.id}:[/] {task.title}")

    if ideas:
        console.print(f"\n[bold cyan]Ideas ({len(ideas)} available)[/]")
        for task in ideas:
            crit_marker = "[yellow]★[/] " if task.id in critical_path else "  "
            console.print(f"  {crit_marker}[bold]{task.id}:[/] {task.title}")

    console.print(f"\n[dim]★ = On critical path[/]")
    console.print(
        "[dim]Use `bl claim TASK_ID` to claim a specific task, or `backlog grab` to auto-claim the best available task.[/]"
    )


def _list_json(
    tree,
    critical_path,
    next_available,
    complexity=None,
    priority=None,
    show_all=False,
    unfinished=False,
    show_completed_aux=False,
    include_normal=True,
    include_bugs=True,
    include_ideas=True,
    scoped_phases=None,
    scope_query=None,
):
    """Output list as JSON."""
    phases_to_show = (
        scoped_phases
        if scoped_phases is not None
        else tree.phases if include_normal else []
    )
    if unfinished:
        phases_to_show = [p for p in phases_to_show if _has_unfinished_milestones(p)]

    bugs_for_json = (
        [
            {
                "id": b.id,
                "title": b.title,
                "status": b.status.value,
                "priority": b.priority.value,
                "estimate_hours": b.estimate_hours,
                "on_critical_path": b.id in critical_path,
            }
            for b in getattr(tree, "bugs", [])
            if _include_aux_item(b.status, unfinished, show_completed_aux)
        ]
        if include_bugs
        else []
    )

    ideas_for_json = (
        [
            {
                "id": i.id,
                "title": i.title,
                "status": i.status.value,
                "priority": i.priority.value,
                "estimate_hours": i.estimate_hours,
                "on_critical_path": i.id in critical_path,
            }
            for i in getattr(tree, "ideas", [])
            if _include_aux_item(i.status, unfinished, show_completed_aux)
        ]
        if include_ideas
        else []
    )

    output = {
        "critical_path": critical_path,
        "next_available": next_available,
        "stats": tree.stats,
        "phases": [
            {
                "id": p.id,
                "name": p.name,
                "status": p.status.value,
                "stats": _get_phase_stats(p),
                "milestones": [
                    {
                        "id": m.id,
                        "name": m.name,
                        "status": m.status.value,
                        "stats": _get_milestone_stats(m),
                    }
                    for m in (
                        p.milestones
                        if not unfinished
                        else [m for m in p.milestones if _has_unfinished_epics(m)]
                    )
                ],
            }
            for p in phases_to_show
        ],
        "bugs": bugs_for_json,
        "ideas": ideas_for_json,
    }

    # Add filter metadata and filtered stats when filters are specified
    if complexity or priority:
        filtered_stats = {
            "total_tasks": 0,
            "done": 0,
            "in_progress": 0,
            "blocked": 0,
            "pending": 0,
        }

        for p in phases_to_show:
            for m in p.milestones:
                for e in m.epics:
                    for t in e.tasks:
                        if _task_matches_filters(t, complexity, priority):
                            if unfinished and not _is_unfinished(t.status):
                                continue
                            filtered_stats["total_tasks"] += 1
                            if t.status.value == "done":
                                filtered_stats["done"] += 1
                            elif t.status.value == "in_progress":
                                filtered_stats["in_progress"] += 1
                            elif t.status.value == "blocked":
                                filtered_stats["blocked"] += 1
                            elif t.status.value == "pending":
                                filtered_stats["pending"] += 1

        output["filter"] = {}
        if complexity:
            output["filter"]["complexity"] = complexity
        if priority:
            output["filter"]["priority"] = priority
        output["filtered_stats"] = filtered_stats

    click.echo(json.dumps(output, indent=2))


def _list_text(
    tree,
    critical_path,
    complexity=None,
    priority=None,
    show_all=False,
    unfinished=False,
    show_completed_aux=False,
    include_normal=True,
    include_bugs=True,
    include_ideas=True,
    scoped_phases=None,
    scope_query=None,
    scoped_depth=None,
):
    """Output list as text."""
    console.print(
        f"\n[bold cyan]Critical Path:[/] {' → '.join(critical_path[:10])}"
        f"{'...' if len(critical_path) > 10 else ''}\n"
    )

    _show_filter_banner(complexity, priority)

    phases_to_show = (
        scoped_phases
        if scoped_phases is not None
        else tree.phases if include_normal else []
    )
    if scoped_phases is not None and not phases_to_show:
        if scope_query:
            console.print(f"No list nodes found for path query: {scope_query.raw}")
        return

    if scoped_phases is not None and scoped_depth:
        for i, p in enumerate(phases_to_show):
            is_last = i == len(phases_to_show) - 1
            lines = _render_phase(
                p,
                is_last,
                "",
                critical_path,
                unfinished,
                False,
                scoped_depth,
                1,
            )
            for line in lines:
                console.print(line)
        return

    if unfinished:
        phases_to_show = [p for p in phases_to_show if _has_unfinished_milestones(p)]

    for p in phases_to_show:
        # Calculate stats with optional filters
        if complexity or priority:
            all_tasks = []
            for m in p.milestones:
                for e in m.epics:
                    all_tasks.extend(
                        [
                            t
                            for t in e.tasks
                            if _task_matches_filters(t, complexity, priority)
                        ]
                    )

            if not all_tasks:
                continue

            done = sum(1 for t in all_tasks if t.status.value == "done")
            total = len(all_tasks)
            in_progress = sum(1 for t in all_tasks if t.status.value == "in_progress")
        else:
            stats = p.stats
            done = stats["done"]
            total = stats["total_tasks"]
            in_progress = stats["in_progress"]

        status_display = f"{done}/{total} tasks done"
        if in_progress:
            status_display += f", {in_progress} in progress"

        console.print(f"[bold]{p.name} ({p.id})[/] ({status_display})")

        # Show up to 5 milestones (or all with --all)
        milestones_to_show = []
        milestones_list = p.milestones
        if unfinished:
            milestones_list = [m for m in p.milestones if _has_unfinished_epics(m)]

        for m in milestones_list:
            if complexity or priority:
                # Check if milestone has any tasks matching active filters
                milestone_tasks = []
                for e in m.epics:
                    milestone_tasks.extend(
                        [
                            t
                            for t in e.tasks
                            if _task_matches_filters(t, complexity, priority)
                        ]
                    )

                if not milestone_tasks:
                    continue

                m_done = sum(1 for t in milestone_tasks if t.status.value == "done")
                m_total = len(milestone_tasks)
            else:
                m_stats = m.stats
                m_done = m_stats["done"]
                m_total = m_stats["total_tasks"]

            milestones_to_show.append((m, m_done, m_total))

        milestone_limit = len(milestones_to_show) if show_all else 5
        for i, (m, m_done, m_total) in enumerate(milestones_to_show[:milestone_limit]):
            is_last = (
                i == min(milestone_limit, len(milestones_to_show)) - 1
                and len(milestones_to_show) <= milestone_limit
            )
            prefix = "└──" if is_last else "├──"
            console.print(
                f"  {prefix} {m.name} ({m.id}) ({m_done}/{m_total} tasks done)"
            )

        if len(milestones_to_show) > milestone_limit:
            console.print(
                f"  └── ... and {len(milestones_to_show) - milestone_limit} more milestone{'s' if len(milestones_to_show) - milestone_limit > 1 else ''}\n"
            )
        else:
            console.print()

    # Show bugs section
    bugs_to_show = (
        [
            b
            for b in getattr(tree, "bugs", [])
            if _include_aux_item(b.status, unfinished, show_completed_aux)
        ]
        if include_bugs
        else []
    )
    if bugs_to_show:
        bugs_done = sum(1 for b in bugs_to_show if b.status == Status.DONE)
        console.print(f"[bold]Bugs[/] ({bugs_done}/{len(bugs_to_show)} done)")
        for i, b in enumerate(bugs_to_show):
            is_last = i == len(bugs_to_show) - 1
            prefix = "└──" if is_last else "├──"
            icon = _get_status_icon(b.status)
            crit_marker = "[yellow]★[/] " if b.id in critical_path else ""
            console.print(
                f"  {prefix} {icon} {crit_marker}{b.id}: {b.title} [{b.priority.value}]"
            )
        console.print()

    ideas_to_show = (
        [
            i
            for i in getattr(tree, "ideas", [])
            if _include_aux_item(i.status, unfinished, show_completed_aux)
        ]
        if include_ideas
        else []
    )
    if ideas_to_show:
        ideas_done = sum(1 for i in ideas_to_show if i.status == Status.DONE)
        console.print(f"[bold]Ideas[/] ({ideas_done}/{len(ideas_to_show)} done)")
        for i, idea in enumerate(ideas_to_show):
            is_last = i == len(ideas_to_show) - 1
            prefix = "└──" if is_last else "├──"
            icon = _get_status_icon(idea.status)
            crit_marker = "[yellow]★[/] " if idea.id in critical_path else ""
            console.print(
                f"  {prefix} {icon} {crit_marker}{idea.id}: {idea.title} [{idea.priority.value}]"
            )
        console.print()


# ============================================================================
# Tree Command
# ============================================================================


def _render_task(task, is_last, prefix, critical_path, show_details):
    """Render a task line in the tree."""
    icon = _get_status_icon(task.status)
    branch = "└── " if is_last else "├── "
    line = f"{prefix}{branch}{icon} {task.id}: {task.title}"

    if show_details:
        details = []
        if task.estimate_hours > 0:
            details.append(f"({task.estimate_hours}h)")
        if task.status:
            details.append(f"[{task.status.value}]")
        if task.claimed_by:
            details.append(f"@{task.claimed_by}")
        if task.depends_on:
            details.append(f"depends:{','.join(task.depends_on)}")
        if task.id in critical_path:
            details.append("★")

        if details:
            line += f" {' '.join(details)}"

    return line


def _render_epic(
    epic,
    is_last,
    prefix,
    critical_path,
    unfinished,
    show_details,
    max_depth,
    current_depth,
):
    """Render an epic and its tasks in the tree."""
    stats = _get_epic_stats(epic)
    branch = "└── " if is_last else "├── "
    continuation = "    " if is_last else "│   "
    lines = []

    lines.append(
        f"{prefix}{branch}{epic.name} ({stats['done']}/{stats['total']}) [{epic.status.value}]"
    )

    if current_depth >= max_depth:
        return lines

    tasks_to_show = _filter_unfinished_tasks(epic.tasks) if unfinished else epic.tasks
    new_prefix = prefix + continuation

    for i, t in enumerate(tasks_to_show):
        task_is_last = i == len(tasks_to_show) - 1
        lines.append(
            _render_task(t, task_is_last, new_prefix, critical_path, show_details)
        )

    return lines


def _render_milestone(
    milestone,
    is_last,
    prefix,
    critical_path,
    unfinished,
    show_details,
    max_depth,
    current_depth,
):
    """Render a milestone and its epics in the tree."""
    stats = _get_milestone_stats(milestone)
    branch = "└── " if is_last else "├── "
    continuation = "    " if is_last else "│   "
    lines = []

    lines.append(
        f"{prefix}{branch}{milestone.name} ({stats['done']}/{stats['total']}) [{milestone.status.value}]"
    )

    if current_depth >= max_depth:
        return lines

    epics_to_show = milestone.epics
    if unfinished:
        epics_to_show = [e for e in milestone.epics if _has_unfinished_tasks(e)]

    new_prefix = prefix + continuation

    for i, e in enumerate(epics_to_show):
        epic_is_last = i == len(epics_to_show) - 1
        lines.extend(
            _render_epic(
                e,
                epic_is_last,
                new_prefix,
                critical_path,
                unfinished,
                show_details,
                max_depth,
                current_depth + 1,
            )
        )

    return lines


def _render_phase(
    phase,
    is_last,
    prefix,
    critical_path,
    unfinished,
    show_details,
    max_depth,
    current_depth,
):
    """Render a phase and its milestones in the tree."""
    stats = _get_phase_stats(phase)
    branch = "└── " if is_last else "├── "
    continuation = "    " if is_last else "│   "
    lines = []

    lines.append(
        f"{prefix}{branch}[bold]{phase.name}[/] ({stats['done']}/{stats['total']}) [{phase.status.value}]"
    )

    if current_depth >= max_depth:
        return lines

    milestones_to_show = phase.milestones
    if unfinished:
        milestones_to_show = [m for m in phase.milestones if _has_unfinished_epics(m)]

    new_prefix = prefix + continuation

    for i, m in enumerate(milestones_to_show):
        milestone_is_last = i == len(milestones_to_show) - 1
        lines.extend(
            _render_milestone(
                m,
                milestone_is_last,
                new_prefix,
                critical_path,
                unfinished,
                show_details,
                max_depth,
                current_depth + 1,
            )
        )

    return lines


@cli.command()
@click.option("--json", "output_json", is_flag=True, help="Output as JSON")
@click.option("--unfinished", is_flag=True, help="Show only unfinished items")
@click.option(
    "--show-completed-aux",
    is_flag=True,
    help="Include completed/cancelled/rejected bugs and ideas",
)
@click.option(
    "--details",
    is_flag=True,
    help="Show task metadata (estimates, agents, dependencies)",
)
@click.option(
    "--depth",
    type=int,
    default=4,
    help="Limit tree expansion depth (1=phases, 2=milestones, 3=epics, 4=tasks)",
)
@click.argument("path_queries", nargs=-1)
def tree(output_json, unfinished, show_completed_aux, details, depth, path_queries):
    """Display full hierarchical tree of phases, milestones, epics, and tasks."""
    try:
        loader = TaskLoader()
        tree_data = loader.load("metadata", include_bugs=True, include_ideas=True)
        parsed_queries = [PathQuery.parse(query) for query in path_queries]
        is_scoped_query = len(parsed_queries) > 0
        config = load_config()

        calc = CriticalPathCalculator(tree_data, config["complexity_multipliers"])
        critical_path, next_available = calc.calculate()
        tree_data.critical_path = critical_path
        tree_data.next_available = next_available

        if parsed_queries:
            scoped_slices = [filter_tree_by_path_query(tree_data, parsed_query) for parsed_query in parsed_queries]
            phases_to_show = _merge_scoped_phases(scoped_slices)
        else:
            phases_to_show = tree_data.phases

        if output_json:
            if unfinished:
                phases_to_show = [
                    p for p in phases_to_show if _has_unfinished_milestones(p)
                ]

            output = {
                "critical_path": critical_path,
                "next_available": next_available,
                "max_depth": depth,
                "show_details": details,
                "unfinished_only": unfinished,
                "phases": [
                    {
                        "id": p.id,
                        "name": p.name,
                        "status": p.status.value,
                        "stats": _get_phase_stats(p),
                        "milestones": [
                            {
                                "id": m.id,
                                "name": m.name,
                                "status": m.status.value,
                                "stats": _get_milestone_stats(m),
                                "epics": [
                                    {
                                        "id": e.id,
                                        "name": e.name,
                                        "status": e.status.value,
                                        "stats": _get_epic_stats(e),
                                        "tasks": [
                                            {
                                                "id": t.id,
                                                "title": t.title,
                                                "status": t.status.value,
                                                "estimate_hours": t.estimate_hours,
                                                "claimed_by": t.claimed_by,
                                                "depends_on": t.depends_on,
                                                "on_critical_path": t.id
                                                in critical_path,
                                            }
                                            for t in (
                                                _filter_unfinished_tasks(e.tasks)
                                                if unfinished
                                                else e.tasks
                                            )
                                        ],
                                    }
                                    for e in m.epics
                                    if not unfinished or _has_unfinished_tasks(e)
                                ],
                            }
                            for m in p.milestones
                            if not unfinished or _has_unfinished_epics(m)
                        ],
                    }
                    for p in phases_to_show
                ],
            }
            click.echo(json.dumps(output, indent=2))
            return

        # Text output
        if unfinished:
            phases_to_show = [
                p for p in phases_to_show if _has_unfinished_milestones(p)
            ]

        bugs_to_show = []
        ideas_to_show = []
        if not is_scoped_query:
            bugs_to_show = [
                b
                for b in getattr(tree_data, "bugs", [])
                if _include_aux_item(b.status, unfinished, show_completed_aux)
            ]
            ideas_to_show = [
                i
                for i in getattr(tree_data, "ideas", [])
                if _include_aux_item(i.status, unfinished, show_completed_aux)
            ]
        has_bugs = len(bugs_to_show) > 0
        has_ideas = len(ideas_to_show) > 0
        has_aux = has_bugs or has_ideas

        for i, p in enumerate(phases_to_show):
            is_last = i == len(phases_to_show) - 1 and not has_aux
            lines = _render_phase(
                p, is_last, "", critical_path, unfinished, details, depth, 1
            )
            for line in lines:
                console.print(line)

        if parsed_queries and not phases_to_show:
            console.print("No tree nodes found for path query: " + ", ".join(path_queries))

        # Render auxiliary sections
        if has_bugs:
            bugs_done = sum(1 for b in bugs_to_show if b.status == Status.DONE)
            branch = "└──" if not has_ideas else "├──"
            console.print(f"{branch} [bold]Bugs[/] ({bugs_done}/{len(bugs_to_show)})")
            bugs_prefix = "    " if not has_ideas else "│   "
            for i, b in enumerate(bugs_to_show):
                is_last_bug = i == len(bugs_to_show) - 1 and not has_ideas
                line = _render_task(b, is_last_bug, bugs_prefix, critical_path, details)
                console.print(line)

        if has_ideas:
            ideas_done = sum(1 for i in ideas_to_show if i.status == Status.DONE)
            console.print(f"└── [bold]Ideas[/] ({ideas_done}/{len(ideas_to_show)})")
            for i, idea in enumerate(ideas_to_show):
                is_last_idea = i == len(ideas_to_show) - 1
                line = _render_task(idea, is_last_idea, "    ", critical_path, details)
                console.print(line)

    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


# ============================================================================
# Show Command
# ============================================================================


def _task_file_header(task_id: str) -> str:
    return f"{'-' * 20} [ {task_id} ] {'-' * 20}"


def _show_not_found(item_type: str, item_id: str, scope_hint: str | None = None) -> None:
    console.print(f"[red]Error:[/] {item_type} not found: {item_id}")
    if scope_hint:
        console.print(
            f"[yellow]Tip:[/] Use 'backlog tree {scope_hint}' to verify available IDs."
        )
    else:
        console.print("[yellow]Tip:[/] Use 'backlog tree' to list available IDs.")
    raise click.Abort()


def _find_cat_fixed_task(fixed_id: str):
    index_path = Path(get_data_dir_name()) / "fixes" / "index.yaml"
    if not index_path.exists():
        return None

    index = yaml.safe_load(index_path.read_text()) or {}
    for entry in index.get("fixes", []):
        if not isinstance(entry, dict):
            continue
        if str(entry.get("id", "")) != fixed_id:
            continue
        filename = str(entry.get("file", ""))
        if not filename:
            return None
        return SimpleNamespace(id=fixed_id, file=str(Path("fixes") / filename))
    return None


def _entry_matches_id(entry, *ids: str | None) -> bool:
    entry_id = str(entry.get("id", ""))
    return any(candidate and entry_id == candidate for candidate in ids)


def _find_index_entry(index_path: Path, key: str, *ids: str | None):
    if not index_path.exists():
        return None
    index = yaml.safe_load(index_path.read_text()) or {}
    for entry in index.get(key, []):
        if isinstance(entry, dict) and _entry_matches_id(entry, *ids):
            return entry
    return None


def _find_cat_normal_task(task_id: str):
    task_path = TaskPath.parse(task_id)
    if not task_path.is_task:
        parent_path = task_path.parent()
        _show_not_found("Task", task_id, parent_path.full_id if parent_path else None)

    data_dir = Path(get_data_dir_name())
    phase = _find_index_entry(data_dir / "index.yaml", "phases", task_path.phase)
    if not phase:
        return None
    phase_dir = data_dir / str(phase.get("path", ""))

    milestone = _find_index_entry(
        phase_dir / "index.yaml",
        "milestones",
        task_path.milestone_id,
        task_path.milestone,
    )
    if not milestone:
        return None
    milestone_dir = phase_dir / str(milestone.get("path", ""))

    epic = _find_index_entry(
        milestone_dir / "index.yaml",
        "epics",
        task_path.epic_id,
        task_path.epic,
    )
    if not epic:
        return None
    epic_dir = milestone_dir / str(epic.get("path", ""))

    task = _find_index_entry(
        epic_dir / "index.yaml",
        "tasks",
        task_path.task_id,
        task_path.task,
    )
    if not task or not task.get("file"):
        return None

    relative_file = epic_dir.relative_to(data_dir) / str(task["file"])
    return SimpleNamespace(id=task_id, file=str(relative_file))


def _find_cat_task(loader: TaskLoader, tree, task_id: str):
    if is_bug_id(task_id) or is_idea_id(task_id):
        if tree is None:
            tree = loader.load("metadata")
        task = builtin_next(
            (
                item
                for item in [
                    *getattr(tree, "bugs", []),
                    *getattr(tree, "ideas", []),
                ]
                if item.id == task_id
            ),
            None,
        )
        if not task:
            _show_not_found("Task", task_id, None)
        return task, tree

    if is_fixed_id(task_id):
        task = _find_cat_fixed_task(task_id)
        if not task:
            _show_not_found("Task", task_id, None)
        return task, tree

    task = _find_cat_normal_task(task_id)
    if not task:
        task_path = TaskPath.parse(task_id)
        parent_path = task_path.parent()
        parent = parent_path.full_id if parent_path else None
        _show_not_found("Task", task_id, parent)
    return task, tree


def _separator_padding(previous_content: bytes) -> bytes:
    if previous_content.endswith(b"\n\n"):
        return b""
    if previous_content.endswith(b"\n"):
        return b"\n"
    return b"\n\n"


def _write_task_file_contents(tasks) -> None:
    stdout = click.get_binary_stream("stdout")
    previous_content = b""
    for index, task in enumerate(tasks):
        task_file = task_file_path(task)
        if not task_file.exists():
            console.print(f"[yellow]Warning:[/] Task file missing for {task.id}: {task_file}")
            raise click.Abort()
        try:
            content = task_file.read_bytes()
        except OSError as exc:
            console.print(f"[yellow]Warning:[/] Task file for {task.id} is not readable: {exc}")
            raise click.Abort()
        if index > 0:
            stdout.write(_separator_padding(previous_content))
            stdout.write(f"{_task_file_header(task.id)}\n".encode())
        stdout.write(content)
        previous_content = content


@cli.command(name="cat")
@click.argument("task_ids", nargs=-1, required=True)
def cat_command(task_ids):
    """Print complete raw .todo task file contents.

    TASK_IDS can be one or more task IDs like P1.M01.E1.T001.

    Examples:
        backlog cat P1.M2.E1.T001
        backlog cat P1.M2.E1.T001 P1.M2.E1.T002
    """
    try:
        loader = TaskLoader()
        tree = None
        tasks = []
        for task_id in task_ids:
            task, tree = _find_cat_task(loader, tree, task_id)
            tasks.append(task)
        _write_task_file_contents(tasks)
    except ValueError as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


@cli.command()
@click.option("--all", "show_all", is_flag=True, help="Show the full .todo file (frontmatter and body).")
@click.option("--long", "show_long", is_flag=True, help="Show the entire task file body.")
@click.argument("path_ids", nargs=-1)
def show(path_ids, show_long, show_all):
    """Show detailed information for phases, milestones, epics, or tasks.

    PATH_IDS can be one or more IDs like P1, P1.M01, P1.M01.E1, P1.M01.E1.T001.
    If no PATH_ID is provided, shows the current working task.

    Examples:
        backlog show P1.M2
        backlog show P1.M2 P4.M1.E2
        backlog show              # Shows current working task
    """
    try:
        # Use current task from context if no path_ids provided
        if not path_ids:
            current = get_current_task_id()
            if not current:
                console.print(
                    "[dim]No task specified and no current working task set.[/]"
                )
                console.print(
                    "[dim]Use 'backlog work <task-id>' to set a working task.[/]"
                )
                return
            path_ids = (current,)

        loader = TaskLoader()
        tree = None

        for i, path_id in enumerate(path_ids):
            if i > 0:
                console.print("\n" + "═" * 60 + "\n")

            # Check for auxiliary IDs before TaskPath.parse.
            if is_bug_id(path_id) or is_idea_id(path_id) or is_fixed_id(path_id):
                if tree is None:
                    tree = loader.load("metadata")
                if is_fixed_id(path_id):
                    fixed_task = loader.find_fixed_task(path_id)
                    if not fixed_task:
                        console.print(f"[red]Error:[/] Task not found: {path_id}")
                        raise click.Abort()
                    _show_fixed_task(fixed_task)
                else:
                    aux_task = builtin_next(
                        (
                            t
                            for t in [
                                *getattr(tree, "bugs", []),
                                *getattr(tree, "ideas", []),
                            ]
                            if t.id == path_id
                        ),
                        None,
                    )
                    if not aux_task:
                        console.print(f"[red]Error:[/] Task not found: {path_id}")
                        raise click.Abort()
                    console.print(
                        f"{aux_task.id}: {aux_task.title}\nstatus={aux_task.status.value} "
                        f"estimate={aux_task.estimate_hours}"
                    )
                    if is_idea_id(path_id) and aux_task.status == Status.PENDING:
                        _show_idea_instructions(aux_task)
                continue

            task_path = TaskPath.parse(path_id)
            if task_path.is_task:
                if tree is None:
                    tree = loader.load("metadata")
                scope_tree = tree
            else:
                scope_tree = loader.load_scope(
                    task_path,
                    mode="metadata",
                    include_bugs=False,
                    include_ideas=False,
                )
            parent_path = task_path.parent()
            parent = parent_path.full_id if parent_path else None

            if task_path.is_phase:
                phase = scope_tree.find_phase(path_id)
                if not phase:
                    _show_not_found("Phase", path_id, None)
                _show_phase(scope_tree, path_id)
            elif task_path.is_milestone:
                milestone = scope_tree.find_milestone(path_id)
                display_tree = scope_tree
                if not milestone:
                    if tree is None:
                        tree = loader.load("metadata")
                    display_tree = tree
                    milestone = display_tree.find_milestone(path_id)
                if not milestone:
                    _show_not_found("Milestone", path_id, parent)
                _show_milestone(display_tree, milestone.id)
            elif task_path.is_epic:
                epic = scope_tree.find_epic(path_id)
                display_tree = scope_tree
                if not epic:
                    if tree is None:
                        tree = loader.load("metadata")
                    display_tree = tree
                    epic = display_tree.find_epic(path_id)
                if not epic:
                    _show_not_found("Epic", path_id, parent)
                _show_epic(display_tree, epic.id)
            else:
                task = scope_tree.find_task(path_id)
                if not task:
                    _show_not_found("Task", path_id, parent)
                _show_task(scope_tree, path_id, show_long, show_all)

    except ValueError as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


def _sum_task_estimate_hours(tasks):
    return sum(task.estimate_hours for task in tasks)


def _dependency_icon(task_status: Status) -> str:
    if task_status == Status.DONE:
        return "[green]✓[/]"
    return "[red]✗[/]"


def _show_dependency_details(tree, task):
    """Display explicit or implicit task dependencies for show output."""
    rendered = False

    if task.depends_on:
        console.print("[bold]Explicit dependencies:[/]")
        for dep_id in task.depends_on:
            dep = tree.find_task(dep_id)
            if dep:
                console.print(
                    f"  {_dependency_icon(dep.status)} {dep.id} - {dep.title} ({dep.status.value})"
                )
            else:
                console.print(f"  [red]?[/] {dep_id} (not found)")
        rendered = True
    elif task.epic_id:
        epic = tree.find_epic(task.epic_id)
        if epic:
            task_index = None
            for idx, epic_task in enumerate(epic.tasks):
                if epic_task.id == task.id:
                    task_index = idx
                    break

            if task_index and task_index > 0:
                prev_task = epic.tasks[task_index - 1]
                console.print("[bold]Implicit dependency (previous in epic):[/]")
                console.print(
                    f"  {_dependency_icon(prev_task.status)} {prev_task.id} - {prev_task.title} ({prev_task.status.value})"
                )
                rendered = True

    if rendered:
        console.print("[dim]Legend: ✓ done | ✗ not done | ? not found[/]")


def _show_phase(tree, phase_id):
    """Display phase details."""
    phase = tree.find_phase(phase_id)
    if not phase:
        console.print(f"[red]Error:[/] Phase not found: {phase_id}")
        raise click.Abort()

    stats = phase.stats
    console.print(f"\n[bold cyan]Phase:[/] {phase.id} - {phase.name}\n")
    console.print(f"[bold]Path:[/] {format_phase_path(phase)}")
    console.print(f"[bold]Status:[/] {phase.status.value}")
    console.print(f"[bold]Weeks:[/] {phase.weeks}")
    console.print(f"[bold]Estimate:[/] {phase.estimate_hours} hours")
    console.print(f"[bold]Priority:[/] {phase.priority.value}")
    console.print(
        f"[bold]Total Duration:[/] {_sum_task_estimate_hours(task for milestone in phase.milestones for epic in milestone.epics for task in epic.tasks):.2f} hours"
    )
    console.print(
        f"\n[bold]Progress:[/] {stats['done']}/{stats['total_tasks']} tasks done"
    )

    console.print(f"\n[bold]Milestones ({len(phase.milestones)}):[/]")
    for m in phase.milestones:
        m_stats = m.stats
        status_icon = "[green]✓[/]" if m.is_complete else "○"
        console.print(f"\n  {status_icon} [bold]{m.id}[/] - {m.name}")
        console.print(f"      {m_stats['done']}/{m_stats['total_tasks']} tasks")
        console.print(f"      [dim]{format_milestone_path(tree, m)}[/]")
    console.print()


def _show_milestone(tree, milestone_id):
    """Display milestone details."""
    milestone = tree.find_milestone(milestone_id)
    if not milestone:
        console.print(f"[red]Error:[/] Milestone not found: {milestone_id}")
        raise click.Abort()

    stats = milestone.stats
    console.print(f"\n[bold cyan]Milestone:[/] {milestone.id} - {milestone.name}\n")
    console.print(f"[bold]Path:[/] {format_milestone_path(tree, milestone)}")
    console.print(f"[bold]Status:[/] {milestone.status.value}")
    console.print(f"[bold]Estimate:[/] {milestone.estimate_hours} hours")
    console.print(
        f"[bold]Total Duration:[/] {_sum_task_estimate_hours(task for epic in milestone.epics for task in epic.tasks):.2f} hours"
    )
    console.print(
        f"\n[bold]Progress:[/] {stats['done']}/{stats['total_tasks']} tasks done"
    )

    console.print(f"\n[bold]Epics ({len(milestone.epics)}):[/]")
    for e in milestone.epics:
        e_stats = e.stats
        status_icon = "[green]✓[/]" if e.is_complete else "○"
        console.print(f"\n  {status_icon} [bold]{e.id}[/] - {e.name}")
        console.print(f"      {e_stats['done']}/{e_stats['total']} tasks")
        console.print(f"      [dim]{format_epic_path(tree, e)}[/]")
    console.print()


def _show_epic(tree, epic_id):
    """Display epic details."""
    epic = tree.find_epic(epic_id)
    if not epic:
        console.print(f"[red]Error:[/] Epic not found: {epic_id}")
        raise click.Abort()

    stats = epic.stats
    console.print(f"\n[bold cyan]Epic:[/] {epic.id} - {epic.name}\n")
    console.print(f"[bold]Path:[/] {format_epic_path(tree, epic)}")
    console.print(f"[bold]Status:[/] {epic.status.value}")
    console.print(f"[bold]Estimate:[/] {epic.estimate_hours} hours")
    console.print(
        f"[bold]Total Duration:[/] {_sum_task_estimate_hours(epic.tasks):.2f} hours"
    )
    console.print(f"\n[bold]Progress:[/] {stats['done']}/{stats['total']} tasks done")

    console.print(f"\n[bold]Tasks ({len(epic.tasks)}):[/]")
    for t in epic.tasks:
        status_icons = {
            "done": "[green]✓[/]",
            "in_progress": "[yellow]→[/]",
            "pending": "[ ]",
            "blocked": "[red]✗[/]",
        }
        icon = status_icons.get(t.status.value, "?")
        console.print(f"\n  {icon} [bold]{t.id}[/]: {t.title}")
        console.print(
            f"      {t.estimate_hours}h, {t.complexity.value}, {t.priority.value}"
        )
        console.print(f"      [dim].tasks/{t.file}[/]")
    console.print()


def _show_task(tree, task_id, show_long=False, show_all=False):
    """Display task details."""
    task = tree.find_task(task_id)
    if not task:
        console.print(f"[red]Error:[/] Task not found: {task_id}")
        raise click.Abort()

    console.print(f"\n[bold]Task:[/] {task.id}")
    console.print(f"[bold]Title:[/] {task.title}")
    console.print(f"[bold]Status:[/] {task.status.value}")
    console.print(f"[bold]Estimate:[/] {task.estimate_hours} hours")
    console.print(f"[bold]Complexity:[/] {task.complexity.value}")
    console.print(f"[bold]Priority:[/] {task.priority.value}")

    if task.claimed_by:
        console.print(f"[bold]Claimed by:[/] {task.claimed_by}")
        if task.claimed_at:
            age = (utc_now() - to_utc(task.claimed_at)).total_seconds() / 3600
            console.print(
                f"[bold]Claimed at:[/] {task.claimed_at.isoformat()} ({age:.1f}h ago)"
            )

    _show_dependency_details(tree, task)

    console.print(f"\n[bold]File:[/] .tasks/{task.file}")

    task_file = task_file_path(task)
    if _warn_task_file_issues(task):
        return

    content = task_file.read_text()
    console.print(
        f"[bold]File stats:[/] {task_file.stat().st_size} bytes, {len(content.splitlines())} lines\n"
    )
    parts = content.split("---\n")
    body = ""
    if len(parts) >= 3:
        body = parts[2]
    else:
        body = content
    lines = body.strip().splitlines()
    if show_all:
        console.print("[bold]Task file:[/]")
        for line in content.splitlines():
            console.print(f"  {line}")
        return
    if lines:
        console.print("[bold]Body:[/]")
    if show_long:
        for line in lines:
            console.print(f"  {line}")
    else:
        preview_count = min(SHOW_TASK_PREVIEW_LINES, len(lines))
        for line in lines[:preview_count]:
            console.print(f"  {line}")
        if len(lines) > preview_count:
            console.print(f"[dim]  ... ({len(lines) - preview_count} more lines)[/]")
            console.print(f"[dim]To view the full file, run: cat {task_file}[/]")


def _show_idea_instructions(idea):
    """Display planning instructions for a pending idea."""
    data_dir = get_data_dir_name()
    console.print("\n[bold]Instructions:[/]")
    console.print(f"  1. Read the idea file at {data_dir}/{idea.file}")
    console.print("  2. Understand the idea in the context of the codebase")
    console.print("  3. Plan out a solution design")
    console.print(
        "  4. Create phases, milestones, epics, and tasks as appropriate using:"
    )
    console.print("     - `tasks add-phase` for new phases")
    console.print("     - `tasks add-milestone` for milestones within phases")
    console.print("     - `tasks add-epic` for epics within milestones")
    console.print("     - `tasks add` for tasks within epics")
    console.print(
        '  5. Update this idea\'s "Created Work Items" section with the new IDs'
    )
    console.print("  6. Mark this idea as done when planning is complete")


def _print_next_commands(*commands):
    """Print a short list of suggested next commands."""
    filtered = [command.strip() for command in commands if command and command.strip()]
    if not filtered:
        return
    console.print("[bold]Next:[/]")
    for command in filtered:
        console.print(f"  - {command}")


def _show_fixed_task(task):
    """Display fixed task summary."""
    console.print(f"{task.id}: {task.title}\nstatus={task.status.value} estimate={task.estimate_hours}")


# ============================================================================
# Ls Command
# ============================================================================


def _match_ls_scoped_item(items, scope_id):
    for item in items:
        if item.id == scope_id:
            return item

    scoped = scope_id if scope_id.startswith(".") else f".{scope_id}"
    matches = [item for item in items if item.id.endswith(scoped)]
    if len(matches) >= 1:
        return matches[0]
    return None


def _validate_ls_scope(tree, scope):
    if is_bug_id(scope) or is_idea_id(scope):
        raise ValueError(f"ls does not support bug/idea IDs. Use: backlog show {scope}")
    parsed = TaskPath.parse(scope)
    if parsed.is_phase and not tree.find_phase(scope):
        raise ValueError(f"Phase not found: {scope}")
    if parsed.is_milestone and not _match_ls_scoped_item(
        [m for p in tree.phases for m in p.milestones], scope
    ):
        raise ValueError(f"Milestone not found: {scope}")
    if parsed.is_epic and not _match_ls_scoped_item(
        [e for p in tree.phases for m in p.milestones for e in m.epics], scope
    ):
        raise ValueError(f"Epic not found: {scope}")
    if parsed.is_task and not tree.find_task(scope):
        raise ValueError(f"Task not found: {scope}")


def _render_ls_scope(tree, scope):
    parsed = TaskPath.parse(scope)
    if parsed.is_phase:
        phase = tree.find_phase(scope)
        if not phase.milestones:
            console.print(f"Phase {scope} has no milestones.")
            return

        for milestone in phase.milestones:
            stats = milestone.stats
            console.print(
                f"{milestone.id}: {milestone.name} "
                f"[{milestone.status.value}] "
                f"{stats['done']}/{stats['total_tasks']} tasks done "
                f"(in_progress={stats['in_progress']}, blocked={stats['blocked']})"
            )
        return

    if parsed.is_milestone:
        milestone = _match_ls_scoped_item(
            [m for p in tree.phases for m in p.milestones], scope
        )
        if not milestone.epics:
            console.print(f"Milestone {scope} has no epics.")
            return

        for epic in milestone.epics:
            stats = epic.stats
            console.print(
                f"{epic.id}: {epic.name} "
                f"[{epic.status.value}] "
                f"{stats['done']}/{stats['total']} tasks done "
                f"(in_progress={stats['in_progress']}, blocked={stats['blocked']})"
            )
        return

    if parsed.is_task:
        task = tree.find_task(scope)
        _show_ls_task_summary(task)
        return

    epic = _match_ls_scoped_item(
        [e for p in tree.phases for m in p.milestones for e in m.epics],
        scope,
    )
    if not epic.tasks:
        console.print(f"Epic {scope} has no tasks.")
        return

    for task in epic.tasks:
        console.print(
            f"{task.id}: {task.title} "
            f"[{task.status.value}] "
            f"{task.estimate_hours}h",
            markup=False,
        )


@cli.command()
@click.argument("scopes", nargs=-1)
def ls(scopes):
    """List tasks by hierarchy scope.

    Without SCOPE, lists all phases.
    - P1: list milestones under phase
    - P1.M1: list epics under milestone
    - P1.M1.E1: list tasks under epic
    - P1.M1.E1.T001: show a compact task summary
    """
    try:
        loader = TaskLoader()
        include_aux_items = not bool(scopes)
        tree = loader.load(
            "metadata", include_bugs=include_aux_items, include_ideas=include_aux_items
        )

        if not scopes:
            if not tree.phases:
                console.print("No phases found.")
            else:
                for phase in tree.phases:
                    stats = phase.stats
                    console.print(
                        f"{phase.id}: {phase.name} "
                        f"[{phase.status.value}] "
                        f"{stats['done']}/{stats['total_tasks']} tasks done "
                        f"(in_progress={stats['in_progress']}, blocked={stats['blocked']})"
                    )

            bugs_done = sum(1 for bug in tree.bugs if bug.status == Status.DONE)
            ideas_done = sum(1 for idea in tree.ideas if idea.status == Status.DONE)
            fixes_done, fixes_total = _summarize_fixed_tasks(loader)
            console.print(f"Bugs ({bugs_done}/{len(tree.bugs)})")
            console.print(f"Ideas ({ideas_done}/{len(tree.ideas)})")
            console.print(f"Fixes ({fixes_done}/{fixes_total})")
            return

        for scope in scopes:
            _validate_ls_scope(tree, scope)

        for i, scope in enumerate(scopes):
            if i > 0:
                console.print("")
            _render_ls_scope(tree, scope)

    except ValueError as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


def _show_ls_task_summary(task):
    task_file = task_file_path(task)
    if not task_file.exists():
        console.print(f"[yellow]Task file missing for {task.id}[/]")
        console.print(f"Task: {task.id} - {task.title}")
        console.print("[yellow]Cannot load frontmatter for missing task file.[/]")
        console.print(f"Run 'backlog show {task.id}' for full details.")
        return

    with open(task_file) as f:
        content = f.read()

    frontmatter = {}
    body = content
    try:
        parts = content.split("---\n", 2)
        if len(parts) >= 3:
            frontmatter = yaml.safe_load(parts[1]) or {}
            body = parts[2]
    except Exception:
        pass

    console.print(f"Task: {task.id} - {task.title}")
    console.print("Frontmatter:")
    if frontmatter:
        console.print(yaml.dump(frontmatter, sort_keys=False).strip())
    else:
        console.print("  (unavailable)")
    console.print(f"Body length: {len(body)}")
    console.print(f"Run 'backlog show {task.id}' for full details.")


def _summarize_fixed_tasks(loader):
    """Summarize fixed tasks as a done/total count."""
    fixes_index_path = loader.tasks_dir / "fixes" / "index.yaml"
    if not fixes_index_path.exists():
        return (0, 0)

    try:
        raw_index = yaml.safe_load(fixes_index_path.read_text()) or {}
        raw_fixes = raw_index.get("fixes") if isinstance(raw_index, dict) else None
        if not isinstance(raw_fixes, type([])):
            return (0, 0)
    except Exception:
        return (0, 0)

    done = 0
    total = 0
    for entry in raw_fixes:
        if not isinstance(entry, dict):
            continue
        relative_file = str(entry.get("file", "")).strip()
        if not relative_file:
            continue

        fix_path = loader.tasks_dir / "fixes" / relative_file
        if not fix_path.exists():
            continue

        try:
            fix_content = fix_path.read_text()
            parts = fix_content.split("---\n", 2)
            if len(parts) < 3:
                continue
            frontmatter = yaml.safe_load(parts[1]) or {}
        except Exception:
            continue
        if not isinstance(frontmatter, dict):
            continue

        total += 1
        status = (
            str(frontmatter.get("status", ""))
            .strip()
            .lower()
            .replace("-", "_")
            .replace(" ", "_")
        )
        if status in {"done", "complete", "completed"}:
            done += 1

    return (done, total)


# ============================================================================
# Next Command
# ============================================================================


@cli.command()
@click.option("--json", "output_json", is_flag=True, help="Output as JSON")
def next(output_json):
    """Get next available task on critical path."""
    try:
        loader = TaskLoader()
        tree = loader.load("metadata")
        config = load_config()

        calc = CriticalPathCalculator(tree, config["complexity_multipliers"])
        critical_path, next_available = calc.calculate()

        if not next_available:
            console.print("[yellow]No available tasks found.[/]\n")
            _show_blocking_tasks(tree)
            return

        task = tree.find_task(next_available)

        if output_json:
            output = {
                "id": task.id,
                "title": task.title,
                "file": task.file,
                "file_exists": not is_task_file_missing(task),
                "estimate_hours": task.estimate_hours,
                "complexity": task.complexity.value,
            }
            click.echo(json.dumps(output, indent=2))
        else:
            console.print("\n[bold green]Next task on critical path:[/]\n")
            console.print(f"  [bold]ID:[/] {task.id}")
            console.print(f"  [bold]Title:[/] {task.title}")
            console.print(f"  [bold]Estimate:[/] {task.estimate_hours} hours")
            console.print(f"  [bold]File:[/] .tasks/{task.file}\n")
            _warn_missing_task_file(task)
            console.print(
                f"[dim]To claim:[/] 'backlog grab' or 'backlog claim {task.id}'\n"
            )

    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


@cli.command()
@click.option("--json", "output_json", is_flag=True, help="Output as JSON")
def preview(output_json):
    """Preview top available work and grab batching details."""
    try:
        loader = TaskLoader()
        tree = loader.load("metadata")
        config = load_config()

        calc = CriticalPathCalculator(tree, config["complexity_multipliers"])
        critical_path, next_available = calc.calculate()

        available = calc.find_all_available()
        if not available:
            console.print("[yellow]No available tasks found.[/]\n")
            _show_blocking_tasks(tree)
            return

        prioritized = calc.prioritize_task_ids(available, critical_path)

        normal_preview = []
        bug_preview = []
        idea_preview = []

        for task_id in prioritized:
            task = tree.find_task(task_id)
            if not task:
                continue

            if task.id.startswith("B"):
                if len(bug_preview) < PREVIEW_AUX_LIMIT:
                    bug_preview.append(
                        _preview_task_payload(task, critical_path, calc, tree, output_json=output_json)
                    )
            elif task.id.startswith("I"):
                if len(idea_preview) < PREVIEW_AUX_LIMIT:
                    idea_preview.append(
                        _preview_task_payload(task, critical_path, calc, tree, output_json=output_json)
                    )
            else:
                if len(normal_preview) < PREVIEW_DISPLAY_LIMIT:
                    normal_preview.append(
                        _preview_task_payload(task, critical_path, calc, tree, output_json=output_json)
                    )

            if (
                len(normal_preview) >= PREVIEW_DISPLAY_LIMIT
                and len(bug_preview) >= PREVIEW_AUX_LIMIT
                and len(idea_preview) >= PREVIEW_AUX_LIMIT
            ):
                break

        if output_json:
            payload = {
                "critical_path": critical_path,
                "next_available": next_available,
                "normal": normal_preview,
                "bugs": bug_preview,
                "ideas": idea_preview,
            }
            click.echo(json.dumps(payload, indent=2))
            return

        console.print("\n[bold green]Preview available work:[/]\n")

        if normal_preview:
            console.print("[bold cyan]Normal Tasks[/]")
            for row in normal_preview:
                task = tree.find_task(row["id"])
                if not task:
                    continue
                crit = "[yellow]★[/] " if row["on_critical_path"] else "  "
                console.print(f"  {crit}[bold]{task.id}[/]: {task.title}")
                console.print(
                    f"    File: {row['path']} | "
                    f"Estimate: {task.estimate_hours}h | {task.priority.value} / {task.complexity.value}"
                )
                grab_additional = row["grab_additional"]
                if grab_additional:
                    console.print(
                        "    [dim]If you run `backlog grab`, you would also get: "
                        f"{', '.join(grab_additional)}[/]"
                    )
                else:
                    console.print(
                        "    [dim]If you run `backlog grab`, you get this task only.[/]"
                    )

        if bug_preview:
            console.print("\n[bold magenta]Bugs[/]")
            for row in bug_preview:
                task = tree.find_task(row["id"])
                if not task:
                    continue
                crit = "[yellow]★[/] " if row["on_critical_path"] else "  "
                console.print(f"  {crit}[bold]{task.id}[/]: {task.title}")
                console.print(
                    f"    File: {row['path']} | "
                    f"Estimate: {task.estimate_hours}h | {task.priority.value} / {task.complexity.value}"
                )
                grab_additional = row["grab_additional"]
                if grab_additional:
                    console.print(
                        "    [dim]If you run `backlog grab`, you would also get: "
                        f"{', '.join(grab_additional)}[/]"
                    )
                else:
                    console.print(
                        "    [dim]If you run `backlog grab`, you get this task only.[/]"
                    )

        if idea_preview:
            console.print("\n[bold blue]Ideas[/]")
            for row in idea_preview:
                task = tree.find_task(row["id"])
                if not task:
                    continue
                crit = "[yellow]★[/] " if row["on_critical_path"] else "  "
                console.print(f"  {crit}[bold]{task.id}[/]: {task.title}")
                console.print(
                    f"    File: {row['path']} | "
                    f"Estimate: {task.estimate_hours}h | {task.priority.value} / {task.complexity.value}"
                )
                grab_additional = row["grab_additional"]
                if grab_additional:
                    console.print(
                        "    [dim]If you run `backlog grab`, you would also get: "
                        f"{', '.join(grab_additional)}[/]"
                    )
                else:
                    console.print(
                        "    [dim]If you run `backlog grab`, you get this task only.[/]"
                    )

        console.print("\n[dim]★ = On critical path[/]\n")

    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


def _show_blocking_tasks(tree):
    """Show tasks that may be blocking progress."""
    in_progress = [t for t in get_all_tasks(tree) if t.status == Status.IN_PROGRESS]

    if in_progress:
        console.print(f"There are {len(in_progress)} task(s) in progress:\n")
        for t in in_progress[:5]:
            age_str = ""
            if t.claimed_at:
                now = utc_now()
                claimed = to_utc(t.claimed_at)
                age_minutes = (now - claimed).total_seconds() / 60
                age_str = (
                    f" ({int(age_minutes)}m ago)"
                    if age_minutes < 60
                    else f" ({age_minutes / 60:.1f}h ago)"
                )
            console.print(f"  [yellow]{t.id}[/] - {t.claimed_by or 'unknown'}{age_str}")


# ============================================================================
# Claim Command
# ============================================================================


@cli.command()
@click.argument("task_ids", nargs=-1, required=False)
@click.option("--agent", help="Agent session ID (uses config default if not set)")
@click.option("--force", is_flag=True, help="Override existing claim")
@click.option("--no-content", is_flag=True, help="Suppress .todo file contents")
def claim(task_ids, agent, force, no_content):
    """Claim a task and mark as in-progress."""
    try:
        if not task_ids:
            console.print("[red]Error:[/] claim requires at least one TASK_ID")
            raise click.Abort()

        # Use config default if agent not specified
        if not agent:
            agent = get_default_agent()

        metadata: tuple[str, str] | None = None

        def _run() -> tuple[str, str] | None:
            nonlocal metadata
            loader = TaskLoader()
            tree = loader.load("metadata")
            show_details = len(task_ids) == 1
            for task_id in task_ids:
                task = tree.find_task(task_id)

                if not task:
                    console.print(
                        "[yellow]Warning:[/] claim only works with task IDs."
                    )
                    console.print(
                        f"[yellow]Showing `backlog show {task_id}` for context.[/]"
                    )
                    show((task_id,))
                    continue
                if _warn_task_file_issues(task):
                    console.print(
                        f"[red]Error:[/] Cannot claim {task.id} because the task file is missing."
                    )
                    raise click.Abort()

                claim_task(task, agent, force)
                loader.save_task(task)
                if metadata is None:
                    metadata = (task.id, task.title)

                console.print(f"\n[green]✓ Claimed:[/] {task.id} - {task.title}")

                if not show_details:
                    continue

                console.print(f"  Agent:      {agent}")
                console.print(f"  Claimed at: {task.claimed_at.isoformat()}")
                console.print(f"  Estimate:   {task.estimate_hours} hours\n")

                console.print(f"[bold]File:[/] .tasks/{task.file}\n")

                if not no_content:
                    task_file = task_file_path(task)
                    if task_file.exists():
                        with open(task_file) as f:
                            content = f.read()
                        console.print("─" * 50)
                        console.print(content)
                        console.print("─" * 50 + "\n")
                    else:
                        _warn_task_file_issues(task)

                console.print(f"[dim]Mark done:[/] 'backlog done {task.id}'\n")
            return metadata

        run_with_auto_commit(
            "claim",
            _run,
            warn=lambda msg: console.print(f"[yellow]Warning:[/] {msg}"),
        )

    except StatusError as e:
        console.print(json.dumps(e.to_dict(), indent=2))
        raise click.Abort()


@cli.command(name="edit")
@click.argument("task_ids", nargs=-1, required=False)
def edit(task_ids):
    """Open a task todo file in your editor."""
    def _run() -> tuple[str, str] | None:
        if len(task_ids) != 1:
            console.print("[red]Error:[/] edit requires exactly one TASK_ID")
            raise click.Abort()

        task_id = task_ids[0]
        if not is_task_id(task_id) and not is_bug_id(task_id) and not is_idea_id(task_id) and not is_fixed_id(task_id):
            console.print("[red]Error:[/] malformed task id")
            raise click.Abort()

        editor = os.environ.get("VISUAL", "").strip() or os.environ.get("EDITOR", "").strip()
        if not editor:
            console.print("[red]Error:[/] No editor configured. Set EDITOR or VISUAL.")
            raise click.Abort()

        loader = TaskLoader()
        tree = loader.load("metadata")
        task = tree.find_task(task_id)
        if not task:
            console.print("[yellow]Warning:[/] edit only works with task IDs.")
            console.print(f"[yellow]Showing `backlog show {task_id}` for context.[/]")
            show((task_id,))
            return None

        if _warn_missing_task_file(task):
            console.print(f"[red]Error:[/] Cannot edit {task.id} because the task file is missing.")
            raise click.Abort()

        task_file = task_file_path(task)
        editor_parts = editor.split()
        if not editor_parts:
            console.print("[red]Error:[/] No editor configured. Set EDITOR or VISUAL.")
            raise click.Abort()

        command = editor_parts[0]
        args = editor_parts[1:] + [str(task_file)]

        try:
            subprocess.run([command] + args, check=True)
        except (subprocess.CalledProcessError, FileNotFoundError) as e:
            console.print(f"[red]Error:[/] {str(e)}")
            raise click.Abort()

        return task.id, task.title

    run_with_auto_commit(
        "edit",
        _run,
        warn=lambda msg: console.print(f"[yellow]Warning:[/] {msg}"),
    )


# ============================================================================
# Done Command (Enhanced with unblocked tasks)
# ============================================================================


@cli.command()
@click.argument("task_ids", nargs=-1, required=False)
@click.option("--verify", is_flag=True, help="Confirm epic/milestone review")
@click.option("--force", is_flag=True, help="Mark task done even if not in progress")
def done(task_ids, verify, force):
    """Mark task as complete.

    Shows newly unblocked tasks after completion.
    If TASK_ID is not provided, uses the current working task.
    """
    try:
        # Use current task from context if not provided
        if not task_ids:
            task_id = get_current_task_id()
            if not task_id:
                console.print(
                    "[red]Error:[/] No task ID provided and no current working task set."
                )
                console.print(
                    "[dim]Use 'backlog work <task-id>' to set a working task.[/]"
                )
                raise click.Abort()
            task_ids = (task_id,)

        metadata: tuple[str, str] | None = None

        def _run() -> tuple[str, str] | None:
            nonlocal metadata
            loader = TaskLoader()
            tree = loader.load("metadata")
            config = load_config()

            for task_id in task_ids:
                task = tree.find_task(task_id)

                if not task:
                    console.print(f"[red]Error:[/] Task not found: {task_id}")
                    raise click.Abort()

                if task.status == Status.DONE:
                    console.print(f"[yellow]⚠ Already done:[/] {task.id} - {task.title}")
                    continue

                # Calculate duration
                duration = None
                if task.started_at:
                    started = to_utc(task.started_at)
                    duration = (utc_now() - started).total_seconds() / 60
                    task.duration_minutes = duration

                complete_task(task, force=force)
                loader.save_task(task)
                if metadata is None:
                    metadata = (task.id, task.title)

                console.print(f"\n[green]✓ Completed:[/] {task.id} - {task.title}\n")
                if duration:
                    console.print(f"  Duration: {int(duration)} minutes\n")

                # Show unblocked tasks
                calc = CriticalPathCalculator(tree, config["complexity_multipliers"])
                critical_path, _ = calc.calculate()
                unblocked = find_newly_unblocked(tree, calc, task_id)

                if unblocked:
                    console.print(f"[cyan]Unblocked {len(unblocked)} task(s):[/]")
                    for t in unblocked:
                        crit = " [yellow]★[/]" if t.id in critical_path else ""
                        console.print(f"  → {t.id}: {t.title}{crit}")

                    # Suggest next task
                    on_crit = [t for t in unblocked if t.id in critical_path]
                    if on_crit:
                        console.print(
                            f"\n[dim]Claim next:[/] 'backlog grab' or 'backlog cycle'\n"
                        )

                if task.epic_id:
                    completion_status = loader.set_item_done(task.id)
                else:
                    completion_status = {
                        "epic_completed": False,
                        "milestone_completed": False,
                        "phase_completed": False,
                        "phase_locked": False,
                    }
                # Check epic/milestone/phase completion and print review instructions
                _completion_status = print_completion_notices(console, tree, task)

                # Handle multi-task context
                agent = get_default_agent()
                primary, additional = get_all_current_tasks(agent)

                if primary == task_id and additional:
                    # Primary completed, promote first additional to primary
                    console.print(
                        "\n[yellow]Primary task completed. Additional tasks still active.[/]"
                    )
                    new_primary = additional[0]
                    new_additional = additional[1:]

                    if new_additional:
                        set_multi_task_context(agent, new_primary, new_additional)
                    else:
                        set_current_task(new_primary, agent)

                    console.print(f"[bold]New primary task:[/] {new_primary}\n")
                elif task_id in additional:
                    # Additional task completed, remove from list
                    new_additional = [t for t in additional if t != task_id]
                    if new_additional:
                        set_multi_task_context(agent, primary, new_additional)
                    else:
                        set_current_task(primary, agent)

            return metadata

        run_with_auto_commit(
            "done",
            _run,
            warn=lambda msg: console.print(f"[yellow]Warning:[/] {msg}"),
        )

    except StatusError as e:
        console.print(json.dumps(e.to_dict(), indent=2))
        raise click.Abort()


@cli.command()
@click.argument("item_id")
def undone(item_id):
    """Mark a task/epic/milestone/phase as not done (pending)."""
    try:
        def _run() -> tuple[str, str] | None:
            loader = TaskLoader()
            tree = loader.load("metadata")
            result = loader.set_item_not_done(item_id)
            console.print(
                f"\n[green]✓ Marked not done:[/] {result['item_id']}\n"
                f"  Reset tasks: {result['updated_tasks']}\n"
            )
            task = tree.find_task(result["item_id"])
            title = task.title if task else ""
            return (result["item_id"], title)

        run_with_auto_commit(
            "undone",
            _run,
            warn=lambda msg: console.print(f"[yellow]Warning:[/] {msg}"),
        )
    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


# ============================================================================
# Update Command
# ============================================================================


@cli.command()
@click.argument("task_id")
@click.argument("new_status")
@click.option("--reason", help="Reason for status change")
def update(task_id, new_status, reason):
    """Update task status."""
    try:
        loader = TaskLoader()
        tree = loader.load("metadata")
        task = tree.find_task(task_id)

        if not task:
            console.print(f"[red]Error:[/] Task not found: {task_id}")
            raise click.Abort()

        status = Status(new_status)
        update_status(task, status, reason)
        loader.save_task(task)

        console.print(f"\n[green]✓ Updated:[/] {task.id}\n")
        console.print(f"  Status: {task.status.value}")
        if reason:
            console.print(f"  Reason: {reason}\n")

    except StatusError as e:
        console.print(json.dumps(e.to_dict(), indent=2))
        raise click.Abort()


@cli.command()
@click.argument("task_id")
@click.option(
    "--status",
    "status_value",
    type=click.Choice(
        ["pending", "in_progress", "done", "blocked", "rejected", "cancelled"]
    ),
    help="Set status",
)
@click.option(
    "--priority",
    type=click.Choice(["low", "medium", "high", "critical"]),
    help="Set priority",
)
@click.option(
    "--complexity",
    type=click.Choice(["low", "medium", "high", "critical"]),
    help="Set complexity",
)
@click.option("--estimate", type=float, help="Set estimate in hours")
@click.option("--title", help="Set title")
@click.option("--depends-on", default=None, help="Set comma-separated dependencies")
@click.option("--tags", default=None, help="Set comma-separated tags")
@click.option(
    "--reason", help="Reason for status change (required for blocked/rejected)"
)
@click.option("--body", "-b", default=None, help="Custom body content (replaces task body)")
@click.option(
    "--append-body",
    is_flag=True,
    help="Append provided body to existing task body (requires --body)",
)
def set(
    task_id,
    status_value,
    priority,
    complexity,
    estimate,
    title,
    depends_on,
    tags,
    reason,
    body,
    append_body,
):
    """Set task properties (status, priority, complexity, estimate, title, deps, tags)."""
    try:
        if append_body and body is None:
            raise click.ClickException("--append-body requires --body")
        if (
            status_value is None
            and priority is None
            and complexity is None
            and estimate is None
            and title is None
            and depends_on is None
            and tags is None
            and body is None
            and not append_body
        ):
            raise click.ClickException("set requires at least one property flag")
        metadata: tuple[str, str] | None = None

        def _run() -> tuple[str, str] | None:
            nonlocal metadata
            loader = TaskLoader()
            tree = loader.load("metadata")
            task = tree.find_task(task_id)

            if not task:
                console.print(f"[red]Error:[/] Task not found: {task_id}")
                raise click.Abort()

            if title is not None:
                task.title = title
            if estimate is not None:
                task.estimate_hours = estimate
            if complexity is not None:
                task.complexity = Complexity(complexity)
            if priority is not None:
                task.priority = Priority(priority)
            if depends_on is not None:
                task.depends_on = parse_dependency_ids(depends_on)
            if tags is not None:
                task.tags = [t.strip() for t in tags.split(",") if t.strip()]
            if status_value is not None:
                update_status(task, Status(status_value), reason)

            if body is not None:
                loader.save_task(task, body=body, append_body=append_body)
            else:
                loader.save_task(task)

            metadata = (task.id, task.title)

            console.print(f"\n[green]✓ Updated:[/] {task.id}\n")
            console.print(f"  Title: {task.title}")
            console.print(f"  Status: {task.status.value}")
            console.print(f"  Estimate: {task.estimate_hours}")
            console.print(f"  Complexity: {task.complexity.value}")
            console.print(f"  Priority: {task.priority.value}")
            console.print(
                f"  Depends on: {', '.join(task.depends_on) if task.depends_on else '-'}"
            )
            console.print(f"  Tags: {', '.join(task.tags) if task.tags else '-'}")
            return metadata

        run_with_auto_commit(
            "set",
            _run,
            warn=lambda msg: console.print(f"[yellow]Warning:[/] {msg}"),
        )

    except ValueError as e:
        console.print(f"[red]Error:[/] {e}")
        raise click.Abort()
    except StatusError as e:
        console.print(json.dumps(e.to_dict(), indent=2))
        raise click.Abort()


# ============================================================================
# Sync Command
# ============================================================================


@cli.command()
def sync():
    """Recalculate statistics and critical path."""
    try:
        loader = TaskLoader()
        tree = loader.load("metadata")
        config = load_config()

        calc = CriticalPathCalculator(tree, config["complexity_multipliers"])
        critical_path, next_available = calc.calculate(explicit_only=True)
        tree.critical_path = critical_path
        tree.next_available = next_available
        _warn_non_task_cycle(calc)

        loader.save_stats(tree)

        stats = tree.stats
        console.print("\n[green]✓ Synchronized task tree[/]\n")
        console.print(f"  Total tasks: {stats['total_tasks']}")
        console.print(f"  Done: {stats['done']}")
        console.print(f"  In progress: {stats['in_progress']}")
        console.print(f"  Pending: {stats['pending']}")
        console.print(f"  Blocked: {stats['blocked']}\n")
        console.print(f"  Critical path length: {len(critical_path)} tasks")
        if next_available:
            console.print(f"  Next available: {next_available}\n")

        # Check stale claims
        all_tasks = get_all_tasks(tree)
        stale = check_stale_claims(
            all_tasks,
            config["stale_claim"]["warn_after_minutes"],
            config["stale_claim"]["error_after_minutes"],
        )

        if stale:
            console.print("[yellow]Stale claims:[/]")
            for item in stale:
                console.print(f"  {item['level'].upper()}: {item['message']}")

    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


# ============================================================================
# Unclaim Stale Command
# ============================================================================


@cli.command("unclaim-stale")
@click.option(
    "--threshold",
    "-t",
    default=None,
    type=int,
    help="Age threshold in minutes (uses error threshold from config if not specified)",
)
@click.option(
    "--dry-run",
    is_flag=True,
    help="Show what would be unclaimed without making changes",
)
def unclaim_stale(threshold, dry_run):
    """Unclaim stale task claims.

    Finds tasks that have been claimed for longer than the threshold
    and releases them back to pending status.

    Examples:
        backlog unclaim-stale                    # Use config error threshold
        backlog unclaim-stale --threshold 120    # Unclaim tasks older than 120 minutes
        backlog unclaim-stale --dry-run          # Preview without making changes
    """
    try:
        loader = TaskLoader()
        tree = loader.load("metadata")
        config = load_config()

        # Use error threshold from config if not specified
        if threshold is None:
            threshold = config["stale_claim"]["error_after_minutes"]

        all_tasks = get_all_tasks(tree)
        now = utc_now()

        # Find stale claims
        stale_tasks = []
        for task in all_tasks:
            if task.status == Status.IN_PROGRESS and task.claimed_at:
                age_minutes = (now - to_utc(task.claimed_at)).total_seconds() / 60
                if age_minutes >= threshold:
                    stale_tasks.append(
                        {
                            "task": task,
                            "age_minutes": age_minutes,
                        }
                    )

        if not stale_tasks:
            console.print(
                f"\n[green]✓ No stale claims found[/] (threshold: {threshold} minutes)\n"
            )
            return

        console.print(
            f"\n[yellow]Found {len(stale_tasks)} stale claim(s)[/] (threshold: {threshold} minutes)\n"
        )

        for item in stale_tasks:
            task = item["task"]
            age = item["age_minutes"]

            console.print(f"  {task.id}: {task.title}")
            console.print(f"    Claimed by: {task.claimed_by or 'unknown'}")
            console.print(f"    Age: {int(age)} minutes ({age / 60:.1f} hours)")

            if not dry_run:
                # Unclaim the task by transitioning to pending
                update_status(task, Status.PENDING, reason=f"Stale claim ({int(age)}m)")
                loader.save_task(task)
                console.print(f"    [green]✓ Unclaimed[/]")
            else:
                console.print(f"    [dim]Would unclaim (dry-run)[/]")

            console.print()

        if dry_run:
            console.print(
                f"[dim]Run without --dry-run to actually unclaim these tasks[/]\n"
            )
        else:
            console.print(f"[green]✓ Unclaimed {len(stale_tasks)} task(s)[/]\n")

    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


# ============================================================================
# Agents Command
# ============================================================================


@cli.command()
@click.option(
    "--profile",
    type=click.Choice(["short", "medium", "long", "all"]),
    default="all",
    show_default=True,
    help="Select output draft length.",
)
def agents(profile):
    """Print concise AGENTS.md draft text for task workflow defaults."""
    order = ["short", "medium", "long"] if profile == "all" else [profile]
    for i, key in enumerate(order):
        if i:
            console.print("\n" + "=" * 72 + "\n")
        console.print(AGENTS_SNIPPETS[key])


# ============================================================================
# Add Commands
# ============================================================================


@cli.command("move")
@click.argument("source_id")
@click.option("--to", "dest_id", required=True, help="Destination parent ID")
def move_item(source_id, dest_id):
    """Move task->epic, epic->milestone, or milestone->phase."""
    try:
        loader = TaskLoader()
        result = loader.move_item(source_id, dest_id)
        console.print(f"\n[green]✓ Moved:[/] {result['source_id']}")
        console.print(f"  To:      {result['dest_id']}")
        console.print(f"  New ID:  {result['new_id']}")
    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


@cli.command()
@click.argument("epic_id")
@click.option("--title", "-T", required=True, help="Task title")
@click.option("--estimate", "-e", default=1.0, type=float, help="Hours estimate")
@click.option(
    "--complexity",
    "-c",
    default="medium",
    type=click.Choice(["low", "medium", "high", "critical"]),
)
@click.option(
    "--priority",
    "-p",
    default="medium",
    type=click.Choice(["low", "medium", "high", "critical"]),
)
@click.option("--depends-on", "-d", default="", help="Comma-separated task IDs")
@click.option("--tags", default="", help="Comma-separated tags")
@click.option(
    "--body", "-b", default="", help="Custom body content (replaces default template)"
)
def add(epic_id, title, estimate, complexity, priority, depends_on, tags, body):
    """Add a new task to an epic."""

    def _run() -> tuple[str, str] | None:
        loader = TaskLoader()

        depends_list = parse_dependency_ids(depends_on)
        tags_list = [t.strip() for t in tags.split(",") if t.strip()]

        task_data = {
            "title": title,
            "estimate_hours": estimate,
            "complexity": complexity,
            "priority": priority,
            "depends_on": depends_list,
            "tags": tags_list,
            "body": body if body else None,
        }

        task = loader.create_task(epic_id, task_data)

        metadata = (task.id, task.title)

        console.print(f"\n[green]✓ Created task:[/] {task.id}\n")
        console.print(f"  Title:      {task.title}")
        console.print(f"  Estimate:   {task.estimate_hours}h")
        click.echo(f"\nFile: .tasks/{task.file}")
        if not body:
            console.print(
                "[yellow]IMPORTANT:[/] You MUST fill in the .todo file that was created.\n"
            )
        _print_next_commands(
            f"backlog show {task.id}",
            f"backlog claim {task.id}",
        )

        return metadata
    try:
        run_with_auto_commit(
            "add",
            _run,
            warn=lambda msg: console.print(f"[yellow]Warning:[/] {msg}"),
        )
    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


@cli.command("add-epic")
@click.argument("milestone_id")
@click.option("--title", "-T", "--name", "-n", required=True, help="Epic title")
@click.option("--estimate", "-e", default=4.0, type=float, help="Hours estimate")
@click.option("--description", help="Epic description")
@click.option(
    "--complexity",
    "-c",
    default="medium",
    type=click.Choice(["low", "medium", "high", "critical"]),
)
@click.option("--depends-on", "-d", default="", help="Comma-separated epic IDs")
def add_epic(milestone_id, title, estimate, description, complexity, depends_on):
    """Add a new epic to a milestone."""
    try:
        loader = TaskLoader()

        depends_list = parse_dependency_ids(depends_on)

        epic_data = {
            "name": title,
            "estimate_hours": estimate,
            "description": description,
            "complexity": complexity,
            "depends_on": depends_list,
        }

        epic = loader.create_epic(milestone_id, epic_data)

        console.print(f"\n[green]✓ Created epic:[/] {epic.id}\n")
        console.print(f"  Title:    {epic.name}")
        console.print(f"  Estimate: {epic.estimate_hours}h")
        tree = loader.load("metadata")
        display_epic = tree.find_epic(epic.id) or epic
        console.print(
            f"\n[bold]File:[/] {format_epic_path(tree, display_epic)}/index.yaml\n"
        )
        _print_next_commands(
            f"backlog show {epic.id}",
            f"backlog add {epic.id} --title \"<task title>\"",
        )

    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


@cli.command("add-milestone")
@click.argument("phase_id")
@click.option("--title", "-T", "--name", "-n", required=True, help="Milestone title")
@click.option("--estimate", "-e", default=8.0, type=float, help="Hours estimate")
@click.option(
    "--complexity",
    "-c",
    default="medium",
    type=click.Choice(["low", "medium", "high", "critical"]),
)
@click.option("--depends-on", "-d", default="", help="Comma-separated milestone IDs")
@click.option("--description", help="Milestone description")
def add_milestone(phase_id, title, estimate, complexity, depends_on, description):
    """Add a new milestone to a phase.

    Examples:
        backlog add-milestone P1 --title "Project Setup" --estimate 16
        backlog add-milestone P4 -T "Tool Registry" -e 12 -c high
    """
    try:
        loader = TaskLoader()

        depends_list = parse_dependency_ids(depends_on)

        milestone_data = {
            "name": title,
            "estimate_hours": estimate,
            "complexity": complexity,
            "depends_on": depends_list,
            "description": description,
        }

        milestone = loader.create_milestone(phase_id, milestone_data)

        console.print(f"\n[green]✓ Created milestone:[/] {milestone.id}\n")
        console.print(f"  Title:    {milestone.name}")
        console.print(f"  Estimate: {milestone.estimate_hours}h")
        tree = loader.load("metadata")
        display_milestone = tree.find_milestone(milestone.id) or milestone
        console.print(
            f"\n[bold]File:[/] {format_milestone_path(tree, display_milestone)}/index.yaml\n"
        )
        _print_next_commands(
            f"backlog show {milestone.id}",
            f"backlog add-epic {milestone.id} --title \"<epic title>\"",
        )

    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


@cli.command("add-phase")
@click.option("--title", "-T", "--name", "-n", required=True, help="Phase title")
@click.option("--weeks", "-w", default=2, type=int, help="Timeline in weeks")
@click.option("--estimate", "-e", default=40.0, type=float, help="Hours estimate")
@click.option(
    "--priority",
    "-p",
    default="medium",
    type=click.Choice(["low", "medium", "high", "critical"]),
)
@click.option("--depends-on", "-d", default="", help="Comma-separated phase IDs")
@click.option("--description", help="Phase description")
def add_phase(title, weeks, estimate, priority, depends_on, description):
    """Add a new phase to the project.

    Phases are automatically numbered sequentially.

    Examples:
        backlog add-phase --title "Security Hardening" --weeks 3 --priority high
        backlog add-phase -T "Beta Testing" -w 4 -e 60 -p medium
    """
    try:
        loader = TaskLoader()

        depends_list = parse_dependency_ids(depends_on)

        phase_data = {
            "name": title,
            "weeks": weeks,
            "estimate_hours": estimate,
            "priority": priority,
            "depends_on": depends_list,
            "description": description,
        }

        phase = loader.create_phase(phase_data)

        console.print(f"\n[green]✓ Created phase:[/] {phase.id}\n")
        console.print(f"  Title:    {phase.name}")
        console.print(f"  Weeks:    {phase.weeks}")
        console.print(f"  Estimate: {phase.estimate_hours}h")
        console.print(f"\n[bold]File:[/] {format_phase_path(phase)}/index.yaml\n")
        _print_next_commands(
            f"backlog show {phase.id}",
            f"backlog add-milestone {phase.id} --title \"<milestone title>\"",
        )

    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


@cli.command()
@click.argument("item_id")
def lock(item_id):
    """Lock a phase/milestone/epic so no new child items can be added."""
    try:
        loader = TaskLoader()
        canonical_id = loader.set_item_locked(item_id, True)
        console.print(f"[green]✓ Locked:[/] {canonical_id}")
    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


@cli.command()
@click.argument("item_id")
def unlock(item_id):
    """Unlock a phase/milestone/epic so new child items can be added."""
    try:
        loader = TaskLoader()
        canonical_id = loader.set_item_locked(item_id, False)
        console.print(f"[green]✓ Unlocked:[/] {canonical_id}")
    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


# ============================================================================
# Bug Command
# ============================================================================


@cli.command()
@click.argument("bug_words", nargs=-1, required=False)
@click.option("--title", "-T", required=False, help="Bug title")
@click.option("--estimate", "-e", default=1.0, type=float, help="Hours estimate")
@click.option(
    "--complexity",
    "-c",
    default="medium",
    type=click.Choice(["low", "medium", "high", "critical"]),
)
@click.option(
    "--priority",
    "-p",
    default="high",
    type=click.Choice(["low", "medium", "high", "critical"]),
)
@click.option("--depends-on", "-d", default="", help="Comma-separated task IDs")
@click.option("--tags", default="", help="Comma-separated tags")
@click.option("--simple", "-s", is_flag=True, help="Simple bug (no template body)")
@click.option(
    "--body", "-b", default="", help="Custom body content (replaces default template)"
)
def bug(
    bug_words, title, estimate, complexity, priority, depends_on, tags, simple, body
):
    """Create a new bug report."""

    def _run() -> tuple[str, str] | None:
        loader = TaskLoader()

        positional_title = " ".join(bug_words).strip()
        is_simple = simple
        bug_title = title
        if not bug_title and positional_title:
            bug_title = positional_title
            is_simple = True
        if not bug_title:
            raise ValueError("bug requires --title or description text")
        _require_at_least_two_words(bug_title, "bug")

        depends_list = parse_dependency_ids(depends_on)
        tags_list = [t.strip() for t in tags.split(",") if t.strip()]

        bug_data = {
            "title": bug_title,
            "estimate_hours": estimate,
            "complexity": complexity,
            "priority": priority,
            "depends_on": depends_list,
            "tags": tags_list,
            "simple": is_simple,
            "body": body if body else None,
        }

        created = loader.create_bug(bug_data)
        metadata = (created.id, created.title)

        console.print(f"\n[green]✓ Created bug:[/] {created.id}\n")
        console.print(f"  Title:    {created.title}")
        console.print(f"  Priority: {created.priority.value}")
        console.print(f"  Estimate: {created.estimate_hours}h")
        from .data_dir import get_data_dir_name

        click.echo(f"\nFile: {get_data_dir_name()}/{created.file}")
        if not is_simple and not body:
            console.print(
                "[yellow]IMPORTANT:[/] You MUST fill in the .todo file that was created.\n"
            )
        _print_next_commands(
            f"backlog show {created.id}",
            f"backlog claim {created.id}",
        )

        return metadata
    try:
        run_with_auto_commit(
            "bug",
            _run,
            warn=lambda msg: console.print(f"[yellow]Warning:[/] {msg}"),
        )
    except Exception as e:
        console.print(f"[red]Error:[/] {str(e)}")
        raise click.Abort()


# ============================================================================
# Migrate Command
# ============================================================================


@cli.command()
@click.option(
    "--force", "-f", is_flag=True, help="Force migration even if both dirs exist"
)
@click.option("--no-symlink", is_flag=True, help="Skip creating .tasks symlink")
def migrate(force, no_symlink):
    """Migrate .tasks/ directory to .backlog/."""
    from .data_dir import migrate_data_dir

    success, message = migrate_data_dir(create_symlink=not no_symlink, force=force)

    if success:
        console.print(f"[green]✓ {message}[/]")
    else:
        console.print(f"[red]Error:[/] {message}")
        raise click.Abort()


# ============================================================================
# Register Commands from Modules
# ============================================================================


def _register_all_commands():
    """Register commands from all modules."""
    from .commands import (
        workflow,
        display,
        search,
        reports,
        session,
        timeline,
        data,
        skills,
        intake,
        schema,
        check,
    )

    workflow.register_commands(cli)
    display.register_commands(cli)
    search.register_commands(cli)
    reports.register_commands(cli)
    session.register_commands(cli)
    timeline.register_commands(cli)
    data.register_commands(cli)
    skills.register_commands(cli)
    intake.register_commands(cli)
    schema.register_commands(cli)
    check.register_commands(cli)


_register_all_commands()


if __name__ == "__main__":
    cli()
