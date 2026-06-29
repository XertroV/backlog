package runner

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taskcontext "github.com/XertroV/tasks/backlog_go/internal/context"
	"github.com/XertroV/tasks/backlog_go/internal/critical_path"
	"github.com/XertroV/tasks/backlog_go/internal/loader"
	"github.com/XertroV/tasks/backlog_go/internal/models"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	runInDirMu.Lock()
	defer runInDirMu.Unlock()

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = writer

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, reader)
		close(done)
	}()

	fn()

	_ = writer.Close()
	os.Stdout = oldStdout
	<-done
	return buf.String()
}

func mustRun(t *testing.T, root string, args ...string) string {
	t.Helper()
	output, err := runInDir(t, root, args...)
	if err != nil {
		t.Fatalf("run %v = %v", args, err)
	}
	return output
}

func assertContainsAll(t *testing.T, value string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(value, want) {
			t.Fatalf("output = %q, expected to contain %q", value, want)
		}
	}
}

func TestRunCycleAutoGrabsNextTask(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_ = mustRun(t, root, "claim", "P1.M1.E1.T001")

	output := mustRun(t, root, "cycle", "P1.M1.E1.T001")
	assertContainsAll(t, output, "Completed: P1.M1.E1.T001 - a", "Grabbed: P1.M1.E1.T002 - b", "/01-phase/01-ms/01-epic/T002-b.todo", "-----== EOF ==-----")

	dataDir := filepath.Join(root, ".tasks")
	currentTask, err := taskcontext.GetCurrentTask(dataDir)
	if err != nil {
		t.Fatalf("GetCurrentTask() = %v", err)
	}
	if currentTask != "P1.M1.E1.T002" {
		t.Fatalf("current task = %q, expected P1.M1.E1.T002", currentTask)
	}
}

func TestRunCycleAdvancesSiblingContext(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_ = mustRun(t, root, "claim", "P1.M1.E1.T001")
	dataDir := filepath.Join(root, ".tasks")
	if err := taskcontext.SetSiblingTaskContext(dataDir, "cli-user", "P1.M1.E1.T001", []string{"P1.M1.E1.T002"}); err != nil {
		t.Fatalf("SetSiblingTaskContext() = %v", err)
	}

	output := mustRun(t, root, "cycle")
	assertContainsAll(t, output, "Primary sibling completed. Next sibling: P1.M1.E1.T002")

	ctx, err := taskcontext.LoadContext(dataDir)
	if err != nil {
		t.Fatalf("LoadContext() = %v", err)
	}
	if ctx.CurrentTask != "P1.M1.E1.T002" {
		t.Fatalf("context current task = %q, expected P1.M1.E1.T002", ctx.CurrentTask)
	}
}

func TestAdvanceCycleContextBranches(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	dataDir := filepath.Join(root, ".tasks")

	if err := taskcontext.SetSiblingTaskContext(dataDir, "cli-user", "P1.M1.E1.T001", []string{"P1.M1.E1.T002", "P1.M1.E1.T003"}); err != nil {
		t.Fatalf("SetSiblingTaskContext() = %v", err)
	}
	handled, err := advanceCycleContext("P1.M1.E1.T002", "cli-user", dataDir)
	if err != nil {
		t.Fatalf("advanceCycleContext(sibling) = %v", err)
	}
	if !handled {
		t.Fatal("expected sibling context to be handled")
	}

	ctx, err := taskcontext.LoadContext(dataDir)
	if err != nil {
		t.Fatalf("LoadContext() = %v", err)
	}
	if ctx.PrimaryTask != "P1.M1.E1.T001" || len(ctx.SiblingTasks) != 1 || ctx.SiblingTasks[0] != "P1.M1.E1.T003" {
		t.Fatalf("unexpected sibling context after completion: %+v", ctx)
	}

	if err := taskcontext.SetMultiTaskContext(dataDir, "cli-user", "P1.M1.E1.T001", []string{"P1.M1.E1.T002"}); err != nil {
		t.Fatalf("SetMultiTaskContext() = %v", err)
	}
	handled, err = advanceCycleContext("P1.M1.E1.T001", "cli-user", dataDir)
	if err != nil {
		t.Fatalf("advanceCycleContext(multi primary) = %v", err)
	}
	if !handled {
		t.Fatal("expected multi context to be handled")
	}

	ctx, err = taskcontext.LoadContext(dataDir)
	if err != nil {
		t.Fatalf("LoadContext() = %v", err)
	}
	if ctx.CurrentTask != "P1.M1.E1.T002" {
		t.Fatalf("multi context current task = %q, expected P1.M1.E1.T002", ctx.CurrentTask)
	}
}

func TestRunListAvailableAndProgressViews(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)

	availableOut := mustRun(t, root, "list", "--available")
	assertContainsAll(t, availableOut, "Primary Phase")

	availableJSON := mustRun(t, root, "list", "--available", "--json", "--bugs")
	var payload map[string]interface{}
	decodeJSONPayload(t, availableJSON, &payload)
	entries, ok := payload["available"].([]interface{})
	if !ok {
		t.Fatalf("available payload missing available list: %#v", payload)
	}
	for _, raw := range entries {
		entry := raw.(map[string]interface{})
		if !strings.HasPrefix(asString(entry["id"]), "B") {
			t.Fatalf("expected only bugs in bug-only available output, got %v", entry["id"])
		}
	}

	progressOut := mustRun(t, root, "list", "--progress", "--phase", "P1")
	assertContainsAll(t, progressOut, "Project Progress", "P1 (")
}

func TestRenderListProgressNoNodesMessage(t *testing.T) {
	output := captureStdout(t, func() {
		err := renderListProgress(
			models.TaskTree{},
			nil,
			true,
			nil,
			"",
			"",
			"",
			"",
			0,
			func(models.Task) bool { return true },
		)
		if err != nil {
			t.Fatalf("renderListProgress() = %v", err)
		}
	})

	if !strings.Contains(output, "No list nodes found for path query: unknown") {
		t.Fatalf("output = %q, expected no-nodes message", output)
	}
}

func TestRunTreePathQueryFiltering(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	filteredOut := mustRun(t, root, "tree", "P1.M1.E1")
	assertContainsAll(t, filteredOut, "Epic One")

	rootNoAux := setupWorkflowFixture(t)
	emptyOut := mustRun(t, rootNoAux, "tree", "P9")
	assertContainsAll(t, emptyOut, "No tree nodes found for path query: P9")
}

func TestRunLsTaskSummaryAndMissingFile(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	summaryOut := mustRun(t, root, "ls", "P1.M1.E1.T001")
	assertContainsAll(t, summaryOut, "Task: P1.M1.E1.T001 - a", "Frontmatter:")

	taskPath := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo")
	if err := os.Remove(taskPath); err != nil {
		t.Fatalf("remove task file: %v", err)
	}
	missingOut := mustRun(t, root, "ls", "P1.M1.E1.T001")
	assertContainsAll(t, missingOut, "Task file missing")
}

func TestRunShowAuxiliaryAndErrorBranch(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	ideaOut := mustRun(t, root, "show", "I1")
	assertContainsAll(t, ideaOut, "Idea instructions")

	bugOut := mustRun(t, root, "show", "B1")
	assertContainsAll(t, bugOut, "Status:", "Next:")

	_, err := runInDir(t, root, "show", "B99")
	if err == nil {
		t.Fatal("show B99 expected task-not-found error")
	}
}

func TestRunClaimRejectsUnknownFlag(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_, err := runInDir(t, root, "claim", "P1.M1.E1.T001", "--bogus")
	if err == nil {
		t.Fatal("claim with unknown flag expected error")
	}
	if !strings.Contains(err.Error(), "unexpected flag: --bogus") {
		t.Fatalf("error = %q, expected unknown-flag message", err)
	}
}

func TestShowNotFoundAndMiscHelpers(t *testing.T) {
	output := captureStdout(t, func() {
		if err := printCommandNotImplemented("unknown"); err != nil {
			t.Fatalf("printCommandNotImplemented() = %v", err)
		}
	})
	if !strings.Contains(output, "unknown command is not implemented yet") {
		t.Fatalf("output = %q, expected not-implemented message", output)
	}

	if got := leafID("P1.M1.E1.T001"); got != "T001" {
		t.Fatalf("leafID() = %q, expected T001", got)
	}
	if got := leafID("single"); got != "single" {
		t.Fatalf("leafID(single) = %q, expected single", got)
	}

	path := phasePathFromID(".P1.M1")
	if path == nil || path.FullID() != "P1.M1" {
		t.Fatalf("phasePathFromID() = %#v, expected P1.M1", path)
	}
	if phasePathFromID("P1") != nil {
		t.Fatalf("phasePathFromID() should return nil for non-dotted input")
	}
}

func TestFindIndependentCandidatesAndScopes(t *testing.T) {
	tree := models.TaskTree{
		Phases: []models.Phase{
			{
				ID: "P1",
				Milestones: []models.Milestone{
					{
						ID: "P1.M1",
						Epics: []models.Epic{
							{
								ID: "P1.M1.E1",
								Tasks: []models.Task{
									{
										ID:         "P1.M1.E1.T001",
										Title:      "primary",
										Status:     models.StatusPending,
										Complexity: models.ComplexityLow,
										Priority:   models.PriorityHigh,
										EpicID:     "P1.M1.E1",
									},
								},
							},
							{
								ID: "P1.M1.E2",
								Tasks: []models.Task{
									{
										ID:         "P1.M1.E2.T001",
										Title:      "independent",
										Status:     models.StatusPending,
										Complexity: models.ComplexityLow,
										Priority:   models.PriorityMedium,
										EpicID:     "P1.M1.E2",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	calc := critical_path.NewCriticalPathCalculator(tree, nil)
	primary := *tree.FindTask("P1.M1.E1.T001")
	candidates, err := findIndependentCandidates(tree, calc, primary, 3)
	if err != nil {
		t.Fatalf("findIndependentCandidates() = %v", err)
	}
	if len(candidates) != 1 || candidates[0] != "P1.M1.E2.T001" {
		t.Fatalf("candidates = %v, expected [P1.M1.E2.T001]", candidates)
	}

	phases, scopedTaskIDs, depth, scopeID := resolveListScopeByPhase(tree, "P1")
	if len(phases) != 1 || depth != 1 || scopeID != "P1" {
		t.Fatalf("resolveListScopeByPhase() = phases=%d depth=%d scopeID=%q", len(phases), depth, scopeID)
	}
	if len(scopedTaskIDs) != 2 {
		t.Fatalf("collectPhaseTaskIDs output = %v, expected 2 tasks", scopedTaskIDs)
	}
}

func TestRunGrabModesAndScope(t *testing.T) {
	t.Parallel()

	rootExplicit := setupWorkflowFixture(t)
	explicitOut := mustRun(t, rootExplicit, "grab", "P1.M1.E1.T001", "P1.M1.E1.T002", "--agent", "agent-x")
	assertContainsAll(
		t,
		explicitOut,
		"✓ Claimed: P1.M1.E1.T001 - a",
		"✓ Claimed: P1.M1.E1.T002 - b",
		"/01-phase/01-ms/01-epic/T001-a.todo",
		"/01-phase/01-ms/01-epic/T002-b.todo",
		"-----== EOF ==-----",
	)

	ctx, err := taskcontext.LoadContext(filepath.Join(rootExplicit, ".tasks"))
	if err != nil {
		t.Fatalf("LoadContext() = %v", err)
	}
	if ctx.Mode != "multi" || ctx.PrimaryTask != "P1.M1.E1.T001" {
		t.Fatalf("unexpected context after explicit multi-grab: %+v", ctx)
	}

	rootSingle := setupWorkflowFixture(t)
	singleOut := mustRun(t, rootSingle, "grab", "--single")
	assertContainsAll(
		t,
		singleOut,
		"Grabbed: P1.M1.E1.T001 - a",
		"it only claims one task at a time",
		"Consider dropping `--single`",
	)

	rootScope := setupWorkflowFixture(t)
	_, err = runInDir(t, rootScope, "grab", "--scope", "P9")
	if err == nil {
		t.Fatalf("run grab --scope P9 expected error")
	}
	if !strings.Contains(err.Error(), "No list nodes found for path query: P9") {
		t.Fatalf("err = %q, expected missing path query error", err)
	}

	rootMultiScope := setupWorkflowFixture(t)
	_, err = runInDir(t, rootMultiScope, "grab", "--scope", "P1.M1", "--scope", "P9")
	if err == nil {
		t.Fatalf("run grab --scope P1.M1 --scope P9 expected error")
	}
	if !strings.Contains(err.Error(), "No list nodes found for path query: P9") {
		t.Fatalf("err = %q, expected missing path query error", err)
	}
}

func TestRunGrabMultiClaimsAdditionalTasks(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output := mustRun(t, root, "grab", "--multi", "--count", "2")
	assertContainsAll(t, output, "Also grabbed")
}

func TestRunWorkSetShowAndClear(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	setOut := mustRun(t, root, "work", "P1.M1.E1.T001")
	assertContainsAll(t, setOut, "Working task set: P1.M1.E1.T001 - a")

	showOut := mustRun(t, root, "work")
	assertContainsAll(t, showOut, "Current Working Task", "ID: P1.M1.E1.T001")

	clearOut := mustRun(t, root, "work", "--clear")
	assertContainsAll(t, clearOut, "Cleared working task context.")
}

func TestRunBlockedAutoGrabAndUnclaimFromContext(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	_ = mustRun(t, root, "claim", "P1.M1.E1.T001", "--agent", "agent-z")

	blockedOut := mustRun(t, root, "blocked", "P1.M1.E1.T001", "--reason", "waiting", "--grab", "--agent", "agent-z")
	assertContainsAll(t, blockedOut, "Blocked: P1.M1.E1.T001 (waiting)", "Grabbed:", "-----== EOF ==-----")

	unclaimOut := mustRun(t, root, "unclaim")
	assertContainsAll(t, unclaimOut, "Unclaimed:")
}

func TestRunBlockedRejectsNoGrabFlag(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_, err := runInDir(t, root, "blocked", "P1.M1.E1.T001", "--reason", "waiting", "--no-grab")
	if err == nil {
		t.Fatal("blocked --no-grab expected flag validation error")
	}
	if !strings.Contains(err.Error(), "unexpected flag: --no-grab") {
		t.Fatalf("error = %q, expected unexpected-flag message", err)
	}
}

func TestRunDoneSupportsMultipleTaskIDsAndVerifyFlag(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_ = mustRun(t, root, "claim", "P1.M1.E1.T001")
	_ = mustRun(t, root, "claim", "P1.M1.E1.T002")

	output := mustRun(t, root, "done", "P1.M1.E1.T001", "P1.M1.E1.T002", "--verify")
	assertContainsAll(t, output, "Completed: P1.M1.E1.T001 - a", "Completed: P1.M1.E1.T002 - b")

	taskOne := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"))
	taskTwo := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo"))
	assertContainsAll(t, taskOne, "status: done")
	assertContainsAll(t, taskTwo, "status: done")
}

func TestRunSkipPendingUnclaimedIsNoop(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output := mustRun(t, root, "skip", "P1.M1.E1.T001")
	assertContainsAll(t, output, "Task is not in progress: pending")
}

func TestRunPreviewTextOutput(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	previewOut := mustRun(t, root, "preview")
	assertContainsAll(t, previewOut, "Preview available work:", "Normal Tasks")
}

func TestShowScopedItemBranches(t *testing.T) {
	root := setupListAuxAndScopeFixture(t)
	tree, err := loader.New(filepath.Join(root, ".tasks")).Load("metadata", true, true)
	if err != nil {
		t.Fatalf("load tree = %v", err)
	}

	dataDir := filepath.Join(root, ".tasks")
	phasePath := models.ForPhase("P1")
	phaseOut := captureStdout(t, func() {
		if err := showScopedItem(tree, "P1", &phasePath, dataDir, true, false, false); err != nil {
			t.Fatalf("showScopedItem(phase) = %v", err)
		}
	})
	if !strings.Contains(phaseOut, "P1: Primary Phase") {
		t.Fatalf("phase output = %q, expected phase summary", phaseOut)
	}

	milestonePath, _ := models.ParseTaskPath("P1.M1")
	milestoneOut := captureStdout(t, func() {
		if err := showScopedItem(tree, "P1.M1", &milestonePath, dataDir, true, false, false); err != nil {
			t.Fatalf("showScopedItem(milestone) = %v", err)
		}
	})
	if !strings.Contains(milestoneOut, "P1.M1: Milestone One") {
		t.Fatalf("milestone output = %q, expected milestone summary", milestoneOut)
	}

	epicPath, _ := models.ParseTaskPath("P1.M1.E1")
	epicOut := captureStdout(t, func() {
		if err := showScopedItem(tree, "P1.M1.E1", &epicPath, dataDir, true, false, false); err != nil {
			t.Fatalf("showScopedItem(epic) = %v", err)
		}
	})
	if !strings.Contains(epicOut, "P1.M1.E1: Epic One") {
		t.Fatalf("epic output = %q, expected epic summary", epicOut)
	}

	taskPath, _ := models.ParseTaskPath("P1.M1.E1.T001")
	taskOut := captureStdout(t, func() {
		if err := showScopedItem(tree, "P1.M1.E1.T001", &taskPath, dataDir, true, false, false); err != nil {
			t.Fatalf("showScopedItem(task) = %v", err)
		}
	})
	if !strings.Contains(taskOut, "Task: P1.M1.E1.T001") {
		t.Fatalf("task output = %q, expected task detail", taskOut)
	}

	missingPath := models.ForPhase("P9")
	missingOut := captureStdout(t, func() {
		err := showScopedItem(tree, "P9", &missingPath, dataDir, true, false, false)
		if err == nil {
			t.Fatal("showScopedItem missing phase expected error")
		}
	})
	if !strings.Contains(missingOut, "Error: Phase not found: P9") {
		t.Fatalf("missing phase output = %q, expected not-found message", missingOut)
	}
}

func TestRunnerIndexAndSliceHelpers(t *testing.T) {
	if got := nextIndexID([]string{"T001", "T009", "T010"}, "T"); got != 11 {
		t.Fatalf("nextIndexID() = %d, expected 11", got)
	}
	if got := nextIndexID([]string{"E1", "E4"}, "E"); got != 5 {
		t.Fatalf("nextIndexID(E) = %d, expected 5", got)
	}

	values := asSlice([]map[string]interface{}{{"id": "T001"}})
	if len(values) != 1 {
		t.Fatalf("asSlice([]map) length = %d, expected 1", len(values))
	}
	if len(asSlice("not-a-slice")) != 0 {
		t.Fatal("asSlice(non-slice) expected empty result")
	}

	taskIDs := extractTaskLeafIDs([]interface{}{
		map[string]interface{}{"id": "T001"},
		map[string]interface{}{"id": "T002"},
	})
	if len(taskIDs) != 2 || taskIDs[0] != "T001" || taskIDs[1] != "T002" {
		t.Fatalf("extractTaskLeafIDs() = %v, expected [T001 T002]", taskIDs)
	}

	epicIDs := extractEpicOrMilestoneLeafIDs([]interface{}{
		map[string]interface{}{"id": "E1"},
		map[string]interface{}{"id": "M1"},
	}, "E")
	if len(epicIDs) != 1 || epicIDs[0] != "E1" {
		t.Fatalf("extractEpicOrMilestoneLeafIDs() = %v, expected [E1]", epicIDs)
	}
}

func TestShowNotFoundPrefixedNumberAndUnfinishedFilter(t *testing.T) {
	tree := models.TaskTree{
		Phases: []models.Phase{
			{
				ID: "P1",
				Milestones: []models.Milestone{
					{
						ID: "P1.M1",
						Epics: []models.Epic{
							{ID: "P1.M1.E1", Tasks: []models.Task{{ID: "P1.M1.E1.T001"}}},
						},
					},
				},
			},
		},
	}
	output := captureStdout(t, func() {
		err := showNotFound(tree, "Task", "P9.M1.E1.T001", "P9")
		if err == nil {
			t.Fatal("showNotFound() expected non-nil error")
		}
	})
	if !strings.Contains(output, "Error: Task not found: P9.M1.E1.T001") {
		t.Fatalf("showNotFound output = %q", output)
	}

	if got := prefixedNumber("B012-bug.todo", "B"); got != 12 {
		t.Fatalf("prefixedNumber() = %d, expected 12", got)
	}
	if got := prefixedNumber("note.md", "B"); got != 0 {
		t.Fatalf("prefixedNumber(non-prefixed) = %d, expected 0", got)
	}

	filtered := filterUnfinishedTasks([]models.Task{
		{ID: "P1.M1.E1.T001", Status: models.StatusDone},
		{ID: "P1.M1.E1.T002", Status: models.StatusPending},
		{ID: "P1.M1.E1.T003", Status: models.StatusInProgress},
		{ID: "P1.M1.E1.T004", Status: models.StatusRejected},
	})
	if len(filtered) != 2 {
		t.Fatalf("filterUnfinishedTasks() returned %d tasks, expected 2", len(filtered))
	}
}
