import { afterEach, describe, expect, test } from "bun:test";
import { chmodSync, existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { parse, stringify } from "yaml";
import { renderStartupLogo, stripAnsiCodes } from "../src/logo";

let oldCwd = process.cwd();
let root = "";
const packageRoot = fileURLToPath(new URL("..", import.meta.url));
const cliPath = join(packageRoot, "src", "cli.ts");

afterEach(() => {
  if (root) {
    process.chdir(oldCwd);
    rmSync(root, { recursive: true, force: true });
    root = "";
  }
});

function setupFixture(includeBug = false): string {
  return setupFixtureInDir(".tasks", includeBug);
}

function setupFixtureBacklog(includeBug = false): string {
  return setupFixtureInDir(".backlog", includeBug);
}

function setupFixtureInDir(tasksDir: string, includeBug = false): string {
  const r = mkdtempSync(join(tmpdir(), "tasks-ts-native-"));
  const t = join(r, tasksDir);
  mkdirSync(join(t, "01-phase", "01-ms", "01-epic"), { recursive: true });
  writeFileSync(
    join(t, "index.yaml"),
    `project: Native\nphases:\n  - id: P1\n    name: Phase\n    path: 01-phase\ncritical_path: []\n`,
  );
  writeFileSync(join(t, "01-phase", "index.yaml"), `milestones:\n  - id: M1\n    name: M\n    path: 01-ms\n`);
  writeFileSync(join(t, "01-phase", "01-ms", "index.yaml"), `epics:\n  - id: E1\n    name: E\n    path: 01-epic\n`);
  writeFileSync(
    join(t, "01-phase", "01-ms", "01-epic", "index.yaml"),
    `tasks:\n  - id: T001\n    file: T001-a.todo\n  - id: T002\n    file: T002-b.todo\n`,
  );
  writeFileSync(
    join(t, "01-phase", "01-ms", "01-epic", "T001-a.todo"),
    `---\nid: P1.M1.E1.T001\ntitle: A\nstatus: pending\nestimate_hours: 1\ncomplexity: low\npriority: medium\ndepends_on: []\ntags: []\n---\n# A\n`,
  );
  writeFileSync(
    join(t, "01-phase", "01-ms", "01-epic", "T002-b.todo"),
    `---\nid: P1.M1.E1.T002\ntitle: B\nstatus: pending\nestimate_hours: 2\ncomplexity: medium\npriority: high\ndepends_on:\n  - P1.M1.E1.T001\ntags: []\n---\n# B\n`,
  );

  if (includeBug) {
    mkdirSync(join(t, "bugs"), { recursive: true });
    writeFileSync(join(t, "bugs", "index.yaml"), "bugs:\n  - id: B001\n    file: B001-critical-bug.todo\n");
    writeFileSync(
      join(t, "bugs", "B001-critical-bug.todo"),
      `---\nid: B001\ntitle: Critical Bug\nstatus: pending\nestimate_hours: 5\ncomplexity: high\npriority: critical\ndepends_on: []\ntags: [bug]\n---\n# Critical Bug\n`,
    );
  }

  return r;
}

function setupHierarchyCycleFixture(): string {
  const r = mkdtempSync(join(tmpdir(), "tasks-ts-hierarchy-"));
  const t = join(r, ".tasks");
  mkdirSync(join(t, "01-phase-one", "01-ms", "01-epic"), { recursive: true });
  mkdirSync(join(t, "02-phase-two", "01-ms", "01-epic"), { recursive: true });

  writeFileSync(
    join(t, "index.yaml"),
    `project: Hierarchy Cycle\nphases:\n  - id: P1\n    name: Phase One\n    path: 01-phase-one\n    depends_on:\n      - P2\n  - id: P2\n    name: Phase Two\n    path: 02-phase-two\n    depends_on:\n      - P1\ncritical_path: []\n`,
  );

  for (const [phaseId, phasePath, phaseName] of [
    ["P1", "01-phase-one", "Phase One"],
    ["P2", "02-phase-two", "Phase Two"],
  ] as const) {
    writeFileSync(
      join(t, phasePath, "index.yaml"),
      `milestones:\n  - id: M1\n    name: ${phaseName} Milestone\n    path: 01-ms\n`,
    );
    writeFileSync(
      join(t, phasePath, "01-ms", "index.yaml"),
      `epics:\n  - id: E1\n    name: ${phaseName} Epic\n    path: 01-epic\n`,
    );
    writeFileSync(
      join(t, phasePath, "01-ms", "01-epic", "index.yaml"),
      `tasks:\n  - id: T001\n    file: T001-a.todo\n`,
    );
    writeFileSync(
      join(t, phasePath, "01-ms", "01-epic", "T001-a.todo"),
      `---\nid: ${phaseId}.M1.E1.T001\ntitle: ${phaseName} Task\nstatus: pending\nestimate_hours: 1\ncomplexity: medium\npriority: high\ndepends_on: []\ntags: []\n---\n# ${phaseName} Task\n`,
    );
  }

  return r;
}

function run(args: string[], cwd: string, extraEnv: NodeJS.ProcessEnv = {}) {
  return Bun.spawnSync(["bun", "run", cliPath, ...args], {
    cwd,
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env, ...extraEnv },
  });
}

function runWithColor(args: string[], cwd: string) {
  return run(args, cwd, { FORCE_COLOR: "1", TERM: "xterm-256color" });
}

function updateTodoFrontmatter(taskPath: string, mutate: (frontmatter: Record<string, any>) => void): void {
  const content = readFileSync(taskPath, "utf8");
  const match = content.match(/^---\n([\s\S]*?)\n---\n/);
  if (!match) {
    throw new Error(`Missing frontmatter in ${taskPath}`);
  }

  const frontmatter = parse(match[1] as string) as Record<string, any>;
  mutate(frontmatter);

  const body = content.slice(match[0].length);
  writeFileSync(taskPath, `---\n${stringify(frontmatter)}---\n${body}`);
}

function runGit(cwd: string, ...args: string[]): string {
  const result = Bun.spawnSync(["git", ...args], {
    cwd,
    stdout: "pipe",
    stderr: "pipe",
  });
  if (result.exitCode !== 0) {
    throw new Error(result.stderr.toString() || result.stdout.toString() || `git ${args.join(" ")} failed`);
  }
  return result.stdout.toString().trim();
}

function initializeTestGitRepo(cwd: string): void {
  runGit(cwd, "init", "-q");
  runGit(cwd, "config", "user.name", "backlog-tests");
  runGit(cwd, "config", "user.email", "backlog-tests@example.com");
  runGit(cwd, "add", ".tasks");
  runGit(cwd, "commit", "-m", "baseline");
}

function gitCommitCount(cwd: string): number {
  const count = Number.parseInt(runGit(cwd, "rev-list", "--count", "HEAD"), 10);
  if (Number.isNaN(count)) {
    throw new Error("git rev-list --count HEAD did not return a number");
  }
  return count;
}

describe("native cli", () => {
  test("no args renders usage help", () => {
    root = setupFixture();
    const p = run([], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Usage: backlog <command> [options]");
  });

  test("renderStartupLogo is deterministic and supports no-color output", () => {
    const first = renderStartupLogo(80, 3, true);
    const second = renderStartupLogo(80, 3, true);
    expect(first).toEqual(second);
    expect(first.some((line) => line.includes("\x1b["))).toBeTrue();

    const plain = renderStartupLogo(80, 3, false);
    expect(plain).toEqual(renderStartupLogo(80, 3, false));
    for (const line of plain) {
      expect(stripAnsiCodes(line)).toBe(line);
    }
  });

  test("list/next/show json and text", () => {
    root = setupFixture();
    const list = run(["list", "--json"], root);
    expect(list.exitCode).toBe(0);
    const payload = JSON.parse(list.stdout.toString());
    expect(Array.isArray(payload.tasks)).toBeTrue();

    const next = run(["next", "--json"], root);
    expect(next.exitCode).toBe(0);
    expect(JSON.parse(next.stdout.toString()).id).toBe("P1.M1.E1.T001");

    const show = run(["show", "P1.M1.E1.T001"], root);
    expect(show.exitCode).toBe(0);
    expect(show.stdout.toString()).toContain("P1.M1.E1.T001");
  });

  test("next can return bugs and list marks bug critical path", () => {
    root = setupFixture(true);

    const next = run(["next", "--json"], root);
    expect(next.exitCode).toBe(0);
    expect(JSON.parse(next.stdout.toString()).id).toBe("B001");

    const list = run(["list"], root);
    expect(list.exitCode).toBe(0);
    expect(list.stdout.toString()).toContain("★ B001: Critical Bug");
  });

  test("preview returns categorized items and grab suggestions", () => {
    root = setupFixture();

    const add1 = run([
      "add",
      "P1.M1.E1",
      "--title",
      "Third Normal Task",
    ], root);
    expect(add1.exitCode).toBe(0);
    const add2 = run([
      "add",
      "P1.M1.E1",
      "--title",
      "Fourth Normal Task",
    ], root);
    expect(add2.exitCode).toBe(0);

    for (let i = 1; i <= 6; i++) {
      const bug = run(["bug", `Preview bug ${i}`], root);
      expect(bug.exitCode).toBe(0);
    }

    for (let i = 1; i <= 7; i++) {
      const idea = run(["idea", `preview idea ${i}`], root);
      expect(idea.exitCode).toBe(0);
    }

    const preview = run(["preview", "--json"], root);
    expect(preview.exitCode).toBe(0);
    const payload = JSON.parse(preview.stdout.toString());

    expect(payload.next_available).toBe("B001");
    expect(payload.normal.length).toBe(1);
    expect(payload.bugs.length).toBe(5);
    expect(payload.ideas.length).toBe(5);

    const normalById = Object.fromEntries(payload.normal.map((row: { id: string }) => [row.id, row]));
    expect(normalById["P1.M1.E1.T001"].grab_additional).toEqual([
      "P1.M1.E1.T002",
      "P1.M1.E1.T003",
      "P1.M1.E1.T004",
    ]);
  }, 12000);

  test("list --available includes bugs and ideas", () => {
    root = setupFixture(true);

    const idea = run(["idea", "capture planning intake"], root);
    expect(idea.exitCode).toBe(0);

    const list = run(["list", "--available"], root);
    expect(list.exitCode).toBe(0);
    const output = list.stdout.toString();
    expect(output).toContain("B001: Critical Bug");
    expect(output).toContain("I001: capture planning intake");
    expect(output).toContain(
      "Use `bl claim TASK_ID` to claim a specific task, or `backlog grab` to auto-claim the best available task.",
    );
  });

  test("list hides completed bugs by default and can include via flag", () => {
    root = setupFixture(true);
    const bugPath = join(root, ".tasks", "bugs", "B001-critical-bug.todo");
    let bugText = readFileSync(bugPath, "utf8");
    bugText = bugText.replace("status: pending", "status: done");
    writeFileSync(bugPath, bugText);

    let p = run(["list"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).not.toContain("B001: Critical Bug");

    p = run(["list", "--show-completed-aux"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("B001: Critical Bug");
  });

  test("list --all --bugs includes completed bugs", () => {
    root = setupFixture(true);
    const bugPath = join(root, ".tasks", "bugs", "B001-critical-bug.todo");
    let bugText = readFileSync(bugPath, "utf8");
    bugText = bugText.replace("status: pending", "status: done");
    writeFileSync(bugPath, bugText);

    const p = run(["list", "--all", "--bugs"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("B001: Critical Bug");
  });

  test("list --all --ideas includes completed ideas", () => {
    root = setupFixture();

    const idea = run(["idea", "capture planning backlog"], root);
    expect(idea.exitCode).toBe(0);

    const ideasIndex = parse(
      readFileSync(join(root, ".tasks", "ideas", "index.yaml"), "utf8",
    )) as { ideas: { id: string; file: string }[] };
    const ideaFile = ideasIndex.ideas[0]?.file;
    expect(ideaFile).toBeTruthy();
    const ideaPath = join(root, ".tasks", "ideas", ideaFile!);
    updateTodoFrontmatter(ideaPath, (frontmatter) => {
      frontmatter.status = "done";
    });

    const p = run(["list", "--all", "--ideas"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("I001: capture planning backlog");
  });

  test("list --json hides completed bugs by default", () => {
    root = setupFixture(true);
    const bugPath = join(root, ".tasks", "bugs", "B001-critical-bug.todo");
    let bugText = readFileSync(bugPath, "utf8");
    bugText = bugText.replace("status: pending", "status: done");
    writeFileSync(bugPath, bugText);

    const p = run(["list", "--json"], root);
    expect(p.exitCode).toBe(0);
    const payload = JSON.parse(p.stdout.toString());
    expect(Array.isArray(payload.bugs)).toBeTrue();
    expect(payload.bugs.length).toBe(0);
  });

  test("list positional scope shows deeper tree and hides bugs/ideas", () => {
    root = setupFixture(true);

    const p = run(["list", "P1.M1", "--all"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("E");
    expect(out).toContain("P1.M1.E1.T001");
    expect(out).toContain("P1.M1.E1.T002");
    expect(out).not.toContain("Bugs");
    expect(out).not.toContain("Ideas");
    expect(out).not.toContain("B001");
  });

  test("list positional scope supports json without bugs/ideas", () => {
    root = setupFixture(true);

    const p = run(["list", "P1.M1", "--json"], root);
    expect(p.exitCode).toBe(0);
    const payload = JSON.parse(p.stdout.toString());
    expect(Array.isArray(payload.phases)).toBeTrue();
    expect(payload.phases.length).toBeGreaterThan(0);
    expect(payload.bugs.length).toBe(0);
    expect(payload.ideas.length).toBe(0);
    const taskIds = payload.tasks.map((task: any) => task.id);
    expect(taskIds).toContain("P1.M1.E1.T001");
    expect(taskIds).toContain("P1.M1.E1.T002");
  });

  test("list positional scope with no match reports path query", () => {
    root = setupFixture();

    const p = run(["list", "P9"], root);
    expect(p.exitCode).toBe(1);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("No list nodes found for path query: P9");
  });

  test("list accepts multiple positional scopes", () => {
    root = setupFixture();

    const p = run(["list", "P1", "P1.M1"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("P1.M1.E1.T001");
    expect(out).toContain("P1.M1.E1.T002");
  });

  test("list fails on explicit dependency cycle", () => {
    root = setupFixture();
    const task1 = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const task2 = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");
    updateTodoFrontmatter(task1, (frontmatter) => {
      frontmatter.depends_on = ["P1.M1.E1.T002"];
    });
    updateTodoFrontmatter(task2, (frontmatter) => {
      frontmatter.depends_on = ["P1.M1.E1.T001"];
    });

    const p = run(["list"], root);
    expect(p.exitCode).toBe(1);
    const normalized = `${p.stdout.toString()} ${p.stderr.toString()}`.replace(/\s+/g, " ");
    expect(normalized).toContain("task dependency cycle detected");
    expect(normalized).toContain("P1.M1.E1.T001 -> P1.M1.E1.T002 -> P1.M1.E1.T001");
  });

  test("list warns but succeeds on hierarchy cycle", () => {
    root = setupHierarchyCycleFixture();

    const p = run(["list"], root);
    expect(p.exitCode).toBe(0);
    const out = `${p.stdout.toString()} ${p.stderr.toString()}`;
    expect(out).toContain("Warning:");
    expect(out).toContain("non-task dependency cycle detected");
    expect(out).toContain("P1.M1.E1.T001");
    expect(out).toContain("P2.M1.E1.T001");
  });

  test("claim done update sync mutate files", () => {
    root = setupFixture();
    let p = run(["claim", "P1.M1.E1.T001", "--agent", "agent-z"], root);
    expect(p.exitCode).toBe(0);

    const todoPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    expect(readFileSync(todoPath, "utf8")).toContain("status: in_progress");

    p = run(["done", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(readFileSync(todoPath, "utf8")).toContain("status: done");

    p = run(["update", "P1.M1.E1.T002", "blocked", "--reason", "wait"], root);
    expect(p.exitCode).toBe(0);

    p = run(["sync"], root);
    expect(p.exitCode).toBe(0);
    const rootIndex = readFileSync(join(root, ".tasks", "index.yaml"), "utf8");
    expect(rootIndex).toContain("critical_path:");
    expect(rootIndex).toContain("next_available:");
    const rootParsed = parse(rootIndex) as Record<string, any>;
    expect(rootParsed.stats).toBeDefined();
    expect(rootParsed.stats?.total_tasks).toBe(2);
    expect(rootParsed.stats?.done).toBe(1);
    expect(rootParsed.stats?.in_progress).toBe(0);
    expect(rootParsed.stats?.blocked).toBe(1);

    const phaseIndex = parse(
      readFileSync(join(root, ".tasks", "01-phase", "index.yaml"), "utf8"),
    ) as Record<string, any>;
    expect(phaseIndex.stats).toBeDefined();
    expect(phaseIndex.stats?.total_tasks).toBe(2);
    expect(phaseIndex.stats?.done).toBe(1);
    expect(phaseIndex.stats?.pending).toBe(0);
    expect(phaseIndex.stats?.blocked).toBe(1);

    const milestoneIndex = parse(
      readFileSync(join(root, ".tasks", "01-phase", "01-ms", "index.yaml"), "utf8"),
    ) as Record<string, any>;
    expect(milestoneIndex.stats).toBeDefined();
    expect(milestoneIndex.stats?.total_tasks).toBe(2);
    expect(milestoneIndex.stats?.done).toBe(1);
    expect(milestoneIndex.stats?.pending).toBe(0);
    expect(milestoneIndex.stats?.blocked).toBe(1);

    const epicIndex = parse(
      readFileSync(
        join(root, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml"),
        "utf8",
      ),
    ) as Record<string, any>;
    expect(epicIndex.stats).toBeDefined();
    expect(epicIndex.stats?.total).toBe(2);
    expect(epicIndex.stats?.done).toBe(1);
    expect(epicIndex.stats?.in_progress).toBe(0);
    expect(epicIndex.stats?.blocked).toBe(1);
    expect(epicIndex.stats?.pending).toBe(0);
  });

  test("sync fails on explicit dependency cycle", () => {
    root = setupFixture();
    const task1 = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const task2 = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");
    updateTodoFrontmatter(task1, (frontmatter) => {
      frontmatter.depends_on = ["P1.M1.E1.T002"];
    });
    updateTodoFrontmatter(task2, (frontmatter) => {
      frontmatter.depends_on = ["P1.M1.E1.T001"];
    });

    const p = run(["sync"], root);
    expect(p.exitCode).toBe(1);
    const normalized = `${p.stdout.toString()} ${p.stderr.toString()}`.replace(/\s+/g, " ");
    expect(normalized).toContain("task dependency cycle detected");
    expect(normalized).toContain("P1.M1.E1.T001 -> P1.M1.E1.T002 -> P1.M1.E1.T001");
  });

  test("sync warns but succeeds on hierarchy cycle", () => {
    root = setupHierarchyCycleFixture();

    const p = run(["sync"], root);
    expect(p.exitCode).toBe(0);
    const out = `${p.stdout.toString()} ${p.stderr.toString()}`;
    expect(out).toContain("Warning:");
    expect(out).toContain("non-task dependency cycle detected");
    expect(out).toContain("P1.M1.E1.T001");
    expect(out).toContain("P2.M1.E1.T001");
  });

  test("claim accepts multiple task ids", () => {
    root = setupFixture();
    const p = run([
      "claim",
      "P1.M1.E1.T001",
      "P1.M1.E1.T002",
      "--agent",
      "agent-z",
    ], root);

    expect(p.exitCode).toBe(0);
    const output = p.stdout.toString();
    expect(output).toContain("Claimed: P1.M1.E1.T001");
    expect(output).toContain("✓ Claimed: P1.M1.E1.T002 - B");

    const taskOne = readFileSync(
      join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"),
      "utf8",
    );
    const taskTwo = readFileSync(
      join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo"),
      "utf8",
    );
    expect(taskOne).toContain("status: in_progress");
    expect(taskTwo).toContain("status: in_progress");
  });

  test("claiming scoped ids falls back to show output", () => {
    root = setupFixture();
    const p = run(["claim", "P1.M1", "--agent", "agent-z"], root);

    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Warning: claim only works with task IDs.");
    expect(out).toContain("Showing `backlog show P1.M1` for context.");
    expect(out).toContain("P1.M1");
  });

  test("claim can claim bug ids", () => {
    root = setupFixture(true);

    const p = run(["claim", "B001", "--agent", "agent-z"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Claimed: B001");

    const bugPath = join(root, ".tasks", "bugs", "B001-critical-bug.todo");
    expect(readFileSync(bugPath, "utf8")).toContain("status: in_progress");
  });

  test("claim can claim idea ids", () => {
    root = setupFixture();
    const idea = run(["idea", "capture planning backlog"], root);
    expect(idea.exitCode).toBe(0);

    const ideasIndex = parse(readFileSync(join(root, ".tasks", "ideas", "index.yaml"), "utf8")) as {
      ideas: { id: string; file: string }[];
    };
    const ideaId = ideasIndex.ideas[0]!.id;
    const ideaFile = join(root, ".tasks", "ideas", ideasIndex.ideas[0]!.file);

    const p = run(["claim", ideaId, "--agent", "agent-z"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain(`Claimed: ${ideaId}`);
    expect(readFileSync(ideaFile, "utf8")).toContain("status: in_progress");
  });

  test("edit --help renders command-specific guidance", () => {
    root = setupFixture();

    const p = run(["edit", "--help"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Command Help: backlog edit");
    expect(out).toContain("Usage: backlog edit <TASK_ID>");
  });

  test("edit requires exactly one task id", () => {
    root = setupFixture();

    let p = run(["edit"], root);
    expect(p.exitCode).not.toBe(0);
    expect(p.stdout.toString() + p.stderr.toString()).toContain("edit requires exactly one TASK_ID");

    p = run(["edit", "P1.M1.E1.T001", "P1.M1.E1.T002"], root);
    expect(p.exitCode).not.toBe(0);
    expect(p.stdout.toString() + p.stderr.toString()).toContain("edit requires exactly one TASK_ID");
  });

  test("edit falls back to show for scoped IDs", () => {
    root = setupFixture();

    const p = run(["edit", "P1"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Warning: edit only works with task IDs.");
    expect(out).toContain("Showing `backlog show P1` for context.");
    expect(out).toContain("P1");
  });

  test("edit fails when task file is missing", () => {
    root = setupFixture();
    const todoPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    rmSync(todoPath);

    const p = run(["edit", "P1.M1.E1.T001"], root);
    expect(p.exitCode).not.toBe(0);
    expect(p.stdout.toString() + p.stderr.toString()).toContain("Cannot edit P1.M1.E1.T001 because the task file is missing.");
  });

  test("edit needs editor configured", () => {
    root = setupFixture();

    const p = run(["edit", "P1.M1.E1.T001"], root, { EDITOR: "", VISUAL: "" });
    expect(p.exitCode).not.toBe(0);
    expect(p.stdout.toString() + p.stderr.toString()).toContain("No editor configured. Set EDITOR or VISUAL.");
  });

  test("edit launches configured editor", () => {
    root = setupFixture();
    const editor = join(root, "editor.sh");
    writeFileSync(editor, "#!/bin/sh\necho \"$1\" > editor-called.txt\n");
    chmodSync(editor, 0o755);

    const p = run(["edit", "P1.M1.E1.T001"], root, { VISUAL: editor });
    expect(p.exitCode).toBe(0);
    const called = readFileSync(join(root, "editor-called.txt"), "utf8");
    expect(called).toContain("T001-a.todo");
  });

  test("edit auto-commits updated task when no staged files exist", () => {
    root = setupFixture();
    const editor = join(root, "editor.sh");
    writeFileSync(editor, "#!/bin/sh\necho \"edited\" >> \"$1\"\n");
    chmodSync(editor, 0o755);

    initializeTestGitRepo(root);
    const initialCommitCount = gitCommitCount(root);

    const p = run(["edit", "P1.M1.E1.T001"], root, { VISUAL: editor });
    expect(p.exitCode).toBe(0);
    expect(gitCommitCount(root)).toBe(initialCommitCount + 1);
    expect(runGit(root, "log", "-1", "--pretty=%B")).toBe("bl edit P1.M1.E1.T001: A");
  });

  test("edit auto-commit skips when staged files already exist", () => {
    root = setupFixture();
    const editor = join(root, "editor.sh");
    writeFileSync(editor, "#!/bin/sh\necho \"edited\" >> \"$1\"\n");
    chmodSync(editor, 0o755);

    initializeTestGitRepo(root);
    writeFileSync(join(root, "staged-change.txt"), "staged\n");
    runGit(root, "add", "staged-change.txt");

    const initialCommitCount = gitCommitCount(root);
    const p = run(["edit", "P1.M1.E1.T001"], root, { VISUAL: editor });
    expect(p.exitCode).toBe(0);
    expect(gitCommitCount(root)).toBe(initialCommitCount);
    expect(runGit(root, "status", "--short")).toContain("A  staged-change.txt");
  });

  test("done refuses to complete a non-in-progress task", () => {
    root = setupFixture();
    const todoPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    let todo = readFileSync(todoPath, "utf8");
    todo = todo.replace("status: pending", "status: blocked");
    writeFileSync(todoPath, todo);

    const p = run(["done", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(1);
    expect(p.stdout.toString()).toContain("INVALID_STATUS");
    expect(readFileSync(todoPath, "utf8")).toContain("status: blocked");
  });

  test("done --force marks non-in-progress task as done", () => {
    root = setupFixture();
    const todoPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    let todo = readFileSync(todoPath, "utf8");
    todo = todo.replace("status: pending", "status: blocked");
    writeFileSync(todoPath, todo);

    const p = run(["done", "--force", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Completed:");
    expect(readFileSync(todoPath, "utf8")).toContain("status: done");
  });

  test("done supports multiple task ids", () => {
    root = setupFixture(true);

    updateTodoFrontmatter(
      join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"),
      (frontmatter) => {
        frontmatter.status = "in_progress";
      },
    );
    updateTodoFrontmatter(
      join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo"),
      (frontmatter) => {
        frontmatter.status = "in_progress";
      },
    );
    updateTodoFrontmatter(
      join(root, ".tasks", "bugs", "B001-critical-bug.todo"),
      (frontmatter) => {
        frontmatter.status = "in_progress";
      },
    );

    const p = run(
      ["done", "P1.M1.E1.T001", "P1.M1.E1.T002", "B001"],
      root,
    );

    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Completed:");
    expect(p.stdout.toString()).toContain("P1.M1.E1.T001");
    expect(p.stdout.toString()).toContain("P1.M1.E1.T002");

    expect(readFileSync(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"), "utf8")).toContain("status: done");
    expect(readFileSync(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo"), "utf8")).toContain("status: done");
    expect(readFileSync(join(root, ".tasks", "bugs", "B001-critical-bug.todo"), "utf8")).toContain("status: done");
  });

  test("done marks completed hierarchy levels and locks phase", () => {
    root = setupFixture();

    updateTodoFrontmatter(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"), (frontmatter) => {
      frontmatter.status = "done";
    });
    updateTodoFrontmatter(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo"), (frontmatter) => {
      frontmatter.status = "in_progress";
      frontmatter.claimed_by = "agent-c";
    });

    const p = run(["done", "P1.M1.E1.T002"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Completed: P1.M1.E1.T002");
    expect(p.stdout.toString()).toContain("EPIC COMPLETE");
    expect(p.stdout.toString()).toContain("PHASE COMPLETE");

    const rootIndex = parse(readFileSync(join(root, ".tasks", "index.yaml"), "utf8")) as {
      phases: Array<{ id: string; status: string; locked?: boolean }>;
    };
    const phaseEntry = rootIndex.phases.find((p) => p.id === "P1");
    expect(phaseEntry?.status).toBe("done");
    expect(phaseEntry?.locked).toBeTrue();

    const phaseIndex = parse(readFileSync(join(root, ".tasks", "01-phase", "index.yaml"), "utf8")) as {
      status?: string;
      locked?: boolean;
    };
    expect(phaseIndex.status).toBe("done");
    expect(phaseIndex.locked).toBeTrue();

    const milestoneIndex = parse(readFileSync(join(root, ".tasks", "01-phase", "01-ms", "index.yaml"), "utf8")) as {
      status?: string;
      epics: Array<{ id: string; status?: string }>;
    };
    expect(milestoneIndex.status).toBe("done");
    const epicEntry = milestoneIndex.epics.find((e) => e.id === "E1");
    expect(epicEntry?.status).toBe("done");

    const addErr = run(["add", "P1.M1.E1", "--title", "Blocked"], root);
    const addOutput = `${addErr.stdout.toString()}${addErr.stderr.toString()}`;
    expect(addErr.exitCode).not.toBe(0);
    expect(addOutput).toContain("has been closed and cannot accept new tasks");

    const unlock = run(["unlock", "P1"], root);
    expect(unlock.exitCode).toBe(0);
    expect(unlock.stdout.toString()).toContain("Unlocked: P1");

    const allowedAdd = run(["add", "P1.M1.E1", "--title", "Allowed"], root);
    expect(allowedAdd.exitCode).toBe(0);
    expect(allowedAdd.stdout.toString()).toContain("Created task:");
  });

  test("done shows milestone complete for multi-epic milestone", () => {
    root = setupFixture();

    const milestoneIndex = join(root, ".tasks", "01-phase", "01-ms", "index.yaml");
    const milestoneData = parse(readFileSync(milestoneIndex, "utf8")) as {
      epics: Array<{ id: string; name: string; path: string; status?: string }>;
    };
    milestoneData.epics.push({
      id: "E2",
      name: "Epic 2",
      path: "02-epic",
      status: "in_progress",
    });
    writeFileSync(milestoneIndex, `${stringify(milestoneData)}\n`);

    const secondEpicDir = join(root, ".tasks", "01-phase", "01-ms", "02-epic");
    mkdirSync(secondEpicDir, { recursive: true });
    const secondEpicTodo = join(secondEpicDir, "T001-c.todo");
    writeFileSync(
      join(secondEpicDir, "index.yaml"),
      `tasks:\n  - id: T001\n    file: T001-c.todo\n    status: pending\n    estimate_hours: 1\n    complexity: low\n    priority: medium\n`,
    );
    writeFileSync(
      secondEpicTodo,
      `---\nid: P1.M1.E2.T001\ntitle: C\nstatus: pending\nestimate_hours: 1\ncomplexity: low\npriority: medium\ndepends_on: []\ntags: []\n---\n# C\n`,
    );

    updateTodoFrontmatter(
      join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"),
      (frontmatter) => {
        frontmatter.status = "done";
      },
    );
    updateTodoFrontmatter(
      join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo"),
      (frontmatter) => {
        frontmatter.status = "done";
      },
    );
    updateTodoFrontmatter(secondEpicTodo, (frontmatter) => {
      frontmatter.status = "in_progress";
      frontmatter.claimed_by = "agent-c";
    });

    const p = run(["done", "P1.M1.E2.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Completed: P1.M1.E2.T001");
    expect(p.stdout.toString()).toContain("EPIC COMPLETE");
    expect(p.stdout.toString()).toContain("MILESTONE COMPLETE");
  });

  test("undone resets single task to pending", () => {
    root = setupFixture();
    const todoPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    let todo = readFileSync(todoPath, "utf8");
    todo = todo.replace("status: pending", "status: done");
    writeFileSync(todoPath, todo);

    const p = run(["undone", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(readFileSync(todoPath, "utf8")).toContain("status: pending");
  });

  test("undone resets phase hierarchy and descendant tasks", () => {
    root = setupFixture();
    const t001Path = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const t002Path = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");
    writeFileSync(t001Path, readFileSync(t001Path, "utf8").replace("status: pending", "status: done"));
    writeFileSync(t002Path, readFileSync(t002Path, "utf8").replace("status: pending", "status: done"));

    const rootIndexPath = join(root, ".tasks", "index.yaml");
    writeFileSync(
      rootIndexPath,
      readFileSync(rootIndexPath, "utf8").replace("path: 01-phase\n", "path: 01-phase\n    status: done\n"),
    );
    const phaseIndexPath = join(root, ".tasks", "01-phase", "index.yaml");
    writeFileSync(
      phaseIndexPath,
      readFileSync(phaseIndexPath, "utf8").replace("path: 01-ms\n", "path: 01-ms\n    status: done\n"),
    );
    const msIndexPath = join(root, ".tasks", "01-phase", "01-ms", "index.yaml");
    writeFileSync(
      msIndexPath,
      readFileSync(msIndexPath, "utf8").replace("path: 01-epic\n", "path: 01-epic\n    status: done\n"),
    );

    const p = run(["undone", "P1"], root);
    expect(p.exitCode).toBe(0);
    expect(readFileSync(rootIndexPath, "utf8")).toContain("status: pending");
    expect(readFileSync(phaseIndexPath, "utf8")).toContain("status: pending");
    expect(readFileSync(msIndexPath, "utf8")).toContain("status: pending");
    expect(readFileSync(t001Path, "utf8")).toContain("status: pending");
    expect(readFileSync(t002Path, "utf8")).toContain("status: pending");
  });

  test("set updates task properties", () => {
    root = setupFixture();
    const p = run([
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
      "# Updated body content\nline 2",
    ], root);
    expect(p.exitCode).toBe(0);

    const todo = readFileSync(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"), "utf8");
    expect(todo).toContain("title: Updated Task");
    expect(todo).toContain("priority: critical");
    expect(todo).toContain("complexity: high");
    expect(todo).toContain("estimate_hours: 3.5");
    expect(todo).toContain("- B060");
    expect(todo).toContain("- P1.M1.E1.T002");
    expect(todo).toContain("- bugfix");
    expect(todo).toContain("- urgent");
    expect(todo).toContain("Updated body content");
    expect(todo).not.toContain("## Requirements");
  });

  test("set appends body with --append-body", () => {
    root = setupFixture();
    const first = run(["set", "P1.M1.E1.T001", "--body", "first body"], root);
    expect(first.exitCode).toBe(0);
    expect(first.stdout.toString()).toContain("Updated: P1.M1.E1.T001");

    const second = run([
      "set",
      "P1.M1.E1.T001",
      "--body",
      "appended body",
      "--append-body",
    ], root);
    expect(second.exitCode).toBe(0);
    expect(second.stdout.toString()).toContain("Updated: P1.M1.E1.T001");

    const todo = readFileSync(
      join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"),
      "utf8",
    );
    expect(todo).toContain("first body");
    expect(todo).toContain("appended body");
    expect(todo.indexOf("appended body")).toBeGreaterThan(todo.indexOf("first body"));
  });

  test("set rejects invalid dependency id", () => {
    root = setupFixture();
    const p = run(["set", "P1.M1.E1.T001", "--depends-on", "not a task id"], root);
    expect(p.exitCode).toBe(1);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("invalid dependency id");
  });

  test("set requires at least one field", () => {
    root = setupFixture();
    const p = run(["set", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(1);
    expect(p.stderr.toString()).toContain("set requires at least one property flag");
  });

  test("work set/show/clear", () => {
    root = setupFixture();
    let p = run(["work"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("No current working task set.");

    p = run(["work", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Working task set");

    p = run(["work"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Current Working Task");
    expect(p.stdout.toString()).toContain("P1.M1.E1.T001");

    p = run(["work", "--clear"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Cleared working task context.");

    p = run(["work", "P1.M1.E1.T001", "P1.M1.E1.T002"], root);
    expect(p.exitCode).toBe(1);
    const tooManyWork = `${p.stdout.toString()}${p.stderr.toString()}`;
    expect(tooManyWork).toContain("work accepts at most one TASK_ID");

    p = run(["work", "P1.M1.E1.T001", "--clear"], root);
    expect(p.exitCode).toBe(1);
    const clearWithTask = `${p.stdout.toString()}${p.stderr.toString()}`;
    expect(clearWithTask).toContain("work --clear does not accept a TASK_ID");
  });

  test("version command prints and rejects positional arguments", () => {
    root = setupFixture();
    let p = run(["version"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("backlog version 0.1.0");

    p = run(["version", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(1);
    const versionWithArg = `${p.stdout.toString()}${p.stderr.toString()}`;
    expect(versionWithArg).toContain("Command Help: backlog version");
    expect(versionWithArg).toContain("version accepts no TASK_ID arguments");
  });

  test("blocked without --grab keeps task pending", () => {
    root = setupFixture();
    let p = run(["claim", "P1.M1.E1.T001", "--agent", "agent-z"], root);
    expect(p.exitCode).toBe(0);

    p = run(["blocked", "P1.M1.E1.T001", "--reason", "waiting"], root);
    expect(p.exitCode).toBe(0);
    let todo = readFileSync(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"), "utf8");
    expect(todo).toContain("status: blocked");
    expect(todo).toContain("reason: waiting");
    expect(p.stdout.toString()).toContain("Tip: Run `backlog grab` to claim the next available task.");
    expect(p.stdout.toString()).not.toContain("Grabbed:");

    p = run(["update", "P1.M1.E1.T001", "pending", "--reason", "retry"], root);
    expect(p.exitCode).toBe(0);
    p = run(["claim", "P1.M1.E1.T001", "--agent", "agent-z"], root);
    expect(p.exitCode).toBe(0);
    p = run(["unclaim", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    todo = readFileSync(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"), "utf8");
    expect(todo).toContain("status: pending");
  });

  test("unclaim recovers pending claimed tasks", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    let todo = readFileSync(taskPath, "utf8");
    todo = todo.replace("status: pending", "status: pending\nclaimed_by: agent-z\nclaimed_at: 2026-01-01T00:00:00.000Z");
    writeFileSync(taskPath, todo);

    const p = run(["unclaim", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    todo = readFileSync(taskPath, "utf8");
    expect(todo).toContain("status: pending");
    expect(todo).not.toContain("claimed_by: agent-z");
  });

  test("blocked with --grab auto-grabs next task", () => {
    root = setupFixture();
    let p = run(["claim", "P1.M1.E1.T001", "--agent", "agent-b"], root);
    expect(p.exitCode).toBe(0);
    p = run(["blocked", "P1.M1.E1.T001", "--reason", "waiting", "--grab"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Blocked: P1.M1.E1.T001");
    expect(p.stdout.toString()).toContain("No available tasks found.");
    const todo1 = readFileSync(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"), "utf8");
    const todo2 = readFileSync(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo"), "utf8");
    expect(todo1).toContain("status: blocked");
    expect(todo1).toContain("reason: waiting");
    expect(todo2).toContain("status: pending");
  });

  test("grab and cycle workflow", () => {
    root = setupFixture();
    let p = run(["grab", "--agent", "agent-c"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Grabbed:");
    let todo1 = readFileSync(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"), "utf8");
    expect(todo1).toContain("status: in_progress");

    p = run(["cycle", "P1.M1.E1.T001", "--agent", "agent-c"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Completed: P1.M1.E1.T001");
    expect(p.stdout.toString()).toContain("Grabbed: P1.M1.E1.T002");

    todo1 = readFileSync(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"), "utf8");
    const todo2 = readFileSync(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo"), "utf8");
    expect(todo1).toContain("status: done");
    expect(todo2).toContain("status: in_progress");
  });

  test("cycle stops auto-grab when phase is completed", () => {
    root = setupFixture();
    updateTodoFrontmatter(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"), (frontmatter) => {
      frontmatter.status = "done";
    });
    updateTodoFrontmatter(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo"), (frontmatter) => {
      frontmatter.status = "in_progress";
      frontmatter.claimed_by = "agent-c";
    });

    const p = run(["cycle", "P1.M1.E1.T002", "--agent", "agent-c"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Completed: P1.M1.E1.T002");
    expect(p.stdout.toString()).toContain("Review Required");
    expect(p.stdout.toString()).toContain("PHASE COMPLETE");
    expect(p.stdout.toString()).not.toContain("Grabbed:");
  });

  test("cycle auto-selected bug fans out to additional bugs", () => {
    root = setupFixture();
    let p = run(["bug", "--title", "bug one", "--simple"], root);
    expect(p.exitCode).toBe(0);
    p = run(["bug", "--title", "bug two", "--simple"], root);
    expect(p.exitCode).toBe(0);
    p = run(["bug", "--title", "bug three", "--simple"], root);
    expect(p.exitCode).toBe(0);

    p = run(["grab", "B001", "--agent", "agent-cycle", "--no-content"], root);
    expect(p.exitCode).toBe(0);
    p = run(["cycle", "B001", "--agent", "agent-cycle", "--no-content"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Completed: B001");
    expect(out).toContain("B002");
    expect(out).toContain("B003");
  });

  test("session lifecycle", () => {
    root = setupFixture();
    let p = run(["session", "start", "--agent", "agent-s", "--task", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Session started");

    p = run(["session", "heartbeat", "--agent", "agent-s", "--progress", "testing"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Heartbeat updated");

    p = run(["session", "list"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("agent-s");

    p = run(["session", "end", "--agent", "agent-s"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Session ended");

    p = run(["session", "list"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("No active sessions");
  });

  test("check command json and strict behavior", () => {
    root = setupFixture();
    let p = run(["check", "--json"], root);
    expect(p.exitCode).toBe(0);
    const report = JSON.parse(p.stdout.toString());
    expect(report.ok).toBeTrue();
    expect(report.summary.errors).toBe(0);

    // Create stale context warning and ensure strict exits non-zero.
    writeFileSync(join(root, ".tasks", ".context.yaml"), "current_task: P9.M9.E9.T999\n");
    p = run(["check", "--strict"], root);
    expect(p.exitCode).toBe(1);
    expect(p.stdout.toString()).toContain("warning");

    // Zero estimates should warn and fail in strict mode.
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    writeFileSync(taskPath, readFileSync(taskPath, "utf8").replace("estimate_hours: 1", "estimate_hours: 0"));
    p = run(["check"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("zero_estimate_hours");
    p = run(["check", "--strict"], root);
    expect(p.exitCode).toBe(1);
  });

  test("check fails for pending task with claim metadata", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const todo = readFileSync(taskPath, "utf8").replace(
      "status: pending",
      "status: pending\nclaimed_by: agent-z\nclaimed_at: 2026-01-01T00:00:00.000Z",
    );
    writeFileSync(taskPath, todo);

    const p = run(["check"], root);
    expect(p.exitCode).toBe(1);
    expect(p.stdout.toString()).toContain("pending_task_with_claim");
  });

  test("check command works with .backlog directory", () => {
    root = setupFixtureBacklog();
    const p = run(["check"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Consistency check passed");
  });

  test("check accepts task dependencies on existing epic", () => {
    root = setupFixture();
    const milestoneIndex = join(root, ".tasks", "01-phase", "01-ms", "index.yaml");
    const milestoneData = parse(readFileSync(milestoneIndex, "utf8")) as { epics: Array<{ id: string; name: string; path: string }> };
    milestoneData.epics.push({ id: "E2", name: "Epic 2", path: "02-epic" });
    writeFileSync(milestoneIndex, `${stringify(milestoneData)}\n`);

    const epicDir = join(root, ".tasks", "01-phase", "01-ms", "02-epic");
    mkdirSync(epicDir, { recursive: true });
    const epicTask = join(epicDir, "T003-blocker.task");
    writeFileSync(
      join(epicDir, "index.yaml"),
      `tasks:
  - id: T001
    title: Blocker
    file: T003-blocker.task
    status: pending
    estimate_hours: 1
    complexity: low
    priority: medium
`,
    );
    writeFileSync(
      epicTask,
      `---
id: P1.M1.E2.T001
title: Blocker
status: pending
estimate_hours: 1
complexity: low
priority: medium
depends_on: []
tags: []
---`,
    );

    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");
    updateTodoFrontmatter(taskPath, (frontmatter) => {
      frontmatter.depends_on = ["E2"];
    });

    const p = run(["check"], root);
    expect(p.exitCode).toBe(0);
  });

  test("check fails when task dependency references missing epic", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");
    updateTodoFrontmatter(taskPath, (frontmatter) => {
      frontmatter.depends_on = ["E2"];
    });

    const p = run(["check", "--json"], root);
    expect(p.exitCode).toBe(1);
    expect(p.stdout.toString()).toContain("missing_task_dependency");
    expect(p.stdout.toString()).toContain("E2");
  });

  test("check reports task dependency cycle", () => {
    root = setupFixture();
    const task1 = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const task2 = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");
    updateTodoFrontmatter(task1, (frontmatter) => {
      frontmatter.depends_on = ["P1.M1.E1.T002"];
    });
    updateTodoFrontmatter(task2, (frontmatter) => {
      frontmatter.depends_on = ["P1.M1.E1.T001"];
    });

    const p = run(["check"], root);
    expect(p.exitCode).toBe(1);
    expect(p.stdout.toString()).toContain("task_dependency_cycle");
    expect(p.stdout.toString()).toContain("P1.M1.E1.T001");
    expect(p.stdout.toString()).toContain("P1.M1.E1.T002");
  });

  test("check command warns on uninitialized todo files", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    
    // Create a task file with default template content
    const uninitializedContent = `---
id: P1.M1.E1.T001
title: Task 1
status: pending
estimate_hours: 1
complexity: low
priority: medium
depends_on: []
tags: []
---

# Task 1

## Requirements

- TODO: Add requirements

## Acceptance Criteria

- TODO: Add acceptance criteria
`;
    writeFileSync(taskPath, uninitializedContent);
    
    let p = run(["check"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("uninitialized_todo");
    expect(p.stdout.toString()).toContain("P1.M1.E1.T001");
    
    p = run(["check", "--strict"], root);
    expect(p.exitCode).toBe(1);
  });

  test("data summary/export json", () => {
    root = setupFixture();
    let p = run(["data", "summary", "--format", "json"], root);
    expect(p.exitCode).toBe(0);
    const summary = JSON.parse(p.stdout.toString());
    expect(summary.project).toBe("Native");
    expect(summary.overall.total_tasks).toBe(2);

    p = run(["data", "export", "--format", "json"], root);
    expect(p.exitCode).toBe(0);
    const exported = JSON.parse(p.stdout.toString());
    expect(exported.project).toBe("Native");
    expect(Array.isArray(exported.phases)).toBeTrue();
    expect(exported.phases[0].milestones[0].epics[0].tasks.length).toBe(2);

    p = run(["data", "export", "--format", "json", "--scope", "P1", "--scope", "P1.M1"], root);
    expect(p.exitCode).toBe(0);
    const scopedExport = JSON.parse(p.stdout.toString());
    expect(scopedExport.phases.length).toBeGreaterThan(0);

    p = run(["data", "export", "--format", "json", "--scope", "P9"], root);
    expect(p.exitCode).toBe(1);
  });

  test("report progress/velocity/estimate-accuracy json", () => {
    root = setupFixture();
    // create duration-bearing completed task
    let p = run(["claim", "P1.M1.E1.T001", "--agent", "agent-r"], root);
    expect(p.exitCode).toBe(0);
    p = run(["done", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);

    p = run(["bug", "fix flaky integration test"], root);
    expect(p.exitCode).toBe(0);
    p = run(["idea", "capture future optimization idea"], root);
    expect(p.exitCode).toBe(0);

    p = run(["report", "progress", "--format", "json"], root);
    expect(p.exitCode).toBe(0);
    const progress = JSON.parse(p.stdout.toString());
    expect(progress.overall.total).toBe(2);
    expect(progress.auxiliary.bugs.total).toBe(1);
    expect(progress.auxiliary.ideas.total).toBe(1);
    expect(progress.bugs[0].id).toBe("B001");
    expect(progress.ideas[0].id).toBe("I001");

    p = run(["report", "velocity", "--days", "7", "--format", "json"], root);
    expect(p.exitCode).toBe(0);
    const reportVelocity = JSON.parse(p.stdout.toString());
    expect(reportVelocity.days_analyzed).toBe(7);
    expect(Array.isArray(reportVelocity.daily_data)).toBeTrue();
    expect(reportVelocity.daily_data.length).toBe(2);

    p = run(["report", "estimate-accuracy", "--format", "json"], root);
    expect(p.exitCode).toBe(0);
    const accuracy = JSON.parse(p.stdout.toString());
    expect(accuracy.tasks_analyzed).toBeGreaterThanOrEqual(1);

    p = run(["report"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Progress Report");
    expect(p.stdout.toString()).toContain("Report commands:");
    expect(p.stdout.toString()).toContain("Auxiliary:");
    expect(p.stdout.toString()).toContain("All Bugs");
    expect(p.stdout.toString()).toContain("All Ideas");

    p = run(["r", "p", "--format", "json"], root);
    expect(p.exitCode).toBe(0);
    const progressAlias = JSON.parse(p.stdout.toString());
    expect(progressAlias.overall.total).toBe(2);

    p = run(["r", "v", "--days", "7", "--format", "json"], root);
    expect(p.exitCode).toBe(0);
    const velocityAlias = JSON.parse(p.stdout.toString());
    expect(velocityAlias.days_analyzed).toBe(7);
    expect(velocityAlias.daily_data.length).toBe(2);

    p = run(["velocity", "--days", "7", "--format", "json"], root);
    expect(p.exitCode).toBe(0);
    const velocityCommand = JSON.parse(p.stdout.toString());
    expect(velocityCommand).toEqual(reportVelocity);
  });

  test("timeline and schema commands", () => {
    root = setupFixture();
    let p = run(["timeline"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Project Timeline");

    const alias = run(["tl"], root);
    expect(alias.exitCode).toBe(0);
    expect(alias.stdout.toString()).toContain("Project Timeline");
    expect(alias.stdout.toString()).toBe(p.stdout.toString());

    p = run(["timeline", "--scope", "P1", "--scope", "P1.M1"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Project Timeline");

    p = run(["timeline", "--scope", "P9"], root);
    expect(p.exitCode).toBe(1);

    p = run(["schema", "--json"], root);
    expect(p.exitCode).toBe(0);
    const schema = JSON.parse(p.stdout.toString());
    expect(schema.schema_version).toBe(1);
    expect(Array.isArray(schema.files)).toBeTrue();
  });

  test("help shows timeline alias on one line", () => {
    root = setupFixture();
    const p = run(["--help"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("\n  howto");
    const commandsSection = out.split("Commands:\n")[1] ?? "";
    const firstLine = commandsSection.split("\n")[0] ?? "";
    expect(firstLine).toContain("howto");
    expect(out).toContain("timeline        Display an ASCII Gantt chart of the project timeline (alias: tl)");
    expect(out).not.toContain("\n  tl             Display an ASCII Gantt chart of the project timeline.");
  });

  test("show --help renders command-specific guidance", () => {
    root = setupFixture();
    const p = run(["show", "--help"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Command Help: backlog show");
    expect(out).toContain("Usage: backlog show [PATH_ID ...] [--long] [--all]");
  });

  test("cat --help renders command-specific guidance", () => {
    root = setupFixture();
    const p = run(["cat", "--help"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Command Help: backlog cat");
    expect(out).toContain("Usage: backlog cat <TASK_ID ...>");
  });

  test("help sync renders sync command guidance", () => {
    root = setupFixture();
    const p = run(["help", "sync"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Command Help: backlog sync");
    expect(out).toContain("Usage: backlog sync");
    expect(out).toContain("Sync derived metadata in index files.");
  });

  test("ci validate-ids validates shorthand IDs", () => {
    root = setupFixture();
    const p = run(["ci", "validate-ids", "B020", "E1.T02", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toBe("");
  });

  test("ci validate-ids rejects malformed ids", () => {
    root = setupFixture();
    const p = run(["ci", "validate-ids", "B020", "not-an-id"], root);
    expect(p.exitCode).not.toBe(0);
    const out = p.stdout.toString() + p.stderr.toString();
    expect(p.stdout.toString()).toBe("");
    expect(out).toContain("Invalid TASK_ID: not-an-id");
  });

  test("all commands provide non-thin --help output", () => {
    root = setupFixture();
    const commands = [
      "howto", "ls", "list", "tree", "show", "cat", "next", "preview", "claim", "grab", "done", "undone",
      "cycle", "dash", "edit", "update", "set", "work", "unclaim", "blocked", "bug", "idea", "fixed", "search",
      "version", "ci",
      "check", "init", "add", "add-epic", "add-milestone", "add-phase", "lock", "unlock", "move", "session",
      "data", "report", "timeline", "schema", "blockers", "skills", "agents", "log", "migrate", "benchmark",
    ];
    for (const command of commands) {
      const p = run([command, "--help"], root);
      expect(p.exitCode).toBe(0);
      const out = p.stdout.toString();
      expect(out.trim().length).toBeGreaterThan(30);
      expect(out).toContain("Usage");
      expect(out).not.toBe(`Usage: backlog ${command}\n`);
    }
  }, 20000);

  test("howto prints agent guidance in text and json", () => {
    root = setupFixture();
    let p = run(["howto"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("# Backlog How-To");
    expect(p.stdout.toString()).toContain("Core Work Loop");

    p = run(["howto", "--json"], root);
    expect(p.exitCode).toBe(0);
    const payload = JSON.parse(p.stdout.toString());
    expect(payload.name).toBe("backlog-howto");
    expect(typeof payload.skill_version).toBe("string");
    expect(payload.content).toContain("# Backlog How-To");
  });

  test("search and blockers commands", () => {
    root = setupFixture();
    let p = run(["search", "A"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("P1.M1.E1.T001");

    p = run(["blockers"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("waiting on dependencies");

    p = run(["blockers", "--suggest"], root);
    expect(p.exitCode).toBe(0);
  });

  test("skills install dry-run json and write", () => {
    root = setupFixture();
    let p = run(
      ["skills", "install", "plan-task", "--client=codex", "--artifact=skills", "--dry-run", "--json"],
      root,
    );
    expect(p.exitCode).toBe(0);
    const dry = JSON.parse(p.stdout.toString());
    expect(dry.dry_run).toBeTrue();
    expect(Array.isArray(dry.operations)).toBeTrue();
    expect(dry.operations[0].path).toContain(".agents/skills/plan-task/SKILL.md");

    p = run(
      ["skills", "install", "plan-task", "--client=codex", "--artifact=skills"],
      root,
    );
    expect(p.exitCode).toBe(0);
    expect(readFileSync(join(root, ".agents", "skills", "plan-task", "SKILL.md"), "utf8")).toContain("plan-task");
  });

  test("skills install default includes backlog-howto", () => {
    root = setupFixture();
    const p = run(
      ["skills", "install", "--client=codex", "--artifact=skills", "--dry-run", "--json"],
      root,
    );
    expect(p.exitCode).toBe(0);
    const dry = JSON.parse(p.stdout.toString());
    expect(dry.skills).toContain("backlog-howto");
    expect(
      dry.operations.some((op: { path: string }) => op.path.includes(".agents/skills/backlog-howto/SKILL.md")),
    ).toBeTrue();
  });

  test("skills install backlog-howto command artifact references source+version", () => {
    root = setupFixture();
    const p = run(
      ["skills", "install", "backlog-howto", "--client=opencode", "--artifact=commands"],
      root,
    );
    expect(p.exitCode).toBe(0);
    const command = readFileSync(join(root, ".opencode", "commands", "backlog-howto.md"), "utf8");
    expect(command).toContain("bl_skills/backlog-howto/SKILL.md");
    expect(command).toContain("Skill-Version:");
  });

  test("agents profile output", () => {
    root = setupFixture();
    let p = run(["agents", "--profile", "short"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("AGENTS.md (Short)");

    p = run(["agents", "--profile", "all"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("AGENTS.md (Short)");
    expect(out).toContain("AGENTS.md (Medium)");
    expect(out).toContain("AGENTS.md (Long)");
  });

  test("add/add-epic/add-milestone/add-phase commands", () => {
    root = setupFixture();
    const phaseDescription = "Ship planning and prep";
    const milestoneDescription = "Add docs, tests, and tracking";
    const epicDescription = "Capture edge-case tasks";

    let p = run(["add", "P1.M1.E1", "--title", "New Task"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Created task:");
    const createdTaskMatch = p.stdout.toString().match(/Created task: ([A-Z0-9.]+)/);
    const createdTaskId = createdTaskMatch?.[1];
    expect(createdTaskMatch).toBeTruthy();
    const shortTaskId = createdTaskId!.split(".").at(-1);
    expect(p.stdout.toString()).toContain(
      `File: .tasks/01-phase/01-ms/01-epic/${shortTaskId}-new-task.todo`,
    );
    expect(p.stdout.toString()).toContain("Next:");
    expect(p.stdout.toString()).toContain(`backlog show ${createdTaskId}`);
    expect(p.stdout.toString()).toContain(`backlog claim ${createdTaskId}`);

    p = run(
      ["add-epic", "P1.M1", "--title", "New Epic", "--description", epicDescription],
      root,
    );
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Created epic:");
    expect(p.stdout.toString()).toContain(
      "File: .tasks/01-phase/01-ms/02-new-epic/index.yaml",
    );
    expect(p.stdout.toString()).toContain("Next:");
    expect(p.stdout.toString()).toContain("backlog show P1.M1.E2");
    expect(p.stdout.toString()).toContain("backlog add P1.M1.E2 --title");
    const epicIndex = parse(
      readFileSync(
        join(root, ".tasks", "01-phase", "01-ms", "index.yaml"),
        "utf8",
      ),
    ) as {
      epics: {
        id: string;
        description?: string;
      }[];
    };
    const addedEpic = epicIndex.epics.find((entry) => entry.id === "E2");
    expect(addedEpic?.description).toBe(epicDescription);

    p = run(
      [
        "add-milestone",
        "P1",
        "--title",
        "New Milestone",
        "--description",
        milestoneDescription,
      ],
      root,
    );
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Created milestone:");
    expect(p.stdout.toString()).toContain(
      "File: .tasks/01-phase/02-new-milestone/index.yaml",
    );
    expect(p.stdout.toString()).toContain("Next:");
    expect(p.stdout.toString()).toContain("backlog show P1.M2");
    expect(p.stdout.toString()).toContain("backlog add-epic P1.M2 --title");
    const milestoneIndex = parse(
      readFileSync(join(root, ".tasks", "01-phase", "index.yaml"), "utf8"),
    ) as {
      milestones: {
        id: string;
        description?: string;
      }[];
    };
    const addedMilestone = milestoneIndex.milestones.find((entry) => entry.id === "M2");
    expect(addedMilestone?.description).toBe(milestoneDescription);

    p = run(
      ["add-phase", "--title", "New Phase", "--description", phaseDescription],
      root,
    );
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Created phase:");
    expect(p.stdout.toString()).toContain("File: .tasks/02-new-phase/index.yaml");
    expect(p.stdout.toString()).toContain("Next:");
    expect(p.stdout.toString()).toContain("backlog show P2");
    expect(p.stdout.toString()).toContain("backlog add-milestone P2 --title");
    const rootIndex = parse(
      readFileSync(join(root, ".tasks", "index.yaml"), "utf8"),
    ) as {
      phases: { id: string; description?: string }[];
    };
    const addedPhase = rootIndex.phases.find((entry) => entry.id === "P2");
    expect(addedPhase?.description).toBe(phaseDescription);
  });

  test("add/add-epic/add-milestone/add-phase reject invalid dependency ids", () => {
    root = setupFixture();

    let p = run(["add", "P1.M1.E1", "--title", "Bad dependency", "--depends-on", "not a task id"], root);
    expect(p.exitCode).toBe(1);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("invalid dependency id");

    p = run(["add-epic", "P1.M1", "--title", "Bad dependency", "--depends-on", "not a task id"], root);
    expect(p.exitCode).toBe(1);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("invalid dependency id");

    p = run([
      "add-milestone",
      "P1",
      "--title",
      "Bad dependency",
      "--depends-on",
      "not a task id",
    ], root);
    expect(p.exitCode).toBe(1);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("invalid dependency id");

    p = run(["add-phase", "--title", "Bad dependency", "--depends-on", "not a task id"], root);
    expect(p.exitCode).toBe(1);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("invalid dependency id");
  });

  test("lock/unlock gates add commands for epic, milestone, and phase", () => {
    root = setupFixture();

    let p = run(["lock", "P1.M1.E1"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Locked: P1.M1.E1");

    p = run(["add", "P1.M1.E1", "--title", "Blocked"], root);
    expect(p.exitCode).not.toBe(0);
    const addErr = `${p.stdout.toString()}\n${p.stderr.toString()}`;
    expect(addErr).toContain("has been closed and cannot accept new tasks");
    expect(addErr.toLowerCase()).toContain("agent should create a new epic");

    p = run(["unlock", "P1.M1.E1"], root);
    expect(p.exitCode).toBe(0);
    p = run(["add", "P1.M1.E1", "--title", "Allowed"], root);
    expect(p.exitCode).toBe(0);

    p = run(["lock", "P1.M1"], root);
    expect(p.exitCode).toBe(0);
    p = run(["add-epic", "P1.M1", "--title", "Blocked Epic"], root);
    expect(p.exitCode).not.toBe(0);
    const addEpicErr = `${p.stdout.toString()}\n${p.stderr.toString()}`;
    expect(addEpicErr).toContain("has been closed and cannot accept new epics");

    p = run(["lock", "P1"], root);
    expect(p.exitCode).toBe(0);
    p = run(["add-milestone", "P1", "--title", "Blocked Milestone"], root);
    expect(p.exitCode).not.toBe(0);
    const addMsErr = `${p.stdout.toString()}\n${p.stderr.toString()}`;
    expect(addMsErr).toContain("has been closed and cannot accept new milestones");
  });

  test("move task to different epic renumbers ID", () => {
    root = setupFixture();
    const tasksRoot = join(root, ".tasks");
    mkdirSync(join(tasksRoot, "01-phase", "01-ms", "02-target-epic"), { recursive: true });
    writeFileSync(
      join(tasksRoot, "01-phase", "01-ms", "index.yaml"),
      [
        "epics:",
        "  - id: E1",
        "    name: E",
        "    path: 01-epic",
        "  - id: E2",
        "    name: Target",
        "    path: 02-target-epic",
        "",
      ].join("\n"),
    );
    writeFileSync(join(tasksRoot, "01-phase", "01-ms", "02-target-epic", "index.yaml"), "id: P1.M1.E2\nname: Target\ntasks: []\n");

    const p = run(["move", "P1.M1.E1.T001", "--to", "P1.M1.E2"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("New ID: P1.M1.E2.T001");

    const movedPath = join(tasksRoot, "01-phase", "01-ms", "02-target-epic", "T001-a.todo");
    expect(existsSync(movedPath)).toBeTrue();
    expect(readFileSync(movedPath, "utf8")).toContain("id: P1.M1.E2.T001");
  });

  test("move epic to different milestone remaps descendant task IDs", () => {
    root = setupFixture();
    const tasksRoot = join(root, ".tasks");
    mkdirSync(join(tasksRoot, "01-phase", "02-ms"), { recursive: true });
    writeFileSync(
      join(tasksRoot, "01-phase", "index.yaml"),
      [
        "milestones:",
        "  - id: M1",
        "    name: M",
        "    path: 01-ms",
        "  - id: M2",
        "    name: M2",
        "    path: 02-ms",
        "",
      ].join("\n"),
    );
    writeFileSync(join(tasksRoot, "01-phase", "02-ms", "index.yaml"), "epics: []\n");

    const p = run(["move", "P1.M1.E1", "--to", "P1.M2"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("New ID: P1.M2.E1");

    const movedTaskPath = join(tasksRoot, "01-phase", "02-ms", "01-e", "T001-a.todo");
    expect(existsSync(movedTaskPath)).toBeTrue();
    expect(readFileSync(movedTaskPath, "utf8")).toContain("id: P1.M2.E1.T001");
  });

  test("idea command creates planning intake under .tasks/ideas", () => {
    root = setupFixture();
    const p = run(["idea", "add integration tests using gpt-oss-120 via groq"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Created idea:");
    expect(p.stdout.toString()).toContain("File:");
    expect(p.stdout.toString()).toContain("Next:");
    expect(p.stdout.toString()).toContain("backlog add-phase");

    const indexText = readFileSync(join(root, ".tasks", "ideas", "index.yaml"), "utf8");
    expect(indexText).toContain("id: I001");
    const fileMatch = indexText.match(/file:\s*(\S+)/);
    expect(fileMatch).toBeTruthy();

    const ideaText = readFileSync(join(root, ".tasks", "ideas", fileMatch![1]!), "utf8");
    expect(ideaText).toContain("estimate_hours: 10");
    expect(ideaText).toContain("Run `/plan-task");
    expect(ideaText).toContain("tasks add");
    expect(ideaText).toContain("tasks bug");
  });

  test("idea command accepts body metadata flags", () => {
    root = setupFixture();
    const p = run(
      [
        "idea",
        "--title",
        "capture onboarding flow",
        "--estimate",
        "12",
        "--priority",
        "high",
        "--depends-on",
        "I100",
        "--tags",
        "research,ux",
        "--body",
        "Custom idea body",
      ],
      root,
    );
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Created idea: I001");

    const indexText = readFileSync(join(root, ".tasks", "ideas", "index.yaml"), "utf8");
    expect(indexText).toContain("id: I001");
    const fileMatch = indexText.match(/file:\s*(\S+)/);
    expect(fileMatch).toBeTruthy();

    const ideaText = readFileSync(join(root, ".tasks", "ideas", fileMatch![1]!), "utf8");
    expect(ideaText).toContain("estimate_hours: 12");
    expect(ideaText).toContain("priority: high");
    expect(ideaText).toContain("- I100");
    expect(ideaText).toContain("- research");
    expect(ideaText).toContain("- ux");
    expect(ideaText).toContain("Custom idea body");
    expect(ideaText).not.toContain("Created Work Items");
  });

  test("idea command rejects invalid dependency id", () => {
    root = setupFixture();

    const p = run(
      [
        "idea",
        "--title",
        "Bad depends on idea",
        "--depends-on",
        "not a task id",
      ],
      root,
    );
    expect(p.exitCode).not.toBe(0);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("invalid dependency id");
  });

  test("add auto-commits created task when no staged files exist", () => {
    root = setupFixture();
    initializeTestGitRepo(root);
    const initialCommitCount = gitCommitCount(root);

    const p = run(["add", "P1.M1.E1", "--title", "auto-commit task"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Created task: P1.M1.E1.T003");
    expect(gitCommitCount(root)).toBe(initialCommitCount + 1);
    expect(runGit(root, "log", "-1", "--pretty=%B")).toBe("bl add P1.M1.E1.T003: auto-commit task");
    expect(runGit(root, "status", "--short")).toBe("");
  });

  test("add skips auto-commit if staged files already exist", () => {
    root = setupFixture();
    initializeTestGitRepo(root);
    writeFileSync(join(root, "staged-change.txt"), "staged\n");
    runGit(root, "add", "staged-change.txt");

    const initialCommitCount = gitCommitCount(root);
    const p = run(["add", "P1.M1.E1", "--title", "skip commit"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Created task: P1.M1.E1.T003");
    expect(gitCommitCount(root)).toBe(initialCommitCount);
    expect(runGit(root, "status", "--short")).toContain("A  staged-change.txt");
  });

  test("add amends prior local bl add commit when unpushed", () => {
    root = setupFixture();
    initializeTestGitRepo(root);

    let p = run(["add", "P1.M1.E1", "--title", "first auto task"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Created task: P1.M1.E1.T003");
    const commitCountAfterFirst = gitCommitCount(root);

    p = run(["add", "P1.M1.E1", "--title", "second auto task"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Created task: P1.M1.E1.T004");
    expect(gitCommitCount(root)).toBe(commitCountAfterFirst);
    expect(runGit(root, "log", "-1", "--pretty=%B")).toBe("bl add P1.M1.E1.T003: first auto task");
  });

  test("bug auto-commits created bug when no staged files exist", () => {
    root = setupFixture();
    initializeTestGitRepo(root);
    const initialCommitCount = gitCommitCount(root);

    const p = run(["bug", "--title", "Auto commit bug"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Created bug: B001");
    expect(gitCommitCount(root)).toBe(initialCommitCount + 1);
    expect(runGit(root, "log", "-1", "--pretty=%B")).toBe("bl bug B001: Auto commit bug");
  });

  test("bug command rejects invalid dependency id", () => {
    root = setupFixture();

    const p = run(
      [
        "bug",
        "--title",
        "Bad depends on bug",
        "--depends-on",
        "not a task id",
      ],
      root,
    );
    expect(p.exitCode).not.toBe(0);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("invalid dependency id");
  });

  test("idea auto-commits created idea when no staged files exist", () => {
    root = setupFixture();
    initializeTestGitRepo(root);
    const initialCommitCount = gitCommitCount(root);

    const p = run(["idea", "auto commit idea"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Created idea: I001");
    expect(gitCommitCount(root)).toBe(initialCommitCount + 1);
    expect(runGit(root, "log", "-1", "--pretty=%B")).toBe("bl idea I001: auto commit idea");
  });

  test("claim auto-commits claimed task when no staged files exist", () => {
    root = setupFixture();
    initializeTestGitRepo(root);
    const initialCommitCount = gitCommitCount(root);

    const p = run(["claim", "P1.M1.E1.T001", "--agent", "agent-ts"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Claimed:");
    expect(gitCommitCount(root)).toBe(initialCommitCount + 1);
    expect(runGit(root, "log", "-1", "--pretty=%B")).toBe("bl claim P1.M1.E1.T001: A");
    expect(runGit(root, "status", "--short")).toBe("");
  });

  test("claim auto-commit skips when staged files already exist", () => {
    root = setupFixture();
    initializeTestGitRepo(root);
    writeFileSync(join(root, "staged-change.txt"), "staged\n");
    runGit(root, "add", "staged-change.txt");

    const initialCommitCount = gitCommitCount(root);
    const p = run(["claim", "P1.M1.E1.T001", "--agent", "agent-ts"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Claimed:");
    expect(gitCommitCount(root)).toBe(initialCommitCount);
    expect(runGit(root, "status", "--short")).toContain("A  staged-change.txt");
  });

  test("grab auto-commits grabbed task when no staged files exist", () => {
    root = setupFixture();
    initializeTestGitRepo(root);
    const initialCommitCount = gitCommitCount(root);

    const p = run(
      ["grab", "--single", "--agent", "agent-ts", "--no-content"],
      root,
    );
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("P1.M1.E1.T001");
    expect(gitCommitCount(root)).toBe(initialCommitCount + 1);
    expect(runGit(root, "log", "-1", "--pretty=%B")).toBe("bl grab P1.M1.E1.T001: A");
  });

  test("grab auto-commit skips when staged files already exist", () => {
    root = setupFixture();
    initializeTestGitRepo(root);
    writeFileSync(join(root, "staged-change.txt"), "staged\n");
    runGit(root, "add", "staged-change.txt");

    const initialCommitCount = gitCommitCount(root);
    const p = run(["grab", "--single", "--agent", "agent-ts", "--no-content"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("P1.M1.E1.T001");
    expect(gitCommitCount(root)).toBe(initialCommitCount);
    expect(runGit(root, "status", "--short")).toContain("A  staged-change.txt");
  });

  test("done auto-commits completed task when no staged files exist", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    updateTodoFrontmatter(taskPath, (frontmatter) => {
      frontmatter.status = "in_progress";
      frontmatter.claimed_by = "agent-ts";
      frontmatter.claimed_at = new Date().toISOString();
      frontmatter.started_at = new Date().toISOString();
    });

    initializeTestGitRepo(root);
    const initialCommitCount = gitCommitCount(root);

    const p = run(["done", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Completed:");
    expect(gitCommitCount(root)).toBe(initialCommitCount + 1);
    expect(runGit(root, "log", "-1", "--pretty=%B")).toBe("bl done P1.M1.E1.T001: A");
  });

  test("done auto-commit skips when staged files already exist", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    updateTodoFrontmatter(taskPath, (frontmatter) => {
      frontmatter.status = "in_progress";
      frontmatter.claimed_by = "agent-ts";
      frontmatter.claimed_at = new Date().toISOString();
      frontmatter.started_at = new Date().toISOString();
    });

    initializeTestGitRepo(root);
    writeFileSync(join(root, "staged-change.txt"), "staged\n");
    runGit(root, "add", "staged-change.txt");

    const initialCommitCount = gitCommitCount(root);
    const p = run(["done", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Completed:");
    expect(gitCommitCount(root)).toBe(initialCommitCount);
    expect(runGit(root, "status", "--short")).toContain("A  staged-change.txt");
  });

  test("set auto-commits updated task when no staged files exist", () => {
    root = setupFixture();
    initializeTestGitRepo(root);
    const initialCommitCount = gitCommitCount(root);

    const p = run(["set", "P1.M1.E1.T001", "--priority", "critical"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Updated: P1.M1.E1.T001");
    expect(gitCommitCount(root)).toBe(initialCommitCount + 1);
    expect(runGit(root, "log", "-1", "--pretty=%B")).toBe("bl set P1.M1.E1.T001: A");
  });

  test("set auto-commit skips when staged files already exist", () => {
    root = setupFixture();
    initializeTestGitRepo(root);
    writeFileSync(join(root, "staged-change.txt"), "staged\n");
    runGit(root, "add", "staged-change.txt");

    const initialCommitCount = gitCommitCount(root);
    const p = run(["set", "P1.M1.E1.T001", "--priority", "high"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Updated: P1.M1.E1.T001");
    expect(gitCommitCount(root)).toBe(initialCommitCount);
    expect(runGit(root, "status", "--short")).toContain("A  staged-change.txt");
  });

  test("undone auto-commits reset task when no staged files exist", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    updateTodoFrontmatter(taskPath, (frontmatter) => {
      frontmatter.status = "done";
      frontmatter.completed_at = "2026-01-01T00:00:00.000Z";
    });

    initializeTestGitRepo(root);
    const initialCommitCount = gitCommitCount(root);

    const p = run(["undone", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Marked not done:");
    expect(gitCommitCount(root)).toBe(initialCommitCount + 1);
    expect(runGit(root, "log", "-1", "--pretty=%B")).toBe("bl undone P1.M1.E1.T001: A");
  });

  test("undone auto-commit skips when staged files already exist", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    updateTodoFrontmatter(taskPath, (frontmatter) => {
      frontmatter.status = "done";
      frontmatter.completed_at = "2026-01-01T00:00:00.000Z";
    });

    initializeTestGitRepo(root);
    writeFileSync(join(root, "staged-change.txt"), "staged\n");
    runGit(root, "add", "staged-change.txt");

    const initialCommitCount = gitCommitCount(root);
    const p = run(["undone", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Marked not done:");
    expect(gitCommitCount(root)).toBe(initialCommitCount);
    expect(runGit(root, "status", "--short")).toContain("A  staged-change.txt");
  });

  test("unclaim auto-commits reset task when no staged files exist", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    updateTodoFrontmatter(taskPath, (frontmatter) => {
      frontmatter.status = "in_progress";
      frontmatter.claimed_by = "agent-ts";
      frontmatter.claimed_at = "2026-01-01T00:00:00.000Z";
      frontmatter.started_at = "2026-01-01T00:00:00.000Z";
    });

    initializeTestGitRepo(root);
    const initialCommitCount = gitCommitCount(root);

    const p = run(["unclaim", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Unclaimed:");
    expect(gitCommitCount(root)).toBe(initialCommitCount + 1);
    expect(runGit(root, "log", "-1", "--pretty=%B")).toBe("bl unclaim P1.M1.E1.T001: A");
  });

  test("unclaim auto-commit skips when staged files already exist", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    updateTodoFrontmatter(taskPath, (frontmatter) => {
      frontmatter.status = "in_progress";
      frontmatter.claimed_by = "agent-ts";
      frontmatter.claimed_at = "2026-01-01T00:00:00.000Z";
      frontmatter.started_at = "2026-01-01T00:00:00.000Z";
    });

    initializeTestGitRepo(root);
    writeFileSync(join(root, "staged-change.txt"), "staged\n");
    runGit(root, "add", "staged-change.txt");

    const initialCommitCount = gitCommitCount(root);
    const p = run(["unclaim", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Unclaimed:");
    expect(gitCommitCount(root)).toBe(initialCommitCount);
    expect(runGit(root, "status", "--short")).toContain("A  staged-change.txt");
  });

  test("idea rejects single-word title", () => {
    root = setupFixture();
    const p = run(["idea", "plan"], root);
    expect(p.exitCode).toBe(1);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("at least two words");
  });

  test("bug accepts positional description as simple title", () => {
    root = setupFixture();
    const p = run(["bug", "fix flaky integration test"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Created bug:");
    expect(out).toContain("IMPORTANT: You MUST fill in the .todo file that was created.");
    expect(out).toContain("File:");
    expect(out).toContain("Next:");
    expect(out).toContain("backlog show B001");
    expect(out).toContain("backlog claim B001");

    const bugsIndexText = readFileSync(join(root, ".tasks", "bugs", "index.yaml"), "utf8");
    const fileMatch = bugsIndexText.match(/file:\s*(\S+)/);
    expect(fileMatch).toBeTruthy();

    const bugText = readFileSync(join(root, ".tasks", "bugs", fileMatch![1]!), "utf8");
    expect(bugText).toContain("title: fix flaky integration test");
    expect(bugText).toContain("## Steps to Reproduce");
  });

  test("bug rejects single-word positional title", () => {
    root = setupFixture();
    const p = run(["bug", "list"], root);
    expect(p.exitCode).toBe(1);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("at least two words");
  });

  test("bug rejects single-word --title", () => {
    root = setupFixture();
    const p = run(["bug", "--title", "fix"], root);
    expect(p.exitCode).toBe(1);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("at least two words");
  });

  test("grab prioritizes normal tasks over ideas", () => {
    root = setupFixture();
    let p = run(["idea", "collect future refactor idea"], root);
    expect(p.exitCode).toBe(0);

    p = run(["grab", "--single", "--agent", "agent-priority", "--no-content"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("P1.M1.E1.T001");
    expect(out).not.toContain("I001");
  });

  test("grab prioritizes bugs over normal tasks", () => {
    root = setupFixture();
    let p = run(["bug", "--title", "tiny low bug", "--estimate", "0.1", "--complexity", "low", "--priority", "low", "--simple"], root);
    expect(p.exitCode).toBe(0);

    p = run(["grab", "--single", "--agent", "agent-priority", "--no-content"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("B001");
  });

  test("grab supports repeated --scope and fails strictly on invalid scope", () => {
    root = setupFixture();

    let p = run(["grab", "--single", "--scope", "P1", "--scope", "P1.M1", "--agent", "agent-scope"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("P1.M1.E1.T001");

    p = run(["grab", "--single", "--scope", "P9", "--agent", "agent-scope"], root);
    expect(p.exitCode).toBe(1);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("No list nodes found for path query: P9");
  });

  test("grab auto-selected bug fans out to additional bugs", () => {
    root = setupFixture();
    let p = run(["bug", "--title", "bug one", "--simple"], root);
    expect(p.exitCode).toBe(0);
    p = run(["bug", "--title", "bug two", "--simple"], root);
    expect(p.exitCode).toBe(0);
    p = run(["bug", "--title", "bug three", "--simple"], root);
    expect(p.exitCode).toBe(0);

    p = run(["grab", "--agent", "agent-fanout", "--single", "--no-content"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("B001");
    expect(out).toContain("B002");
    expect(out).toContain("B003");
    expect(out.toLowerCase()).toContain("parallel");
  });

  test("grab auto-selected bug fans out in priority order", () => {
    root = setupFixture();
    let p = run(["bug", "--title", "primary bug", "--priority", "critical", "--simple"], root);
    expect(p.exitCode).toBe(0);
    p = run(["bug", "--title", "low bug", "--priority", "low", "--simple"], root);
    expect(p.exitCode).toBe(0);
    p = run(["bug", "--title", "high bug", "--priority", "high", "--simple"], root);
    expect(p.exitCode).toBe(0);

    p = run(["grab", "--agent", "agent-fanout", "--single", "--no-content"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    const idxB3 = out.indexOf("B003");
    const idxB2 = out.indexOf("B002");
    expect(idxB3).toBeGreaterThan(-1);
    expect(idxB2).toBeGreaterThan(-1);
    expect(idxB3).toBeLessThan(idxB2);
  });

  test("list and next include idea tasks", () => {
    root = setupFixture();
    const t001Path = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const t002Path = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");
    writeFileSync(t001Path, readFileSync(t001Path, "utf8").replace("status: pending", "status: done"));
    writeFileSync(t002Path, readFileSync(t002Path, "utf8").replace("status: pending", "status: done"));

    let p = run(["idea", "capture planning work"], root);
    expect(p.exitCode).toBe(0);

    p = run(["list"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Ideas");
    expect(p.stdout.toString()).toContain("I001: capture planning work");

    p = run(["next", "--json"], root);
    expect(p.exitCode).toBe(0);
    expect(JSON.parse(p.stdout.toString()).id).toBe("I001");
  });

  test("list --bugs and --ideas filter auxiliary sections", () => {
    root = setupFixture();

    let p = run(["bug", "--title", "critical bug", "--simple"], root);
    expect(p.exitCode).toBe(0);

    p = run(["idea", "future planning idea"], root);
    expect(p.exitCode).toBe(0);

    p = run(["list", "--bugs"], root);
    expect(p.exitCode).toBe(0);
    const bugsOut = p.stdout.toString();
    expect(bugsOut).toContain("Bugs");
    expect(bugsOut).toContain("B001");
    expect(bugsOut).not.toContain("Ideas");
    expect(bugsOut).not.toContain("Phase");

    p = run(["list", "--ideas"], root);
    expect(p.exitCode).toBe(0);
    const ideasOut = p.stdout.toString();
    expect(ideasOut).toContain("Ideas");
    expect(ideasOut).toContain("I001");
    expect(ideasOut).not.toContain("Bugs");
    expect(ideasOut).not.toContain("Phase");
  });

  test("list -a/-b/-i filter auxiliary sections", () => {
    root = setupFixture();

    let p = run(["bug", "--title", "critical bug", "--simple"], root);
    expect(p.exitCode).toBe(0);

    p = run(["idea", "future planning idea"], root);
    expect(p.exitCode).toBe(0);

    p = run(["list", "-b"], root);
    expect(p.exitCode).toBe(0);
    const bugsOut = p.stdout.toString();
    expect(bugsOut).toContain("Bugs");
    expect(bugsOut).toContain("B001");
    expect(bugsOut).not.toContain("Ideas");
    expect(bugsOut).not.toContain("Phase");

    p = run(["list", "-i"], root);
    expect(p.exitCode).toBe(0);
    const ideasOut = p.stdout.toString();
    expect(ideasOut).toContain("Ideas");
    expect(ideasOut).toContain("I001");
    expect(ideasOut).not.toContain("Bugs");
    expect(ideasOut).not.toContain("Phase");

    p = run(["list", "-a"], root);
    expect(p.exitCode).toBe(0);
    const availableOut = p.stdout.toString();
    expect(availableOut).toContain("Available Tasks");
  });

  test("list falls back to aux filename when title metadata is missing", () => {
    root = setupFixture();

    let p = run(["bug", "--title", "critical bug", "--simple"], root);
    expect(p.exitCode).toBe(0);
    p = run(["idea", "future planning idea"], root);
    expect(p.exitCode).toBe(0);

    const bugsIndexPath = join(root, ".tasks", "bugs", "index.yaml");
    const bugsIndex = parse(readFileSync(bugsIndexPath, "utf8")) as {
      bugs: Array<{ id?: string; file: string; title?: string }>;
    };
    delete bugsIndex.bugs[0]!.title;
    writeFileSync(bugsIndexPath, stringify(bugsIndex));

    const ideasIndexPath = join(root, ".tasks", "ideas", "index.yaml");
    const ideasIndex = parse(readFileSync(ideasIndexPath, "utf8")) as {
      ideas: Array<{ id?: string; file: string; title?: string }>;
    };
    delete ideasIndex.ideas[0]!.title;
    writeFileSync(ideasIndexPath, stringify(ideasIndex));

    writeFileSync(
      join(root, ".tasks", "bugs", bugsIndex.bugs[0]!.file),
      `---\nid: B001\nstatus: pending\nestimate_hours: 1\ncomplexity: medium\npriority: high\ndepends_on: []\ntags: []\n---\n`,
    );
    writeFileSync(
      join(root, ".tasks", "ideas", ideasIndex.ideas[0]!.file),
      `---\nid: I001\nstatus: pending\nestimate_hours: 1\ncomplexity: medium\npriority: high\ndepends_on: []\ntags: []\n---\n`,
    );

    p = run(["list"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("B001: critical bug");
    expect(out).toContain("I001: future planning idea");
  });

  test("grab can claim idea tasks", () => {
    root = setupFixture();
    const t001Path = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const t002Path = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");
    writeFileSync(t001Path, readFileSync(t001Path, "utf8").replace("status: pending", "status: done"));
    writeFileSync(t002Path, readFileSync(t002Path, "utf8").replace("status: pending", "status: done"));

    let p = run(["idea", "capture architecture idea"], root);
    expect(p.exitCode).toBe(0);

    p = run(["grab", "--single", "--agent", "agent-idea", "--no-content"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("I001");

    const indexText = readFileSync(join(root, ".tasks", "ideas", "index.yaml"), "utf8");
    const fileMatch = indexText.match(/file:\s*(\S+)/);
    expect(fileMatch).toBeTruthy();
    const ideaText = readFileSync(join(root, ".tasks", "ideas", fileMatch![1]!), "utf8");
    expect(ideaText).toContain("status: in_progress");
    expect(ideaText).toContain("claimed_by: agent-idea");
  });

  test("enhanced list command shows milestones with task counts", () => {
    root = setupFixture();
    let p = run(["list"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Phase (P1) (0/2 tasks done)");
    expect(out).toContain("M (P1.M1) (0/2 tasks done)");
  });

  test("list --all shows all milestones", () => {
    root = setupFixture();
    // Add more milestones to test truncation
    const t = join(root, ".tasks");
    for (let i = 2; i <= 7; i++) {
      mkdirSync(join(t, "01-phase", `0${i}-ms`), { recursive: true });
      writeFileSync(join(t, "01-phase", `0${i}-ms`, "index.yaml"), `epics: []\n`);
    }
    const phaseIndex = join(t, "01-phase", "index.yaml");
    let content = `milestones:\n  - id: M1\n    name: M\n    path: 01-ms\n`;
    for (let i = 2; i <= 7; i++) {
      content += `  - id: M${i}\n    name: M${i}\n    path: 0${i}-ms\n`;
    }
    writeFileSync(phaseIndex, content);

    let p = run(["list"], root);
    expect(p.exitCode).toBe(0);
    let out = p.stdout.toString();
    expect(out).toContain("... and 2 more milestones");

    p = run(["list", "--all"], root);
    expect(p.exitCode).toBe(0);
    out = p.stdout.toString();
    expect(out).not.toContain("... and");
    expect(out).toContain("M7");
  });

  test("list --unfinished filters completed items", () => {
    root = setupFixture();
    let p = run(["claim", "P1.M1.E1.T001", "--agent", "test"], root);
    expect(p.exitCode).toBe(0);
    p = run(["done", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);

    p = run(["list", "--unfinished"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    // Stats should show actual completion (1/2), not filtered view
    expect(out).toContain("Phase (P1) (1/2 tasks done)");
    expect(out).toContain("M (P1.M1) (1/2 tasks done)");
  });

  test("list --json includes milestone metadata", () => {
    root = setupFixture();
    let p = run(["list", "--json"], root);
    expect(p.exitCode).toBe(0);
    const data = JSON.parse(p.stdout.toString());
    expect(Array.isArray(data.phases)).toBeTrue();
    expect(data.phases[0].milestones).toBeDefined();
    expect(data.phases[0].milestones[0].stats).toBeDefined();
    expect(data.phases[0].milestones[0].stats.total).toBe(2);
  });

  test("list --progress shows phase and milestone IDs", () => {
    root = setupFixture();
    // Complete a task so the phase has progress and milestones are displayed
    let p = run(["claim", "P1.M1.E1.T001", "--agent", "test"], root);
    expect(p.exitCode).toBe(0);
    p = run(["done", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);

    p = run(["list", "--progress"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("P1: Phase");
    expect(out).toContain("P1.M1:");
    expect(out).toContain("Project Progress");
  });

  test("list --progress shows yellow in-progress bar segment", () => {
    root = setupFixture();
    let p = run(["claim", "P1.M1.E1.T001", "--agent", "test"], root);
    expect(p.exitCode).toBe(0);

    p = runWithColor(["list", "--progress"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("▓");
  });

  test("list --progress --unfinished filters completed items", () => {
    root = setupFixture();
    let p = run(["claim", "P1.M1.E1.T001", "--agent", "test"], root);
    expect(p.exitCode).toBe(0);
    p = run(["done", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);

    p = run(["list", "--progress", "--unfinished"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("P1: Phase");
  });

  test("tree command shows full 4-level hierarchy", () => {
    root = setupFixture();
    let p = run(["tree"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Phase");
    expect(out).toContain("M (0/2)");
    expect(out).toContain("E (0/2)");
    expect(out).toContain("P1.M1.E1.T001");
    expect(out).toContain("P1.M1.E1.T002");
    expect(out).toContain("├──");
    expect(out).toContain("└──");
  });

  test("tree --unfinished filters completed work", () => {
    root = setupFixture();
    let p = run(["claim", "P1.M1.E1.T001", "--agent", "test"], root);
    expect(p.exitCode).toBe(0);
    p = run(["done", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);

    p = run(["tree", "--unfinished"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).not.toContain("P1.M1.E1.T001");
    expect(out).toContain("P1.M1.E1.T002");
  });

  test("tree hides completed bugs by default and can include via flag", () => {
    root = setupFixture(true);
    const bugPath = join(root, ".tasks", "bugs", "B001-critical-bug.todo");
    let bugText = readFileSync(bugPath, "utf8");
    bugText = bugText.replace("status: pending", "status: done");
    writeFileSync(bugPath, bugText);

    let p = run(["tree"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).not.toContain("B001: Critical Bug");

    p = run(["tree", "--show-completed-aux"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("B001: Critical Bug");
  });

  test("tree --details shows metadata", () => {
    root = setupFixture();
    let p = run(["claim", "P1.M1.E1.T001", "--agent", "agent-x"], root);
    expect(p.exitCode).toBe(0);

    p = run(["tree", "--details"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("@agent-x");
    expect(out).toContain("(1h)");
    expect(out).toContain("[in_progress]");
    expect(out).toContain("depends:P1.M1.E1.T001");
  });

  test("tree --depth limits expansion correctly", () => {
    root = setupFixture();
    let p = run(["tree", "--depth", "1"], root);
    expect(p.exitCode).toBe(0);
    let out = p.stdout.toString();
    expect(out).toContain("Phase");
    expect(out).not.toContain("M (0/2)");

    p = run(["tree", "--depth", "2"], root);
    expect(p.exitCode).toBe(0);
    out = p.stdout.toString();
    expect(out).toContain("Phase");
    expect(out).toContain("M (0/2)");
    expect(out).not.toContain("E (0/2)");

    p = run(["tree", "--depth", "3"], root);
    expect(p.exitCode).toBe(0);
    out = p.stdout.toString();
    expect(out).toContain("Phase");
    expect(out).toContain("M (0/2)");
    expect(out).toContain("E (0/2)");
    expect(out).not.toContain("P1.M1.E1.T001");
  });

  test("tree --json outputs complete hierarchy", () => {
    root = setupFixture();
    let p = run(["tree", "--json"], root);
    expect(p.exitCode).toBe(0);
    const data = JSON.parse(p.stdout.toString());
    expect(data.max_depth).toBe(4);
    expect(data.show_details).toBe(false);
    expect(data.unfinished_only).toBe(false);
    expect(Array.isArray(data.phases)).toBeTrue();
    expect(data.phases[0].milestones[0].epics[0].tasks.length).toBe(2);
  });

  test("tree path query filters to matching task and ancestors", () => {
    root = setupFixture();
    let p = run(["tree", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Phase");
    expect(out).toContain("M (0/1)");
    expect(out).toContain("E (0/1)");
    expect(out).toContain("P1.M1.E1.T001");
    expect(out).not.toContain("P1.M1.E1.T002");
  });

  test("tree scoped path query hides bug and idea sections", () => {
    root = setupFixture(true);
    let p = run(["idea", "Root idea"], root);
    expect(p.exitCode).toBe(0);

    p = run(["tree", "P1.M1.E1"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).not.toContain("Bugs");
    expect(out).not.toContain("Ideas");
    expect(out).not.toContain("Root idea");
    expect(out).not.toContain("Critical Bug");
    expect(out).toContain("P1.M1.E1.T001");
  });

  test("tree path query wildcard keeps descendants", () => {
    root = setupFixture();
    let p = run(["tree", "P1.*"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("P1.M1.E1.T001");
    expect(out).toContain("P1.M1.E1.T002");
  });

  test("tree parses path query before --depth option", () => {
    root = setupFixture();
    let p = run(["tree", "P1.*", "--depth", "1"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Phase");
    expect(out).not.toContain("P1.M1.E1.T001");
  });

  test("tree path query with no match prints helpful message", () => {
    root = setupFixture();
    let p = run(["tree", "P9"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("No tree nodes found for path query: P9");
  });

  test("tree path query no match prints helpful message with aux items", () => {
    root = setupFixture(true);
    let p = run(["tree", "P9"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("No tree nodes found for path query: P9");
  });

  test("tree accepts multiple path query arguments", () => {
    root = setupFixture();
    let p = run(["tree", "P1.M1.E1.T001", "P1.M1.E1.T002"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("P1.M1.E1.T001");
    expect(out).toContain("P1.M1.E1.T002");
    expect(out).not.toContain("No tree nodes found for path query");
  });

  test("tree path query with no match returns empty json phases", () => {
    root = setupFixture();
    let p = run(["tree", "P9", "--json"], root);
    expect(p.exitCode).toBe(0);
    const data = JSON.parse(p.stdout.toString());
    expect(Array.isArray(data.phases)).toBeTrue();
    expect(data.phases.length).toBe(0);
  });

  test("tree handles empty epics gracefully", () => {
    root = setupFixture();
    const t = join(root, ".tasks");
    mkdirSync(join(t, "01-phase", "01-ms", "02-empty"), { recursive: true });
    writeFileSync(join(t, "01-phase", "01-ms", "02-empty", "index.yaml"), `tasks: []\n`);
    const msIndex = join(t, "01-phase", "01-ms", "index.yaml");
    writeFileSync(
      msIndex,
      `epics:\n  - id: E1\n    name: E\n    path: 01-epic\n  - id: E2\n    name: Empty\n    path: 02-empty\n`,
    );

    let p = run(["tree"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Empty (0/0)");
  });

  test("list and tree show consistent task counts", () => {
    root = setupFixture();
    let listOut = run(["list"], root);
    expect(listOut.exitCode).toBe(0);
    let treeOut = run(["tree"], root);
    expect(treeOut.exitCode).toBe(0);

    const listStr = listOut.stdout.toString();
    const treeStr = treeOut.stdout.toString();

    expect(listStr).toContain("(0/2 tasks done)");
    expect(treeStr).toContain("(0/2)");
  });

  test("log command supports json and text output", () => {
    root = setupFixture();
    const now = Date.now();
    const task1Path = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const task2Path = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");

    updateTodoFrontmatter(task1Path, (frontmatter) => {
      frontmatter.status = "done";
      frontmatter.title = "Task One";
      frontmatter.claimed_by = "agent-a";
      frontmatter.claimed_at = new Date(now - 3 * 60 * 60 * 1000).toISOString();
      frontmatter.started_at = new Date(now - (2.5 * 60 * 60 * 1000)).toISOString();
      frontmatter.completed_at = new Date(now - 60 * 60 * 1000).toISOString();
    });
    updateTodoFrontmatter(task2Path, (frontmatter) => {
      frontmatter.status = "in_progress";
      frontmatter.title = "Task Two";
      frontmatter.claimed_by = "agent-b";
      frontmatter.claimed_at = new Date(now - 90 * 60 * 1000).toISOString();
      frontmatter.started_at = new Date(now - 45 * 60 * 1000).toISOString();
    });

    let p = run(["log", "--json"], root);
    expect(p.exitCode).toBe(0);
    const events = JSON.parse(p.stdout.toString());
    expect(Array.isArray(events)).toBeTrue();
    expect(events[0].task_id).toBe("P1.M1.E1.T002");
    expect(events[0].event).toBe("started");
    expect(events[0].actor).toBe("agent-b");
    expect(events[0].kind).toBe("started");
    expect(events[1].task_id).toBe("P1.M1.E1.T001");
    expect(events[1].event).toBe("completed");
    expect(events[1].actor).toBe("agent-a");
    expect(events[1].kind).toBe("completed");
    expect(events.some((item: { event: string }) => item.event === "claimed")).toBeTrue();

    p = run(["log", "--limit", "2"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Recent Activity Log");
    expect(out).toContain("✓");
    expect(out).toContain("P1.M1.E1.T001");
    expect(out).toContain("agent-a");
    expect(out).toContain("[completed]");
  });

  test("log command includes added events without timestamps", () => {
    root = setupFixture();
    let p = run(["log", "--json"], root);
    expect(p.exitCode).toBe(0);

    const events = JSON.parse(p.stdout.toString());
    expect(Array.isArray(events)).toBeTrue();
    expect(events).toHaveLength(2);
    expect(events.every((event: { event: string }) => event.event === "added")).toBeTrue();
    expect(events.every((event: { kind: string }) => event.kind === "created")).toBeTrue();
    expect(events.every((event: { actor: string | null }) => event.actor === null)).toBeTrue();
    expect(events.every((event: { timestamp: string }) => typeof event.timestamp === "string")).toBeTrue();
  });

  test("log command filters only bugs", () => {
    root = setupFixture(true);

    let p = run(["log", "--json", "--bugs"], root);
    expect(p.exitCode).toBe(0);
    const events = JSON.parse(p.stdout.toString());
    expect(Array.isArray(events)).toBeTrue();
    expect(events).toHaveLength(1);
    expect(events[0].task_id).toBe("B001");
    expect(events[0].event).toBe("added");
  });

  test("log command filters only ideas", () => {
    root = setupFixture();
    const idea = run(["idea", "refactor startup"], root);
    expect(idea.exitCode).toBe(0);

    const p = run(["log", "--json", "--ideas"], root);
    expect(p.exitCode).toBe(0);
    const events = JSON.parse(p.stdout.toString());
    expect(Array.isArray(events)).toBeTrue();
    expect(events).toHaveLength(1);
    expect(events[0].task_id).toBe("I001");
    expect(events[0].event).toBe("added");
  });

  test("log command can show bugs and ideas together", () => {
    root = setupFixture(true);
    const idea = run(["idea", "capture future direction"], root);
    expect(idea.exitCode).toBe(0);

    const p = run(["log", "--json", "--bugs", "--ideas"], root);
    expect(p.exitCode).toBe(0);
    const events = JSON.parse(p.stdout.toString());
    expect(events).toHaveLength(2);
    const taskIds = new Set(events.map((event: { task_id: string }) => event.task_id));
    expect(taskIds.has("B001")).toBeTrue();
    expect(taskIds.has("I001")).toBeTrue();
  });

  test("show on pending idea displays Instructions section", () => {
    root = setupFixture();
    let p = run(["idea", "refactor auth module"], root);
    expect(p.exitCode).toBe(0);

    p = run(["show", "I001"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("Instructions:");
    expect(out).toContain("Read the idea file at");
    expect(out).toContain("tasks add-phase");
    expect(out).toContain("tasks add-milestone");
    expect(out).toContain("tasks add-epic");
    expect(out).toContain("tasks add");
    expect(out).toContain("Created Work Items");
    expect(out).toContain("Mark this idea as done");
  });

  test("show missing phase includes tree hint", () => {
    root = setupFixture();
    const p = run(["show", "P9"], root);
    const output = `${p.stdout.toString()}${p.stderr.toString()}`;
    expect(p.exitCode).not.toBe(0);
    expect(output).toContain("Phase not found: P9");
    expect(output).toContain("Tip: Use 'backlog tree' to list available IDs.");
  });

  test("show missing task includes scoped tree hint", () => {
    root = setupFixture();
    const p = run(["show", "P1.M1.E1.T999"], root);
    const output = `${p.stdout.toString()}${p.stderr.toString()}`;
    expect(p.exitCode).not.toBe(0);
    expect(output).toContain("Task not found: P1.M1.E1.T999");
    expect(output).toContain("Tip: Use 'backlog tree P1.M1.E1' to verify available IDs.");
  });

  test("show displays explicit dependencies with legend", () => {
    root = setupFixture();
    const task1Path = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    updateTodoFrontmatter(task1Path, (frontmatter) => {
      frontmatter.status = "done";
    });

    const p = run(["show", "P1.M1.E1.T002"], root);
    expect(p.exitCode).toBe(0);
    const output = p.stdout.toString();
    expect(output).toContain("Explicit dependencies:");
    expect(output).toContain("✓ P1.M1.E1.T001");
    expect(output).toContain("Legend: ✓ done | ✗ not done | ? not found");
    expect(output).not.toContain("Implicit dependency");
  });

  test("show displays implicit previous-task dependency with legend", () => {
    root = setupFixture();
    const task2Path = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");
    updateTodoFrontmatter(task2Path, (frontmatter) => {
      frontmatter.depends_on = [];
    });

    const p = run(["show", "P1.M1.E1.T002"], root);
    expect(p.exitCode).toBe(0);
    const output = p.stdout.toString();
    expect(output).toContain("Implicit dependency (previous in epic):");
    expect(output).toContain("✗ P1.M1.E1.T001");
    expect(output).toContain("Legend: ✓ done | ✗ not done | ? not found");
    expect(output).not.toContain("Explicit dependencies:");
  });

  test("show phase displays total duration", () => {
    root = setupFixture();
    const p = run(["show", "P1"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Total Duration: 3.00h");
  });

  test("show milestone displays total duration", () => {
    root = setupFixture();
    const p = run(["show", "P1.M1"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Total Duration: 3.00h");
  });

  test("show epic displays total duration", () => {
    root = setupFixture();
    const p = run(["show", "P1.M1.E1"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("Total Duration: 3.00h");
  });

  test("show task previews body and truncates by default", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const relativeTaskPath = taskPath.replace(`${root}/`, "");
    const content = readFileSync(taskPath, "utf8");
    const markerIndex = content.indexOf("---\n", 4);
    expect(markerIndex).toBeGreaterThanOrEqual(0);
    const frontmatterBlock = content.slice(0, markerIndex + 4);
    const previewBody = [
      "# A",
      "",
      "Line 1",
      "Line 2",
      "Line 3",
      "Line 4",
      "Line 5",
      "Line 6",
      "Line 7",
      "Line 8",
      "Line 9",
      "Line 10",
      "Line 11",
      "Line 12",
    ].join("\n");
    writeFileSync(taskPath, `${frontmatterBlock}${previewBody}\n`);
    const taskContent = readFileSync(taskPath, "utf8");
    const taskLineCount =
      taskContent === "" ? 0 : taskContent.replace(/\r?\n$/, "").split(/\r?\n/).length;
    const taskSize = Buffer.byteLength(taskContent, "utf8");

    const p = run(["show", "P1.M1.E1.T001"], root);
    expect(p.exitCode).toBe(0);
    const output = p.stdout.toString();
    expect(output).toContain(`File stats: ${taskSize} bytes, ${taskLineCount} lines`);
    expect(output).toContain("Body:");
    expect(output).toContain("Line 10");
    expect(output).not.toContain("Line 11");
    expect(output).toContain("  ... (2 more lines)");
    expect(output).toContain(`To view the full file, run: cat ${relativeTaskPath}`);
  });

  test("show --long prints the entire task body", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const content = readFileSync(taskPath, "utf8");
    const markerIndex = content.indexOf("---\n", 4);
    expect(markerIndex).toBeGreaterThanOrEqual(0);
    const frontmatterBlock = content.slice(0, markerIndex + 4);
    const previewBody = [
      "# A",
      "",
      "Line 1",
      "Line 2",
      "Line 3",
      "Line 4",
      "Line 5",
      "Line 6",
      "Line 7",
      "Line 8",
      "Line 9",
      "Line 10",
      "Line 11",
      "Line 12",
    ].join("\n");
    writeFileSync(taskPath, `${frontmatterBlock}${previewBody}\n`);
    const taskContent = readFileSync(taskPath, "utf8");
    const taskLineCount =
      taskContent === "" ? 0 : taskContent.replace(/\r?\n$/, "").split(/\r?\n/).length;
    const taskSize = Buffer.byteLength(taskContent, "utf8");

    const p = run(["show", "P1.M1.E1.T001", "--long"], root);
    expect(p.exitCode).toBe(0);
    const output = p.stdout.toString();
    expect(output).toContain(`File stats: ${taskSize} bytes, ${taskLineCount} lines`);
    expect(output).toContain("Body:");
    expect(output).toContain("Line 11");
    expect(output).toContain("Line 12");
    expect(output).not.toContain("  ... (2 more lines)");
    expect(output).not.toContain("To view the full file, run: cat");
  });

  test("show --all prints the complete task file", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const content = readFileSync(taskPath, "utf8");
    const markerIndex = content.indexOf("---\n", 4);
    expect(markerIndex).toBeGreaterThanOrEqual(0);
    const frontmatterBlock = content.slice(0, markerIndex + 4);
    const fullBody = [
      "# A",
      "",
      "Line 1",
      "Line 2",
      "Line 3",
      "Line 4",
      "Line 5",
      "Line 6",
      "Line 7",
      "Line 8",
      "Line 9",
      "Line 10",
      "Line 11",
      "Line 12",
    ].join("\n");
    writeFileSync(taskPath, `${frontmatterBlock}${fullBody}\n`);
    const taskContent = readFileSync(taskPath, "utf8");

    const p = run(["show", "P1.M1.E1.T001", "--all"], root);
    expect(p.exitCode).toBe(0);
    const output = p.stdout.toString();
    expect(output).toContain("Task file:");
    expect(output).toContain(`id: P1.M1.E1.T001`);
    expect(output).toContain("Line 12");
    expect(output).toContain(`File: ${join(".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo")}`);
    expect(output).not.toContain("  ... (2 more lines)");
    expect(output).not.toContain("To view the full file, run: cat");
    expect(output).not.toContain("Body:");
  });

  test("cat prints a single complete task file", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");

    const p = run(["cat", "P1.M1.E1.T001"], root);

    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toBe(readFileSync(taskPath, "utf8"));
  });

  test("cat prints multiple task files with fancy headers between files", () => {
    root = setupFixture();
    const firstPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const secondPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");

    const p = run(["cat", "P1.M1.E1.T001", "P1.M1.E1.T002"], root);

    const expected = [
      readFileSync(firstPath, "utf8"),
      "-------------------- [ P1.M1.E1.T002 ] --------------------\n" + readFileSync(secondPath, "utf8"),
    ].join("\n");
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toBe(expected);
  });

  test("cat preserves a blank line before headers after files without trailing newlines", () => {
    root = setupFixture();
    const firstPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const secondPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");
    const firstRaw = readFileSync(firstPath, "utf8").replace(/\n+$/, "");
    writeFileSync(firstPath, firstRaw);

    const p = run(["cat", "P1.M1.E1.T001", "P1.M1.E1.T002"], root);

    const expected =
      firstRaw +
      "\n\n-------------------- [ P1.M1.E1.T002 ] --------------------\n" +
      readFileSync(secondPath, "utf8");
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toBe(expected);
  });

  test("cat prints malformed normal task file verbatim", () => {
    root = setupFixture();
    const taskPath = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const raw = "---\n:\n---\n# Raw\n";
    writeFileSync(taskPath, raw);

    const p = run(["cat", "P1.M1.E1.T001"], root);

    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toBe(raw);
  });

  test("cat prints auxiliary bug task file", () => {
    root = setupFixture(true);
    const bugPath = join(root, ".tasks", "bugs", "B001-critical-bug.todo");

    const p = run(["cat", "B001"], root);

    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toBe(readFileSync(bugPath, "utf8"));
  });

  test("cat prints auxiliary idea task file", () => {
    root = setupFixture();
    const created = run(["idea", "capture planning backlog"], root);
    expect(created.exitCode).toBe(0);
    const ideasIndex = parse(readFileSync(join(root, ".tasks", "ideas", "index.yaml"), "utf8")) as {
      ideas: { id: string; file: string }[];
    };
    const idea = ideasIndex.ideas[0]!;
    const ideaPath = join(root, ".tasks", "ideas", idea.file);

    const p = run(["cat", idea.id], root);

    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toBe(readFileSync(ideaPath, "utf8"));
  });

  test("cat prints malformed fixed task file verbatim", () => {
    root = setupFixture();
    const fixesDir = join(root, ".tasks", "fixes", "2026-02");
    mkdirSync(fixesDir, { recursive: true });
    writeFileSync(
      join(root, ".tasks", "fixes", "index.yaml"),
      stringify({
        fixes: [
          {
            id: "F001",
            file: "2026-02/F001-ship-patch.todo",
          },
        ],
      }),
    );
    const raw = "---\n:\n---\n# Ship patch\nRaw body survives.\n";
    writeFileSync(join(fixesDir, "F001-ship-patch.todo"), raw);

    const p = run(["cat", "F001"], root);

    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toBe(raw);
  });

  test("cat missing task includes scoped tree hint", () => {
    root = setupFixture();

    const p = run(["cat", "P1.M1.E1.T999"], root);

    const out = p.stdout.toString() + p.stderr.toString();
    expect(p.exitCode).not.toBe(0);
    expect(out).toContain("Task not found: P1.M1.E1.T999");
    expect(out).toContain("Tip: Use 'backlog tree P1.M1.E1' to verify available IDs.");
  });

  test("show on non-pending idea hides Instructions section", () => {
    root = setupFixture();
    // Complete all normal tasks so grab picks the idea
    const t001Path = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo");
    const t002Path = join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo");
    writeFileSync(t001Path, readFileSync(t001Path, "utf8").replace("status: pending", "status: done"));
    writeFileSync(t002Path, readFileSync(t002Path, "utf8").replace("status: pending", "status: done"));

    let p = run(["idea", "refactor auth module"], root);
    expect(p.exitCode).toBe(0);

    // Claim it to change status to in_progress
    p = run(["grab", "--single", "--agent", "test-agent", "--no-content"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).toContain("I001");

    p = run(["show", "I001"], root);
    expect(p.exitCode).toBe(0);
    expect(p.stdout.toString()).not.toContain("Instructions:");
  });

  test("ls lists all phases", () => {
    root = setupFixture();
    const out = run(["ls"], root).stdout.toString();

    expect(out).toContain("P1: Phase");
    expect(out).toContain("0/2 tasks done");
  });

  test("ls root includes bug, idea, and fixed summaries", () => {
    root = setupFixture();
    let p = run(["bug", "restore integration auth state"], root);
    expect(p.exitCode).toBe(0);
    p = run(["idea", "plan cache invalidation"], root);
    expect(p.exitCode).toBe(0);
    p = run(
      [
        "fixed",
        "--title",
        "restore stale auth token",
        "--at",
        "2026-02-20T12:00:00Z",
        "--body",
        "Added regression guard.",
      ],
      root,
    );
    expect(p.exitCode).toBe(0);

    const out = run(["ls"], root).stdout.toString();
    expect(out).toContain("Bugs (0/1)");
    expect(out).toContain("Ideas (0/1)");
    expect(out).toContain("Fixes (1/1)");
  });

  test("ls lists milestones for a phase", () => {
    root = setupFixture();
    const out = run(["ls", "P1"], root).stdout.toString();

    expect(out).toContain("P1.M1: M");
    expect(out).toContain("0/2 tasks done");
  });

  test("ls lists epics for a milestone", () => {
    root = setupFixture();
    const out = run(["ls", "P1.M1"], root).stdout.toString();

    expect(out).toContain("P1.M1.E1: E");
    expect(out).toContain("0/2 tasks done");
  });

  test("ls lists tasks for an epic", () => {
    root = setupFixture();
    const out = run(["ls", "P1.M1.E1"], root).stdout.toString();

    expect(out).toContain("P1.M1.E1.T001: A [pending] 1h");
    expect(out).toContain("P1.M1.E1.T002: B [pending] 2h");
  });

  test("ls task alias prints frontmatter summary and show hint", () => {
    root = setupFixture();
    const out = run(["ls", "P1.M1.E1.T001"], root).stdout.toString();

    expect(out).toContain("Task: P1.M1.E1.T001 - A");
    expect(out).toContain("id: P1.M1.E1.T001");
    expect(out).toContain("title: A");
    expect(out).toContain("Body length:");
    expect(out).toContain("Run 'backlog show P1.M1.E1.T001' for full details.");
  });

  test("ls accepts multiple scopes and fails strictly for invalid scope", () => {
    root = setupFixture();
    let p = run(["ls", "P1", "P1.M1"], root);
    expect(p.exitCode).toBe(0);
    const out = p.stdout.toString();
    expect(out).toContain("P1.M1: M");
    expect(out).toContain("P1.M1.E1: E");

    p = run(["ls", "P1", "P9"], root);
    expect(p.exitCode).toBe(1);
    expect(`${p.stdout.toString()}${p.stderr.toString()}`).toContain("Phase not found: P9");
  });

  test("fixed command captures metadata in todo and index", () => {
    root = setupFixture();
    const result = run(
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
      root,
    );

    expect(result.exitCode).toBe(0);
    const output = `${result.stdout.toString()}${result.stderr.toString()}`;
    expect(output).toContain("Created fixed:");
    expect(output).toContain("F001");

    const indexPath = join(root, ".tasks", "fixes", "index.yaml");
    const index = parse(readFileSync(indexPath, "utf8")) as {
      fixes: { id: string; file: string }[];
    };
    expect(Array.isArray(index.fixes)).toBeTrue();
    expect(index.fixes.length).toBe(1);
    expect(index.fixes[0]).toMatchObject({
      id: "F001",
      file: "2026-02/F001-restore-stale-auth-token.todo",
    });

    const fixPath = join(root, ".tasks", "fixes", index.fixes[0].file);
    const fixText = readFileSync(fixPath, "utf8");
    expect(fixText).toContain("type: fixed");
    expect(fixText).toContain("title: restore stale auth token");
    expect(fixText).toContain("description: Restore token handling to avoid forced logouts");
    expect(fixText).toContain("status: done");
    expect(fixText).toContain("created_at: 2026-02-20T12:00:00.000Z");
    expect(fixText).toContain("completed_at: 2026-02-20T12:00:00.000Z");
    expect(fixText).toContain("- auth");
    expect(fixText).toContain("- hotfix");
    expect(fixText).toContain("Added regression guard for token refresh.");
  });

  test("fixed rejects single-word title", () => {
    root = setupFixture();
    const result = run(["fixed", "patch"], root);
    expect(result.exitCode).toBe(1);
    expect(`${result.stdout.toString()}${result.stderr.toString()}`).toContain(
      "at least two words",
    );
  });

  test("fixed rejects single-word --title", () => {
    root = setupFixture();
    const result = run(["fixed", "--title", "fix"], root);
    expect(result.exitCode).toBe(1);
    expect(`${result.stdout.toString()}${result.stderr.toString()}`).toContain(
      "at least two words",
    );
  });

  test("show displays fixed task details", () => {
    root = setupFixture();
    const fixed = run(
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
      root,
    );
    expect(fixed.exitCode).toBe(0);

    const show = run(["show", "F001"], root);
    expect(show.exitCode).toBe(0);
    const output = show.stdout.toString();
    expect(output).toContain("F001");
    expect(output).toContain("restore stale auth token");
    expect(output).toContain("status=done");
  });

  test("benchmark command returns timing summary and text report", () => {
    root = setupFixture();
    rmSync(join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo"));

    const jsonOut = run(["benchmark", "--json"], root);
    expect(jsonOut.exitCode).toBe(0);
    const payload = JSON.parse(jsonOut.stdout.toString());

    expect(payload.summary.node_counts.phases).toBe(1);
    expect(payload.summary.node_counts.milestones).toBe(1);
    expect(payload.summary.node_counts.epics).toBe(1);
    expect(payload.summary.task_files_total).toBe(2);
    expect(payload.summary.task_files_found).toBe(1);
    expect(payload.summary.task_files_missing).toBe(1);
    expect(payload.summary.parse_mode).toBe("full");
    expect(payload.summary.parse_task_body).toBeTrue();
    expect(payload.summary.index_parse_ms).toBeGreaterThanOrEqual(0);
    expect(payload.summary.task_frontmatter_parse_ms).toBeGreaterThanOrEqual(0);
    expect(payload.summary.task_body_parse_ms).toBeGreaterThanOrEqual(0);
    expect(payload.summary.task_parse_other_ms).toBeGreaterThanOrEqual(0);

    const jsonNoBody = run(["benchmark", "--mode", "full", "--no-parse-body", "--json"], root);
    expect(jsonNoBody.exitCode).toBe(0);
    const noBodyPayload = JSON.parse(jsonNoBody.stdout.toString());
    expect(noBodyPayload.summary.parse_mode).toBe("full");
    expect(noBodyPayload.summary.parse_task_body).toBeFalse();
    expect(noBodyPayload.summary.task_body_parse_ms).toBe(0);

    const jsonMetadata = run(["benchmark", "--mode", "metadata", "--json"], root);
    expect(jsonMetadata.exitCode).toBe(0);
    const metadataPayload = JSON.parse(jsonMetadata.stdout.toString());
    expect(metadataPayload.summary.parse_mode).toBe("metadata");
    expect(metadataPayload.summary.parse_task_body).toBeFalse();

    const jsonIndex = run(["benchmark", "--mode", "index", "--json"], root);
    expect(jsonIndex.exitCode).toBe(0);
    const indexPayload = JSON.parse(jsonIndex.stdout.toString());
    expect(indexPayload.summary.parse_mode).toBe("index");
    expect(indexPayload.summary.parse_task_body).toBeFalse();
    expect(indexPayload.summary.task_files_total).toBe(2);
    expect(indexPayload.summary.task_files_found).toBe(2);
    expect(indexPayload.summary.task_files_missing).toBe(0);

    const textOut = run(["benchmark"], root);
    expect(textOut.exitCode).toBe(0);
    const output = textOut.stdout.toString();
    expect(output).toContain("Task Tree Benchmark");
    expect(output).toContain("Slowest phases");
    expect(output).toContain("Files by type");
    expect(output).toContain("Index parse time");
    expect(output).toContain("Task frontmatter parse time");
    expect(output).toContain("Task body parse time");
    expect(output).toContain("Parse mode");
  });
});
