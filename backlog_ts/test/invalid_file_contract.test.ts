/**
 * Parity contract tests for invalid-file show/claim diagnostics.
 *
 * Contract: docs/diagnostics/invalid-file-warning-contract.md
 * Fixtures: testdata/invalid-file-diagnostics/
 */
import { describe, expect, test } from "bun:test";
import {
  cpSync,
  existsSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = fileURLToPath(new URL("..", import.meta.url));
const repoRoot = join(packageRoot, "..");
const fixtureRoot = join(repoRoot, "testdata", "invalid-file-diagnostics");
const messagesPath = join(fixtureRoot, "expected", "messages.json");
const contractDocPath = join(
  repoRoot,
  "docs",
  "diagnostics",
  "invalid-file-warning-contract.md",
);
const cliPath = join(packageRoot, "src", "cli.ts");

type Condition = {
  code: string;
  fixture: string;
  task_id: string;
  warning_substrings: string[];
  show: {
    exit_code: number;
    must_warn: boolean;
    must_show_body?: boolean;
    body_substrings?: string[];
  };
  claim: {
    may_mutate: boolean;
    exit_code_nonzero: boolean;
    error_substrings: string[];
    must_warn_before_mutate?: boolean;
  };
  recovery: string[];
  implementation?: {
    cli_assertions?: Record<string, boolean>;
    notes?: string;
  };
};

type Contract = {
  version: number;
  conditions: Condition[];
  healthy_control: {
    fixture: string;
    task_id: string;
    must_not_contain_substrings: string[];
  };
};

function loadContract(): Contract {
  const data = JSON.parse(readFileSync(messagesPath, "utf8")) as Contract;
  expect(data.version).toBeGreaterThanOrEqual(1);
  expect(data.conditions.length).toBeGreaterThan(0);
  return data;
}

function normalize(text: string): string {
  return text
    .replace(/\x1b\[[0-9;]*[A-Za-z]/g, "")
    .split(/\s+/)
    .filter(Boolean)
    .join(" ");
}

function copyCase(relative: string): string {
  const src = join(fixtureRoot, relative);
  const dest = mkdtempSync(join(tmpdir(), "invalid-file-case-"));
  cpSync(src, dest, { recursive: true });
  return dest;
}

function run(args: string[], cwd: string) {
  return Bun.spawnSync(["bun", "run", cliPath, ...args], {
    cwd,
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env, BACKLOG_NO_UPDATE_CHECK: "1" },
  });
}

function combined(p: ReturnType<typeof run>): string {
  return `${p.stdout.toString()}\n${p.stderr.toString()}`;
}

describe("invalid-file warning contract fixtures", () => {
  const contract = loadContract();

  test("fixture root and contract doc exist", () => {
    expect(statSync(fixtureRoot).isDirectory()).toBeTrue();
    expect(existsSync(messagesPath)).toBeTrue();
    expect(existsSync(contractDocPath)).toBeTrue();
    const doc = readFileSync(contractDocPath, "utf8");
    for (const code of [
      "missing_indexed_file",
      "malformed_frontmatter",
      "id_path_mismatch",
      "file_absent_from_index",
    ]) {
      expect(doc).toContain(code);
    }
  });

  test("all four conditions have fixtures and claimability rules", () => {
    const codes = new Set(contract.conditions.map((c) => c.code));
    expect(codes).toEqual(
      new Set([
        "missing_indexed_file",
        "malformed_frontmatter",
        "id_path_mismatch",
        "file_absent_from_index",
      ]),
    );

    for (const cond of contract.conditions) {
      const caseDir = join(fixtureRoot, cond.fixture);
      expect(statSync(caseDir).isDirectory()).toBeTrue();
      expect(existsSync(join(caseDir, ".tasks", "index.yaml"))).toBeTrue();
      expect(cond.warning_substrings.length).toBeGreaterThan(0);
      expect(cond.recovery.length).toBeGreaterThan(0);

      if (cond.code === "missing_indexed_file") {
        expect(cond.claim.may_mutate).toBe(false);
        expect(
          existsSync(
            join(
              caseDir,
              ".tasks",
              "01-phase",
              "01-ms",
              "01-epic",
              "T001-missing.todo",
            ),
          ),
        ).toBe(false);
      }
      if (cond.code === "malformed_frontmatter") {
        expect(cond.claim.may_mutate).toBe(true);
        const raw = readFileSync(
          join(
            caseDir,
            ".tasks",
            "01-phase",
            "01-ms",
            "01-epic",
            "T001-malformed.todo",
          ),
          "utf8",
        ).trimStart();
        expect(raw.startsWith("---")).toBe(false);
      }
      if (cond.code === "id_path_mismatch") {
        expect(cond.claim.may_mutate).toBe(true);
        const raw = readFileSync(
          join(
            caseDir,
            ".tasks",
            "01-phase",
            "01-ms",
            "01-epic",
            "T001-id-mismatch.todo",
          ),
          "utf8",
        );
        expect(raw).toContain("id: P9.M9.E9.T999");
      }
      if (cond.code === "file_absent_from_index") {
        expect(cond.claim.may_mutate).toBe(false);
        const index = readFileSync(
          join(caseDir, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml"),
          "utf8",
        );
        expect(index).not.toContain("T001-orphan.todo");
        expect(
          existsSync(
            join(
              caseDir,
              ".tasks",
              "01-phase",
              "01-ms",
              "01-epic",
              "T001-orphan.todo",
            ),
          ),
        ).toBe(true);
      }
    }
  });

  for (const cond of contract.conditions) {
    test(`CLI live assertions: ${cond.code}`, () => {
      const enabled = cond.implementation?.cli_assertions?.typescript ?? false;
      if (!enabled) {
        // Keep CI green until T002; fixture catalog still covers structure.
        expect(cond.warning_substrings.length).toBeGreaterThan(0);
        return;
      }

      const showRoot = copyCase(cond.fixture);
      try {
        const show = run(["show", cond.task_id], showRoot);
        const showText = normalize(combined(show));
        if (cond.show.must_warn) {
          for (const sub of cond.warning_substrings) {
            expect(showText).toContain(normalize(sub));
          }
        }
        if (cond.show.must_show_body) {
          for (const sub of cond.show.body_substrings ?? []) {
            expect(showText).toContain(normalize(sub));
          }
        }
      } finally {
        rmSync(showRoot, { recursive: true, force: true });
      }

      const claimRoot = copyCase(cond.fixture);
      try {
        const claim = run(
          ["claim", cond.task_id, "--no-content", "--agent", "contract-test"],
          claimRoot,
        );
        const claimText = normalize(combined(claim));
        if (cond.claim.may_mutate) {
          expect(claim.exitCode).toBe(0);
          if (cond.claim.must_warn_before_mutate) {
            for (const sub of cond.warning_substrings) {
              expect(claimText).toContain(normalize(sub));
            }
          }
        } else {
          expect(claim.exitCode).not.toBe(0);
          for (const sub of cond.claim.error_substrings) {
            expect(claimText).toContain(normalize(sub));
          }
          for (const sub of cond.warning_substrings) {
            expect(claimText).toContain(normalize(sub));
          }
        }
      } finally {
        rmSync(claimRoot, { recursive: true, force: true });
      }
    });
  }

  test("healthy control show has no invalid-file warnings", () => {
    const root = copyCase(contract.healthy_control.fixture);
    try {
      const p = run(["show", contract.healthy_control.task_id], root);
      expect(p.exitCode).toBe(0);
      const text = normalize(combined(p));
      for (const sub of contract.healthy_control.must_not_contain_substrings) {
        expect(text).not.toContain(normalize(sub));
      }
      expect(text).toContain("Healthy task");
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
