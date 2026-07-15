import { expect, test } from "bun:test";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = fileURLToPath(new URL("..", import.meta.url));
const cliPath = join(packageRoot, "bin", "backlog");

function run(args: string[]) {
  return Bun.spawnSync(["bun", cliPath, ...args], {
    cwd: packageRoot,
    stdout: "pipe",
    stderr: "pipe",
  });
}

test("cli bridge reaches python tasks", () => {
  const p = run(["--version"]);
  expect(p.exitCode).toBe(0);
  const out = p.stdout.toString() + p.stderr.toString();
  expect(out).toContain("0.0.0");
});

test("version command is explicitly supported", () => {
  const p = run(["version"]);
  expect(p.exitCode).toBe(0);
  const out = p.stdout.toString() + p.stderr.toString();
  expect(out).toContain("backlog version 0.0.0");
});
