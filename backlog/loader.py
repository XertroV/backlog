"""Load task tree from YAML files."""

import os
import re
import shutil
import yaml
from pathlib import Path
from datetime import datetime, timezone
from time import perf_counter
from typing import Dict, Any, Optional, Literal, Union
from .models import (
    TaskTree,
    Phase,
    Milestone,
    Epic,
    Task,
    Status,
    Complexity,
    Priority,
    TaskPath,
)
from .data_dir import get_data_dir, BACKLOG_DIR, TASKS_DIR


class TaskLoader:
    """Load task tree from .backlog/ or .tasks/ directory."""

    def __init__(self, tasks_dir: Optional[str] = None):
        if tasks_dir is None:
            self.tasks_dir = get_data_dir()
        else:
            self.tasks_dir = Path(tasks_dir)
            if not self.tasks_dir.exists():
                raise FileNotFoundError(f"Data directory not found: {tasks_dir}")

    def _new_benchmark(self) -> Dict[str, Any]:
        return {
            "overall_ms": 0.0,
            "index_parse_ms": 0.0,
            "task_frontmatter_parse_ms": 0.0,
            "task_body_parse_ms": 0.0,
            "files": {
                "total": 0,
                "by_type": {
                    "root_index": 0,
                    "phase_index": 0,
                    "milestone_index": 0,
                    "epic_index": 0,
                    "todo_file": 0,
                    "bug_index": 0,
                    "idea_index": 0,
                    "bug_file": 0,
                    "idea_file": 0,
                },
                "by_type_ms": {
                    "root_index": 0.0,
                    "phase_index": 0.0,
                    "milestone_index": 0.0,
                    "epic_index": 0.0,
                    "todo_file": 0.0,
                    "bug_index": 0.0,
                    "idea_index": 0.0,
                    "bug_file": 0.0,
                    "idea_file": 0.0,
                },
            },
            "counts": {
                "phases": 0,
                "milestones": 0,
                "epics": 0,
                "tasks": 0,
            },
            "missing_task_files": 0,
            "phase_timings": [],
            "milestone_timings": [],
            "epic_timings": [],
            "task_timings": [],
        }

    def _record_file(
        self,
        benchmark: Dict[str, Any],
        file_type: str,
        path: Path,
        elapsed_ms: float,
    ) -> None:
        benchmark["files"]["total"] += 1
        benchmark["files"]["by_type"][file_type] = (
            benchmark["files"]["by_type"].get(file_type, 0) + 1
        )
        benchmark["files"]["by_type_ms"][file_type] = (
            benchmark["files"]["by_type_ms"].get(file_type, 0.0) + elapsed_ms
        )
        if file_type.endswith("_index"):
            benchmark["index_parse_ms"] += elapsed_ms

    def _record_timing(self, collection: list, timing: Dict[str, Any], elapsed_ms: float) -> None:
        timing["ms"] = elapsed_ms
        collection.append(timing)

    LoadMode = Literal["full", "metadata", "index"]

    def load(
        self,
        mode: "TaskLoader.LoadMode" = "full",
        include_bugs: bool = True,
        include_ideas: bool = True,
    ) -> TaskTree:
        """Load complete task tree."""
        return self._load_tree(
            mode=mode,
            include_bugs=include_bugs,
            include_ideas=include_ideas,
        )

    def load_with_benchmark(
        self,
        mode: "TaskLoader.LoadMode" = "full",
        parse_task_body: bool = True,
        include_bugs: bool = True,
        include_ideas: bool = True,
    ) -> tuple[TaskTree, Dict[str, Any]]:
        """Load complete task tree and return parse timing metrics."""
        effective_parse_task_body = parse_task_body and mode == "full"
        benchmark = self._new_benchmark()
        benchmark["parse_mode"] = mode
        benchmark["parse_task_body"] = effective_parse_task_body
        start = perf_counter()
        tree = self._load_tree(
            benchmark=benchmark,
            mode=mode,
            parse_task_body=effective_parse_task_body,
            include_bugs=include_bugs,
            include_ideas=include_ideas,
        )
        benchmark["overall_ms"] = (perf_counter() - start) * 1000
        return tree, benchmark

    def load_scope(
        self,
        scope: str | TaskPath,
        mode: "TaskLoader.LoadMode" = "full",
        parse_task_body: bool = False,
        include_bugs: bool = True,
        include_ideas: bool = True,
    ) -> TaskTree:
        """Load a focused subtree for a specific scope without traversing the full tree."""
        path = scope if isinstance(scope, TaskPath) else TaskPath.parse(scope)
        effective_parse_task_body = parse_task_body and mode == "full"
        root_index_path = self.tasks_dir / "index.yaml"
        root_index = self._load_yaml(
            root_index_path, file_type="root_index", benchmark=None
        )

        tree = TaskTree(
            project=root_index["project"],
            description=root_index.get("description", ""),
            timeline_weeks=root_index.get("timeline_weeks", 0),
            critical_path=root_index.get("critical_path", []),
            next_available=root_index.get("next_available"),
        )

        for phase_data in root_index.get("phases", []):
            if phase_data.get("id") != path.phase:
                continue

            try:
                milestone_filter = (
                    path.milestone
                    if path.depth >= 2 and path.milestone
                    else None
                )
                epic_filter = path.epic if path.depth >= 3 and path.epic else None
                task_filter = path.task_id if path.depth >= 4 else None
                phase = self._load_phase(
                    phase_data,
                    load_mode=mode,
                    parse_task_body=effective_parse_task_body,
                    milestone_filter=milestone_filter,
                    epic_filter=epic_filter,
                    task_filter=task_filter,
                )
                if phase:
                    tree.phases.append(phase)
            except Exception as e:
                phase_id = phase_data.get("id", "UNKNOWN")
                phase_path = phase_data.get("path", "UNKNOWN")
                raise RuntimeError(
                    f"Error loading phase {phase_id} (path: {phase_path}): {str(e)}"
                ) from e

        tree.bugs = self._load_bugs(
            load_mode=mode,
            parse_task_body=effective_parse_task_body,
            include_bugs=include_bugs,
        )
        tree.ideas = self._load_ideas(
            load_mode=mode,
            parse_task_body=effective_parse_task_body,
            include_ideas=include_ideas,
        )
        return tree

    def find_fixed_task(self, fixed_id: str) -> Optional[Task]:
        """Load a fixed task by its fixed ID."""
        fixes_dir = self.tasks_dir / "fixes"
        index_path = fixes_dir / "index.yaml"
        if not index_path.exists():
            return None

        idx = self._load_yaml(index_path)
        for entry in idx.get("fixes", []):
            if not isinstance(entry, dict):
                continue
            entry_id = str(entry.get("id", ""))
            if entry_id != fixed_id:
                continue

            filename = str(entry.get("file", ""))
            if not filename:
                return None

            fix_file = fixes_dir / filename
            if not fix_file.exists():
                return None

            frontmatter = self._parse_todo_frontmatter(
                fix_file,
                file_type="todo_file",
            )

            return Task(
                id=str(frontmatter.get("id", fixed_id)),
                title=str(frontmatter.get("title", "")),
                file=str(Path("fixes") / filename),
                status=self._coerce_status(frontmatter.get("status", "done")),
                estimate_hours=float(self._get_estimate_hours(frontmatter, 0.0)),
                complexity=Complexity(frontmatter.get("complexity", "low")),
                priority=Priority(frontmatter.get("priority", "low")),
                depends_on=(
                    frontmatter["depends_on"] if isinstance(frontmatter.get("depends_on"), list) else []
                ),
                claimed_by=frontmatter.get("claimed_by"),
                claimed_at=self._parse_datetime(frontmatter.get("claimed_at")),
                started_at=self._parse_datetime(frontmatter.get("started_at")),
                created_at=self._parse_datetime(frontmatter.get("created_at")),
                updated_at=self._parse_datetime(frontmatter.get("updated_at")),
                completed_at=self._parse_datetime(frontmatter.get("completed_at")),
                duration_minutes=float(frontmatter["duration_minutes"])
                if frontmatter.get("duration_minutes") is not None
                else None,
                tags=frontmatter.get("tags", []) if isinstance(frontmatter.get("tags"), list) else [],
                reason=frontmatter.get("reason"),
                epic_id=None,
                milestone_id=None,
                phase_id=None,
            )

        return None

    def _load_tree(
        self,
        benchmark: Optional[Dict[str, Any]] = None,
        mode: "TaskLoader.LoadMode" = "full",
        parse_task_body: bool = True,
        include_bugs: bool = True,
        include_ideas: bool = True,
    ) -> TaskTree:
        """Load complete task tree."""
        root_index_path = self.tasks_dir / "index.yaml"
        try:
            # Load root index
            root_index = self._load_yaml(
                root_index_path, benchmark=benchmark, file_type="root_index"
            )

            # Create task tree
            tree = TaskTree(
                project=root_index["project"],
                description=root_index.get("description", ""),
                timeline_weeks=root_index.get("timeline_weeks", 0),
                critical_path=root_index.get("critical_path", []),
                next_available=root_index.get("next_available"),
            )

            # Load phases
            for phase_data in root_index.get("phases", []):
                try:
                    phase = self._load_phase(
                        phase_data,
                        benchmark=benchmark,
                        load_mode=mode,
                        parse_task_body=parse_task_body,
                    )
                    tree.phases.append(phase)
                except Exception as e:
                    phase_id = phase_data.get("id", "UNKNOWN")
                    phase_path = phase_data.get("path", "UNKNOWN")
                    raise RuntimeError(
                        f"Error loading phase {phase_id} (path: {phase_path}): {str(e)}"
                    ) from e

            # Load bugs
            tree.bugs = self._load_bugs(
                benchmark=benchmark,
                load_mode=mode,
                parse_task_body=parse_task_body,
                include_bugs=include_bugs,
            )
            tree.ideas = self._load_ideas(
                benchmark=benchmark,
                load_mode=mode,
                parse_task_body=parse_task_body,
                include_ideas=include_ideas,
            )

            return tree
        except KeyError as e:
            raise RuntimeError(
                f"Missing required field {str(e)} in root index: {root_index_path}"
            ) from e

    def _get_estimate_hours(self, data: Dict[str, Any], default: float = 0.0) -> float:
        """Read estimate from either estimate_hours or estimated_hours."""
        value = data.get("estimate_hours")
        if value is None:
            value = data.get("estimated_hours", default)
        return float(value)

    def _coerce_status(self, raw_status: Any, default: str = "pending") -> Status:
        """Normalize legacy status aliases and return Status enum."""
        value = raw_status if raw_status is not None else default
        if isinstance(value, Status):
            return value

        if isinstance(value, str):
            normalized = value.strip().lower().replace("-", "_").replace(" ", "_")
            aliases = {
                "complete": "done",
                "completed": "done",
            }
            normalized = aliases.get(normalized, normalized)
            return Status(normalized)

        return Status(str(value))

    def _load_phase(
        self,
        phase_data: Dict[str, Any],
        benchmark: Optional[Dict[str, Any]] = None,
        load_mode: "TaskLoader.LoadMode" = "full",
        parse_task_body: bool = True,
        milestone_filter: Optional[str] = None,
        epic_filter: Optional[str] = None,
        task_filter: Optional[str] = None,
    ) -> Phase:
        """Load a phase and its milestones."""
        start = perf_counter()
        try:
            phase_path = self.tasks_dir / phase_data["path"]
            if benchmark is not None:
                benchmark["counts"]["phases"] += 1

            # Load phase index (skip if not exists)
            if not (phase_path / "index.yaml").exists():
                # Phase not yet populated, return minimal phase
                phase = Phase(
                    id=phase_data["id"],
                    name=phase_data["name"],
                    path=phase_data["path"],
                    status=self._coerce_status(phase_data.get("status", "pending")),
                    weeks=phase_data.get("weeks", 0),
                    estimate_hours=self._get_estimate_hours(phase_data, 0.0),
                    priority=Priority(phase_data.get("priority", "medium")),
                    depends_on=phase_data.get("depends_on", []),
                    description=phase_data.get("description"),
                    locked=bool(phase_data.get("locked", False)),
                )
                if benchmark is not None:
                    self._record_timing(
                        benchmark["phase_timings"],
                        {"id": phase.id, "path": phase.path},
                        (perf_counter() - start) * 1000,
                    )
                return phase

            phase_index_path = phase_path / "index.yaml"
            phase_index = self._load_yaml(
                phase_index_path,
                benchmark=benchmark,
                file_type="phase_index",
            )

            phase = Phase(
                id=phase_data["id"],
                name=phase_data["name"],
                path=phase_data["path"],
                status=self._coerce_status(phase_data.get("status", "pending")),
                weeks=phase_data.get("weeks", 0),
                estimate_hours=self._get_estimate_hours(phase_data, 0.0),
                priority=Priority(phase_data.get("priority", "medium")),
                depends_on=phase_data.get("depends_on", []),
                description=phase_data.get("description"),
                locked=bool(phase_data.get("locked", phase_index.get("locked", False))),
            )

            # Load milestones
            for milestone_data in phase_index.get("milestones", []):
                milestone_id = milestone_data.get("id")
                if milestone_filter and milestone_id != milestone_filter:
                    continue
                try:
                    milestone = self._load_milestone(
                        phase_path,
                        milestone_data,
                        phase.id,
                        benchmark=benchmark,
                        load_mode=load_mode,
                        parse_task_body=parse_task_body,
                        epic_filter=epic_filter,
                        task_filter=task_filter,
                    )
                    if epic_filter and not milestone.epics:
                        continue
                    phase.milestones.append(milestone)
                except Exception as e:
                    milestone_id = milestone_data.get("id", "UNKNOWN")
                    milestone_path = milestone_data.get("path", "UNKNOWN")
                    raise RuntimeError(
                        f"Error loading milestone {phase.id}.{milestone_id} "
                        f"(path: {phase.path}/{milestone_path}): {str(e)}"
                    ) from e

            if benchmark is not None:
                self._record_timing(
                    benchmark["phase_timings"],
                    {"id": phase.id, "path": phase.path},
                    (perf_counter() - start) * 1000,
                )

            return phase
        except KeyError as e:
            raise RuntimeError(
                f"Missing required field {str(e)} in phase data: {phase_data}"
            ) from e

    def _load_milestone(
        self,
        phase_path: Path,
        milestone_data: Dict[str, Any],
        phase_id: str,
        benchmark: Optional[Dict[str, Any]] = None,
        load_mode: "TaskLoader.LoadMode" = "full",
        parse_task_body: bool = True,
        epic_filter: Optional[str] = None,
        task_filter: Optional[str] = None,
    ) -> Milestone:
        """Load a milestone and its epics."""
        start = perf_counter()
        try:
            milestone_path = phase_path / milestone_data["path"]

            # Build fully qualified milestone ID using TaskPath
            milestone_short_id = milestone_data["id"]
            ms_path = TaskPath.for_milestone(phase_id, milestone_short_id)

            if benchmark is not None:
                benchmark["counts"]["milestones"] += 1

            # Load milestone index (may not exist if not yet populated)
            if (milestone_path / "index.yaml").exists():
                milestone_index_path = milestone_path / "index.yaml"
                milestone_index = self._load_yaml(
                    milestone_index_path,
                    benchmark=benchmark,
                    file_type="milestone_index",
                )
            else:
                milestone_index = {"epics": []}

            milestone = Milestone(
                id=ms_path.full_id,  # Fully qualified: "P1.M1"
                name=milestone_data["name"],
                path=milestone_data["path"],
                status=self._coerce_status(milestone_data.get("status", "pending")),
                estimate_hours=self._get_estimate_hours(milestone_data, 0.0),
                complexity=Complexity(milestone_data.get("complexity", "medium")),
                depends_on=milestone_data.get("depends_on", []),
                description=milestone_data.get("description"),
                phase_id=phase_id,
                locked=bool(
                    milestone_data.get("locked", milestone_index.get("locked", False))
                ),
            )

            # Load epics
            for epic_data in milestone_index.get("epics", []):
                if isinstance(epic_data, dict):
                    epic_id = epic_data.get("id")
                    if epic_filter and epic_id != epic_filter:
                        continue

                try:
                    epic = self._load_epic(
                        milestone_path,
                        epic_data,
                        ms_path,
                        benchmark=benchmark,
                        load_mode=load_mode,
                        parse_task_body=parse_task_body,
                        task_filter=task_filter,
                    )
                    if task_filter and not epic.tasks:
                        continue
                    milestone.epics.append(epic)
                except Exception as e:
                    epic_id = (
                        epic_data.get("id", "UNKNOWN")
                        if isinstance(epic_data, dict)
                        else "UNKNOWN"
                    )
                    epic_file_path = (
                        epic_data.get("path", "UNKNOWN")
                        if isinstance(epic_data, dict)
                        else str(epic_data)
                    )
                    full_path = (
                        f"{phase_path.name}/{milestone_data['path']}/{epic_file_path}"
                    )
                    raise RuntimeError(
                        f"Error loading epic {ms_path.full_id}.{epic_id} "
                        f"(path: {full_path}): {str(e)}\n"
                        f"Epic data type: {type(epic_data)}, value: {epic_data}"
                    ) from e

            if benchmark is not None:
                self._record_timing(
                    benchmark["milestone_timings"],
                    {"id": ms_path.full_id, "path": milestone_data.get("path", "")},
                    (perf_counter() - start) * 1000,
                )

            return milestone
        except KeyError as e:
            raise RuntimeError(
                f"Missing required field {str(e)} in milestone data: {milestone_data}"
            ) from e

    def _load_epic(
        self,
        milestone_path: Path,
        epic_data: Dict[str, Any],
        ms_path: TaskPath,
        benchmark: Optional[Dict[str, Any]] = None,
        load_mode: "TaskLoader.LoadMode" = "full",
        parse_task_body: bool = True,
        task_filter: Optional[str] = None,
    ) -> Epic:
        """Load an epic and its tasks."""
        epic_file_path = milestone_path / epic_data["path"]
        start = perf_counter()

        if benchmark is not None:
            benchmark["counts"]["epics"] += 1

        # Build fully qualified epic ID using TaskPath
        epic_path = ms_path.with_epic(epic_data["id"])

        # Load epic index (may not exist if not yet populated)
        if (epic_file_path / "index.yaml").exists():
            epic_index = self._load_yaml(
                epic_file_path / "index.yaml",
                benchmark=benchmark,
                file_type="epic_index",
            )
        else:
            epic_index = {"tasks": []}

        epic = Epic(
            id=epic_path.full_id,  # Fully qualified: "P1.M1.E1"
            name=epic_data["name"],
            path=epic_data["path"],
            status=self._coerce_status(epic_data.get("status", "pending")),
            estimate_hours=self._get_estimate_hours(epic_data, 0.0),
            complexity=Complexity(epic_data.get("complexity", "medium")),
            depends_on=epic_data.get("depends_on", []),
            description=epic_data.get("description"),
            milestone_id=ms_path.full_id,  # Fully qualified: "P1.M1"
            phase_id=ms_path.phase,
            locked=bool(epic_data.get("locked", epic_index.get("locked", False))),
        )

        # Load tasks
        for task_data in epic_index.get("tasks", []):
            if task_filter and not self._task_matches_filter(task_data, epic_path, task_filter):
                continue
            task = self._load_task(
                epic_file_path,
                task_data,
                epic_path,
                benchmark=benchmark,
                load_mode=load_mode,
                parse_task_body=parse_task_body,
            )
            epic.tasks.append(task)
            if task_filter and self._ids_match(task.id, task_filter):
                break

        if benchmark is not None:
            self._record_timing(
                benchmark["epic_timings"],
                {"id": epic_path.full_id, "path": epic_data.get("path", "")},
                (perf_counter() - start) * 1000,
            )

        return epic

    @staticmethod
    def _ids_match(candidate: str, target: str) -> bool:
        if not candidate or not target:
            return False
        if candidate == target:
            return True
        return candidate.endswith(f".{target}") or target.endswith(f".{candidate}")

    @staticmethod
    def _task_matches_filter(
        task_data: Dict[str, Any] | str, epic_path: TaskPath, task_filter: str
    ) -> bool:
        if isinstance(task_data, str):
            task_short_id = task_data.split("-")[0].replace(".todo", "")
            task_id = epic_path.with_task(task_short_id).full_id
            return TaskLoader._ids_match(task_id, task_filter)

        if not isinstance(task_data, dict):
            return False

        raw_id = task_data.get("id")
        if isinstance(raw_id, str):
            candidate = (
                raw_id
                if raw_id.startswith(epic_path.full_id)
                else epic_path.with_task(raw_id).full_id
            )
            return TaskLoader._ids_match(candidate, task_filter)

        filename = task_data.get("file") or task_data.get("path") or ""
        if not isinstance(filename, str):
            return False
        short_id = filename.split("-")[0].replace(".todo", "")
        task_id = epic_path.with_task(short_id).full_id
        return TaskLoader._ids_match(task_id, task_filter)

    def _load_task(
        self,
        epic_file_path: Path,
        task_data: Dict[str, Any] | str,
        epic_path: TaskPath,
        benchmark: Optional[Dict[str, Any]] = None,
        load_mode: "TaskLoader.LoadMode" = "full",
        parse_task_body: bool = True,
    ) -> Task:
        """Load a task from its .todo file."""
        # Handle both formats: dict with metadata or simple string filename
        if isinstance(task_data, str):
            # Simple format: just a filename
            # Extract task ID from filename (e.g., "T001" from "T001-description.todo")
            filename = task_data
            task_short_id = filename.split("-")[0].replace(".todo", "")
            # Build full qualified task ID using TaskPath
            task_path = epic_path.with_task(task_short_id)
            task_data = {"file": filename, "id": task_path.full_id}
        elif not isinstance(task_data, dict):
            raise ValueError(
                f"Task data must be dict or str, got {type(task_data).__name__}: {task_data}"
            )

        # Support both 'file' and 'path' keys
        filename = task_data.get("file") or task_data.get("path")
        if not filename:
            raise KeyError(
                f"Task data missing 'file' or 'path' key. Task data: {task_data}"
            )

        task_file = epic_file_path / filename
        parse_ms = 0.0
        parse_start = perf_counter() if benchmark is not None else None

        # Parse .todo file (YAML frontmatter + Markdown)
        if load_mode == "index":
            if isinstance(task_data, dict):
                frontmatter = dict(task_data)
        elif task_file.exists():
            if load_mode == "full":
                frontmatter, _ = self._parse_todo_file(
                    task_file,
                    benchmark=benchmark,
                    file_type="todo_file",
                    include_body=parse_task_body,
                )
            else:
                frontmatter = self._parse_todo_frontmatter(
                    task_file, benchmark=benchmark, file_type="todo_file"
                )
            if parse_start is not None:
                parse_ms = (perf_counter() - parse_start) * 1000
        elif load_mode != "index":
            # Task file doesn't exist yet, use data from index
            if benchmark is not None:
                benchmark["missing_task_files"] += 1
            frontmatter = {}

        # Merge frontmatter with task_data (frontmatter takes precedence)
        task_id = frontmatter.get("id", task_data.get("id", ""))

        # If task_id doesn't start with the phase prefix, it's incomplete - build the full ID
        if task_id and not task_id.startswith(f"{epic_path.phase}."):
            # Extract just the T### part
            task_short_id = task_id.split(".")[
                -1
            ]  # Gets T001 from either "T001" or "P1.M2.E3.T001"
            task_path = epic_path.with_task(task_short_id)
            task_id = task_path.full_id

        title = frontmatter.get("title", task_data.get("title", ""))
        status = frontmatter.get("status", task_data.get("status", "pending"))
        estimate_hours = frontmatter.get("estimate_hours")
        if estimate_hours is None:
            estimate_hours = frontmatter.get("estimated_hours")
        if estimate_hours is None:
            estimate_hours = task_data.get("estimate_hours")
        if estimate_hours is None:
            estimate_hours = task_data.get("estimated_hours", 0.0)
        complexity = frontmatter.get("complexity", task_data.get("complexity", "low"))
        priority = frontmatter.get("priority", task_data.get("priority", "medium"))
        depends_on = frontmatter.get("depends_on", task_data.get("depends_on", []))

        depends_on = self._expand_depends_on(depends_on, epic_path)

        # Parse datetime fields
        claimed_at = self._parse_datetime(frontmatter.get("claimed_at"))
        started_at = self._parse_datetime(frontmatter.get("started_at"))
        created_at = self._parse_datetime(frontmatter.get("created_at"))
        updated_at = self._parse_datetime(frontmatter.get("updated_at"))
        completed_at = self._parse_datetime(frontmatter.get("completed_at"))

        # Parse duration if present
        duration_minutes = frontmatter.get("duration_minutes")
        if duration_minutes is not None:
            duration_minutes = float(duration_minutes)

        if benchmark is not None:
            benchmark["counts"]["tasks"] += 1
            self._record_timing(
                benchmark["task_timings"],
                {"id": task_id, "path": task_file.as_posix(), "epic_id": epic_path.full_id},
                parse_ms,
            )

        task = Task(
            id=task_id,
            title=title,
            file=str(task_file.relative_to(self.tasks_dir)),
            status=self._coerce_status(status),
            estimate_hours=float(estimate_hours),
            complexity=Complexity(complexity),
            priority=Priority(priority),
            depends_on=depends_on if isinstance(depends_on, list) else [],
            claimed_by=frontmatter.get("claimed_by"),
            claimed_at=claimed_at,
            started_at=started_at,
            created_at=created_at,
            updated_at=updated_at,
            completed_at=completed_at,
            duration_minutes=duration_minutes,
            tags=frontmatter.get("tags", []),
            reason=frontmatter.get("reason"),
            epic_id=epic_path.full_id,  # Fully qualified: "P1.M1.E1"
            milestone_id=epic_path.milestone_id,  # Fully qualified: "P1.M1"
            phase_id=epic_path.phase,
        )

        return task

    def _load_yaml(
        self,
        filepath: Path,
        benchmark: Optional[Dict[str, Any]] = None,
        file_type: str = "yaml",
    ) -> Dict[str, Any]:
        """Load YAML file."""
        start = perf_counter()
        try:
            with open(filepath, "r") as f:
                data = yaml.safe_load(f)
                if data is None:
                    raise ValueError(f"YAML file is empty or invalid: {filepath}")
                if not isinstance(data, dict):
                    raise ValueError(
                        f"YAML file does not contain a dictionary. "
                        f"Got {type(data).__name__}: {filepath}"
                    )
                return data
        except yaml.YAMLError as e:
            raise RuntimeError(f"YAML parsing error in {filepath}: {str(e)}") from e
        except FileNotFoundError:
            raise FileNotFoundError(f"YAML file not found: {filepath}")
        except Exception as e:
            raise RuntimeError(f"Error reading {filepath}: {str(e)}") from e
        finally:
            if benchmark is not None:
                self._record_file(benchmark, file_type, filepath, (perf_counter() - start) * 1000)

    def _parse_todo_file(
        self,
        filepath: Path,
        benchmark: Optional[Dict[str, Any]] = None,
        file_type: str = "todo_file",
        include_body: bool = True,
    ) -> tuple[Dict[str, Any], str]:
        """Parse .todo file with YAML frontmatter."""
        start = perf_counter()
        frontmatter: Dict[str, Any] = {}
        frontmatter_parse_ms = 0.0
        body_parse_ms = 0.0

        if include_body:
            with open(filepath, "r") as f:
                content = f.read()

            # Split frontmatter and body
            parts = content.split("---\n", 2)
            if len(parts) >= 3:
                frontmatter_str = parts[1]
                frontmatter_parse_start = perf_counter()
                frontmatter = yaml.safe_load(frontmatter_str) or {}
                frontmatter_parse_ms = (perf_counter() - frontmatter_parse_start) * 1000

                body_start = perf_counter()
                body = parts[2]
                body_parse_ms = (perf_counter() - body_start) * 1000
                result = (frontmatter, body)
            else:
                result = ({}, content)
        else:
            with open(filepath, "r") as f:
                first_line = f.readline()
                if first_line.replace("\ufeff", "").strip() != "---":
                    result = ({}, "")
                else:
                    lines = []
                    for line in f:
                        if line.strip() == "---":
                            break
                        lines.append(line)
                    if lines:
                        frontmatter_parse_start = perf_counter()
                        frontmatter = yaml.safe_load("".join(lines)) or {}
                        frontmatter_parse_ms = (perf_counter() - frontmatter_parse_start) * 1000
                    result = (frontmatter, "")

        if benchmark is not None:
            self._record_file(benchmark, file_type, filepath, (perf_counter() - start) * 1000)
            benchmark["task_frontmatter_parse_ms"] += frontmatter_parse_ms
            benchmark["task_body_parse_ms"] += body_parse_ms
        return result

    def _parse_todo_frontmatter(
        self,
        filepath: Path,
        benchmark: Optional[Dict[str, Any]] = None,
        file_type: str = "todo_file",
    ) -> Dict[str, Any]:
        """Parse only the YAML frontmatter section of a .todo file."""
        start = perf_counter()
        frontmatter: Dict[str, Any] = {}
        frontmatter_parse_ms = 0.0

        try:
            with open(filepath, "r") as f:
                first_line = f.readline()
                if first_line.replace("\ufeff", "").strip() != "---":
                    return {}

                lines = []
                for line in f:
                    if line.strip() == "---":
                        break
                    lines.append(line)

                if lines:
                    parse_start = perf_counter()
                    frontmatter = yaml.safe_load("".join(lines)) or {}
                    frontmatter_parse_ms = (perf_counter() - parse_start) * 1000
        finally:
            if benchmark is not None:
                self._record_file(
                    benchmark,
                    file_type,
                    filepath,
                    (perf_counter() - start) * 1000,
                )
                benchmark["task_frontmatter_parse_ms"] += frontmatter_parse_ms

        return frontmatter

    def _parse_datetime(self, dt_str: Optional[str]) -> Optional[datetime]:
        """Parse datetime string."""
        if not dt_str:
            return None
        try:
            return datetime.fromisoformat(dt_str.replace("Z", "+00:00"))
        except:
            return None

    def _expand_depends_on(self, depends_on: list, epic_path: TaskPath) -> list[str]:
        """Expand short dependency IDs to fully qualified IDs.

        Converts:
        - "T001" -> "P1.M1.E1.T001" (within same epic)
        - "P1.M2.E3.T004" -> "P1.M2.E3.T004" (already qualified, unchanged)
        """
        if not depends_on or not isinstance(depends_on, list):
            return []

        expanded = []
        for dep in depends_on:
            if not isinstance(dep, str):
                continue
            if dep.startswith("P") and dep.count(".") >= 3:
                expanded.append(dep)
            elif dep.startswith("T"):
                expanded_dep = f"{epic_path.full_id}.{dep}"
                expanded.append(expanded_dep)
            else:
                expanded.append(dep)
        return expanded

    def save_task(
        self, task: Task, body: Optional[str] = None, append_body: bool = False
    ) -> None:
        """Save task updates to its .todo file."""
        task_file = self.tasks_dir / task.file

        # Load existing file
        if task_file.exists():
            frontmatter, existing_body = self._parse_todo_file(task_file)
        else:
            raise FileNotFoundError(f"Task file not found: {task_file}")

        # Update frontmatter fields
        frontmatter["title"] = task.title
        frontmatter["status"] = task.status.value
        frontmatter["estimate_hours"] = task.estimate_hours
        frontmatter["complexity"] = task.complexity.value
        frontmatter["priority"] = task.priority.value
        frontmatter["depends_on"] = task.depends_on
        frontmatter["tags"] = task.tags
        frontmatter["claimed_by"] = task.claimed_by
        frontmatter["claimed_at"] = (
            task.claimed_at.isoformat() if task.claimed_at else None
        )
        frontmatter["started_at"] = (
            task.started_at.isoformat() if task.started_at else None
        )
        frontmatter["created_at"] = (
            task.created_at.isoformat() if task.created_at else None
        )
        frontmatter["updated_at"] = (
            task.updated_at.isoformat() if task.updated_at else None
        )
        frontmatter["completed_at"] = (
            task.completed_at.isoformat() if task.completed_at else None
        )
        if task.reason is not None:
            frontmatter["reason"] = task.reason
        elif "reason" in frontmatter:
            del frontmatter["reason"]
        if task.duration_minutes is not None:
            frontmatter["duration_minutes"] = task.duration_minutes

        if body is None:
            body = existing_body
        elif append_body:
            if existing_body:
                if existing_body.endswith("\n\n"):
                    body = existing_body + body
                elif existing_body.endswith("\n"):
                    body = existing_body + "\n" + body
                else:
                    body = existing_body + "\n\n" + body

        # Write back
        with open(task_file, "w") as f:
            f.write("---\n")
            yaml.dump(frontmatter, f, default_flow_style=False, sort_keys=False)
            f.write("---\n")
            f.write(body)

    def save_stats(self, tree: TaskTree) -> None:
        """Update statistics in index files."""
        # Update root index
        root_index_path = self.tasks_dir / "index.yaml"
        root_index = self._load_yaml(root_index_path)
        root_index["stats"] = tree.stats
        root_index["critical_path"] = tree.critical_path
        root_index["next_available"] = tree.next_available

        with open(root_index_path, "w") as f:
            yaml.dump(root_index, f, default_flow_style=False, sort_keys=False)

        # Update phase indices
        for phase in tree.phases:
            phase_index_path = self.tasks_dir / phase.path / "index.yaml"

            # Skip if phase index doesn't exist yet
            if not phase_index_path.exists():
                continue

            phase_index = self._load_yaml(phase_index_path)
            phase_index["stats"] = phase.stats

            with open(phase_index_path, "w") as f:
                yaml.dump(phase_index, f, default_flow_style=False, sort_keys=False)

            # Update milestone indices
            for milestone in phase.milestones:
                milestone_index_path = (
                    self.tasks_dir / phase.path / milestone.path / "index.yaml"
                )
                if milestone_index_path.exists():
                    milestone_index = self._load_yaml(milestone_index_path)
                    milestone_index["stats"] = milestone.stats
                    with open(milestone_index_path, "w") as f:
                        yaml.dump(
                            milestone_index,
                            f,
                            default_flow_style=False,
                            sort_keys=False,
                        )

                # Update epic indices
                for epic in milestone.epics:
                    epic_index_path = (
                        self.tasks_dir
                        / phase.path
                        / milestone.path
                        / epic.path
                        / "index.yaml"
                    )
                    if not epic_index_path.exists():
                        continue
                    epic_index = self._load_yaml(epic_index_path)
                    epic_index["stats"] = epic.stats
                    with open(epic_index_path, "w") as f:
                        yaml.dump(
                            epic_index,
                            f,
                            default_flow_style=False,
                            sort_keys=False,
                        )

    def _load_bugs(
        self,
        benchmark: Optional[Dict[str, Any]] = None,
        load_mode: "TaskLoader.LoadMode" = "full",
        parse_task_body: bool = True,
        include_bugs: bool = True,
    ):
        """Load bugs from .tasks/bugs/ directory."""
        if not include_bugs:
            return []

        bugs_dir = self.tasks_dir / "bugs"
        index_path = bugs_dir / "index.yaml"
        if not index_path.exists():
            return []

        idx = self._load_yaml(index_path, benchmark=benchmark, file_type="bug_index")
        bugs = []
        for entry in idx.get("bugs", []):
            if not isinstance(entry, dict):
                continue
            filename = entry.get("file", "")
            if not filename:
                continue
            file_path = bugs_dir / filename
            if benchmark is not None:
                benchmark["counts"]["tasks"] += 1
            if load_mode == "index":
                frontmatter = dict(entry)
            else:
                if not file_path.exists():
                    if benchmark is not None:
                        benchmark["missing_task_files"] += 1
                    continue
                if load_mode == "full":
                    frontmatter, _ = self._parse_todo_file(
                        file_path,
                        benchmark=benchmark,
                        file_type="bug_file",
                        include_body=parse_task_body,
                    )
                else:
                    frontmatter = self._parse_todo_frontmatter(
                        file_path, benchmark=benchmark, file_type="bug_file"
                    )
            title = str(frontmatter.get("title") or entry.get("title") or "").strip()
            if not title:
                title = self._fallback_aux_title(filename, str(frontmatter.get("id", "")))
            bugs.append(
                Task(
                    id=str(frontmatter.get("id", "")),
                    title=title,
                    file=str(Path("bugs") / filename),
                    status=self._coerce_status(frontmatter.get("status", "pending")),
                    estimate_hours=float(self._get_estimate_hours(frontmatter, 1.0)),
                    complexity=Complexity(frontmatter.get("complexity", "medium")),
                    priority=Priority(frontmatter.get("priority", "medium")),
                    depends_on=frontmatter.get("depends_on", [])
                    if isinstance(frontmatter.get("depends_on"), list)
                    else [],
                    claimed_by=frontmatter.get("claimed_by"),
                    claimed_at=self._parse_datetime(frontmatter.get("claimed_at")),
                    started_at=self._parse_datetime(frontmatter.get("started_at")),
                    created_at=self._parse_datetime(frontmatter.get("created_at")),
                    updated_at=self._parse_datetime(frontmatter.get("updated_at")),
                    completed_at=self._parse_datetime(frontmatter.get("completed_at")),
                    duration_minutes=float(frontmatter["duration_minutes"])
                    if frontmatter.get("duration_minutes") is not None
                    else None,
                    tags=frontmatter.get("tags", [])
                    if isinstance(frontmatter.get("tags"), list)
                    else [],
                    reason=frontmatter.get("reason"),
                    epic_id=None,
                    milestone_id=None,
                    phase_id=None,
                )
            )
        return bugs

    def _load_ideas(
        self,
        benchmark: Optional[Dict[str, Any]] = None,
        load_mode: "TaskLoader.LoadMode" = "full",
        parse_task_body: bool = True,
        include_ideas: bool = True,
    ):
        """Load ideas from .tasks/ideas/ directory."""
        if not include_ideas:
            return []

        ideas_dir = self.tasks_dir / "ideas"
        index_path = ideas_dir / "index.yaml"
        if not index_path.exists():
            return []

        idx = self._load_yaml(index_path, benchmark=benchmark, file_type="idea_index")
        ideas = []
        for entry in idx.get("ideas", []):
            if not isinstance(entry, dict):
                continue
            filename = entry.get("file", "")
            if not filename:
                continue
            file_path = ideas_dir / filename
            if benchmark is not None:
                benchmark["counts"]["tasks"] += 1
            if load_mode == "index":
                frontmatter = dict(entry)
            else:
                if not file_path.exists():
                    if benchmark is not None:
                        benchmark["missing_task_files"] += 1
                    continue
                if load_mode == "full":
                    frontmatter, _ = self._parse_todo_file(
                        file_path,
                        benchmark=benchmark,
                        file_type="idea_file",
                        include_body=parse_task_body,
                    )
                else:
                    frontmatter = self._parse_todo_frontmatter(
                        file_path, benchmark=benchmark, file_type="idea_file"
                    )
            title = str(frontmatter.get("title") or entry.get("title") or "").strip()
            if not title:
                title = self._fallback_aux_title(filename, str(frontmatter.get("id", "")))
            ideas.append(
                Task(
                    id=str(frontmatter.get("id", "")),
                    title=title,
                    file=str(Path("ideas") / filename),
                    status=self._coerce_status(frontmatter.get("status", "pending")),
                    estimate_hours=float(self._get_estimate_hours(frontmatter, 1.0)),
                    complexity=Complexity(frontmatter.get("complexity", "medium")),
                    priority=Priority(frontmatter.get("priority", "medium")),
                    depends_on=frontmatter.get("depends_on", [])
                    if isinstance(frontmatter.get("depends_on"), list)
                    else [],
                    claimed_by=frontmatter.get("claimed_by"),
                    claimed_at=self._parse_datetime(frontmatter.get("claimed_at")),
                    started_at=self._parse_datetime(frontmatter.get("started_at")),
                    created_at=self._parse_datetime(frontmatter.get("created_at")),
                    updated_at=self._parse_datetime(frontmatter.get("updated_at")),
                    completed_at=self._parse_datetime(frontmatter.get("completed_at")),
                    duration_minutes=float(frontmatter["duration_minutes"])
                    if frontmatter.get("duration_minutes") is not None
                    else None,
                    tags=frontmatter.get("tags", [])
                    if isinstance(frontmatter.get("tags"), list)
                    else [],
                    reason=frontmatter.get("reason"),
                    epic_id=None,
                    milestone_id=None,
                    phase_id=None,
                )
            )
        return ideas

    def create_fixed(self, fixed_data: dict) -> Task:
        """Create a completed ad-hoc fix note."""
        fixes_dir = self.tasks_dir / "fixes"
        fixes_dir.mkdir(parents=True, exist_ok=True)
        index_path = fixes_dir / "index.yaml"

        raw_at = fixed_data.get("at")
        if raw_at:
            created_at = self._parse_datetime(str(raw_at))
            if created_at is None:
                raise ValueError("fixed --at must be an ISO 8601 timestamp")
            if created_at.tzinfo is None:
                created_at = created_at.replace(tzinfo=timezone.utc)
        else:
            created_at = datetime.now(timezone.utc)

        month_dir_name = created_at.strftime("%Y-%m")
        month_dir = fixes_dir / month_dir_name
        month_dir.mkdir(parents=True, exist_ok=True)

        next_num = 1
        if index_path.exists():
            idx = self._load_yaml(index_path)
            existing = idx.get("fixes", [])
            nums = []
            for entry in existing:
                if not isinstance(entry, dict):
                    continue
                entry_id = str(entry.get("id", ""))
                match = re.match(r"F(\d+)", entry_id)
                if match:
                    nums.append(int(match.group(1)))
                else:
                    file = str(entry.get("file", ""))
                    file_match = re.match(r".*/?F(\d+)-", file)
                    if file_match:
                        nums.append(int(file_match.group(1)))
            if nums:
                next_num = max(nums) + 1

        fixed_id = f"F{next_num:03d}"
        slug = self._slugify(fixed_data["title"])
        filename = f"{fixed_id}-{slug}.todo"
        file_path = month_dir / filename

        body = fixed_data.get("body") or ""
        frontmatter = {
            "id": fixed_id,
            "type": "fixed",
            "title": fixed_data["title"],
            "description": fixed_data.get("description", fixed_data["title"]),
            "status": "done",
            "estimate_hours": 0.0,
            "complexity": "low",
            "priority": "low",
            "depends_on": [],
            "tags": fixed_data.get("tags", []),
            "created_at": created_at.isoformat(),
            "updated_at": created_at.isoformat(),
            "completed_at": created_at.isoformat(),
        }

        with open(file_path, "w") as f:
            f.write("---\n")
            yaml.dump(frontmatter, f, default_flow_style=False, sort_keys=False)
            f.write("---\n")
            f.write(body)

        if index_path.exists():
            idx = self._load_yaml(index_path)
        else:
            idx = {"fixes": []}

        fixes_list = idx.get("fixes", [])
        fixes_list.append({"id": fixed_id, "file": f"{month_dir_name}/{filename}"})
        idx["fixes"] = fixes_list
        with open(index_path, "w") as f:
            yaml.dump(idx, f, default_flow_style=False, sort_keys=False)

        return Task(
            id=fixed_id,
            title=fixed_data["title"],
            file=str(Path("fixes") / month_dir_name / filename),
            status=Status.DONE,
            estimate_hours=0.0,
            complexity=Complexity.LOW,
            priority=Priority.LOW,
            depends_on=[],
            created_at=created_at,
            updated_at=created_at,
            tags=fixed_data.get("tags", []),
            epic_id=None,
            milestone_id=None,
            phase_id=None,
        )

    def create_bug(self, bug_data: dict) -> Task:
        """
        Create a new bug report.

        Args:
            bug_data: Dict with keys: title, estimate_hours, complexity, priority,
                       depends_on, tags

        Returns:
            The created Task object
        """
        bugs_dir = self.tasks_dir / "bugs"
        bugs_dir.mkdir(parents=True, exist_ok=True)
        index_path = bugs_dir / "index.yaml"

        # Determine next bug number
        next_num = 1
        if index_path.exists():
            idx = self._load_yaml(index_path)
            existing = idx.get("bugs", [])
            nums = []
            for entry in existing:
                match = re.match(r"B(\d+)", entry.get("file", ""))
                if match:
                    nums.append(int(match.group(1)))
            if nums:
                next_num = max(nums) + 1

        bug_id = f"B{next_num:03d}"
        slug = self._slugify(bug_data["title"])
        filename = f"{bug_id}-{slug}.todo"
        file_path = bugs_dir / filename

        # Prepare frontmatter
        created_at = datetime.now(timezone.utc)
        frontmatter = {
            "id": bug_id,
            "title": bug_data["title"],
            "status": "pending",
            "estimate_hours": bug_data.get("estimate_hours", 1.0),
            "complexity": bug_data.get("complexity", "medium"),
            "priority": bug_data.get("priority", "high"),
            "depends_on": bug_data.get("depends_on", []),
            "tags": bug_data.get("tags", []),
            "created_at": created_at.isoformat(),
            "updated_at": created_at.isoformat(),
        }

        if bug_data.get("body"):
            body = bug_data["body"]
        elif bug_data.get("simple"):
            body = f"\n{bug_data['title']}\n"
        else:
            body = f"""
# {bug_data["title"]}

## Steps to Reproduce

1. TODO: Add steps

## Expected Behavior

TODO: Describe expected behavior

## Actual Behavior

TODO: Describe actual behavior
"""

        with open(file_path, "w") as f:
            f.write("---\n")
            yaml.dump(frontmatter, f, default_flow_style=False, sort_keys=False)
            f.write("---\n")
            f.write(body)

        # Update bugs index
        if index_path.exists():
            idx = self._load_yaml(index_path)
        else:
            idx = {"bugs": []}
        bugs_list = idx.get("bugs", [])
        bugs_list.append({"file": filename})
        idx["bugs"] = bugs_list
        with open(index_path, "w") as f:
            yaml.dump(idx, f, default_flow_style=False, sort_keys=False)

        return Task(
            id=bug_id,
            title=bug_data["title"],
            file=str(Path("bugs") / filename),
            status=Status.PENDING,
            estimate_hours=float(bug_data.get("estimate_hours", 1.0)),
            complexity=Complexity(bug_data.get("complexity", "medium")),
            priority=Priority(bug_data.get("priority", "high")),
            depends_on=bug_data.get("depends_on", []),
            created_at=created_at,
            updated_at=created_at,
            tags=bug_data.get("tags", []),
            epic_id=None,
            milestone_id=None,
            phase_id=None,
        )

    def create_idea(self, idea_data: dict) -> Task:
        """Create a new idea intake item for later planning and ingestion."""
        ideas_dir = self.tasks_dir / "ideas"
        ideas_dir.mkdir(parents=True, exist_ok=True)
        index_path = ideas_dir / "index.yaml"

        # Determine next idea number
        next_num = 1
        if index_path.exists():
            idx = self._load_yaml(index_path)
            existing = idx.get("ideas", [])
            nums = []
            for entry in existing:
                entry_id = str(entry.get("id", ""))
                match = re.match(r"I(\d+)$", entry_id)
                if not match:
                    match = re.match(r"I(\d+)", entry.get("file", ""))
                if match:
                    nums.append(int(match.group(1)))
            if nums:
                next_num = max(nums) + 1

        idea_id = f"I{next_num:03d}"
        slug = self._slugify(idea_data["title"])
        filename = f"{idea_id}-{slug}.todo"
        file_path = ideas_dir / filename
        created_at = datetime.now(timezone.utc)

        frontmatter = {
            "id": idea_id,
            "title": idea_data["title"],
            "status": "pending",
            "estimate_hours": idea_data.get("estimate_hours", 10.0),
            "complexity": idea_data.get("complexity", "medium"),
            "priority": idea_data.get("priority", "medium"),
            "depends_on": idea_data.get("depends_on", []),
            "tags": idea_data.get("tags", ["idea", "planning"]),
            "created_at": created_at.isoformat(),
            "updated_at": created_at.isoformat(),
        }

        body = str(idea_data.get("body") or "").strip()
        if body:
            rendered_body = body
        elif idea_data.get("simple"):
            rendered_body = f"\n{idea_data['title']}\n"
        else:
            rendered_body = f"""
# Idea Intake: {idea_data["title"]}

## Original Idea

{idea_data["title"]}

## Planning Task (Equivalent of /plan-task)

- Run `/plan-task "{idea_data["title"]}"` to decompose this idea into actionable work.
- Confirm placement in the current `.tasks` hierarchy before creating work items.

## Ingest Plan Into .tasks

- Create implementation items with `tasks add` and related hierarchy commands (`tasks add-epic`, `tasks add-milestone`, `tasks add-phase`) as needed.
- Create follow-up defects with `tasks bug` when bug-style work is identified.
- Record all created IDs below and wire dependencies.

## Created Work Items

- Add created task IDs
- Add created bug IDs (if any)

## Completion Criteria

- Idea has been decomposed into concrete `.tasks` work items.
- New items include clear acceptance criteria and dependencies.
- This idea intake is updated with created IDs and marked done.
"""

        with open(file_path, "w") as f:
            f.write("---\n")
            yaml.dump(frontmatter, f, default_flow_style=False, sort_keys=False)
            f.write("---\n")
            f.write(rendered_body)

        if index_path.exists():
            idx = self._load_yaml(index_path)
        else:
            idx = {"ideas": []}

        ideas_list = idx.get("ideas", [])
        ideas_list.append({"id": idea_id, "file": filename})
        idx["ideas"] = ideas_list

        with open(index_path, "w") as f:
            yaml.dump(idx, f, default_flow_style=False, sort_keys=False)

        return Task(
            id=idea_id,
            title=idea_data["title"],
            file=str(Path("ideas") / filename),
            status=Status.PENDING,
            estimate_hours=float(idea_data.get("estimate_hours", 10.0)),
            complexity=Complexity(idea_data.get("complexity", "medium")),
            priority=Priority(idea_data.get("priority", "medium")),
            depends_on=idea_data.get("depends_on", []),
            created_at=created_at,
            updated_at=created_at,
            tags=idea_data.get("tags", ["idea", "planning"]),
            epic_id=None,
            milestone_id=None,
            phase_id=None,
        )

    def create_task(self, epic_id: str, task_data: dict) -> Task:
        """
        Create a new task in the specified epic.

        Args:
            epic_id: Fully qualified epic ID (e.g., "P5.M04.E4")
            task_data: Dict with keys: title, estimate_hours, complexity, priority,
                       depends_on, tags, description

        Returns:
            The created Task object

        Raises:
            ValueError: If epic not found or invalid data
        """
        # Parse the epic path
        epic_path = TaskPath.parse(epic_id)
        if not epic_path.is_epic:
            raise ValueError(f"Invalid epic ID: {epic_id}")

        # Find the epic directory
        epic_dir = self._find_epic_dir(epic_path)
        if not epic_dir:
            raise ValueError(f"Epic directory not found for: {epic_id}")
        tree = self.load("metadata")
        epic = tree.find_epic(epic_id)
        if not epic:
            raise ValueError(f"Epic not found: {epic_id}")
        milestone = tree.find_milestone(epic.milestone_id or "")
        phase = tree.find_phase(epic.phase_id or "")
        if phase and phase.locked:
            raise ValueError(
                f"Phase {phase.id} has been closed and cannot accept new tasks. The agent should create a new epic."
            )
        if milestone and milestone.locked:
            raise ValueError(
                f"Milestone {milestone.id} has been closed and cannot accept new tasks. The agent should create a new epic."
            )
        if epic.locked:
            raise ValueError(
                f"Epic {epic_id} has been closed and cannot accept new tasks. The agent should create a new epic."
            )

        # Determine next task number
        next_num = self._get_next_task_number(epic_dir)

        # Generate task ID
        task_short_id = f"T{next_num:03d}"
        full_task_id = f"{epic_id}.{task_short_id}"

        # Generate slug from title
        slug = self._slugify(task_data["title"])

        # Create filename
        filename = f"{task_short_id}-{slug}.todo"
        task_file = epic_dir / filename

        # Prepare frontmatter
        created_at = datetime.now(timezone.utc)
        frontmatter = {
            "id": full_task_id,
            "title": task_data["title"],
            "status": "pending",
            "estimate_hours": task_data.get("estimate_hours", 1.0),
            "complexity": task_data.get("complexity", "medium"),
            "priority": task_data.get("priority", "medium"),
            "depends_on": task_data.get("depends_on", []),
            "tags": task_data.get("tags", []),
            "created_at": created_at.isoformat(),
            "updated_at": created_at.isoformat(),
        }

        # Prepare body
        description = task_data.get("description", "")
        if task_data.get("body"):
            body = task_data["body"]
        else:
            body = f"""
# {task_data["title"]}

{description}

## Requirements

- TODO: Add requirements

## Acceptance Criteria

- TODO: Add acceptance criteria
"""

        # Write .todo file
        with open(task_file, "w") as f:
            f.write("---\n")
            yaml.dump(frontmatter, f, default_flow_style=False, sort_keys=False)
            f.write("---\n")
            f.write(body)

        # Update epic index.yaml to include the new task
        self._add_task_to_epic_index(epic_dir, task_short_id, filename, task_data)

        # Create and return Task object
        return Task(
            id=full_task_id,
            title=task_data["title"],
            file=str(task_file.relative_to(self.tasks_dir)),
            status=Status.PENDING,
            estimate_hours=float(task_data.get("estimate_hours", 1.0)),
            complexity=Complexity(task_data.get("complexity", "medium")),
            priority=Priority(task_data.get("priority", "medium")),
            depends_on=task_data.get("depends_on", []),
            created_at=created_at,
            updated_at=created_at,
            tags=task_data.get("tags", []),
            epic_id=epic_id,
            milestone_id=epic_path.milestone_id,
            phase_id=epic_path.phase,
        )

    def create_epic(self, milestone_id: str, epic_data: dict) -> Epic:
        """
        Create a new epic in the specified milestone.

        Args:
            milestone_id: Fully qualified milestone ID (e.g., "P5.M04")
            epic_data: Dict with keys: name, estimate_hours, complexity, depends_on, description

        Returns:
            The created Epic object

        Raises:
            ValueError: If milestone not found or invalid data
        """
        # Parse the milestone path
        ms_path = TaskPath.parse(milestone_id)
        if not ms_path.is_milestone:
            raise ValueError(f"Invalid milestone ID: {milestone_id}")

        # Find the milestone directory
        milestone_dir = self._find_milestone_dir(ms_path)
        if not milestone_dir:
            raise ValueError(f"Milestone directory not found for: {milestone_id}")
        tree = self.load("metadata")
        milestone = tree.find_milestone(milestone_id)
        if not milestone:
            raise ValueError(f"Milestone not found: {milestone_id}")
        phase = tree.find_phase(milestone.phase_id or "")
        if phase and phase.locked:
            raise ValueError(
                f"Phase {phase.id} has been closed and cannot accept new milestones. Create a new phase."
            )
        if milestone.locked:
            raise ValueError(
                f"Milestone {milestone_id} has been closed and cannot accept new epics. The agent should create a new epic."
            )

        # Determine next epic number
        next_num = self._get_next_epic_number(milestone_dir)

        # Generate epic ID
        epic_short_id = f"E{next_num}"
        full_epic_id = f"{milestone_id}.{epic_short_id}"

        # Generate slug from name
        slug = self._slugify(epic_data["name"])

        # Create directory name with zero-padded number
        dir_name = f"{next_num:02d}-{slug}"
        epic_dir = milestone_dir / dir_name

        # Create epic directory
        epic_dir.mkdir(parents=True, exist_ok=True)

        # Create epic index.yaml
        epic_index = {
            "id": full_epic_id,
            "name": epic_data["name"],
            "status": "pending",
            "estimate_hours": epic_data.get("estimate_hours", 4.0),
            "complexity": epic_data.get("complexity", "medium"),
            "depends_on": epic_data.get("depends_on", []),
            "locked": False,
            "tasks": [],
            "stats": {
                "total": 0,
                "done": 0,
                "in_progress": 0,
                "blocked": 0,
                "pending": 0,
            },
        }

        # Add description as a comment at the top
        epic_index_path = epic_dir / "index.yaml"
        with open(epic_index_path, "w") as f:
            f.write(f"# Epic: {epic_data['name']}\n")
            f.write(f"# {milestone_id}, Epic {next_num} ({full_epic_id})\n\n")
            yaml.dump(epic_index, f, default_flow_style=False, sort_keys=False)

        # Update milestone index.yaml to include the new epic
        self._add_epic_to_milestone_index(
            milestone_dir, epic_short_id, dir_name, epic_data
        )

        # Create and return Epic object
        epic_path = ms_path.with_epic(epic_short_id)
        return Epic(
            id=full_epic_id,
            name=epic_data["name"],
            path=dir_name,
            status=Status.PENDING,
            estimate_hours=float(epic_data.get("estimate_hours", 4.0)),
            complexity=Complexity(epic_data.get("complexity", "medium")),
            depends_on=epic_data.get("depends_on", []),
            description=epic_data.get("description"),
            milestone_id=milestone_id,
            phase_id=ms_path.phase,
            locked=False,
        )

    def create_milestone(self, phase_id: str, milestone_data: dict):
        """
        Create a new milestone in the specified phase.

        Args:
            phase_id: Phase ID (e.g., "P1")
            milestone_data: Dict with keys: name, estimate_hours, complexity,
                            depends_on, description

        Returns:
            The created Milestone object

        Raises:
            ValueError: If phase not found or invalid data
        """
        # Parse and validate phase_id
        phase_path = TaskPath.parse(phase_id)
        if not phase_path.is_phase:
            raise ValueError(f"Invalid phase ID: {phase_id}")

        # Find the phase directory
        phase_dir = self._find_phase_dir(phase_path)
        if not phase_dir:
            raise ValueError(f"Phase directory not found for: {phase_id}")
        tree = self.load("metadata")
        phase = tree.find_phase(phase_id)
        if not phase:
            raise ValueError(f"Phase not found: {phase_id}")
        if phase.locked:
            raise ValueError(
                f"Phase {phase_id} has been closed and cannot accept new milestones. Create a new phase."
            )

        # Determine next milestone number
        next_num = self._get_next_milestone_number(phase_dir)

        # Generate milestone ID (NOT zero-padded)
        milestone_short_id = f"M{next_num}"
        full_milestone_id = f"{phase_id}.{milestone_short_id}"

        # Generate slug from name
        slug = self._slugify(milestone_data["name"])

        # Create directory name with zero-padded number
        dir_name = f"{next_num:02d}-{slug}"
        milestone_dir = phase_dir / dir_name

        # Create milestone directory
        milestone_dir.mkdir(parents=True, exist_ok=True)

        # Create milestone index.yaml
        milestone_index = {
            "id": full_milestone_id,
            "name": milestone_data["name"],
            "status": "pending",
            "estimate_hours": milestone_data.get("estimate_hours", 8.0),
            "complexity": milestone_data.get("complexity", "medium"),
            "depends_on": milestone_data.get("depends_on", []),
            "locked": False,
            "epics": [],
            "stats": {
                "total_tasks": 0,
                "done": 0,
                "in_progress": 0,
                "blocked": 0,
                "pending": 0,
            },
        }

        milestone_index_path = milestone_dir / "index.yaml"
        with open(milestone_index_path, "w") as f:
            f.write(f"# Milestone: {milestone_data['name']}\n")
            f.write(f"# {phase_id}, Milestone {next_num} ({full_milestone_id})\n\n")
            yaml.dump(milestone_index, f, default_flow_style=False, sort_keys=False)

        # Update phase index.yaml
        self._add_milestone_to_phase_index(
            phase_dir, milestone_short_id, dir_name, milestone_data
        )

        # Create and return Milestone object
        from .models import Milestone

        return Milestone(
            id=full_milestone_id,
            name=milestone_data["name"],
            path=dir_name,
            status=Status.PENDING,
            estimate_hours=float(milestone_data.get("estimate_hours", 8.0)),
            complexity=Complexity(milestone_data.get("complexity", "medium")),
            depends_on=milestone_data.get("depends_on", []),
            description=milestone_data.get("description"),
            phase_id=phase_id,
            locked=False,
        )

    def create_phase(self, phase_data: dict):
        """
        Create a new phase in the project.

        Args:
            phase_data: Dict with keys: name, weeks, estimate_hours, priority,
                        depends_on, description

        Returns:
            The created Phase object

        Raises:
            ValueError: If invalid data or directory conflict
        """
        # Determine next phase number
        next_num = self._get_next_phase_number()

        # Generate phase ID (NOT zero-padded)
        phase_id = f"P{next_num}"

        # Generate slug from name
        slug = self._slugify(phase_data["name"])

        # Create directory name with zero-padded number
        dir_name = f"{next_num:02d}-{slug}"
        phase_dir = self.tasks_dir / dir_name

        # Check for directory conflicts
        if phase_dir.exists():
            raise ValueError(f"Phase directory already exists: {dir_name}")

        # Create phase directory
        phase_dir.mkdir(parents=True, exist_ok=True)

        # Create phase index.yaml
        phase_index = {
            "id": phase_id,
            "name": phase_data["name"],
            "status": "pending",
            "locked": False,
            "weeks": phase_data.get("weeks", 2),
            "estimate_hours": phase_data.get("estimate_hours", 40.0),
            "complexity": "medium",
            "depends_on": phase_data.get("depends_on", []),
            "milestones": [],
            "stats": {
                "total_tasks": 0,
                "done": 0,
                "in_progress": 0,
                "blocked": 0,
                "pending": 0,
            },
        }

        phase_index_path = phase_dir / "index.yaml"
        with open(phase_index_path, "w") as f:
            f.write(f"# Phase: {phase_data['name']}\n")
            f.write(f"# Phase {next_num} ({phase_id})\n\n")
            yaml.dump(phase_index, f, default_flow_style=False, sort_keys=False)

        # Update root index.yaml
        self._add_phase_to_root_index(phase_id, dir_name, phase_data)

        # Create and return Phase object
        from .models import Phase, Priority

        return Phase(
            id=phase_id,
            name=phase_data["name"],
            path=dir_name,
            status=Status.PENDING,
            weeks=phase_data.get("weeks", 2),
            estimate_hours=float(phase_data.get("estimate_hours", 40.0)),
            priority=Priority(phase_data.get("priority", "medium")),
            depends_on=phase_data.get("depends_on", []),
            description=phase_data.get("description"),
            locked=False,
        )

    def _find_epic_dir(self, epic_path: TaskPath) -> Optional[Path]:
        """Find the directory for an epic given its TaskPath."""
        # First find the phase directory
        tree = self.load("metadata")
        phase = tree.find_phase(epic_path.phase)
        if not phase:
            return None

        # Find the milestone
        milestone_id = epic_path.milestone_id
        if not milestone_id:
            return None
        milestone = tree.find_milestone(milestone_id)
        if not milestone:
            return None

        # Find the epic
        epic = tree.find_epic(epic_path.full_id)
        if not epic:
            return None

        # Build the full path
        return self.tasks_dir / phase.path / milestone.path / epic.path

    def _find_milestone_dir(self, ms_path: TaskPath) -> Optional[Path]:
        """Find the directory for a milestone given its TaskPath."""
        tree = self.load("metadata")
        phase = tree.find_phase(ms_path.phase)
        if not phase:
            return None

        milestone = tree.find_milestone(ms_path.full_id)
        if not milestone:
            return None

        return self.tasks_dir / phase.path / milestone.path

    def _get_next_task_number(self, epic_dir: Path) -> int:
        """Get the next task number for an epic."""
        existing_tasks = list(epic_dir.glob("T*.todo"))
        if not existing_tasks:
            return 1

        max_num = 0
        for task_file in existing_tasks:
            match = re.match(r"T(\d+)", task_file.name)
            if match:
                num = int(match.group(1))
                if num > max_num:
                    max_num = num

        return max_num + 1

    def _get_next_epic_number(self, milestone_dir: Path) -> int:
        """Get the next epic number for a milestone."""
        # Read the milestone index to find existing epics
        index_path = milestone_dir / "index.yaml"
        if not index_path.exists():
            return 1

        index = self._load_yaml(index_path)
        epics = index.get("epics", [])
        if not epics:
            return 1

        max_num = 0
        for epic in epics:
            epic_id = epic.get("id", "")
            match = re.search(r"(?:^|\.)E(\d+)$", epic_id)
            if match:
                num = int(match.group(1))
                if num > max_num:
                    max_num = num

        return max_num + 1

    def _find_phase_dir(self, phase_path: TaskPath) -> Optional[Path]:
        """Find the directory for a phase given its TaskPath."""
        tree = self.load("metadata")
        phase = tree.find_phase(phase_path.phase)
        if not phase:
            return None
        return self.tasks_dir / phase.path

    def _get_next_milestone_number(self, phase_dir: Path) -> int:
        """Get the next milestone number for a phase."""
        index_path = phase_dir / "index.yaml"
        if not index_path.exists():
            return 1

        index = self._load_yaml(index_path)
        milestones = index.get("milestones", [])
        if not milestones:
            return 1

        max_num = 0
        for milestone in milestones:
            milestone_id = milestone.get("id", "")
            match = re.search(r"(?:^|\.)M(\d+)$", milestone_id)
            if match:
                num = int(match.group(1))
                if num > max_num:
                    max_num = num

        return max_num + 1

    def _get_next_phase_number(self) -> int:
        """Get the next phase number from root index."""
        root_index_path = self.tasks_dir / "index.yaml"
        if not root_index_path.exists():
            return 1

        index = self._load_yaml(root_index_path)
        phases = index.get("phases", [])
        if not phases:
            return 1

        max_num = 0
        for phase in phases:
            phase_id = phase.get("id", "")
            match = re.match(r"P(\d+)", phase_id)
            if match:
                num = int(match.group(1))
                if num > max_num:
                    max_num = num

        return max_num + 1

    def _add_milestone_to_phase_index(
        self,
        phase_dir: Path,
        milestone_short_id: str,
        dir_name: str,
        milestone_data: dict,
    ) -> None:
        """Add a milestone entry to the phase's index.yaml."""
        index_path = phase_dir / "index.yaml"

        if not index_path.exists():
            raise ValueError(f"Phase index not found: {index_path}")

        index = self._load_yaml(index_path)

        milestone_entry = {
            "id": milestone_short_id,
            "name": milestone_data["name"],
            "path": dir_name,
            "status": "pending",
            "estimate_hours": milestone_data.get("estimate_hours", 8.0),
            "complexity": milestone_data.get("complexity", "medium"),
            "depends_on": milestone_data.get("depends_on", []),
            "description": milestone_data.get("description", ""),
            "locked": False,
        }

        if "milestones" not in index:
            index["milestones"] = []
        index["milestones"].append(milestone_entry)

        with open(index_path, "w") as f:
            yaml.dump(index, f, default_flow_style=False, sort_keys=False)

    def _add_phase_to_root_index(
        self, phase_id: str, dir_name: str, phase_data: dict
    ) -> None:
        """Add a phase entry to the root index.yaml."""
        root_index_path = self.tasks_dir / "index.yaml"

        if not root_index_path.exists():
            raise ValueError(f"Root index not found: {root_index_path}")

        index = self._load_yaml(root_index_path)

        phase_entry = {
            "id": phase_id,
            "name": phase_data["name"],
            "path": dir_name,
            "status": "pending",
            "weeks": phase_data.get("weeks", 2),
            "estimate_hours": phase_data.get("estimate_hours", 40.0),
            "priority": phase_data.get("priority", "medium"),
            "depends_on": phase_data.get("depends_on", []),
            "description": phase_data.get("description", ""),
            "locked": False,
        }

        if "phases" not in index:
            index["phases"] = []
        index["phases"].append(phase_entry)

        with open(root_index_path, "w") as f:
            yaml.dump(index, f, default_flow_style=False, sort_keys=False)

    def _slugify(self, text: str, max_length: int = 30) -> str:
        """Convert text to a slug for filenames."""
        # Convert to lowercase
        slug = text.lower()
        # Replace spaces and special chars with hyphens
        slug = re.sub(r"[^a-z0-9]+", "-", slug)
        # Remove leading/trailing hyphens
        slug = slug.strip("-")
        # Truncate to max_length
        if len(slug) > max_length:
            slug = slug[:max_length].rstrip("-")
        return slug

    def _fallback_aux_title(self, filename: str, task_id: str = "") -> str:
        """Build a readable aux title from filename when title metadata is absent."""
        stem = Path(filename).stem
        if task_id:
            prefixes = [task_id, task_id.upper(), task_id.lower()]
            for prefix in prefixes:
                if stem.startswith(prefix):
                    stem = stem[len(prefix) :]
                    stem = stem.lstrip("-_")
                    break
        stem = stem.replace("-", " ").replace("_", " ")
        stem = " ".join(stem.split()).strip()
        if stem:
            return stem
        return task_id or "untitled"

    def _add_task_to_epic_index(
        self, epic_dir: Path, task_short_id: str, filename: str, task_data: dict
    ) -> None:
        """Add a task entry to the epic's index.yaml."""
        index_path = epic_dir / "index.yaml"

        if index_path.exists():
            index = self._load_yaml(index_path)
        else:
            # Create minimal epic index if it doesn't exist
            index = {"tasks": [], "stats": {}}

        # Add task to list
        task_entry = {
            "id": task_short_id,
            "file": filename,
            "title": task_data["title"],
            "status": "pending",
            "estimate_hours": task_data.get("estimate_hours", 1.0),
            "complexity": task_data.get("complexity", "medium"),
            "priority": task_data.get("priority", "medium"),
            "depends_on": task_data.get("depends_on", []),
        }

        if "tasks" not in index:
            index["tasks"] = []
        index["tasks"].append(task_entry)

        # Update stats
        if "stats" not in index:
            index["stats"] = {}
        stats = index["stats"]
        stats["total"] = stats.get("total", 0) + 1
        stats["pending"] = stats.get("pending", 0) + 1

        # Write back
        with open(index_path, "w") as f:
            yaml.dump(index, f, default_flow_style=False, sort_keys=False)

    def _add_epic_to_milestone_index(
        self, milestone_dir: Path, epic_short_id: str, dir_name: str, epic_data: dict
    ) -> None:
        """Add an epic entry to the milestone's index.yaml."""
        index_path = milestone_dir / "index.yaml"

        if not index_path.exists():
            raise ValueError(f"Milestone index not found: {index_path}")

        index = self._load_yaml(index_path)

        # Add epic to list
        epic_entry = {
            "id": epic_short_id,
            "name": epic_data["name"],
            "path": dir_name,
            "status": "pending",
            "estimate_hours": epic_data.get("estimate_hours", 4.0),
            "complexity": epic_data.get("complexity", "medium"),
            "depends_on": epic_data.get("depends_on", []),
            "description": epic_data.get("description", ""),
            "locked": False,
        }

        if "epics" not in index:
            index["epics"] = []
        index["epics"].append(epic_entry)

        # Write back
        with open(index_path, "w") as f:
            yaml.dump(index, f, default_flow_style=False, sort_keys=False)

    def _write_yaml(self, filepath: Path, data: Dict[str, Any]) -> None:
        """Write a YAML dictionary preserving key order."""
        with open(filepath, "w") as f:
            yaml.dump(data, f, default_flow_style=False, sort_keys=False)

    def _leaf_id(self, full_id: str) -> str:
        """Return the leaf token of a hierarchical ID."""
        return full_id.split(".")[-1]

    def _replace_mapped_values(self, value: Any, remap: Dict[str, str]) -> Any:
        """Recursively replace ID values in selected YAML shapes."""
        if isinstance(value, str):
            return remap.get(value, value)
        if isinstance(value, list):
            return [self._replace_mapped_values(v, remap) for v in value]
        if isinstance(value, dict):
            out = {}
            for k, v in value.items():
                if k in {
                    "id",
                    "depends_on",
                    "current_task",
                    "primary_task",
                    "additional_tasks",
                    "sibling_tasks",
                }:
                    out[k] = self._replace_mapped_values(v, remap)
                else:
                    out[k] = self._replace_mapped_values(v, remap)
            return out
        return value

    def _apply_id_remap(self, remap: Dict[str, str]) -> None:
        """Apply ID remapping to all YAML and todo frontmatter files."""
        if not remap:
            return

        # Update YAML files
        for yaml_path in self.tasks_dir.rglob("index.yaml"):
            data = self._load_yaml(yaml_path)
            updated = self._replace_mapped_values(data, remap)
            self._write_yaml(yaml_path, updated)

        for runtime_name in [".context.yaml", ".sessions.yaml"]:
            runtime_path = self.tasks_dir / runtime_name
            if runtime_path.exists():
                data = self._load_yaml(runtime_path)
                updated = self._replace_mapped_values(data, remap)
                self._write_yaml(runtime_path, updated)

        # Update task/bug/idea file frontmatter
        for todo_path in self.tasks_dir.rglob("*.todo"):
            frontmatter, body = self._parse_todo_file(todo_path)
            updated = self._replace_mapped_values(frontmatter, remap)
            with open(todo_path, "w") as f:
                f.write("---\n")
                yaml.dump(updated, f, default_flow_style=False, sort_keys=False)
                f.write("---\n")
                f.write(body)

    def _next_epic_number_for_milestone(self, milestone_dir: Path) -> int:
        """Find the next epic number for a milestone directory."""
        return self._get_next_epic_number(milestone_dir)

    def _next_milestone_number_for_phase(self, phase_dir: Path) -> int:
        """Find the next milestone number for a phase directory."""
        return self._get_next_milestone_number(phase_dir)

    def move_item(self, source_id: str, dest_id: str) -> Dict[str, Any]:
        """Move task->epic, epic->milestone, or milestone->phase with renumbering."""
        src_path = TaskPath.parse(source_id)
        dst_path = TaskPath.parse(dest_id)
        tree = self.load("metadata")

        remap: Dict[str, str] = {}

        if src_path.is_task and dst_path.is_epic:
            src_task = tree.find_task(source_id)
            if not src_task:
                raise ValueError(f"Task not found: {source_id}")
            dst_epic = tree.find_epic(dest_id)
            if not dst_epic:
                raise ValueError(f"Epic not found: {dest_id}")

            src_epic = tree.find_epic(src_task.epic_id or "")
            src_milestone = tree.find_milestone(src_task.milestone_id or "")
            src_phase = tree.find_phase(src_task.phase_id or "")
            dst_milestone = tree.find_milestone(dst_epic.milestone_id or "")
            dst_phase = tree.find_phase(dst_epic.phase_id or "")
            if not all([src_epic, src_milestone, src_phase, dst_milestone, dst_phase]):
                raise ValueError("Could not resolve source/destination hierarchy paths")

            src_epic_dir = (
                self.tasks_dir / src_phase.path / src_milestone.path / src_epic.path
            )
            dst_epic_dir = (
                self.tasks_dir / dst_phase.path / dst_milestone.path / dst_epic.path
            )

            old_filename = Path(src_task.file).name
            old_file = src_epic_dir / old_filename
            if not old_file.exists():
                raise FileNotFoundError(f"Task file not found: {old_file}")

            next_num = self._get_next_task_number(dst_epic_dir)
            new_short = f"T{next_num:03d}"
            new_id = f"{dst_epic.id}.{new_short}"
            new_filename = f"{new_short}-{self._slugify(src_task.title)}.todo"
            new_file = dst_epic_dir / new_filename

            shutil.move(str(old_file), str(new_file))

            # Update source epic index
            src_index_path = src_epic_dir / "index.yaml"
            src_index = (
                self._load_yaml(src_index_path)
                if src_index_path.exists()
                else {"tasks": []}
            )
            src_tasks = []
            old_leaf = self._leaf_id(source_id)
            for entry in src_index.get("tasks", []):
                if isinstance(entry, str):
                    if entry == old_filename:
                        continue
                    src_tasks.append(entry)
                    continue
                entry_file = entry.get("file") or entry.get("path")
                entry_id = str(entry.get("id", ""))
                entry_leaf = self._leaf_id(entry_id) if entry_id else ""
                if (
                    entry_file == old_filename
                    or entry_id == source_id
                    or entry_leaf == old_leaf
                ):
                    continue
                src_tasks.append(entry)
            src_index["tasks"] = src_tasks
            self._write_yaml(src_index_path, src_index)

            # Update destination epic index
            dst_index_path = dst_epic_dir / "index.yaml"
            dst_index = (
                self._load_yaml(dst_index_path)
                if dst_index_path.exists()
                else {"tasks": []}
            )
            dst_tasks = list(dst_index.get("tasks", []))
            dst_tasks.append(
                {
                    "id": new_short,
                    "file": new_filename,
                    "title": src_task.title,
                    "status": src_task.status.value,
                    "estimate_hours": src_task.estimate_hours,
                    "complexity": src_task.complexity.value,
                    "priority": src_task.priority.value,
                    "depends_on": src_task.depends_on,
                }
            )
            dst_index["tasks"] = dst_tasks
            self._write_yaml(dst_index_path, dst_index)

            remap[source_id] = new_id

        elif src_path.is_epic and dst_path.is_milestone:
            src_epic = tree.find_epic(source_id)
            if not src_epic:
                raise ValueError(f"Epic not found: {source_id}")
            dst_milestone = tree.find_milestone(dest_id)
            if not dst_milestone:
                raise ValueError(f"Milestone not found: {dest_id}")

            src_milestone = tree.find_milestone(src_epic.milestone_id or "")
            src_phase = tree.find_phase(src_epic.phase_id or "")
            dst_phase = tree.find_phase(dst_milestone.phase_id or "")
            if not all([src_milestone, src_phase, dst_phase]):
                raise ValueError("Could not resolve source/destination hierarchy paths")

            src_milestone_dir = self.tasks_dir / src_phase.path / src_milestone.path
            dst_milestone_dir = self.tasks_dir / dst_phase.path / dst_milestone.path
            src_epic_dir = src_milestone_dir / src_epic.path

            next_num = self._next_epic_number_for_milestone(dst_milestone_dir)
            new_short_epic = f"E{next_num}"
            new_epic_id = f"{dst_milestone.id}.{new_short_epic}"
            new_epic_dir_name = f"{next_num:02d}-{self._slugify(src_epic.name)}"
            dst_epic_dir = dst_milestone_dir / new_epic_dir_name

            shutil.move(str(src_epic_dir), str(dst_epic_dir))

            # Update source milestone index
            src_ms_index_path = src_milestone_dir / "index.yaml"
            src_ms_index = self._load_yaml(src_ms_index_path)
            src_epics = []
            old_leaf = self._leaf_id(source_id)
            for entry in src_ms_index.get("epics", []):
                entry_id = str(entry.get("id", "")) if isinstance(entry, dict) else ""
                entry_path = entry.get("path") if isinstance(entry, dict) else None
                entry_leaf = self._leaf_id(entry_id) if entry_id else ""
                if (
                    entry_id == source_id
                    or entry_leaf == old_leaf
                    or entry_path == src_epic.path
                ):
                    continue
                src_epics.append(entry)
            src_ms_index["epics"] = src_epics
            self._write_yaml(src_ms_index_path, src_ms_index)

            # Update destination milestone index
            dst_ms_index_path = dst_milestone_dir / "index.yaml"
            dst_ms_index = self._load_yaml(dst_ms_index_path)
            dst_epics = list(dst_ms_index.get("epics", []))
            dst_epics.append(
                {
                    "id": new_short_epic,
                    "name": src_epic.name,
                    "path": new_epic_dir_name,
                    "status": src_epic.status.value,
                    "estimate_hours": src_epic.estimate_hours,
                    "complexity": src_epic.complexity.value,
                    "depends_on": src_epic.depends_on,
                    "description": src_epic.description or "",
                }
            )
            dst_ms_index["epics"] = dst_epics
            self._write_yaml(dst_ms_index_path, dst_ms_index)

            # Build remap for epic and descendant tasks
            old_prefix = source_id + "."
            new_prefix = new_epic_id + "."
            remap[source_id] = new_epic_id
            for task in src_epic.tasks:
                if task.id.startswith(old_prefix):
                    remap[task.id] = task.id.replace(old_prefix, new_prefix, 1)

        elif src_path.is_milestone and dst_path.is_phase:
            src_milestone = tree.find_milestone(source_id)
            if not src_milestone:
                raise ValueError(f"Milestone not found: {source_id}")
            dst_phase = tree.find_phase(dest_id)
            if not dst_phase:
                raise ValueError(f"Phase not found: {dest_id}")

            src_phase = tree.find_phase(src_milestone.phase_id or "")
            if not src_phase:
                raise ValueError("Could not resolve source phase")

            src_phase_dir = self.tasks_dir / src_phase.path
            dst_phase_dir = self.tasks_dir / dst_phase.path
            src_ms_dir = src_phase_dir / src_milestone.path

            next_num = self._next_milestone_number_for_phase(dst_phase_dir)
            new_short_ms = f"M{next_num}"
            new_ms_id = f"{dst_phase.id}.{new_short_ms}"
            new_ms_dir_name = f"{next_num:02d}-{self._slugify(src_milestone.name)}"
            dst_ms_dir = dst_phase_dir / new_ms_dir_name

            shutil.move(str(src_ms_dir), str(dst_ms_dir))

            # Update source phase index
            src_phase_index_path = src_phase_dir / "index.yaml"
            src_phase_index = self._load_yaml(src_phase_index_path)
            src_milestones = []
            old_leaf = self._leaf_id(source_id)
            for entry in src_phase_index.get("milestones", []):
                entry_id = str(entry.get("id", "")) if isinstance(entry, dict) else ""
                entry_path = entry.get("path") if isinstance(entry, dict) else None
                entry_leaf = self._leaf_id(entry_id) if entry_id else ""
                if (
                    entry_id == source_id
                    or entry_leaf == old_leaf
                    or entry_path == src_milestone.path
                ):
                    continue
                src_milestones.append(entry)
            src_phase_index["milestones"] = src_milestones
            self._write_yaml(src_phase_index_path, src_phase_index)

            # Update destination phase index
            dst_phase_index_path = dst_phase_dir / "index.yaml"
            dst_phase_index = self._load_yaml(dst_phase_index_path)
            dst_milestones = list(dst_phase_index.get("milestones", []))
            dst_milestones.append(
                {
                    "id": new_short_ms,
                    "name": src_milestone.name,
                    "path": new_ms_dir_name,
                    "status": src_milestone.status.value,
                    "estimate_hours": src_milestone.estimate_hours,
                    "complexity": src_milestone.complexity.value,
                    "depends_on": src_milestone.depends_on,
                    "description": src_milestone.description or "",
                }
            )
            dst_phase_index["milestones"] = dst_milestones
            self._write_yaml(dst_phase_index_path, dst_phase_index)

            # Build remap for milestone, epics, and descendant tasks
            old_ms_prefix = source_id + "."
            new_ms_prefix = new_ms_id + "."
            remap[source_id] = new_ms_id
            for epic in src_milestone.epics:
                new_epic_id = epic.id.replace(old_ms_prefix, new_ms_prefix, 1)
                remap[epic.id] = new_epic_id
                old_epic_prefix = epic.id + "."
                new_epic_prefix = new_epic_id + "."
                for task in epic.tasks:
                    if task.id.startswith(old_epic_prefix):
                        remap[task.id] = task.id.replace(
                            old_epic_prefix, new_epic_prefix, 1
                        )

        else:
            raise ValueError(
                "Invalid move: supported moves are task->epic, epic->milestone, milestone->phase"
            )

        # Apply remap across all files and metadata
        self._apply_id_remap(remap)

        new_id = remap.get(source_id, source_id)
        return {
            "source_id": source_id,
            "dest_id": dest_id,
            "new_id": new_id,
            "remapped_ids": remap,
        }

    def set_item_locked(self, item_id: str, locked: bool) -> str:
        """Set lock state for phase/milestone/epic in both list and local index files."""
        path = TaskPath.parse(item_id)
        tree = self.load("metadata")
        desired = bool(locked)

        if path.is_phase:
            phase = tree.find_phase(path.full_id)
            if not phase:
                raise ValueError(f"Phase not found: {item_id}")
            root_path = self.tasks_dir / "index.yaml"
            root = self._load_yaml(root_path)
            for entry in root.get("phases", []):
                if entry.get("id") in {phase.id, path.phase}:
                    entry["locked"] = desired
                    break
            self._write_yaml(root_path, root)
            phase_index_path = self.tasks_dir / phase.path / "index.yaml"
            if phase_index_path.exists():
                phase_index = self._load_yaml(phase_index_path)
                phase_index["locked"] = desired
                self._write_yaml(phase_index_path, phase_index)
            return phase.id

        if path.is_milestone:
            milestone = tree.find_milestone(path.full_id)
            if not milestone:
                raise ValueError(f"Milestone not found: {item_id}")
            phase = tree.find_phase(milestone.phase_id or "")
            if not phase:
                raise ValueError(f"Phase not found for milestone: {item_id}")
            phase_index_path = self.tasks_dir / phase.path / "index.yaml"
            phase_index = self._load_yaml(phase_index_path)
            for entry in phase_index.get("milestones", []):
                if entry.get("id") in {milestone.id, path.milestone}:
                    entry["locked"] = desired
                    break
            self._write_yaml(phase_index_path, phase_index)
            ms_index_path = self.tasks_dir / phase.path / milestone.path / "index.yaml"
            if ms_index_path.exists():
                ms_index = self._load_yaml(ms_index_path)
                ms_index["locked"] = desired
                self._write_yaml(ms_index_path, ms_index)
            return milestone.id

        if path.is_epic:
            epic = tree.find_epic(path.full_id)
            if not epic:
                raise ValueError(f"Epic not found: {item_id}")
            milestone = tree.find_milestone(epic.milestone_id or "")
            phase = tree.find_phase(epic.phase_id or "")
            if not milestone or not phase:
                raise ValueError(f"Could not resolve parent paths for epic: {item_id}")
            ms_index_path = self.tasks_dir / phase.path / milestone.path / "index.yaml"
            ms_index = self._load_yaml(ms_index_path)
            for entry in ms_index.get("epics", []):
                if entry.get("id") in {epic.id, path.epic}:
                    entry["locked"] = desired
                    break
            self._write_yaml(ms_index_path, ms_index)
            epic_index_path = (
                self.tasks_dir / phase.path / milestone.path / epic.path / "index.yaml"
            )
            if epic_index_path.exists():
                epic_index = self._load_yaml(epic_index_path)
                epic_index["locked"] = desired
                self._write_yaml(epic_index_path, epic_index)
            return epic.id

        raise ValueError("lock/unlock supports only phase, milestone, or epic IDs")

    def set_item_not_done(self, item_id: str) -> dict:
        """Mark a task or hierarchy item as not done (pending)."""
        tree = self.load("metadata")

        def reset_task(task: Task) -> None:
            task.status = Status.PENDING
            task.claimed_by = None
            task.claimed_at = None
            task.started_at = None
            task.completed_at = None
            task.duration_minutes = None
            self.save_task(task)

        # Handle direct task IDs first (includes bugs/ideas)
        task = tree.find_task(item_id)
        if task:
            reset_task(task)
            return {"item_id": task.id, "updated_tasks": 1}

        path = TaskPath.parse(item_id)

        if path.is_phase:
            phase = tree.find_phase(path.full_id)
            if not phase:
                raise ValueError(f"Phase not found: {item_id}")

            root_path = self.tasks_dir / "index.yaml"
            root = self._load_yaml(root_path)
            for entry in root.get("phases", []):
                if entry.get("id") in {phase.id, path.phase}:
                    entry["status"] = Status.PENDING.value
                    break
            self._write_yaml(root_path, root)

            phase_index_path = self.tasks_dir / phase.path / "index.yaml"
            if phase_index_path.exists():
                phase_index = self._load_yaml(phase_index_path)
                phase_index["status"] = Status.PENDING.value
                for entry in phase_index.get("milestones", []):
                    entry["status"] = Status.PENDING.value
                self._write_yaml(phase_index_path, phase_index)

            updated = 0
            for milestone in phase.milestones:
                ms_index_path = self.tasks_dir / phase.path / milestone.path / "index.yaml"
                if ms_index_path.exists():
                    ms_index = self._load_yaml(ms_index_path)
                    ms_index["status"] = Status.PENDING.value
                    for entry in ms_index.get("epics", []):
                        entry["status"] = Status.PENDING.value
                    self._write_yaml(ms_index_path, ms_index)

                for epic in milestone.epics:
                    epic_index_path = (
                        self.tasks_dir / phase.path / milestone.path / epic.path / "index.yaml"
                    )
                    if epic_index_path.exists():
                        epic_index = self._load_yaml(epic_index_path)
                        epic_index["status"] = Status.PENDING.value
                        self._write_yaml(epic_index_path, epic_index)
                    for t in epic.tasks:
                        reset_task(t)
                        updated += 1
            return {"item_id": phase.id, "updated_tasks": updated}

        if path.is_milestone:
            milestone = tree.find_milestone(path.full_id)
            if not milestone:
                raise ValueError(f"Milestone not found: {item_id}")
            phase = tree.find_phase(milestone.phase_id or "")
            if not phase:
                raise ValueError(f"Phase not found for milestone: {item_id}")

            phase_index_path = self.tasks_dir / phase.path / "index.yaml"
            phase_index = self._load_yaml(phase_index_path)
            for entry in phase_index.get("milestones", []):
                if entry.get("id") in {milestone.id, path.milestone}:
                    entry["status"] = Status.PENDING.value
                    break
            self._write_yaml(phase_index_path, phase_index)

            ms_index_path = self.tasks_dir / phase.path / milestone.path / "index.yaml"
            if ms_index_path.exists():
                ms_index = self._load_yaml(ms_index_path)
                ms_index["status"] = Status.PENDING.value
                for entry in ms_index.get("epics", []):
                    entry["status"] = Status.PENDING.value
                self._write_yaml(ms_index_path, ms_index)

            updated = 0
            for epic in milestone.epics:
                epic_index_path = (
                    self.tasks_dir / phase.path / milestone.path / epic.path / "index.yaml"
                )
                if epic_index_path.exists():
                    epic_index = self._load_yaml(epic_index_path)
                    epic_index["status"] = Status.PENDING.value
                    self._write_yaml(epic_index_path, epic_index)
                for t in epic.tasks:
                    reset_task(t)
                    updated += 1
            return {"item_id": milestone.id, "updated_tasks": updated}

        if path.is_epic:
            epic = tree.find_epic(path.full_id)
            if not epic:
                raise ValueError(f"Epic not found: {item_id}")
            milestone = tree.find_milestone(epic.milestone_id or "")
            phase = tree.find_phase(epic.phase_id or "")
            if not milestone or not phase:
                raise ValueError(f"Could not resolve parent paths for epic: {item_id}")

            ms_index_path = self.tasks_dir / phase.path / milestone.path / "index.yaml"
            ms_index = self._load_yaml(ms_index_path)
            for entry in ms_index.get("epics", []):
                if entry.get("id") in {epic.id, path.epic}:
                    entry["status"] = Status.PENDING.value
                    break
            self._write_yaml(ms_index_path, ms_index)

            epic_index_path = (
                self.tasks_dir / phase.path / milestone.path / epic.path / "index.yaml"
            )
            if epic_index_path.exists():
                epic_index = self._load_yaml(epic_index_path)
                epic_index["status"] = Status.PENDING.value
                self._write_yaml(epic_index_path, epic_index)

            updated = 0
            for t in epic.tasks:
                reset_task(t)
                updated += 1
            return {"item_id": epic.id, "updated_tasks": updated}

        raise ValueError("undone supports only task, phase, milestone, or epic IDs")

    def set_item_done(self, item_id: str) -> dict:
        """Mark a task as done and propagate done status to completed parents."""
        tree = self.load("metadata")

        task = tree.find_task(item_id)
        if not task:
            raise ValueError(f"Task not found: {item_id}")

        if not task.epic_id or not task.milestone_id or not task.phase_id:
            return {
                "epic_completed": False,
                "milestone_completed": False,
                "phase_completed": False,
                "phase_locked": False,
            }

        epic = tree.find_epic(task.epic_id)
        milestone = tree.find_milestone(task.milestone_id)
        phase = tree.find_phase(task.phase_id)
        if not epic or not milestone or not phase:
            raise ValueError(f"Could not resolve parent hierarchy for: {item_id}")

        epic_completed = epic.is_complete
        milestone_completed = milestone.is_complete
        phase_completed = phase.is_complete
        result = {
            "epic_completed": bool(epic_completed),
            "milestone_completed": bool(milestone_completed),
            "phase_completed": bool(phase_completed),
            "phase_locked": False,
        }

        def _matches_index_id(entry_id: object, target_id: str) -> bool:
            entry_id_str = str(entry_id)
            return (
                entry_id_str == target_id
                or self._leaf_id(entry_id_str) == self._leaf_id(target_id)
            )

        root_path = self.tasks_dir / "index.yaml"
        root_index = self._load_yaml(root_path)
        for entry in root_index.get("phases", []):
            if _matches_index_id(entry.get("id"), phase.id):
                if phase_completed:
                    entry["status"] = Status.DONE.value
                    entry["locked"] = True
                    result["phase_locked"] = True
                break

        self._write_yaml(root_path, root_index)

        phase_index_path = self.tasks_dir / phase.path / "index.yaml"
        if phase_index_path.exists():
            phase_index = self._load_yaml(phase_index_path)
            if phase_completed:
                phase_index["status"] = Status.DONE.value
                phase_index["locked"] = True
            if milestone_completed:
                for entry in phase_index.get("milestones", []):
                    if _matches_index_id(entry.get("id"), milestone.id):
                        entry["status"] = Status.DONE.value
                        break
            self._write_yaml(phase_index_path, phase_index)

        ms_index_path = self.tasks_dir / phase.path / milestone.path / "index.yaml"
        if ms_index_path.exists():
            milestone_index = self._load_yaml(ms_index_path)
            if milestone_completed:
                milestone_index["status"] = Status.DONE.value
            if epic_completed:
                for entry in milestone_index.get("epics", []):
                    if _matches_index_id(entry.get("id"), epic.id):
                        entry["status"] = Status.DONE.value
                        break
            self._write_yaml(ms_index_path, milestone_index)

        epic_index_path = self.tasks_dir / phase.path / milestone.path / epic.path / "index.yaml"
        if epic_index_path.exists():
            epic_index = self._load_yaml(epic_index_path)
            if epic_completed:
                epic_index["status"] = Status.DONE.value
            self._write_yaml(epic_index_path, epic_index)

        return result
