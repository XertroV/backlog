package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type parityCommandResult struct {
	Code   int
	Stdout string
}

const parityCommandTimeout = 20 * time.Second

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

var parityVectors = [][]string{
	{"list", "--json"},
	{"next", "--json"},
	{"search", "one"},
	{"blockers"},
	{"show"},
	{"show", "P1.M1.E1.T001"},
	{"timeline"},
	{"report", "progress"},
	{"check", "--json"},
	{"data", "summary"},
	{"schema", "--json"},
	{"howto"},
	{"howto", "--json"},
	{"work"},
	{"work", "P1.M1.E1.T001"},
	{"claim", "P1.M1.E1.T001", "--agent", "agent-x"},
	{"claim", "P1.M1.E1.T001", "--agent", "agent-x", "--no-content"},
	{"grab", "--agent", "agent-x"},
	{"done", "P1.M1.E1.T001"},
	{"done", "P1.M1.E1.T001", "P1.M1.E1.T002", "--force", "--verify"},
	{"update", "P1.M1.E1.T002", "blocked", "--reason", "waiting"},
	{"unclaim", "P1.M1.E1.T001"},
	{"skills", "install", "plan-task", "--client=codex", "--artifact=skills", "--dry-run", "--json"},
	{"log"},
	{"tree"},
	{"add", "P1.M1.E1", "--title", "Parity Task"},
	{"add-epic", "P1", "--title", "Parity Epic"},
	{"add-milestone", "P1", "--title", "Parity Milestone"},
	{"add-phase", "--title", "Parity Phase"},
	{"session", "start", "--agent", "agent-p", "--task", "P1.M1.E1.T001"},
	{"session", "end", "--agent", "agent-p"},
	{"sync"},
}

var pythonGoOnlyParityVectors = [][]string{
	{"skip", "P1.M1.E1.T001"},
	{"handoff", "P1.M1.E1.T001", "--to", "agent-y", "--force", "--notes", "parity"},
	{"unclaim-stale", "--threshold", "30", "--dry-run"},
}

func TestGoParityFixtureSetIsCanonical(t *testing.T) {
	t.Parallel()

	root := parityFixtureRoot(t)
	if _, err := os.Stat(filepath.Join(root, ".tasks")); err != nil {
		t.Fatalf("missing parity fixture root .tasks: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".tasks", "index.yaml")); err != nil {
		t.Fatalf("missing parity fixture root index: %v", err)
	}
}

func TestRunParityCrossValidateGoAgainstPythonAndTypeScript(t *testing.T) {
	projectRoot := mustProjectRoot(t)
	pythonBinary, err := findExecutable("python", "python3")
	if err != nil {
		t.Fatalf("python runtime is required for parity tests: %v", err)
	}
	bunBinary, err := findExecutable("bun")
	if err != nil {
		t.Fatalf("bun runtime is required for parity tests: %v", err)
	}

	fixtureRoot := parityFixtureRoot(t)
	template := filepath.Join(fixtureRoot, ".tasks")
	pythonScript := filepath.Join(projectRoot, "backlog.py")
	tsBundle := buildTypeScriptParityBundle(t, bunBinary, projectRoot)

	for _, vector := range parityVectors {
		vector := vector
		t.Run(strings.Join(vector, " "), func(t *testing.T) {
			goRoot := t.TempDir()
			pyRoot := t.TempDir()
			tsRoot := t.TempDir()

			copyFixture(t, template, filepath.Join(goRoot, ".tasks"))
			copyFixture(t, template, filepath.Join(pyRoot, ".tasks"))
			copyFixture(t, template, filepath.Join(tsRoot, ".tasks"))

			goResult := runGoCommandInDir(t, goRoot, vector...)
			pyResult, runErr := runCommand(pythonBinary, []string{pythonScript}, vector, pyRoot)
			if runErr != nil {
				t.Fatalf("python %s = %v", strings.Join(vector, " "), runErr)
			}
			tsResult, runErr := runCommand(bunBinary, []string{tsBundle}, vector, tsRoot)
			if runErr != nil {
				t.Fatalf("bun %s %s = %v", tsBundle, strings.Join(vector, " "), runErr)
			}
			tsResult.Stdout = sanitizeOutput(tsResult.Stdout, tsRoot)

			if goResult.Code != pyResult.Code {
				t.Fatalf(
					"exit code mismatch go=%d py=%d for %q\n--- go stdout ---\n%s\n--- py stdout ---\n%s",
					goResult.Code, pyResult.Code, strings.Join(vector, " "),
					goResult.Stdout, pyResult.Stdout,
				)
			}
			if goResult.Code != tsResult.Code {
				t.Fatalf(
					"exit code mismatch go=%d ts=%d for %q\n--- go stdout ---\n%s\n--- ts stdout ---\n%s",
					goResult.Code, tsResult.Code, strings.Join(vector, " "),
					goResult.Stdout, tsResult.Stdout,
				)
			}

			if hasArg(vector, "--json") {
				assertCommandJSONParity(t, vector, goResult.Stdout, pyResult.Stdout, tsResult.Stdout)
			}

			switch vector[0] {
			case "sync":
				assertSyncStateParity(t, goRoot, pyRoot, tsRoot)
			case "add":
				assertListCountParity(t, filepath.Join(goRoot, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml"), filepath.Join(pyRoot, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml"), filepath.Join(tsRoot, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml"), "tasks")
			case "add-epic":
				assertListCountParity(t, filepath.Join(goRoot, ".tasks", "01-phase", "01-ms", "index.yaml"), filepath.Join(pyRoot, ".tasks", "01-phase", "01-ms", "index.yaml"), filepath.Join(tsRoot, ".tasks", "01-phase", "01-ms", "index.yaml"), "epics")
			case "add-milestone":
				assertListCountParity(t, filepath.Join(goRoot, ".tasks", "01-phase", "index.yaml"), filepath.Join(pyRoot, ".tasks", "01-phase", "index.yaml"), filepath.Join(tsRoot, ".tasks", "01-phase", "index.yaml"), "milestones")
			case "add-phase":
				assertListCountParity(t, filepath.Join(goRoot, ".tasks", "index.yaml"), filepath.Join(pyRoot, ".tasks", "index.yaml"), filepath.Join(tsRoot, ".tasks", "index.yaml"), "phases")
			case "idea":
				assertListCountParity(t, filepath.Join(goRoot, ".tasks", "ideas", "index.yaml"), filepath.Join(pyRoot, ".tasks", "ideas", "index.yaml"), filepath.Join(tsRoot, ".tasks", "ideas", "index.yaml"), "ideas")
			default:
				assertRootIndexParity(t, goRoot, pyRoot, tsRoot)
			}

			assertCommandStateParity(t, vector, goRoot, pyRoot, tsRoot)
		})
	}
}

func TestRunParityCrossValidateGoAgainstPython(t *testing.T) {
	projectRoot := mustProjectRoot(t)
	pythonBinary, err := findExecutable("python", "python3")
	if err != nil {
		t.Fatalf("python runtime is required for parity tests: %v", err)
	}

	fixtureRoot := parityFixtureRoot(t)
	template := filepath.Join(fixtureRoot, ".tasks")
	pythonScript := filepath.Join(projectRoot, "backlog.py")

	for _, vector := range pythonGoOnlyParityVectors {
		vector := vector
		t.Run(strings.Join(vector, " "), func(t *testing.T) {
			goRoot := t.TempDir()
			pyRoot := t.TempDir()
			copyFixture(t, template, filepath.Join(goRoot, ".tasks"))
			copyFixture(t, template, filepath.Join(pyRoot, ".tasks"))

			goResult := runGoCommandInDir(t, goRoot, vector...)
			pyResult, runErr := runCommand(pythonBinary, []string{pythonScript}, vector, pyRoot)
			if runErr != nil {
				t.Fatalf("python %s = %v", strings.Join(vector, " "), runErr)
			}

			if goResult.Code != pyResult.Code {
				t.Fatalf(
					"exit code mismatch go=%d py=%d for %q\n--- go stdout ---\n%s\n--- py stdout ---\n%s",
					goResult.Code, pyResult.Code, strings.Join(vector, " "),
					goResult.Stdout, pyResult.Stdout,
				)
			}

			if hasArg(vector, "--json") {
				assertCommandJSONParity(t, vector, goResult.Stdout, pyResult.Stdout, pyResult.Stdout)
			}

			assertRootIndexParity(t, goRoot, pyRoot, pyRoot)
			assertCommandStateParity(t, vector, goRoot, pyRoot, pyRoot)
		})
	}
}

func TestRunParityCrossValidateFailureModesAgainstPythonAndTypeScript(t *testing.T) {
	projectRoot := mustProjectRoot(t)
	pythonBinary, err := findExecutable("python", "python3")
	if err != nil {
		t.Fatalf("python runtime is required for parity tests: %v", err)
	}
	bunBinary, err := findExecutable("bun")
	if err != nil {
		t.Fatalf("bun runtime is required for parity tests: %v", err)
	}

	fixtureRoot := parityFixtureRoot(t)
	template := filepath.Join(fixtureRoot, ".tasks")
	tsBundle := buildTypeScriptParityBundle(t, bunBinary, projectRoot)

	type parityFailureCase struct {
		command []string
		setup   func(*testing.T, string)
	}

	cases := []parityFailureCase{
		{
			command: []string{"show", "invalid-id"},
		},
		{
			command: []string{"done", "P1.M1.E1.T001"},
		},
		{
			command: []string{"update", "P1.M1.E1.T001", "blocked", "--reason", "waiting"},
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-one.todo")); err != nil {
					t.Fatalf("remove fixture task file for missing-task setup: %v", err)
				}
			},
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(strings.Join(testCase.command, " "), func(t *testing.T) {
			goRoot := t.TempDir()
			pyRoot := t.TempDir()
			tsRoot := t.TempDir()

			copyFixture(t, template, filepath.Join(goRoot, ".tasks"))
			copyFixture(t, template, filepath.Join(pyRoot, ".tasks"))
			copyFixture(t, template, filepath.Join(tsRoot, ".tasks"))

			if testCase.setup != nil {
				testCase.setup(t, goRoot)
				testCase.setup(t, pyRoot)
				testCase.setup(t, tsRoot)
			}

			goResult := runGoCommandInDir(t, goRoot, testCase.command...)
			pyResult, runErr := runCommand(pythonBinary, []string{filepath.Join(projectRoot, "backlog.py")}, testCase.command, pyRoot)
			if runErr != nil {
				t.Fatalf("python %s = %v", strings.Join(testCase.command, " "), runErr)
			}
			tsResult, runErr := runCommand(bunBinary, []string{tsBundle}, testCase.command, tsRoot)
			if runErr != nil {
				t.Fatalf("bun %s %s = %v", tsBundle, strings.Join(testCase.command, " "), runErr)
			}

			if goResult.Code == 0 {
				t.Fatalf("expected go to fail %s", strings.Join(testCase.command, " "))
			}
			if goResult.Code != pyResult.Code {
				t.Fatalf(
					"exit code mismatch go=%d py=%d for %q\n--- go stdout ---\n%s\n--- py stdout ---\n%s",
					goResult.Code, pyResult.Code, strings.Join(testCase.command, " "),
					goResult.Stdout, pyResult.Stdout,
				)
			}
			if goResult.Code != tsResult.Code {
				t.Fatalf(
					"exit code mismatch go=%d ts=%d for %q\n--- go stdout ---\n%s\n--- ts stdout ---\n%s",
					goResult.Code, tsResult.Code, strings.Join(testCase.command, " "),
					goResult.Stdout, tsResult.Stdout,
				)
			}

			if strings.TrimSpace(goResult.Stdout) == "" && goResult.Code != 0 {
				t.Fatalf("expected non-empty output for failed command %q", strings.Join(testCase.command, " "))
			}
		})
	}
}

func TestRunParityUXSemanticAnchors(t *testing.T) {
	projectRoot := mustProjectRoot(t)
	pythonBinary, err := findExecutable("python", "python3")
	if err != nil {
		t.Fatalf("python runtime is required for parity tests: %v", err)
	}
	bunBinary, err := findExecutable("bun")
	if err != nil {
		t.Fatalf("bun runtime is required for parity tests: %v", err)
	}

	fixtureRoot := parityFixtureRoot(t)
	template := filepath.Join(fixtureRoot, ".tasks")
	tsBundle := buildTypeScriptParityBundle(t, bunBinary, projectRoot)

	t.Run("show-invalid-id-has-error-and-next-step-guidance", func(t *testing.T) {
		goRoot := t.TempDir()
		pyRoot := t.TempDir()
		tsRoot := t.TempDir()

		copyFixture(t, template, filepath.Join(goRoot, ".tasks"))
		copyFixture(t, template, filepath.Join(pyRoot, ".tasks"))
		copyFixture(t, template, filepath.Join(tsRoot, ".tasks"))

		command := []string{"show", "P9"}
		goResult := runGoCommandInDir(t, goRoot, command...)
		pyResult, runErr := runCommand(pythonBinary, []string{filepath.Join(projectRoot, "backlog.py")}, command, pyRoot)
		if runErr != nil {
			t.Fatalf("python %s = %v", strings.Join(command, " "), runErr)
		}
		tsResult, runErr := runCommand(bunBinary, []string{tsBundle}, command, tsRoot)
		if runErr != nil {
			t.Fatalf("bun %s %s = %v", tsBundle, strings.Join(command, " "), runErr)
		}

		for _, impl := range []struct {
			name   string
			result parityCommandResult
		}{
			{name: "go", result: goResult},
			{name: "python", result: pyResult},
			{name: "typescript", result: tsResult},
		} {
			if impl.result.Code == 0 {
				t.Fatalf("%s expected non-zero exit for %q", impl.name, strings.Join(command, " "))
			}
			assertContainsAllFragments(t, impl.name, impl.result.Stdout, "Phase not found", "Tip: Use 'backlog tree'")
		}
	})

	t.Run("claim-missing-id-has-actionable-error", func(t *testing.T) {
		goRoot := t.TempDir()
		pyRoot := t.TempDir()
		tsRoot := t.TempDir()

		copyFixture(t, template, filepath.Join(goRoot, ".tasks"))
		copyFixture(t, template, filepath.Join(pyRoot, ".tasks"))
		copyFixture(t, template, filepath.Join(tsRoot, ".tasks"))

		command := []string{"claim"}
		goResult := runGoCommandInDir(t, goRoot, command...)
		pyResult, runErr := runCommand(pythonBinary, []string{filepath.Join(projectRoot, "backlog.py")}, command, pyRoot)
		if runErr != nil {
			t.Fatalf("python %s = %v", strings.Join(command, " "), runErr)
		}
		tsResult, runErr := runCommand(bunBinary, []string{tsBundle}, command, tsRoot)
		if runErr != nil {
			t.Fatalf("bun %s %s = %v", tsBundle, strings.Join(command, " "), runErr)
		}

		for _, impl := range []struct {
			name   string
			result parityCommandResult
		}{
			{name: "go", result: goResult},
			{name: "python", result: pyResult},
			{name: "typescript", result: tsResult},
		} {
			if impl.result.Code == 0 {
				t.Fatalf("%s expected non-zero exit for %q", impl.name, strings.Join(command, " "))
			}
			assertContainsAllFragments(t, impl.name, impl.result.Stdout, "claim requires at least one TASK_ID")
		}
	})

	t.Run("claim-scope-falls-back-to-show", func(t *testing.T) {
		goRoot := t.TempDir()
		pyRoot := t.TempDir()
		tsRoot := t.TempDir()

		copyFixture(t, template, filepath.Join(goRoot, ".tasks"))
		copyFixture(t, template, filepath.Join(pyRoot, ".tasks"))
		copyFixture(t, template, filepath.Join(tsRoot, ".tasks"))

		command := []string{"claim", "P1.M1"}
		goResult := runGoCommandInDir(t, goRoot, command...)
		pyResult, runErr := runCommand(pythonBinary, []string{filepath.Join(projectRoot, "backlog.py")}, command, pyRoot)
		if runErr != nil {
			t.Fatalf("python %s = %v", strings.Join(command, " "), runErr)
		}
		tsResult, runErr := runCommand(bunBinary, []string{tsBundle}, command, tsRoot)
		if runErr != nil {
			t.Fatalf("bun %s %s = %v", tsBundle, strings.Join(command, " "), runErr)
		}

		for _, impl := range []struct {
			name   string
			result parityCommandResult
		}{
			{name: "go", result: goResult},
			{name: "python", result: pyResult},
			{name: "typescript", result: tsResult},
		} {
			if impl.result.Code != 0 {
				t.Fatalf("%s expected zero exit for %q", impl.name, strings.Join(command, " "))
			}
			assertContainsAllFragments(t, impl.name, impl.result.Stdout, "claim only works with task IDs.", "show P1.M1")
		}
	})

	t.Run("help-mentions-core-commands-and-alias-discoverability", func(t *testing.T) {
		goRoot := t.TempDir()
		pyRoot := t.TempDir()
		tsRoot := t.TempDir()

		copyFixture(t, template, filepath.Join(goRoot, ".tasks"))
		copyFixture(t, template, filepath.Join(pyRoot, ".tasks"))
		copyFixture(t, template, filepath.Join(tsRoot, ".tasks"))

		command := []string{"--help"}
		goResult := runGoCommandInDir(t, goRoot, command...)
		pyResult, runErr := runCommand(pythonBinary, []string{filepath.Join(projectRoot, "backlog.py")}, command, pyRoot)
		if runErr != nil {
			t.Fatalf("python %s = %v", strings.Join(command, " "), runErr)
		}
		tsResult, runErr := runCommand(bunBinary, []string{tsBundle}, command, tsRoot)
		if runErr != nil {
			t.Fatalf("bun %s %s = %v", tsBundle, strings.Join(command, " "), runErr)
		}

		for _, impl := range []struct {
			name   string
			result parityCommandResult
		}{
			{name: "go", result: goResult},
			{name: "python", result: pyResult},
			{name: "typescript", result: tsResult},
		} {
			if impl.result.Code != 0 {
				t.Fatalf("%s expected zero exit for %q, got %d", impl.name, strings.Join(command, " "), impl.result.Code)
			}
			assertContainsAllFragments(t, impl.name, impl.result.Stdout, "list", "report", "timeline")
			assertContainsAnyFragment(t, impl.name, impl.result.Stdout, "(alias:", "alias: tl", "  ls")
		}
	})
}

func parityFixtureRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve parity fixture caller path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "parity-fixture"))
	return root
}

func mustProjectRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve project root caller path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}

func buildTypeScriptParityBundle(t *testing.T, bunBinary, projectRoot string) string {
	t.Helper()
	entrypoint := filepath.Join(projectRoot, "backlog_ts", "src", "cli.ts")
	outPath := filepath.Join(t.TempDir(), "backlog-ts-cli.bundle.js")
	ctx, cancel := context.WithTimeout(context.Background(), parityCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		bunBinary,
		"build",
		entrypoint,
		"--outfile",
		outPath,
		"--target",
		"bun",
	)
	cmd.Dir = projectRoot
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("bun build timed out after %s: %s", parityCommandTimeout, strings.TrimSpace(output.String()))
		}
		t.Fatalf("bun build failed: %v\n%s", err, strings.TrimSpace(output.String()))
	}

	return outPath
}

func runCommand(executable string, fixedArgs []string, command []string, cwd string) (parityCommandResult, error) {
	args := append(append([]string{}, fixedArgs...), command...)
	ctx, cancel := context.WithTimeout(context.Background(), parityCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = cwd
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return parityCommandResult{
			Code:   124,
			Stdout: sanitizeOutput(buf.String(), cwd),
		}, fmt.Errorf("command timed out after %s: %s %s", parityCommandTimeout, executable, strings.Join(args, " "))
	}
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			return parityCommandResult{}, err
		}
	}
	return parityCommandResult{
		Code:   code,
		Stdout: sanitizeOutput(buf.String(), cwd),
	}, nil
}

func runGoCommandInDir(t *testing.T, root string, args ...string) parityCommandResult {
	t.Helper()
	output, err := runInDir(t, root, args...)
	code := 0
	if err != nil {
		code = 1
		if strings.TrimSpace(output) == "" {
			output = err.Error()
		}
	}
	return parityCommandResult{
		Code:   code,
		Stdout: sanitizeOutput(output, root),
	}
}

func sanitizeOutput(raw string, cwd string) string {
	// The Go command may print raw .tasks paths; trim root prefixes to make
	// comparisons deterministic across environments.
	replaced := strings.ReplaceAll(raw, cwd+string(filepath.Separator), "")
	noAnsi := ansiEscape.ReplaceAllString(replaced, "")
	return strings.ReplaceAll(strings.TrimSpace(noAnsi), "\r\n", "\n")
}

func hasArg(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}

func assertCommandJSONParity(t *testing.T, command []string, goRaw, pyRaw, tsRaw string) {
	t.Helper()

	var goPayload any
	var pyPayload any
	var tsPayload any
	if err := json.Unmarshal([]byte(goRaw), &goPayload); err != nil {
		t.Fatalf("go json parse error for %q: %v", strings.Join(command, " "), err)
	}
	if err := json.Unmarshal([]byte(pyRaw), &pyPayload); err != nil {
		t.Fatalf("python json parse error for %q: %v", strings.Join(command, " "), err)
	}
	if err := json.Unmarshal([]byte(tsRaw), &tsPayload); err != nil {
		t.Fatalf("typescript json parse error for %q: %v", strings.Join(command, " "), err)
	}

	switch {
	case command[0] == "next":
		assertAllJSONHaveString(t, command, "id", goPayload, pyPayload, tsPayload)
	case command[0] == "list":
		if !arrayAt(goPayload, "critical_path") || !arrayAt(pyPayload, "critical_path") || !arrayAt(tsPayload, "critical_path") {
			t.Fatalf("list --json missing critical_path")
		}
		assertNextAvailableParity(t, command, goPayload, pyPayload, tsPayload)
	case command[0] == "dash":
		assertMapHasKey(t, command, "overall", goPayload)
		assertMapHasKey(t, command, "critical_path", goPayload)
		assertMapHasKey(t, command, "overall", pyPayload)
		assertMapHasKey(t, command, "critical_path", pyPayload)
		assertMapHasKey(t, command, "overall", tsPayload)
		assertMapHasKey(t, command, "critical_path", tsPayload)
	case command[0] == "check":
		goOK, goErrors, ok := parseCheckJSON(goPayload)
		if !ok {
			t.Fatalf("check --json payload malformed for go")
		}
		pyOK, pyErrors, ok := parseCheckJSON(pyPayload)
		if !ok {
			t.Fatalf("check --json payload malformed for python")
		}
		tsOK, tsErrors, ok := parseCheckJSON(tsPayload)
		if !ok {
			t.Fatalf("check --json payload malformed for typescript")
		}
		if goOK != pyOK || goOK != tsOK {
			t.Fatalf("check --json ok mismatch go=%v py=%v ts=%v", goOK, pyOK, tsOK)
		}
		if goErrors != pyErrors || goErrors != tsErrors {
			t.Fatalf("check --json summary.errors mismatch go=%v py=%v ts=%v", goErrors, pyErrors, tsErrors)
		}
	case command[0] == "data" && len(command) > 1 && command[1] == "summary":
		goTotal, _, ok := parseDataSummaryJSON(goPayload)
		if !ok {
			t.Fatalf("data summary payload malformed for go")
		}
		pyTotal, _, ok := parseDataSummaryJSON(pyPayload)
		if !ok {
			t.Fatalf("data summary payload malformed for python")
		}
		tsTotal, _, ok := parseDataSummaryJSON(tsPayload)
		if !ok {
			t.Fatalf("data summary payload malformed for typescript")
		}
		assertIntEqual(t, goTotal, pyTotal, tsTotal, command)
		assertAllJSONStringEq(t, command, "project", goPayload, pyPayload, tsPayload)
	case command[0] == "data" && len(command) > 1 && command[1] == "export":
		goProject, goSections, ok := parseDataExportJSON(goPayload)
		if !ok {
			t.Fatalf("data export payload malformed for go")
		}
		pyProject, pySections, ok := parseDataExportJSON(pyPayload)
		if !ok {
			t.Fatalf("data export payload malformed for python")
		}
		tsProject, tsSections, ok := parseDataExportJSON(tsPayload)
		if !ok {
			t.Fatalf("data export payload malformed for typescript")
		}
		if goProject != pyProject || goProject != tsProject {
			t.Fatalf("data export --json project mismatch go=%q py=%q ts=%q", goProject, pyProject, tsProject)
		}
		if len(goSections) != len(pySections) || len(goSections) != len(tsSections) {
			t.Fatalf("data export --json phases mismatch go=%d py=%d ts=%d", len(goSections), len(pySections), len(tsSections))
		}
		if !arrayAt(goPayload, "phases") || !arrayAt(pyPayload, "phases") || !arrayAt(tsPayload, "phases") {
			t.Fatalf("data export --json missing phases")
		}
	case command[0] == "report" && len(command) > 1 && command[1] == "progress":
		assertMapHasKey(t, command, "overall", goPayload)
		assertMapHasKey(t, command, "overall", pyPayload)
		assertMapHasKey(t, command, "overall", tsPayload)
	case command[0] == "report" && len(command) > 1 && command[1] == "velocity":
		if !arrayAt(goPayload, "daily_data") || !arrayAt(pyPayload, "daily_data") || !arrayAt(tsPayload, "daily_data") {
			t.Fatalf("report velocity --json missing daily_data")
		}
	case command[0] == "report" && len(command) > 1 && command[1] == "estimate-accuracy":
		if !numberAt(goPayload, "tasks_analyzed") || !numberAt(pyPayload, "tasks_analyzed") || !numberAt(tsPayload, "tasks_analyzed") {
			t.Fatalf("report estimate-accuracy --json missing tasks_analyzed")
		}
	case command[0] == "schema":
		for _, payload := range []any{goPayload, pyPayload, tsPayload} {
			if !numberAt(payload, "schema_version") {
				t.Fatalf("schema --json payload missing schema_version")
			}
			if !arrayAt(payload, "files") {
				t.Fatalf("schema --json payload missing files")
			}
		}
	case command[0] == "skills" && len(command) > 1 && command[1] == "install":
		if !arrayAt(goPayload, "operations") || !arrayAt(pyPayload, "operations") || !arrayAt(tsPayload, "operations") {
			t.Fatalf("skills install --json payload missing operations")
		}
	}
}

func assertNextAvailableParity(t *testing.T, command []string, goPayload, pyPayload, tsPayload any) {
	t.Helper()
	goValue := parseNextAvailable(goPayload, t, command)
	pyValue := parseNextAvailable(pyPayload, t, command)
	tsValue := parseNextAvailable(tsPayload, t, command)
	if goValue != pyValue || goValue != tsValue {
		t.Fatalf("%s next_available mismatch go=%q py=%q ts=%q", strings.Join(command, " "), goValue, pyValue, tsValue)
	}
}

func parseNextAvailable(payload any, t *testing.T, command []string) string {
	t.Helper()
	m := asMap(payload)
	if m == nil {
		t.Fatalf("%s payload missing root", strings.Join(command, " "))
	}
	raw, exists := m["next_available"]
	if !exists || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return value
	default:
		t.Fatalf("%s payload has invalid next_available type %T", strings.Join(command, " "), value)
	}
	return ""
}

func assertIntEqual(t *testing.T, goValue, pyValue, tsValue int, command []string) {
	t.Helper()
	if goValue != pyValue || goValue != tsValue {
		t.Fatalf("%s total mismatch go=%d py=%d ts=%d", strings.Join(command, " "), goValue, pyValue, tsValue)
	}
}

func asMap(payload any) map[string]interface{} {
	if m, ok := payload.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func arrayAt(payload any, key string) bool {
	m := asMap(payload)
	if m == nil {
		return false
	}
	_, ok := m[key].([]interface{})
	return ok
}

func assertMapHasKey(t *testing.T, command []string, key string, payload any) {
	t.Helper()
	m := asMap(payload)
	if m == nil {
		t.Fatalf("%s payload missing object root", strings.Join(command, " "))
	}
	if _, ok := m[key]; !ok {
		t.Fatalf("%s payload missing %q", strings.Join(command, " "), key)
	}
}

func numberAt(payload any, key string) bool {
	m := asMap(payload)
	if m == nil {
		return false
	}
	switch m[key].(type) {
	case float64, float32, int, int64, int32:
		return true
	default:
		return false
	}
}

func assertAllJSONStringEq(t *testing.T, command []string, key string, values ...any) {
	t.Helper()
	var first string
	for i, value := range values {
		m := asMap(value)
		if m == nil {
			t.Fatalf("%s payload missing object map", strings.Join(command, " "))
		}
		got, ok := m[key].(string)
		if !ok {
			t.Fatalf("%s payload missing %q string", strings.Join(command, " "), key)
		}
		if i == 0 {
			first = got
		} else if got != first {
			t.Fatalf("%s payload mismatch for %q: got %q, expected %q", strings.Join(command, " "), key, got, first)
		}
	}
}

func assertAllJSONHaveString(t *testing.T, command []string, key string, values ...any) {
	t.Helper()
	for _, value := range values {
		m := asMap(value)
		if m == nil {
			t.Fatalf("%s payload missing object map", strings.Join(command, " "))
		}
		if _, ok := m[key].(string); !ok {
			t.Fatalf("%s payload missing %q string", strings.Join(command, " "), key)
		}
	}
}

func parseCheckJSON(payload any) (bool, int, bool) {
	m := asMap(payload)
	if m == nil {
		return false, 0, false
	}
	okValue, ok := m["ok"].(bool)
	if !ok {
		return false, 0, false
	}
	summary, ok := m["summary"].(map[string]interface{})
	if !ok {
		return false, 0, false
	}
	raw, ok := summary["errors"]
	if !ok {
		return false, 0, false
	}
	errorsValue, ok := raw.(float64)
	if !ok {
		return false, 0, false
	}
	return okValue, int(errorsValue), true
}

func parseDataSummaryJSON(payload any) (int, string, bool) {
	m := asMap(payload)
	if m == nil {
		return 0, "", false
	}
	overall, ok := m["overall"].(map[string]interface{})
	if !ok {
		return 0, "", false
	}
	total, ok := overall["total"].(float64)
	if !ok {
		return 0, "", false
	}
	project, ok := m["project"].(string)
	if !ok {
		return 0, "", false
	}
	return int(total), project, true
}

func parseDataExportJSON(payload any) (string, []interface{}, bool) {
	m := asMap(payload)
	if m == nil {
		return "", nil, false
	}
	sections, ok := m["phases"].([]interface{})
	if !ok {
		return "", nil, false
	}
	project, ok := m["project"].(string)
	if !ok {
		return "", nil, false
	}
	return project, sections, true
}

func assertRootIndexParity(t *testing.T, goRoot, pyRoot, tsRoot string) {
	t.Helper()
	goIndex := readYAMLMapForTest(t, filepath.Join(goRoot, ".tasks", "index.yaml"))
	pyIndex := readYAMLMapForTest(t, filepath.Join(pyRoot, ".tasks", "index.yaml"))
	tsIndex := readYAMLMapForTest(t, filepath.Join(tsRoot, ".tasks", "index.yaml"))
	if !reflect.DeepEqual(goIndex, pyIndex) || !reflect.DeepEqual(goIndex, tsIndex) {
		t.Fatalf("root index mismatch\n\ngo=%#v\npy=%#v\nts=%#v\n", goIndex, pyIndex, tsIndex)
	}
}

func assertSyncStateParity(t *testing.T, goRoot, pyRoot, tsRoot string) {
	t.Helper()

	goIndex := readYAMLMapForTest(t, filepath.Join(goRoot, ".tasks", "index.yaml"))
	pyIndex := readYAMLMapForTest(t, filepath.Join(pyRoot, ".tasks", "index.yaml"))
	tsIndex := readYAMLMapForTest(t, filepath.Join(tsRoot, ".tasks", "index.yaml"))

	if _, ok := pyIndex["critical_path"].([]interface{}); !ok {
		t.Fatalf("sync state missing critical_path in python index")
	}
	if _, ok := tsIndex["critical_path"].([]interface{}); !ok {
		t.Fatalf("sync state missing critical_path in typescript index")
	}
	if _, ok := goIndex["critical_path"].([]interface{}); !ok {
		t.Fatalf("sync state missing critical_path in go index")
	}

	pyHasNext := pyIndex["next_available"] != nil
	tsHasNext := tsIndex["next_available"] != nil
	goHasNext := goIndex["next_available"] != nil
	if pyHasNext != tsHasNext || pyHasNext != goHasNext {
		t.Fatalf("sync next_available nullability mismatch")
	}

	if _, ok := goIndex["stats"].(map[string]interface{}); !ok {
		t.Fatalf("sync state missing go stats")
	}
	if _, ok := pyIndex["stats"].(map[string]interface{}); !ok {
		t.Fatalf("sync state missing python stats")
	}
	if _, ok := tsIndex["stats"].(map[string]interface{}); !ok {
		t.Fatalf("sync state missing typescript stats")
	}

	checkStats := func(name string, payload map[string]interface{}, requireTotalTasks bool) {
		stats, ok := payload["stats"].(map[string]interface{})
		if !ok {
			t.Fatalf("sync state missing %s stats", name)
		}
		totalKey := "total"
		if requireTotalTasks {
			totalKey = "total_tasks"
		}
		for _, key := range []string{totalKey, "done", "in_progress", "blocked", "pending"} {
			switch stats[key].(type) {
			case int, int32, int64, float64:
			default:
				t.Fatalf("sync stats for %s missing or invalid key %s: %#v", name, key, stats)
			}
		}
	}

	checkPathSyncStats := func(path string, requireTotalTasks bool) {
		checkStats(path+" phase", readYAMLMapForTest(t, filepath.Join(goRoot, ".tasks", path)), requireTotalTasks)
		checkStats(path+" phase", readYAMLMapForTest(t, filepath.Join(pyRoot, ".tasks", path)), requireTotalTasks)
		checkStats(path+" phase", readYAMLMapForTest(t, filepath.Join(tsRoot, ".tasks", path)), requireTotalTasks)
	}

	checkPathSyncStats(filepath.Join("01-phase", "index.yaml"), true)
	checkPathSyncStats(filepath.Join("01-phase", "01-ms", "index.yaml"), true)
	checkPathSyncStats(filepath.Join("01-phase", "01-ms", "01-epic", "index.yaml"), false)
}

func assertCommandStateParity(t *testing.T, command []string, goRoot, pyRoot, tsRoot string) {
	t.Helper()
	switch command[0] {
	case "claim":
		assertTaskStatusMatch(t, command, goRoot, pyRoot, tsRoot, "P1.M1.E1.T001", filepath.Join("01-phase", "01-ms", "01-epic", "T001-one.todo"))
	case "grab":
		assertTaskStatusMatch(t, command, goRoot, pyRoot, tsRoot, "P1.M1.E1.T001", filepath.Join("01-phase", "01-ms", "01-epic", "T001-one.todo"))
	case "done":
		assertTaskStatusMatch(t, command, goRoot, pyRoot, tsRoot, "P1.M1.E1.T001", filepath.Join("01-phase", "01-ms", "01-epic", "T001-one.todo"))
	case "update":
		assertTaskStatusMatch(t, command, goRoot, pyRoot, tsRoot, "P1.M1.E1.T002", filepath.Join("01-phase", "01-ms", "01-epic", "T002-two.todo"))
	case "blocked":
		assertTaskStatusMatch(t, command, goRoot, pyRoot, tsRoot, "P1.M1.E1.T001", filepath.Join("01-phase", "01-ms", "01-epic", "T001-one.todo"))
		if !containsNoGrab(command) {
			assertTaskStatusMatch(t, command, goRoot, pyRoot, tsRoot, "P1.M1.E1.T002", filepath.Join("01-phase", "01-ms", "01-epic", "T002-two.todo"))
		}
	case "session":
		if len(command) > 1 && command[1] == "start" {
			assertSessionHasAgent(t, filepath.Join(goRoot, ".tasks", ".sessions.yaml"), "agent-p")
			assertSessionHasAgent(t, filepath.Join(pyRoot, ".tasks", ".sessions.yaml"), "agent-p")
			assertSessionHasAgent(t, filepath.Join(tsRoot, ".tasks", ".sessions.yaml"), "agent-p")
		}
		if len(command) > 1 && command[1] == "end" {
			assertSessionMissingAgent(t, filepath.Join(goRoot, ".tasks", ".sessions.yaml"), "agent-p")
			assertSessionMissingAgent(t, filepath.Join(pyRoot, ".tasks", ".sessions.yaml"), "agent-p")
			assertSessionMissingAgent(t, filepath.Join(tsRoot, ".tasks", ".sessions.yaml"), "agent-p")
		}
	case "idea":
		assertIdeaFileName(t, goRoot, filepath.Join(goRoot, ".tasks", "ideas", "index.yaml"))
		assertIdeaFileName(t, pyRoot, filepath.Join(pyRoot, ".tasks", "ideas", "index.yaml"))
		assertIdeaFileName(t, tsRoot, filepath.Join(tsRoot, ".tasks", "ideas", "index.yaml"))
	}
}

func containsNoGrab(args []string) bool {
	for _, arg := range args {
		if arg == "--no-grab" {
			return true
		}
	}
	return false
}

func assertListCountParity(t *testing.T, goPath, pyPath, tsPath, section string) {
	t.Helper()
	goCount := countIndexSectionEntries(t, goPath, section)
	pyCount := countIndexSectionEntries(t, pyPath, section)
	tsCount := countIndexSectionEntries(t, tsPath, section)
	if goCount != pyCount || goCount != tsCount {
		t.Fatalf("%s count mismatch go=%d py=%d ts=%d", section, goCount, pyCount, tsCount)
	}
}

func countIndexSectionEntries(t *testing.T, path string, section string) int {
	t.Helper()
	index := readYAMLMapForTest(t, path)
	raw, ok := index[section]
	if !ok {
		return 0
	}
	values, ok := raw.([]interface{})
	if !ok {
		return 0
	}
	return len(values)
}

func readYAMLMapForTest(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var payload map[string]interface{}
	if err := yaml.Unmarshal(content, &payload); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return payload
}

func taskStatusInFixture(root string, relPath string) string {
	path := filepath.Join(root, ".tasks", relPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	parts := strings.Split(rawString(raw), "---")
	if len(parts) < 3 {
		return ""
	}
	var frontmatter struct {
		Status string `yaml:"status"`
	}
	if err := yaml.Unmarshal([]byte(parts[1]), &frontmatter); err != nil {
		return ""
	}
	return frontmatter.Status
}

func rawString(raw []byte) string {
	return string(raw)
}

func assertTaskStatusMatch(t *testing.T, vector []string, goRoot, pyRoot, tsRoot string, taskID, relativePath string) {
	t.Helper()
	goStatus := taskStatusInFixture(goRoot, relativePath)
	pyStatus := taskStatusInFixture(pyRoot, relativePath)
	tsStatus := taskStatusInFixture(tsRoot, relativePath)
	if goStatus == "" || pyStatus == "" || tsStatus == "" {
		t.Fatalf("status missing for %q in one or more implementations (%s)", taskID, strings.Join(vector, " "))
	}
	if goStatus != pyStatus || goStatus != tsStatus {
		t.Fatalf("status mismatch for %s: go=%q py=%q ts=%q", taskID, goStatus, pyStatus, tsStatus)
	}
}

func assertSessionHasAgent(t *testing.T, path, agent string) {
	t.Helper()
	session := readYAMLMapFileIfExists(t, path)
	if _, ok := session[agent]; !ok {
		t.Fatalf("missing session for %q in %s", agent, path)
	}
}

func assertSessionMissingAgent(t *testing.T, path, agent string) {
	t.Helper()
	session := readYAMLMapFileIfExists(t, path)
	if _, ok := session[agent]; ok {
		t.Fatalf("session %q was expected to be removed in %s", agent, path)
	}
}

func readYAMLMapFileIfExists(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		return map[string]interface{}{}
	}
	var payload map[string]interface{}
	if err := yaml.Unmarshal(content, &payload); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return payload
}

func assertIdeaFileName(t *testing.T, root string, path string) {
	t.Helper()
	ideaIndex := readYAMLMapForTest(t, path)
	raw, ok := ideaIndex["ideas"].([]interface{})
	if !ok || len(raw) == 0 {
		t.Fatalf("missing ideas in %s", root)
	}
	first, ok := raw[0].(map[string]interface{})
	if !ok {
		t.Fatalf("invalid idea item in %s", root)
	}
	name, ok := first["file"].(string)
	if !ok || !strings.HasPrefix(name, "I001-") {
		t.Fatalf("idea file mismatch in %s: %v", root, first["file"])
	}
}

func copyFixture(t *testing.T, src string, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, info os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, content, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture from %s to %s: %v", src, dst, err)
	}
}

func findExecutable(names ...string) (string, error) {
	for _, name := range names {
		execPath, err := exec.LookPath(name)
		if err == nil {
			return execPath, nil
		}
	}
	return "", fmt.Errorf("required executable not found in PATH: %s", strings.Join(names, "/"))
}

func assertContainsAllFragments(t *testing.T, implementation, output string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(output, fragment) {
			t.Fatalf("%s output missing fragment %q\noutput:\n%s", implementation, fragment, output)
		}
	}
}

func assertContainsAnyFragment(t *testing.T, implementation, output string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if strings.Contains(output, fragment) {
			return
		}
	}
	t.Fatalf("%s output missing all expected fragments %v\noutput:\n%s", implementation, fragments, output)
}
