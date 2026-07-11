import { 
  existsSync,
  lstatSync,
  readFileSync,
  readlinkSync,
  renameSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { join, resolve } from "node:path";
import { stdout, stdin } from "node:process";

export const BACKLOG_DIR = ".backlog";
export const TASKS_DIR = ".tasks";

export const MIGRATION_COMMENT = "<!-- CLI migrated: 'tasks' → 'backlog' (alias 'bl' also works). -->\n";
export const BACKLOG_AGENTS_BOOTSTRAP = `# AGENTS.md\n\n## Agent workflow\n\n- Always use \`bl\` to create and modify backlog tasks.\n- Use \`bl --help\` before acting if you need command-level guidance.\n- Run \`bl howto\` to review workflow expectations.\n`;

export const KNOWN_COMMANDS = [
  "list", "ls", "show", "cat", "next", "claim", "grab", "done", "cycle", "work", "update", "sync", "check",
  "unclaim-stale", "add", "add-epic", "add-milestone", "add-phase", "move", "idea", "bug",
  "fixed",
  "blocked", "skip", "unclaim", "handoff", "why", "dash", "search", "blockers",
  "timeline", "tl", "session", "report", "velocity", "data", "edit", "schema", "skills", "version", "migrate",
];

export function getDataDir(): string {
  let current = resolve(process.cwd());
  let firstTasksDir: string | null = null;

  while (true) {
    const backlogPath = join(current, BACKLOG_DIR);
    if (existsSync(backlogPath)) {
      ensureBacklogAgents(backlogPath);
      return backlogPath;
    }

    if (firstTasksDir === null) {
      const tasksPath = join(current, TASKS_DIR);
      if (existsSync(tasksPath)) {
        firstTasksDir = tasksPath;
      }
    }

    const parent = resolve(current, "..");
    if (parent === current) break;
    current = parent;
  }

  if (firstTasksDir !== null) {
    return firstTasksDir;
  }

  throw new Error(`No data directory found. Expected ${BACKLOG_DIR}/ or ${TASKS_DIR}/`);
}

export function getDataDirName(): string {
  return getDataDir();
}

export function needsMigration(): boolean {
  return existsSync(TASKS_DIR) && !existsSync(BACKLOG_DIR);
}

export function isSymlinkTo(path: string, target: string): boolean {
  try {
    if (!lstatSync(path).isSymbolicLink()) return false;
    const linkTarget = readlinkSync(path);
    if (linkTarget === target) return true;
    return resolve(join(path, "..", linkTarget)) === resolve(target);
  } catch {
    return false;
  }
}

export function ensureBacklogAgents(backlogDir: string): void {
  const agentsPath = join(backlogDir, "AGENTS.md");
  if (!existsSync(agentsPath)) {
    writeFileSync(agentsPath, BACKLOG_AGENTS_BOOTSTRAP);
  }

  const claudePath = join(backlogDir, "CLAUDE.md");
  if (isSymlinkTo(claudePath, agentsPath)) {
    return;
  }
  rmSync(claudePath, { force: true, recursive: true });
  symlinkSync("AGENTS.md", claudePath);
}

export function isInteractive(): boolean {
  return stdin.isTTY && stdout.isTTY;
}

export function updateMdFile(filePath: string): boolean {
  if (!existsSync(filePath)) return false;
  
  let content: string;
  try {
    content = readFileSync(filePath, "utf8");
  } catch {
    return false;
  }
  
  if (content.includes(MIGRATION_COMMENT)) return false;
  
  const lines = content.split("\n");
  const newLines = lines.map((line) => {
    let newLine = line;
    
    for (const cmd of KNOWN_COMMANDS) {
      newLine = newLine.replace(new RegExp(`\`tasks ${cmd}`, "g"), `\`backlog ${cmd}`);
      newLine = newLine.replace(new RegExp(`    tasks ${cmd}`, "g"), `    backlog ${cmd}`);
      newLine = newLine.replace(new RegExp(`- tasks ${cmd}`, "g"), `- backlog ${cmd}`);
    }
    
    newLine = newLine.replace(/\`tasks --/g, "`backlog --");
    newLine = newLine.replace(/\`tasks \[/g, "`backlog [");
    newLine = newLine.replace(/python -m tasks/g, "python -m backlog");
    newLine = newLine.replace(/\.\/tasks\.py/g, "./backlog.py");
    newLine = newLine.replace(/\`tasks\/\`/g, "`backlog/`");
    newLine = newLine.replace(/"tasks\//g, '"backlog/');
    
    return newLine;
  });
  
  const newContent = newLines.join("\n");
  
  if (newContent !== content) {
    writeFileSync(filePath, MIGRATION_COMMENT + newContent);
    return true;
  }
  
  return false;
}

export interface MigrationResult {
  success: boolean;
  message: string;
}

export function migrateDataDir(createSymlink = true, force = false): MigrationResult {
  if (existsSync(BACKLOG_DIR)) {
    if (isSymlinkTo(TASKS_DIR, BACKLOG_DIR)) {
      try {
        ensureBacklogAgents(BACKLOG_DIR);
      } catch (e) {
        return { success: false, message: `Failed to initialize AGENTS files: ${e}` };
      }
      return { success: true, message: "Already migrated (.tasks is symlink to .backlog)" };
    }
    if (existsSync(TASKS_DIR) && !lstatSync(TASKS_DIR).isSymbolicLink()) {
      if (!force) {
        return { success: false, message: "Both .tasks/ and .backlog/ exist. Use --force to proceed." };
      }
      try {
        ensureBacklogAgents(BACKLOG_DIR);
      } catch (e) {
        return { success: false, message: `Failed to initialize AGENTS files: ${e}` };
      }
      return { success: true, message: "Both directories exist (force mode - using .backlog/)" };
    }
    try {
      ensureBacklogAgents(BACKLOG_DIR);
    } catch (e) {
      return { success: false, message: `Failed to initialize AGENTS files: ${e}` };
    }
    return { success: true, message: "Already migrated (.backlog/ exists)" };
  }
  
  if (!existsSync(TASKS_DIR)) {
    return { success: false, message: "No .tasks/ directory found to migrate" };
  }
  
  try {
    renameSync(TASKS_DIR, BACKLOG_DIR);
  } catch (e) {
    return { success: false, message: `Failed to rename .tasks/ to .backlog/: ${e}` };
  }
  
  if (createSymlink) {
    try {
      symlinkSync(BACKLOG_DIR, TASKS_DIR);
    } catch (e) {
      return { success: false, message: `Migrated but failed to create symlink: ${e}` };
    }
  }
  
  const mdFiles = ["AGENTS.md", "CLAUDE.md"];
  const updatedFiles: string[] = [];
  
  for (const mdFile of mdFiles) {
    if (existsSync(mdFile) && !["README.md", "PARITY_DIFFS.md"].includes(mdFile)) {
      if (updateMdFile(mdFile)) {
        updatedFiles.push(mdFile);
      }
    }
  }

  try {
    ensureBacklogAgents(BACKLOG_DIR);
  } catch (e) {
    return { success: false, message: `Failed to initialize AGENTS files: ${e}` };
  }
  
  let msg = "Migrated .tasks/ → .backlog/";
  if (createSymlink) msg += " (with symlink)";
  if (updatedFiles.length > 0) {
    msg += `\nUpdated doc files: ${updatedFiles.join(", ")}`;
  }
  
  return { success: true, message: msg };
}
