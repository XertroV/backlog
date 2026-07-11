"""Status transition validation and management."""

from typing import Optional, Tuple
from .models import Status, Task
from .time_utils import utc_now, to_utc


# Valid status transitions
TRANSITIONS = {
    Status.PENDING: [Status.IN_PROGRESS, Status.BLOCKED, Status.CANCELLED],
    Status.IN_PROGRESS: [Status.DONE, Status.BLOCKED, Status.REJECTED, Status.PENDING],
    Status.DONE: [Status.BLOCKED, Status.REJECTED],
    Status.BLOCKED: [Status.PENDING, Status.CANCELLED],
    Status.REJECTED: [Status.PENDING],
    Status.CANCELLED: [],  # Terminal state
}


class StatusError(Exception):
    """Status transition error."""

    def __init__(self, message: str, error_code: str, context: dict, suggestion: str):
        self.message = message
        self.error_code = error_code
        self.context = context
        self.suggestion = suggestion
        super().__init__(message)

    def to_dict(self):
        """Convert to JSON-serializable dict."""
        return {
            "error": self.error_code,
            "message": self.message,
            "context": self.context,
            "suggestion": self.suggestion,
        }


def validate_transition(current: Status, new: Status) -> Tuple[bool, Optional[str]]:
    """
    Validate status transition.

    Returns:
        (is_valid, error_message)
    """
    if new in TRANSITIONS.get(current, []):
        return True, None

    valid = TRANSITIONS.get(current, [])
    return (
        False,
        f"Cannot transition from '{current.value}' to '{new.value}'. Valid transitions: {[v.value for v in valid]}",
    )


def claim_task(task: Task, agent_id: str, force: bool = False) -> None:
    """
    Claim a task.

    Raises:
        StatusError: If claim fails
    """
    # Check if already claimed
    if task.claimed_by and not force:
        age_minutes = (
            (utc_now() - to_utc(task.claimed_at)).total_seconds() / 60
            if task.claimed_at
            else 0
        )

        raise StatusError(
            message=f"Task {task.id} is already claimed by {task.claimed_by}",
            error_code="ALREADY_CLAIMED",
            context={
                "task_id": task.id,
                "claimed_by": task.claimed_by,
                "claimed_at": task.claimed_at.isoformat() if task.claimed_at else None,
                "claimed_duration_minutes": int(age_minutes),
                "stale_warning": age_minutes > 60,
            },
            suggestion=f"Use 'backlog claim {task.id} --force' to override, or wait for the claiming agent to finish.",
        )

    # Validate status
    if task.status != Status.PENDING:
        raise StatusError(
            message=f"Cannot claim task {task.id}: task is {task.status.value}, not pending",
            error_code="INVALID_STATUS",
            context={
                "task_id": task.id,
                "current_status": task.status.value,
            },
            suggestion=f"Only pending tasks can be claimed. Current status: {task.status.value}",
        )

    # Claim task
    now = utc_now()
    task.status = Status.IN_PROGRESS
    task.claimed_by = agent_id
    task.claimed_at = now
    task.started_at = now
    task.updated_at = now


def complete_task(task: Task, force: bool = False) -> None:
    """
    Mark task as complete.

    Raises:
        StatusError: If completion fails
    """
    if not force and task.status != Status.IN_PROGRESS:
        raise StatusError(
            message=f"Cannot complete task {task.id}: task is {task.status.value}, not in progress",
            error_code="INVALID_STATUS",
            context={
                "task_id": task.id,
                "current_status": task.status.value,
            },
            suggestion=f"Only in-progress tasks can be completed. Current status: {task.status.value}",
        )

    now = utc_now()
    task.status = Status.DONE
    task.completed_at = now
    task.updated_at = now


def update_status(task: Task, new_status: Status, reason: Optional[str] = None) -> None:
    """
    Update task status with validation.

    Raises:
        StatusError: If transition is invalid
    """
    # Validate transition
    is_valid, error = validate_transition(task.status, new_status)
    if not is_valid:
        valid_transitions = TRANSITIONS.get(task.status, [])

        raise StatusError(
            message=error,
            error_code="INVALID_STATUS_TRANSITION",
            context={
                "task_id": task.id,
                "current_status": task.status.value,
                "requested_status": new_status.value,
                "valid_transitions": [s.value for s in valid_transitions],
            },
            suggestion=f"Valid transitions from {task.status.value}: {[s.value for s in valid_transitions]}",
        )

    # Check if reason is required
    if new_status in [Status.BLOCKED, Status.REJECTED, Status.CANCELLED]:
        if not reason:
            raise StatusError(
                message=f"Reason required when marking task as {new_status.value}",
                error_code="MISSING_REASON",
                context={
                    "task_id": task.id,
                    "requested_status": new_status.value,
                },
                suggestion=f'Add --reason="explanation" when changing to {new_status.value}',
            )

    if new_status in [Status.BLOCKED, Status.REJECTED, Status.CANCELLED]:
        task.reason = reason
    else:
        task.reason = None

    # Update status
    task.status = new_status
    now = utc_now()

    # Clear claim if transitioning to pending
    if new_status == Status.PENDING:
        task.claimed_by = None
        task.claimed_at = None

    # Set completion time if transitioning to done
    if new_status == Status.DONE and not task.completed_at:
        task.completed_at = now
    task.updated_at = now


def check_stale_claims(
    tasks: list[Task], warn_minutes: int = 60, error_minutes: int = 120
) -> list[dict]:
    """
    Check for stale task claims.

    Returns:
        List of stale claim warnings/errors
    """
    stale = []
    now = utc_now()

    for task in tasks:
        if task.status == Status.IN_PROGRESS and task.claimed_at:
            claimed = to_utc(task.claimed_at)
            age_minutes = (now - claimed).total_seconds() / 60

            if age_minutes >= error_minutes:
                stale.append(
                    {
                        "level": "error",
                        "task_id": task.id,
                        "claimed_by": task.claimed_by,
                        "age_minutes": int(age_minutes),
                        "message": f"Task {task.id} claimed {int(age_minutes)}m ago (ERROR threshold: {error_minutes}m)",
                    }
                )
            elif age_minutes >= warn_minutes:
                stale.append(
                    {
                        "level": "warning",
                        "task_id": task.id,
                        "claimed_by": task.claimed_by,
                        "age_minutes": int(age_minutes),
                        "message": f"Task {task.id} claimed {int(age_minutes)}m ago (WARN threshold: {warn_minutes}m)",
                    }
                )

    return stale
