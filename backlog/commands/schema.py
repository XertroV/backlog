"""Schema command: show file-kind schemas for task system files."""

from __future__ import annotations

import json

import click
from rich.console import Console

from ..helpers import _context_file, _sessions_file
from ..models import Complexity, Priority, Status

console = Console()


def _field(
    name: str,
    type_name: str,
    required: bool,
    description: str,
    default=None,
    enum=None,
):
    """Create a normalized field descriptor."""
    item = {
        "name": name,
        "type": type_name,
        "required": required,
        "description": description,
    }
    if default is not None:
        item["default"] = default
    if enum is not None:
        item["enum"] = enum
    return item


def _build_schema_spec() -> dict:
    """Build the static schema specification for all supported file types."""
    status_values = [s.value for s in Status]
    complexity_values = [c.value for c in Complexity]
    priority_values = [p.value for p in Priority]

    return {
        "schema_version": 1,
        "scope": "file-kinds",
        "description": (
            "Schema for each supported .tasks file type. This is type-level schema, "
            "not per-file-instance introspection."
        ),
        "enums": {
            "status": status_values,
            "complexity": complexity_values,
            "priority": priority_values,
            "context_mode": ["single", "multi", "siblings"],
        },
        "files": [
            {
                "name": "Root index",
                "path_pattern": ".backlog/index.yaml",
                "format": "yaml",
                "fields": [
                    _field("project", "string", True, "Project display name."),
                    _field(
                        "phases",
                        "list[PhaseEntry]",
                        True,
                        "Phase entries used to load phase directories.",
                    ),
                    _field(
                        "description",
                        "string",
                        False,
                        "Project description.",
                        default="",
                    ),
                    _field(
                        "timeline_weeks",
                        "integer",
                        False,
                        "Project timeline length in weeks.",
                        default=0,
                    ),
                    _field(
                        "critical_path",
                        "list[string]",
                        False,
                        "Computed critical-path task IDs.",
                    ),
                    _field(
                        "next_available",
                        "string|null",
                        False,
                        "Computed next available task ID.",
                    ),
                    _field(
                        "stats",
                        "object",
                        False,
                        "Computed aggregate stats persisted by sync/save_stats.",
                    ),
                ],
            },
            {
                "name": "Phase index",
                "path_pattern": ".backlog/<phase-path>/index.yaml",
                "format": "yaml",
                "fields": [
                    _field(
                        "milestones",
                        "list[MilestoneEntry]",
                        True,
                        "Milestones for this phase.",
                    ),
                    _field(
                        "stats",
                        "object",
                        False,
                        "Computed aggregate stats for this phase.",
                    ),
                ],
            },
            {
                "name": "Milestone index",
                "path_pattern": ".backlog/<phase-path>/<milestone-path>/index.yaml",
                "format": "yaml",
                "fields": [
                    _field(
                        "epics", "list[EpicEntry]", True, "Epics for this milestone."
                    ),
                    _field(
                        "stats",
                        "object",
                        False,
                        "Computed aggregate stats for this milestone.",
                    ),
                ],
            },
            {
                "name": "Epic index",
                "path_pattern": ".backlog/<phase-path>/<milestone-path>/<epic-path>/index.yaml",
                "format": "yaml",
                "fields": [
                    _field(
                        "tasks",
                        "list[TaskIndexEntry|string]",
                        True,
                        "Task entries. Supports object form or legacy filename string form.",
                    ),
                    _field(
                        "stats",
                        "object",
                        False,
                        "Computed stats for tasks in this epic.",
                    ),
                ],
            },
            {
                "name": "Task file",
                "path_pattern": ".backlog/<phase-path>/<milestone-path>/<epic-path>/T###-*.todo",
                "format": "markdown-with-yaml-frontmatter",
                "fields": [
                    _field(
                        "frontmatter",
                        "TaskFrontmatter",
                        True,
                        "YAML task metadata used by loader/status commands.",
                    ),
                    _field(
                        "body",
                        "markdown",
                        True,
                        "Task implementation details, requirements, and acceptance criteria.",
                    ),
                ],
                "notes": [
                    "Canonical files use a YAML frontmatter block between --- delimiters.",
                    "Body commonly includes sections: Requirements and Acceptance Criteria.",
                ],
            },
            {
                "name": "Context file",
                "path_pattern": ".backlog/.context.yaml",
                "format": "yaml",
                "fields": [
                    _field(
                        "mode",
                        "string",
                        True,
                        "Current work context mode.",
                        enum=["single", "multi", "siblings"],
                    ),
                    _field(
                        "started_at",
                        "datetime-iso8601",
                        False,
                        "When current context began.",
                    ),
                    _field(
                        "agent",
                        "string",
                        False,
                        "Agent identifier. Expected in multi/siblings modes.",
                    ),
                    _field(
                        "current_task",
                        "string",
                        False,
                        "Used in single mode.",
                    ),
                    _field(
                        "primary_task",
                        "string",
                        False,
                        "Used in multi and siblings modes.",
                    ),
                    _field(
                        "additional_tasks",
                        "list[string]",
                        False,
                        "Used in multi mode.",
                    ),
                    _field(
                        "sibling_tasks",
                        "list[string]",
                        False,
                        "Used in siblings mode.",
                    ),
                ],
            },
            {
                "name": "Sessions file",
                "path_pattern": ".backlog/.sessions.yaml",
                "format": "yaml",
                "fields": [
                    _field(
                        "<agent_id>",
                        "SessionRecord",
                        True,
                        "Top-level mapping key per active agent.",
                    )
                ],
            },
            {
                "name": "Config file",
                "path_pattern": ".backlog/config.yaml",
                "format": "yaml",
                "fields": [
                    _field(
                        "agent",
                        "object",
                        False,
                        "Agent defaults (default_agent, auto_claim_after_done).",
                    ),
                    _field(
                        "session",
                        "object",
                        False,
                        "Session settings (heartbeat_timeout_minutes).",
                    ),
                    _field(
                        "stale_claim",
                        "object",
                        False,
                        "Stale claim thresholds (warn_after_minutes, error_after_minutes).",
                    ),
                    _field(
                        "complexity_multipliers",
                        "object",
                        False,
                        "Critical path cost weights by complexity.",
                    ),
                    _field(
                        "display",
                        "object",
                        False,
                        "Display settings (e.g., progress bar style).",
                    ),
                    _field(
                        "timeline",
                        "object",
                        False,
                        "Timeline defaults (weeks and hours_per_week).",
                    ),
                    _field(
                        "skills",
                        "object",
                        False,
                        "Optional skill installer overrides (paths and plan_ingest config).",
                    ),
                ],
            },
        ],
        "object_types": [
            {
                "name": "PhaseEntry",
                "fields": [
                    _field("id", "string", True, "Short phase ID, e.g., P1."),
                    _field("name", "string", True, "Phase name."),
                    _field("path", "string", True, "Phase directory path."),
                    _field(
                        "status",
                        "string",
                        False,
                        "Phase status.",
                        default="pending",
                        enum=status_values,
                    ),
                    _field(
                        "weeks", "integer", False, "Phase duration in weeks.", default=2
                    ),
                    _field(
                        "estimate_hours",
                        "number",
                        False,
                        "Phase estimate in hours.",
                        default=40.0,
                    ),
                    _field(
                        "priority",
                        "string",
                        False,
                        "Phase priority.",
                        default="medium",
                        enum=priority_values,
                    ),
                    _field(
                        "depends_on",
                        "list[string]",
                        False,
                        "Phase dependencies.",
                        default=[],
                    ),
                    _field("description", "string", False, "Phase description."),
                ],
            },
            {
                "name": "MilestoneEntry",
                "fields": [
                    _field("id", "string", True, "Short milestone ID, e.g., M1."),
                    _field("name", "string", True, "Milestone name."),
                    _field("path", "string", True, "Milestone directory path."),
                    _field(
                        "status",
                        "string",
                        False,
                        "Milestone status.",
                        default="pending",
                        enum=status_values,
                    ),
                    _field(
                        "estimate_hours",
                        "number",
                        False,
                        "Milestone estimate in hours.",
                        default=8.0,
                    ),
                    _field(
                        "complexity",
                        "string",
                        False,
                        "Milestone complexity.",
                        default="medium",
                        enum=complexity_values,
                    ),
                    _field(
                        "depends_on",
                        "list[string]",
                        False,
                        "Milestone dependencies.",
                        default=[],
                    ),
                    _field("description", "string", False, "Milestone description."),
                ],
            },
            {
                "name": "EpicEntry",
                "fields": [
                    _field("id", "string", True, "Short epic ID, e.g., E1."),
                    _field("name", "string", True, "Epic name."),
                    _field("path", "string", True, "Epic directory path."),
                    _field(
                        "status",
                        "string",
                        False,
                        "Epic status.",
                        default="pending",
                        enum=status_values,
                    ),
                    _field(
                        "estimate_hours",
                        "number",
                        False,
                        "Epic estimate in hours.",
                        default=4.0,
                    ),
                    _field(
                        "complexity",
                        "string",
                        False,
                        "Epic complexity.",
                        default="medium",
                        enum=complexity_values,
                    ),
                    _field(
                        "depends_on",
                        "list[string]",
                        False,
                        "Epic dependencies.",
                        default=[],
                    ),
                    _field("description", "string", False, "Epic description."),
                ],
            },
            {
                "name": "TaskIndexEntry",
                "fields": [
                    _field("id", "string", False, "Task short ID (T###) or full ID."),
                    _field(
                        "file", "string", False, "Task filename relative to epic dir."
                    ),
                    _field("path", "string", False, "Alias for file."),
                    _field("title", "string", False, "Task title fallback."),
                    _field(
                        "status",
                        "string",
                        False,
                        "Task status fallback.",
                        default="pending",
                        enum=status_values,
                    ),
                    _field(
                        "estimate_hours", "number", False, "Task estimate fallback."
                    ),
                    _field(
                        "complexity",
                        "string",
                        False,
                        "Task complexity fallback.",
                        enum=complexity_values,
                    ),
                    _field(
                        "priority",
                        "string",
                        False,
                        "Task priority fallback.",
                        enum=priority_values,
                    ),
                    _field(
                        "depends_on",
                        "list[string]",
                        False,
                        "Task dependencies fallback.",
                    ),
                ],
                "notes": [
                    "If string form is used, it is treated as filename and task ID is derived from filename.",
                    "Frontmatter in .todo file takes precedence over index entry values.",
                ],
            },
            {
                "name": "TaskFrontmatter",
                "fields": [
                    _field("id", "string", True, "Fully qualified task ID."),
                    _field("title", "string", True, "Task title."),
                    _field(
                        "status",
                        "string",
                        True,
                        "Task status.",
                        enum=status_values,
                    ),
                    _field(
                        "estimate_hours", "number", True, "Estimated effort in hours."
                    ),
                    _field(
                        "complexity",
                        "string",
                        True,
                        "Task complexity.",
                        enum=complexity_values,
                    ),
                    _field(
                        "priority",
                        "string",
                        True,
                        "Task priority.",
                        enum=priority_values,
                    ),
                    _field(
                        "depends_on",
                        "list[string]",
                        True,
                        "Dependency task IDs.",
                    ),
                    _field("tags", "list[string]", True, "Task tags."),
                    _field(
                        "claimed_by",
                        "string|null",
                        False,
                        "Current claimant agent ID.",
                    ),
                    _field(
                        "claimed_at",
                        "datetime-iso8601|null",
                        False,
                        "Claim timestamp.",
                    ),
                    _field(
                        "started_at",
                        "datetime-iso8601|null",
                        False,
                        "Work start timestamp.",
                    ),
                    _field(
                        "completed_at",
                        "datetime-iso8601|null",
                        False,
                        "Completion timestamp.",
                    ),
                    _field(
                        "created_at",
                        "datetime-iso8601|null",
                        False,
                        "Creation timestamp.",
                    ),
                    _field(
                        "updated_at",
                        "datetime-iso8601|null",
                        False,
                        "Last metadata update timestamp.",
                    ),
                    _field(
                        "duration_minutes",
                        "number|null",
                        False,
                        "Recorded duration after completion.",
                    ),
                ],
            },
            {
                "name": "SessionRecord",
                "fields": [
                    _field(
                        "started_at",
                        "datetime-iso8601",
                        True,
                        "Session start timestamp.",
                    ),
                    _field(
                        "last_heartbeat",
                        "datetime-iso8601",
                        True,
                        "Most recent heartbeat timestamp.",
                    ),
                    _field(
                        "current_task",
                        "string|null",
                        False,
                        "Current task ID.",
                    ),
                    _field(
                        "progress",
                        "string|null",
                        False,
                        "Latest freeform progress note.",
                    ),
                ],
            },
        ],
    }


def _parse_only_tokens(raw_only: tuple[str, ...]) -> list[str]:
    """Parse --only values (supports repeated flag and comma-separated lists)."""
    tokens = []
    for raw in raw_only:
        if not raw:
            continue
        parts = [part.strip() for part in raw.split(",")]
        tokens.extend([part for part in parts if part])
    return tokens


def _matches_any_token(candidate: str, tokens: list[str]) -> bool:
    """Case-insensitive substring match against filter tokens."""
    lowered = candidate.lower()
    return any(token.lower() in lowered for token in tokens)


def _filter_schema_spec(spec: dict, only_tokens: list[str]) -> dict:
    """Filter schema by file/object names and path patterns."""
    if not only_tokens:
        return spec

    files = [
        item
        for item in spec["files"]
        if _matches_any_token(item["name"], only_tokens)
        or _matches_any_token(item["path_pattern"], only_tokens)
    ]
    object_types = [
        item
        for item in spec["object_types"]
        if _matches_any_token(item["name"], only_tokens)
    ]

    if not files and not object_types:
        known = [item["name"] for item in spec["files"]] + [
            item["name"] for item in spec["object_types"]
        ]
        known_list = ", ".join(sorted(known))
        requested = ", ".join(only_tokens)
        raise click.ClickException(
            f"No schema entries matched --only={requested}. Known entries: {known_list}"
        )

    filtered = dict(spec)
    filtered["files"] = files
    filtered["object_types"] = object_types
    return filtered


def _find_file_spec(spec: dict, name: str) -> dict | None:
    """Find a file-kind spec by name."""
    for item in spec["files"]:
        if item["name"] == name:
            return item
    return None


def _find_object_spec(spec: dict, name: str) -> dict | None:
    """Find an object-type spec by name."""
    for item in spec["object_types"]:
        if item["name"] == name:
            return item
    return None


def _field_names(entry: dict) -> set[str]:
    """Return field names for a schema entry."""
    return {f["name"] for f in entry.get("fields", [])}


def _validate_schema_sync(spec: dict) -> list[str]:
    """Validate schema against parser/runtime expectations."""
    errors = []

    # Enum sync with models
    status_values = [s.value for s in Status]
    complexity_values = [c.value for c in Complexity]
    priority_values = [p.value for p in Priority]

    if spec["enums"]["status"] != status_values:
        errors.append("enums.status does not match Status enum values.")
    if spec["enums"]["complexity"] != complexity_values:
        errors.append("enums.complexity does not match Complexity enum values.")
    if spec["enums"]["priority"] != priority_values:
        errors.append("enums.priority does not match Priority enum values.")
    if spec["enums"]["context_mode"] != ["single", "multi", "siblings"]:
        errors.append("enums.context_mode must be ['single', 'multi', 'siblings'].")

    # Path sync with helpers constants
    context_file = _find_file_spec(spec, "Context file")
    ctx_path = str(_context_file())
    if not context_file:
        errors.append("Missing file schema: Context file.")
    elif context_file["path_pattern"] != ctx_path:
        errors.append(
            f"Context file path mismatch: expected {ctx_path}, got {context_file['path_pattern']}."
        )

    sessions_file = _find_file_spec(spec, "Sessions file")
    sess_path = str(_sessions_file())
    if not sessions_file:
        errors.append("Missing file schema: Sessions file.")
    elif sessions_file["path_pattern"] != sess_path:
        errors.append(
            f"Sessions file path mismatch: expected {sess_path}, got {sessions_file['path_pattern']}."
        )

    # Parsed key sets sync (loader/helpers parsing contract)
    expected_phase = {
        "id",
        "name",
        "path",
        "status",
        "weeks",
        "estimate_hours",
        "priority",
        "depends_on",
        "description",
    }
    expected_milestone = {
        "id",
        "name",
        "path",
        "status",
        "estimate_hours",
        "complexity",
        "depends_on",
        "description",
    }
    expected_epic = {
        "id",
        "name",
        "path",
        "status",
        "estimate_hours",
        "complexity",
        "depends_on",
        "description",
    }
    expected_task_index = {
        "id",
        "file",
        "path",
        "title",
        "status",
        "estimate_hours",
        "complexity",
        "priority",
        "depends_on",
    }
    expected_task_frontmatter = {
        "id",
        "title",
        "status",
        "estimate_hours",
        "complexity",
        "priority",
        "depends_on",
        "tags",
        "claimed_by",
        "claimed_at",
        "started_at",
        "created_at",
        "updated_at",
        "completed_at",
        "duration_minutes",
    }
    expected_session_record = {
        "started_at",
        "last_heartbeat",
        "current_task",
        "progress",
    }
    expected_context_keys = {
        "mode",
        "started_at",
        "agent",
        "current_task",
        "primary_task",
        "additional_tasks",
        "sibling_tasks",
    }
    expected_config_top = {
        "agent",
        "session",
        "stale_claim",
        "complexity_multipliers",
        "display",
        "timeline",
    }

    checks = [
        ("PhaseEntry", expected_phase),
        ("MilestoneEntry", expected_milestone),
        ("EpicEntry", expected_epic),
        ("TaskIndexEntry", expected_task_index),
        ("TaskFrontmatter", expected_task_frontmatter),
        ("SessionRecord", expected_session_record),
    ]
    for name, expected in checks:
        obj = _find_object_spec(spec, name)
        if not obj:
            errors.append(f"Missing object type: {name}.")
            continue
        actual = _field_names(obj)
        missing = sorted(expected - actual)
        if missing:
            errors.append(f"{name} missing fields: {', '.join(missing)}.")

    if context_file:
        context_fields = _field_names(context_file)
        missing_ctx = sorted(expected_context_keys - context_fields)
        if missing_ctx:
            errors.append(f"Context file missing fields: {', '.join(missing_ctx)}.")

    config_file = _find_file_spec(spec, "Config file")
    if not config_file:
        errors.append("Missing file schema: Config file.")
    else:
        config_fields = _field_names(config_file)
        missing_cfg = sorted(expected_config_top - config_fields)
        if missing_cfg:
            errors.append(
                "Config file missing expected top-level keys: "
                + ", ".join(missing_cfg)
                + "."
            )

    return errors


def _format_compact_fields(fields: list[dict], limit: int = 5) -> str:
    """Render compact field list using required marker."""
    names = []
    for field in fields[:limit]:
        marker = "*" if field["required"] else ""
        names.append(f"{field['name']}{marker}")
    if len(fields) > limit:
        names.append(f"...(+{len(fields) - limit})")
    return ", ".join(names)


def _print_text_schema(spec: dict) -> None:
    """Render schema in a readable text format."""
    console.print("\n[bold cyan].backlog File-Type Schema[/]")
    console.print("[dim]Type-level schema; does not enumerate each task instance.[/]\n")

    if spec["files"]:
        console.print("[bold]File Kinds[/]")
        for item in spec["files"]:
            console.print(f"\n[bold]{item['name']}[/]")
            console.print(f"  Path: {item['path_pattern']}")
            console.print(f"  Format: {item['format']}")
            for field in item["fields"]:
                req = "required" if field["required"] else "optional"
                line = f"  - {field['name']} ({field['type']}, {req})"
                if "default" in field:
                    line += f", default={field['default']}"
                if "enum" in field:
                    line += f", enum={', '.join(field['enum'])}"
                line += f" - {field['description']}"
                console.print(line)
            for note in item.get("notes", []):
                console.print(f"  Note: {note}")

    if spec["object_types"]:
        console.print("\n[bold]Object Types[/]")
        for obj in spec["object_types"]:
            console.print(f"\n[bold]{obj['name']}[/]")
            for field in obj["fields"]:
                req = "required" if field["required"] else "optional"
                line = f"  - {field['name']} ({field['type']}, {req})"
                if "default" in field:
                    line += f", default={field['default']}"
                if "enum" in field:
                    line += f", enum={', '.join(field['enum'])}"
                line += f" - {field['description']}"
                console.print(line)
            for note in obj.get("notes", []):
                console.print(f"  Note: {note}")

    console.print()


def _print_compact_schema(spec: dict) -> None:
    """Render compact schema text output."""
    console.print("\n[bold cyan].backlog File-Type Schema (Compact)[/]")
    console.print("[dim]* required field[/]\n")

    if spec["files"]:
        console.print("[bold]File Kinds (compact)[/]")
        for item in spec["files"]:
            fields_str = _format_compact_fields(item["fields"])
            console.print(
                f"- {item['name']} | {item['path_pattern']} | {item['format']} | {fields_str}"
            )

    if spec["object_types"]:
        console.print("\n[bold]Object Types (compact)[/]")
        for obj in spec["object_types"]:
            fields_str = _format_compact_fields(obj["fields"])
            console.print(f"- {obj['name']} | {fields_str}")

    console.print()


@click.command()
@click.option("--json", "as_json", is_flag=True, help="Output schema as JSON.")
@click.option("--compact", is_flag=True, help="Compact text output.")
@click.option(
    "--only",
    "only_filters",
    multiple=True,
    help=(
        "Filter by file/object name or path pattern. "
        "Repeat flag or provide comma-separated values."
    ),
)
@click.option(
    "--check-sync",
    is_flag=True,
    help="Validate schema against parser/runtime contracts.",
)
def schema(as_json, compact, only_filters, check_sync):
    """Show schema details for all supported .tasks file types."""
    full_spec = _build_schema_spec()
    sync_errors = _validate_schema_sync(full_spec)

    only_tokens = _parse_only_tokens(only_filters)
    spec = _filter_schema_spec(full_spec, only_tokens)
    if check_sync and sync_errors:
        raise click.ClickException("\n".join(sync_errors))

    if as_json:
        payload = dict(spec)
        if check_sync:
            payload["sync_check"] = {
                "passed": len(sync_errors) == 0,
                "errors": sync_errors,
            }
        click.echo(json.dumps(payload, indent=2))
        return

    if compact:
        _print_compact_schema(spec)
    else:
        _print_text_schema(spec)

    if check_sync:
        console.print("[green]Schema sync check passed.[/]\n")


def register_commands(cli):
    """Register schema command."""
    cli.add_command(schema)
