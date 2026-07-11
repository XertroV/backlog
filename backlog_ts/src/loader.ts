import { existsSync, readFileSync } from "node:fs";
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { basename, dirname, extname, join } from "node:path";
import { performance } from "node:perf_hooks";
import { parse, stringify } from "yaml";
import { Complexity, Priority, Status, TaskPath, type Epic, type Milestone, type Phase, type Task, type TaskTree } from "./models";
import { getDataDir } from "./data_dir";
import { utcNow } from "./time";

interface AnyRec {
  [k: string]: unknown;
}

interface TimingRecord {
  id: string;
  path: string;
  ms: number;
}

interface TaskTimingRecord extends TimingRecord {
  epic_id: string;
}

interface BenchmarkReport {
  overall_ms: number;
  index_parse_ms: number;
  task_frontmatter_parse_ms: number;
  task_body_parse_ms: number;
  files: {
    total: number;
    by_type: Record<string, number>;
    by_type_ms: Record<string, number>;
  };
  counts: {
    phases: number;
    milestones: number;
    epics: number;
    tasks: number;
  };
  missing_task_files: number;
  phase_timings: Array<{ id: string; path: string; ms: number }>;
  milestone_timings: Array<{ id: string; path: string; ms: number }>;
  epic_timings: Array<{ id: string; path: string; ms: number }>;
  task_timings: Array<{ id: string; epic_id: string; path: string; ms: number }>;
  parse_mode?: "full" | "metadata" | "index";
  parse_task_body?: boolean;
}

type LoadMode = "full" | "metadata" | "index";

function estimateHoursFrom(data: AnyRec): number {
  return Number(data.estimate_hours ?? data.estimated_hours ?? 0);
}

function coerceStatus(rawStatus: unknown, fallback: Status = Status.PENDING): Status {
  if (rawStatus === undefined || rawStatus === null) return fallback;
  if (Object.values(Status).includes(rawStatus as Status)) return rawStatus as Status;
  if (typeof rawStatus === "string") {
    const normalized = rawStatus.trim().toLowerCase().replace(/[-\s]+/g, "_");
    const aliases: Record<string, Status> = {
      complete: Status.DONE,
      completed: Status.DONE,
    };
    return aliases[normalized] ?? (normalized as Status);
  }
  return String(rawStatus) as Status;
}

function parseTodo(content: string): { frontmatter: AnyRec; body: string } {
  const parts = content.split("---\n");
  if (parts.length >= 3) {
    return { frontmatter: (parse(parts[1] ?? "") as AnyRec) ?? {}, body: parts.slice(2).join("---\n") };
  }
  return { frontmatter: {}, body: content };
}

function slugify(text: string, maxLength = 30): string {
  const base = text.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  return base.length > maxLength ? base.slice(0, maxLength).replace(/-+$/g, "") : base;
}

function fallbackAuxTitle(filename: string, taskId: string, entryTitle?: string, frontmatterTitle?: string): string {
  const frontmatter = String(frontmatterTitle ?? "").trim();
  if (frontmatter) return frontmatter;

  const entry = String(entryTitle ?? "").trim();
  if (entry) return entry;

  let stem = basename(filename, extname(filename));
  const prefixes = [taskId, taskId.toUpperCase(), taskId.toLowerCase()].filter(Boolean);
  for (const prefix of prefixes) {
    if (stem.startsWith(prefix)) {
      stem = stem.slice(prefix.length).replace(/^[-_]+/, "");
      break;
    }
  }
  stem = stem.replace(/[-_]+/g, " ").trim().replace(/\s+/g, " ");
  return stem || taskId || "untitled";
}

export class TaskLoader {
  private readonly tasksDir: string;
  
  constructor(tasksDir?: string) {
    this.tasksDir = tasksDir ?? getDataDir();
    if (!existsSync(this.tasksDir)) {
      throw new Error(`Data directory not found: ${this.tasksDir}`);
    }
  }

  async load(
    mode: LoadMode = "full",
    includeBugs = true,
    includeIdeas = true,
  ): Promise<TaskTree> {
    return this.loadTree(undefined, mode, true, includeBugs, includeIdeas);
  }

  async loadScope(
    scope: string | TaskPath,
    mode: LoadMode = "full",
    parseTaskBody = false,
    includeBugs = true,
    includeIdeas = true,
  ): Promise<TaskTree> {
    const path = typeof scope === "string" ? TaskPath.parse(scope) : scope;
    const effectiveParseTaskBody = parseTaskBody && mode === "full";
    const root = this.mustYaml(join(this.tasksDir, "index.yaml"), undefined, "root_index");
    const tree: TaskTree = {
      project: String(root.project ?? ""),
      description: String(root.description ?? ""),
      timelineWeeks: Number(root.timeline_weeks ?? 0),
      criticalPath: (root.critical_path as string[] | undefined) ?? [],
      nextAvailable: root.next_available as string | undefined,
      phases: [],
      bugs: [],
      ideas: [],
    };

    const milestoneFilter = path.milestone;
    const epicFilter = path.epic;
    const taskFilter = path.taskId;

    for (const p of ((root.phases as AnyRec[]) ?? [])) {
      if (p.id !== path.phase) {
        continue;
      }
      tree.phases.push(
        await this.loadPhase(
          p,
          undefined,
          mode,
          effectiveParseTaskBody,
          milestoneFilter,
          epicFilter,
          taskFilter,
        ),
      );
    }
    tree.bugs = await this.loadBugs(undefined, mode, effectiveParseTaskBody, includeBugs);
    tree.ideas = await this.loadIdeas(undefined, mode, effectiveParseTaskBody, includeIdeas);
    return tree;
  }

  async findFixedTask(fixedTaskId: string): Promise<import("./models").Task | undefined> {
    const fixesDir = join(this.tasksDir, "fixes");
    const indexPath = join(fixesDir, "index.yaml");
    if (!existsSync(indexPath)) return undefined;

    const index = this.mustYaml(indexPath);
    const entry = ((index.fixes as AnyRec[]) ?? []).find(
      (item) => typeof item === "object" && item !== null && String((item as AnyRec).id) === fixedTaskId,
    );
    if (!entry || !entry.file) return undefined;

    const filename = String(entry.file);
    const fixPath = join(fixesDir, filename);
    if (!existsSync(fixPath)) return undefined;

    const { frontmatter } = await this.loadTodoFrontmatter(fixPath);
    return {
      id: String(frontmatter.id ?? fixedTaskId),
      title: String(frontmatter.title ?? ""),
      file: join("fixes", filename),
      status: coerceStatus(frontmatter.status),
      estimateHours: estimateHoursFrom(frontmatter),
      complexity: (frontmatter.complexity as Complexity) ?? Complexity.LOW,
      priority: (frontmatter.priority as Priority) ?? Priority.LOW,
      dependsOn: ((frontmatter.depends_on as string[]) ?? []).slice(),
      claimedBy: frontmatter.claimed_by as string | undefined,
      claimedAt: frontmatter.claimed_at ? new Date(String(frontmatter.claimed_at)) : undefined,
      startedAt: frontmatter.started_at ? new Date(String(frontmatter.started_at)) : undefined,
      completedAt: frontmatter.completed_at ? new Date(String(frontmatter.completed_at)) : undefined,
      createdAt: frontmatter.created_at ? new Date(String(frontmatter.created_at)) : undefined,
      updatedAt: frontmatter.updated_at ? new Date(String(frontmatter.updated_at)) : undefined,
      durationMinutes: frontmatter.duration_minutes ? Number(frontmatter.duration_minutes) : undefined,
      tags: ((frontmatter.tags as string[]) ?? []).slice(),
      reason: frontmatter.reason as string | undefined,
      epicId: undefined,
      milestoneId: undefined,
      phaseId: undefined,
    };
  }

  async loadWithBenchmark(
    mode: LoadMode = "full",
    parseTaskBody = true,
    includeBugs = true,
    includeIdeas = true,
  ): Promise<{ tree: TaskTree; benchmark: BenchmarkReport }> {
    const effectiveParseTaskBody = mode === "full" && parseTaskBody;
    const benchmark = this.newBenchmark();
    benchmark.parse_mode = mode;
    benchmark.parse_task_body = effectiveParseTaskBody;
    const start = performance.now();
    const tree = await this.loadTree(
      benchmark,
      mode,
      effectiveParseTaskBody,
      includeBugs,
      includeIdeas,
    );
    benchmark.overall_ms = performance.now() - start;
    return { tree, benchmark };
  }

  async loadTree(
    benchmark?: BenchmarkReport,
    mode: LoadMode = "full",
    parseTaskBody = true,
    includeBugs = true,
    includeIdeas = true,
  ): Promise<TaskTree> {
    const root = this.mustYaml(
      join(this.tasksDir, "index.yaml"),
      benchmark,
      "root_index",
    );
    const tree: TaskTree = {
      project: String(root.project ?? ""),
      description: String(root.description ?? ""),
      timelineWeeks: Number(root.timeline_weeks ?? 0),
      criticalPath: (root.critical_path as string[] | undefined) ?? [],
      nextAvailable: root.next_available as string | undefined,
      phases: [],
      bugs: [],
      ideas: [],
    };

    for (const p of ((root.phases as AnyRec[]) ?? [])) {
      tree.phases.push(await this.loadPhase(p, benchmark, mode, parseTaskBody));
    }
    tree.bugs = await this.loadBugs(benchmark, mode, parseTaskBody, includeBugs);
    tree.ideas = await this.loadIdeas(benchmark, mode, parseTaskBody, includeIdeas);
    return tree;
  }

  private newBenchmark(): BenchmarkReport {
    return {
      overall_ms: 0,
      index_parse_ms: 0,
      task_frontmatter_parse_ms: 0,
      task_body_parse_ms: 0,
      files: {
        total: 0,
        by_type: {
          root_index: 0,
          phase_index: 0,
          milestone_index: 0,
          epic_index: 0,
          todo_file: 0,
          bug_index: 0,
          idea_index: 0,
          bug_file: 0,
          idea_file: 0,
        },
        by_type_ms: {
          root_index: 0,
          phase_index: 0,
          milestone_index: 0,
          epic_index: 0,
          todo_file: 0,
          bug_index: 0,
          idea_index: 0,
          bug_file: 0,
          idea_file: 0,
        },
      },
      counts: {
        phases: 0,
        milestones: 0,
        epics: 0,
        tasks: 0,
      },
      missing_task_files: 0,
      phase_timings: [],
      milestone_timings: [],
      epic_timings: [],
      task_timings: [],
    };
  }

  private recordFile(
    benchmark: BenchmarkReport,
    fileType: string,
    filePath: string,
    elapsedMs: number,
  ): void {
    benchmark.files.total += 1;
    benchmark.files.by_type[fileType] = (benchmark.files.by_type[fileType] ?? 0) + 1;
    benchmark.files.by_type_ms[fileType] = (benchmark.files.by_type_ms[fileType] ?? 0) + elapsedMs;
    if (fileType.endsWith("_index")) {
      benchmark.index_parse_ms += elapsedMs;
    }
  }

  private recordTiming(
    bucket: Array<TimingRecord | TaskTimingRecord>,
    item: TimingRecord | Omit<TaskTimingRecord, "ms">,
    elapsedMs: number,
  ): void {
    bucket.push({
      ...item,
      ms: elapsedMs,
    } as TimingRecord | TaskTimingRecord);
  }

  private async loadPhase(
    phaseData: AnyRec,
    benchmark?: BenchmarkReport,
    loadMode: LoadMode = "full",
    parseTaskBody = true,
    milestoneFilter?: string,
    epicFilter?: string,
    taskFilter?: string,
  ): Promise<Phase> {
    const start = performance.now();
    if (benchmark) {
      benchmark.counts.phases += 1;
    }

    const phase: Phase = {
      id: String(phaseData.id),
      name: String(phaseData.name ?? ""),
      path: String(phaseData.path ?? ""),
      status: coerceStatus(phaseData.status, Status.PENDING),
      locked: Boolean(phaseData.locked ?? false),
      weeks: Number(phaseData.weeks ?? 0),
      estimateHours: estimateHoursFrom(phaseData),
      priority: (phaseData.priority as Priority) ?? Priority.MEDIUM,
      dependsOn: ((phaseData.depends_on as string[]) ?? []).slice(),
      milestones: [],
      description: String(phaseData.description ?? ""),
    };
    const phaseIndexPath = join(this.tasksDir, phase.path, "index.yaml");
    if (!existsSync(phaseIndexPath)) {
      if (benchmark) {
        this.recordTiming(benchmark.phase_timings, { id: phase.id, path: phase.path }, performance.now() - start);
      }
      return phase;
    }
    const idx = this.mustYaml(phaseIndexPath, benchmark, "phase_index");
    for (const m of ((idx.milestones as AnyRec[]) ?? [])) {
      if (milestoneFilter && String(m.id) !== milestoneFilter) {
        continue;
      }
      const milestone = await this.loadMilestone(
        join(this.tasksDir, phase.path),
        phase.id,
        m,
        benchmark,
        loadMode,
        parseTaskBody,
        epicFilter,
        taskFilter,
      );
      if (epicFilter && milestone.epics.length === 0) {
        continue;
      }
      phase.milestones.push(milestone);
    }
    if (benchmark) {
      this.recordTiming(benchmark.phase_timings, { id: phase.id, path: phase.path }, performance.now() - start);
    }
    return phase;
  }

  private async loadMilestone(
    phasePath: string,
    phaseId: string,
    milestoneData: AnyRec,
    benchmark?: BenchmarkReport,
    loadMode: LoadMode = "full",
    parseTaskBody = true,
    epicFilter?: string,
    taskFilter?: string,
  ): Promise<Milestone> {
    const start = performance.now();
    const msPath = TaskPath.forMilestone(phaseId, String(milestoneData.id));
    const m: Milestone = {
      id: msPath.fullId,
      name: String(milestoneData.name ?? ""),
      path: String(milestoneData.path ?? ""),
      status: coerceStatus(milestoneData.status, Status.PENDING),
      locked: Boolean(milestoneData.locked ?? false),
      estimateHours: estimateHoursFrom(milestoneData),
      complexity: (milestoneData.complexity as Complexity) ?? Complexity.MEDIUM,
      dependsOn: ((milestoneData.depends_on as string[]) ?? []).slice(),
      epics: [],
      description: String(milestoneData.description ?? ""),
      phaseId,
    };
    const idxPath = join(phasePath, m.path, "index.yaml");
    if (!existsSync(idxPath)) {
      if (benchmark) {
        benchmark.counts.milestones += 1;
        this.recordTiming(
          benchmark.milestone_timings,
          { id: msPath.fullId, path: m.path },
          performance.now() - start,
        );
      }
      return m;
    }
    const idx = this.mustYaml(idxPath, benchmark, "milestone_index");
    if (benchmark) {
      benchmark.counts.milestones += 1;
    }
    for (const e of ((idx.epics as AnyRec[]) ?? [])) {
      if (epicFilter && String(e.id) !== epicFilter) {
        continue;
      }
      const epic = await this.loadEpic(
        join(phasePath, m.path),
        msPath,
        e,
        benchmark,
        loadMode,
        parseTaskBody,
        taskFilter,
      );
      if (taskFilter && !epic.tasks.length) {
        continue;
      }
      m.epics.push(epic);
    }
    if (benchmark) {
      this.recordTiming(
        benchmark.milestone_timings,
        { id: msPath.fullId, path: m.path },
        performance.now() - start,
      );
    }
    return m;
  }

  private async loadEpic(
    milestonePath: string,
    msPath: TaskPath,
    epicData: AnyRec,
    benchmark?: BenchmarkReport,
    loadMode: LoadMode = "full",
    parseTaskBody = true,
    taskFilter?: string,
  ): Promise<Epic> {
    const start = performance.now();
    const epicPath = msPath.withEpic(String(epicData.id));
    const e: Epic = {
      id: epicPath.fullId,
      name: String(epicData.name ?? ""),
      path: String(epicData.path ?? ""),
      status: coerceStatus(epicData.status, Status.PENDING),
      locked: Boolean(epicData.locked ?? false),
      estimateHours: estimateHoursFrom(epicData),
      complexity: (epicData.complexity as Complexity) ?? Complexity.MEDIUM,
      dependsOn: ((epicData.depends_on as string[]) ?? []).slice(),
      tasks: [],
      description: String(epicData.description ?? ""),
      milestoneId: msPath.fullId,
      phaseId: msPath.phase,
    };
    const idxPath = join(milestonePath, e.path, "index.yaml");
    if (!existsSync(idxPath)) {
      if (benchmark) {
        benchmark.counts.epics += 1;
        this.recordTiming(benchmark.epic_timings, { id: epicPath.fullId, path: e.path }, performance.now() - start);
      }
      return e;
    }
    const idx = this.mustYaml(idxPath, benchmark, "epic_index");
    if (benchmark) {
      benchmark.counts.epics += 1;
    }
    for (const taskData of ((idx.tasks as (AnyRec | string)[]) ?? [])) {
      if (taskFilter && !this.taskMatchesFilter(taskData, epicPath, taskFilter)) {
        continue;
      }
      e.tasks.push(
        await this.loadTask(
          join(milestonePath, e.path),
          epicPath,
          taskData,
          benchmark,
          loadMode,
          parseTaskBody,
        ),
      );
      if (taskFilter && this.idsMatch(e.tasks[e.tasks.length - 1]!.id, taskFilter)) {
        break;
      }
    }
    if (benchmark) {
      this.recordTiming(
        benchmark.epic_timings,
        { id: epicPath.fullId, path: e.path },
        performance.now() - start,
      );
    }
    return e;
  }

  private idsMatch(candidate: string, target: string): boolean {
    if (!candidate || !target) return false;
    if (candidate === target) return true;
    return candidate.endsWith(`.${target}`) || target.endsWith(`.${candidate}`);
  }

  private taskMatchesFilter(taskData: AnyRec | string, epicPath: TaskPath, taskFilter: string): boolean {
    let taskId: string | undefined;
    if (typeof taskData === "string") {
      const shortId = taskData.split("-")[0]?.replace(".todo", "");
      taskId = epicPath.withTask(shortId).fullId;
    } else {
      const rawId = (taskData as AnyRec).id;
      if (typeof rawId === "string") {
        taskId = rawId.includes(".") ? rawId : epicPath.withTask(rawId).fullId;
      } else {
        const filename = String((taskData.file as string) ?? (taskData.path as string) ?? "");
        if (filename) {
          const shortId = filename.split("-")[0]?.replace(".todo", "") ?? "";
          if (shortId) {
            taskId = epicPath.withTask(shortId).fullId;
          }
        }
      }
    }
    return typeof taskId === "string" ? this.idsMatch(taskId, taskFilter) : false;
  }

  private async loadTask(
    epicPath: string,
    epPath: TaskPath,
    taskData: AnyRec | string,
    benchmark?: BenchmarkReport,
    loadMode: LoadMode = "full",
    parseTaskBody = true,
  ): Promise<Task> {
    let normalized: AnyRec;
    if (typeof taskData === "string") {
      const short = taskData.split("-")[0]?.replace(".todo", "") ?? "T001";
      normalized = { file: taskData, id: epPath.withTask(short).fullId };
    } else {
      normalized = taskData;
    }
    const filename = String(normalized.file ?? normalized.path ?? "");
    const taskFile = join(epicPath, filename);
    let frontmatter: AnyRec = {};
    let parseMs = 0;
    if (benchmark) {
      benchmark.counts.tasks += 1;
    }
    if (loadMode === "index") {
      frontmatter = { ...normalized };
    } else if (existsSync(taskFile)) {
      const parsed =
        loadMode === "full"
          ? await this.loadTodoFile(taskFile, benchmark, "todo_file", parseTaskBody)
          : await this.loadTodoFrontmatter(taskFile, benchmark, "todo_file");
      frontmatter = parsed.frontmatter;
      parseMs = parsed.elapsedMs;
    } else if (benchmark) {
      benchmark.missing_task_files += 1;
    }
    let id = String(frontmatter.id ?? normalized.id ?? "");
    if (id && !id.startsWith(`${epPath.phase}.`)) {
      const short = id.split(".").at(-1) ?? "T001";
      id = epPath.withTask(short).fullId;
    }
    if (benchmark) {
      this.recordTiming(
        benchmark.task_timings,
        { id, path: taskFile, epic_id: epPath.fullId },
        parseMs,
      );
    }
    return {
      id,
      title: String(frontmatter.title ?? normalized.title ?? ""),
      file: taskFile.replace(`${this.tasksDir}/`, ""),
      status: coerceStatus(frontmatter.status ?? normalized.status, Status.PENDING),
      estimateHours: Number(frontmatter.estimate_hours ?? frontmatter.estimated_hours ?? normalized.estimate_hours ?? normalized.estimated_hours ?? 0),
      complexity: (frontmatter.complexity as Complexity) ?? (normalized.complexity as Complexity) ?? Complexity.LOW,
      priority: (frontmatter.priority as Priority) ?? (normalized.priority as Priority) ?? Priority.MEDIUM,
      dependsOn: ((frontmatter.depends_on as string[]) ?? (normalized.depends_on as string[]) ?? []).slice(),
      claimedBy: frontmatter.claimed_by as string | undefined,
      claimedAt: frontmatter.claimed_at ? new Date(String(frontmatter.claimed_at)) : undefined,
      startedAt: frontmatter.started_at ? new Date(String(frontmatter.started_at)) : undefined,
      completedAt: frontmatter.completed_at ? new Date(String(frontmatter.completed_at)) : undefined,
      createdAt: frontmatter.created_at ? new Date(String(frontmatter.created_at)) : undefined,
      updatedAt: frontmatter.updated_at ? new Date(String(frontmatter.updated_at)) : undefined,
      durationMinutes: frontmatter.duration_minutes ? Number(frontmatter.duration_minutes) : undefined,
      tags: ((frontmatter.tags as string[]) ?? []).slice(),
      reason: frontmatter.reason as string | undefined,
      epicId: epPath.fullId,
      milestoneId: epPath.milestoneId,
      phaseId: epPath.phase,
    };
  }

  private async loadTodoFile(
    todoPath: string,
    benchmark?: BenchmarkReport,
    fileType = "todo_file",
    includeBody = true,
  ): Promise<{
    frontmatter: AnyRec;
    body: string;
    elapsedMs: number;
    task_frontmatter_parse_ms?: number;
    task_body_parse_ms?: number;
  }> {
    const start = benchmark ? performance.now() : 0;
    let frontmatter: AnyRec = {};
    let body = "";
    let elapsedMs = 0;
    let frontmatterParseMs = 0;
    let bodyParseMs = 0;
    try {
      const content = await readFile(todoPath, "utf8");
      const normalized = content.replace(/\r\n/g, "\n");
      const lines = normalized.split("\n");
      if ((lines[0] ?? "").replace("\ufeff", "").trim() !== "---") {
        elapsedMs = benchmark ? performance.now() - start : 0;
        return {
          frontmatter,
          body,
          elapsedMs,
          task_frontmatter_parse_ms: frontmatterParseMs,
          task_body_parse_ms: bodyParseMs,
        };
      }

      const parts = normalized.split("---\n");
      if (parts.length >= 3) {
        const fmStart = benchmark ? performance.now() : 0;
        frontmatter = (parse(parts[1] ?? "") as AnyRec) ?? {};
        frontmatterParseMs = benchmark ? performance.now() - fmStart : 0;

        if (includeBody) {
          const bodyStart = benchmark ? performance.now() : 0;
          body = parts.slice(2).join("---\n");
          bodyParseMs = benchmark ? performance.now() - bodyStart : 0;
        }
      } else {
        const fmLines: string[] = [];
        let hasClosing = false;
        for (let i = 1; i < lines.length; i += 1) {
          const line = lines[i] ?? "";
          if (line.trim() === "---") {
            hasClosing = true;
            if (includeBody) {
              body = lines.slice(i + 1).join("\n");
            }
            break;
          }
          fmLines.push(line);
        }
        if (fmLines.length > 0) {
          const parseStart = benchmark ? performance.now() : 0;
          frontmatter = (parse(fmLines.join("\n")) as AnyRec) ?? {};
          frontmatterParseMs = benchmark ? performance.now() - parseStart : 0;
        }
        if (!hasClosing && includeBody) {
          body = normalized;
        }
      }
      elapsedMs = benchmark ? performance.now() - start : 0;
      return { frontmatter, body, elapsedMs, task_frontmatter_parse_ms: frontmatterParseMs, task_body_parse_ms: bodyParseMs };
    } finally {
      if (benchmark) {
        this.recordFile(benchmark, fileType, todoPath, performance.now() - start);
        benchmark.task_frontmatter_parse_ms += frontmatterParseMs;
        benchmark.task_body_parse_ms += bodyParseMs;
      }
    }
  }

  async saveTask(task: Task, body?: string, appendBody = false): Promise<void> {
    const filePath = join(this.tasksDir, task.file);
    const existing = await readFile(filePath, "utf8");
    const { frontmatter, body: existingBody } = parseTodo(existing);
    let contentBody = existingBody;

    if (body !== undefined && appendBody) {
      if (!contentBody) {
        contentBody = body;
      } else if (contentBody.endsWith("\n\n")) {
        contentBody = `${contentBody}${body}`;
      } else if (contentBody.endsWith("\n")) {
        contentBody = `${contentBody}\n${body}`;
      } else {
        contentBody = `${contentBody}\n\n${body}`;
      }
    } else if (body !== undefined) {
      contentBody = body;
    }

    frontmatter.title = task.title;
    frontmatter.status = task.status;
    frontmatter.estimate_hours = task.estimateHours;
    frontmatter.complexity = task.complexity;
    frontmatter.priority = task.priority;
    frontmatter.depends_on = task.dependsOn;
    frontmatter.tags = task.tags;
    frontmatter.claimed_by = task.claimedBy ?? null;
    frontmatter.claimed_at = task.claimedAt?.toISOString() ?? null;
    frontmatter.started_at = task.startedAt?.toISOString() ?? null;
    frontmatter.completed_at = task.completedAt?.toISOString() ?? null;
    frontmatter.created_at = task.createdAt?.toISOString() ?? null;
    frontmatter.updated_at = task.updatedAt?.toISOString() ?? null;
    if (task.reason !== undefined) {
      frontmatter.reason = task.reason;
    } else {
      delete frontmatter.reason;
    }
    if (task.durationMinutes !== undefined) frontmatter.duration_minutes = task.durationMinutes;
    await writeFile(filePath, `---\n${stringify(frontmatter)}---\n${contentBody}`);
  }

  private async loadTodoFrontmatter(
    todoPath: string,
    benchmark?: BenchmarkReport,
    fileType = "todo_file",
  ): Promise<{ frontmatter: AnyRec; elapsedMs: number; task_frontmatter_parse_ms?: number; task_body_parse_ms?: number }> {
    const start = benchmark ? performance.now() : 0;
    let frontmatter: AnyRec = {};
    let elapsedMs = 0;
    let frontmatterParseMs = 0;
    try {
      const content = await readFile(todoPath, "utf8");
      const lines = content.split(/\r?\n/);
      if (lines[0]?.trim() !== "---") {
        return { frontmatter, elapsedMs: 0 };
      }
      const bodyLines: string[] = [];
      for (let i = 1; i < lines.length; i += 1) {
        const line = lines[i] ?? "";
        if (line.trim() === "---") {
          break;
        }
        bodyLines.push(line);
      }

      if (bodyLines.length) {
        const parseStart = benchmark ? performance.now() : 0;
        frontmatter = (parse(bodyLines.join("\n")) as AnyRec) ?? {};
        frontmatterParseMs = benchmark ? performance.now() - parseStart : 0;
      }
      elapsedMs = benchmark ? performance.now() - start : 0;
      return {
        frontmatter,
        elapsedMs,
        task_frontmatter_parse_ms: frontmatterParseMs,
      };
    } finally {
      if (benchmark) {
        this.recordFile(benchmark, fileType, todoPath, performance.now() - start);
        benchmark.task_frontmatter_parse_ms += frontmatterParseMs;
      }
    }
  }

  private async loadBugs(
    benchmark?: BenchmarkReport,
    loadMode: LoadMode = "full",
    parseTaskBody = true,
    includeBugs = true,
  ): Promise<import("./models").Task[]> {
    if (!includeBugs) return [];

    const bugsDir = join(this.tasksDir, "bugs");
    const indexPath = join(bugsDir, "index.yaml");
    if (!existsSync(indexPath)) return [];
    const idx = this.mustYaml(indexPath, benchmark, "bug_index");
    const bugs: import("./models").Task[] = [];
    for (const entry of ((idx.bugs as AnyRec[]) ?? [])) {
      if (typeof entry !== "object" || entry === null) continue;
      const filename = String(entry.file ?? "");
      if (!filename) continue;
      const filePath = join(bugsDir, filename);
      if (benchmark) {
        benchmark.counts.tasks += 1;
      }
      let frontmatter: AnyRec;
      if (loadMode === "index") {
        frontmatter = { ...entry };
      } else {
        if (!existsSync(filePath)) {
          if (benchmark) {
            benchmark.missing_task_files += 1;
          }
          continue;
        }
        const parsed = loadMode === "full"
          ? await this.loadTodoFile(filePath, benchmark, "bug_file", parseTaskBody)
          : await this.loadTodoFrontmatter(filePath, benchmark, "bug_file");
        frontmatter = parsed.frontmatter;
      }
      bugs.push({
        id: String(frontmatter.id ?? ""),
        title: fallbackAuxTitle(
          filename,
          String(frontmatter.id ?? ""),
          String(entry.title ?? ""),
          String(frontmatter.title ?? ""),
        ),
        file: join("bugs", filename),
        status: coerceStatus(frontmatter.status, Status.PENDING),
        estimateHours: estimateHoursFrom(frontmatter),
        complexity: (frontmatter.complexity as Complexity) ?? Complexity.MEDIUM,
        priority: (frontmatter.priority as Priority) ?? Priority.MEDIUM,
        dependsOn: ((frontmatter.depends_on as string[]) ?? []).slice(),
        claimedBy: frontmatter.claimed_by as string | undefined,
        claimedAt: frontmatter.claimed_at ? new Date(String(frontmatter.claimed_at)) : undefined,
        startedAt: frontmatter.started_at ? new Date(String(frontmatter.started_at)) : undefined,
        completedAt: frontmatter.completed_at ? new Date(String(frontmatter.completed_at)) : undefined,
        createdAt: frontmatter.created_at ? new Date(String(frontmatter.created_at)) : undefined,
        updatedAt: frontmatter.updated_at ? new Date(String(frontmatter.updated_at)) : undefined,
        durationMinutes: frontmatter.duration_minutes ? Number(frontmatter.duration_minutes) : undefined,
        tags: ((frontmatter.tags as string[]) ?? []).slice(),
        reason: frontmatter.reason as string | undefined,
        epicId: undefined,
        milestoneId: undefined,
        phaseId: undefined,
      });
    }
    return bugs;
  }

  private async loadIdeas(
    benchmark?: BenchmarkReport,
    loadMode: LoadMode = "full",
    parseTaskBody = true,
    includeIdeas = true,
  ): Promise<import("./models").Task[]> {
    if (!includeIdeas) return [];

    const ideasDir = join(this.tasksDir, "ideas");
    const indexPath = join(ideasDir, "index.yaml");
    if (!existsSync(indexPath)) return [];
    const idx = this.mustYaml(indexPath, benchmark, "idea_index");
    const ideas: import("./models").Task[] = [];
    for (const entry of ((idx.ideas as AnyRec[]) ?? [])) {
      if (typeof entry !== "object" || entry === null) continue;
      const filename = String(entry.file ?? "");
      if (!filename) continue;
      const filePath = join(ideasDir, filename);
      if (benchmark) {
        benchmark.counts.tasks += 1;
      }
      let frontmatter: AnyRec;
      if (loadMode === "index") {
        frontmatter = { ...entry };
      } else {
        if (!existsSync(filePath)) {
          if (benchmark) {
            benchmark.missing_task_files += 1;
          }
          continue;
        }
        const { frontmatter: fm } = loadMode === "full"
          ? await this.loadTodoFile(filePath, benchmark, "idea_file", parseTaskBody)
          : await this.loadTodoFrontmatter(filePath, benchmark, "idea_file");
        frontmatter = fm;
      }
      ideas.push({
        id: String(frontmatter.id ?? ""),
        title: fallbackAuxTitle(
          filename,
          String(frontmatter.id ?? ""),
          String(entry.title ?? ""),
          String(frontmatter.title ?? ""),
        ),
        file: join("ideas", filename),
        status: coerceStatus(frontmatter.status, Status.PENDING),
        estimateHours: estimateHoursFrom(frontmatter),
        complexity: (frontmatter.complexity as Complexity) ?? Complexity.MEDIUM,
        priority: (frontmatter.priority as Priority) ?? Priority.MEDIUM,
        dependsOn: ((frontmatter.depends_on as string[]) ?? []).slice(),
        claimedBy: frontmatter.claimed_by as string | undefined,
        claimedAt: frontmatter.claimed_at ? new Date(String(frontmatter.claimed_at)) : undefined,
        startedAt: frontmatter.started_at ? new Date(String(frontmatter.started_at)) : undefined,
        completedAt: frontmatter.completed_at ? new Date(String(frontmatter.completed_at)) : undefined,
        createdAt: frontmatter.created_at ? new Date(String(frontmatter.created_at)) : undefined,
        updatedAt: frontmatter.updated_at ? new Date(String(frontmatter.updated_at)) : undefined,
        durationMinutes: frontmatter.duration_minutes ? Number(frontmatter.duration_minutes) : undefined,
        tags: ((frontmatter.tags as string[]) ?? []).slice(),
        reason: frontmatter.reason as string | undefined,
        epicId: undefined,
        milestoneId: undefined,
        phaseId: undefined,
      });
    }
    return ideas;
  }

  async createBug(data: { title: string; priority?: string; estimate?: number; complexity?: string; dependsOn?: string[]; tags?: string[]; simple?: boolean; body?: string }): Promise<import("./models").Task> {
    const bugsDir = join(this.tasksDir, "bugs");
    await mkdir(bugsDir, { recursive: true });
    const indexPath = join(bugsDir, "index.yaml");

    // Determine next bug number
    let nextNum = 1;
    if (existsSync(indexPath)) {
      const idx = this.mustYaml(indexPath);
      const existing = ((idx.bugs as AnyRec[]) ?? [])
        .map((e) => String(e.file ?? ""))
        .map((f) => f.match(/^B(\d+)/))
        .filter(Boolean)
        .map((m) => Number(m![1]));
      if (existing.length) nextNum = Math.max(...existing) + 1;
    }

    const bugId = `B${String(nextNum).padStart(3, "0")}`;
    const filename = `${bugId}-${slugify(data.title)}.todo`;
    const filePath = join(bugsDir, filename);

    const now = utcNow();
    const fm: AnyRec = {
      id: bugId,
      title: data.title,
      status: "pending",
      estimate_hours: data.estimate ?? 1,
      complexity: data.complexity ?? "medium",
      priority: data.priority ?? "high",
      depends_on: data.dependsOn ?? [],
      tags: data.tags ?? [],
      created_at: now.toISOString(),
      updated_at: now.toISOString(),
    };
    let body: string;
    if (data.body) {
      body = data.body;
    } else if (data.simple) {
      body = `\n${data.title}\n`;
    } else {
      body = `\n# ${data.title}\n\n## Steps to Reproduce\n\n1. TODO: Add steps\n\n## Expected Behavior\n\nTODO: Describe expected behavior\n\n## Actual Behavior\n\nTODO: Describe actual behavior\n`;
    }
    await writeFile(filePath, `---\n${stringify(fm)}---\n${body}`);

    // Update bugs index
    const idxData: AnyRec = existsSync(indexPath) ? this.mustYaml(indexPath) : { bugs: [] };
    const bugsList = ((idxData.bugs as AnyRec[]) ?? []).slice();
    bugsList.push({ file: filename });
    idxData.bugs = bugsList;
    await mkdir(dirname(indexPath), { recursive: true });
    await writeFile(indexPath, stringify(idxData));

    return {
      id: bugId,
      title: data.title,
      file: join("bugs", filename),
      status: Status.PENDING,
      estimateHours: data.estimate ?? 1,
      complexity: (data.complexity as Complexity) ?? Complexity.MEDIUM,
      priority: (data.priority as Priority) ?? Priority.HIGH,
      dependsOn: data.dependsOn ?? [],
      createdAt: now,
      updatedAt: now,
      tags: data.tags ?? [],
      epicId: undefined,
      milestoneId: undefined,
      phaseId: undefined,
    };
  }

  async createFixed(data: { title: string; description?: string; at?: string; tags?: string[]; body?: string }): Promise<import("./models").Task> {
    const fixesDir = join(this.tasksDir, "fixes");
    await mkdir(fixesDir, { recursive: true });
    const indexPath = join(fixesDir, "index.yaml");

    let createdAt = new Date();
    if (data.at) {
      const normalizedAt = /[zZ]|[+-]\d{2}:\d{2}$/.test(data.at) ? data.at : `${data.at}Z`;
      const parsed = new Date(normalizedAt);
      if (Number.isNaN(parsed.getTime())) {
        throw new Error("fixed --at must be an ISO 8601 timestamp");
      }
      createdAt = parsed;
    }

    const monthDirName = createdAt.toISOString().slice(0, 7);
    const monthDir = join(fixesDir, monthDirName);
    await mkdir(monthDir, { recursive: true });

    let nextNum = 1;
    if (existsSync(indexPath)) {
      const idx = this.mustYaml(indexPath);
      const existing = ((idx.fixes as AnyRec[]) ?? [])
        .filter((e) => e && typeof e === "object")
        .map((entry) => {
          const e = entry as AnyRec;
          const id = String(e.id ?? "");
          const idMatch = id.match(/^F(\d+)$/);
          if (idMatch?.[1]) return Number(idMatch[1]);
          const file = String(e.file ?? "");
          const fileMatch = file.match(/(?:^|\/)F(\d+)-/);
          return fileMatch?.[1] ? Number(fileMatch[1]) : 0;
        })
        .filter((n) => Number.isFinite(n) && n > 0);
      if (existing.length) nextNum = Math.max(...existing) + 1;
    }

    const fixedId = `F${String(nextNum).padStart(3, "0")}`;
    const filename = `${fixedId}-${slugify(data.title)}.todo`;
    const filePath = join(monthDir, filename);

    const fm: AnyRec = {
      id: fixedId,
      type: "fixed",
      title: data.title,
      description: data.description ?? data.title,
      status: "done",
      estimate_hours: 0.0,
      complexity: "low",
      priority: "low",
      depends_on: [],
      tags: data.tags ?? [],
      created_at: createdAt.toISOString(),
      updated_at: createdAt.toISOString(),
      completed_at: createdAt.toISOString(),
    };
    await writeFile(filePath, `---\n${stringify(fm)}---\n${data.body ?? ""}`);

    const idxData: AnyRec = existsSync(indexPath) ? this.mustYaml(indexPath) : { fixes: [] };
    const fixesList = ((idxData.fixes as AnyRec[]) ?? []).slice();
    fixesList.push({ id: fixedId, file: `${monthDirName}/${filename}` });
    idxData.fixes = fixesList;
    await mkdir(dirname(indexPath), { recursive: true });
    await writeFile(indexPath, stringify(idxData));

    return {
      id: fixedId,
      title: data.title,
      file: join("fixes", monthDirName, filename),
      status: Status.DONE,
      estimateHours: 0.0,
      complexity: Complexity.LOW,
      priority: Priority.LOW,
      dependsOn: [],
      createdAt,
      updatedAt: createdAt,
      tags: data.tags ?? [],
      epicId: undefined,
      milestoneId: undefined,
      phaseId: undefined,
    };
  }

  async createIdea(data: { title: string; estimate?: number; complexity?: string; priority?: string; dependsOn?: string[]; tags?: string[]; simple?: boolean; body?: string }): Promise<import("./models").Task> {
    const ideasDir = join(this.tasksDir, "ideas");
    await mkdir(ideasDir, { recursive: true });
    const indexPath = join(ideasDir, "index.yaml");

    let nextNum = 1;
    if (existsSync(indexPath)) {
      const idx = this.mustYaml(indexPath);
      const existing = ((idx.ideas as AnyRec[]) ?? [])
        .map((e) => {
          const id = String(e.id ?? "");
          const idMatch = id.match(/^I(\d+)$/);
          if (idMatch?.[1]) return Number(idMatch[1]);
          const file = String(e.file ?? "");
          const fileMatch = file.match(/^I(\d+)/);
          return fileMatch?.[1] ? Number(fileMatch[1]) : 0;
        })
        .filter((n) => Number.isFinite(n) && n > 0);
      if (existing.length) nextNum = Math.max(...existing) + 1;
    }

    const ideaId = `I${String(nextNum).padStart(3, "0")}`;
    const filename = `${ideaId}-${slugify(data.title)}.todo`;
    const filePath = join(ideasDir, filename);

    const now = utcNow();
    const fm: AnyRec = {
      id: ideaId,
      title: data.title,
      status: "pending",
      estimate_hours: data.estimate ?? 10,
      complexity: data.complexity ?? "medium",
      priority: data.priority ?? "medium",
      depends_on: data.dependsOn ?? [],
      tags: data.tags ?? ["idea", "planning"],
      created_at: now.toISOString(),
      updated_at: now.toISOString(),
    };

    const body = typeof data.body === "string" && data.body.trim().length > 0
      ? data.body
      : data.simple
        ? `\n${data.title}\n`
        : `
# Idea Intake: ${data.title}

## Original Idea

${data.title}

## Planning Task (Equivalent of /plan-task)

- Run \`/plan-task "${data.title}"\` to decompose this idea into actionable work.
- Confirm placement in the current \`.tasks\` hierarchy before creating work items.

## Ingest Plan Into .tasks

- Create implementation items with \`tasks add\` and related hierarchy commands (\`tasks add-epic\`, \`tasks add-milestone\`, \`tasks add-phase\`) as needed.
- Create follow-up defects with \`tasks bug\` when bug-style work is identified.
- Record all created IDs below and wire dependencies.

## Created Work Items

- Add created task IDs
- Add created bug IDs (if any)

## Completion Criteria

- Idea has been decomposed into concrete \`.tasks\` work items.
- New items include clear acceptance criteria and dependencies.
- This idea intake is updated with created IDs and marked done.
`;
    await writeFile(filePath, `---\n${stringify(fm)}---\n${body}`);

    const idxData: AnyRec = existsSync(indexPath) ? this.mustYaml(indexPath) : { ideas: [] };
    const ideasList = ((idxData.ideas as AnyRec[]) ?? []).slice();
    ideasList.push({ id: ideaId, file: filename });
    idxData.ideas = ideasList;
    await mkdir(dirname(indexPath), { recursive: true });
    await writeFile(indexPath, stringify(idxData));

    return {
      id: ideaId,
      title: data.title,
      file: join("ideas", filename),
      status: Status.PENDING,
      estimateHours: data.estimate ?? 10,
      complexity: (data.complexity as Complexity) ?? Complexity.MEDIUM,
      priority: (data.priority as Priority) ?? Priority.MEDIUM,
      dependsOn: data.dependsOn ?? [],
      createdAt: now,
      updatedAt: now,
      tags: data.tags ?? ["idea", "planning"],
      epicId: undefined,
      milestoneId: undefined,
      phaseId: undefined,
    };
  }

  async writeYaml(path: string, value: AnyRec): Promise<void> {
    await mkdir(dirname(path), { recursive: true });
    await writeFile(path, stringify(value));
  }

  private mustYaml(path: string, benchmark?: BenchmarkReport, fileType?: string): AnyRec {
    const text = readFileSync(path, "utf8");
    const start = performance.now();
    try {
      const parsed = parse(text);
      if (!parsed || typeof parsed !== "object") {
        throw new Error(`YAML file invalid: ${path}`);
      }
      return parsed as AnyRec;
    } finally {
      if (benchmark && fileType) {
        this.recordFile(benchmark, fileType, path, performance.now() - start);
      }
    }
  }

  private leafId(fullId: string): string {
    return fullId.split(".").at(-1) ?? fullId;
  }

  private replaceMappedValues(value: unknown, remap: Record<string, string>): unknown {
    if (typeof value === "string") return remap[value] ?? value;
    if (Array.isArray(value)) return value.map((v) => this.replaceMappedValues(v, remap));
    if (value && typeof value === "object") {
      const out: AnyRec = {};
      for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
        out[k] = this.replaceMappedValues(v, remap);
      }
      return out;
    }
    return value;
  }

  private async applyIdRemap(remap: Record<string, string>): Promise<void> {
    if (Object.keys(remap).length === 0) return;

    const root = this.mustYaml(join(this.tasksDir, "index.yaml"));
    await this.writeYaml(join(this.tasksDir, "index.yaml"), this.replaceMappedValues(root, remap) as AnyRec);

    const tree = await this.load("metadata");
    for (const phase of tree.phases) {
      const phaseIndexPath = join(this.tasksDir, phase.path, "index.yaml");
      if (existsSync(phaseIndexPath)) {
        const data = this.mustYaml(phaseIndexPath);
        await this.writeYaml(phaseIndexPath, this.replaceMappedValues(data, remap) as AnyRec);
      }
      for (const milestone of phase.milestones) {
        const msIndexPath = join(this.tasksDir, phase.path, milestone.path, "index.yaml");
        if (existsSync(msIndexPath)) {
          const data = this.mustYaml(msIndexPath);
          await this.writeYaml(msIndexPath, this.replaceMappedValues(data, remap) as AnyRec);
        }
        for (const epic of milestone.epics) {
          const epicIndexPath = join(this.tasksDir, phase.path, milestone.path, epic.path, "index.yaml");
          if (existsSync(epicIndexPath)) {
            const data = this.mustYaml(epicIndexPath);
            await this.writeYaml(epicIndexPath, this.replaceMappedValues(data, remap) as AnyRec);
          }
          for (const task of epic.tasks) {
            const taskPath = join(this.tasksDir, task.file);
            if (!existsSync(taskPath)) continue;
            const parsed = parseTodo(await readFile(taskPath, "utf8"));
            const fm = this.replaceMappedValues(parsed.frontmatter, remap) as AnyRec;
            await writeFile(taskPath, `---\n${stringify(fm)}---\n${parsed.body}`);
          }
        }
      }
    }

    for (const auxDir of ["bugs", "ideas"]) {
      const idxPath = join(this.tasksDir, auxDir, "index.yaml");
      if (existsSync(idxPath)) {
        const idx = this.mustYaml(idxPath);
        await this.writeYaml(idxPath, this.replaceMappedValues(idx, remap) as AnyRec);
      }
    }

    const refreshed = await this.load("metadata");
    for (const aux of [...(refreshed.bugs ?? []), ...(refreshed.ideas ?? [])]) {
      const filePath = join(this.tasksDir, aux.file);
      if (!existsSync(filePath)) continue;
      const parsed = parseTodo(await readFile(filePath, "utf8"));
      const fm = this.replaceMappedValues(parsed.frontmatter, remap) as AnyRec;
      await writeFile(filePath, `---\n${stringify(fm)}---\n${parsed.body}`);
    }

    for (const runtime of [".context.yaml", ".sessions.yaml"]) {
      const path = join(this.tasksDir, runtime);
      if (!existsSync(path)) continue;
      const data = this.mustYaml(path);
      await this.writeYaml(path, this.replaceMappedValues(data, remap) as AnyRec);
    }
  }

  async moveItem(sourceId: string, destId: string): Promise<{ source_id: string; dest_id: string; new_id: string; remapped_ids: Record<string, string> }> {
    const src = TaskPath.parse(sourceId);
    const dst = TaskPath.parse(destId);
    const tree = await this.load("metadata");
    const remap: Record<string, string> = {};

    if (src.isTask && dst.isEpic) {
      const srcTask = tree.phases.flatMap((p) => p.milestones.flatMap((m) => m.epics.flatMap((e) => e.tasks))).find((t) => t.id === sourceId);
      if (!srcTask) throw new Error(`Task not found: ${sourceId}`);
      const dstEpic = tree.phases.flatMap((p) => p.milestones.flatMap((m) => m.epics)).find((e) => e.id === destId);
      if (!dstEpic) throw new Error(`Epic not found: ${destId}`);
      const srcEpic = tree.phases.flatMap((p) => p.milestones.flatMap((m) => m.epics)).find((e) => e.id === srcTask.epicId);
      const srcMilestone = tree.phases.flatMap((p) => p.milestones).find((m) => m.id === srcTask.milestoneId);
      const srcPhase = tree.phases.find((p) => p.id === srcTask.phaseId);
      const dstMilestone = tree.phases.flatMap((p) => p.milestones).find((m) => m.id === dstEpic.milestoneId);
      const dstPhase = tree.phases.find((p) => p.id === dstEpic.phaseId);
      if (!srcEpic || !srcMilestone || !srcPhase || !dstMilestone || !dstPhase) throw new Error("Could not resolve source/destination hierarchy paths");

      const srcEpicDir = join(this.tasksDir, srcPhase.path, srcMilestone.path, srcEpic.path);
      const dstEpicDir = join(this.tasksDir, dstPhase.path, dstMilestone.path, dstEpic.path);
      const oldFilename = srcTask.file.split("/").at(-1)!;
      const oldFile = join(srcEpicDir, oldFilename);
      if (!existsSync(oldFile)) throw new Error(`Task file not found: ${oldFile}`);

      const dstEpicIndex = this.mustYaml(join(dstEpicDir, "index.yaml"));
      const dstTasks = ((dstEpicIndex.tasks as AnyRec[]) ?? []).slice();
      const nums = dstTasks.map((t) => Number(String(t.id ?? "").match(/T(\d+)/)?.[1] ?? "0")).filter((n) => Number.isFinite(n));
      const next = (nums.length ? Math.max(...nums) : 0) + 1;
      const newShort = `T${String(next).padStart(3, "0")}`;
      const newId = `${dstEpic.id}.${newShort}`;
      const newFilename = `${newShort}-${slugify(srcTask.title)}.todo`;
      await rename(oldFile, join(dstEpicDir, newFilename));

      const srcEpicIndexPath = join(srcEpicDir, "index.yaml");
      const srcEpicIndex = this.mustYaml(srcEpicIndexPath);
      const oldLeaf = this.leafId(sourceId);
      srcEpicIndex.tasks = (((srcEpicIndex.tasks as AnyRec[]) ?? [])).filter((entry) => {
        const id = String(entry.id ?? "");
        const file = String(entry.file ?? entry.path ?? "");
        return !(id === sourceId || this.leafId(id) === oldLeaf || file === oldFilename);
      });
      await this.writeYaml(srcEpicIndexPath, srcEpicIndex);

      dstTasks.push({
        id: newShort,
        file: newFilename,
        title: srcTask.title,
        status: srcTask.status,
        estimate_hours: srcTask.estimateHours,
        complexity: srcTask.complexity,
        priority: srcTask.priority,
        depends_on: srcTask.dependsOn,
      });
      dstEpicIndex.tasks = dstTasks;
      await this.writeYaml(join(dstEpicDir, "index.yaml"), dstEpicIndex);

      remap[sourceId] = newId;
    } else if (src.isEpic && dst.isMilestone) {
      const srcEpic = tree.phases.flatMap((p) => p.milestones.flatMap((m) => m.epics)).find((e) => e.id === sourceId);
      if (!srcEpic) throw new Error(`Epic not found: ${sourceId}`);
      const dstMilestone = tree.phases.flatMap((p) => p.milestones).find((m) => m.id === destId);
      if (!dstMilestone) throw new Error(`Milestone not found: ${destId}`);
      const srcMilestone = tree.phases.flatMap((p) => p.milestones).find((m) => m.id === srcEpic.milestoneId);
      const srcPhase = tree.phases.find((p) => p.id === srcEpic.phaseId);
      const dstPhase = tree.phases.find((p) => p.id === dstMilestone.phaseId);
      if (!srcMilestone || !srcPhase || !dstPhase) throw new Error("Could not resolve source/destination hierarchy paths");

      const srcMsDir = join(this.tasksDir, srcPhase.path, srcMilestone.path);
      const dstMsDir = join(this.tasksDir, dstPhase.path, dstMilestone.path);
      const srcEpicDir = join(srcMsDir, srcEpic.path);

      const dstMsIndex = this.mustYaml(join(dstMsDir, "index.yaml"));
      const dstEpics = ((dstMsIndex.epics as AnyRec[]) ?? []).slice();
      const nums = dstEpics.map((e) => Number(String(e.id ?? "").match(/E(\d+)/)?.[1] ?? "0")).filter((n) => Number.isFinite(n));
      const next = (nums.length ? Math.max(...nums) : 0) + 1;
      const newShort = `E${next}`;
      const newEpicId = `${dstMilestone.id}.${newShort}`;
      const newDirName = `${String(next).padStart(2, "0")}-${slugify(srcEpic.name)}`;
      await rename(srcEpicDir, join(dstMsDir, newDirName));

      const srcMsIndexPath = join(srcMsDir, "index.yaml");
      const srcMsIndex = this.mustYaml(srcMsIndexPath);
      const oldLeaf = this.leafId(sourceId);
      srcMsIndex.epics = (((srcMsIndex.epics as AnyRec[]) ?? [])).filter((entry) => {
        const id = String(entry.id ?? "");
        const path = String(entry.path ?? "");
        return !(id === sourceId || this.leafId(id) === oldLeaf || path === srcEpic.path);
      });
      await this.writeYaml(srcMsIndexPath, srcMsIndex);

      dstEpics.push({
        id: newShort,
        name: srcEpic.name,
        path: newDirName,
        status: srcEpic.status,
        estimate_hours: srcEpic.estimateHours,
        complexity: srcEpic.complexity,
        depends_on: srcEpic.dependsOn,
        description: srcEpic.description ?? "",
      });
      dstMsIndex.epics = dstEpics;
      await this.writeYaml(join(dstMsDir, "index.yaml"), dstMsIndex);

      remap[sourceId] = newEpicId;
      for (const t of srcEpic.tasks) {
        remap[t.id] = t.id.replace(`${sourceId}.`, `${newEpicId}.`);
      }
    } else if (src.isMilestone && dst.isPhase) {
      const srcMilestone = tree.phases.flatMap((p) => p.milestones).find((m) => m.id === sourceId);
      if (!srcMilestone) throw new Error(`Milestone not found: ${sourceId}`);
      const dstPhase = tree.phases.find((p) => p.id === destId);
      if (!dstPhase) throw new Error(`Phase not found: ${destId}`);
      const srcPhase = tree.phases.find((p) => p.id === srcMilestone.phaseId);
      if (!srcPhase) throw new Error("Could not resolve source phase");

      const srcPhaseDir = join(this.tasksDir, srcPhase.path);
      const dstPhaseDir = join(this.tasksDir, dstPhase.path);
      const srcMsDir = join(srcPhaseDir, srcMilestone.path);

      const dstPhaseIndex = this.mustYaml(join(dstPhaseDir, "index.yaml"));
      const dstMilestones = ((dstPhaseIndex.milestones as AnyRec[]) ?? []).slice();
      const nums = dstMilestones.map((m) => Number(String(m.id ?? "").match(/M(\d+)/)?.[1] ?? "0")).filter((n) => Number.isFinite(n));
      const next = (nums.length ? Math.max(...nums) : 0) + 1;
      const newShort = `M${next}`;
      const newMsId = `${dstPhase.id}.${newShort}`;
      const newDirName = `${String(next).padStart(2, "0")}-${slugify(srcMilestone.name)}`;
      await rename(srcMsDir, join(dstPhaseDir, newDirName));

      const srcPhaseIndexPath = join(srcPhaseDir, "index.yaml");
      const srcPhaseIndex = this.mustYaml(srcPhaseIndexPath);
      const oldLeaf = this.leafId(sourceId);
      srcPhaseIndex.milestones = (((srcPhaseIndex.milestones as AnyRec[]) ?? [])).filter((entry) => {
        const id = String(entry.id ?? "");
        const path = String(entry.path ?? "");
        return !(id === sourceId || this.leafId(id) === oldLeaf || path === srcMilestone.path);
      });
      await this.writeYaml(srcPhaseIndexPath, srcPhaseIndex);

      dstMilestones.push({
        id: newShort,
        name: srcMilestone.name,
        path: newDirName,
        status: srcMilestone.status,
        estimate_hours: srcMilestone.estimateHours,
        complexity: srcMilestone.complexity,
        depends_on: srcMilestone.dependsOn,
        description: srcMilestone.description ?? "",
      });
      dstPhaseIndex.milestones = dstMilestones;
      await this.writeYaml(join(dstPhaseDir, "index.yaml"), dstPhaseIndex);

      remap[sourceId] = newMsId;
      for (const epic of srcMilestone.epics) {
        const newEpicId = epic.id.replace(`${sourceId}.`, `${newMsId}.`);
        remap[epic.id] = newEpicId;
        for (const task of epic.tasks) {
          remap[task.id] = task.id.replace(`${epic.id}.`, `${newEpicId}.`);
        }
      }
    } else {
      throw new Error("Invalid move: supported moves are task->epic, epic->milestone, milestone->phase");
    }

    await this.applyIdRemap(remap);
    return {
      source_id: sourceId,
      dest_id: destId,
      new_id: remap[sourceId] ?? sourceId,
      remapped_ids: remap,
    };
  }

  async setItemLocked(itemId: string, locked: boolean): Promise<string> {
    const path = TaskPath.parse(itemId);
    const tree = await this.load("metadata");
    const desired = Boolean(locked);

    if (path.isPhase) {
      const phase = tree.phases.find((p) => p.id === path.fullId);
      if (!phase) throw new Error(`Phase not found: ${itemId}`);
      const rootPath = join(this.tasksDir, "index.yaml");
      const root = this.mustYaml(rootPath);
      const phases = ((root.phases as AnyRec[]) ?? []).slice();
      for (const p of phases) {
        if (String(p.id ?? "") === phase.id || String(p.id ?? "") === path.phase) {
          p.locked = desired;
          break;
        }
      }
      root.phases = phases;
      await this.writeYaml(rootPath, root);
      const phaseIndexPath = join(this.tasksDir, phase.path, "index.yaml");
      if (existsSync(phaseIndexPath)) {
        const idx = this.mustYaml(phaseIndexPath);
        idx.locked = desired;
        await this.writeYaml(phaseIndexPath, idx);
      }
      return phase.id;
    }

    if (path.isMilestone) {
      const milestone = tree.phases.flatMap((p) => p.milestones).find((m) => m.id === path.fullId);
      if (!milestone) throw new Error(`Milestone not found: ${itemId}`);
      const phase = tree.phases.find((p) => p.id === milestone.phaseId);
      if (!phase) throw new Error(`Phase not found for milestone: ${itemId}`);
      const phaseIndexPath = join(this.tasksDir, phase.path, "index.yaml");
      const phaseIndex = this.mustYaml(phaseIndexPath);
      const milestones = ((phaseIndex.milestones as AnyRec[]) ?? []).slice();
      for (const m of milestones) {
        const id = String(m.id ?? "");
        if (id === milestone.id || id === (path.milestone ?? "")) {
          m.locked = desired;
          break;
        }
      }
      phaseIndex.milestones = milestones;
      await this.writeYaml(phaseIndexPath, phaseIndex);
      const msIndexPath = join(this.tasksDir, phase.path, milestone.path, "index.yaml");
      if (existsSync(msIndexPath)) {
        const msIndex = this.mustYaml(msIndexPath);
        msIndex.locked = desired;
        await this.writeYaml(msIndexPath, msIndex);
      }
      return milestone.id;
    }

    if (path.isEpic) {
      const epic = tree.phases.flatMap((p) => p.milestones.flatMap((m) => m.epics)).find((e) => e.id === path.fullId);
      if (!epic) throw new Error(`Epic not found: ${itemId}`);
      const milestone = tree.phases.flatMap((p) => p.milestones).find((m) => m.id === epic.milestoneId);
      const phase = tree.phases.find((p) => p.id === epic.phaseId);
      if (!milestone || !phase) throw new Error(`Could not resolve parent paths for epic: ${itemId}`);
      const msIndexPath = join(this.tasksDir, phase.path, milestone.path, "index.yaml");
      const msIndex = this.mustYaml(msIndexPath);
      const epics = ((msIndex.epics as AnyRec[]) ?? []).slice();
      for (const e of epics) {
        const id = String(e.id ?? "");
        if (id === epic.id || id === (path.epic ?? "")) {
          e.locked = desired;
          break;
        }
      }
      msIndex.epics = epics;
      await this.writeYaml(msIndexPath, msIndex);
      const epicIndexPath = join(this.tasksDir, phase.path, milestone.path, epic.path, "index.yaml");
      if (existsSync(epicIndexPath)) {
        const epicIndex = this.mustYaml(epicIndexPath);
        epicIndex.locked = desired;
        await this.writeYaml(epicIndexPath, epicIndex);
      }
      return epic.id;
    }

    throw new Error("lock/unlock supports only phase, milestone, or epic IDs");
  }

  async setItemNotDone(itemId: string): Promise<{ item_id: string; updated_tasks: number }> {
    const tree = await this.load("metadata");

    const resetTask = async (task: Task): Promise<void> => {
      task.status = Status.PENDING;
      task.claimedBy = undefined;
      task.claimedAt = undefined;
      task.startedAt = undefined;
      task.completedAt = undefined;
      task.updatedAt = utcNow();
      task.durationMinutes = undefined;
      await this.saveTask(task);
    };

    const directTask = tree.phases.flatMap((p) => p.milestones.flatMap((m) => m.epics.flatMap((e) => e.tasks)))
      .concat(tree.bugs ?? [])
      .concat(tree.ideas ?? [])
      .find((t) => t.id === itemId);
    if (directTask) {
      await resetTask(directTask);
      return { item_id: directTask.id, updated_tasks: 1 };
    }

    const path = TaskPath.parse(itemId);

    if (path.isPhase) {
      const phase = tree.phases.find((p) => p.id === path.fullId);
      if (!phase) throw new Error(`Phase not found: ${itemId}`);

      const rootPath = join(this.tasksDir, "index.yaml");
      const root = this.mustYaml(rootPath);
      for (const entry of ((root.phases as AnyRec[]) ?? [])) {
        if (String(entry.id ?? "") === phase.id || String(entry.id ?? "") === path.phase) {
          entry.status = Status.PENDING;
          break;
        }
      }
      await this.writeYaml(rootPath, root);

      const phaseIndexPath = join(this.tasksDir, phase.path, "index.yaml");
      if (existsSync(phaseIndexPath)) {
        const phaseIndex = this.mustYaml(phaseIndexPath);
        phaseIndex.status = Status.PENDING;
        for (const entry of ((phaseIndex.milestones as AnyRec[]) ?? [])) entry.status = Status.PENDING;
        await this.writeYaml(phaseIndexPath, phaseIndex);
      }

      let updated = 0;
      for (const milestone of phase.milestones) {
        const msIndexPath = join(this.tasksDir, phase.path, milestone.path, "index.yaml");
        if (existsSync(msIndexPath)) {
          const msIndex = this.mustYaml(msIndexPath);
          msIndex.status = Status.PENDING;
          for (const entry of ((msIndex.epics as AnyRec[]) ?? [])) entry.status = Status.PENDING;
          await this.writeYaml(msIndexPath, msIndex);
        }
        for (const epic of milestone.epics) {
          const epicIndexPath = join(this.tasksDir, phase.path, milestone.path, epic.path, "index.yaml");
          if (existsSync(epicIndexPath)) {
            const epicIndex = this.mustYaml(epicIndexPath);
            epicIndex.status = Status.PENDING;
            await this.writeYaml(epicIndexPath, epicIndex);
          }
          for (const task of epic.tasks) {
            await resetTask(task);
            updated += 1;
          }
        }
      }
      return { item_id: phase.id, updated_tasks: updated };
    }

    if (path.isMilestone) {
      const milestone = tree.phases.flatMap((p) => p.milestones).find((m) => m.id === path.fullId);
      if (!milestone) throw new Error(`Milestone not found: ${itemId}`);
      const phase = tree.phases.find((p) => p.id === milestone.phaseId);
      if (!phase) throw new Error(`Phase not found for milestone: ${itemId}`);

      const phaseIndexPath = join(this.tasksDir, phase.path, "index.yaml");
      const phaseIndex = this.mustYaml(phaseIndexPath);
      for (const entry of ((phaseIndex.milestones as AnyRec[]) ?? [])) {
        const id = String(entry.id ?? "");
        if (id === milestone.id || id === (path.milestone ?? "")) {
          entry.status = Status.PENDING;
          break;
        }
      }
      await this.writeYaml(phaseIndexPath, phaseIndex);

      const msIndexPath = join(this.tasksDir, phase.path, milestone.path, "index.yaml");
      if (existsSync(msIndexPath)) {
        const msIndex = this.mustYaml(msIndexPath);
        msIndex.status = Status.PENDING;
        for (const entry of ((msIndex.epics as AnyRec[]) ?? [])) entry.status = Status.PENDING;
        await this.writeYaml(msIndexPath, msIndex);
      }

      let updated = 0;
      for (const epic of milestone.epics) {
        const epicIndexPath = join(this.tasksDir, phase.path, milestone.path, epic.path, "index.yaml");
        if (existsSync(epicIndexPath)) {
          const epicIndex = this.mustYaml(epicIndexPath);
          epicIndex.status = Status.PENDING;
          await this.writeYaml(epicIndexPath, epicIndex);
        }
        for (const task of epic.tasks) {
          await resetTask(task);
          updated += 1;
        }
      }
      return { item_id: milestone.id, updated_tasks: updated };
    }

    if (path.isEpic) {
      const epic = tree.phases.flatMap((p) => p.milestones.flatMap((m) => m.epics)).find((e) => e.id === path.fullId);
      if (!epic) throw new Error(`Epic not found: ${itemId}`);
      const milestone = tree.phases.flatMap((p) => p.milestones).find((m) => m.id === epic.milestoneId);
      const phase = tree.phases.find((p) => p.id === epic.phaseId);
      if (!milestone || !phase) throw new Error(`Could not resolve parent paths for epic: ${itemId}`);

      const msIndexPath = join(this.tasksDir, phase.path, milestone.path, "index.yaml");
      const msIndex = this.mustYaml(msIndexPath);
      for (const entry of ((msIndex.epics as AnyRec[]) ?? [])) {
        const id = String(entry.id ?? "");
        if (id === epic.id || id === (path.epic ?? "")) {
          entry.status = Status.PENDING;
          break;
        }
      }
      await this.writeYaml(msIndexPath, msIndex);

      const epicIndexPath = join(this.tasksDir, phase.path, milestone.path, epic.path, "index.yaml");
      if (existsSync(epicIndexPath)) {
        const epicIndex = this.mustYaml(epicIndexPath);
        epicIndex.status = Status.PENDING;
        await this.writeYaml(epicIndexPath, epicIndex);
      }

      let updated = 0;
      for (const task of epic.tasks) {
        await resetTask(task);
        updated += 1;
      }
      return { item_id: epic.id, updated_tasks: updated };
    }

    throw new Error("undone supports only task, phase, milestone, or epic IDs");
  }

  async setItemDone(itemId: string): Promise<{
    item_id: string;
    epic_completed: boolean;
    milestone_completed: boolean;
    phase_completed: boolean;
    phase_locked: boolean;
  }> {
    const tree = await this.load("metadata");

    const task = [
      ...tree.phases.flatMap((p) => p.milestones.flatMap((m) => m.epics.flatMap((e) => e.tasks))),
      ...(tree.bugs ?? []),
      ...(tree.ideas ?? []),
    ].find((t) => t.id === itemId);

    if (!task) {
      throw new Error(`Task not found: ${itemId}`);
    }

    if (!task.epicId || !task.milestoneId || !task.phaseId) {
      return {
        item_id: task.id,
        epic_completed: false,
        milestone_completed: false,
        phase_completed: false,
        phase_locked: false,
      };
    }

    const epic = tree.phases
      .flatMap((p) => p.milestones.flatMap((m) => m.epics))
      .find((e) => e.id === task.epicId);
    const milestone = tree.phases
      .flatMap((p) => p.milestones)
      .find((m) => m.id === task.milestoneId);
    const phase = tree.phases.find((p) => p.id === task.phaseId);

    if (!epic || !milestone || !phase) {
      throw new Error(`Could not resolve parent hierarchy for task: ${itemId}`);
    }

    const isEpicComplete = (e: Epic): boolean => {
      return e.tasks.every((t) => t.status === Status.DONE);
    };
    const isMilestoneComplete = (m: Milestone): boolean => {
      return m.epics.every(isEpicComplete);
    };
    const isPhaseComplete = (p: Phase): boolean => {
      return p.milestones.every(isMilestoneComplete);
    };

    const epicCompleted = isEpicComplete(epic);
    const milestoneCompleted = isMilestoneComplete(milestone);
    const phaseCompleted = isPhaseComplete(phase);

    const result = {
      item_id: task.id,
      epic_completed: epicCompleted,
      milestone_completed: milestoneCompleted,
      phase_completed: phaseCompleted,
      phase_locked: false,
    };

    const leafId = (id: string): string => id.split(".").at(-1) ?? id;
    const idsMatch = (entryId: unknown, targetId: string): boolean => {
      const left = String(entryId ?? "");
      const right = targetId;
      return left === right || leafId(left) === leafId(right);
    };

    const rootPath = join(this.tasksDir, "index.yaml");
    const root = this.mustYaml(rootPath);
    for (const entry of ((root.phases as AnyRec[]) ?? [])) {
      if (idsMatch(entry.id, phase.id)) {
        if (phaseCompleted) {
          entry.status = Status.DONE;
          entry.locked = true;
          result.phase_locked = true;
        }
        break;
      }
    }
    await this.writeYaml(rootPath, root);

    const phaseIndexPath = join(this.tasksDir, phase.path, "index.yaml");
    if (existsSync(phaseIndexPath)) {
      const phaseIndex = this.mustYaml(phaseIndexPath);
      if (phaseCompleted) {
        phaseIndex.status = Status.DONE;
        phaseIndex.locked = true;
      }
      if (milestoneCompleted) {
        for (const entry of ((phaseIndex.milestones as AnyRec[]) ?? [])) {
          if (idsMatch(entry.id, milestone.id)) {
            entry.status = Status.DONE;
            break;
          }
        }
      }
      await this.writeYaml(phaseIndexPath, phaseIndex);
    }

    const msIndexPath = join(this.tasksDir, phase.path, milestone.path, "index.yaml");
    if (existsSync(msIndexPath)) {
      const milestoneIndex = this.mustYaml(msIndexPath);
      if (milestoneCompleted) {
        milestoneIndex.status = Status.DONE;
      }
      if (epicCompleted) {
        for (const entry of ((milestoneIndex.epics as AnyRec[]) ?? [])) {
          if (idsMatch(entry.id, epic.id)) {
            entry.status = Status.DONE;
            break;
          }
        }
      }
      await this.writeYaml(msIndexPath, milestoneIndex);
    }

    const epicIndexPath = join(this.tasksDir, phase.path, milestone.path, epic.path, "index.yaml");
    if (existsSync(epicIndexPath)) {
      const epicIndex = this.mustYaml(epicIndexPath);
      if (epicCompleted) {
        epicIndex.status = Status.DONE;
      }
      await this.writeYaml(epicIndexPath, epicIndex);
    }

    return result;
  }
}
