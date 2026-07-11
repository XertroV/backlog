import { Status, type Task } from "./models";
import { toUtc, utcNow } from "./time";

export class StatusError extends Error {
  constructor(
    message: string,
    public readonly errorCode: string,
    public readonly context: Record<string, unknown>,
    public readonly suggestion: string,
  ) {
    super(message);
  }

  toJSON(): Record<string, unknown> {
    return {
      error: this.errorCode,
      message: this.message,
      context: this.context,
      suggestion: this.suggestion,
    };
  }
}

export const TRANSITIONS: Record<Status, Status[]> = {
  [Status.PENDING]: [Status.IN_PROGRESS, Status.BLOCKED, Status.CANCELLED],
  [Status.IN_PROGRESS]: [Status.DONE, Status.BLOCKED, Status.REJECTED, Status.PENDING],
  [Status.DONE]: [Status.BLOCKED, Status.REJECTED],
  [Status.BLOCKED]: [Status.PENDING, Status.CANCELLED],
  [Status.REJECTED]: [Status.PENDING],
  [Status.CANCELLED]: [],
};

export function validateTransition(current: Status, next: Status): [boolean, string | null] {
  if (TRANSITIONS[current].includes(next)) return [true, null];
  return [false, `Cannot transition from '${current}' to '${next}'. Valid transitions: ${JSON.stringify(TRANSITIONS[current])}`];
}

export function claimTask(task: Task, agentId: string, force = false): void {
  if (task.claimedBy && !force) {
    const ageMinutes = task.claimedAt ? (utcNow().getTime() - toUtc(task.claimedAt).getTime()) / 60000 : 0;
    throw new StatusError(
      `Task ${task.id} is already claimed by ${task.claimedBy}`,
      "ALREADY_CLAIMED",
      {
        task_id: task.id,
        claimed_by: task.claimedBy,
        claimed_duration_minutes: Math.floor(ageMinutes),
      },
      `Use 'backlog claim ${task.id} --force' to override.`,
    );
  }
  if (task.status !== Status.PENDING) {
    throw new StatusError(
      `Cannot claim task ${task.id}: task is ${task.status}, not pending`,
      "INVALID_STATUS",
      { task_id: task.id, current_status: task.status },
      "Only pending tasks can be claimed.",
    );
  }
  const now = utcNow();
  task.status = Status.IN_PROGRESS;
  task.claimedBy = agentId;
  task.claimedAt = now;
  task.startedAt = now;
  task.updatedAt = now;
}

export function completeTask(task: Task, force = false): void {
  if (!force && task.status !== Status.IN_PROGRESS) {
    throw new StatusError(
      `Cannot complete task ${task.id}: task is ${task.status}, not in progress`,
      "INVALID_STATUS",
      { task_id: task.id, current_status: task.status },
      "Only in-progress tasks can be completed.",
    );
  }
  task.status = Status.DONE;
  const now = utcNow();
  task.completedAt = now;
  task.updatedAt = now;
}

export function updateStatus(task: Task, next: Status, reason?: string): void {
  const [valid, error] = validateTransition(task.status, next);
  if (!valid) {
    throw new StatusError(
      error ?? "Invalid status transition",
      "INVALID_STATUS_TRANSITION",
      {
        task_id: task.id,
        current_status: task.status,
        requested_status: next,
        valid_transitions: TRANSITIONS[task.status],
      },
      `Valid transitions from ${task.status}: ${TRANSITIONS[task.status].join(", ")}`,
    );
  }

  if ([Status.BLOCKED, Status.REJECTED, Status.CANCELLED].includes(next) && !reason) {
    throw new StatusError(
      `Reason required when marking task as ${next}`,
      "MISSING_REASON",
      { task_id: task.id, requested_status: next },
      `Add --reason="..." when changing to ${next}`,
    );
  }

  if ([Status.BLOCKED, Status.REJECTED, Status.CANCELLED].includes(next)) {
    task.reason = reason;
  } else {
    task.reason = undefined;
  }

  task.status = next;
  if (next === Status.PENDING) {
    task.claimedBy = undefined;
    task.claimedAt = undefined;
  }
  const now = utcNow();
  task.updatedAt = now;
  if (next === Status.DONE && !task.completedAt) {
    task.completedAt = now;
  }
}

export function checkStaleClaims(
  tasks: Task[],
  warnMinutes = 60,
  errorMinutes = 120,
): Array<Record<string, unknown>> {
  const stale: Array<Record<string, unknown>> = [];
  const now = utcNow();

  for (const task of tasks) {
    if (task.status === Status.IN_PROGRESS && task.claimedAt) {
      const ageMinutes = (now.getTime() - toUtc(task.claimedAt).getTime()) / 60000;
      if (ageMinutes >= errorMinutes) {
        stale.push({
          level: "error",
          task_id: task.id,
          claimed_by: task.claimedBy,
          age_minutes: Math.floor(ageMinutes),
          message: `Task ${task.id} claimed ${Math.floor(ageMinutes)}m ago (ERROR threshold: ${errorMinutes}m)`,
        });
      } else if (ageMinutes >= warnMinutes) {
        stale.push({
          level: "warning",
          task_id: task.id,
          claimed_by: task.claimedBy,
          age_minutes: Math.floor(ageMinutes),
          message: `Task ${task.id} claimed ${Math.floor(ageMinutes)}m ago (WARN threshold: ${warnMinutes}m)`,
        });
      }
    }
  }

  return stale;
}
