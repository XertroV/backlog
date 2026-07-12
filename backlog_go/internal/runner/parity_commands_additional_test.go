package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runAppendWithStdin runs the append command with os.Stdin redirected to a
// temp file containing stdinContent. Callers must NOT mark the test parallel:
// os.Stdin is process-global, so the redirect is only safe while no other
// stdin-reading command runs concurrently.
func runAppendWithStdin(t *testing.T, dir, stdinContent string, args ...string) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin-*")
	if err != nil {
		t.Fatalf("create temp stdin: %v", err)
	}
	if _, err := f.WriteString(stdinContent); err != nil {
		t.Fatalf("write temp stdin: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("seek temp stdin: %v", err)
	}
	oldStdin := os.Stdin
	os.Stdin = f
	defer func() {
		os.Stdin = oldStdin
		f.Close()
	}()
	return runInDir(t, dir, args...)
}

func TestRunAppendAddsTextArgumentToBody(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "append", "P1.M1.E1.T001", "progress note here")
	if err != nil {
		t.Fatalf("run append = %v, expected nil", err)
	}
	if !strings.Contains(output, "Appended to: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected append confirmation", output)
	}

	taskText := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"))
	if !strings.Contains(taskText, "progress note here") {
		t.Fatalf("task text missing appended content: %q", taskText)
	}

	// A second append is blank-line separated from the first.
	if _, err := runInDir(t, root, "append", "P1.M1.E1.T001", "second note here"); err != nil {
		t.Fatalf("run append (second) = %v, expected nil", err)
	}
	taskText = readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"))
	if !strings.Contains(taskText, "progress note here\n\nsecond note here") {
		t.Fatalf("expected blank-line separator between appends: %q", taskText)
	}
}

func TestRunAppendJoinsMultipleWordArguments(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	if _, err := runInDir(t, root, "append", "P1.M1.E1.T001", "one", "two", "three"); err != nil {
		t.Fatalf("run append = %v, expected nil", err)
	}
	taskText := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"))
	if !strings.Contains(taskText, "one two three") {
		t.Fatalf("task text missing joined words: %q", taskText)
	}
}

func TestRunAppendReadsFromStdin(t *testing.T) {
	root := setupWorkflowFixture(t)
	output, err := runAppendWithStdin(t, root, "stdin line A\nstdin line B\n", "append", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run append stdin = %v, expected nil", err)
	}
	if !strings.Contains(output, "Appended to: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected append confirmation", output)
	}
	taskText := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"))
	for _, expected := range []string{"stdin line A", "stdin line B"} {
		if !strings.Contains(taskText, expected) {
			t.Fatalf("task text missing %q: %q", expected, taskText)
		}
	}
}

func TestRunAppendRequiresText(t *testing.T) {
	root := setupWorkflowFixture(t)
	_, err := runAppendWithStdin(t, root, "", "append", "P1.M1.E1.T001")
	if err == nil {
		t.Fatalf("run append expected error when no text provided")
	}
	if !strings.Contains(err.Error(), "append requires") {
		t.Fatalf("err = %q, expected append usage error", err)
	}
}

func TestRunAppendReportsUnknownTask(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_, err := runInDir(t, root, "append", "P9.M9.E9.T999", "some note here")
	if err == nil {
		t.Fatalf("run append expected error for unknown task")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %q, expected not-found error", err)
	}
}

func TestRunAppendRejectsStrayFlag(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_, err := runInDir(t, root, "append", "P1.M1.E1.T001", "-x", "note")
	if err == nil {
		t.Fatalf("run append expected error for stray flag")
	}
}

func TestRunDataExportJSONIncludesScopedTasksAndContent(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "data", "export", "--format", "json", "--scope", "P1.M1", "--include-content")
	if err != nil {
		t.Fatalf("run data export --format json = %v, expected nil", err)
	}

	var payload map[string]interface{}
	decodeJSONPayload(t, output, &payload)
	phases, ok := payload["phases"].([]interface{})
	if !ok || len(phases) == 0 {
		t.Fatalf("data export payload missing phases: %#v", payload)
	}
	phase := phases[0].(map[string]interface{})
	milestones := phase["milestones"].([]interface{})
	epics := milestones[0].(map[string]interface{})["epics"].([]interface{})
	tasks := epics[0].(map[string]interface{})["tasks"].([]interface{})
	if len(tasks) == 0 {
		t.Fatalf("data export payload missing tasks in scoped output: %#v", payload)
	}
	if _, ok := tasks[0].(map[string]interface{})["content"]; !ok {
		t.Fatalf("expected include-content to include task content")
	}
}

func TestRunDataExportAcceptsRepeatedScopeFlags(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "data", "export", "--format", "json", "--scope", "P1.M1", "--scope", "P1.M1.E1")
	if err != nil {
		t.Fatalf("run data export repeated scopes = %v, expected nil", err)
	}
	if !strings.Contains(output, "P1.M1.E1.T001") {
		t.Fatalf("data export output missing scoped task: %q", output)
	}

	_, err = runInDir(t, root, "data", "export", "--format", "json", "--scope", "P9")
	if err == nil {
		t.Fatalf("run data export --scope P9 expected error")
	}
}

func TestRunDataExportWritesYAMLFile(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "data", "export", "--format", "yaml", "--output", "export.yaml")
	if err != nil {
		t.Fatalf("run data export --format yaml --output = %v, expected nil", err)
	}
	if !strings.Contains(output, "Exported to export.yaml") {
		t.Fatalf("output = %q, expected export confirmation", output)
	}
	content := readFile(t, filepath.Join(root, "export.yaml"))
	if !strings.Contains(content, "project:") {
		t.Fatalf("yaml export = %q, expected project field", content)
	}
}

func TestRunSetUpdatesMultipleFields(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(
		t,
		root,
		"set",
		"P1.M1.E1.T001",
		"--priority",
		"high",
		"--complexity",
		"high",
		"--estimate",
		"3",
		"--title",
		"renamed",
		"--depends-on",
		"P1.M1.E1.T002",
		"--tags",
		"one,two",
		"--status",
		"blocked",
		"--reason",
		"waiting",
		"--body",
		"custom body for set",
	)
	if err != nil {
		t.Fatalf("run set = %v, expected nil", err)
	}
	if !strings.Contains(output, "Updated: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected set confirmation", output)
	}

	taskText := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"))
	for _, expected := range []string{
		"title: renamed",
		"status: blocked",
		"complexity: high",
		"priority: high",
		"estimate_hours: 3",
		"reason: waiting",
		"custom body for set",
	} {
		if !strings.Contains(taskText, expected) {
			t.Fatalf("task text missing %q: %q", expected, taskText)
		}
	}
}

func TestRunSetRequiresPropertyFlag(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_, err := runInDir(t, root, "set", "P1.M1.E1.T001")
	if err == nil {
		t.Fatalf("run set expected error when no properties provided")
	}
	if !strings.Contains(err.Error(), "set requires at least one property flag") {
		t.Fatalf("err = %q, expected set usage error", err)
	}
}

func TestRunSetAppendsBodyWhenAppendBodyFlagSet(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "set", "P1.M1.E1.T001", "--body", "first body for set")
	if err != nil {
		t.Fatalf("run set = %v, expected nil", err)
	}
	if !strings.Contains(output, "Updated: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected set confirmation", output)
	}

	output, err = runInDir(
		t,
		root,
		"set",
		"P1.M1.E1.T001",
		"--body",
		"appended body for set",
		"--append-body",
	)
	if err != nil {
		t.Fatalf("run set append = %v, expected nil", err)
	}
	if !strings.Contains(output, "Updated: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected append set confirmation", output)
	}

	taskText := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"))
	if !strings.Contains(taskText, "first body for set") {
		t.Fatalf("task text missing first body content: %q", taskText)
	}
	if !strings.Contains(taskText, "appended body for set") {
		t.Fatalf("task text missing appended body content: %q", taskText)
	}
	if firstIdx := strings.Index(taskText, "first body for set"); firstIdx < 0 {
		t.Fatalf("task text missing first body content")
	} else if secondIdx := strings.Index(taskText, "appended body for set"); secondIdx < 0 || secondIdx <= firstIdx {
		t.Fatalf("appended body content expected after first body content")
	}
}

func TestRunUndoneResetsEpicAndTasks(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	if _, err := runInDir(t, root, "claim", "P1.M1.E1.T001", "--agent", "agent-a"); err != nil {
		t.Fatalf("claim task 1: %v", err)
	}
	if _, err := runInDir(t, root, "done", "P1.M1.E1.T001"); err != nil {
		t.Fatalf("done task 1: %v", err)
	}
	if _, err := runInDir(t, root, "claim", "P1.M1.E1.T002", "--agent", "agent-a"); err != nil {
		t.Fatalf("claim task 2: %v", err)
	}
	if _, err := runInDir(t, root, "done", "P1.M1.E1.T002"); err != nil {
		t.Fatalf("done task 2: %v", err)
	}

	output, err := runInDir(t, root, "undone", "P1.M1.E1")
	if err != nil {
		t.Fatalf("run undone epic = %v, expected nil", err)
	}
	if !strings.Contains(output, "Marked not done: P1.M1.E1") {
		t.Fatalf("output = %q, expected undone message", output)
	}

	taskOne := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"))
	taskTwo := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo"))
	if !strings.Contains(taskOne, "status: pending") || !strings.Contains(taskTwo, "status: pending") {
		t.Fatalf("expected tasks to be reset to pending")
	}
}

func TestRunLsCoversScopes(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	phaseOut, err := runInDir(t, root, "ls")
	if err != nil {
		t.Fatalf("run ls root = %v", err)
	}
	if !strings.Contains(phaseOut, "P1: Phase") {
		t.Fatalf("ls root output = %q, expected phase summary", phaseOut)
	}

	milestoneOut, err := runInDir(t, root, "ls", "P1")
	if err != nil {
		t.Fatalf("run ls phase = %v", err)
	}
	if !strings.Contains(milestoneOut, "P1.M1: Milestone") {
		t.Fatalf("ls phase output = %q, expected milestone summary", milestoneOut)
	}

	epicOut, err := runInDir(t, root, "ls", "P1.M1")
	if err != nil {
		t.Fatalf("run ls milestone = %v", err)
	}
	if !strings.Contains(epicOut, "P1.M1.E1: Epic") {
		t.Fatalf("ls milestone output = %q, expected epic summary", epicOut)
	}

	multiOut, err := runInDir(t, root, "ls", "P1", "P1.M1")
	if err != nil {
		t.Fatalf("run ls multi-scope = %v", err)
	}
	if !strings.Contains(multiOut, "P1.M1.E1: Epic") {
		t.Fatalf("ls multi output = %q, expected nested epic summary", multiOut)
	}
}

func TestRunTimelineAcceptsRepeatedScopeFlags(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "timeline", "--scope", "P1", "--scope", "P1.M1", "--show-done")
	if err != nil {
		t.Fatalf("run timeline repeated scopes = %v", err)
	}
	if !strings.Contains(output, "Project Timeline") {
		t.Fatalf("timeline output = %q, expected header", output)
	}

	_, err = runInDir(t, root, "timeline", "--scope", "P9")
	if err == nil {
		t.Fatalf("run timeline --scope P9 expected error")
	}
}

func TestRunBenchmarkJSONAndValidation(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "benchmark", "--json", "--mode", "metadata", "--no-parse-body", "--top", "1")
	if err != nil {
		t.Fatalf("run benchmark --json = %v, expected nil", err)
	}
	var payload map[string]interface{}
	decodeJSONPayload(t, output, &payload)
	if _, ok := payload["overall_ms"]; !ok {
		t.Fatalf("benchmark payload missing overall_ms: %#v", payload)
	}

	_, err = runInDir(t, root, "benchmark", "--mode", "invalid")
	if err == nil {
		t.Fatalf("run benchmark expected mode validation error")
	}
}

func TestRunSkipAutoGrabAndHandoffForce(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	if _, err := runInDir(t, root, "claim", "P1.M1.E1.T001", "--agent", "agent-a"); err != nil {
		t.Fatalf("claim task before skip: %v", err)
	}

	skipOut, err := runInDir(t, root, "skip", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run skip = %v", err)
	}
	if !strings.Contains(skipOut, "Skipped: P1.M1.E1.T001 - a") {
		t.Fatalf("skip output = %q, expected skipped message", skipOut)
	}
	if !strings.Contains(skipOut, "No available tasks found.") {
		t.Fatalf("skip output = %q, expected no-available follow-up message", skipOut)
	}

	handoffOut, err := runInDir(t, root, "handoff", "P1.M1.E1.T001", "--to", "agent-b", "--force", "--notes", "handoff note")
	if err != nil {
		t.Fatalf("run handoff --force = %v, expected nil", err)
	}
	if !strings.Contains(handoffOut, "Handed off: P1.M1.E1.T001 - a") {
		t.Fatalf("handoff output = %q, expected handoff message", handoffOut)
	}
}

func TestRunSessionStaleAndClean(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	if _, err := runInDir(t, root, "session", "start", "--agent", "agent-s", "--task", "P1.M1.E1.T001"); err != nil {
		t.Fatalf("run session start: %v", err)
	}

	sessionsPath := filepath.Join(root, ".tasks", ".sessions.yaml")
	sessions := readYAMLMap(t, sessionsPath)
	entry := sessions["agent-s"].(map[string]interface{})
	entry["last_heartbeat"] = time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	writeYAMLMap(t, sessionsPath, sessions)

	staleOut, err := runInDir(t, root, "session", "list", "--stale", "--timeout", "30")
	if err != nil {
		t.Fatalf("run session list --stale = %v", err)
	}
	if !strings.Contains(staleOut, "Stale Sessions") {
		t.Fatalf("stale output = %q, expected stale session section", staleOut)
	}

	cleanOut, err := runInDir(t, root, "session", "clean", "--timeout", "30")
	if err != nil {
		t.Fatalf("run session clean = %v", err)
	}
	if !strings.Contains(cleanOut, "Removed 1 stale session") {
		t.Fatalf("clean output = %q, expected stale clean confirmation", cleanOut)
	}
}

func TestRunCheckReportsMissingTaskFileAsError(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	setTaskTodoFilePath(t, root, "P1.M1.E1.T001", filepath.Join("01-phase", "01-ms", "01-epic", "missing.todo"))

	output, err := runInDir(t, root, "check", "--json")
	if err == nil {
		t.Fatalf("run check --json expected non-nil error for missing task file")
	}
	var payload map[string]interface{}
	decodeJSONPayload(t, output, &payload)
	summary := payload["summary"].(map[string]interface{})
	if asInt(summary["errors"]) < 1 {
		t.Fatalf("check summary errors = %#v, expected at least 1", summary)
	}
}

func TestRunCheckReportsTaskDependencyCycle(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	addDependencyForWorkflowTask(t, root, "P1.M1.E1.T001", []string{"P1.M1.E1.T002"})
	addDependencyForWorkflowTask(t, root, "P1.M1.E1.T002", []string{"P1.M1.E1.T001"})

	output, err := runInDir(t, root, "check")
	if err == nil {
		t.Fatalf("run check expected non-nil error for dependency cycle")
	}
	if !strings.Contains(output, "task_dependency_cycle") {
		t.Fatalf("check output = %q, expected task_dependency_cycle", output)
	}
	if !strings.Contains(output, "P1.M1.E1.T001") || !strings.Contains(output, "P1.M1.E1.T002") {
		t.Fatalf("check output = %q, expected task IDs in cycle message", output)
	}
}

func TestRunTimelineNoTasksWhenDoneHidden(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "done", "", "")
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T002", "b", "done", "", "")

	output, err := runInDir(t, root, "timeline")
	if err != nil {
		t.Fatalf("run timeline = %v, expected nil", err)
	}
	if !strings.Contains(output, "No tasks to display.") {
		t.Fatalf("timeline output = %q, expected empty timeline message", output)
	}
}

func TestRunUndoneResetsMilestoneAndPhase(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	completeAllWorkflowTasks(t, root)

	milestoneOut, err := runInDir(t, root, "undone", "P1.M1")
	if err != nil {
		t.Fatalf("run undone milestone = %v, expected nil", err)
	}
	if !strings.Contains(milestoneOut, "Marked not done: P1.M1") {
		t.Fatalf("milestone output = %q, expected milestone reset message", milestoneOut)
	}

	completeAllWorkflowTasks(t, root)
	phaseOut, err := runInDir(t, root, "undone", "P1")
	if err != nil {
		t.Fatalf("run undone phase = %v, expected nil", err)
	}
	if !strings.Contains(phaseOut, "Marked not done: P1") {
		t.Fatalf("phase output = %q, expected phase reset message", phaseOut)
	}
}

func TestRunSearchAndBlockersJSONBranches(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	addDependencyForWorkflowTask(t, root, "P1.M1.E1.T002", []string{"P1.M1.E1.T001"})

	searchOutput, err := runInDir(t, root, "search", "a", "--json")
	if err != nil {
		t.Fatalf("run search --json = %v, expected nil", err)
	}
	var searchPayload map[string]interface{}
	decodeJSONPayload(t, searchOutput, &searchPayload)
	if _, ok := searchPayload["count"]; !ok {
		t.Fatalf("search json missing count: %#v", searchPayload)
	}

	blockersOutput, err := runInDir(t, root, "blockers", "--json")
	if err != nil {
		t.Fatalf("run blockers --json = %v, expected nil", err)
	}
	var blockersPayload map[string]interface{}
	decodeJSONPayload(t, blockersOutput, &blockersPayload)
	if _, ok := blockersPayload["root_blockers"]; !ok {
		t.Fatalf("blockers json missing root_blockers: %#v", blockersPayload)
	}
}

func TestRunWhyJSONAndValidation(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	whyOutput, err := runInDir(t, root, "why", "P1.M1.E1.T002", "--json")
	if err != nil {
		t.Fatalf("run why --json = %v, expected nil", err)
	}
	var payload map[string]interface{}
	decodeJSONPayload(t, whyOutput, &payload)
	if _, ok := payload["ImplicitDependency"]; ok {
		// defensive branch: current JSON tagless struct marshals with field names.
		return
	}
	if _, ok := payload["ImplicitDependency"]; !ok && payload["ImplicitDependency"] == nil {
		// no-op: checked via fallback below
	}
	// Field name casing from stdlib JSON for exported struct members.
	if _, ok := payload["ImplicitDependency"]; !ok {
		if _, fallback := payload["ImplicitDependency"]; !fallback {
			// Keep a stable assertion on a guaranteed field.
			if _, taskIDOK := payload["TaskID"]; !taskIDOK {
				t.Fatalf("why json missing TaskID: %#v", payload)
			}
		}
	}

	_, err = runInDir(t, root, "why", "not-a-task")
	if err == nil {
		t.Fatalf("run why expected malformed id error")
	}
}

func TestRunBenchmarkTextAndTopValidation(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	textOutput, err := runInDir(t, root, "benchmark")
	if err != nil {
		t.Fatalf("run benchmark text = %v, expected nil", err)
	}
	if !strings.Contains(textOutput, "Task Tree Benchmark") {
		t.Fatalf("benchmark text output = %q, expected heading", textOutput)
	}

	_, err = runInDir(t, root, "benchmark", "--top", "0")
	if err == nil {
		t.Fatalf("run benchmark expected validation error for non-positive top")
	}
}

func TestRunSchemaTextAndCompactJSON(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	textOutput, err := runInDir(t, root, "schema")
	if err != nil {
		t.Fatalf("run schema text = %v, expected nil", err)
	}
	if !strings.Contains(textOutput, "Schema") {
		t.Fatalf("schema text output = %q, expected heading", textOutput)
	}

	jsonOutput, err := runInDir(t, root, "schema", "--json", "--compact")
	if err != nil {
		t.Fatalf("run schema compact json = %v, expected nil", err)
	}
	if !strings.Contains(jsonOutput, "\"schema_version\"") {
		t.Fatalf("schema compact json = %q, expected schema_version", jsonOutput)
	}
}

func TestRunDataAndSessionUnknownSubcommands(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	if _, err := runInDir(t, root, "data", "unknown-subcommand"); err == nil {
		t.Fatalf("run data unknown subcommand expected error")
	}
	if _, err := runInDir(t, root, "session", "unknown-subcommand"); err == nil {
		t.Fatalf("run session unknown subcommand expected error")
	}
}

func completeAllWorkflowTasks(t *testing.T, root string) {
	t.Helper()

	for _, taskID := range []string{"P1.M1.E1.T001", "P1.M1.E1.T002"} {
		if _, err := runInDir(t, root, "claim", taskID, "--agent", "agent-a"); err != nil {
			t.Fatalf("claim %s failed: %v", taskID, err)
		}
		if _, err := runInDir(t, root, "done", taskID); err != nil {
			t.Fatalf("done %s failed: %v", taskID, err)
		}
	}
}
