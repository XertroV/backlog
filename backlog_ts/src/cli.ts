#!/usr/bin/env bun
import pc from "picocolors";
import { dirname, join } from "node:path";
import { existsSync, readFileSync, statSync } from "node:fs";
import { mkdir } from "node:fs/promises";
import { createInterface } from "node:readline/promises";
import { parse, stringify } from "yaml";
import { CriticalPathCalculator } from "./critical_path";
import {
  clearContext,
  endSession,
  findEpic,
  findMilestone,
  findPhase,
  findTask,
  filterTreeByPathQuery,
  getActiveSessions,
  getAllTasks,
  getCurrentTaskId,
  getStaleSessions,
  isBugId,
  parseDependencyIds,
  isIdeaId,
  isFixedId,
  isValidTaskId,
  isTaskFileMissing,
  loadConfig,
  loadContext,
  loadSessions,
  saveSessions,
  setCurrentTask,
  startSession,
  taskFilePath,
  updateSessionHeartbeat,
} from "./helpers";
import { TaskLoader } from "./loader";
import {
  Complexity,
  Priority,
  Status,
  TaskPath,
  PathQuery,
  type Epic,
  type Milestone,
  type Phase,
  type Task,
  type TaskTree,
} from "./models";
import { claimTask, completeTask, StatusError, updateStatus } from "./status";
import { utcNow } from "./time";
import { runChecks } from "./check";
import { BACKLOG_DIR, getDataDirName } from "./data_dir";
import { logoShouldUseColor, renderStartupLogo, startupLogoWidth } from "./logo";
import {
  BACKLOG_HOWTO_SKILL_MD,
  BACKLOG_HOWTO_SKILL_VERSION,
} from "./generated/backlogHowtoSkill";

const AGENTS_SNIPPETS: Record<string, string> = {
  short: `# AGENTS.md (Short)

# Work Loop & Task Backlog

## Task Workflow
- Use \`backlog grab\` to claim work, then \`backlog done\` or \`backlog cycle\`.
- If a command fails to parse args/usage, run exactly one recovery command: \`backlog cycle\`.
- For explicit task IDs, use \`backlog claim <TASK_ID> [TASK_ID ...]\`.
- Prefer critical-path work, then \`critical > high > medium > low\` priority.
- If blocked, run \`backlog blocked --reason "<why>"\` and handoff quickly.
- Keep each change scoped to one task; update status as soon as state changes.
- Before done: run targeted tests for changed code.
- For more see \`backlog --help\`.
`,
  medium: `# AGENTS.md (Medium)

# Work Loop & Task Backlog

## Defaults
- Claim with \`backlog grab\` (or \`backlog grab --single\` for focused work).
- Use \`backlog claim <TASK_ID> [TASK_ID ...]\` when task IDs are provided.
- If command argument parsing fails, run \`backlog cycle\` once to recover.
- CLI selection order is: critical-path first, then task priority.
- Use \`backlog work <id>\` when switching context; use \`backlog show\` to review details.
`,
  long: `# AGENTS.md (Long)

# Work Loop & Task Backlog

## Operating Model
- Default command: \`backlog\`. Use local \`.backlog/\` state as source of truth.
- Selection strategy: critical-path first, then \`critical > high > medium > low\`.
- Treat task files as contracts: requirements + acceptance criteria drive scope.
`,
};

const PREVIEW_DISPLAY_LIMIT = 5;
const PREVIEW_GRAB_FOLLOW_COUNT = 4;
const PREVIEW_AUX_LIMIT = 5;
const PREVIEW_BUG_FANOUT_COUNT = 2;
const TASK_PREVIEW_LINES = 12;

export function parseFlag(args: string[], name: string): boolean {
  return args.includes(name);
}

export function parseOpt(args: string[], name: string): string | undefined {
  const exact = args.find((a) => a.startsWith(`${name}=`));
  if (exact) return exact.slice(name.length + 1);
  const idx = args.indexOf(name);
  if (idx >= 0 && idx + 1 < args.length) return args[idx + 1];
  return undefined;
}

export function parseOpts(args: string[], name: string): string[] {
  const out: string[] = [];
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (!arg) continue;
    if (arg === name) {
      if (i + 1 < args.length) out.push(args[i + 1]!);
      i += 1;
      continue;
    }
    if (arg.startsWith(`${name}=`)) {
      out.push(arg.slice(name.length + 1));
    }
  }
  return out;
}

type AutoCommitContext = {
  hasStaged: boolean;
  preStatus: Map<string, string>;
  canAmendBlAdd: boolean;
  prevAddUnpushed: boolean;
};

type AutoCommitMetadata = {
  id: string;
  title: string;
};

function gitRun(args: string[]): string {
  const output = Bun.spawnSync(["git", ...args], {
    cwd: process.cwd(),
    stdout: "pipe",
    stderr: "pipe",
  });
  const stdout = output.stdout.toString();
  const stderr = output.stderr.toString();
  if (output.exitCode !== 0) {
    throw new Error((stderr || stdout || "git command failed").trim());
  }
  return stdout.replace(/[\r\n]+$/g, "");
}

function gitStatusSnapshot(): Map<string, string> {
  const statusText = gitRun(["status", "--porcelain=v1"]);
  const status = new Map<string, string>();
  for (const rawLine of statusText.split("\n")) {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    if (line.length < 3) {
      continue;
    }
    const state = line.slice(0, 2);
    let path = line.slice(3);
    if (!path) {
      continue;
    }
    if (state.startsWith("R")) {
      const parts = path.split(" -> ");
      if (parts.length === 2) {
        path = parts[1];
      }
    }
    status.set(path, state);
  }
  return status;
}

function hasStagedChanges(status: Map<string, string>): boolean {
  for (const state of status.values()) {
    if (state.length > 0 && state[0] !== " " && state[0] !== "?") {
      return true;
    }
  }
  return false;
}

function changedTrackedPaths(before: Map<string, string>, after: Map<string, string>): string[] {
  const paths = new Set<string>();
  for (const [path, afterState] of after.entries()) {
    const beforeState = before.get(path);
    if (beforeState === undefined || beforeState !== afterState) {
      paths.add(path);
    }
  }
  for (const path of before.keys()) {
    if (!after.has(path)) {
      paths.add(path);
    }
  }
  return Array.from(paths);
}

function normalizeAutoCommitMetadata(value: string): string {
  return value.replace(/\r/g, " ").replace(/\n/g, " ").trim().split(/\s+/).join(" ");
}

function formatAutoCommitMessage(prefix: string, metadata: AutoCommitMetadata): string {
  const id = normalizeAutoCommitMetadata(metadata.id);
  const title = normalizeAutoCommitMetadata(metadata.title);
  if (id && title) return `${prefix} ${id}: ${title}`;
  if (id) return `${prefix} ${id}`;
  if (title) return `${prefix} ${title}`;
  return prefix;
}

function autoCommitMessage(command: string, metadata: AutoCommitMetadata | null): string {
  if (command === "add") {
    if (!metadata?.id) return "bl add";
    return formatAutoCommitMessage("bl add", metadata);
  }
  if (command === "bug") {
    if (!metadata?.id) return "bl bug";
    return formatAutoCommitMessage("bl bug", metadata);
  }
  if (command === "idea") {
    if (!metadata?.id) return "bl idea";
    return formatAutoCommitMessage("bl idea", metadata);
  }
  if (command === "claim") {
    if (!metadata?.id) return "bl claim";
    return formatAutoCommitMessage("bl claim", metadata);
  }
  if (command === "grab") {
    if (!metadata?.id) return "bl grab";
    return formatAutoCommitMessage("bl grab", metadata);
  }
  if (command === "done") {
    if (!metadata?.id) return "bl done";
    return formatAutoCommitMessage("bl done", metadata);
  }
  if (command === "unclaim") {
    if (!metadata?.id) return "bl unclaim";
    return formatAutoCommitMessage("bl unclaim", metadata);
  }
  if (command === "undone") {
    if (!metadata?.id) return "bl undone";
    return formatAutoCommitMessage("bl undone", metadata);
  }
  if (command === "set") {
    if (!metadata?.id) return "bl set";
    return formatAutoCommitMessage("bl set", metadata);
  }
  if (command === "edit") {
    if (!metadata?.id) return "bl edit";
    return formatAutoCommitMessage("bl edit", metadata);
  }
  return `backlog ${command}`;
}

function isPreviousCommitFromBlAdd(): boolean {
  const message = gitRun(["log", "-1", "--pretty=%B"]);
  if (!message) {
    return false;
  }
  return message.split("\n", 1)[0].startsWith("bl add");
}

function isPreviousCommitUnpushed(): boolean {
  try {
    gitRun(["rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"]);
  } catch {
    return true;
  }

  const aheadRaw = gitRun(["rev-list", "--count", "@{u}..HEAD"]);
  const ahead = Number.parseInt(aheadRaw, 10);
  if (Number.isNaN(ahead)) {
    return false;
  }
  return ahead > 0;
}

function gitAddPaths(paths: string[]): void {
  if (!paths.length) {
    return;
  }
  gitRun(["add", "--", ...paths]);
}

function gitCommit(message: string): void {
  gitRun(["commit", "-m", message]);
}

function gitAmendNoEdit(): void {
  gitRun(["commit", "--amend", "--no-edit"]);
}

function captureAutoCommitContext(): AutoCommitContext {
  const preStatus = gitStatusSnapshot();
  const context: AutoCommitContext = {
    hasStaged: hasStagedChanges(preStatus),
    preStatus,
    canAmendBlAdd: false,
    prevAddUnpushed: false,
  };
  if (!context.hasStaged) {
    context.canAmendBlAdd = isPreviousCommitFromBlAdd();
    context.prevAddUnpushed = isPreviousCommitUnpushed();
  }
  return context;
}

async function executeAutoCommit(
  command: string,
  context: AutoCommitContext,
  metadata: AutoCommitMetadata | null,
): Promise<void> {
  const postStatus = gitStatusSnapshot();
  const changed = changedTrackedPaths(context.preStatus, postStatus);
  if (!changed.length) {
    return;
  }

  gitAddPaths(changed);

  if (command === "add" && context.canAmendBlAdd && context.prevAddUnpushed) {
    try {
      gitAmendNoEdit();
      return;
    } catch {
      // Fall back to creating a new commit if amend fails.
    }
  }

  gitCommit(autoCommitMessage(command, metadata));
}

async function runWithAutoCommit(
  command: string,
  action: () => Promise<AutoCommitMetadata>,
): Promise<void> {
  let context: AutoCommitContext | null = null;
  try {
    context = captureAutoCommitContext();
  } catch {
    context = null;
  }

  const metadata = await action();

  if (!context || context.hasStaged) {
    return;
  }

  try {
    await executeAutoCommit(command, context, metadata);
  } catch (err) {
    console.log(`Warning: Auto-commit skipped: ${(err as Error).toString()}`);
  }
}

type FlagMode = "boolean" | "value";

function positionalArgsForCommand(
  args: string[],
  flags: Record<string, FlagMode>,
  commandForUsage?: string,
): string[] {
  const positional: string[] = [];
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (!arg) continue;
    if (arg === "--") {
      positional.push(...args.slice(i + 1));
      break;
    }

    if (!arg.startsWith("-")) {
      positional.push(arg);
      continue;
    }

    const idx = arg.indexOf("=");
    const flag = idx === -1 ? arg : arg.slice(0, idx);
    const mode = flags[flag];
    if (!mode) {
      if (commandForUsage) {
        printCommandHelpForCommand(commandForUsage);
      }
      textError(`unexpected flag: ${arg}`);
    }

    if (mode === "value" && idx === -1) {
      i += 1;
    } else if (mode === "boolean" && idx !== -1) {
      if (commandForUsage) {
        printCommandHelpForCommand(commandForUsage);
      }
      textError(`unexpected flag: ${arg}`);
    }
  }
  return positional;
}

function parseNegatedFlag(args: string[], name: string, defaultValue: boolean): boolean {
  const yesIndex = args.indexOf(`--${name}`);
  const noIndex = args.indexOf(`--no-${name}`);
  if (yesIndex === -1 && noIndex === -1) return defaultValue;
  if (yesIndex === -1) return false;
  if (noIndex === -1) return true;
  return yesIndex > noIndex;
}

function listScopeArgs(args: string[]): string[] {
  const optionValues = new Set<number>();
  const optionValueNames = new Set([
    "--status",
    "--complexity",
    "--priority",
    "--phase",
    "--milestone",
    "--epic",
  ]);

  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (!arg) continue;
    if (optionValueNames.has(arg) && i + 1 < args.length) {
      optionValues.add(i + 1);
    }
  }

  const scopes: string[] = [];
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (!arg) continue;
    if (arg.startsWith("-")) continue;
    if (optionValues.has(i)) continue;
    scopes.push(arg);
  }

  return scopes;
}

function validateAtLeastTwoWords(command: string, title: string | null | undefined): void {
  if (!title) {
    textError(`${command} requires at least two words`);
  }
  if (title.trim().split(/\s+/).filter(Boolean).length < 2) {
    textError(`${command} requires at least two words`);
  }
}

function collectTaskIdsFromPhases(phases: Phase[]): Set<string> {
  const ids = new Set<string>();
  for (const phase of phases) {
    for (const milestone of phase.milestones) {
      for (const epic of milestone.epics) {
        for (const task of epic.tasks) {
          ids.add(task.id);
        }
      }
    }
  }
  return ids;
}

function anyScopeMatch(candidate: string, scopes: string[]): boolean {
  return scopes.some((scope) => candidate.startsWith(scope) || scope.startsWith(candidate));
}

function validateScopeTokens(tree: TaskTree, scopes: string[]): void {
  for (const scope of scopes) {
    const token = scope.trim();
    if (!token) {
      textError("Scope cannot be empty.");
    }
    let parsed = false;
    try {
      PathQuery.parse(token);
      parsed = true;
    } catch {
      parsed = false;
    }
    if (!parsed) {
      textError(`Invalid path format: ${token}`);
    }
    const matches =
      !!findPhase(tree, token) ||
      !!findMilestone(tree, token) ||
      !!findEpic(tree, token) ||
      getAllTasks(tree).some((t) => t.id.startsWith(token));
    if (!matches) {
      textError(`No list nodes found for path query: ${token}`);
    }
  }
}

function mergeScopedPhases(scopes: Phase[][]): Phase[] {
  const phaseMap = new Map<string, Phase>();
  for (const scopedPhases of scopes) {
    for (const phase of scopedPhases) {
      const phaseOut = phaseMap.get(phase.id) ?? { ...phase, milestones: [] };
      if (!phaseMap.has(phase.id)) phaseMap.set(phase.id, phaseOut);
      const milestoneMap = new Map(phaseOut.milestones.map((m) => [m.id, m]));
      for (const milestone of phase.milestones) {
        const milestoneOut = milestoneMap.get(milestone.id) ?? { ...milestone, epics: [] };
        if (!milestoneMap.has(milestone.id)) {
          phaseOut.milestones.push(milestoneOut);
          milestoneMap.set(milestone.id, milestoneOut);
        }
        const epicMap = new Map(milestoneOut.epics.map((e) => [e.id, e]));
        for (const epic of milestone.epics) {
          const epicOut = epicMap.get(epic.id) ?? { ...epic, tasks: [] };
          if (!epicMap.has(epic.id)) {
            milestoneOut.epics.push(epicOut);
            epicMap.set(epic.id, epicOut);
          }
          const taskSet = new Set(epicOut.tasks.map((t) => t.id));
          for (const task of epic.tasks) {
            if (!taskSet.has(task.id)) {
              epicOut.tasks.push(task);
              taskSet.add(task.id);
            }
          }
        }
      }
    }
  }
  return [...phaseMap.values()];
}

export function jsonOut(obj: unknown): void {
  console.log(JSON.stringify(obj, null, 2));
}

function formatMs(ms: number): string {
  return `${ms.toFixed(2)}ms`;
}

export function textError(message: string): never {
  console.error(pc.red(`Error: ${message}`));
  process.exit(1);
}

function usage(): void {
  console.log(`Usage: backlog <command> [options]

Commands:
  howto           Show the agent how to use backlog effectively
  ls              List by scope or show task summary by full ID
  list            List tasks with filtering options
  tree            Display full hierarchical tree
  show            Show detailed info for a task/phase/milestone/epic
  next            Get next available task on critical path
  preview         Show upcoming work preview with grab suggestions
  claim           Claim specific task ID(s)
  edit            Open a task todo file in your editor
  grab            Auto-claim next task (or claim IDs)
  done            Mark task as complete
  undone          Mark task/epic/milestone/phase as not done
  cycle           Complete current task and grab next
  dash            Show quick dashboard of project status
  update          Update task status
  set             Set task properties (status/priority/etc)
  work            Set/show current working task
  unclaim         Release a claimed task
  blocked         Mark task as blocked and optionally grab next with --grab
  bug             Create a new bug report
  idea            Capture an idea as planning intake
  fixed           Capture an ad-hoc completed fix note
  ci              Validate task IDs for CI workflows
  search          Search tasks by pattern
  check           Check task tree consistency
  init            Initialize a new .backlog project
  add             Add a new task to an epic
  add-epic        Add a new epic to a milestone
  add-milestone   Add a new milestone to a phase
  add-phase       Add a new phase to the project
  lock            Lock a phase/milestone/epic
  unlock          Unlock a phase/milestone/epic
  move            Move task/epic/milestone to new parent
  session         Manage agent sessions
  data            Export/summarize task data
  report          Generate reports (progress, velocity, estimates) [alias: r]
  version         Show CLI version
  timeline        Display an ASCII Gantt chart of the project timeline (alias: tl)
  schema          Show file schema information
  blockers        Show blocking tasks
  skills          Install skill files
  agents          Print AGENTS.md snippets
  log             Show recent activity log (claims, starts, completions, and added items)
  migrate         Migrate .tasks/ to .backlog/
  benchmark       Show load-time benchmark for the full task tree

Quick rules:
  - Prefer 'backlog claim <TASK_ID> [TASK_ID ...]' for explicit IDs.
  - Use 'backlog grab' for automatic selection.
  - If command parsing fails, run 'backlog cycle' once.`);
}

type CommandHelpSpec = {
  summary: string;
  usage: string;
  options: string[];
  examples: string[];
  tip?: string;
};

const helpAliases: Record<string, string> = {
  ls: "list",
  r: "report",
  tl: "timeline",
};

const commandHelpSpecs: Record<string, CommandHelpSpec> = {
  howto: { summary: "Show the agent how to use backlog effectively.", usage: "backlog howto [--json]", options: ["--json"], examples: ["backlog howto", "backlog howto --json"] },
  add: { summary: "Add a new task to an epic.", usage: "backlog add <EPIC_ID> --title <TITLE> [options]", options: ["--title, -T", "--estimate, -e", "--complexity, -c", "--priority, -p", "--depends-on, -d", "--tags", "--body, -b"], examples: ["backlog add P1.M1.E1 --title \"Implement parser\""] },
  "add-epic": { summary: "Add a new epic to a milestone.", usage: "backlog add-epic <MILESTONE_ID> --title <TITLE> [options]", options: ["--title, -T", "--name, -n", "--estimate, -e", "--complexity, -c", "--depends-on, -d", "--description"], examples: ["backlog add-epic P1.M1 --title \"CLI polish\""] },
  "add-milestone": { summary: "Add a new milestone to a phase.", usage: "backlog add-milestone <PHASE_ID> --title <TITLE> [options]", options: ["--title, -T", "--name, -n", "--estimate, -e", "--complexity, -c", "--depends-on, -d", "--description"], examples: ["backlog add-milestone P1 --title \"Beta readiness\""] },
  "add-phase": { summary: "Add a new phase to the project.", usage: "backlog add-phase --title <TITLE> [options]", options: ["--title, -T", "--name, -n", "--weeks, -w", "--estimate, -e", "--priority, -p", "--depends-on, -d", "--description"], examples: ["backlog add-phase --title \"Stabilization\""] },
  admin: { summary: "Administrative checks and diagnostics.", usage: "backlog admin [check-file-sync|check-ids] [--json]", options: ["--json"], examples: ["backlog admin check-file-sync", "backlog admin check-ids --json"] },
  agents: { summary: "Print AGENTS.md snippets.", usage: "backlog agents [short|medium|full] [--json]", options: ["--json"], examples: ["backlog agents", "backlog agents medium", "backlog agents full --json"] },
  benchmark: { summary: "Show load-time benchmark for the full task tree.", usage: "backlog benchmark [--json] [--top N] [--mode metadata|full] [--parse-task-body]", options: ["--json", "--top", "--mode", "--parse-task-body"], examples: ["backlog benchmark", "backlog benchmark --json --top 10"] },
  blocked: { summary: "Mark task as blocked and optionally grab the next task.", usage: "backlog blocked <TASK_ID> --reason REASON [--agent AGENT] [--grab] [--json]", options: ["--reason", "--agent", "--grab", "--json"], examples: ["backlog blocked P1.M1.E1.T001 --reason \"waiting on API\""] },
  blockers: { summary: "Show blocking tasks.", usage: "backlog blockers [--deep] [--suggest] [--json]", options: ["--deep", "--suggest", "--json"], examples: ["backlog blockers", "backlog blockers --deep --suggest"] },
  bug: { summary: "Create a new bug report.", usage: "backlog bug [--title <TITLE> | BUG_TEXT] [options]", options: ["--title, -T", "--estimate, -e", "--complexity, -c", "--priority, -p", "--depends-on, -d", "--tags", "--simple, -s", "--body, -b"], examples: ["backlog bug \"Crash when opening report\""] },
  check: { summary: "Check task tree consistency.", usage: "backlog check [--json] [--strict]", options: ["--json", "--strict"], examples: ["backlog check", "backlog check --strict"] },
  claim: { summary: "Claim specific task ID(s).", usage: "backlog claim <TASK_ID> [TASK_ID ...] [--agent AGENT] [--force] [--no-content]", options: ["--agent", "--force", "--no-content"], examples: ["backlog claim P1.M1.E1.T001", "backlog claim P1.M1.E1.T001 P1.M1.E1.T002 --agent agent-a"] },
  edit: { summary: "Open a task todo file in your editor.", usage: "backlog edit <TASK_ID>", options: [], examples: ["backlog edit P1.M1.E1.T001"] },
  cycle: { summary: "Complete current task and grab next.", usage: "backlog cycle [--agent AGENT] [--json] [--no-content]", options: ["--agent", "--json", "--no-content"], examples: ["backlog cycle", "backlog cycle --agent agent-a"] },
  dash: { summary: "Show quick dashboard of project status.", usage: "backlog dash [--json]", options: ["--json"], examples: ["backlog dash", "backlog dash --json"] },
  data: { summary: "Export/summarize task data.", usage: "backlog data <summary|export> [--format json|yaml] [--scope SCOPE] [--include-content]", options: ["--format", "--scope", "--include-content"], examples: ["backlog data summary --format json"] },
  ci: { summary: "Validate task IDs for CI workflows.", usage: "backlog ci validate-ids <TASK_ID...>", options: ["--help"], examples: ["backlog ci validate-ids B020 E1.T02", "backlog ci validate-ids P1.M1.E1.T001"] },
  done: { summary: "Mark task as complete.", usage: "backlog done <TASK_ID> [TASK_ID ...] [--status STATUS] [--force] [--verify]", options: ["--status", "--force", "--verify"], examples: ["backlog done P1.M1.E1.T001"] },
  fixed: { summary: "Capture an ad-hoc completed fix note.", usage: "backlog fixed [--title <TITLE> | FIX_TEXT] [--description DESC] [--at ISO8601]", options: ["--title, -T", "--description", "--at", "--tags", "--body, -b"], examples: ["backlog fixed \"Prevented deadlock in worker loop\""] },
  grab: { summary: "Auto-claim next task (or claim IDs).", usage: "backlog grab [TASK_ID ...] [--agent AGENT] [--single] [--json] [--no-content]", options: ["--agent", "--single", "--json", "--no-content"], examples: ["backlog grab", "backlog grab --single"] },
  handoff: { summary: "Transfer task ownership to another agent.", usage: "backlog handoff <TASK_ID> --to AGENT [--notes \"...\"] [--force]", options: ["--to", "--notes", "--force"], examples: ["backlog handoff P1.M1.E1.T001 --to agent-b --notes \"taking over\""] },
  help: { summary: "Show command overview and guidance.", usage: "backlog help [COMMAND]", options: [], examples: ["backlog help", "backlog help show"] },
  idea: { summary: "Capture an idea as planning intake.", usage: "backlog idea [--title <TITLE> | IDEA_TEXT] [options]", options: ["--title, -T", "--estimate, -e", "--complexity, -c", "--priority, -p", "--depends-on, -d", "--tags", "--simple, -s", "--body, -b"], examples: ["backlog idea \"improve migration diagnostics\"", "backlog idea --title \"Improve docs flow\" --simple"] },
  init: { summary: "Initialize a new .backlog project.", usage: "backlog init --project NAME [--description TEXT] [--timeline-weeks N]", options: ["--project, -p", "--description, -d", "--timeline-weeks, -w"], examples: ["backlog init --project my-project"] },
  list: { summary: "List tasks with filtering options.", usage: "backlog list [<SCOPE>] [options]", options: ["--status", "--critical", "--available, -a", "--complexity", "--priority", "--progress", "--json", "--all", "--unfinished", "--bugs, -b", "--ideas, -i", "--show-completed-aux", "--phase", "--milestone", "--epic"], examples: ["backlog list", "backlog list P1.M1 --progress", "backlog list --json"] },
  lock: { summary: "Lock a phase/milestone/epic.", usage: "backlog lock <ITEM_ID>", options: [], examples: ["backlog lock P1.M1"] },
  log: { summary: "Show recent activity log.", usage: "backlog log [--bugs, -b] [--ideas, -i] [--limit N] [--json]", options: ["--bugs, -b", "--ideas, -i", "--limit", "--json"], examples: ["backlog log", "backlog log --bugs --ideas --limit 20 --json"] },
  migrate: { summary: "Migrate .tasks/ to .backlog/.", usage: "backlog migrate [--force] [--no-symlink]", options: ["--force", "--no-symlink"], examples: ["backlog migrate"] },
  move: { summary: "Move task/epic/milestone to new parent.", usage: "backlog move <SOURCE_ID> --to <DEST_ID>", options: ["--to"], examples: ["backlog move P1.M1.E1.T001 --to P1.M1.E2"] },
  next: { summary: "Get next available task on critical path.", usage: "backlog next [--json]", options: ["--json"], examples: ["backlog next", "backlog next --json"] },
  preview: { summary: "Show upcoming work preview with grab suggestions.", usage: "backlog preview [--json]", options: ["--json"], examples: ["backlog preview", "backlog preview --json"] },
  report: { summary: "Generate reports (progress, velocity, estimates).", usage: "backlog report [progress|velocity|estimate-accuracy|p|v|ea] [--json] [--format json|table]", options: ["progress|velocity|estimate-accuracy", "--json", "--format"], examples: ["backlog report progress", "backlog r v --json"] },
  velocity: { summary: "Generate the velocity report.", usage: "backlog velocity [--json] [--format json|table] [--days DAYS]", options: ["--json", "--format", "--days"], examples: ["backlog velocity --days 14", "backlog velocity --json"] },
  schema: { summary: "Show file schema information.", usage: "backlog schema [--json] [--compact]", options: ["--json", "--compact"], examples: ["backlog schema", "backlog schema --json"] },
  search: { summary: "Search tasks by pattern.", usage: "backlog search <PATTERN> [options]", options: ["--status", "--tags", "--complexity", "--priority", "--limit", "--json"], examples: ["backlog search auth", "backlog search --status pending --limit 5 auth"] },
  session: { summary: "Manage agent sessions.", usage: "backlog session <start|heartbeat|list|end|clean> [--agent AGENT] [--timeout MINUTES]", options: ["start", "heartbeat", "list", "end", "clean"], examples: ["backlog session start --agent agent-a", "backlog session list"] },
  set: { summary: "Set task properties (status/priority/etc).", usage: "backlog set <TASK_ID> [property flags]", options: ["--status", "--priority", "--complexity", "--estimate", "--title", "--depends-on", "--tags", "--reason", "--body, -b", "--append-body"], examples: ["backlog set P1.M1.E1.T001 --priority high --tags api,auth", "backlog set P1.M1.E1.T001 --body \"seed\" --append-body"] },
  show: {
    summary: "Show detailed info for task/phase/milestone/epic.",
    usage: "backlog show [PATH_ID ...] [--long] [--all]",
    options: ["--long", "--all"],
    examples: ["backlog show P1.M1.E1.T001", "backlog show P1.M1 P2.M1.E3", "backlog show", "backlog show P1.M1.E1.T001 --long", "backlog show P1.M1.E1.T001 --all"],
  },
  skills: { summary: "Install skill files.", usage: "backlog skills install <SKILL> [--client codex|claude] [--artifact skills|commands] [--dry-run] [--json]", options: ["--client", "--artifact", "--dry-run", "--json"], examples: ["backlog skills install plan-task --client=codex --artifact=skills"] },
  skip: { summary: "Skip current task and move on.", usage: "backlog skip <TASK_ID> [--agent AGENT] [--no-grab]", options: ["--agent", "--no-grab"], examples: ["backlog skip P1.M1.E1.T001 --agent agent-a"] },
  sync: { summary: "Sync derived metadata in index files.", usage: "backlog sync", options: [], examples: ["backlog sync"] },
  timeline: { summary: "Display an ASCII Gantt chart of the project timeline.", usage: "backlog timeline [--scope SCOPE] [--group-by phase|milestone|epic|status] [--show-done] [--json]", options: ["--scope", "--group-by", "--show-done", "--json"], examples: ["backlog timeline", "backlog timeline --group-by milestone"] },
  tree: { summary: "Display full hierarchical task tree.", usage: "backlog tree [PATH_QUERY ...] [--json] [--unfinished] [--show-completed-aux] [--details] [--depth N]", options: ["--json", "--unfinished", "--show-completed-aux", "--details", "--depth"], examples: ["backlog tree", "backlog tree P1.M1 --details", "backlog tree P1.M1 P1.M2.E1 --depth 3"] },
  unclaim: { summary: "Release a claimed task.", usage: "backlog unclaim <TASK_ID> [--agent AGENT]", options: ["--agent"], examples: ["backlog unclaim P1.M1.E1.T001"] },
  "unclaim-stale": { summary: "Release stale claims older than threshold.", usage: "backlog unclaim-stale [--threshold MINUTES] [--dry-run]", options: ["--threshold", "--dry-run"], examples: ["backlog unclaim-stale --threshold 120"] },
  undone: { summary: "Mark task/epic/milestone/phase as not done.", usage: "backlog undone <ITEM_ID>", options: [], examples: ["backlog undone P1.M1.E1.T001"] },
  unlock: { summary: "Unlock a phase/milestone/epic.", usage: "backlog unlock <ITEM_ID>", options: [], examples: ["backlog unlock P1.M1"] },
  update: { summary: "Update task status.", usage: "backlog update <TASK_ID> <STATUS> [--reason REASON]", options: ["--reason"], examples: ["backlog update P1.M1.E1.T001 blocked --reason \"waiting on API\""] },
  version: { summary: "Show CLI version.", usage: "backlog version", options: [], examples: ["backlog version"] },
  why: { summary: "Explain why a task is blocked/unavailable.", usage: "backlog why <TASK_ID> [--json]", options: ["--json"], examples: ["backlog why P1.M1.E1.T001"] },
  work: { summary: "Set/show current working task.", usage: "backlog work [TASK_ID|clear] [--agent AGENT]", options: ["--agent"], examples: ["backlog work", "backlog work P1.M1.E1.T001", "backlog work clear"] },
};

function maybePrintCommandHelp(command: string, args: string[]): boolean {
  if (!parseFlag(args, "--help", "-h")) return false;
  printCommandHelpForCommand(command);
  return true;
}

function runHelp(rest: string[]): void {
  const command = rest.find((arg) => arg && !arg.startsWith("-"));
  if (command) {
    const normalized = helpAliases[command] ?? command;
    if (!commandHelpSpecs[normalized]) {
      textError(`Unknown command: ${command}`);
    }
    printCommandHelpForCommand(normalized);
    return;
  }
  if (parseFlag(rest, "--help", "-h")) {
    printCommandHelpForCommand("help");
    return;
  }
  if (rest.length > 0) {
    const unknown = rest.find((arg) => arg.startsWith("-"));
    textError(`Unknown flag: ${unknown ?? rest[0]}`);
  }
  usage();
}

function printCommandHelpForCommand(command: string): void {
  const normalized = helpAliases[command] ?? command;
  const spec = commandHelpSpecs[normalized];
  if (!spec) {
    console.log(`\nCommand Help: backlog ${normalized}`);
    console.log(`Usage: backlog ${normalized}`);
    return;
  }

  console.log(`\nCommand Help: backlog ${normalized}`);
  console.log(spec.summary);
  console.log(`\nUsage: ${spec.usage}`);
  if (spec.options.length > 0) {
    console.log("\nOptions");
    for (const option of spec.options) console.log(`  ${option}`);
  }
  if (spec.examples.length > 0) {
    console.log("\nExamples");
    for (const example of spec.examples) console.log(`  ${example}`);
  }
  console.log("\nTip: Use `backlog list` or `backlog tree` to find valid IDs.");
}

// Helper functions for filtering and stats
function isUnfinished(status: Status): boolean {
  return status !== Status.DONE && status !== Status.CANCELLED && status !== Status.REJECTED;
}

type LogEventType = "added" | "claimed" | "started" | "completed";
interface LogEvent {
  taskId: string;
  title: string;
  event: LogEventType;
  timestamp: Date;
  actor: string | null;
}

interface PreviewTaskPayload {
  id: string;
  title: string;
  status: Status;
  file: string;
  file_exists: boolean;
  estimate_hours: number;
  complexity: Complexity;
  priority: Priority;
  on_critical_path: boolean;
  grab_additional: string[];
  path?: string;
}

function formatRelativeTime(value: Date): string {
  const delta = utcNow().getTime() - value.getTime();
  const seconds = Math.max(0, Math.floor(delta / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d ago`;
  return `${Math.floor(days / 7)}w ago`;
}

function activityIcon(eventType: LogEventType): string {
  if (eventType === "completed") return pc.green("✓");
  if (eventType === "started") return pc.blue("▶");
  if (eventType === "claimed") return pc.yellow("✎");
  return pc.magenta("✚");
}

function activityKind(eventType: LogEventType): string {
  if (eventType === "added") return "created";
  return eventType;
}

function collectActivity(tree: TaskTree): LogEvent[] {
  const events: LogEvent[] = [];
  const dataDir = getDataDirName();
  const eventOrder: Record<LogEventType, number> = {
    added: 0,
    claimed: 1,
    started: 2,
    completed: 3,
  };

  for (const task of getAllTasks(tree)) {
    const taskPath = join(dataDir, task.file);

    if (task.completedAt) {
      events.push({
        taskId: task.id,
        title: task.title,
        event: "completed",
        timestamp: task.completedAt,
        actor: task.claimedBy ?? null,
      });
    }
    if (task.startedAt) {
      events.push({
        taskId: task.id,
        title: task.title,
        event: "started",
        timestamp: task.startedAt,
        actor: task.claimedBy ?? null,
      });
    }
    if (task.claimedAt) {
      events.push({
        taskId: task.id,
        title: task.title,
        event: "claimed",
        timestamp: task.claimedAt,
        actor: task.claimedBy ?? null,
      });
    }
    if (!task.claimedAt && !task.startedAt && !task.completedAt && existsSync(taskPath)) {
      const stats = statSync(taskPath);
      events.push({
        taskId: task.id,
        title: task.title,
        event: "added",
        timestamp: stats.mtime,
        actor: null,
      });
    }
  }

  events.sort((a, b) => {
    const timeDiff = b.timestamp.getTime() - a.timestamp.getTime();
    if (timeDiff !== 0) return timeDiff;
    return eventOrder[b.event] - eventOrder[a.event];
  });

  return events;
}

function includeAuxItem(status: Status, unfinished: boolean, showCompletedAux: boolean): boolean {
  if (unfinished) return isUnfinished(status);
  if (showCompletedAux) return true;
  return isUnfinished(status);
}

function previewGrabCandidates(tree: TaskTree, calc: CriticalPathCalculator, primaryTask: Task): Task[] {
  const candidateIds = isBugId(primaryTask.id)
    ? calc.findAdditionalBugs(primaryTask, PREVIEW_BUG_FANOUT_COUNT)
    : calc.findSiblingTasks(primaryTask, PREVIEW_GRAB_FOLLOW_COUNT);

  const candidates: Task[] = [];
  for (const taskId of candidateIds) {
    const task = findTask(tree, taskId);
    if (!task || isTaskFileMissing(task)) {
      continue;
    }
    if (candidates.some((candidate) => candidate.id === task.id)) {
      continue;
    }
    candidates.push(task);
  }
  return candidates;
}

function buildPreviewTaskPayload(
  task: Task,
  criticalPath: string[],
  calc: CriticalPathCalculator,
  tree: TaskTree,
  includePath = false,
): PreviewTaskPayload {
  const payload: PreviewTaskPayload = {
    id: task.id,
    title: task.title,
    status: task.status,
    file: task.file,
    file_exists: !isTaskFileMissing(task),
    estimate_hours: task.estimateHours,
    complexity: task.complexity,
    priority: task.priority,
    on_critical_path: criticalPath.includes(task.id),
    grab_additional: previewGrabCandidates(tree, calc, task).map((t) => t.id),
  };
  if (includePath) {
    payload.path = `${getDataDirName()}/${task.file}`;
  }
  return payload;
}

function filterUnfinishedTasks(tasks: Task[]): Task[] {
  return tasks.filter((t) => isUnfinished(t.status));
}

function hasUnfinishedTasks(epic: Epic): boolean {
  return epic.tasks.some((t) => isUnfinished(t.status));
}

function hasUnfinishedEpics(milestone: Milestone): boolean {
  return milestone.epics.some((e) => hasUnfinishedTasks(e));
}

function hasUnfinishedMilestones(phase: Phase): boolean {
  return phase.milestones.some((m) => hasUnfinishedEpics(m));
}

function calculateTaskStats(tasks: Task[]): { done: number; total: number } {
  return {
    done: tasks.filter((t) => t.status === Status.DONE).length,
    total: tasks.length,
  };
}

function calculateTaskEstimateHours(tasks: Task[]): number {
  return tasks.reduce((acc, task) => acc + task.estimateHours, 0);
}

function getEpicStats(epic: Epic): { done: number; total: number } {
  return calculateTaskStats(epic.tasks);
}

function getEpicTotalEstimateHours(epic: Epic): number {
  return calculateTaskEstimateHours(epic.tasks);
}

function getMilestoneStats(milestone: Milestone): { done: number; total: number } {
  const tasks = milestone.epics.flatMap((e) => e.tasks);
  return calculateTaskStats(tasks);
}

function getMilestoneTotalEstimateHours(milestone: Milestone): number {
  const tasks = milestone.epics.flatMap((e) => e.tasks);
  return calculateTaskEstimateHours(tasks);
}

function getPhaseStats(phase: Phase): { done: number; total: number } {
  const tasks = phase.milestones.flatMap((m) => m.epics.flatMap((e) => e.tasks));
  return calculateTaskStats(tasks);
}

function getPhaseTotalEstimateHours(phase: Phase): number {
  const tasks = phase.milestones.flatMap((m) => m.epics.flatMap((e) => e.tasks));
  return calculateTaskEstimateHours(tasks);
}

function findMilestoneByScopedId(scopeTree: TaskTree, id: string): Milestone | undefined {
  return findMilestone(scopeTree, id);
}

function findEpicByScopedId(scopeTree: TaskTree, id: string): Epic | undefined {
  return findEpic(scopeTree, id);
}

function makeProgressBar(done: number, total: number, width = 20): string {
  return makeProgressBarWithInProgress(done, 0, total, width);
}

function makeProgressBarWithInProgress(
  done: number,
  inProgress: number,
  total: number,
  width = 20,
): string {
  if (total === 0) return pc.dim("░".repeat(width));

  const safeDone = Math.max(0, done);
  const safeInProgress = Math.max(0, inProgress);
  const remainingInProgress = Math.max(total - safeDone, 0);
  const clampedInProgress = Math.min(safeInProgress, remainingInProgress);

  const doneWidth = Math.min(width, Math.floor((safeDone / total) * width));
  const inProgressWidth = Math.min(
    width - doneWidth,
    Math.floor(((safeDone + clampedInProgress) / total) * width) - doneWidth,
  );
  const pendingWidth = Math.max(width - doneWidth - inProgressWidth, 0);

  return `${pc.green("█".repeat(doneWidth))}${pc.yellow("▓".repeat(inProgressWidth))}${pc.dim(
    "░".repeat(pendingWidth),
  )}`;
}

function listWithProgress(
  tree: TaskTree,
  unfinished: boolean,
  showCompletedAux: boolean,
  includeNormal: boolean,
  includeBugs: boolean,
  includeIdeas: boolean,
  scopedPhases?: Phase[],
): void {
  console.log();
  console.log(pc.bold(pc.cyan("Project Progress")));
  console.log();

  const phasesToShow = scopedPhases
    ? scopedPhases
    : includeNormal
      ? (unfinished ? tree.phases.filter((p) => hasUnfinishedMilestones(p)) : tree.phases)
    : [];
  const completedPhases: Array<{ id: string; name: string; total: number }> = [];

  for (const phase of phasesToShow) {
    const stats = getPhaseStats(phase);
    const { done, total } = stats;
    const inProgress = phase.milestones.flatMap((m) => m.epics.flatMap((e) => e.tasks)).filter((t) => t.status === Status.IN_PROGRESS).length;
    const blocked = phase.milestones.flatMap((m) => m.epics.flatMap((e) => e.tasks)).filter((t) => t.status === Status.BLOCKED).length;

    const pct = total > 0 ? (done / total) * 100 : 0;

    if (pct === 100) {
      completedPhases.push({ id: phase.id, name: phase.name, total });
      continue;
    }

    const bar = makeProgressBarWithInProgress(done, inProgress, total);

    let indicator: string;
    if (inProgress > 0) {
      indicator = pc.yellow("→");
    } else if (blocked > 0) {
      indicator = pc.red("🔒");
    } else {
      indicator = "[ ]";
    }

    console.log(`${indicator} ${pc.bold(`${phase.id}: ${phase.name}`)}`);
    console.log(`    ${bar} ${pct.toFixed(1).padStart(5)}% (${done}/${total})`);

    for (const m of phase.milestones) {
      const mStats = getMilestoneStats(m);
      const mDone = mStats.done;
      const mTotal = mStats.total;
      const mInProgress = m.epics.flatMap((e) => e.tasks).filter((t) => t.status === Status.IN_PROGRESS).length;

      const mPct = mTotal > 0 ? (mDone / mTotal) * 100 : 0;

      if (mPct === 100) continue;

      const mBar = makeProgressBarWithInProgress(mDone, mInProgress, mTotal, 15);

      const mInd = mInProgress > 0 ? pc.yellow("→") : "○";

      console.log(`    ${mInd} ${m.id}: ${mBar} ${Math.round(mPct).toString().padStart(3)}%`);
    }

    console.log();
  }

  // Show bugs summary in progress view
  const bugs = includeBugs
    ? (tree.bugs ?? []).filter((b) => includeAuxItem(b.status, unfinished, showCompletedAux))
    : [];
  if (bugs.length > 0) {
    const bugsDone = bugs.filter((b) => b.status === Status.DONE).length;
    const bugsTotal = bugs.length;
    const bugPct = bugsTotal > 0 ? (bugsDone / bugsTotal) * 100 : 0;
    const bugBar = makeProgressBar(bugsDone, bugsTotal);
    console.log(`${bugPct === 100 ? pc.green("✓") : "🐛"} ${pc.bold("Bugs")}`);
    console.log(`    ${bugBar} ${bugPct.toFixed(1).padStart(5)}% (${bugsDone}/${bugsTotal})`);
    console.log();
  }

  const ideas = includeIdeas
    ? (tree.ideas ?? []).filter((i) => includeAuxItem(i.status, unfinished, showCompletedAux))
    : [];
  if (ideas.length > 0) {
    const ideasDone = ideas.filter((i) => i.status === Status.DONE).length;
    const ideasTotal = ideas.length;
    const ideaPct = ideasTotal > 0 ? (ideasDone / ideasTotal) * 100 : 0;
    const ideaBar = makeProgressBar(ideasDone, ideasTotal);
    console.log(`${ideaPct === 100 ? pc.green("✓") : "💡"} ${pc.bold("Ideas")}`);
    console.log(`    ${ideaBar} ${ideaPct.toFixed(1).padStart(5)}% (${ideasDone}/${ideasTotal})`);
    console.log();
  }

  if (completedPhases.length > 0) {
    const completedStr = completedPhases.map((p) => `${p.id} (${p.total})`).join(", ");
    console.log(`${pc.green("✓ Completed:")} ${completedStr}`);
    console.log();
  }
}

// Display helpers
function getStatusIcon(status: Status): string {
  switch (status) {
    case Status.DONE:
      return pc.green("[✓]");
    case Status.IN_PROGRESS:
      return pc.yellow("[→]");
    case Status.PENDING:
      return "[ ]";
    case Status.BLOCKED:
      return pc.red("[✗]");
    case Status.CANCELLED:
    case Status.REJECTED:
      return pc.dim("[X]");
    default:
      return "[ ]";
  }
}

function getTreeChars(isLast: boolean): { branch: string; continuation: string } {
  return {
    branch: isLast ? "└── " : "├── ",
    continuation: isLast ? "    " : "│   ",
  };
}

function listAvailable(
  tree: TaskTree,
  availableIds: string[],
  criticalPath: string[],
  statusFilter: string[],
  outputJson: boolean,
  includeNormal: boolean,
  includeBugs: boolean,
  includeIdeas: boolean,
  scopedTaskIds?: Set<string>,
): void {
  const availableTasks = availableIds
    .map((taskId) => findTask(tree, taskId))
    .filter((task): task is Task => !!task)
    .filter((task) => {
      const isBug = /^B\d+$/.test(task.id);
      const isIdea = /^I\d+$/.test(task.id);
      if (scopedTaskIds && !scopedTaskIds.has(task.id)) {
        return false;
      }
      if (isBug) return includeBugs;
      if (isIdea) return includeIdeas;
      return includeNormal;
    })
    .filter((task) => (statusFilter.length ? statusFilter.includes(task.status) : true));

  if (!availableTasks.length) {
    console.log("No available tasks found.");
    return;
  }

  if (outputJson) {
    const output = availableTasks.map((task) => ({
      id: task.id,
      title: task.title,
      estimate_hours: task.estimateHours,
      complexity: task.complexity,
      priority: task.priority,
      on_critical_path: criticalPath.includes(task.id),
    }));
    jsonOut(output);
    return;
  }

  const normalTasks = new Map<string, Task[]>();
  const bugs: Task[] = [];
  const ideas: Task[] = [];
  for (const task of availableTasks) {
    if (isBugId(task.id)) {
      bugs.push(task);
      continue;
    }
    if (isIdeaId(task.id)) {
      ideas.push(task);
      continue;
    }
    const phaseId = task.phaseId ?? "UNKNOWN";
    const grouped = normalTasks.get(phaseId) ?? [];
    grouped.push(task);
    normalTasks.set(phaseId, grouped);
  }

  console.log(`\n${pc.green(`Available Tasks (${availableTasks.length}):`)}`);
  console.log();

  for (const phase of tree.phases) {
    const tasks = normalTasks.get(phase.id);
    if (!tasks?.length) continue;

    console.log(pc.bold(`${phase.name} (${tasks.length} available)`));
    for (const task of tasks) {
      const critMarker = criticalPath.includes(task.id) ? `${pc.yellow("★")} ` : "";
      console.log(`  ${critMarker}${task.id}: ${task.title}`);
    }
    console.log();
  }

  if (bugs.length > 0) {
    console.log(pc.bold(`Bugs (${bugs.length})`));
    for (let i = 0; i < bugs.length; i++) {
      const bug = bugs[i]!;
      const isLast = i === bugs.length - 1;
      const prefix = isLast ? "└── " : "├── ";
      const critMarker = criticalPath.includes(bug.id) ? `${pc.yellow("★")} ` : "";
      console.log(`  ${prefix}${critMarker}${bug.id}: ${bug.title}`);
    }
  }

  if (ideas.length > 0) {
    console.log(pc.bold(`Ideas (${ideas.length})`));
    for (let i = 0; i < ideas.length; i++) {
      const idea = ideas[i]!;
      const isLast = i === ideas.length - 1;
      const prefix = isLast ? "└── " : "├── ";
      const critMarker = criticalPath.includes(idea.id) ? `${pc.yellow("★")} ` : "";
      console.log(`  ${prefix}${critMarker}${idea.id}: ${idea.title}`);
    }
  }
}

async function cmdList(args: string[]): Promise<void> {
  const outputJson = parseFlag(args, "--json");
  const statusFilter = parseOpt(args, "--status")?.split(",") ?? [];
  const showAll = parseFlag(args, "--all");
  const unfinished = parseFlag(args, "--unfinished");
  const bugsOnly = parseFlag(args, "--bugs") || parseFlag(args, "-b");
  const ideasOnly = parseFlag(args, "--ideas") || parseFlag(args, "-i");
  const available = parseFlag(args, "--available") || parseFlag(args, "-a");
  const showCompletedAux = parseFlag(args, "--show-completed-aux");
  const showProgress = parseFlag(args, "--progress");
  const phase = parseOpt(args, "--phase");
  const milestone = parseOpt(args, "--milestone");
  const epic = parseOpt(args, "--epic");
  const positionalScopes = listScopeArgs(args);
  const hasScopeFlag = Boolean(phase || milestone || epic);
  const scopeInputs = hasScopeFlag ? [phase ?? milestone ?? epic ?? ""] : positionalScopes;
  let scopedPhases: Phase[] | undefined;
  let scopedTaskIds: Set<string> | undefined;
  let scopeMaxDepth: number | undefined;

  let effectiveShowCompletedAux = showCompletedAux || (showAll && (bugsOnly || ideasOnly));

  let includeNormal = !bugsOnly && !ideasOnly;
  let includeBugs = bugsOnly || (!bugsOnly && !ideasOnly);
  let includeIdeas = ideasOnly || (!bugsOnly && !ideasOnly);
  if (scopeInputs.length > 0) {
    effectiveShowCompletedAux = false;
    includeNormal = true;
    includeBugs = false;
    includeIdeas = false;
  }

  const loader = new TaskLoader();
  const tree = await loader.load("metadata", includeBugs, includeIdeas);
  const cfg = loadConfig();
  const calc = new CriticalPathCalculator(tree, (cfg.complexity_multipliers as Record<string, number>) ?? {});
  const { criticalPath, nextAvailable } = calc.calculate();
  tree.criticalPath = criticalPath;
  tree.nextAvailable = nextAvailable;

  if (scopeInputs.length > 0) {
    const scopedSlices: Phase[][] = [];
    const scopeDepths: number[] = [];

    if (phase) {
      const phaseMatch = findPhase(tree, phase);
      if (!phaseMatch) textError(`Phase not found: ${phase}`);
      const scopeQuery = PathQuery.parse(phaseMatch.id);
      const phasesForScope = filterTreeByPathQuery(tree.phases, scopeQuery);
      if (!phasesForScope.length) textError(`Phase not found: ${phase}`);
      scopedSlices.push(phasesForScope);
      scopeDepths.push(TaskPath.parse(phaseMatch.id).depth);
    } else if (milestone) {
      const milestoneMatch = findMilestone(tree, milestone);
      if (!milestoneMatch) textError(`Milestone not found: ${milestone}`);
      const scopeQuery = PathQuery.parse(milestoneMatch.id);
      const phasesForScope = filterTreeByPathQuery(tree.phases, scopeQuery);
      if (!phasesForScope.length) textError(`Milestone not found: ${milestone}`);
      scopedSlices.push(phasesForScope);
      scopeDepths.push(TaskPath.parse(milestoneMatch.id).depth);
    } else if (epic) {
      const epicMatch = findEpic(tree, epic);
      if (!epicMatch) textError(`Epic not found: ${epic}`);
      const scopeQuery = PathQuery.parse(epicMatch.id);
      const phasesForScope = filterTreeByPathQuery(tree.phases, scopeQuery);
      if (!phasesForScope.length) textError(`Epic not found: ${epic}`);
      scopedSlices.push(phasesForScope);
      scopeDepths.push(TaskPath.parse(epicMatch.id).depth);
    } else {
      validateScopeTokens(tree, scopeInputs);
      for (const input of scopeInputs) {
        let scopeId = input;
        let scopeQuery = PathQuery.parse(scopeId);
        let phasesForScope = filterTreeByPathQuery(tree.phases, scopeQuery);
        if (!phasesForScope.length) {
          const fallback = findPhase(tree, scopeId) ?? findMilestone(tree, scopeId) ?? findEpic(tree, scopeId);
          if (fallback) {
            scopeId = fallback.id;
            scopeQuery = PathQuery.parse(scopeId);
            phasesForScope = filterTreeByPathQuery(tree.phases, scopeQuery);
          }
        }
        if (!phasesForScope.length) {
          textError(`No list nodes found for path query: ${input}`);
        }
        scopedSlices.push(phasesForScope);
        try {
          scopeDepths.push(TaskPath.parse(scopeId).depth);
        } catch {
          scopeDepths.push(scopeQuery.segments.length);
        }
      }
    }

    scopedPhases = mergeScopedPhases(scopedSlices);
    scopedTaskIds = collectTaskIdsFromPhases(scopedPhases);
    if (scopeDepths.length > 0) {
      scopeMaxDepth = Math.min(4, Math.max(...scopeDepths) + 2);
    }
  }

  if (showProgress) {
    listWithProgress(tree, unfinished, effectiveShowCompletedAux, includeNormal, includeBugs, includeIdeas, scopedPhases);
    return;
  }

  if (available) {
    let allAvailable = calc.findAllAvailable();
    if (scopedTaskIds) {
      allAvailable = allAvailable.filter((taskId) => scopedTaskIds.has(taskId));
    }
    listAvailable(
      tree,
      allAvailable,
      criticalPath,
      statusFilter,
      outputJson,
      includeNormal,
      includeBugs,
      includeIdeas,
      scopedTaskIds,
    );
    return;
  }

  const baseTasks = getAllTasks(tree).filter((t) => !scopedTaskIds || scopedTaskIds.has(t.id));
  const tasks = baseTasks
    .filter((t) => (statusFilter.length ? statusFilter.includes(t.status) : true))
    .filter((t) => {
      const isBug = /^B\d+$/.test(t.id);
      const isIdea = /^I\d+$/.test(t.id);
      if (isBug) return includeBugs;
      if (isIdea) return includeIdeas;
      return includeNormal;
    });
  if (outputJson) {
    const phasesSource = scopedPhases ?? (includeNormal ? tree.phases : []);
    const filteredPhases = phasesSource
      .map((p) => ({
        ...p,
        milestones: p.milestones
          .filter((m) => (!unfinished || hasUnfinishedEpics(m)))
          .map((m) => ({
            ...m,
            epics: m.epics
              .filter((e) => (!unfinished || hasUnfinishedTasks(e)))
              .map((e) => ({
                ...e,
                tasks: unfinished ? filterUnfinishedTasks(e.tasks) : e.tasks,
              })),
          })),
      }))
      .filter((p) => (!unfinished || hasUnfinishedMilestones(p)));

    const bugsForJson = (includeBugs ? (tree.bugs ?? []) : [])
      .filter((b) => (statusFilter.length ? statusFilter.includes(b.status) : true))
      .filter((b) => includeAuxItem(b.status, unfinished, effectiveShowCompletedAux));
    const ideasForJson = (includeIdeas ? (tree.ideas ?? []) : [])
      .filter((i) => (statusFilter.length ? statusFilter.includes(i.status) : true))
      .filter((i) => includeAuxItem(i.status, unfinished, effectiveShowCompletedAux));
    jsonOut({
      critical_path: criticalPath,
      next_available: nextAvailable,
      phases: filteredPhases.map((p) => ({
        id: p.id,
        name: p.name,
        status: p.status,
        stats: getPhaseStats(p),
        milestones: p.milestones.map((m) => ({
          id: m.id,
          name: m.name,
          status: m.status,
          stats: getMilestoneStats(m),
        })),
      })),
      tasks: tasks
        .filter((t) => (!unfinished || isUnfinished(t.status)))
        .map((t) => ({
          id: t.id,
          title: t.title,
          status: t.status,
          estimate_hours: t.estimateHours,
          complexity: t.complexity,
          priority: t.priority,
          on_critical_path: criticalPath.includes(t.id),
        })),
      bugs: bugsForJson.map((b) => ({
        id: b.id,
        title: b.title,
        status: b.status,
        priority: b.priority,
        estimate_hours: b.estimateHours,
        on_critical_path: criticalPath.includes(b.id),
      })),
      ideas: ideasForJson.map((i) => ({
        id: i.id,
        title: i.title,
        status: i.status,
        priority: i.priority,
        estimate_hours: i.estimateHours,
        on_critical_path: criticalPath.includes(i.id),
      })),
    });
    return;
  }

  if (scopeInputs.length > 0) {
    const maxDepth = scopeMaxDepth ?? 4;
    for (let i = 0; i < scopedPhases.length; i++) {
      const p = scopedPhases[i]!;
      const isLast = i === scopedPhases.length - 1;
      const lines = renderPhase(p, isLast, "", criticalPath, unfinished, false, maxDepth, 1);
      console.log(lines.join("\n"));
    }
    return;
  }

  console.log(pc.cyan("Critical Path:"), criticalPath.slice(0, 10).join(" -> "));
  console.log();

  const phasesToShow = includeNormal
    ? (unfinished ? tree.phases.filter((p) => hasUnfinishedMilestones(p)) : tree.phases)
    : [];

  for (const p of phasesToShow) {
    const stats = getPhaseStats(p);
    console.log(`${pc.bold(`${p.name} (${p.id})`)} (${stats.done}/${stats.total} tasks done)`);

    const milestonesToShow = unfinished ? p.milestones.filter((m) => hasUnfinishedEpics(m)) : p.milestones;
    const milestoneLimit = showAll ? milestonesToShow.length : 5;
    const displayedMilestones = milestonesToShow.slice(0, milestoneLimit);
    const hiddenCount = milestonesToShow.length - displayedMilestones.length;

    for (let i = 0; i < displayedMilestones.length; i++) {
      const m = displayedMilestones[i]!;
      const mStats = getMilestoneStats(m);
      const isLast = i === displayedMilestones.length - 1 && hiddenCount === 0;
      const prefix = isLast ? "└── " : "├── ";
      console.log(`  ${prefix}${m.name} (${m.id}) (${mStats.done}/${mStats.total} tasks done)`);
    }

    if (hiddenCount > 0) {
      console.log(`  └── ... and ${hiddenCount} more milestone${hiddenCount === 1 ? "" : "s"}`);
    }
  }

  // Show bugs section
  const bugsToShow = (includeBugs ? (tree.bugs ?? []) : [])
    .filter((b) => (statusFilter.length ? statusFilter.includes(b.status) : true))
    .filter((b) => includeAuxItem(b.status, unfinished, effectiveShowCompletedAux));
  if (bugsToShow.length > 0) {
    console.log();
    const bugsDone = bugsToShow.filter((b) => b.status === Status.DONE).length;
    console.log(`${pc.bold("Bugs")} (${bugsDone}/${bugsToShow.length} done)`);
    for (let i = 0; i < bugsToShow.length; i++) {
      const b = bugsToShow[i]!;
      const isLast = i === bugsToShow.length - 1;
      const prefix = isLast ? "└── " : "├── ";
      const icon = getStatusIcon(b.status);
      const critMarker = criticalPath.includes(b.id) ? `${pc.yellow("★")} ` : "";
      console.log(`  ${prefix}${icon} ${critMarker}${b.id}: ${b.title} [${b.priority}]`);
    }
  }

  const ideasToShow = (includeIdeas ? (tree.ideas ?? []) : [])
    .filter((i) => (statusFilter.length ? statusFilter.includes(i.status) : true))
    .filter((i) => includeAuxItem(i.status, unfinished, effectiveShowCompletedAux));
  if (ideasToShow.length > 0) {
    console.log();
    const ideasDone = ideasToShow.filter((i) => i.status === Status.DONE).length;
    console.log(`${pc.bold("Ideas")} (${ideasDone}/${ideasToShow.length} done)`);
    for (let i = 0; i < ideasToShow.length; i++) {
      const idea = ideasToShow[i]!;
      const isLast = i === ideasToShow.length - 1;
      const prefix = isLast ? "└── " : "├── ";
      const icon = getStatusIcon(idea.status);
      const critMarker = criticalPath.includes(idea.id) ? `${pc.yellow("★")} ` : "";
      console.log(`  ${prefix}${icon} ${critMarker}${idea.id}: ${idea.title} [${idea.priority}]`);
    }
  }
}

async function cmdLog(args: string[]): Promise<void> {
  const limitOpt = parseOpt(args, "--limit");
  const bugsOnly = parseFlag(args, "--bugs") || parseFlag(args, "-b");
  const ideasOnly = parseFlag(args, "--ideas") || parseFlag(args, "-i");
  const outputJson = parseFlag(args, "--json");
  const includeNormal = !bugsOnly && !ideasOnly;
  const includeBugs = bugsOnly || !includeNormal;
  const includeIdeas = ideasOnly || !includeNormal;
  const limit = limitOpt ? Number(limitOpt) : 20;
  if (!Number.isInteger(limit) || limit <= 0) {
    textError("--limit must be a positive integer");
  }

  const loader = new TaskLoader();
  const tree = await loader.load("metadata", includeBugs, includeIdeas);
  const events = collectActivity(tree)
    .filter((event) => {
      const isBug = isBugId(event.taskId);
      const isIdea = isIdeaId(event.taskId);
      if (isBug) {
        return includeBugs && !includeNormal;
      }
      if (isIdea) {
        return includeIdeas && !includeNormal;
      }
      return includeNormal;
    })
    .slice(0, limit);

  if (outputJson) {
    jsonOut(
      events.map((event) => ({
        task_id: event.taskId,
        title: event.title,
        event: event.event,
        kind: activityKind(event.event),
        timestamp: event.timestamp.toISOString(),
        actor: event.actor,
      })),
    );
    return;
  }

  if (!events.length) {
    console.log(pc.yellow("No recent activity found."));
    return;
  }

  console.log(pc.cyan("Recent Activity Log"));
  for (const event of events) {
    const age = formatRelativeTime(event.timestamp);
    const actor = event.actor ? ` (${event.actor})` : "";
    const kind = activityKind(event.event);
    console.log(
      `${activityIcon(event.event)} [${kind}] [${event.event}] ${event.taskId}${actor}`,
    );
    console.log(`  ${event.title}`);
    console.log(`  ${event.timestamp.toISOString()} (${age})`);
    console.log();
  }
}

async function cmdBenchmark(args: string[]): Promise<void> {
  const outputJson = parseFlag(args, "--json");
  const topArg = parseOpt(args, "--top");
  const mode = parseOpt(args, "--mode") ?? "full";
  if (mode !== "full" && mode !== "metadata" && mode !== "index") {
    textError("--mode must be one of: full, metadata, index");
  }
  const parseTaskBody = parseNegatedFlag(args, "parse-body", true);
  const effectiveParseTaskBody = mode === "full" ? parseTaskBody : false;
  const top = Number(topArg ?? 10);
  const topN = Number.isFinite(top) && top > 0 ? Math.floor(top) : 10;

  const loader = new TaskLoader();
  const { benchmark } = await loader.loadWithBenchmark(mode, effectiveParseTaskBody);
  const taskTotal = benchmark.counts.tasks;
  const missing = benchmark.missing_task_files;
  const found = taskTotal - missing;
  const indexParseMs = benchmark.index_parse_ms;
  const taskFrontmatterParseMs = benchmark.task_frontmatter_parse_ms;
  const taskBodyParseMs = benchmark.task_body_parse_ms;
  const otherParseMs = Math.max(
    0,
    benchmark.overall_ms - indexParseMs - taskFrontmatterParseMs - taskBodyParseMs,
  );

  const slowestPhases = [...benchmark.phase_timings]
    .sort((a, b) => b.ms - a.ms)
    .slice(0, topN);
  const slowestMilestones = [...benchmark.milestone_timings]
    .sort((a, b) => b.ms - a.ms)
    .slice(0, topN);
  const slowestEpics = [...benchmark.epic_timings]
    .sort((a, b) => b.ms - a.ms)
    .slice(0, topN);

  if (outputJson) {
    jsonOut({
      ...benchmark,
      summary: {
        overall_ms: benchmark.overall_ms,
        files_parsed: benchmark.files.total,
        task_files_total: taskTotal,
        task_files_found: found,
        task_files_missing: missing,
        index_parse_ms: indexParseMs,
        task_frontmatter_parse_ms: taskFrontmatterParseMs,
        task_body_parse_ms: taskBodyParseMs,
        task_parse_other_ms: otherParseMs,
        parse_mode: mode,
        parse_task_body: effectiveParseTaskBody,
        node_counts: {
          phases: benchmark.counts.phases,
          milestones: benchmark.counts.milestones,
          epics: benchmark.counts.epics,
        },
      },
      slowest: {
        phases: slowestPhases,
        milestones: slowestMilestones,
        epics: slowestEpics,
      },
    });
    return;
  }

  console.log("\nTask Tree Benchmark");
  console.log(`Overall parse time: ${formatMs(benchmark.overall_ms)}`);
  console.log(`Parse mode: ${mode}`);
  console.log(`Task body parsing: ${effectiveParseTaskBody ? "enabled" : "disabled"}`);
  console.log(`Index parse time: ${formatMs(indexParseMs)}`);
  console.log(`Task frontmatter parse time: ${formatMs(taskFrontmatterParseMs)}`);
  console.log(`Task body parse time: ${formatMs(taskBodyParseMs)}`);
  console.log(`Other parse time: ${formatMs(otherParseMs)}`);
  console.log(`Total files parsed: ${benchmark.files.total}`);
  console.log(
    `Task files (leaves): ${taskTotal} (${found} found, ${missing} missing)`,
  );
  console.log(`Phases parsed: ${benchmark.counts.phases}`);
  console.log(`Milestones parsed: ${benchmark.counts.milestones}`);
  console.log(`Epics parsed: ${benchmark.counts.epics}`);
  console.log("");

  const fileTypes = Object.keys(benchmark.files.by_type).sort();
  if (fileTypes.length) {
    console.log("Files by type:");
    for (const fileType of fileTypes) {
      const count = benchmark.files.by_type[fileType];
      const msTotal = benchmark.files.by_type_ms[fileType];
      if (count > 0) {
        console.log(`  ${fileType}: ${count} files (${formatMs(msTotal)})`);
      }
    }
  }

  if (slowestPhases.length) {
    console.log("\nSlowest phases:");
    for (const item of slowestPhases) {
      console.log(`  ${item.id} (${item.path}): ${formatMs(item.ms)}`);
    }
  }

  if (slowestMilestones.length) {
    console.log("\nSlowest milestones:");
    for (const item of slowestMilestones) {
      console.log(`  ${item.id} (${item.path}): ${formatMs(item.ms)}`);
    }
  }

  if (slowestEpics.length) {
    console.log("\nSlowest epics:");
    for (const item of slowestEpics) {
      console.log(`  ${item.id} (${item.path}): ${formatMs(item.ms)}`);
    }
  }
}

// Tree rendering functions
function renderTask(task: Task, isLast: boolean, prefix: string, criticalPath: string[], showDetails: boolean): string {
  const icon = getStatusIcon(task.status);
  const { branch } = getTreeChars(isLast);
  let line = `${prefix}${branch}${icon} ${task.id}: ${task.title}`;

  if (showDetails) {
    const details: string[] = [];
    if (task.estimateHours > 0) details.push(`(${task.estimateHours}h)`);
    if (task.status) details.push(`[${task.status}]`);
    if (task.claimedBy) details.push(`@${task.claimedBy}`);
    if (task.dependsOn.length > 0) details.push(`depends:${task.dependsOn.join(",")}`);
    if (criticalPath.includes(task.id)) details.push("★");

    if (details.length > 0) {
      line += ` ${details.join(" ")}`;
    }
  }

  return line;
}

function renderEpic(
  epic: Epic,
  isLast: boolean,
  prefix: string,
  criticalPath: string[],
  unfinished: boolean,
  showDetails: boolean,
  maxDepth: number,
  currentDepth: number,
): string[] {
  const stats = getEpicStats(epic);
  const { branch, continuation } = getTreeChars(isLast);
  const lines: string[] = [];

  lines.push(`${prefix}${branch}${epic.name} (${stats.done}/${stats.total}) [${epic.status}]`);

  if (currentDepth >= maxDepth) return lines;

  const tasksToShow = unfinished ? filterUnfinishedTasks(epic.tasks) : epic.tasks;
  const newPrefix = prefix + continuation;

  for (let i = 0; i < tasksToShow.length; i++) {
    const t = tasksToShow[i]!;
    const taskIsLast = i === tasksToShow.length - 1;
    lines.push(renderTask(t, taskIsLast, newPrefix, criticalPath, showDetails));
  }

  return lines;
}

function renderMilestone(
  milestone: Milestone,
  isLast: boolean,
  prefix: string,
  criticalPath: string[],
  unfinished: boolean,
  showDetails: boolean,
  maxDepth: number,
  currentDepth: number,
): string[] {
  const stats = getMilestoneStats(milestone);
  const { branch, continuation } = getTreeChars(isLast);
  const lines: string[] = [];

  lines.push(`${prefix}${branch}${milestone.name} (${stats.done}/${stats.total}) [${milestone.status}]`);

  if (currentDepth >= maxDepth) return lines;

  const epicsToShow = unfinished ? milestone.epics.filter((e) => hasUnfinishedTasks(e)) : milestone.epics;
  const newPrefix = prefix + continuation;

  for (let i = 0; i < epicsToShow.length; i++) {
    const e = epicsToShow[i]!;
    const epicIsLast = i === epicsToShow.length - 1;
    lines.push(...renderEpic(e, epicIsLast, newPrefix, criticalPath, unfinished, showDetails, maxDepth, currentDepth + 1));
  }

  return lines;
}

function renderPhase(
  phase: Phase,
  isLast: boolean,
  prefix: string,
  criticalPath: string[],
  unfinished: boolean,
  showDetails: boolean,
  maxDepth: number,
  currentDepth: number,
): string[] {
  const stats = getPhaseStats(phase);
  const { branch, continuation } = getTreeChars(isLast);
  const lines: string[] = [];

  lines.push(`${prefix}${branch}${pc.bold(phase.name)} (${stats.done}/${stats.total}) [${phase.status}]`);

  if (currentDepth >= maxDepth) return lines;

  const milestonesToShow = unfinished ? phase.milestones.filter((m) => hasUnfinishedEpics(m)) : phase.milestones;
  const newPrefix = prefix + continuation;

  for (let i = 0; i < milestonesToShow.length; i++) {
    const m = milestonesToShow[i]!;
    const milestoneIsLast = i === milestonesToShow.length - 1;
    lines.push(...renderMilestone(m, milestoneIsLast, newPrefix, criticalPath, unfinished, showDetails, maxDepth, currentDepth + 1));
  }

  return lines;
}

async function cmdTree(args: string[]): Promise<void> {
  const outputJson = parseFlag(args, "--json");
  const unfinished = parseFlag(args, "--unfinished");
  const showCompletedAux = parseFlag(args, "--show-completed-aux");
  const showDetails = parseFlag(args, "--details");
  const depthStr = parseOpt(args, "--depth");
  const maxDepth = depthStr ? Number(depthStr) : 4;
  const depthValueIndex = args.findIndex((arg) => arg === "--depth");
  const pathQueryTexts = args.filter((arg, index) => {
    if (arg.startsWith("-")) return false;
    if (depthValueIndex >= 0 && (index === depthValueIndex || index === depthValueIndex + 1)) {
      return false;
    }
    return true;
  });
  let pathQueries: PathQuery[] = [];
  for (const pathQueryText of pathQueryTexts) {
    try {
      pathQueries.push(PathQuery.parse(pathQueryText));
    } catch (error) {
      printCommandHelpForCommand("tree");
      textError((error as Error).message);
    }
  }
  const isScopedPathQuery = pathQueries.length > 0;

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const cfg = loadConfig();
  const calc = new CriticalPathCalculator(tree, (cfg.complexity_multipliers as Record<string, number>) ?? {});
  const { criticalPath, nextAvailable } = calc.calculate();
  tree.criticalPath = criticalPath;
  tree.nextAvailable = nextAvailable;
  let phasesToShow = tree.phases;
  if (isScopedPathQuery) {
    const scopedSlices: Phase[][] = [];
    for (const pathQuery of pathQueries) {
      scopedSlices.push(filterTreeByPathQuery(tree.phases, pathQuery));
    }
    phasesToShow = mergeScopedPhases(scopedSlices);
  }

  if (outputJson) {
    if (unfinished) {
      phasesToShow = phasesToShow.filter((p) => hasUnfinishedMilestones(p));
    }

    const filteredPhases = phasesToShow
      .map((p) => ({
        ...p,
        milestones: p.milestones
          .filter((m) => (!unfinished || hasUnfinishedEpics(m)))
          .map((m) => ({
            ...m,
            epics: m.epics
              .filter((e) => (!unfinished || hasUnfinishedTasks(e)))
              .map((e) => ({
                ...e,
                tasks: unfinished ? filterUnfinishedTasks(e.tasks) : e.tasks,
              })),
          })),
      }))
      .filter((p) => (!unfinished || hasUnfinishedMilestones(p)));

    jsonOut({
      critical_path: criticalPath,
      next_available: nextAvailable,
      max_depth: maxDepth,
      show_details: showDetails,
      unfinished_only: unfinished,
      phases: filteredPhases,
    });
    return;
  }

  if (unfinished) {
    phasesToShow = phasesToShow.filter((p) => hasUnfinishedMilestones(p));
  }
  let bugsToShow: Task[] = [];
  let ideasToShow: Task[] = [];
  if (!isScopedPathQuery) {
    bugsToShow = (tree.bugs ?? []).filter((b) => includeAuxItem(b.status, unfinished, showCompletedAux));
    ideasToShow = (tree.ideas ?? []).filter((i) => includeAuxItem(i.status, unfinished, showCompletedAux));
  }
  const hasBugs = bugsToShow.length > 0;
  const hasIdeas = ideasToShow.length > 0;
  const hasAux = hasBugs || hasIdeas;

  for (let i = 0; i < phasesToShow.length; i++) {
    const p = phasesToShow[i]!;
    const isLast = i === phasesToShow.length - 1 && !hasAux;
    const lines = renderPhase(p, isLast, "", criticalPath, unfinished, showDetails, maxDepth, 1);
    console.log(lines.join("\n"));
  }

  // Render auxiliary sections
  if (hasBugs) {
    const bugsDone = bugsToShow.filter((b) => b.status === Status.DONE).length;
    const branch = hasIdeas ? "├── " : "└── ";
    const continuation = hasIdeas ? "│   " : "    ";
    console.log(`${branch}${pc.bold("Bugs")} (${bugsDone}/${bugsToShow.length})`);
    for (let i = 0; i < bugsToShow.length; i++) {
      const b = bugsToShow[i]!;
      const isLastBug = i === bugsToShow.length - 1 && !hasIdeas;
      const icon = getStatusIcon(b.status);
      console.log(renderTask(b, isLastBug, continuation, criticalPath, showDetails));
    }
  }

  if (hasIdeas) {
    const ideasDone = ideasToShow.filter((i) => i.status === Status.DONE).length;
    const { branch, continuation } = getTreeChars(true);
    console.log(`${branch}${pc.bold("Ideas")} (${ideasDone}/${ideasToShow.length})`);
    for (let i = 0; i < ideasToShow.length; i++) {
      const idea = ideasToShow[i]!;
      const isLastIdea = i === ideasToShow.length - 1;
      const icon = getStatusIcon(idea.status);
      console.log(renderTask(idea, isLastIdea, continuation, criticalPath, showDetails));
    }
  }

  if (isScopedPathQuery && phasesToShow.length === 0) {
    const queryText = pathQueries.map((query) => query.raw).join(", ");
    console.log(
      pc.yellow(`No tree nodes found for path query: ${queryText}`),
    );
  }
}

async function cmdNext(args: string[]): Promise<void> {
  const outputJson = parseFlag(args, "--json");
  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const cfg = loadConfig();
  const calc = new CriticalPathCalculator(tree, (cfg.complexity_multipliers as Record<string, number>) ?? {});
  const { nextAvailable } = calc.calculate();

  if (!nextAvailable) {
    console.log("No available tasks found.");
    return;
  }
  const task = findTask(tree, nextAvailable);
  if (!task) textError(`Task not found: ${selectedTaskId}`);

  if (outputJson) {
    jsonOut({
      id: task.id,
      title: task.title,
      file: task.file,
      file_exists: !isTaskFileMissing(task),
      estimate_hours: task.estimateHours,
      complexity: task.complexity,
    });
    return;
  }
  console.log(`${task.id}: ${task.title}`);
}

async function cmdPreview(args: string[]): Promise<void> {
  const outputJson = parseFlag(args, "--json");
  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const cfg = loadConfig();
  const calc = new CriticalPathCalculator(tree, (cfg.complexity_multipliers as Record<string, number>) ?? {});
  const { criticalPath, nextAvailable } = calc.calculate();
  const available = calc.findAllAvailable();
  if (!available.length) {
    console.log("No available tasks found.");
    return;
  }

  const prioritized = calc.prioritizeTaskIds(available, criticalPath);
  const normal: PreviewTaskPayload[] = [];
  const bugs: PreviewTaskPayload[] = [];
  const ideas: PreviewTaskPayload[] = [];

  for (const taskId of prioritized) {
    const task = findTask(tree, taskId);
    if (!task) {
      continue;
    }
    if (isBugId(task.id)) {
      if (bugs.length < PREVIEW_AUX_LIMIT) {
        bugs.push(buildPreviewTaskPayload(task, criticalPath, calc, tree, !outputJson));
      }
    } else if (isIdeaId(task.id)) {
      if (ideas.length < PREVIEW_AUX_LIMIT) {
        ideas.push(buildPreviewTaskPayload(task, criticalPath, calc, tree, !outputJson));
      }
    } else if (normal.length < PREVIEW_DISPLAY_LIMIT) {
      normal.push(buildPreviewTaskPayload(task, criticalPath, calc, tree, !outputJson));
    }

    if (
      normal.length >= PREVIEW_DISPLAY_LIMIT &&
      bugs.length >= PREVIEW_AUX_LIMIT &&
      ideas.length >= PREVIEW_AUX_LIMIT
    ) {
      break;
    }
  }

  if (outputJson) {
    jsonOut({
      critical_path: criticalPath,
      next_available: nextAvailable,
      normal,
      bugs,
      ideas,
    });
    return;
  }

  const printPreviewItems = (label: string, items: PreviewTaskPayload[]): void => {
    if (!items.length) return;

    console.log(`\n${pc.bold(label)} (${items.length})`);
    for (const item of items) {
      const crit = item.on_critical_path ? `${pc.yellow("★")} ` : "  ";
      console.log(`  ${crit}${pc.bold(item.id)}: ${item.title}`);
      console.log(`    File: ${item.path} | Estimate: ${item.estimate_hours}h | ${item.priority} / ${item.complexity}`);
      if (item.grab_additional.length > 0) {
        console.log(`    ${pc.dim(`If you run \`backlog grab\`, you would also get: ${item.grab_additional.join(", ")}`)}`);
      } else {
        console.log(pc.dim("    If you run `backlog grab`, you get this task only."));
      }
    }
  };

  console.log(`\n${pc.green("Preview available work:")}`);
  printPreviewItems("Normal Tasks", normal);
  printPreviewItems("Bugs", bugs);
  printPreviewItems("Ideas", ideas);
  console.log(`\n${pc.dim("★ = On critical path")}`);
}

async function cmdShow(args: string[]): Promise<void> {
  const showLong = parseFlag(args, "--long");
  const showAll = parseFlag(args, "--all");
  const ids = positionalArgsForCommand(
    args,
    { "--long": "boolean", "--all": "boolean" },
    "show",
  );
  const loader = new TaskLoader();
  let tree: TaskTree | undefined = undefined;
  let fullTree: TaskTree | undefined = undefined;
  let idList = ids;
  if (!ids.length) {
    const current = await getCurrentTaskId();
    if (!current) {
      console.log("No task specified and no current working task set.");
      return;
    }
    idList = [current];
  }

  for (const id of idList) {
    const parsedScope = (() => {
      try {
        return TaskPath.parse(id);
      } catch (_error) {
        return null;
      }
    })();
    const scopeHint = parsedScope ? parsedScope.parent()?.fullId : undefined;

    // Check auxiliary IDs before TaskPath.parse.
    if (isBugId(id) || isIdeaId(id) || isFixedId(id)) {
      if (isFixedId(id)) {
        const fixed = await loader.findFixedTask(id);
        if (!fixed) textError(`Task not found: ${id}`);
        console.log(`${fixed.id}: ${fixed.title}\nstatus=${fixed.status} estimate=${fixed.estimateHours}`);
      } else {
        const auxTree = tree ?? (tree = await loader.load("metadata"));
        const aux = [...(auxTree.bugs ?? []), ...(auxTree.ideas ?? [])].find((t) => t.id === id);
        if (!aux) textError(`Task not found: ${id}`);
        console.log(`${aux.id}: ${aux.title}\nstatus=${aux.status} estimate=${aux.estimateHours}`);
        if (isIdeaId(id) && aux.status === Status.PENDING) {
          showIdeaInstructions(aux);
        }
      }
      continue;
    }
    if (!parsedScope) textError(`Invalid path format: ${id}`);
    const path = parsedScope as TaskPath;
    const scopeTree = path.isTask
      ? (tree ?? (tree = await loader.load("metadata", false, false)))
      : await loader.loadScope(id, "metadata", false, false, false);
    const searchTree = fullTree ?? (fullTree = await loader.load("metadata", false, false));

    if (path.isPhase) {
      const phase = findPhase(scopeTree, id);
      if (!phase) showNotFound("Phase", id, scopeHint);
      console.log(`${phase.id} ${phase.name}`);
      console.log(`Total Duration: ${getPhaseTotalEstimateHours(phase).toFixed(2)}h`);
      continue;
    }
    if (path.isMilestone) {
      const m = findMilestoneByScopedId(searchTree, id);
      if (!m) showNotFound("Milestone", id, scopeHint);
      console.log(`${m.id} ${m.name}`);
      console.log(`Total Duration: ${getMilestoneTotalEstimateHours(m).toFixed(2)}h`);
      continue;
    }
    if (path.isEpic) {
      const e = findEpicByScopedId(searchTree, id);
      if (!e) showNotFound("Epic", id, scopeHint);
      console.log(`${e.id} ${e.name}`);
      console.log(`Total Duration: ${getEpicTotalEstimateHours(e).toFixed(2)}h`);
      continue;
    }
    const t = findTask(scopeTree, id);
    if (!t) showNotFound("Task", id, scopeHint);
    console.log(`${t.id}: ${t.title}\nstatus=${t.status} estimate=${t.estimateHours}`);
    renderTaskDependencyOverview(t, fullTree ?? scopeTree);
    const taskFile = taskFilePath(t);
    const taskFilePathData = existsSync(taskFile) ? readFileSync(taskFile, "utf8") : null;
    if (taskFilePathData !== null) {
      const taskFileLineCount =
        taskFilePathData === "" ? 0 : taskFilePathData.replace(/\r?\n$/, "").split(/\r?\n/).length;
      const taskFileSize = statSync(taskFile).size;
      console.log(`File: ${taskFile}`);
      console.log(`File stats: ${taskFileSize} bytes, ${taskFileLineCount} lines`);
    } else {
      console.log(`File: ${taskFile}`);
    }
    const { warnings, missing: isMissing, body } = parseTodoFrontmatter(taskFile, t.id);
    printTodoFileWarnings(warnings);
    if (isMissing) {
      continue;
    }
    if (showAll) {
      console.log(pc.bold("Task file:"));
      if (taskFilePathData !== null) {
        const fullTaskFileText = taskFilePathData.replace(/\r?\n$/, "");
        for (const line of fullTaskFileText.split(/\r?\n/)) {
          console.log(`  ${line}`);
        }
      } else {
        console.log(pc.dim(`  Missing task file: ${taskFile}`));
      }
      continue;
    }

    const bodyLines = body.trim().split("\n");
    if (bodyLines.length > 0 && bodyLines[0] !== "") {
      console.log(pc.bold("Body:"));
      if (showLong) {
        for (const line of bodyLines) {
          console.log(`  ${line}`);
        }
      } else {
        const limit = Math.min(TASK_PREVIEW_LINES, bodyLines.length);
        for (const line of bodyLines.slice(0, limit)) {
          console.log(`  ${line}`);
        }
        if (bodyLines.length > limit) {
          console.log(pc.dim(`  ... (${bodyLines.length - limit} more lines)`));
          console.log(pc.dim(`To view the full file, run: cat ${taskFile}`));
        }
      }
    }
  }
}

function dependencyMarker(status?: Status): string {
  if (!status) {
    return pc.red("?");
  }
  return status === Status.DONE ? pc.green("✓") : pc.red("✗");
}

function renderTaskDependencyOverview(task: Task, tree: TaskTree): void {
  let rendered = false;

  if (task.dependsOn.length > 0) {
    console.log(pc.bold("Explicit dependencies:"));
    for (const depId of task.dependsOn) {
      const dep = findTask(tree, depId);
      if (dep) {
        console.log(`  ${dependencyMarker(dep.status)} ${dep.id} - ${dep.title} (${dep.status})`);
      } else {
        console.log(`  ${pc.red("?")} ${depId} (not found)`);
      }
    }
    rendered = true;
  } else if (task.epicId) {
    const epic = findEpic(tree, task.epicId);
    if (epic) {
      const taskIndex = epic.tasks.findIndex((candidate) => candidate.id === task.id);
      if (taskIndex > 0) {
        const prevTask = epic.tasks[taskIndex - 1];
        console.log(pc.bold("Implicit dependency (previous in epic):"));
        console.log(`  ${dependencyMarker(prevTask.status)} ${prevTask.id} - ${prevTask.title} (${prevTask.status})`);
        rendered = true;
      }
    }
  }

  if (rendered) {
    console.log(pc.dim("Legend: ✓ done | ✗ not done | ? not found"));
  }
}

function showNotFound(itemType: string, itemId: string, scopeHint?: string): never {
  console.error(pc.red(`Error: ${itemType} not found: ${itemId}`));
  if (scopeHint) {
    console.error(pc.yellow(`Tip: Use 'backlog tree ${scopeHint}' to verify available IDs.`));
  } else {
    console.error(pc.yellow("Tip: Use 'backlog tree' to list available IDs."));
  }
  process.exit(1);
}

function findClaimableTask(tree: TaskTree, id: string): Task | undefined {
  const task = findTask(tree, id);
  if (!task) return undefined;
  return task;
}

function taskCount(tasks: Array<{ status: Status }>): {
  done: number;
  inProgress: number;
  pending: number;
  blocked: number;
  total: number;
} {
  return tasks.reduce(
    (acc, t) => {
      acc.total += 1;
      if (t.status === Status.DONE) acc.done += 1;
      else if (t.status === Status.IN_PROGRESS) acc.inProgress += 1;
      else if (t.status === Status.BLOCKED) acc.blocked += 1;
      else acc.pending += 1;
      return acc;
    },
    { done: 0, inProgress: 0, pending: 0, blocked: 0, total: 0 },
  );
}

function taskSyncStats(tasks: Array<{ status: Status }>): {
  total_tasks: number;
  done: number;
  in_progress: number;
  blocked: number;
  pending: number;
} {
  const counts = taskCount(tasks);
  return {
    total_tasks: counts.total,
    done: counts.done,
    in_progress: counts.inProgress,
    blocked: counts.blocked,
    pending: counts.pending,
  };
}

function epicSyncStats(epic: Epic): {
  total: number;
  done: number;
  in_progress: number;
  blocked: number;
  pending: number;
} {
  const counts = taskCount(epic.tasks);
  return {
    total: counts.total,
    done: counts.done,
    in_progress: counts.inProgress,
    blocked: counts.blocked,
    pending: counts.pending,
  };
}

function findMilestoneByScope(tree: TaskTree, scopeId: string): Milestone | undefined {
  const direct = findMilestone(tree, scopeId);
  if (direct) return direct;
  const suffixed = scopeId.startsWith(".") ? scopeId : `.${scopeId}`;
  return tree.phases.flatMap((p) => p.milestones).find((m) => m.id.endsWith(suffixed));
}

function findEpicByScope(tree: TaskTree, scopeId: string): Epic | undefined {
  const direct = findEpic(tree, scopeId);
  if (direct) return direct;
  const suffixed = scopeId.startsWith(".") ? scopeId : `.${scopeId}`;
  return tree.phases
    .flatMap((p) => p.milestones.flatMap((m) => m.epics))
    .find((e) => e.id.endsWith(suffixed));
}

type CompletionNoticeStatus = {
  epic_completed: boolean;
  milestone_completed: boolean;
  phase_completed: boolean;
};

function printCompletionNotices(
  tree: TaskTree,
  task: Task,
  completion: CompletionNoticeStatus,
): void {
  if (!completion.epic_completed && !completion.milestone_completed && !completion.phase_completed) {
    return;
  }

  if (completion.epic_completed) {
    const epic = task.epicId ? findEpic(tree, task.epicId) : undefined;
    if (epic) {
      console.log("═".repeat(60));
      console.log(`${pc.yellow("🔍 EPIC COMPLETE:")} ${epic.name} (${epic.id})`);
      console.log("=".repeat(60));
      console.log("Review the completed epic before moving on.");
      console.log();
    }
  }

  if (completion.milestone_completed) {
    const milestone = task.milestoneId ? findMilestone(tree, task.milestoneId) : undefined;
    if (milestone) {
      if (milestone.epics.length > 1) {
        console.log("═".repeat(60));
        console.log(`${pc.green("🎯 MILESTONE COMPLETE:")} ${milestone.name} (${milestone.id})`);
        console.log("=".repeat(60));
        console.log("Review the completed milestone before moving on.");
        console.log();
      }
    }
  }

  if (completion.phase_completed) {
    const phase = task.phaseId ? findPhase(tree, task.phaseId) : undefined;
    if (phase) {
      console.log("═".repeat(60));
      console.log(`${pc.magenta("🏁 PHASE COMPLETE:")} ${phase.name} (${phase.id})`);
      console.log("=".repeat(60));
      console.log("Review the completed phase before moving on.");
      console.log();
    }
  }
}

function parseTodoForLs(task: Task): { frontmatter: Record<string, unknown>; body: string } {
  const taskPath = taskFilePath(task);
  return parseTodoFrontmatter(taskPath);
}

type TodoFileParseResult = {
  frontmatter: Record<string, unknown>;
  body: string;
  warnings: string[];
  missing: boolean;
};

function printTodoFileWarnings(warnings: string[]): void {
  for (const warning of warnings) {
    console.log(pc.yellow(`Warning: ${warning}`));
  }
}

function parseTodoFrontmatter(taskPath: string, taskId = ""): TodoFileParseResult {
  if (!existsSync(taskPath)) {
    const warningTarget = taskId || taskPath;
    return {
      frontmatter: {},
      body: "",
      warnings: [`Task file missing for ${warningTarget}: ${taskPath}`],
      missing: true,
    };
  }
  let content = "";
  try {
    content = readFileSync(taskPath, "utf8");
  } catch (error) {
    const warningTarget = taskId || taskPath;
    return {
      frontmatter: {},
      body: "",
      warnings: [`Task file for ${warningTarget} is not readable: ${String(error)}`],
      missing: false,
    };
  }

  const lines = content.split("\n");
  if (lines.length === 0 || lines[0]?.trim() !== "---") {
    return {
      frontmatter: {},
      body: content,
      warnings: [
        `Invalid frontmatter format in ${taskId || "task file"}: missing opening \`---\` marker.`,
      ],
      missing: false,
    };
  }

  let closingIndex = -1;
  for (let i = 1; i < lines.length; i += 1) {
    if (lines[i]?.trim() === "---") {
      closingIndex = i;
      break;
    }
  }
  if (closingIndex < 0) {
    return {
      frontmatter: {},
      body: content,
      warnings: [
        `Invalid frontmatter format in ${taskId || "task file"}: missing closing \`---\` marker.`,
      ],
      missing: false,
    };
  }

  const body = lines.slice(closingIndex + 1).join("\n");
  const frontmatterText = lines.slice(1, closingIndex).join("\n").trim();

  if (!frontmatterText) {
    return { frontmatter: {}, body, warnings: [], missing: false };
  }

  let parsed: unknown;
  try {
    parsed = parse(frontmatterText);
  } catch (error) {
    return {
      frontmatter: {},
      body,
      warnings: [
        `Invalid task file YAML in ${taskId || "task file"}: ${String(error)}`,
      ],
      missing: false,
    };
  }

  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    return {
      frontmatter: {},
      body,
      warnings: [`Invalid frontmatter in ${taskId || "task file"}: expected a mapping.`],
      missing: false,
    };
  }

  const frontmatter = parsed as Record<string, unknown>;
  const frontmatterId = String(frontmatter.id ?? "").trim();
  const warnings = [] as string[];
  if (taskId && frontmatterId && frontmatterId !== taskId) {
    warnings.push(`Frontmatter ID mismatch in ${taskId}: ${frontmatterId}`);
  }

  return {
    frontmatter: frontmatter,
    body,
    warnings,
    missing: false,
  };
}

async function summarizeFixes(): Promise<{ done: number; total: number }> {
  const fixesPath = join(getDataDirName(), "fixes", "index.yaml");
  if (!existsSync(fixesPath)) {
    return { done: 0, total: 0 };
  }

  const raw = parse(readFileSync(fixesPath, "utf8"));
  if (typeof raw !== "object" || raw === null) {
    return { done: 0, total: 0 };
  }

  const rawFixes = (raw as Record<string, unknown>).fixes;
  const fixes = Array.isArray(rawFixes) ? rawFixes : [];
  let done = 0;
  let total = 0;

  for (const rawFix of fixes) {
    if (!rawFix || typeof rawFix !== "object") {
      continue;
    }
    const fix = rawFix as Record<string, unknown>;
    const relativeFile = String(fix.file ?? "").trim();
    if (!relativeFile) {
      continue;
    }

    const fixPath = join(getDataDirName(), "fixes", relativeFile);
    if (!existsSync(fixPath)) {
      continue;
    }

    const { frontmatter } = parseTodoFrontmatter(fixPath);
    if (!frontmatter || Object.keys(frontmatter).length === 0) {
      continue;
    }

    total += 1;
    const status = String(frontmatter.status ?? "")
      .trim()
      .toLowerCase()
      .replace("-", "_")
      .replace(" ", "_");
    if (status === "done" || status === "complete" || status === "completed") {
      done += 1;
    }
  }

  return { done, total };
}

async function cmdLs(args: string[]): Promise<void> {
  const scopes = listScopeArgs(args);
  const loader = new TaskLoader();
  const includeAux = scopes.length === 0;
  const tree = await loader.load("metadata", includeAux, includeAux);

  if (!scopes.length) {
    if (!tree.phases.length) {
      console.log("No phases found.");
    } else {
      for (const phase of tree.phases) {
        const phaseTasks = phase.milestones.flatMap((m) => m.epics.flatMap((e) => e.tasks));
        const stats = taskCount(phaseTasks);
        console.log(
          `${phase.id}: ${phase.name} [${phase.status}] ${stats.done}/${stats.total} tasks done (in_progress=${stats.inProgress}, blocked=${stats.blocked})`,
        );
      }
    }

    const bugsDone = taskCount(tree.bugs ?? []).done;
    const ideasDone = taskCount(tree.ideas ?? []).done;
    const { done: fixesDone, total: fixesTotal } = await summarizeFixes();
    console.log(`Bugs (${bugsDone}/${(tree.bugs ?? []).length})`);
    console.log(`Ideas (${ideasDone}/${(tree.ideas ?? []).length})`);
    console.log(`Fixes (${fixesDone}/${fixesTotal})`);
    return;
  }

  for (const scope of scopes) {
    if (isBugId(scope) || isIdeaId(scope)) {
      textError(`ls does not support bug/idea IDs. Use: backlog show ${scope}`);
    }
    let parsed: TaskPath;
    try {
      parsed = TaskPath.parse(scope);
    } catch {
      textError(`Invalid path format: ${scope}`);
    }
    if (parsed.isPhase && !findPhase(tree, scope)) textError(`Phase not found: ${scope}`);
    if (parsed.isMilestone && !findMilestoneByScope(tree, scope)) textError(`Milestone not found: ${scope}`);
    if (parsed.isEpic && !findEpicByScope(tree, scope)) textError(`Epic not found: ${scope}`);
    if (parsed.isTask && !findTask(tree, scope)) textError(`Task not found: ${scope}`);
  }

  for (let i = 0; i < scopes.length; i += 1) {
    const scope = scopes[i]!;
    const parsed = TaskPath.parse(scope);
    if (i > 0) console.log("");

    if (parsed.isPhase) {
      const phase = findPhase(tree, scope)!;
      if (!phase.milestones.length) {
        console.log(`Phase ${scope} has no milestones.`);
        continue;
      }
      for (const milestone of phase.milestones) {
        const milestoneTasks = milestone.epics.flatMap((e) => e.tasks);
        const stats = taskCount(milestoneTasks);
        console.log(
          `${milestone.id}: ${milestone.name} [${milestone.status}] ${stats.done}/${stats.total} tasks done (in_progress=${stats.inProgress}, blocked=${stats.blocked})`,
        );
      }
      continue;
    }

    if (parsed.isMilestone) {
      const milestone = findMilestoneByScope(tree, scope)!;
      if (!milestone.epics.length) {
        console.log(`Milestone ${scope} has no epics.`);
        continue;
      }
      for (const epic of milestone.epics) {
        const stats = taskCount(epic.tasks);
        console.log(
          `${epic.id}: ${epic.name} [${epic.status}] ${stats.done}/${stats.total} tasks done (in_progress=${stats.inProgress}, blocked=${stats.blocked})`,
        );
      }
      continue;
    }

    if (parsed.isEpic) {
      const epic = findEpicByScope(tree, scope)!;
      if (!epic.tasks.length) {
        console.log(`Epic ${scope} has no tasks.`);
        continue;
      }
      for (const task of epic.tasks) {
        console.log(`${task.id}: ${task.title} [${task.status}] ${task.estimateHours}h`);
      }
      continue;
    }

    const task = findTask(tree, scope)!;
    const { frontmatter, body } = parseTodoForLs(task);
    console.log(`Task: ${task.id} - ${task.title}`);
    console.log("Frontmatter:");
    if (Object.keys(frontmatter).length > 0) {
      console.log(stringify(frontmatter).trim());
    } else {
      console.log("  (unavailable)");
    }
    console.log(`Body length: ${body.length}`);
    console.log(`Run 'backlog show ${task.id}' for full details.`);
  }
}

function showIdeaInstructions(idea: Task): void {
  const dataDir = getDataDirName();
  console.log(`\nInstructions:`);
  console.log(`  1. Read the idea file at ${dataDir}/${idea.file}`);
  console.log(`  2. Understand the idea in the context of the codebase`);
  console.log(`  3. Plan out a solution design`);
  console.log(`  4. Create phases, milestones, epics, and tasks as appropriate using:`);
  console.log(`     - \`tasks add-phase\` for new phases`);
  console.log(`     - \`tasks add-milestone\` for milestones within phases`);
  console.log(`     - \`tasks add-epic\` for epics within milestones`);
  console.log(`     - \`tasks add\` for tasks within epics`);
  console.log(`  5. Update this idea's "Created Work Items" section with the new IDs`);
  console.log(`  6. Mark this idea as done when planning is complete`);
}

async function cmdClaim(args: string[]): Promise<AutoCommitMetadata> {
  const taskIds: string[] = [];
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i]!;
    if (arg === "--agent") {
      i += 1;
      continue;
    }
    if (arg.startsWith("--agent=")) continue;
    if (arg.startsWith("-")) continue;
    taskIds.push(arg);
  }
  if (taskIds.length === 0) textError("claim requires at least one TASK_ID");
  const agent = parseOpt(args, "--agent") ?? ((loadConfig().agent as Record<string, unknown>)?.default_agent as string) ?? "cli-user";
  const force = parseFlag(args, "--force");
  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const showDetails = taskIds.length === 1;
  let metadata: AutoCommitMetadata | null = null;
  let hasSetCurrent = false;
  for (const taskId of taskIds) {
    const task = findClaimableTask(tree, taskId);
    if (!task) {
      console.log(pc.yellow("Warning: claim only works with task IDs."));
      console.log(pc.yellow(`Showing \`backlog show ${taskId}\` for context.`));
      await cmdShow([taskId]);
      continue;
    }
    const taskPath = taskFilePath(task);
    const { warnings, missing: isMissing } = parseTodoFrontmatter(taskPath, task.id);
    printTodoFileWarnings(warnings);
    if (isMissing) {
      textError(`Cannot claim ${task.id} because the task file is missing.`);
    }

    try {
      claimTask(task, agent, force);
      await loader.saveTask(task);
      if (!metadata) {
        metadata = { id: task.id, title: task.title };
      }
      if (!hasSetCurrent) {
        await setCurrentTask(task.id, agent);
        hasSetCurrent = true;
      }
      if (showDetails) {
        console.log(`Claimed: ${task.id}`);
      } else {
        console.log(`${pc.green("✓ Claimed:")} ${task.id} - ${task.title}`);
      }
    } catch (e) {
      if (e instanceof StatusError) {
        jsonOut(e.toJSON());
        process.exit(1);
      }
      throw e;
    }
  }

  return metadata ?? { id: "", title: "" };
}

async function cmdGrab(args: string[]): Promise<AutoCommitMetadata> {
  const agent = parseOpt(args, "--agent") ?? ((loadConfig().agent as Record<string, unknown>)?.default_agent as string) ?? "cli-user";
  const scopes = parseOpts(args, "--scope").map((s) => s.trim()).filter((s) => s.length > 0);
  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  let metadata: AutoCommitMetadata | null = null;

  // Check for positional task IDs (excluding option values)
  const taskIds: string[] = [];
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i]!;
    if (arg === "--agent" || arg === "--scope") {
      i += 1;
      continue;
    }
    if (arg.startsWith("--agent=")) continue;
    if (arg.startsWith("--scope=")) continue;
    if (arg.startsWith("-")) continue;
    taskIds.push(arg);
  }
  if (taskIds.length > 0) {
    const claimedTasks: Task[] = [];
    for (const tid of taskIds) {
      const task = findTask(tree, tid);
      if (!task) textError(`Task not found: ${tid}`);
      if (isTaskFileMissing(task)) textError(`Cannot claim ${tid} because the task file is missing.`);
      try {
        claimTask(task, agent, false);
        await loader.saveTask(task);
        if (!metadata) {
          metadata = { id: task.id, title: task.title };
        }
        claimedTasks.push(task);
        console.log(`${pc.green("✓ Claimed:")} ${task.id} - ${task.title}`);
      } catch (e) {
        if (e instanceof StatusError) {
          jsonOut(e.toJSON());
          process.exit(1);
        }
        throw e;
      }
    }
    if (claimedTasks.length > 0) {
      const primary = claimedTasks[0]!;
      await setCurrentTask(primary.id, agent);
      console.log(`\n${pc.bold("Working on:")} ${primary.id}`);
    }
    return metadata ?? { id: "", title: "" };
  }

  if (scopes.length > 0) {
    validateScopeTokens(tree, scopes);
  }

  // Auto-select next available
  const cfg = loadConfig();
  const calc = new CriticalPathCalculator(tree, (cfg.complexity_multipliers as Record<string, number>) ?? {});
  const { criticalPath, nextAvailable } = calc.calculate();
  if (!nextAvailable) {
    console.log("No available tasks found.");
    return metadata ?? { id: "", title: "" };
  }
  let selectedTaskId = nextAvailable;
  if (scopes.length > 0) {
    const available = calc.findAllAvailable();
    const filtered = available.filter((taskId) => scopes.some((scope) => taskId.startsWith(scope)));
    const prioritized = calc.prioritizeTaskIds(filtered, criticalPath);
    if (!prioritized.length) {
      console.log(`No available tasks in scope '${scopes.join(", ")}'`);
      return metadata ?? { id: "", title: "" };
    }
    selectedTaskId = prioritized[0]!;
  }

  const task = findTask(tree, selectedTaskId);
  if (!task) textError(`Task not found: ${nextAvailable}`);
  if (isTaskFileMissing(task)) textError(`Cannot claim ${task.id} because the task file is missing.`);

  claimTask(task, agent, false);
  await loader.saveTask(task);
  if (!metadata) {
    metadata = { id: task.id, title: task.title };
  }
  await setCurrentTask(task.id, agent);
  console.log(`Grabbed: ${task.id} - ${task.title}`);

  if (/^B\d+$/.test(task.id)) {
    const additionalBugIds = calc.findAdditionalBugs(task, 2);
    const additionalBugs: Task[] = [];
    for (const id of additionalBugIds) {
      const bug = findTask(tree, id);
      if (!bug) continue;
      if (isTaskFileMissing(bug)) continue;
      claimTask(bug, agent, false);
      await loader.saveTask(bug);
      additionalBugs.push(bug);
    }
    if (additionalBugs.length > 0) {
      console.log(`Also grabbed ${additionalBugs.length} additional bug(s): ${additionalBugs.map((b) => b.id).join(", ")}`);
      console.log("Run these bug fixes in parallel with multiple subagents when independent.");
      console.log(`Mark each done individually: ${[task, ...additionalBugs].map((b) => `backlog done ${b.id}`).join(" | ")}`);
    }
  }

  return metadata ?? { id: "", title: "" };
}

async function cmdDone(args: string[]): Promise<AutoCommitMetadata> {
  const taskIds = args.filter((a) => !a.startsWith("-"));
  const force = parseFlag(args, "--force");
  if (taskIds.length === 0) {
    const currentTaskId = await getCurrentTaskId();
    if (!currentTaskId) textError("No task ID provided and no current working task set.");
    taskIds.push(currentTaskId);
  }

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  let metadata: AutoCommitMetadata | null = null;

  try {
    for (const taskId of taskIds) {
      const task = findTask(tree, taskId);
      if (!task) textError(`Task not found: ${taskId}`);
      if (task.startedAt) {
        task.durationMinutes = (utcNow().getTime() - task.startedAt.getTime()) / 60000;
      }
      completeTask(task, force);
      await loader.saveTask(task);
      if (!metadata) {
        metadata = { id: task.id, title: task.title };
      }
      console.log(`Completed: ${task.id}`);

      const completionStatus = await loader.setItemDone(task.id);
      const refreshed = await loader.load("metadata");
      printCompletionNotices(refreshed, task, completionStatus);
    }
  } catch (e) {
    if (e instanceof StatusError) {
      jsonOut(e.toJSON());
      process.exit(1);
    }
    throw e;
  }

  return metadata ?? { id: "", title: "" };
}

async function cmdUndone(args: string[]): Promise<AutoCommitMetadata> {
  const itemId = args.find((a) => !a.startsWith("-"));
  if (!itemId) textError("undone requires ITEM_ID");

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const task = findTask(tree, itemId);
  const result = await loader.setItemNotDone(itemId);
  console.log(`Marked not done: ${result.item_id}`);
  console.log(`Reset tasks: ${result.updated_tasks}`);
  return { id: result.item_id, title: task ? task.title : "" };
}

async function cmdCycle(args: string[]): Promise<void> {
  let taskId = args.find((a) => !a.startsWith("-"));
  if (!taskId) taskId = await getCurrentTaskId();
  if (!taskId) textError("No task ID provided and no current working task set.");
  const agent = parseOpt(args, "--agent") ?? ((loadConfig().agent as Record<string, unknown>)?.default_agent as string) ?? "cli-user";

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const task = findTask(tree, taskId);
  if (!task) textError(`Task not found: ${taskId}`);

  try {
    if (task.startedAt) {
      task.durationMinutes = (utcNow().getTime() - task.startedAt.getTime()) / 60000;
    }
    completeTask(task);
    await loader.saveTask(task);
    console.log(`Completed: ${task.id}`);
  } catch (e) {
    if (e instanceof StatusError) {
      jsonOut(e.toJSON());
      process.exit(1);
    }
    throw e;
  }

  const completionStatus = await loader.setItemDone(task.id);
  const completionTree = await loader.load("metadata");
  printCompletionNotices(completionTree, task, completionStatus);

  if (
    completionStatus.epic_completed ||
    completionStatus.milestone_completed ||
    completionStatus.phase_completed
  ) {
    await clearContext();
    console.log("Review Required");
    console.log("Please review the completed work before continuing.");
    console.log("Run backlog grab once review is complete.");
    return;
  }

  const cfg = loadConfig();
  const calc = new CriticalPathCalculator(completionTree, (cfg.complexity_multipliers as Record<string, number>) ?? {});
  const { nextAvailable } = calc.calculate();
  if (!nextAvailable) {
    await clearContext();
    console.log("No more available tasks.");
    return;
  }
  const nextTask = findTask(completionTree, nextAvailable);
  if (!nextTask) {
    await clearContext();
    console.log("No more available tasks.");
    return;
  }
  claimTask(nextTask, agent, false);
  await loader.saveTask(nextTask);
  await setCurrentTask(nextTask.id, agent);
  console.log(`Grabbed: ${nextTask.id} - ${nextTask.title}`);

  if (/^B\d+$/.test(nextTask.id)) {
    const additionalBugIds = calc.findAdditionalBugs(nextTask, 2);
    const additionalBugs: Task[] = [];
    for (const id of additionalBugIds) {
      const bug = findTask(completionTree, id);
      if (!bug) continue;
      if (isTaskFileMissing(bug)) continue;
      claimTask(bug, agent, false);
      await loader.saveTask(bug);
      additionalBugs.push(bug);
    }
    if (additionalBugs.length > 0) {
      console.log(`Also grabbed ${additionalBugs.length} additional bug(s): ${additionalBugs.map((b) => b.id).join(", ")}`);
      console.log("Run these bug fixes in parallel with multiple subagents when independent.");
      console.log(`Mark each done individually: ${[nextTask, ...additionalBugs].map((b) => `backlog done ${b.id}`).join(" | ")}`);
    }
  }
}

async function cmdUpdate(args: string[]): Promise<void> {
  const pos = args.filter((a) => !a.startsWith("-"));
  const taskId = pos[0];
  const newStatus = pos[1];
  if (!taskId || !newStatus) textError("update requires TASK_ID STATUS");
  const reason = parseOpt(args, "--reason");
  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const task = findTask(tree, taskId);
  if (!task) textError(`Task not found: ${taskId}`);

  try {
    updateStatus(task, newStatus as Status, reason);
    await loader.saveTask(task);
    console.log(`Updated: ${task.id} -> ${task.status}`);
  } catch (e) {
    if (e instanceof StatusError) {
      jsonOut(e.toJSON());
      process.exit(1);
    }
    throw e;
  }
}

async function cmdSet(args: string[]): Promise<AutoCommitMetadata> {
  const taskId = args.find((a) => !a.startsWith("-"));
  if (!taskId) textError("set requires TASK_ID");

  const statusValue = parseOpt(args, "--status");
  const priority = parseOpt(args, "--priority");
  const complexity = parseOpt(args, "--complexity");
  const estimateText = parseOpt(args, "--estimate");
  const title = parseOpt(args, "--title");
  const dependsOnText = parseOpt(args, "--depends-on");
  const tagsText = parseOpt(args, "--tags");
  const reason = parseOpt(args, "--reason");
  const body = parseOpt(args, "--body") ?? parseOpt(args, "-b");
  const appendBody = parseFlag(args, "--append-body");

  const hasAny =
    statusValue !== undefined ||
    priority !== undefined ||
    complexity !== undefined ||
    estimateText !== undefined ||
    title !== undefined ||
    dependsOnText !== undefined ||
    tagsText !== undefined ||
    body !== undefined ||
    appendBody;
  if (appendBody && body === undefined) {
    textError("--append-body requires --body");
  }
  if (!hasAny) textError("set requires at least one property flag");

  const allowedPriority = new Set(Object.values(Priority));
  const allowedComplexity = new Set(Object.values(Complexity));
  const allowedStatus = new Set(Object.values(Status));

  if (priority && !allowedPriority.has(priority as Priority)) {
    textError(`Invalid priority: ${priority}`);
  }
  if (complexity && !allowedComplexity.has(complexity as Complexity)) {
    textError(`Invalid complexity: ${complexity}`);
  }
  if (statusValue && !allowedStatus.has(statusValue as Status)) {
    textError(`Invalid status: ${statusValue}`);
  }

  let estimate: number | undefined;
  if (estimateText !== undefined) {
    estimate = Number(estimateText);
    if (!Number.isFinite(estimate)) {
      textError(`Invalid estimate: ${estimateText}`);
    }
  }

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const task = findTask(tree, taskId);
  if (!task) textError(`Task not found: ${taskId}`);

  if (title !== undefined) task.title = title;
  if (estimate !== undefined) task.estimateHours = estimate;
  if (complexity !== undefined) task.complexity = complexity as Complexity;
  if (priority !== undefined) task.priority = priority as Priority;
  if (dependsOnText !== undefined) task.dependsOn = parseDependencyList(dependsOnText);
  if (tagsText !== undefined) task.tags = parseCsv(tagsText);

  try {
    if (statusValue !== undefined) {
      updateStatus(task, statusValue as Status, reason);
    }
    if (body !== undefined) {
      await loader.saveTask(task, body, appendBody);
    } else {
      await loader.saveTask(task);
    }
    console.log(`Updated: ${task.id}`);
  } catch (e) {
    if (e instanceof StatusError) {
      jsonOut(e.toJSON());
      process.exit(1);
    }
    throw e;
  }

  const metadata: AutoCommitMetadata = { id: task.id, title: task.title };
  return metadata;
}

async function cmdWork(args: string[]): Promise<void> {
  const commandArgs = positionalArgsForCommand(
    args,
    {
      "--agent": "value",
      "--clear": "boolean",
      "--help": "boolean",
      "-h": "boolean",
    },
    "work",
  );
  const clear = parseFlag(args, "--clear");
  const taskId = commandArgs[0];
  if (clear && commandArgs.length > 0) {
    printCommandHelpForCommand("work");
    textError("work --clear does not accept a TASK_ID");
  }
  if (commandArgs.length > 1) {
    printCommandHelpForCommand("work");
    textError("work accepts at most one TASK_ID");
  }

  if (parseFlag(args, "--help", "-h")) {
    printCommandHelpForCommand("work");
    return;
  }
  if (clear) {
    await clearContext();
    console.log("Cleared working task context.");
    return;
  }

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");

  if (taskId) {
    const agent = parseOpt(args, "--agent")
      ?? ((loadConfig().agent as Record<string, unknown>)?.default_agent as string)
      ?? "cli-user";
    const task = findTask(tree, taskId);
    if (!task) textError(`Task not found: ${taskId}`);
    await setCurrentTask(taskId, agent);
    console.log(`Working task set: ${task.id} - ${task.title}`);
    return;
  }

  const ctx = await loadContext();
  const current = (ctx.current_task as string | undefined) ?? (ctx.primary_task as string | undefined);
  if (!current) {
    console.log("No current working task set.");
    return;
  }
  const task = findTask(tree, current);
  if (!task) {
    console.log(`Working task '${current}' not found in tree.`);
    return;
  }
  console.log("Current Working Task");
  console.log(`ID: ${task.id}`);
  console.log(`Title: ${task.title}`);
  console.log(`Status: ${task.status}`);
  console.log(`Estimate: ${task.estimateHours} hours`);
  console.log(`File: ${getDataDirName()}/${task.file}`);
}

async function cmdCI(args: string[]): Promise<void> {
  if (parseFlag(args, "--help", "-h")) {
    printCommandHelpForCommand("ci");
    return;
  }

  const commandArgs: string[] = [];
  for (const arg of args) {
    if (arg === "--help" || arg === "-h") continue;
    if (arg.startsWith("-")) {
      textError(`unexpected flag: ${arg}`);
    }
    commandArgs.push(arg);
  }

  if (commandArgs.length === 0) {
    textError("ci requires an action: validate-ids");
  }

  const action = commandArgs[0] ?? "";
  if (action !== "validate-ids") {
    textError(`unknown ci action: ${action}`);
  }

  const ids = commandArgs.slice(1);
  if (ids.length === 0) {
    textError("validate-ids requires at least one TASK_ID");
  }
  for (const id of ids) {
    if (!isValidTaskId(id)) {
      textError(`Invalid TASK_ID: ${id}`);
    }
  }
}

async function cmdVersion(args: string[]): Promise<void> {
  const positional = positionalArgsForCommand(
    args,
    {
      "--help": "boolean",
      "-h": "boolean",
    },
    "version",
  );
  if (parseFlag(args, "--help", "-h")) {
    printCommandHelpForCommand("version");
    return;
  }
  if (positional.length > 0) {
    printCommandHelpForCommand("version");
    textError("version accepts no TASK_ID arguments");
  }
  console.log("backlog version 0.1.0");
}

async function cmdUnclaim(args: string[]): Promise<AutoCommitMetadata> {
  let taskId = args.find((a) => !a.startsWith("-"));
  if (!taskId) taskId = await getCurrentTaskId();
  if (!taskId) textError("No task ID provided and no current working task set.");

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const task = findTask(tree, taskId);
  if (!task) textError(`Task not found: ${taskId}`);
  if (task.status !== Status.IN_PROGRESS && task.status !== Status.PENDING) {
    console.log(`Task is not in progress: ${task.status}`);
    return { id: "", title: "" };
  }
  if (task.status === Status.IN_PROGRESS) {
    updateStatus(task, Status.PENDING, "unclaim");
  } else if (!task.claimedBy && !task.claimedAt) {
    console.log(`Task is not in progress: ${task.status}`);
    return { id: "", title: "" };
  } else {
    task.claimedBy = undefined;
    task.claimedAt = undefined;
  }
  await loader.saveTask(task);
  await clearContext();
  console.log(`Unclaimed: ${task.id} - ${task.title}`);
  return { id: task.id, title: task.title };
}

async function cmdEdit(args: string[]): Promise<AutoCommitMetadata> {
  if (parseFlag(args, "--help", "-h")) {
    printCommandHelpForCommand("edit");
    return { id: "", title: "" };
  }

  const taskIds = positionalArgsForCommand(
    args,
    {
      "--help": "boolean",
      "-h": "boolean",
    },
    "edit",
  );
  if (taskIds.length !== 1) textError("edit requires exactly one TASK_ID");

  const taskId = taskIds[0];
  if (!taskId || !isValidTaskId(taskId)) {
    textError(`malformed task id: ${taskId}`);
  }

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const task = findTask(tree, taskId);
  if (!task) {
    console.log(pc.yellow("Warning: edit only works with task IDs."));
    console.log(pc.yellow(`Showing \`backlog show ${taskId}\` for context.`));
    await cmdShow([taskId]);
    return { id: "", title: "" };
  }

  if (isTaskFileMissing(task)) textError(`Cannot edit ${task.id} because the task file is missing.`);

  const editor = process.env.VISUAL?.trim() || process.env.EDITOR?.trim();
  if (!editor) {
    textError("No editor configured. Set EDITOR or VISUAL.");
  }

  const editorParts = editor.split(/\s+/).filter(Boolean);
  if (editorParts.length === 0) {
    textError("No editor configured. Set EDITOR or VISUAL.");
  }

  const editorCommand = editorParts[0];
  if (!editorCommand) {
    textError("No editor configured. Set EDITOR or VISUAL.");
  }

  const commandArgs = [...editorParts.slice(1), taskFilePath(task)];
  let result: ReturnType<typeof Bun.spawnSync>;
  try {
    result = Bun.spawnSync([editorCommand, ...commandArgs], {
      stdout: "inherit",
      stderr: "inherit",
      stdin: "inherit",
    });
  } catch (error) {
    textError(`${error}`);
  }

  if (result.exitCode !== 0) {
    textError(`editor exited with status ${result.exitCode}`);
  }

  return { id: task.id, title: task.title };
}

async function cmdBlocked(args: string[]): Promise<void> {
  const grab = parseFlag(args, "--grab");
  let taskId = args.find((a) => !a.startsWith("-"));
  const reason = parseOpt(args, "--reason") ?? parseOpt(args, "-r");
  if (!reason) textError("blocked requires --reason");
  if (!taskId) taskId = await getCurrentTaskId();
  if (!taskId) textError("No task ID provided and no current working task set.");
  const agent = parseOpt(args, "--agent") ?? ((loadConfig().agent as Record<string, unknown>)?.default_agent as string) ?? "cli-user";

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const task = findTask(tree, taskId);
  if (!task) textError(`Task not found: ${taskId}`);
  updateStatus(task, Status.BLOCKED, reason);
  await loader.saveTask(task);
  await clearContext();
  console.log(`Blocked: ${task.id} (${reason})`);

  if (!grab) {
    console.log("Tip: Run `backlog grab` to claim the next available task.");
    return;
  }

  const refreshed = await loader.load("metadata");
  const cfg = loadConfig();
  const calc = new CriticalPathCalculator(refreshed, (cfg.complexity_multipliers as Record<string, number>) ?? {});
  const { nextAvailable } = calc.calculate();
  if (!nextAvailable) {
    console.log("No available tasks found.");
    return;
  }
  const nextTask = findTask(refreshed, nextAvailable);
  if (!nextTask) {
    console.log("No available tasks found.");
    return;
  }
  if (isTaskFileMissing(nextTask)) {
    console.log(`Skipping auto-grab: ${nextTask.id} has no task file.`);
    return;
  }
  claimTask(nextTask, agent, false);
  await loader.saveTask(nextTask);
  await setCurrentTask(nextTask.id, agent);
  console.log(`Grabbed: ${nextTask.id} - ${nextTask.title}`);
}

async function cmdSync(): Promise<void> {
  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const cfg = loadConfig();
  const calc = new CriticalPathCalculator(tree, (cfg.complexity_multipliers as Record<string, number>) ?? {});
  const { criticalPath, nextAvailable } = calc.calculate();

  const dataDir = getDataDirName();
  const rootPath = join(dataDir, "index.yaml");
  const root = parse(readFileSync(rootPath, "utf8")) as Record<string, unknown>;
  root.critical_path = criticalPath;
  root.next_available = nextAvailable ?? null;
  root.stats = {
    total_tasks: getAllTasks(tree).length,
    done: getAllTasks(tree).filter((t) => t.status === Status.DONE).length,
    in_progress: getAllTasks(tree).filter((t) => t.status === Status.IN_PROGRESS).length,
    blocked: getAllTasks(tree).filter((t) => t.status === Status.BLOCKED).length,
    pending: getAllTasks(tree).filter((t) => t.status === Status.PENDING).length,
  };
  Bun.write(rootPath, stringify(root));

  for (const phase of tree.phases) {
    const phaseIndexPath = join(dataDir, phase.path, "index.yaml");
    if (!existsSync(phaseIndexPath)) {
      continue;
    }
    const phaseIndex = (parse(await Bun.file(phaseIndexPath).text()) as Record<string, unknown>) ?? {};
    phaseIndex.stats = taskSyncStats(phase.milestones.flatMap((m) => m.epics.flatMap((e) => e.tasks)));
    Bun.write(phaseIndexPath, stringify(phaseIndex));

    for (const milestone of phase.milestones) {
      const milestoneIndexPath = join(dataDir, phase.path, milestone.path, "index.yaml");
      if (existsSync(milestoneIndexPath)) {
        const milestoneIndex = (parse(
          await Bun.file(milestoneIndexPath).text(),
        ) as Record<string, unknown>) ?? {};
        milestoneIndex.stats = taskSyncStats(
          milestone.epics.flatMap((epic) => epic.tasks),
        );
        Bun.write(milestoneIndexPath, stringify(milestoneIndex));
      }

      for (const epic of milestone.epics) {
        const epicIndexPath = join(
          dataDir,
          phase.path,
          milestone.path,
          epic.path,
          "index.yaml",
        );
        if (!existsSync(epicIndexPath)) {
          continue;
        }
        const epicIndex = (parse(await Bun.file(epicIndexPath).text()) as Record<string, unknown>) ?? {};
        epicIndex.stats = epicSyncStats(epic);
        Bun.write(epicIndexPath, stringify(epicIndex));
      }
    }
  }

  console.log("Synced");
}

function formatDuration(minutes: number): string {
  if (minutes < 60) return `${minutes}m`;
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return m ? `${h}h ${m}m` : `${h}h`;
}

export function computeStats(tree: { phases: Array<{ milestones: Array<{ epics: Array<{ tasks: Array<{ status: Status }> }> }> }> }): {
  total_tasks: number;
  done: number;
  in_progress: number;
  pending: number;
  blocked: number;
} {
  const all = tree.phases.flatMap((p) => p.milestones.flatMap((m) => m.epics.flatMap((e) => e.tasks)));
  return {
    total_tasks: all.length,
    done: all.filter((t) => t.status === Status.DONE).length,
    in_progress: all.filter((t) => t.status === Status.IN_PROGRESS).length,
    pending: all.filter((t) => t.status === Status.PENDING).length,
    blocked: all.filter((t) => t.status === Status.BLOCKED).length,
  };
}

async function cmdData(args: string[]): Promise<void> {
  const sub = args[0];
  const rest = args.slice(1);
  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const scopes = parseOpts(rest, "--scope").map((s) => s.trim()).filter((s) => s.length > 0);

  if (!sub || sub === "--help") {
    console.log("Usage: backlog data <summary|export> [options]");
    return;
  }

  if (sub === "summary") {
    const outputFormat = parseOpt(rest, "--format") ?? "text";
    const stats = computeStats(tree);
    const total = stats.total_tasks;
    const done = stats.done;
    const pct = total > 0 ? (done / total) * 100 : 0;
    const summary = {
      project: tree.project,
      timestamp: new Date().toISOString(),
      overall: {
        total_tasks: total,
        done,
        in_progress: stats.in_progress,
        pending: stats.pending,
        blocked: stats.blocked,
        percent_complete: Number(pct.toFixed(1)),
      },
      phases: tree.phases.map((p) => {
        const tasks = p.milestones.flatMap((m) => m.epics.flatMap((e) => e.tasks));
        const pDone = tasks.filter((t) => t.status === Status.DONE).length;
        const pTotal = tasks.length;
        return {
          id: p.id,
          name: p.name,
          done: pDone,
          total: pTotal,
          percent_complete: Number((pTotal ? (pDone / pTotal) * 100 : 0).toFixed(1)),
        };
      }),
    };
    if (outputFormat === "json") {
      jsonOut(summary);
      return;
    }
    console.log(tree.project);
    console.log(`Overall: ${done}/${total} tasks (${pct.toFixed(1)}%)`);
    return;
  }

  if (sub === "export") {
    if (scopes.length > 0) {
      validateScopeTokens(tree, scopes);
    }
    const outputFormat = parseOpt(rest, "--format") ?? "json";
    const output = parseOpt(rest, "--output") ?? parseOpt(rest, "-o");
    const includeContent = parseFlag(rest, "--include-content");
    const pretty = !rest.includes("--pretty=false");
    const stats = computeStats(tree);
    const exportData = {
      exported_at: new Date().toISOString(),
      project: tree.project,
      description: tree.description ?? "",
      timeline_weeks: tree.timelineWeeks ?? 0,
      stats,
      phases: tree.phases
        .filter((p) => (scopes.length > 0 ? anyScopeMatch(p.id, scopes) : true))
        .map((p) => ({
          id: p.id,
          name: p.name,
          path: p.path,
          status: p.status,
          weeks: p.weeks,
          estimate_hours: p.estimateHours,
          priority: p.priority,
          depends_on: p.dependsOn,
          milestones: p.milestones
            .filter((m) => (scopes.length > 0 ? anyScopeMatch(m.id, scopes) : true))
            .map((m) => ({
              id: m.id,
              name: m.name,
              path: m.path,
              status: m.status,
              estimate_hours: m.estimateHours,
              complexity: m.complexity,
              depends_on: m.dependsOn,
              epics: m.epics
                .filter((e) => (scopes.length > 0 ? anyScopeMatch(e.id, scopes) : true))
                .map((e) => ({
                  id: e.id,
                  name: e.name,
                  path: e.path,
                  status: e.status,
                  estimate_hours: e.estimateHours,
                  complexity: e.complexity,
                  depends_on: e.dependsOn,
                  tasks: e.tasks
                    .filter((t) => (scopes.length > 0 ? scopes.some((scope) => t.id.startsWith(scope)) : true))
                    .map((t) => ({
                      id: t.id,
                      title: t.title,
                      file: t.file,
                      status: t.status,
                      estimate_hours: t.estimateHours,
                      complexity: t.complexity,
                      priority: t.priority,
                      depends_on: t.dependsOn,
                      tags: t.tags,
                      claimed_by: t.claimedBy ?? null,
                      claimed_at: t.claimedAt?.toISOString() ?? null,
                      started_at: t.startedAt?.toISOString() ?? null,
                      completed_at: t.completedAt?.toISOString() ?? null,
                      duration_minutes: t.durationMinutes ?? null,
                      ...(includeContent
                        ? { content: existsSync(join(getDataDirName(), t.file)) ? readFileSync(join(getDataDirName(), t.file), "utf8") : null }
                        : {}),
                    })),
                })),
            })),
        })),
    };
    const rendered =
      outputFormat === "yaml"
        ? stringify(exportData)
        : JSON.stringify(exportData, null, pretty ? 2 : undefined);
    if (output) {
      await Bun.write(output, rendered);
      console.log(`Exported to ${output}`);
      return;
    }
    console.log(rendered);
    return;
  }

  textError(`Unknown data subcommand: ${sub}`);
}

// cmdReport is extracted to report.ts
import { cmdReport } from "./report";
import { cmdDash } from "./display";

async function cmdTimeline(args: string[]): Promise<void> {
  const scopes = parseOpts(args, "--scope").map((s) => s.trim()).filter((s) => s.length > 0);
  const groupBy = parseOpt(args, "--group-by") ?? "milestone";
  const showDone = parseFlag(args, "--show-done");
  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  if (scopes.length > 0) {
    validateScopeTokens(tree, scopes);
  }
  const cfg = loadConfig();
  const calc = new CriticalPathCalculator(tree, (cfg.complexity_multipliers as Record<string, number>) ?? {});
  const { criticalPath } = calc.calculate();
  let tasks = getAllTasks(tree);
  if (scopes.length > 0) tasks = tasks.filter((t) => scopes.some((scope) => t.id.startsWith(scope)));
  if (!showDone) tasks = tasks.filter((t) => t.status !== Status.DONE);
  if (!tasks.length) {
    console.log("No tasks to display.");
    return;
  }

  const groups = new Map<string, typeof tasks>();
  for (const t of tasks) {
    const key = groupBy === "phase" ? t.phaseId : groupBy === "epic" ? t.epicId : groupBy === "status" ? t.status : t.milestoneId;
    const label = key ?? "Unknown";
    if (!groups.has(label)) groups.set(label, []);
    groups.get(label)!.push(t);
  }
  console.log("Project Timeline");
  for (const [label, group] of groups.entries()) {
    console.log(label);
    for (const t of group) {
      const crit = criticalPath.includes(t.id) ? "*" : " ";
      console.log(`  ${crit} ${t.id} ${t.status} ${t.title}`);
    }
  }
}

async function cmdSchema(args: string[]): Promise<void> {
  const asJson = parseFlag(args, "--json");
  const compact = parseFlag(args, "--compact");
  const spec = {
    schema_version: 1,
    scope: "file-kinds",
    enums: {
      status: Object.values(Status),
      complexity: ["low", "medium", "high", "critical"],
      priority: ["critical", "high", "medium", "low"],
      context_mode: ["single", "multi", "siblings"],
    },
    files: [
      { name: "Root index", path_pattern: `${getDataDirName()}/index.yaml`, format: "yaml" },
      { name: "Phase index", path_pattern: `${getDataDirName()}/<phase-path>/index.yaml`, format: "yaml" },
      { name: "Milestone index", path_pattern: `${getDataDirName()}/<phase-path>/<milestone-path>/index.yaml`, format: "yaml" },
      { name: "Epic index", path_pattern: `${getDataDirName()}/<phase-path>/<milestone-path>/<epic-path>/index.yaml`, format: "yaml" },
      { name: "Task file", path_pattern: `${getDataDirName()}/<phase-path>/<milestone-path>/<epic-path>/T###-*.todo`, format: "markdown-with-yaml-frontmatter" },
      { name: "Context file", path_pattern: `${getDataDirName()}/.context.yaml`, format: "yaml" },
      { name: "Sessions file", path_pattern: `${getDataDirName()}/.sessions.yaml`, format: "yaml" },
      { name: "Config file", path_pattern: `${getDataDirName()}/config.yaml`, format: "yaml" },
    ],
  };
  if (asJson) {
    console.log(JSON.stringify(spec, null, compact ? undefined : 2));
    return;
  }
  console.log("Schema");
  for (const f of spec.files) {
    console.log(`- ${f.name}: ${f.path_pattern}`);
  }
}

async function cmdAgents(args: string[]): Promise<void> {
  const profile = parseOpt(args, "--profile") ?? "all";
  const order = profile === "all" ? ["short", "medium", "long"] : [profile];
  for (let i = 0; i < order.length; i++) {
    const key = order[i]!;
    if (!AGENTS_SNIPPETS[key]) textError(`Invalid profile: ${profile}`);
    if (i > 0) console.log(`\n${"=".repeat(72)}\n`);
    console.log(AGENTS_SNIPPETS[key]);
  }
}

async function cmdHowto(args: string[]): Promise<void> {
  const asJson = parseFlag(args, "--json");
  if (asJson) {
    jsonOut({
      name: "backlog-howto",
      skill_version: BACKLOG_HOWTO_SKILL_VERSION,
      content: BACKLOG_HOWTO_SKILL_MD,
    });
    return;
  }
  console.log(BACKLOG_HOWTO_SKILL_MD);
}

function slugify(text: string, maxLength = 30): string {
  const base = text.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  return base.length > maxLength ? base.slice(0, maxLength).replace(/-+$/g, "") : base;
}

function parseCsv(val: string | undefined): string[] {
  if (!val) return [];
  return val.split(",").map((x) => x.trim()).filter(Boolean);
}

function parseDependencyList(raw: string | undefined): string[] {
  try {
    return parseDependencyIds(raw);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    textError(message);
  }
}

function printNextCommands(commands: string[]): void {
  const filtered = commands.map((command) => command.trim()).filter((command) => command.length > 0);
  if (!filtered.length) {
    return;
  }
  console.log("Next:");
  for (const command of filtered) {
    console.log(`  ${command}`);
  }
}

async function readYamlObj(path: string): Promise<Record<string, unknown>> {
  return (parse(await Bun.file(path).text()) as Record<string, unknown>) ?? {};
}

async function writeYamlObj(path: string, value: Record<string, unknown>): Promise<void> {
  await mkdir(dirname(path), { recursive: true });
  await Bun.write(path, stringify(value));
}

async function cmdAdd(args: string[]): Promise<AutoCommitMetadata> {
  const epicId = args.find((a) => !a.startsWith("-"));
  if (!epicId) textError("add requires EPIC_ID");
  const title = parseOpt(args, "--title") ?? parseOpt(args, "-T");
  if (!title) textError("add requires --title");
  const estimate = Number(parseOpt(args, "--estimate") ?? parseOpt(args, "-e") ?? "1");
  const complexity = parseOpt(args, "--complexity") ?? parseOpt(args, "-c") ?? "medium";
  const priority = parseOpt(args, "--priority") ?? parseOpt(args, "-p") ?? "medium";
  const dependsOn = parseDependencyList(parseOpt(args, "--depends-on") ?? parseOpt(args, "-d"));
  const rawTags = parseOpt(args, "--tags");
  const tags = rawTags === undefined ? undefined : parseCsv(rawTags);
  const bodyOpt = parseOpt(args, "--body") ?? parseOpt(args, "-b");

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const epic = findEpic(tree, epicId);
  if (!epic) textError(`Epic not found: ${epicId}`);
  const phase = findPhase(tree, epic.phaseId ?? "");
  const milestone = findMilestone(tree, epic.milestoneId ?? "");
  if (!phase || !milestone) textError(`Could not resolve parent paths for epic: ${epicId}`);
  if (phase.locked) {
    textError(`Phase ${phase.id} has been closed and cannot accept new tasks. The agent should create a new epic.`);
  }
  if (milestone.locked) {
    textError(`Milestone ${milestone.id} has been closed and cannot accept new tasks. The agent should create a new epic.`);
  }
  if (epic.locked) {
    textError(`Epic ${epic.id} has been closed and cannot accept new tasks. The agent should create a new epic.`);
  }

  const epicDir = join(getDataDirName(), phase.path, milestone.path, epic.path);
  const existing = epic.tasks
    .map((t) => Number((t.id.match(/\.T(\d+)$/)?.[1] ?? "0")))
    .filter((n) => Number.isFinite(n));
  const next = (existing.length ? Math.max(...existing) : 0) + 1;
  const shortId = `T${String(next).padStart(3, "0")}`;
  const fullId = `${epic.id}.${shortId}`;
  const filename = `${shortId}-${slugify(title)}.todo`;
  const relFile = join(phase.path, milestone.path, epic.path, filename);
  const fullFile = join(getDataDirName(), relFile);

  const fm = {
    id: fullId,
    title,
    status: "pending",
    estimate_hours: estimate,
    complexity,
    priority,
    depends_on: dependsOn,
    tags,
  };
  const body = bodyOpt ?? `\n# ${title}\n\n## Requirements\n\n- TODO: Add requirements\n\n## Acceptance Criteria\n\n- TODO: Add acceptance criteria\n`;
  await mkdir(dirname(fullFile), { recursive: true });
  await Bun.write(fullFile, `---\n${stringify(fm)}---\n${body}`);

  const epicIndexPath = join(epicDir, "index.yaml");
  const epicIndex = await readYamlObj(epicIndexPath);
  const tasks = ((epicIndex.tasks as Record<string, unknown>[] | undefined) ?? []).slice();
  tasks.push({
    id: shortId,
    file: filename,
    title,
    status: "pending",
    estimate_hours: estimate,
    complexity,
    priority,
    depends_on: dependsOn,
  });
  epicIndex.tasks = tasks;
  await writeYamlObj(epicIndexPath, epicIndex);
  console.log(`Created task: ${fullId}`);
  console.log(`File: ${getDataDirName()}/${relFile}`);
  if (!bodyOpt) {
    console.log("IMPORTANT: You MUST fill in the .todo file that was created.");
  }
  printNextCommands([
    `backlog show ${fullId}`,
    `backlog claim ${fullId}`,
  ]);
  return { id: fullId, title };
}

async function cmdAddEpic(args: string[]): Promise<void> {
  const milestoneId = args.find((a) => !a.startsWith("-"));
  if (!milestoneId) textError("add-epic requires MILESTONE_ID");
  const title = parseOpt(args, "--title") ?? parseOpt(args, "-T") ?? parseOpt(args, "--name") ?? parseOpt(args, "-n");
  if (!title) textError("add-epic requires --title");
  const estimate = Number(parseOpt(args, "--estimate") ?? parseOpt(args, "-e") ?? "4");
  const complexity = parseOpt(args, "--complexity") ?? parseOpt(args, "-c") ?? "medium";
  const dependsOn = parseDependencyList(parseOpt(args, "--depends-on") ?? parseOpt(args, "-d"));
  const description = parseOpt(args, "--description") ?? "";

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const milestone = findMilestone(tree, milestoneId);
  if (!milestone) textError(`Milestone not found: ${milestoneId}`);
  const phase = findPhase(tree, milestone.phaseId ?? "");
  if (!phase) textError(`Phase not found for milestone: ${milestoneId}`);
  if (phase.locked) {
    textError(`Phase ${phase.id} has been closed and cannot accept new milestones. Create a new phase.`);
  }
  if (milestone.locked) {
    textError(`Milestone ${milestone.id} has been closed and cannot accept new epics. The agent should create a new epic.`);
  }

  const existing = milestone.epics
    .map((e) => Number((e.id.match(/\.E(\d+)$/)?.[1] ?? "0")))
    .filter((n) => Number.isFinite(n));
  const next = (existing.length ? Math.max(...existing) : 0) + 1;
  const shortId = `E${next}`;
  const fullId = `${milestone.id}.${shortId}`;
  const dirName = `${String(next).padStart(2, "0")}-${slugify(title)}`;
  const epicDir = join(getDataDirName(), phase.path, milestone.path, dirName);
  await mkdir(epicDir, { recursive: true });
  await writeYamlObj(join(epicDir, "index.yaml"), {
    id: fullId,
    name: title,
    status: "pending",
    locked: false,
    estimate_hours: estimate,
    complexity,
    depends_on: dependsOn,
    tasks: [],
    stats: { total: 0, done: 0, in_progress: 0, blocked: 0, pending: 0 },
  });

  const msIndexPath = join(getDataDirName(), phase.path, milestone.path, "index.yaml");
  const msIndex = await readYamlObj(msIndexPath);
  const epics = ((msIndex.epics as Record<string, unknown>[] | undefined) ?? []).slice();
  epics.push({
    id: shortId,
    name: title,
    path: dirName,
    status: "pending",
    locked: false,
    estimate_hours: estimate,
    complexity,
    depends_on: dependsOn,
    description,
  });
  msIndex.epics = epics;
  await writeYamlObj(msIndexPath, msIndex);
  console.log(`Created epic: ${fullId}`);
  console.log(`File: ${getDataDirName()}/${phase.path}/${milestone.path}/${dirName}/index.yaml`);
  printNextCommands([
    `backlog show ${fullId}`,
    `backlog add ${fullId} --title "<task title>"`,
  ]);
}

async function cmdAddMilestone(args: string[]): Promise<void> {
  const phaseId = args.find((a) => !a.startsWith("-"));
  if (!phaseId) textError("add-milestone requires PHASE_ID");
  const title = parseOpt(args, "--title") ?? parseOpt(args, "-T") ?? parseOpt(args, "--name") ?? parseOpt(args, "-n");
  if (!title) textError("add-milestone requires --title");
  const estimate = Number(parseOpt(args, "--estimate") ?? parseOpt(args, "-e") ?? "8");
  const complexity = parseOpt(args, "--complexity") ?? parseOpt(args, "-c") ?? "medium";
  const dependsOn = parseDependencyList(parseOpt(args, "--depends-on") ?? parseOpt(args, "-d"));
  const description = parseOpt(args, "--description") ?? "";

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const phase = findPhase(tree, phaseId);
  if (!phase) textError(`Phase not found: ${phaseId}`);
  if (phase.locked) {
    textError(`Phase ${phase.id} has been closed and cannot accept new milestones. Create a new phase.`);
  }

  const existing = phase.milestones
    .map((m) => Number((m.id.match(/\.M(\d+)$/)?.[1] ?? "0")))
    .filter((n) => Number.isFinite(n));
  const next = (existing.length ? Math.max(...existing) : 0) + 1;
  const shortId = `M${next}`;
  const fullId = `${phase.id}.${shortId}`;
  const dirName = `${String(next).padStart(2, "0")}-${slugify(title)}`;
  const msDir = join(getDataDirName(), phase.path, dirName);
  await mkdir(msDir, { recursive: true });
  await writeYamlObj(join(msDir, "index.yaml"), {
    id: fullId,
    name: title,
    status: "pending",
    locked: false,
    estimate_hours: estimate,
    complexity,
    depends_on: dependsOn,
    epics: [],
    stats: { total_tasks: 0, done: 0, in_progress: 0, blocked: 0, pending: 0 },
  });

  const phaseIndexPath = join(getDataDirName(), phase.path, "index.yaml");
  const phaseIndex = await readYamlObj(phaseIndexPath);
  const milestones = ((phaseIndex.milestones as Record<string, unknown>[] | undefined) ?? []).slice();
  milestones.push({
    id: shortId,
    name: title,
    path: dirName,
    status: "pending",
    locked: false,
    estimate_hours: estimate,
    complexity,
    depends_on: dependsOn,
    description,
  });
  phaseIndex.milestones = milestones;
  await writeYamlObj(phaseIndexPath, phaseIndex);
  console.log(`Created milestone: ${fullId}`);
  console.log(`File: ${getDataDirName()}/${phase.path}/${dirName}/index.yaml`);
  printNextCommands([
    `backlog show ${fullId}`,
    `backlog add-epic ${fullId} --title "<epic title>"`,
  ]);
}

async function cmdAddPhase(args: string[]): Promise<void> {
  const title = parseOpt(args, "--title") ?? parseOpt(args, "-T") ?? parseOpt(args, "--name") ?? parseOpt(args, "-n");
  if (!title) textError("add-phase requires --title");
  const weeks = Number(parseOpt(args, "--weeks") ?? "2");
  const estimate = Number(parseOpt(args, "--estimate") ?? parseOpt(args, "-e") ?? "40");
  const priority = parseOpt(args, "--priority") ?? parseOpt(args, "-p") ?? "medium";
  const dependsOn = parseDependencyList(parseOpt(args, "--depends-on") ?? parseOpt(args, "-d"));
  const description = parseOpt(args, "--description") ?? "";

  const rootIndexPath = join(getDataDirName(), "index.yaml");
  const root = await readYamlObj(rootIndexPath);
  const phases = ((root.phases as Record<string, unknown>[] | undefined) ?? []).slice();
  const existing = phases
    .map((p) => Number((String(p.id ?? "").match(/^P(\d+)$/)?.[1] ?? "0")))
    .filter((n) => Number.isFinite(n));
  const next = (existing.length ? Math.max(...existing) : 0) + 1;
  const phaseId = `P${next}`;
  const dirName = `${String(next).padStart(2, "0")}-${slugify(title)}`;
  const phaseDir = join(getDataDirName(), dirName);
  await mkdir(phaseDir, { recursive: true });
  await writeYamlObj(join(phaseDir, "index.yaml"), {
    id: phaseId,
    name: title,
    status: "pending",
    locked: false,
    weeks,
    estimate_hours: estimate,
    complexity: "medium",
    depends_on: dependsOn,
    milestones: [],
    stats: { total_tasks: 0, done: 0, in_progress: 0, blocked: 0, pending: 0 },
  });
  phases.push({
    id: phaseId,
    name: title,
    path: dirName,
    status: "pending",
    locked: false,
    weeks,
    estimate_hours: estimate,
    priority,
    depends_on: dependsOn,
    description,
  });
  root.phases = phases;
  await writeYamlObj(rootIndexPath, root);
  console.log(`Created phase: ${phaseId}`);
  console.log(`File: ${getDataDirName()}/${dirName}/index.yaml`);
  printNextCommands([
    `backlog show ${phaseId}`,
    `backlog add-milestone ${phaseId} --title "<milestone title>"`,
  ]);
}

async function cmdMove(args: string[]): Promise<void> {
  const sourceId = args.find((a) => !a.startsWith("-"));
  if (!sourceId) textError("move requires SOURCE_ID");
  const destId = parseOpt(args, "--to");
  if (!destId) textError("move requires --to DEST_ID");

  const loader = new TaskLoader();
  const result = await loader.moveItem(sourceId, destId);
  console.log(`Moved: ${result.source_id}`);
  console.log(`To: ${result.dest_id}`);
  console.log(`New ID: ${result.new_id}`);
}

async function cmdLock(args: string[]): Promise<void> {
  const itemId = args.find((a) => !a.startsWith("-"));
  if (!itemId) textError("lock requires ITEM_ID");
  const loader = new TaskLoader();
  const canonicalId = await loader.setItemLocked(itemId, true);
  console.log(`Locked: ${canonicalId}`);
}

async function cmdUnlock(args: string[]): Promise<void> {
  const itemId = args.find((a) => !a.startsWith("-"));
  if (!itemId) textError("unlock requires ITEM_ID");
  const loader = new TaskLoader();
  const canonicalId = await loader.setItemLocked(itemId, false);
  console.log(`Unlocked: ${canonicalId}`);
}

type InstallOp = { client: string; artifact: string; path: string; skill: string; action?: string };

function resolveSkills(names: string[]): string[] {
  const valid = ["plan-task", "plan-ingest", "start-tasks", "backlog-howto"];
  const normalized = names.map((n) => n.trim().toLowerCase()).filter(Boolean);
  if (!normalized.length || normalized.includes("all")) return valid;
  for (const n of normalized) {
    if (!valid.includes(n)) throw new Error(`Invalid skill name: ${n}`);
  }
  return Array.from(new Set(normalized));
}

function resolveClients(clientName: string): string[] {
  if (clientName === "common") return ["codex", "claude", "opencode"];
  return [clientName];
}

function resolveArtifacts(artifact: string): string[] {
  if (artifact === "both") return ["skills", "commands"];
  return [artifact];
}

function defaultRoot(client: string, scope: string, artifact: string): string {
  if (client === "codex") {
    if (artifact !== "skills") throw new Error("codex does not support commands artifacts");
    if (scope === "local") return ".agents/skills";
    return process.env.CODEX_HOME ? `${process.env.CODEX_HOME}/skills` : `${process.env.HOME}/.agents/skills`;
  }
  if (client === "claude") return scope === "local" ? `.claude/${artifact}` : `${process.env.HOME}/.claude/${artifact}`;
  if (client === "opencode") return scope === "local" ? `.opencode/${artifact}` : `${process.env.HOME}/.config/opencode/${artifact}`;
  throw new Error(`Unknown client: ${client}`);
}

function skillTemplate(name: string): string {
  if (name === "plan-task") return "---\nname: plan-task\ndescription: plan task\n---\n# plan-task\n";
  if (name === "plan-ingest") return "---\nname: plan-ingest\ndescription: plan ingest\n---\n# plan-ingest\n";
  if (name === "backlog-howto") return BACKLOG_HOWTO_SKILL_MD;
  return "---\nname: start-tasks\ndescription: tasks grab and cycle\n---\n# start-tasks\n";
}

function commandTemplate(name: string): string {
  if (name === "backlog-howto") {
    return `---\ndescription: Backlog overview/manual skill with common workflows and command usage.\n---\n\nLoad the installed \`backlog-howto\` skill instructions.\n\nSource of truth: \`bl_skills/backlog-howto/SKILL.md\`\nSkill-Version: ${BACKLOG_HOWTO_SKILL_VERSION}\n`;
  }
  return `---\ndescription: ${name}\n---\n${name}\n`;
}

async function promptClientSelection(): Promise<string> {
  if (!process.stdin.isTTY || !process.stdout.isTTY) return "common";

  console.log("Select target coding CLI:");
  console.log("  1) codex");
  console.log("  2) claude");
  console.log("  3) opencode");
  console.log("  4) common (all)");

  const rl = createInterface({ input: process.stdin, output: process.stdout });
  const answer = (await rl.question("Choose [1-4]: ")).trim().toLowerCase();
  rl.close();

  const mapped: Record<string, string> = {
    "1": "codex",
    "2": "claude",
    "3": "opencode",
    "4": "common",
  };

  const selected = mapped[answer] ?? answer;
  if (["codex", "claude", "opencode", "common"].includes(selected)) return selected;
  throw new Error("Invalid client selection. Choose 1-4 or codex|claude|opencode|common.");
}

async function cmdSkills(args: string[]): Promise<void> {
  const sub = args[0];
  const rest = args.slice(1);
  if (!sub || sub === "--help") {
    console.log("Usage: backlog skills install [skill_names...] [--scope local|global] [--client codex|claude|opencode|common]");
    return;
  }
  if (sub !== "install") {
    textError(`Unknown skills subcommand: ${sub}`);
  }
  const skillNames = rest.filter((a) => !a.startsWith("-"));
  const scope = parseOpt(rest, "--scope") ?? "local";
  const parsedClient = parseOpt(rest, "--client");
  const client = parsedClient ?? (outputJson ? "common" : await promptClientSelection());
  const artifact = parseOpt(rest, "--artifact") ?? "skills";
  const outputDir = parseOpt(rest, "--dir");
  const force = parseFlag(rest, "--force");
  const dryRun = parseFlag(rest, "--dry-run");
  const outputJson = parseFlag(rest, "--json");

  const selectedSkills = resolveSkills(skillNames);
  const warnings: string[] = [];
  const ops: InstallOp[] = [];

  for (const c of resolveClients(client)) {
    for (const a of resolveArtifacts(artifact)) {
      if (c === "codex" && a === "commands") {
        warnings.push("codex does not support 'commands' artifacts; skipping.");
        continue;
      }
      const root = outputDir ? join(outputDir, a, c) : defaultRoot(c, scope, a);
      for (const skill of selectedSkills) {
        const path = a === "skills" ? join(root, skill, "SKILL.md") : join(root, `${skill}.md`);
        ops.push({ client: c, artifact: a, path, skill });
      }
    }
  }

  const existing = ops.filter((o) => existsSync(o.path));
  if (existing.length && !force && !dryRun) {
    throw new Error(`Refusing to overwrite existing files (use --force):\n${existing.map((o) => o.path).join("\n")}`);
  }

  if (!dryRun) {
    for (const op of ops) {
      await mkdir(dirname(op.path), { recursive: true });
      const content = op.artifact === "skills"
        ? skillTemplate(op.skill)
        : commandTemplate(op.skill);
      await Bun.write(op.path, content);
    }
  }

  const result = {
    skills: selectedSkills,
    scope,
    client,
    artifact,
    output_dir: outputDir ?? null,
    dry_run: dryRun,
    force,
    warnings,
    operations: ops.map((o) => ({ ...o, action: dryRun ? "planned" : "written" })),
    written_count: dryRun ? 0 : ops.length,
  };
  if (outputJson) {
    jsonOut(result);
    return;
  }
  if (dryRun) console.log("Dry run: no files written.");
  else console.log(`Installed ${ops.length} file(s).`);
  for (const w of warnings) console.log(`Warning: ${w}`);
}

async function cmdSearch(args: string[]): Promise<void> {
  const pattern = args.find((a) => !a.startsWith("-"));
  if (!pattern) textError("search requires PATTERN");
  const status = parseOpt(args, "--status");
  const tags = (parseOpt(args, "--tags") ?? "")
    .split(",")
    .map((t) => t.trim().toLowerCase())
    .filter(Boolean);
  const complexity = parseOpt(args, "--complexity");
  const priority = parseOpt(args, "--priority");
  const limit = Number(parseOpt(args, "--limit") ?? "20");
  const regex = new RegExp(pattern, "i");

  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const cfg = loadConfig();
  const calc = new CriticalPathCalculator(tree, (cfg.complexity_multipliers as Record<string, number>) ?? {});
  const { criticalPath } = calc.calculate();

  const matches = getAllTasks(tree).filter((t) => {
    const fileContent = existsSync(join(getDataDirName(), t.file)) ? readFileSync(join(getDataDirName(), t.file), "utf8") : "";
    if (!regex.test(t.title) && !regex.test(fileContent)) return false;
    if (status && t.status !== (status as Status)) return false;
    if (complexity && t.complexity !== complexity) return false;
    if (priority && t.priority !== priority) return false;
    if (tags.length) {
      const tt = new Set(t.tags.map((x) => x.toLowerCase()));
      if (!tags.some((x) => tt.has(x))) return false;
    }
    return true;
  });

  if (!matches.length) {
    console.log(`No tasks found matching '${pattern}'`);
    return;
  }

  console.log(`Found ${matches.length} result(s) for "${pattern}":`);
  for (const t of matches.slice(0, limit)) {
    const crit = criticalPath.includes(t.id) ? "*" : " ";
    console.log(`${crit} ${t.id} ${t.title} [${t.status}]`);
  }
}

async function cmdBlockers(args: string[]): Promise<void> {
  const deep = parseFlag(args, "--deep");
  const suggest = parseFlag(args, "--suggest");
  const loader = new TaskLoader();
  const tree = await loader.load("metadata");
  const cfg = loadConfig();
  const calc = new CriticalPathCalculator(tree, (cfg.complexity_multipliers as Record<string, number>) ?? {});
  const { criticalPath } = calc.calculate();
  const all = getAllTasks(tree);
  const blockedTasks = all.filter((t) => t.status === Status.BLOCKED);

  const pendingBlocked = all.filter((t) => t.status === Status.PENDING && !t.claimedBy && !calc.isTaskAvailable(t));
  if (!blockedTasks.length && !pendingBlocked.length) {
    console.log("No blocked tasks!");
    return;
  }
  console.log(`${blockedTasks.length} task(s) marked as BLOCKED`);
  console.log(`${pendingBlocked.length} task(s) waiting on dependencies`);

  const rootBlockers = new Set<string>();
  for (const t of pendingBlocked) {
    for (const dep of t.dependsOn) {
      const depTask = findTask(tree, dep);
      if (depTask && depTask.status !== Status.DONE) {
        if (depTask.status === Status.IN_PROGRESS || (depTask.status === Status.PENDING && calc.isTaskAvailable(depTask))) {
          rootBlockers.add(depTask.id);
        }
      }
    }
  }

  console.log("Blocking Chains:");
  for (const id of Array.from(rootBlockers).slice(0, deep ? 999 : 10)) {
    const t = findTask(tree, id);
    if (!t) continue;
    const crit = criticalPath.includes(id) ? " CRITICAL" : "";
    console.log(`${id} ${t.status}${crit} ${t.claimedBy ? `@${t.claimedBy}` : "UNCLAIMED"}`);
    if (suggest) {
      if (!t.claimedBy && t.status === Status.PENDING) console.log(`  suggest: grab ${id}`);
    }
  }
}

async function cmdSession(args: string[]): Promise<void> {
  const sub = args[0];
  const rest = args.slice(1);
  const timeout = Number(((loadConfig().session as Record<string, unknown>)?.heartbeat_timeout_minutes as number | undefined) ?? 15);

  if (!sub || sub === "--help") {
    console.log("Usage: backlog session <start|heartbeat|list|end|clean> [options]");
    return;
  }

  if (sub === "start") {
    const agent = parseOpt(rest, "--agent");
    const task = parseOpt(rest, "--task");
    if (!agent) textError("session start requires --agent");
    const sess = await startSession(agent, task);
    console.log(`✓ Session started`);
    console.log(`  Agent: ${agent}`);
    if (task) console.log(`  Task:  ${task}`);
    console.log(`  Time:  ${String(sess.started_at)}`);
    return;
  }

  if (sub === "heartbeat") {
    const agent = parseOpt(rest, "--agent");
    const progress = parseOpt(rest, "--progress");
    if (!agent) textError("session heartbeat requires --agent");
    const ok = await updateSessionHeartbeat(agent, progress);
    if (!ok) {
      console.log(`Warning: No active session for '${agent}'`);
      return;
    }
    console.log(`✓ Heartbeat updated for ${agent}`);
    if (progress) console.log(`  Progress: ${progress}`);
    return;
  }

  if (sub === "end") {
    const agent = parseOpt(rest, "--agent");
    const status = parseOpt(rest, "--status") ?? "completed";
    if (!agent) textError("session end requires --agent");
    const ok = await endSession(agent);
    if (ok) {
      console.log(`✓ Session ended for ${agent}`);
      console.log(`  Status: ${status}`);
    } else {
      console.log(`No active session found for '${agent}'`);
    }
    return;
  }

  if (sub === "list") {
    const onlyStale = parseFlag(rest, "--stale");
    if (onlyStale) {
      const sessions = await getStaleSessions(timeout);
      if (!sessions.length) {
        console.log("✓ No stale sessions");
        return;
      }
      console.log(`Stale Sessions (no heartbeat > ${timeout}m):`);
      for (const sess of sessions) {
        console.log(`  ${String(sess.agent_id)}`);
        if (sess.current_task) console.log(`    Task: ${String(sess.current_task)}`);
        console.log(`    Last heartbeat: ${String(sess.age_minutes)}m ago`);
        if (sess.progress) console.log(`    Last progress: ${String(sess.progress)}`);
      }
      return;
    }

    const active = await getActiveSessions();
    if (!active.length) {
      console.log("No active sessions");
      return;
    }
    for (const sess of active) {
      const hbAge = Number(sess.last_heartbeat_minutes ?? 0);
      const hbStr = hbAge > timeout ? pc.red(`${hbAge}m`) : hbAge > timeout / 2 ? pc.yellow(`${hbAge}m`) : pc.green(`${hbAge}m`);
      console.log(
        `${String(sess.agent_id)} task=${String(sess.current_task ?? "-")} duration=${formatDuration(Number(sess.duration_minutes ?? 0))} last_hb=${hbStr} progress=${String(sess.progress ?? "-")}`,
      );
    }
    return;
  }

  if (sub === "clean") {
    const sessions = await loadSessions();
    const stale = await getStaleSessions(timeout);
    const cleaned: string[] = [];
    for (const s of stale) {
      const agent = String(s.agent_id);
      if (agent in sessions) {
        delete sessions[agent];
        cleaned.push(agent);
      }
    }
    await saveSessions(sessions);
    if (cleaned.length) {
      console.log(`✓ Removed ${cleaned.length} stale session(s):`);
      for (const agent of cleaned) console.log(`  - ${agent}`);
    } else {
      console.log("✓ No stale sessions to clean");
    }
    return;
  }

  textError(`Unknown session subcommand: ${sub}`);
}

async function cmdCheck(args: string[]): Promise<void> {
  const asJson = parseFlag(args, "--json");
  const strict = parseFlag(args, "--strict");
  const report = await runChecks();
  if (asJson) {
    jsonOut(report);
  } else if (!report.errors.length && !report.warnings.length) {
    console.log("Consistency check passed with no issues.");
  } else {
    console.log(`Consistency check results: ${report.summary.errors} error(s), ${report.summary.warnings} warning(s)`);
    if (report.errors.length) {
      console.log("Errors:");
      for (const e of report.errors) {
        console.log(`- ${e.code}: ${e.message}${e.location ? ` (${e.location})` : ""}`);
      }
    }
    if (report.warnings.length) {
      console.log("Warnings:");
      for (const w of report.warnings) {
        console.log(`- ${w.code}: ${w.message}${w.location ? ` (${w.location})` : ""}`);
      }
    }
  }
  if (report.errors.length || (strict && report.warnings.length)) {
    process.exit(1);
  }
}

async function cmdInit(args: string[]): Promise<void> {
  const project = parseOpt(args, "--project") ?? parseOpt(args, "-p");
  if (!project) textError("init requires --project");
  const description = parseOpt(args, "--description") ?? parseOpt(args, "-d") ?? "";
  const timelineWeeks = Number(parseOpt(args, "--timeline-weeks") ?? parseOpt(args, "-w") ?? "0");

  const indexPath = join(BACKLOG_DIR, "index.yaml");
  if (existsSync(indexPath)) textError(`Already initialized (${BACKLOG_DIR}/index.yaml exists)`);

  await writeYamlObj(indexPath, {
    project,
    description,
    timeline_weeks: timelineWeeks,
    phases: [],
  });
  console.log(`Initialized project "${project}" in ${BACKLOG_DIR}/`);
}

async function cmdBug(args: string[]): Promise<AutoCommitMetadata> {
  let title = parseOpt(args, "--title") ?? parseOpt(args, "-T");
  const optionNamesWithValue = new Set([
    "--title",
    "-T",
    "--priority",
    "-p",
    "--estimate",
    "-e",
    "--complexity",
    "-c",
    "--depends-on",
    "-d",
    "--tags",
    "--body",
    "-b",
  ]);
  const bugWords: string[] = [];
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i]!;
    if (optionNamesWithValue.has(arg)) {
      i += 1;
      continue;
    }
    if (arg.startsWith("-")) continue;
    bugWords.push(arg);
  }
  const positionalTitle = bugWords.join(" ").trim();

  const priority = parseOpt(args, "--priority") ?? parseOpt(args, "-p") ?? "high";
  const estimate = Number(parseOpt(args, "--estimate") ?? parseOpt(args, "-e") ?? "1");
  const complexity = parseOpt(args, "--complexity") ?? parseOpt(args, "-c") ?? "medium";
  const dependsOn = parseDependencyList(parseOpt(args, "--depends-on") ?? parseOpt(args, "-d"));
  const rawTags = parseOpt(args, "--tags");
  const tags = rawTags === undefined ? undefined : parseCsv(rawTags);
  let simple = parseFlag(args, "--simple") || parseFlag(args, "-s");
  const body = parseOpt(args, "--body") ?? parseOpt(args, "-b");

  if (!title && positionalTitle) {
    title = positionalTitle;
  }
  if (!title) textError("bug requires --title or description text");
  validateAtLeastTwoWords("bug", title);

  const loader = new TaskLoader();
  const bug = await loader.createBug({
    title,
    priority,
    estimate,
    complexity,
    dependsOn,
    ...(rawTags !== undefined ? { tags } : {}),
    simple,
    body,
  });
  console.log(`Created bug: ${bug.id}`);
  console.log(`File: ${getDataDirName()}/${bug.file}`);
  if (!simple && !body) {
    console.log("IMPORTANT: You MUST fill in the .todo file that was created.");
  }
  printNextCommands([
    `backlog show ${bug.id}`,
    `backlog claim ${bug.id}`,
  ]);
  return bug;
}

async function cmdFixed(args: string[]): Promise<void> {
  let title = parseOpt(args, "--title") ?? parseOpt(args, "-T");
  const optionNamesWithValue = new Set([
    "--title",
    "-T",
    "--description",
    "--desc",
    "--at",
    "--tags",
    "--body",
    "-b",
  ]);
  const fixedWords: string[] = [];
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i]!;
    if (optionNamesWithValue.has(arg)) {
      i += 1;
      continue;
    }
    if (arg.startsWith("-")) continue;
    fixedWords.push(arg);
  }
  const positionalTitle = fixedWords.join(" ").trim();

  const description = parseOpt(args, "--description") ?? parseOpt(args, "--desc") ?? title;
  const at = parseOpt(args, "--at");
  const tags = parseCsv(parseOpt(args, "--tags"));
  const body = parseOpt(args, "--body") ?? parseOpt(args, "-b");

  if (!title && positionalTitle) {
    title = positionalTitle;
  }
  if (!title) textError("fixed requires --title or FIX_TEXT");
  validateAtLeastTwoWords("fixed", title);

  const loader = new TaskLoader();
  const fixed = await loader.createFixed({
    title,
    description: description ?? title,
    at,
    tags,
    body,
  });
  console.log(`Created fixed: ${fixed.id}`);
  console.log(`File: ${getDataDirName()}/${fixed.file}`);
  if (tags.length) {
    console.log(`Tags: ${tags.join(", ")}`);
  }
}

async function cmdIdea(args: string[]): Promise<AutoCommitMetadata> {
  let title = parseOpt(args, "--title") ?? parseOpt(args, "-T");
  const optionNamesWithValue = new Set([
    "--title",
    "-T",
    "--priority",
    "-p",
    "--estimate",
    "-e",
    "--complexity",
    "-c",
    "--depends-on",
    "-d",
    "--tags",
    "--body",
    "-b",
  ]);
  const ideaWords: string[] = [];
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i]!;
    if (optionNamesWithValue.has(arg)) {
      i += 1;
      continue;
    }
    if (arg.startsWith("-")) continue;
    ideaWords.push(arg);
  }
  const positionalTitle = ideaWords.join(" ").trim();

  const estimate = Number(parseOpt(args, "--estimate") ?? parseOpt(args, "-e") ?? "10");
  const complexity = parseOpt(args, "--complexity") ?? parseOpt(args, "-c") ?? "medium";
  const priority = parseOpt(args, "--priority") ?? parseOpt(args, "-p") ?? "medium";
  const dependsOn = parseDependencyList(parseOpt(args, "--depends-on") ?? parseOpt(args, "-d"));
  const rawTags = parseOpt(args, "--tags");
  const tags = rawTags === undefined ? undefined : parseCsv(rawTags);
  let simple = parseFlag(args, "--simple") || parseFlag(args, "-s");
  const body = parseOpt(args, "--body") ?? parseOpt(args, "-b");

  if (!title && positionalTitle) {
    title = positionalTitle;
  }
  if (!title) textError("idea requires --title or IDEA_TEXT");
  validateAtLeastTwoWords("idea", title);

  const loader = new TaskLoader();
  const idea = await loader.createIdea({
    title,
    estimate,
    complexity,
    priority,
    dependsOn,
    ...(rawTags !== undefined ? { tags } : {}),
    simple,
    body,
  });
  console.log(`Created idea: ${idea.id}`);
  console.log(`File: ${getDataDirName()}/${idea.file}`);
  printNextCommands([
    `backlog show ${idea.id}`,
    "backlog add-phase --title \"<phase title>\"",
    "backlog add-milestone <PHASE_ID> --title \"<milestone title>\"",
    "backlog add-epic <MILESTONE_ID> --title \"<epic title>\"",
    "backlog add <EPIC_ID> --title \"<task title>\"",
    "backlog bug --title \"<bug title>\"",
  ]);
  console.log("IMPORTANT: This intake tracks planning work; run `/plan-task` on the idea and ingest resulting items with tasks commands.");
  return idea;
}

async function cmdMigrate(args: string[]): Promise<void> {
  const force = parseFlag(args, "--force") || parseFlag(args, "-f");
  const noSymlink = parseFlag(args, "--no-symlink");
  
  const { migrateDataDir } = await import("./data_dir");
  const result = migrateDataDir(!noSymlink, force);
  
  if (result.success) {
    console.log(`✓ ${result.message}`);
  } else {
    textError(result.message);
  }
}

async function main(): Promise<void> {
  const args = process.argv.slice(2);
  const cmd = args[0];
  const rest = args.slice(1);

  if (!cmd || cmd === "--help") {
    if (process.stdout.isTTY) {
      const lines = renderStartupLogo(startupLogoWidth(), 3, logoShouldUseColor());
      for (const line of lines) {
        console.log(line);
      }
      console.log("");
    } else {
      console.log("");
    }
    usage();
    return;
  }
  if (cmd === "--version") {
    console.log("0.1.0");
    return;
  }
  if (maybePrintCommandHelp(cmd, rest)) {
    return;
  }

  switch (cmd) {
    case "init":
      await cmdInit(rest);
      return;
    case "list":
      await cmdList(rest);
      return;
    case "ls":
      await cmdLs(rest);
      return;
    case "log":
      await cmdLog(rest);
      return;
    case "help":
      runHelp(rest);
      return;
    case "tree":
      await cmdTree(rest);
      return;
    case "next":
      await cmdNext(rest);
      return;
    case "preview":
      await cmdPreview(rest);
      return;
    case "show":
      await cmdShow(rest);
      return;
    case "claim":
      await runWithAutoCommit("claim", () => cmdClaim(rest));
      return;
    case "edit":
      await runWithAutoCommit("edit", () => cmdEdit(rest));
      return;
    case "grab":
      await runWithAutoCommit("grab", () => cmdGrab(rest));
      return;
    case "done":
      await runWithAutoCommit("done", () => cmdDone(rest));
      return;
    case "undone":
      await runWithAutoCommit("undone", () => cmdUndone(rest));
      return;
    case "cycle":
      await cmdCycle(rest);
      return;
    case "dash":
      await cmdDash(rest);
      return;
    case "update":
      await cmdUpdate(rest);
      return;
    case "set":
      await runWithAutoCommit("set", () => cmdSet(rest));
      return;
    case "work":
      await cmdWork(rest);
      return;
    case "unclaim":
      await runWithAutoCommit("unclaim", () => cmdUnclaim(rest));
      return;
    case "blocked":
      await cmdBlocked(rest);
      return;
    case "session":
      await cmdSession(rest);
      return;
    case "check":
      await cmdCheck(rest);
      return;
    case "sync":
      await cmdSync();
      return;
    case "data":
      await cmdData(rest);
      return;
    case "report":
      await cmdReport(rest);
      return;
    case "velocity":
      if (parseFlag(rest, "--help", "-h")) {
        printCommandHelpForCommand("velocity");
        return;
      }
      await cmdReport(["velocity", ...rest]);
      return;
    case "r":
      await cmdReport(rest);
      return;
    case "version":
      await cmdVersion(rest);
      return;
    case "timeline":
      await cmdTimeline(rest);
      return;
    case "tl":
      await cmdTimeline(rest);
      return;
    case "schema":
      await cmdSchema(rest);
      return;
    case "search":
      await cmdSearch(rest);
      return;
    case "blockers":
      await cmdBlockers(rest);
      return;
    case "skills":
      await cmdSkills(rest);
      return;
    case "agents":
      await cmdAgents(rest);
      return;
    case "howto":
      await cmdHowto(rest);
      return;
    case "ci":
      await cmdCI(rest);
      return;
    case "add":
      await runWithAutoCommit("add", () => cmdAdd(rest));
      return;
    case "add-epic":
      await cmdAddEpic(rest);
      return;
    case "add-milestone":
      await cmdAddMilestone(rest);
      return;
    case "add-phase":
      await cmdAddPhase(rest);
      return;
    case "lock":
      await cmdLock(rest);
      return;
    case "unlock":
      await cmdUnlock(rest);
      return;
    case "move":
      await cmdMove(rest);
      return;
    case "bug":
      await runWithAutoCommit("bug", () => cmdBug(rest));
      return;
    case "fixed":
      await cmdFixed(rest);
      return;
    case "idea":
      await runWithAutoCommit("idea", () => cmdIdea(rest));
      return;
    case "migrate":
      await cmdMigrate(rest);
      return;
    case "benchmark":
      await cmdBenchmark(rest);
      return;
    default:
      textError(`Unknown command: ${cmd}`);
  }
}

await main();
