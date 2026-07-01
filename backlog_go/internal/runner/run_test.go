package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	taskcontext "github.com/XertroV/tasks/backlog_go/internal/context"
	"github.com/XertroV/tasks/backlog_go/internal/models"
	"gopkg.in/yaml.v3"
)

var runInDirMu sync.Mutex

func TestRunReturnsNil(t *testing.T) {
	t.Parallel()
	runInDirMu.Lock()
	defer runInDirMu.Unlock()

	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()
	os.Args = []string{"backlog"}

	if err := Run(); err != nil {
		t.Fatalf("Run() = %v, expected nil", err)
	}
}

func TestRunNoArgsShowsUsage(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(t, root)
	if err != nil {
		t.Fatalf("run no args = %v, expected nil", err)
	}
	if !strings.Contains(output, "Usage:") {
		t.Fatalf("output = %q, expected usage line", output)
	}
}

func TestRenderStack2Deterministic(t *testing.T) {
	t.Parallel()

	linesA := renderStack2(80, 3, false)
	linesB := renderStack2(80, 3, false)
	if !reflect.DeepEqual(linesA, linesB) {
		t.Fatalf("renderStack2 output should be deterministic: %#v != %#v", linesA, linesB)
	}
	if len(linesA) < 10 {
		t.Fatalf("renderStack2 output should contain multiple lines: %d", len(linesA))
	}
}

func TestRenderStack2SupportsNoColor(t *testing.T) {
	t.Parallel()

	lines := renderStack2(80, 3, false)
	for _, line := range lines {
		if strings.Contains(line, "\x1b[") {
			t.Fatalf("expected plain line without ansi escape: %q", line)
		}
	}
}

func TestRenderStack2SupportsColor(t *testing.T) {
	t.Parallel()

	lines := renderStack2(80, 3, true)
	hasColor := false
	for _, line := range lines {
		if strings.Contains(line, "\x1b[") {
			hasColor = true
			break
		}
	}
	if !hasColor {
		t.Fatal("expected at least one ansi escape in color logo output")
	}
}

func TestRenderStack2Seed3PlainMatchesPython(t *testing.T) {
	t.Parallel()

	lines := renderStack2(80, 3, false)
	if len(lines) != len(startupLogoPythonStack2Plain) {
		t.Fatalf("renderStack2 line count = %d, expected %d", len(lines), len(startupLogoPythonStack2Plain))
	}
	for i := range startupLogoPythonStack2Plain {
		if lines[i] != startupLogoPythonStack2Plain[i] {
			t.Fatalf("plain line %d mismatch:\nexpected %q\ngot      %q", i+1, startupLogoPythonStack2Plain[i], lines[i])
		}
	}
}

func TestRenderStack2Seed3ColorMatchesPython(t *testing.T) {
	t.Parallel()

	lines := renderStack2(80, 3, true)
	if len(lines) != len(startupLogoPythonStack2Color) {
		t.Fatalf("renderStack2 color line count = %d, expected %d", len(lines), len(startupLogoPythonStack2Color))
	}
	for i := range startupLogoPythonStack2Color {
		if lines[i] != startupLogoPythonStack2Color[i] {
			t.Fatalf("color line %d mismatch:\nexpected %q\ngot      %q", i+1, startupLogoPythonStack2Color[i], lines[i])
		}
	}
}

func TestRunAddCommand(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)

	output, err := runInDir(t, root, "add", "P1.M1.E1", "--title", "New Task")
	if err != nil {
		t.Fatalf("run add = %v, expected nil", err)
	}
	if !strings.Contains(output, "Created task: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected created task id", output)
	}
	if !strings.Contains(output, "File: .tasks/01-phase/01-ms/01-epic/T001-new-task.todo") {
		t.Fatalf("output = %q, expected created task file path", output)
	}
	if !strings.Contains(output, "Next:") {
		t.Fatalf("output = %q, expected next commands", output)
	}
	if !strings.Contains(output, "backlog show P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected show next command", output)
	}
	if !strings.Contains(output, "backlog claim P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected claim next command", output)
	}

	index := readYAMLMap(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml"))
	tasks, ok := index["tasks"].([]interface{})
	if !ok || len(tasks) != 1 {
		t.Fatalf("epic index should contain 1 task, got %#v", index["tasks"])
	}
	task := tasks[0].(map[string]interface{})
	if task["id"] != "T001" {
		t.Fatalf("task id in index = %v, expected T001", task["id"])
	}
	if task["title"] != "New Task" {
		t.Fatalf("task title = %v, expected New Task", task["title"])
	}

	taskFile := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", task["file"].(string))
	content := readFile(t, taskFile)
	if !strings.Contains(content, "id: P1.M1.E1.T001") {
		t.Fatalf("task file missing full id, got %q", content)
	}
	if !strings.Contains(content, "status: pending") {
		t.Fatalf("task file missing pending status, got %q", content)
	}
}

func TestRunAddCommandRejectsInvalidDependencyID(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	_, err := runInDir(t, root, "add", "P1.M1.E1", "--title", "Bad depends", "--depends-on", "not a task id")
	if err == nil {
		t.Fatalf("run add expected dependency validation error")
	}
	if !strings.Contains(err.Error(), "invalid dependency id") {
		t.Fatalf("error = %q, expected invalid dependency id", err)
	}
}

func TestRunAddRejectsLockedEpic(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	epicIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml"))
	epicIndex["locked"] = true
	writeYAMLMap(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml"), epicIndex)

	_, err := runInDir(t, root, "add", "P1.M1.E1", "--title", "Blocked")
	if err == nil {
		t.Fatalf("run add expected error for locked epic")
	}
	if !strings.Contains(err.Error(), "cannot accept new tasks") {
		t.Fatalf("error = %q, expected lock message", err)
	}
}

func TestRunAddEpicCommand(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(
		t,
		root,
		"add-epic",
		"P1.M1",
		"--title",
		"Second Epic",
		"--description",
		"Epic description",
	)
	if err != nil {
		t.Fatalf("run add-epic = %v, expected nil", err)
	}
	if !strings.Contains(output, "Created epic: P1.M1.E2") {
		t.Fatalf("output = %q, expected created epic id", output)
	}
	if !strings.Contains(output, "File: .tasks/01-phase/01-ms/02-second-epic/index.yaml") {
		t.Fatalf("output = %q, expected created epic index path", output)
	}
	if !strings.Contains(output, "Next:") {
		t.Fatalf("output = %q, expected next commands", output)
	}
	if !strings.Contains(output, "backlog show P1.M1.E2") {
		t.Fatalf("output = %q, expected show next command", output)
	}
	if !strings.Contains(output, "backlog add P1.M1.E2 --title") {
		t.Fatalf("output = %q, expected add next command", output)
	}

	milestoneIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "index.yaml"))
	epics, ok := milestoneIndex["epics"].([]interface{})
	if !ok || len(epics) != 2 {
		t.Fatalf("milestone index should contain 2 epics, got %#v", milestoneIndex["epics"])
	}
	added := epics[1].(map[string]interface{})
	if added["id"] != "E2" {
		t.Fatalf("added epic id = %v, expected E2", added["id"])
	}
	if added["description"] != "Epic description" {
		t.Fatalf("added epic description = %v, expected provided description", added["description"])
	}

	epicDir := filepath.Join(root, ".tasks", "01-phase", "01-ms", added["path"].(string), "index.yaml")
	epicData := readYAMLMap(t, epicDir)
	if epicData["id"] != "P1.M1.E2" {
		t.Fatalf("epic index id = %v, expected P1.M1.E2", epicData["id"])
	}
}

func TestRunAddEpicCommandRejectsInvalidDependencyID(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	_, err := runInDir(
		t,
		root,
		"add-epic",
		"P1.M1",
		"--title",
		"Bad depends",
		"--depends-on",
		"not a task id",
	)
	if err == nil {
		t.Fatalf("run add-epic expected dependency validation error")
	}
	if !strings.Contains(err.Error(), "invalid dependency id") {
		t.Fatalf("error = %q, expected invalid dependency id", err)
	}
}

func TestRunAddMilestoneCommand(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(
		t,
		root,
		"add-milestone",
		"P1",
		"--title",
		"Second Milestone",
		"--description",
		"Milestone description",
	)
	if err != nil {
		t.Fatalf("run add-milestone = %v, expected nil", err)
	}
	if !strings.Contains(output, "Created milestone: P1.M2") {
		t.Fatalf("output = %q, expected created milestone id", output)
	}
	if !strings.Contains(output, "File: .tasks/01-phase/02-second-milestone/index.yaml") {
		t.Fatalf("output = %q, expected created milestone index path", output)
	}
	if !strings.Contains(output, "Next:") {
		t.Fatalf("output = %q, expected next commands", output)
	}
	if !strings.Contains(output, "backlog show P1.M2") {
		t.Fatalf("output = %q, expected show next command", output)
	}
	if !strings.Contains(output, "backlog add-epic P1.M2 --title") {
		t.Fatalf("output = %q, expected add-epic next command", output)
	}

	phaseIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "01-phase", "index.yaml"))
	milestones, ok := phaseIndex["milestones"].([]interface{})
	if !ok || len(milestones) != 2 {
		t.Fatalf("phase index should contain 2 milestones, got %#v", phaseIndex["milestones"])
	}
	added := milestones[1].(map[string]interface{})
	if added["id"] != "M2" {
		t.Fatalf("added milestone id = %v, expected M2", added["id"])
	}
	if added["description"] != "Milestone description" {
		t.Fatalf("added milestone description = %v, expected provided description", added["description"])
	}
}

func TestRunAddMilestoneCommandRejectsInvalidDependencyID(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	_, err := runInDir(
		t,
		root,
		"add-milestone",
		"P1",
		"--title",
		"Bad depends",
		"--depends-on",
		"not a task id",
	)
	if err == nil {
		t.Fatalf("run add-milestone expected dependency validation error")
	}
	if !strings.Contains(err.Error(), "invalid dependency id") {
		t.Fatalf("error = %q, expected invalid dependency id", err)
	}
}

func TestRunAddInvalidEpicID(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	_, err := runInDir(t, root, "add", "P1", "--title", "Nope")
	if err == nil {
		t.Fatalf("run add expected error for malformed id")
	}
	if !strings.Contains(err.Error(), "invalid epic id") {
		t.Fatalf("error = %q, expected malformed epic id", err)
	}
}

func TestRunAddEpicCommandRejectsMalformedMilestoneID(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	_, err := runInDir(t, root, "add-epic", "P1", "--title", "Nope")
	if err == nil {
		t.Fatalf("run add-epic expected error for malformed milestone id")
	}
	if !strings.Contains(err.Error(), "invalid milestone id") {
		t.Fatalf("error = %q, expected malformed milestone id", err)
	}
}

func TestRunAddMilestoneCommandRejectsMalformedPhaseID(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	_, err := runInDir(t, root, "add-milestone", "P1.M1", "--title", "Nope")
	if err == nil {
		t.Fatalf("run add-milestone expected error for malformed phase id")
	}
	if !strings.Contains(err.Error(), "invalid phase id") {
		t.Fatalf("error = %q, expected malformed phase id", err)
	}
}

func TestRunAddCommandRejectsLockedMilestoneAndEpic(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	epicIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml"))
	epicIndex["locked"] = true
	writeYAMLMap(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml"), epicIndex)

	milestoneIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "index.yaml"))
	milestoneIndex["locked"] = true
	writeYAMLMap(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "index.yaml"), milestoneIndex)

	_, err := runInDir(t, root, "add", "P1.M1.E1", "--title", "Blocked")
	if err == nil {
		t.Fatalf("run add expected error for locked hierarchy")
	}
	if !strings.Contains(err.Error(), "cannot accept new tasks") {
		t.Fatalf("error = %q, expected locked-task message", err)
	}
}

func TestRunAddEpicCommandAcceptsNameAlias(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(
		t,
		root,
		"add-epic",
		"P1.M1",
		"--name",
		"Alias Epic",
	)
	if err != nil {
		t.Fatalf("run add-epic = %v, expected nil", err)
	}
	if !strings.Contains(output, "Created epic: P1.M1.E2") {
		t.Fatalf("output = %q, expected created epic id", output)
	}
}

func TestRunAddMilestoneCommandAcceptsNameAlias(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(
		t,
		root,
		"add-milestone",
		"P1",
		"--name",
		"Alias Milestone",
	)
	if err != nil {
		t.Fatalf("run add-milestone = %v, expected nil", err)
	}
	if !strings.Contains(output, "Created milestone: P1.M2") {
		t.Fatalf("output = %q, expected created milestone id", output)
	}
}

func TestRunAgentsProfileOutput(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output, err := runInDir(t, root, "agents", "--profile", "short")
	if err != nil {
		t.Fatalf("run agents short = %v, expected nil", err)
	}
	if !strings.Contains(output, "AGENTS.md (Short)") {
		t.Fatalf("output = %q, expected short profile header", output)
	}
	if strings.Contains(output, "AGENTS.md (Medium)") || strings.Contains(output, "AGENTS.md (Long)") {
		t.Fatalf("output = %q, expected only short profile", output)
	}

	output, err = runInDir(t, root, "agents", "--profile", "all")
	if err != nil {
		t.Fatalf("run agents all = %v, expected nil", err)
	}
	if !strings.Contains(output, "AGENTS.md (Short)") || !strings.Contains(output, "AGENTS.md (Medium)") || !strings.Contains(output, "AGENTS.md (Long)") {
		t.Fatalf("output = %q, expected all profile headers", output)
	}
}

func TestRunLockEpicBlocksAddAndUnlockRestoresAdd(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(t, root, "lock", "P1.M1.E1")
	if err != nil {
		t.Fatalf("run lock epic = %v, expected nil", err)
	}
	if !strings.Contains(output, "Locked: P1.M1.E1") {
		t.Fatalf("output = %q, expected lock confirmation", output)
	}

	_, err = runInDir(t, root, "add", "P1.M1.E1", "--title", "Blocked")
	if err == nil {
		t.Fatalf("run add expected lock error")
	}
	if !strings.Contains(err.Error(), "cannot accept new tasks") {
		t.Fatalf("error = %q, expected locked-epic add message", err)
	}

	output, err = runInDir(t, root, "unlock", "P1.M1.E1")
	if err != nil {
		t.Fatalf("run unlock epic = %v, expected nil", err)
	}
	if !strings.Contains(output, "Unlocked: P1.M1.E1") {
		t.Fatalf("output = %q, expected unlock confirmation", output)
	}

	output, err = runInDir(t, root, "add", "P1.M1.E1", "--title", "Restored")
	if err != nil {
		t.Fatalf("run add after unlock = %v, expected nil", err)
	}
	if !strings.Contains(output, "Created task: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected created task id", output)
	}
}

func TestRunLockMilestoneAndPhaseBlockAdds(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(t, root, "lock", "P1.M1")
	if err != nil {
		t.Fatalf("run lock milestone = %v, expected nil", err)
	}
	if !strings.Contains(output, "Locked: P1.M1") {
		t.Fatalf("output = %q, expected milestone lock confirmation", output)
	}

	_, err = runInDir(t, root, "add-epic", "P1.M1", "--title", "Blocked epic")
	if err == nil {
		t.Fatalf("run add-epic expected locked milestone error")
	}
	if !strings.Contains(err.Error(), "cannot accept new epics") {
		t.Fatalf("error = %q, expected milestone lock add-epic message", err)
	}

	output, err = runInDir(t, root, "lock", "P1")
	if err != nil {
		t.Fatalf("run lock phase = %v, expected nil", err)
	}
	if !strings.Contains(output, "Locked: P1") {
		t.Fatalf("output = %q, expected phase lock confirmation", output)
	}

	_, err = runInDir(t, root, "add-milestone", "P1", "--title", "Blocked milestone")
	if err == nil {
		t.Fatalf("run add-milestone expected locked phase error")
	}
	if !strings.Contains(err.Error(), "cannot accept new milestones") {
		t.Fatalf("error = %q, expected phase lock add-milestone message", err)
	}
}

func TestRunIdeaCreatesPlanningIntake(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(t, root, "idea", "capture", "planning", "intake")
	if err != nil {
		t.Fatalf("run idea = %v, expected nil", err)
	}
	if !strings.Contains(output, "Created idea: I001") {
		t.Fatalf("output = %q, expected created idea", output)
	}
	if !strings.Contains(output, "IMPORTANT: This intake tracks planning work") {
		t.Fatalf("output = %q, expected planning guidance", output)
	}
	if !strings.Contains(output, "Next:") {
		t.Fatalf("output = %q, expected next commands", output)
	}
	if !strings.Contains(output, "backlog show I001") {
		t.Fatalf("output = %q, expected show next command", output)
	}

	ideasIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "ideas", "index.yaml"))
	entries, ok := ideasIndex["ideas"].([]interface{})
	if !ok || len(entries) != 1 {
		t.Fatalf("ideas index = %#v, expected one idea entry", ideasIndex["ideas"])
	}
	entry := entries[0].(map[string]interface{})
	if asString(entry["id"]) != "I001" {
		t.Fatalf("idea id = %v, expected I001", entry["id"])
	}
	ideaFile := filepath.Join(root, ".tasks", "ideas", asString(entry["file"]))
	content := readFile(t, ideaFile)
	if !strings.Contains(content, "id: I001") {
		t.Fatalf("idea file = %q, expected I001 frontmatter", content)
	}
	if !strings.Contains(content, "Created Work Items") {
		t.Fatalf("idea file = %q, expected work item follow-up guidance", content)
	}
}

func TestRunIdeaSupportsBodyAndMetadataFlags(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(
		t,
		root,
		"idea",
		"--title",
		"capture onboarding friction",
		"--estimate",
		"16",
		"--priority",
		"high",
		"--depends-on",
		"I001",
		"--tags",
		"research,ux",
		"--body",
		"Custom idea body content",
		"--simple",
	)
	if err != nil {
		t.Fatalf("run idea with metadata = %v, expected nil", err)
	}
	if !strings.Contains(output, "Created idea: I001") {
		t.Fatalf("output = %q, expected created idea id", output)
	}

	ideasIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "ideas", "index.yaml"))
	entries, ok := ideasIndex["ideas"].([]interface{})
	if !ok || len(entries) != 1 {
		t.Fatalf("ideas index = %#v, expected one idea entry", ideasIndex["ideas"])
	}
	entry := entries[0].(map[string]interface{})
	ideaFile := filepath.Join(root, ".tasks", "ideas", asString(entry["file"]))
	content := readFile(t, ideaFile)
	if !strings.Contains(content, "id: I001") {
		t.Fatalf("idea file = %q, expected id frontmatter", content)
	}
	if !strings.Contains(content, "estimate_hours: 16") {
		t.Fatalf("idea file = %q, expected overridden estimate", content)
	}
	if !strings.Contains(content, "priority: high") {
		t.Fatalf("idea file = %q, expected overridden priority", content)
	}
	if !strings.Contains(content, "depends_on:\n    - I001") {
		t.Fatalf("idea file = %q, expected depends_on override", content)
	}
	if !strings.Contains(content, "tags:\n    - research\n    - ux") {
		t.Fatalf("idea file = %q, expected tags override", content)
	}
	if !strings.Contains(content, "Custom idea body content") {
		t.Fatalf("idea file = %q, expected custom body", content)
	}
	if strings.Contains(content, "Created Work Items") {
		t.Fatalf("idea file = %q, should not include template when --body provided", content)
	}
}

func TestRunBugSupportsPositionalTitleAndSimpleBody(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(t, root, "bug", "fix", "flaky", "integration", "test")
	if err != nil {
		t.Fatalf("run bug positional = %v, expected nil", err)
	}
	if !strings.Contains(output, "Created bug: B001") {
		t.Fatalf("output = %q, expected created bug", output)
	}
	if !strings.Contains(output, "File: .tasks/bugs/B001-fix-flaky-integration-test.todo") {
		t.Fatalf("output = %q, expected bug file path", output)
	}
	if !strings.Contains(output, "Next:") {
		t.Fatalf("output = %q, expected next commands", output)
	}
	if !strings.Contains(output, "backlog show B001") {
		t.Fatalf("output = %q, expected show next command", output)
	}
	if !strings.Contains(output, "backlog claim B001") {
		t.Fatalf("output = %q, expected claim next command", output)
	}
	if strings.Contains(output, "IMPORTANT: You MUST fill in the .todo file that was created.") {
		t.Fatalf("output = %q, expected simple bug without template warning", output)
	}

	bugsIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "bugs", "index.yaml"))
	entries, ok := bugsIndex["bugs"].([]interface{})
	if !ok || len(entries) != 1 {
		t.Fatalf("bugs index = %#v, expected one bug entry", bugsIndex["bugs"])
	}
	entry := entries[0].(map[string]interface{})
	bugFile := filepath.Join(root, ".tasks", "bugs", asString(entry["file"]))
	content := readFile(t, bugFile)
	if !strings.Contains(content, "id: B001") {
		t.Fatalf("bug file = %q, expected B001 frontmatter", content)
	}
	if !strings.Contains(content, "priority: high") {
		t.Fatalf("bug file = %q, expected default high priority", content)
	}
}

func TestRunBugRejectsSingleWordPositionalTitle(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	if _, err := runInDir(t, root, "bug", "list"); err == nil {
		t.Fatal("run bug expected one-word positional validation error")
	} else if !strings.Contains(err.Error(), "bug requires at least two words") {
		t.Fatalf("error = %q, expected bug one-word validation", err)
	}
}

func TestRunBugRejectsSingleWordTitleFlag(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	if _, err := runInDir(t, root, "bug", "--title", "fix"); err == nil {
		t.Fatal("run bug expected one-word flag validation error")
	} else if !strings.Contains(err.Error(), "bug requires at least two words") {
		t.Fatalf("error = %q, expected bug one-word validation", err)
	}
}

func TestRunBugTemplateWarnsWhenNotSimple(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(t, root, "bug", "--title", "critical bug")
	if err != nil {
		t.Fatalf("run bug --title = %v, expected nil", err)
	}
	if !strings.Contains(output, "Created bug: B001") {
		t.Fatalf("output = %q, expected created bug", output)
	}
	if !strings.Contains(output, "IMPORTANT: You MUST fill in the .todo file that was created.") {
		t.Fatalf("output = %q, expected template warning", output)
	}
	if !strings.Contains(output, "Next:") {
		t.Fatalf("output = %q, expected next commands", output)
	}
}

func TestRunBugRejectsInvalidDependencyID(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	_, err := runInDir(
		t,
		root,
		"bug",
		"--title",
		"bad depends",
		"--depends-on",
		"not a task id",
	)
	if err == nil {
		t.Fatal("run bug expected invalid dependency id")
	}
	if !strings.Contains(err.Error(), "invalid dependency id") {
		t.Fatalf("error = %q, expected invalid dependency id", err)
	}
}

func TestRunIdeaRejectsSingleWordTitle(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	if _, err := runInDir(t, root, "idea", "plan"); err == nil {
		t.Fatal("run idea expected one-word validation error")
	} else if !strings.Contains(err.Error(), "idea requires at least two words") {
		t.Fatalf("error = %q, expected idea one-word validation", err)
	}
}

func TestRunIdeaRejectsInvalidDependencyID(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	_, err := runInDir(
		t,
		root,
		"idea",
		"--title",
		"bad depends",
		"--depends-on",
		"not a task id",
	)
	if err == nil {
		t.Fatal("run idea expected invalid dependency id")
	}
	if !strings.Contains(err.Error(), "invalid dependency id") {
		t.Fatalf("error = %q, expected invalid dependency id", err)
	}
}

func TestRunAddAutoCommitCreatesBlAddCommit(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	initializeTestGitRepo(t, root)

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(
		t,
		root,
		"add",
		"P1.M1.E1",
		"--title",
		"Auto commit task",
	)
	if err != nil {
		t.Fatalf("run add = %v", err)
	}
	if !strings.Contains(output, "Created task: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected created task", output)
	}
	if gitCommitCount(t, root) != commitCount+1 {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount+1)
	}
	if message := strings.TrimSpace(runGit(t, root, "log", "-1", "--pretty=%B")); message != "bl add P1.M1.E1.T001: Auto commit task" {
		t.Fatalf("latest commit = %q, expected %q", message, "bl add P1.M1.E1.T001: Auto commit task")
	}
}

func TestRunAddAutoCommitSkipsWhenStagedChangesExist(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	initializeTestGitRepo(t, root)

	stagedPath := filepath.Join(root, "staged-change.txt")
	if err := os.WriteFile(stagedPath, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	runGit(t, root, "add", "staged-change.txt")

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(
		t,
		root,
		"add",
		"P1.M1.E1",
		"--title",
		"Should stay staged",
	)
	if err != nil {
		t.Fatalf("run add = %v", err)
	}
	if !strings.Contains(output, "Created task: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected created task", output)
	}
	if gitCommitCount(t, root) != commitCount {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount)
	}
	if status := runGit(t, root, "status", "--short"); !strings.Contains(status, "A  staged-change.txt") {
		t.Fatalf("status = %q, expected staged change to be preserved", status)
	}
}

func TestRunAddAutoCommitAmendsUnpushedBlAdd(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	initializeTestGitRepo(t, root)

	output, err := runInDir(
		t,
		root,
		"add",
		"P1.M1.E1",
		"--title",
		"First auto commit task",
	)
	if err != nil {
		t.Fatalf("first add = %v", err)
	}
	if !strings.Contains(output, "Created task: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected created first task", output)
	}

	commitCount := gitCommitCount(t, root)
	output, err = runInDir(
		t,
		root,
		"add",
		"P1.M1.E1",
		"--title",
		"Second auto commit task",
	)
	if err != nil {
		t.Fatalf("second add = %v", err)
	}
	if !strings.Contains(output, "Created task: P1.M1.E1.T002") {
		t.Fatalf("output = %q, expected created second task", output)
	}
	if gitCommitCount(t, root) != commitCount {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount)
	}
	if message := strings.TrimSpace(runGit(t, root, "log", "-1", "--pretty=%B")); message != "bl add P1.M1.E1.T001: First auto commit task" {
		t.Fatalf("latest commit = %q, expected %q", message, "bl add P1.M1.E1.T001: First auto commit task")
	}
}

func TestRunBugAutoCommitCreatesBlBugCommit(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	initializeTestGitRepo(t, root)

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(t, root, "bug", "root bug title")
	if err != nil {
		t.Fatalf("run bug = %v", err)
	}
	if !strings.Contains(output, "Created bug: B001") {
		t.Fatalf("output = %q, expected created bug", output)
	}
	if gitCommitCount(t, root) != commitCount+1 {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount+1)
	}
	if message := strings.TrimSpace(runGit(t, root, "log", "-1", "--pretty=%B")); message != "bl bug B001: root bug title" {
		t.Fatalf("latest commit = %q, expected %q", message, "bl bug B001: root bug title")
	}
}

func TestRunIdeaAutoCommitCreatesBlIdeaCommit(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	initializeTestGitRepo(t, root)

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(t, root, "idea", "capture planning idea")
	if err != nil {
		t.Fatalf("run idea = %v", err)
	}
	if !strings.Contains(output, "Created idea: I001") {
		t.Fatalf("output = %q, expected created idea", output)
	}
	if gitCommitCount(t, root) != commitCount+1 {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount+1)
	}
	if message := strings.TrimSpace(runGit(t, root, "log", "-1", "--pretty=%B")); message != "bl idea I001: capture planning idea" {
		t.Fatalf("latest commit = %q, expected %q", message, "bl idea I001: capture planning idea")
	}
}

func TestRunClaimAutoCommitCreatesBlClaimCommit(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	initializeTestGitRepo(t, root)

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(t, root, "claim", "P1.M1.E1.T001", "--agent", "agent-x")
	if err != nil {
		t.Fatalf("run claim = %v", err)
	}
	if !strings.Contains(output, "✓ Claimed") {
		t.Fatalf("output = %q, expected claim confirmation", output)
	}
	if gitCommitCount(t, root) != commitCount+1 {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount+1)
	}
	if message := strings.TrimSpace(runGit(t, root, "log", "-1", "--pretty=%B")); message != "bl claim P1.M1.E1.T001: a" {
		t.Fatalf("latest commit = %q, expected %q", message, "bl claim P1.M1.E1.T001: a")
	}
}

func TestRunClaimAutoCommitSkipsWhenStagedChangesExist(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	initializeTestGitRepo(t, root)

	stagedPath := filepath.Join(root, "staged-change.txt")
	if err := os.WriteFile(stagedPath, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	runGit(t, root, "add", "staged-change.txt")

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(t, root, "claim", "P1.M1.E1.T002", "--agent", "agent-x")
	if err != nil {
		t.Fatalf("run claim = %v", err)
	}
	if !strings.Contains(output, "✓ Claimed") {
		t.Fatalf("output = %q, expected claim confirmation", output)
	}
	if gitCommitCount(t, root) != commitCount {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount)
	}
	if status := runGit(t, root, "status", "--short"); !strings.Contains(status, "A  staged-change.txt") {
		t.Fatalf("status = %q, expected staged change to be preserved", status)
	}
}

func TestRunGrabAutoCommitCreatesBlGrabCommit(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	initializeTestGitRepo(t, root)

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(
		t,
		root,
		"grab",
		"--single",
		"--agent",
		"agent-x",
		"--no-content",
	)
	if err != nil {
		t.Fatalf("run grab = %v", err)
	}
	if !strings.Contains(output, "P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected grabbed task", output)
	}
	if gitCommitCount(t, root) != commitCount+1 {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount+1)
	}
	if message := strings.TrimSpace(runGit(t, root, "log", "-1", "--pretty=%B")); message != "bl grab P1.M1.E1.T001: a" {
		t.Fatalf("latest commit = %q, expected %q", message, "bl grab P1.M1.E1.T001: a")
	}
}

func TestRunGrabAutoCommitSkipsWhenStagedChangesExist(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	initializeTestGitRepo(t, root)

	stagedPath := filepath.Join(root, "staged-change.txt")
	if err := os.WriteFile(stagedPath, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	runGit(t, root, "add", "staged-change.txt")

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(
		t,
		root,
		"grab",
		"--single",
		"--agent",
		"agent-x",
		"--no-content",
	)
	if err != nil {
		t.Fatalf("run grab = %v", err)
	}
	if !strings.Contains(output, "P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected attempted grab task", output)
	}
	if gitCommitCount(t, root) != commitCount {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount)
	}
	if status := runGit(t, root, "status", "--short"); !strings.Contains(status, "A  staged-change.txt") {
		t.Fatalf("status = %q, expected staged change to be preserved", status)
	}
}

func TestRunFixedRejectsSingleWordTitle(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	if _, err := runInDir(t, root, "fixed", "patch"); err == nil {
		t.Fatal("run fixed expected positional one-word validation error")
	} else if !strings.Contains(err.Error(), "fixed requires at least two words") {
		t.Fatalf("error = %q, expected fixed one-word validation", err)
	}
}

func TestRunFixedRejectsSingleWordTitleFlag(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	if _, err := runInDir(t, root, "fixed", "--title", "fix"); err == nil {
		t.Fatal("run fixed expected flag-based one-word validation error")
	} else if !strings.Contains(err.Error(), "fixed requires at least two words") {
		t.Fatalf("error = %q, expected fixed one-word validation", err)
	}
}

func TestRunAddPhaseCommand(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(
		t,
		root,
		"add-phase",
		"--title",
		"Second Phase",
	)
	if err != nil {
		t.Fatalf("run add-phase = %v, expected nil", err)
	}
	if !strings.Contains(output, "Created phase: P2") {
		t.Fatalf("output = %q, expected created phase id", output)
	}
	if !strings.Contains(output, "File: .tasks/02-second-phase/index.yaml") {
		t.Fatalf("output = %q, expected created phase index path", output)
	}
	if !strings.Contains(output, "Next:") {
		t.Fatalf("output = %q, expected next commands", output)
	}
	if !strings.Contains(output, "backlog show P2") {
		t.Fatalf("output = %q, expected show next command", output)
	}
	if !strings.Contains(output, "backlog add-milestone P2 --title") {
		t.Fatalf("output = %q, expected add-milestone next command", output)
	}
}

func TestRunAddPhaseCommandRejectsInvalidDependencyID(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	_, err := runInDir(
		t,
		root,
		"add-phase",
		"--title",
		"Bad depends",
		"--depends-on",
		"not a task id",
	)
	if err == nil {
		t.Fatalf("run add-phase expected dependency validation error")
	}
	if !strings.Contains(err.Error(), "invalid dependency id") {
		t.Fatalf("error = %q, expected invalid dependency id", err)
	}
}

func TestRunSetCommandRejectsInvalidDependencyID(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_, err := runInDir(t, root, "set", "P1.M1.E1.T001", "--depends-on", "not a task id")
	if err == nil {
		t.Fatal("run set expected invalid dependency id")
	}
	if !strings.Contains(err.Error(), "invalid dependency id") {
		t.Fatalf("error = %q, expected invalid dependency id", err)
	}
}

func TestRunSetAutoCommitCreatesBlSetCommit(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	initializeTestGitRepo(t, root)

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(t, root, "set", "P1.M1.E1.T001", "--priority", "high")
	if err != nil {
		t.Fatalf("run set = %v", err)
	}
	if !strings.Contains(output, "Updated: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected set confirmation", output)
	}
	if gitCommitCount(t, root) != commitCount+1 {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount+1)
	}
	if message := strings.TrimSpace(runGit(t, root, "log", "-1", "--pretty=%B")); message != "bl set P1.M1.E1.T001: a" {
		t.Fatalf("latest commit = %q, expected %q", message, "bl set P1.M1.E1.T001: a")
	}
}

func TestRunSetAutoCommitSkipsWhenStagedChangesExist(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	initializeTestGitRepo(t, root)

	stagedPath := filepath.Join(root, "staged-change.txt")
	if err := os.WriteFile(stagedPath, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	runGit(t, root, "add", "staged-change.txt")

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(t, root, "set", "P1.M1.E1.T001", "--priority", "critical")
	if err != nil {
		t.Fatalf("run set = %v", err)
	}
	if !strings.Contains(output, "Updated: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected set confirmation", output)
	}
	if gitCommitCount(t, root) != commitCount {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount)
	}
	if status := runGit(t, root, "status", "--short"); !strings.Contains(status, "A  staged-change.txt") {
		t.Fatalf("status = %q, expected staged change to be preserved", status)
	}
}

func TestRunFixedCapturesMetadataAndIndex(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	output, err := runInDir(
		t,
		root,
		"fixed",
		"--title",
		"ship patch",
		"--description",
		"hotfix for cli parity",
		"--at",
		"2026-02-01T12:34:56Z",
		"--tags",
		"ops,urgent",
		"--body",
		"patched and verified",
	)
	if err != nil {
		t.Fatalf("run fixed = %v, expected nil", err)
	}
	if !strings.Contains(output, "Created fixed: F001") {
		t.Fatalf("output = %q, expected created fixed", output)
	}
	if !strings.Contains(output, "File: .tasks/fixes/2026-02/F001-ship-patch.todo") {
		t.Fatalf("output = %q, expected fixed file path", output)
	}
	if !strings.Contains(output, "Tags: ops, urgent") {
		t.Fatalf("output = %q, expected tags summary", output)
	}

	fixesIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "fixes", "index.yaml"))
	entries, ok := fixesIndex["fixes"].([]interface{})
	if !ok || len(entries) != 1 {
		t.Fatalf("fixes index = %#v, expected one fix entry", fixesIndex["fixes"])
	}
	entry := entries[0].(map[string]interface{})
	if asString(entry["id"]) != "F001" {
		t.Fatalf("fix id = %v, expected F001", entry["id"])
	}
	fixFile := filepath.Join(root, ".tasks", "fixes", asString(entry["file"]))
	content := readFile(t, fixFile)
	if !strings.Contains(content, "type: fixed") {
		t.Fatalf("fixed file = %q, expected fixed type", content)
	}
	if !strings.Contains(content, "status: done") {
		t.Fatalf("fixed file = %q, expected done status", content)
	}
	if !strings.Contains(content, "created_at: \"2026-02-01T12:34:56Z\"") {
		t.Fatalf("fixed file = %q, expected created_at timestamp", content)
	}
}

func TestRunFixedRejectsInvalidTimestamp(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	_, err := runInDir(t, root, "fixed", "--title", "bad timestamp", "--at", "not-a-time")
	if err == nil {
		t.Fatalf("run fixed expected timestamp parse error")
	}
	if !strings.Contains(err.Error(), "fixed --at must be an ISO 8601 timestamp") {
		t.Fatalf("error = %q, expected timestamp validation", err)
	}
}

func TestRunMigrateRenamesDataDirAndUpdatesDocs(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Use `tasks list` daily.\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md fixture: %v", err)
	}

	output, err := runInDir(t, root, "migrate", "--no-symlink")
	if err != nil {
		t.Fatalf("run migrate = %v, expected nil", err)
	}
	if !strings.Contains(output, "Migrated .tasks/ -> .backlog/") {
		t.Fatalf("output = %q, expected migration summary", output)
	}
	if _, err := os.Stat(filepath.Join(root, ".backlog", "index.yaml")); err != nil {
		t.Fatalf("expected .backlog/index.yaml after migration: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".tasks")); !os.IsNotExist(err) {
		t.Fatalf("expected .tasks absent without symlink, stat err = %v", err)
	}
	agents := readFile(t, filepath.Join(root, "AGENTS.md"))
	if !strings.Contains(agents, "CLI migrated") {
		t.Fatalf("AGENTS.md = %q, expected migration comment", agents)
	}
	if !strings.Contains(agents, "`backlog list`") {
		t.Fatalf("AGENTS.md = %q, expected command rewrite", agents)
	}
}

func TestRunMigrateRejectsBothDirectoriesWithoutForce(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".tasks"), 0o755); err != nil {
		t.Fatalf("create .tasks fixture: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".backlog"), 0o755); err != nil {
		t.Fatalf("create .backlog fixture: %v", err)
	}

	_, err := runInDir(t, root, "migrate")
	if err == nil {
		t.Fatalf("run migrate expected both-dir conflict error")
	}
	if !strings.Contains(err.Error(), "Both .tasks/ and .backlog/ exist. Use --force to proceed.") {
		t.Fatalf("error = %q, expected conflict guidance", err)
	}
}

func TestRunAddMilestoneCommandRejectsLockedPhase(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	rootIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "index.yaml"))
	phases, _ := rootIndex["phases"].([]interface{})
	phases[0].(map[string]interface{})["locked"] = true
	writeYAMLMap(t, filepath.Join(root, ".tasks", "index.yaml"), rootIndex)

	_, err := runInDir(t, root, "add-milestone", "P1", "--title", "Blocked")
	if err == nil {
		t.Fatalf("run add-milestone expected error for locked phase")
	}
	if !strings.Contains(err.Error(), "cannot accept new milestones") {
		t.Fatalf("error = %q, expected locked milestone message", err)
	}
}

func TestRunClaimRejectsClaimedTask(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "in_progress", "agent-x", "2026-01-01T00:00:00Z")
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T002", "b", "pending", "", "")

	_, err := runInDir(t, root, "claim", "P1.M1.E1.T001")
	if err == nil {
		t.Fatalf("run claim expected error for already claimed task")
	}
	if !strings.Contains(err.Error(), "Task P1.M1.E1.T001 is already claimed by agent-x") {
		t.Fatalf("error = %q, expected claim guard message", err)
	}
}

func TestRunClaimRejectsMissingTaskFile(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	setTaskTodoFilePath(t, root, "P1.M1.E1.T001", filepath.Join("01-phase", "01-ms", "01-epic", "missing.todo"))

	_, err := runInDir(t, root, "claim", "P1.M1.E1.T001")
	if err == nil {
		t.Fatalf("run claim expected error for missing todo file")
	}
	if !strings.Contains(err.Error(), "Cannot claim P1.M1.E1.T001 because the task file is missing.") {
		t.Fatalf("error = %q, expected missing file message", err)
	}
}

func TestRunClaimDoneTaskShowsCompletionTimestamp(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFileWithTimes(
		t,
		root,
		"P1.M1.E1.T001",
		"a",
		"done",
		"agent-x",
		"2026-01-01T00:00:00Z",
		"2026-01-01T00:00:00Z",
		"2026-01-01T00:30:00Z",
	)

	_, err := runInDir(t, root, "claim", "P1.M1.E1.T001")
	if err == nil {
		t.Fatalf("run claim expected error for done task")
	}
	if !strings.Contains(err.Error(), "task is done (completed_at: 2026-01-01T00:30:00Z)") {
		t.Fatalf("error = %q, expected done/completed_at message", err)
	}
}

func TestRunClaimScopeFallsBackToShow(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	output, err := runInDir(t, root, "claim", "P1.M1")
	if err != nil {
		t.Fatalf("run claim = %v, expected nil", err)
	}
	assertContainsAll(t,
		output,
		"Warning: claim only works with task IDs.",
		"backlog show P1.M1",
		"P1.M1",
	)
	if strings.Contains(output, "Next:") {
		t.Fatalf("output = %q, expected no Next guidance for claim fallback", output)
	}
}

func TestRunEditRequiresSingleTaskID(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	_, err := runInDir(t, root, "edit")
	if err == nil {
		t.Fatalf("run edit without ids expected error")
	}
	if !strings.Contains(err.Error(), "edit requires exactly one TASK_ID") {
		t.Fatalf("error = %q, expected single-task requirement", err)
	}

	_, err = runInDir(t, root, "edit", "P1.M1.E1.T001", "P1.M1.E1.T002")
	if err == nil {
		t.Fatalf("run edit with multiple ids expected error")
	}
	if !strings.Contains(err.Error(), "edit requires exactly one TASK_ID") {
		t.Fatalf("error = %q, expected single-task requirement", err)
	}
}

func TestRunEditScopeFallsBackToShow(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	output, err := runInDir(t, root, "edit", "P1")
	if err != nil {
		t.Fatalf("run edit = %v, expected nil", err)
	}
	assertContainsAll(
		t,
		output,
		"Warning: edit only works with task IDs.",
		"Showing `backlog show P1` for context.",
		"P1",
	)
}

func TestRunEditRejectsMissingTaskFile(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	setTaskTodoFilePath(t, root, "P1.M1.E1.T001", filepath.Join("01-phase", "01-ms", "01-epic", "missing.todo"))

	output, err := runInDir(t, root, "edit", "P1.M1.E1.T001")
	if err == nil {
		t.Fatalf("run edit expected error for missing todo file")
	}
	if !strings.Contains(output, "Cannot edit P1.M1.E1.T001 because the task file is missing.") {
		t.Fatalf("output = %q, expected missing file message", output)
	}
}

func TestRunEditWithoutEditorErrors(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	_, err := runInDirWithEnv(t, root, map[string]string{"VISUAL": "", "EDITOR": ""}, "edit", "P1.M1.E1.T001")
	if err == nil {
		t.Fatalf("run edit without editor expected error")
	}
	if !strings.Contains(err.Error(), "No editor configured. Set EDITOR or VISUAL.") {
		t.Fatalf("error = %q, expected editor configuration error", err)
	}
}

func TestRunEditLaunchesConfiguredEditor(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	editorPath := filepath.Join(root, "bin", "backlog-edit-test-editor.sh")
	if err := os.MkdirAll(filepath.Dir(editorPath), 0o755); err != nil {
		t.Fatalf("make editor dir = %v", err)
	}
	editorScript := "#!/bin/sh\necho \"$1\" > .editor-called\n"
	if err := os.WriteFile(editorPath, []byte(editorScript), 0o755); err != nil {
		t.Fatalf("write editor script = %v", err)
	}

	_, err := runInDirWithEnv(t, root, map[string]string{"VISUAL": editorPath}, "edit", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run edit with editor = %v", err)
	}

	invoked, readErr := os.ReadFile(filepath.Join(root, ".editor-called"))
	if readErr != nil {
		t.Fatalf("read editor output: %v", readErr)
	}
	if !strings.Contains(string(invoked), filepath.Join(".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo")) {
		t.Fatalf("editor was not passed task path: %q", string(invoked))
	}
}

func TestRunEditAutoCommitCreatesBlEditCommit(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	editorPath := filepath.Join(root, "bin", "backlog-edit-test-editor.sh")
	if err := os.MkdirAll(filepath.Dir(editorPath), 0o755); err != nil {
		t.Fatalf("make editor dir = %v", err)
	}
	editorScript := "#!/bin/sh\necho \"edited\" >> \"$1\"\n"
	if err := os.WriteFile(editorPath, []byte(editorScript), 0o755); err != nil {
		t.Fatalf("write editor script = %v", err)
	}

	initializeTestGitRepo(t, root)
	commitCount := gitCommitCount(t, root)
	if _, err := runInDirWithEnv(t, root, map[string]string{"VISUAL": editorPath}, "edit", "P1.M1.E1.T001"); err != nil {
		t.Fatalf("run edit = %v", err)
	}

	if gitCommitCount(t, root) != commitCount+1 {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount+1)
	}
	if message := strings.TrimSpace(runGit(t, root, "log", "-1", "--pretty=%B")); message != "bl edit P1.M1.E1.T001: a" {
		t.Fatalf("latest commit = %q, expected %q", message, "bl edit P1.M1.E1.T001: a")
	}
}

func TestRunEditAutoCommitSkipsWhenStagedChangesExist(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	editorPath := filepath.Join(root, "bin", "backlog-edit-test-editor.sh")
	if err := os.MkdirAll(filepath.Dir(editorPath), 0o755); err != nil {
		t.Fatalf("make editor dir = %v", err)
	}
	editorScript := "#!/bin/sh\necho \"edited\" >> \"$1\"\n"
	if err := os.WriteFile(editorPath, []byte(editorScript), 0o755); err != nil {
		t.Fatalf("write editor script = %v", err)
	}

	initializeTestGitRepo(t, root)

	stagedPath := filepath.Join(root, "staged-change.txt")
	if err := os.WriteFile(stagedPath, []byte("staged\n"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	runGit(t, root, "add", "staged-change.txt")

	commitCount := gitCommitCount(t, root)
	if _, err := runInDirWithEnv(t, root, map[string]string{"VISUAL": editorPath}, "edit", "P1.M1.E1.T001"); err != nil {
		t.Fatalf("run edit = %v", err)
	}

	if gitCommitCount(t, root) != commitCount {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount)
	}
	if status := runGit(t, root, "status", "--short"); !strings.Contains(status, "A  staged-change.txt") {
		t.Fatalf("status = %q, expected staged change to be preserved", status)
	}
}

func TestRunClaimAuxiliaryTaskIds(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	if _, err := runInDir(t, root, "bug", "Critical", "bug", "requires", "triage"); err != nil {
		t.Fatalf("run bug = %v, expected nil", err)
	}
	if _, err := runInDir(t, root, "idea", "Improve", "planning", "pipeline"); err != nil {
		t.Fatalf("run idea = %v, expected nil", err)
	}

	output, err := runInDir(t, root, "claim", "B001", "--agent", "agent-z")
	if err != nil {
		t.Fatalf("run claim B001 = %v, expected nil", err)
	}
	assertContainsAll(t, output, "✓ Claimed B001")

	bugsIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "bugs", "index.yaml"))
	bugs, ok := bugsIndex["bugs"].([]interface{})
	if !ok || len(bugs) != 1 {
		t.Fatalf("bugs index = %#v, expected one bug", bugsIndex["bugs"])
	}
	bugEntry := bugs[0].(map[string]interface{})
	bugFile := filepath.Join(root, ".tasks", "bugs", asString(bugEntry["file"]))
	bugTaskFile := readFile(t, bugFile)
	assertContainsAll(t, bugTaskFile, "id: B001", "status: in_progress")

	ideasIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "ideas", "index.yaml"))
	ideas, ok := ideasIndex["ideas"].([]interface{})
	if !ok || len(ideas) != 1 {
		t.Fatalf("ideas index = %#v, expected one idea", ideasIndex["ideas"])
	}
	ideaEntry := ideas[0].(map[string]interface{})
	ideaID := asString(ideaEntry["id"])

	output, err = runInDir(t, root, "claim", ideaID, "--agent", "agent-z")
	if err != nil {
		t.Fatalf("run claim %s = %v, expected nil", ideaID, err)
	}
	assertContainsAll(t, output, fmt.Sprintf("✓ Claimed %s", ideaID))

	ideaFile := filepath.Join(root, ".tasks", "ideas", asString(ideaEntry["file"]))
	ideaTaskFile := readFile(t, ideaFile)
	assertContainsAll(t, ideaTaskFile, fmt.Sprintf("id: %s", ideaID), "status: in_progress")
}

func TestRunGrabExplicitDoneTaskShowsCompletionTimestamp(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFileWithTimes(
		t,
		root,
		"P1.M1.E1.T001",
		"a",
		"done",
		"agent-x",
		"2026-01-01T00:00:00Z",
		"2026-01-01T00:00:00Z",
		"2026-01-01T00:30:00Z",
	)

	_, err := runInDir(t, root, "grab", "P1.M1.E1.T001")
	if err == nil {
		t.Fatalf("run grab explicit expected error for done task")
	}
	if !strings.Contains(err.Error(), "task is done (completed_at: 2026-01-01T00:30:00Z)") {
		t.Fatalf("error = %q, expected done/completed_at message", err)
	}
}

func TestRunClaimSingleTaskRendersDetailCard(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "claim", "P1.M1.E1.T001", "--agent", "agent-z", "--no-content")
	if err != nil {
		t.Fatalf("run claim = %v, expected nil", err)
	}
	if !strings.Contains(output, "✓ Claimed P1.M1.E1.T001 - a") {
		t.Fatalf("claim output = %q, expected rich claimed summary", output)
	}
	if !strings.Contains(output, "Task body preview suppressed via --no-content") {
		t.Fatalf("claim output = %q, expected --no-content hint", output)
	}
	if !strings.Contains(output, "/01-phase/01-ms/01-epic/T001-a.todo") {
		t.Fatalf("claim output = %q, expected task file hint command", output)
	}
	if !strings.Contains(output, "-----== EOF ==-----") {
		t.Fatalf("claim output = %q, expected EOF marker", output)
	}
	if !strings.Contains(output, "When you complete this task, mark it done by either:") {
		t.Fatalf("claim output = %q, expected next-step guidance header", output)
	}
	if !strings.Contains(output, "bl cycle P1.M1.E1.T001") {
		t.Fatalf("claim output = %q, expected cycle guidance", output)
	}
	if !strings.Contains(output, "bl done P1.M1.E1.T001") {
		t.Fatalf("claim output = %q, expected done guidance", output)
	}
	if !strings.Contains(output, "To release this task: `bl unclaim P1.M1.E1.T001`") {
		t.Fatalf("claim output = %q, expected unclaim guidance", output)
	}
}

func TestRunWorkAcceptsAgentAndSetsContext(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "work", "--agent", "agent-a", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run work --agent = %v, expected nil", err)
	}
	assertContainsAll(t, output, "Working task set:", "P1.M1.E1.T001")

	context, err := taskcontext.LoadContext(filepath.Join(root, ".tasks"))
	if err != nil {
		t.Fatalf("load context = %v, expected nil", err)
	}
	if context.Agent != "agent-a" {
		t.Fatalf("context.agent = %q, expected agent-a", context.Agent)
	}
	if context.CurrentTask != "P1.M1.E1.T001" {
		t.Fatalf("context.current_task = %q, expected P1.M1.E1.T001", context.CurrentTask)
	}
}

func TestRunWorkRejectsMultipleTaskIDs(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "work", "P1.M1.E1.T001", "P1.M1.E1.T002")
	if err == nil {
		t.Fatalf("run work with multiple task ids expected error")
	}
	assertContainsAll(t, output, "Command Help: backlog work", "work accepts at most one TASK_ID")
}

func TestRunVersionPrintsVersionOutput(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "version")
	if err != nil {
		t.Fatalf("run version = %v, expected nil", err)
	}
	assertContainsAll(t, output, "backlog version", "0.1.0")
}

func TestRunVersionRejectsPositionalArguments(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "version", "P1.M1.E1.T001")
	if err == nil {
		t.Fatalf("run version with positional arg expected error")
	}
	assertContainsAll(
		t,
		output,
		"Command Help: backlog version",
		"version accepts no TASK_ID arguments",
	)
}

func TestRunAddPhaseHelpRendersCommandSpecificGuidance(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output, err := runInDir(t, root, "add-phase", "--help")
	if err != nil {
		t.Fatalf("run add-phase --help = %v, expected nil", err)
	}
	assertContainsAll(t, output, "Command Help: backlog add-phase", "Usage:", "backlog add-phase --title <TITLE>", "--weeks, -w")
}

func TestRunAddPhaseWithoutArgsPrintsHelpAndError(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "add-phase")
	if err == nil {
		t.Fatalf("run add-phase expected error when missing args")
	}
	assertContainsAll(t, output, "Command Help: backlog add-phase", "backlog add-phase --title <TITLE>")
	if !strings.Contains(err.Error(), "add-phase requires --title") {
		t.Fatalf("err = %q, expected missing title error", err)
	}
}

func TestRunAddPhaseUnknownFlagPrintsHelpAndError(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "add-phase", "--bogus")
	if err == nil {
		t.Fatalf("run add-phase --bogus expected error")
	}
	assertContainsAll(t, output, "Command Help: backlog add-phase", "Usage:")
	if !strings.Contains(err.Error(), "unexpected flag: --bogus") {
		t.Fatalf("err = %q, expected unknown-flag error", err)
	}
}

func TestRunAddHelpRendersCommandSpecificGuidance(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output, err := runInDir(t, root, "add", "--help")
	if err != nil {
		t.Fatalf("run add --help = %v, expected nil", err)
	}
	assertContainsAll(t, output, "Command Help: backlog add", "backlog add <EPIC_ID> --title <TITLE>", "--depends-on, -d")
}

func TestRunClaimHelpRendersCommandSpecificGuidance(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "claim", "--help")
	if err != nil {
		t.Fatalf("run claim --help = %v, expected nil", err)
	}
	assertContainsAll(t, output, "Command Help: backlog claim", "backlog claim <TASK_ID>", "--agent")
}

func TestRunEditHelpRendersCommandSpecificGuidance(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "edit", "--help")
	if err != nil {
		t.Fatalf("run edit --help = %v", err)
	}
	assertContainsAll(
		t,
		output,
		"Command Help: backlog edit",
		"Usage: backlog edit <TASK_ID>",
	)
}

func TestRunCIHelpRendersCommandSpecificGuidance(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output, err := runInDir(t, root, "ci", "--help")
	if err != nil {
		t.Fatalf("run ci --help = %v, expected nil", err)
	}
	assertContainsAll(t, output, "Command Help: backlog ci", "Usage: backlog ci validate-ids <TASK_ID ...>")
}

func TestRunHelpSyncRendersCommandSpecificGuidance(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output, err := runInDir(t, root, "help", "sync")
	if err != nil {
		t.Fatalf("run help sync = %v, expected nil", err)
	}
	assertContainsAll(
		t,
		output,
		"Command Help: backlog sync",
		"Usage: backlog sync",
		"Recalculate derived metadata in index files.",
	)
}

func TestRunCIValidateIDsAcceptsShorthandIDs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output, err := runInDir(t, root, "ci", "validate-ids", "B020", "E1.T02", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run ci validate-ids = %v, expected nil", err)
	}
	if output != "" {
		t.Fatalf("output = %q, expected empty", output)
	}
}

func TestRunCIValidateIDsRejectsMalformedID(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output, err := runInDir(t, root, "ci", "validate-ids", "B020", "not-an-id", "E1.T02")
	if err == nil {
		t.Fatalf("run ci validate-ids expected error")
	}
	if !strings.Contains(err.Error(), "malformed task id: not-an-id") {
		t.Fatalf("err = %q, expected malformed task id error", err)
	}
	assertContainsAll(t, output, "malformed task id: not-an-id")
}

func TestRunShowMissingTaskSuggestsNearbyIDs(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "show", "P1.M1.E1.T999")
	if err == nil {
		t.Fatalf("run show missing task expected error")
	}
	assertContainsAll(t, output, "Did you mean:", "P1.M1.E1.T001")
}

func TestRunMoveTaskToEpicRenumbersID(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	tasksRoot := filepath.Join(root, ".tasks")

	milestoneIndexPath := filepath.Join(tasksRoot, "01-phase", "01-ms", "index.yaml")
	milestoneIndex := readYAMLMap(t, milestoneIndexPath)
	epics, _ := milestoneIndex["epics"].([]interface{})
	epics = append(epics,
		map[string]interface{}{
			"id":   "E2",
			"name": "Target",
			"path": "02-target-epic",
		},
	)
	milestoneIndex["epics"] = epics
	writeYAMLMap(t, milestoneIndexPath, milestoneIndex)
	targetEpicIndex := filepath.Join(tasksRoot, "01-phase", "01-ms", "02-target-epic", "index.yaml")
	writeYAMLMap(t, targetEpicIndex, map[string]interface{}{
		"id":     "P1.M1.E2",
		"name":   "Target",
		"status": "pending",
		"tasks":  []map[string]interface{}{},
	})

	output, err := runInDir(
		t,
		root,
		"move",
		"P1.M1.E1.T001",
		"--to",
		"P1.M1.E2",
	)
	if err != nil {
		t.Fatalf("run move = %v, expected nil", err)
	}
	if !strings.Contains(output, "New ID: P1.M1.E2.T001") {
		t.Fatalf("output = %q, expected remapped id", output)
	}

	moved := readFile(t, filepath.Join(tasksRoot, "01-phase", "01-ms", "02-target-epic", "T001-a.todo"))
	if !strings.Contains(moved, "id: P1.M1.E2.T001") {
		t.Fatalf("task file missing remapped id, got %q", moved)
	}
}

func TestRunMoveEpicToMilestoneRemapsDescendants(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	tasksRoot := filepath.Join(root, ".tasks")
	phaseIndexPath := filepath.Join(tasksRoot, "index.yaml")
	phaseIndex := readYAMLMap(t, phaseIndexPath)
	phases := phaseIndex["phases"].([]interface{})
	phases = append(phases,
		map[string]interface{}{
			"id":   "P2",
			"name": "Second phase",
			"path": "02-phase",
		},
	)
	phaseIndex["phases"] = phases
	writeYAMLMap(t, phaseIndexPath, phaseIndex)

	milestoneIndexPath := filepath.Join(tasksRoot, "01-phase", "index.yaml")
	writeYAMLMap(t, milestoneIndexPath, map[string]interface{}{
		"id":   "P1",
		"name": "Phase",
		"milestones": []map[string]interface{}{
			{
				"id":   "M1",
				"name": "Milestone",
				"path": "01-ms",
			},
			{
				"id":   "M2",
				"name": "Second",
				"path": "02-ms",
			},
		},
	})
	writeYAMLMap(t, filepath.Join(tasksRoot, "02-phase", "index.yaml"), map[string]interface{}{
		"id":   "P2",
		"name": "Second phase",
		"milestones": []map[string]interface{}{
			{
				"id":   "M2",
				"name": "Second",
				"path": "02-ms",
			},
		},
	})
	writeYAMLMap(t, filepath.Join(tasksRoot, "02-phase", "02-ms", "index.yaml"), map[string]interface{}{
		"id":             "P2.M2",
		"name":           "Second",
		"path":           "02-ms",
		"epics":          []map[string]interface{}{},
		"status":         "pending",
		"estimate_hours": 0,
		"complexity":     "medium",
		"depends_on":     []string{},
	})

	output, err := runInDir(
		t,
		root,
		"move",
		"P1.M1.E1",
		"--to",
		"P2.M2",
	)
	if err != nil {
		t.Fatalf("run move = %v, expected nil", err)
	}
	if !strings.Contains(output, "New ID: P2.M2.E1") {
		t.Fatalf("output = %q, expected remapped id", output)
	}

	moved := readFile(t, filepath.Join(tasksRoot, "02-phase", "02-ms", "01-epic", "T001-a.todo"))
	if !strings.Contains(moved, "id: P2.M2.E1.T001") {
		t.Fatalf("task file missing remapped id, got %q", moved)
	}
}

func TestRunUnclaimPendingClaimedTask(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "in_progress", "agent-x", "2026-01-01T00:00:00Z")

	output, err := runInDir(t, root, "unclaim", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run unclaim = %v, expected nil", err)
	}
	if !strings.Contains(output, "Unclaimed: P1.M1.E1.T001 - a") {
		t.Fatalf("output = %q, expected unclaim confirmation", output)
	}
	taskText := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"))
	if !strings.Contains(taskText, "status: pending") {
		t.Fatalf("task text = %q, expected status pending", taskText)
	}
	if strings.Contains(taskText, "claimed_by:") {
		t.Fatalf("task text = %q, expected claim cleared", taskText)
	}
}

func TestRunUnclaimRejectsUnclaimedPendingTask(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "unclaim", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run unclaim = %v, expected nil", err)
	}
	if !strings.Contains(output, "Task is not in progress: pending") {
		t.Fatalf("output = %q, expected not-in-progress message", output)
	}
}

func TestRunUnclaimAutoCommitCreatesBlUnclaimCommit(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "in_progress", "agent-x", "2026-01-01T00:00:00Z")
	initializeTestGitRepo(t, root)

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(t, root, "unclaim", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run unclaim = %v", err)
	}
	if !strings.Contains(output, "Unclaimed: P1.M1.E1.T001 - a") {
		t.Fatalf("output = %q, expected unclaim output", output)
	}
	if gitCommitCount(t, root) != commitCount+1 {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount+1)
	}
	if message := strings.TrimSpace(runGit(t, root, "log", "-1", "--pretty=%B")); message != "bl unclaim P1.M1.E1.T001: a" {
		t.Fatalf("latest commit = %q, expected %q", message, "bl unclaim P1.M1.E1.T001: a")
	}
}

func TestRunUnclaimAutoCommitSkipsWhenStagedChangesExist(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "in_progress", "agent-x", "2026-01-01T00:00:00Z")
	initializeTestGitRepo(t, root)

	stagedPath := filepath.Join(root, "staged-change.txt")
	if err := os.WriteFile(stagedPath, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	runGit(t, root, "add", "staged-change.txt")

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(t, root, "unclaim", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run unclaim = %v", err)
	}
	if !strings.Contains(output, "Unclaimed: P1.M1.E1.T001 - a") {
		t.Fatalf("output = %q, expected unclaim output", output)
	}
	if gitCommitCount(t, root) != commitCount {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount)
	}
	if status := runGit(t, root, "status", "--short"); !strings.Contains(status, "A  staged-change.txt") {
		t.Fatalf("status = %q, expected staged change to be preserved", status)
	}
}

func TestRunBlockedDefaultsToNoAutoGrabAndTip(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	dataDir := filepath.Join(root, ".tasks")
	if err := taskcontext.SetCurrentTask(dataDir, "P1.M1.E1.T001", "agent-a"); err != nil {
		t.Fatalf("set current task = %v, expected nil", err)
	}

	output, err := runInDir(t, root, "blocked", "--reason", "waiting on dependency")
	if err != nil {
		t.Fatalf("run blocked = %v, expected nil", err)
	}
	if !strings.Contains(output, "Blocked: P1.M1.E1.T001 (waiting on dependency)") {
		t.Fatalf("output = %q, expected blocked confirmation", output)
	}
	if !strings.Contains(output, "Tip: Run `backlog grab` to claim the next available task.") {
		t.Fatalf("output = %q, expected grab tip", output)
	}
	if strings.Contains(output, "Grabbed:") {
		t.Fatalf("output = %q, unexpected auto-grab behavior", output)
	}
	taskText := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"))
	if !strings.Contains(taskText, "status: blocked") {
		t.Fatalf("task text = %q, expected status blocked", taskText)
	}
	if !strings.Contains(taskText, "reason: waiting on dependency") {
		t.Fatalf("task text = %q, expected reason field", taskText)
	}
	context, err := taskcontext.LoadContext(dataDir)
	if err != nil {
		t.Fatalf("load context = %v, expected nil", err)
	}
	if context.CurrentTask != "" || context.PrimaryTask != "" {
		t.Fatalf("context current/primary tasks should be cleared after blocked, got current=%q primary=%q", context.CurrentTask, context.PrimaryTask)
	}
}

func TestRunBlockedWithGrabClaimsNextTask(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	dataDir := filepath.Join(root, ".tasks")

	output, err := runInDir(t, root, "blocked", "P1.M1.E1.T001", "--reason", "waiting for dependency", "--grab")
	if err != nil {
		t.Fatalf("run blocked --grab = %v, expected nil", err)
	}
	if !strings.Contains(output, "Blocked: P1.M1.E1.T001 (waiting for dependency)") {
		t.Fatalf("output = %q, expected blocked confirmation", output)
	}
	if !strings.Contains(output, "Grabbed: P1.M1.E1.T002 - b") &&
		!strings.Contains(output, "No available tasks found.") {
		t.Fatalf("output = %q, expected auto-grab", output)
	}
	taskText := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"))
	if !strings.Contains(taskText, "status: blocked") {
		t.Fatalf("task text = %q, expected status blocked", taskText)
	}
	if strings.Contains(output, "Grabbed: P1.M1.E1.T002 - b") {
		nextText := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo"))
		if !strings.Contains(nextText, "status: in_progress") {
			t.Fatalf("task text = %q, expected in_progress after auto-grab", nextText)
		}
	}

	context, err := taskcontext.LoadContext(dataDir)
	if err != nil {
		t.Fatalf("load context = %v, expected nil", err)
	}
	if strings.Contains(output, "Grabbed: P1.M1.E1.T002 - b") {
		if context.CurrentTask != "P1.M1.E1.T002" {
			t.Fatalf("context current task = %q, expected P1.M1.E1.T002", context.CurrentTask)
		}
		if context.Agent != "cli-user" {
			t.Fatalf("context agent = %q, expected cli-user", context.Agent)
		}
		return
	}
	if context.CurrentTask != "" || context.PrimaryTask != "" {
		t.Fatalf("context current/primary tasks should stay cleared when no auto-grab candidate exists")
	}
}

func TestRunSyncWritesDerivedStats(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "sync")
	if err != nil {
		t.Fatalf("run sync = %v, expected nil", err)
	}
	if !strings.Contains(output, "Synced") {
		t.Fatalf("output = %q, expected synced message", output)
	}
	rootIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "index.yaml"))
	statsRaw, ok := rootIndex["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("root stats missing or invalid: %#v", rootIndex["stats"])
	}
	if asInt(statsRaw["total_tasks"]) != 2 {
		t.Fatalf("expected 2 tasks, got %v", statsRaw["total_tasks"])
	}
	if _, ok := rootIndex["critical_path"]; !ok {
		t.Fatalf("root stats missing critical_path")
	}

	phaseIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "01-phase", "index.yaml"))
	phaseStats, ok := phaseIndex["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("phase stats missing or invalid: %#v", phaseIndex["stats"])
	}
	if asInt(phaseStats["total_tasks"]) != 2 {
		t.Fatalf("phase stats expected 2 tasks, got %v", phaseStats["total_tasks"])
	}
	if asInt(phaseStats["done"]) != 0 {
		t.Fatalf("phase stats expected 0 done, got %v", phaseStats["done"])
	}
	if asInt(phaseStats["pending"]) != 2 {
		t.Fatalf("phase stats expected 2 pending, got %v", phaseStats["pending"])
	}

	milestoneIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "index.yaml"))
	milestoneStats, ok := milestoneIndex["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("milestone stats missing or invalid: %#v", milestoneIndex["stats"])
	}
	if asInt(milestoneStats["total_tasks"]) != 2 {
		t.Fatalf("milestone stats expected 2 tasks, got %v", milestoneStats["total_tasks"])
	}
	if asInt(milestoneStats["pending"]) != 2 {
		t.Fatalf("milestone stats expected 2 pending, got %v", milestoneStats["pending"])
	}

	epicIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml"))
	epicStatsRaw, ok := epicIndex["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("epic stats missing or invalid: %#v", epicIndex["stats"])
	}
	if asInt(epicStatsRaw["total"]) != 2 {
		t.Fatalf("epic stats expected 2 tasks, got %v", epicStatsRaw["total"])
	}
	if asInt(epicStatsRaw["pending"]) != 2 {
		t.Fatalf("epic stats expected 2 pending, got %v", epicStatsRaw["pending"])
	}
}

func TestRunSyncFailsOnTaskDependencyCycle(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	addDependencyForWorkflowTask(t, root, "P1.M1.E1.T001", []string{"P1.M1.E1.T002"})
	addDependencyForWorkflowTask(t, root, "P1.M1.E1.T002", []string{"P1.M1.E1.T001"})

	output, err := runInDir(t, root, "sync")
	if err == nil {
		t.Fatalf("run sync expected cycle error, got output %q", output)
	}
	if !strings.Contains(output, "task dependency cycle detected") {
		t.Fatalf("output = %q, expected explicit cycle message", output)
	}
	if !strings.Contains(output, "P1.M1.E1.T001 -> P1.M1.E1.T002 -> P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected cycle chain", output)
	}
}

func TestRunSyncWarnsOnHierarchyCycle(t *testing.T) {
	t.Parallel()

	root := setupHierarchyCycleFixture(t)

	output, err := runInDir(t, root, "sync")
	if err != nil {
		t.Fatalf("run sync = %v, expected nil", err)
	}
	if !strings.Contains(output, "Warning:") {
		t.Fatalf("output = %q, expected warning", output)
	}
	if !strings.Contains(output, "non-task dependency cycle detected") {
		t.Fatalf("output = %q, expected non-task cycle warning", output)
	}
	if !strings.Contains(output, "P1.M1.E1.T001") || !strings.Contains(output, "P2.M1.E1.T001") {
		t.Fatalf("output = %q, expected hierarchy cycle chain", output)
	}
}

func TestFilterTasksByIDs(t *testing.T) {
	t.Parallel()

	tasks := []models.Task{
		{ID: "P1.M1.E1.T001", Status: models.StatusDone},
		{ID: "P1.M1.E1.T002", Status: models.StatusPending},
		{ID: "P1.M1.E1.T003", Status: models.StatusDone},
	}

	filtered := filterTasksByIDs(
		[]string{"P1.M1.E1.T001", "P1.M1.E1.T002", "P1.M1.E1.T999"},
		tasks,
		func(task models.Task) bool {
			return task.Status == models.StatusPending
		},
	)
	if len(filtered) != 1 {
		t.Fatalf("len(filtered) = %d, expected 1", len(filtered))
	}
	if filtered[0].ID != "P1.M1.E1.T002" {
		t.Fatalf("filtered = %v, expected only pending task", filtered)
	}
}

func TestRunShowNoTaskFallsBackToCurrent(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	dataDir := filepath.Join(root, ".tasks")
	if err := taskcontext.SetCurrentTask(dataDir, "P1.M1.E1.T001", ""); err != nil {
		t.Fatalf("set current task = %v, expected nil", err)
	}

	output, err := runInDir(t, root, "show")
	if err != nil {
		t.Fatalf("run show = %v, expected nil", err)
	}
	if !strings.Contains(output, "Task: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected task detail", output)
	}
}

func TestRunShowInProgressTaskRendersCompletionGuidance(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	if _, err := runInDir(t, root, "claim", "P1.M1.E1.T001", "--agent", "agent-snap"); err != nil {
		t.Fatalf("run claim = %v, expected nil", err)
	}
	output, err := runInDir(t, root, "show", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run show = %v, expected nil", err)
	}
	if !strings.Contains(output, "-----=====-----") {
		t.Fatalf("show output = %q, expected bar separator", output)
	}
	if !strings.Contains(output, "When you complete this task, mark it done by either:") {
		t.Fatalf("show output = %q, expected completion guidance", output)
	}
	if !strings.Contains(output, "bl cycle P1.M1.E1.T001") {
		t.Fatalf("show output = %q, expected cycle guidance", output)
	}
	if !strings.Contains(output, "bl done P1.M1.E1.T001") {
		t.Fatalf("show output = %q, expected done guidance", output)
	}
	if !strings.Contains(output, "To release this task: `bl unclaim P1.M1.E1.T001`") {
		t.Fatalf("show output = %q, expected unclaim guidance", output)
	}
}

func TestRunShowTaskPreviewUsesLineLimitByDefault(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	taskPath := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo")
	content := []string{
		"---",
		"id: P1.M1.E1.T001",
		"title: a",
		"status: pending",
		"estimate_hours: 1",
		"complexity: medium",
		"priority: medium",
		"depends_on: []",
		"tags: []",
		"---",
		"line-01",
		"line-02",
		"line-03",
		"line-04",
		"line-05",
		"line-06",
		"line-07",
		"line-08",
		"line-09",
		"line-10",
		"line-11",
		"line-12",
		"line-13",
	}
	if err := os.WriteFile(taskPath, []byte(strings.Join(content, "\n")), 0o644); err != nil {
		t.Fatalf("write task file: %v", err)
	}
	info, err := os.Stat(taskPath)
	if err != nil {
		t.Fatalf("stat task file: %v", err)
	}

	output, err := runInDir(t, root, "show", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run show = %v, expected nil", err)
	}
	if !strings.Contains(output, fmt.Sprintf("File stats: %d bytes, %d lines", info.Size(), len(content))) {
		t.Fatalf("show output = %q, expected file stats line", output)
	}
	if !strings.Contains(output, "line-12") {
		t.Fatalf("show output = %q, expected preview line-12", output)
	}
	if strings.Contains(output, "line-13") {
		t.Fatalf("show output = %q, expected truncated preview output", output)
	}
	if !strings.Contains(output, "... (1 more lines)") {
		t.Fatalf("show output = %q, expected truncated line count message", output)
	}
	if !strings.Contains(output, "To view the full file, run: cat ") {
		t.Fatalf("show output = %q, expected file preview hint", output)
	}
}

func TestRunShowTaskLongPrintsFullBody(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	taskPath := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo")
	content := []string{
		"---",
		"id: P1.M1.E1.T001",
		"title: a",
		"status: pending",
		"estimate_hours: 1",
		"complexity: medium",
		"priority: medium",
		"depends_on: []",
		"tags: []",
		"---",
		"line-01",
		"line-02",
		"line-03",
		"line-04",
		"line-05",
		"line-06",
		"line-07",
		"line-08",
		"line-09",
		"line-10",
		"line-11",
		"line-12",
		"line-13",
	}
	if err := os.WriteFile(taskPath, []byte(strings.Join(content, "\n")), 0o644); err != nil {
		t.Fatalf("write task file: %v", err)
	}
	info, err := os.Stat(taskPath)
	if err != nil {
		t.Fatalf("stat task file: %v", err)
	}

	output, err := runInDir(t, root, "show", "P1.M1.E1.T001", "--long")
	if err != nil {
		t.Fatalf("run show --long = %v, expected nil", err)
	}
	if !strings.Contains(output, fmt.Sprintf("File stats: %d bytes, %d lines", info.Size(), len(content))) {
		t.Fatalf("show output = %q, expected file stats line", output)
	}
	if !strings.Contains(output, "line-13") {
		t.Fatalf("show output = %q, expected full body line-13", output)
	}
	if strings.Contains(output, "... (1 more line)") {
		t.Fatalf("show output = %q, unexpected truncation with --long", output)
	}
	if strings.Contains(output, "To view the full file, run: cat ") {
		t.Fatalf("show output = %q, unexpected file preview hint", output)
	}
}

func TestRunShowTaskAllPrintsCompleteTaskFile(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	taskPath := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo")
	content := []string{
		"---",
		"id: P1.M1.E1.T001",
		"title: a",
		"status: pending",
		"estimate_hours: 1",
		"complexity: medium",
		"priority: medium",
		"depends_on: []",
		"tags: []",
		"---",
		"line-01",
		"line-02",
		"line-03",
		"line-04",
		"line-05",
		"line-06",
		"line-07",
		"line-08",
		"line-09",
		"line-10",
		"line-11",
		"line-12",
		"line-13",
	}
	if err := os.WriteFile(taskPath, []byte(strings.Join(content, "\n")), 0o644); err != nil {
		t.Fatalf("write task file: %v", err)
	}

	output, err := runInDir(t, root, "show", "P1.M1.E1.T001", "--all")
	if err != nil {
		t.Fatalf("run show --all = %v, expected nil", err)
	}
	if !strings.Contains(output, "Task File") {
		t.Fatalf("show output = %q, expected task file output", output)
	}
	if !strings.Contains(output, "id: P1.M1.E1.T001") {
		t.Fatalf("show output = %q, expected frontmatter id", output)
	}
	if !strings.Contains(output, "line-13") {
		t.Fatalf("show output = %q, expected full body line-13", output)
	}
	if strings.Contains(output, "... (1 more lines)") {
		t.Fatalf("show output = %q, unexpected preview truncation with --all", output)
	}
	if strings.Contains(output, "To view the full file, run: cat ") {
		t.Fatalf("show output = %q, unexpected file preview hint with --all", output)
	}
}

func TestRunCatSingleTaskPrintsCompleteTaskFile(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	taskPath := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo")

	output, err := runInDir(t, root, "cat", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run cat = %v, expected nil", err)
	}
	if output != readFile(t, taskPath) {
		t.Fatalf("cat output mismatch:\nexpected %q\ngot      %q", readFile(t, taskPath), output)
	}
}

func TestRunCatMultipleTasksPrintsHeadersBetweenFiles(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	firstPath := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo")
	secondPath := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo")

	output, err := runInDir(t, root, "cat", "P1.M1.E1.T001", "P1.M1.E1.T002")
	if err != nil {
		t.Fatalf("run cat = %v, expected nil", err)
	}

	expected := readFile(t, firstPath) +
		"\n-------------------- [ P1.M1.E1.T002 ] --------------------\n" +
		readFile(t, secondPath)
	if output != expected {
		t.Fatalf("cat output mismatch:\nexpected %q\ngot      %q", expected, output)
	}
}

func TestRunCatMultipleTasksAddsBlankLineAfterFileWithoutTrailingNewline(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	firstPath := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo")
	secondPath := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T002-b.todo")
	firstRaw := strings.TrimRight(readFile(t, firstPath), "\n")
	if err := os.WriteFile(firstPath, []byte(firstRaw), 0o644); err != nil {
		t.Fatalf("write first task without trailing newline: %v", err)
	}

	output, err := runInDir(t, root, "cat", "P1.M1.E1.T001", "P1.M1.E1.T002")
	if err != nil {
		t.Fatalf("run cat = %v, expected nil", err)
	}

	expected := firstRaw +
		"\n\n-------------------- [ P1.M1.E1.T002 ] --------------------\n" +
		readFile(t, secondPath)
	if output != expected {
		t.Fatalf("cat output mismatch:\nexpected %q\ngot      %q", expected, output)
	}
}

func TestRunCatMalformedNormalTaskPrintsRawFile(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	taskPath := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo")
	rawContent := "---\n:\n---\n# Raw\n"
	if err := os.WriteFile(taskPath, []byte(rawContent), 0o644); err != nil {
		t.Fatalf("write malformed normal task: %v", err)
	}

	output, err := runInDir(t, root, "cat", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run cat malformed normal task = %v, expected nil", err)
	}
	if output != rawContent {
		t.Fatalf("cat malformed normal output mismatch:\nexpected %q\ngot      %q", rawContent, output)
	}
}

func TestRunCatFixedTaskPrintsCompleteTaskFile(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	if _, err := runInDir(
		t,
		root,
		"fixed",
		"--title",
		"ship patch",
		"--at",
		"2026-02-01T12:34:56Z",
		"--body",
		"patched and verified",
	); err != nil {
		t.Fatalf("run fixed = %v, expected nil", err)
	}
	fixesIndex := readYAMLMap(t, filepath.Join(root, ".tasks", "fixes", "index.yaml"))
	entries := fixesIndex["fixes"].([]interface{})
	entry := entries[0].(map[string]interface{})
	fixPath := filepath.Join(root, ".tasks", "fixes", asString(entry["file"]))

	output, err := runInDir(t, root, "cat", "F001")
	if err != nil {
		t.Fatalf("run cat fixed = %v, expected nil", err)
	}
	if output != readFile(t, fixPath) {
		t.Fatalf("cat fixed output mismatch:\nexpected %q\ngot      %q", readFile(t, fixPath), output)
	}
}

func TestRunCatMalformedFixedTaskPrintsRawFile(t *testing.T) {
	t.Parallel()

	root := setupAddFixture(t)
	fixesDir := filepath.Join(root, ".tasks", "fixes")
	fixPath := filepath.Join(fixesDir, "2026-02", "F001-ship-patch.todo")
	rawContent := "---\n:\n---\n# Ship patch\nRaw body survives.\n"
	indexContent := "fixes:\n  - id: F001\n    file: 2026-02/F001-ship-patch.todo\n"

	if err := os.MkdirAll(filepath.Dir(fixPath), 0o755); err != nil {
		t.Fatalf("create malformed fixed task dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixesDir, "index.yaml"), []byte(indexContent), 0o644); err != nil {
		t.Fatalf("write fixes index: %v", err)
	}
	if err := os.WriteFile(fixPath, []byte(rawContent), 0o644); err != nil {
		t.Fatalf("write malformed fixed task: %v", err)
	}

	output, err := runInDir(t, root, "cat", "F001")
	if err != nil {
		t.Fatalf("run cat malformed fixed = %v, expected nil", err)
	}
	if output != rawContent {
		t.Fatalf("cat malformed fixed output mismatch:\nexpected %q\ngot      %q", rawContent, output)
	}
}

func TestRunCatMissingTaskShowsTreeHint(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	output, err := runInDir(t, root, "cat", "P1.M1.E1.T999")
	if err == nil {
		t.Fatalf("run cat expected missing task error")
	}
	combined := output + err.Error()
	if !strings.Contains(combined, "Task not found") || !strings.Contains(combined, "P1.M1.E1.T999") {
		t.Fatalf("cat output/error = %q / %q, expected missing task message", output, err)
	}
	if !strings.Contains(combined, "Use 'backlog tree P1.M1.E1' to verify available IDs.") {
		t.Fatalf("cat output/error = %q / %q, expected scoped tree hint", output, err)
	}
}

func TestRunCatHelpShowsUsage(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	output, err := runInDir(t, root, "cat", "--help")
	if err != nil {
		t.Fatalf("run cat --help = %v, expected nil", err)
	}
	if !strings.Contains(output, "Command Help: backlog cat") || !strings.Contains(output, "Usage: backlog cat <TASK_ID ...>") {
		t.Fatalf("cat help output = %q, expected command-specific usage", output)
	}
}

func TestRunShowTaskDisplaysExplicitDependencies(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "done", "", "")
	addDependencyForWorkflowTask(t, root, "P1.M1.E1.T002", []string{"P1.M1.E1.T001"})

	output, err := runInDir(t, root, "show", "P1.M1.E1.T002")
	if err != nil {
		t.Fatalf("run show = %v, expected nil", err)
	}
	if !strings.Contains(output, "Explicit dependencies:") {
		t.Fatalf("show output = %q, expected explicit dependency section", output)
	}
	if !strings.Contains(output, "✓ P1.M1.E1.T001") {
		t.Fatalf("show output = %q, expected explicit dependency marker line", output)
	}
	if !strings.Contains(output, "Legend: ✓ done | ✗ not done | ? not found") {
		t.Fatalf("show output = %q, expected dependency legend", output)
	}
	if strings.Contains(output, "Implicit dependency") {
		t.Fatalf("show output = %q, expected no implicit dependency section", output)
	}
}

func TestRunShowTaskDisplaysImplicitDependencyWhenNoExplicit(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	addDependencyForWorkflowTask(t, root, "P1.M1.E1.T002", []string{})

	output, err := runInDir(t, root, "show", "P1.M1.E1.T002")
	if err != nil {
		t.Fatalf("run show = %v, expected nil", err)
	}
	if !strings.Contains(output, "Implicit dependency (previous in epic):") {
		t.Fatalf("show output = %q, expected implicit dependency section", output)
	}
	if !strings.Contains(output, "✗ P1.M1.E1.T001") {
		t.Fatalf("show output = %q, expected implicit dependency marker line", output)
	}
	if !strings.Contains(output, "Legend: ✓ done | ✗ not done | ? not found") {
		t.Fatalf("show output = %q, expected dependency legend", output)
	}
	if strings.Contains(output, "Explicit dependencies:") {
		t.Fatalf("show output = %q, expected no explicit dependency section", output)
	}
}

func TestRunShowPhaseDisplaysTotalDuration(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "show", "P1")
	if err != nil {
		t.Fatalf("run show = %v, expected nil", err)
	}
	if !strings.Contains(output, "Total Duration:") {
		t.Fatalf("show output = %q, expected total duration label", output)
	}
	if !strings.Contains(output, "2.00h") {
		t.Fatalf("show output = %q, expected 2.00h total duration", output)
	}
}

func TestRunShowMilestoneDisplaysTotalDuration(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "show", "P1.M1")
	if err != nil {
		t.Fatalf("run show = %v, expected nil", err)
	}
	if !strings.Contains(output, "Total Duration:") {
		t.Fatalf("show output = %q, expected total duration label", output)
	}
	if !strings.Contains(output, "2.00h") {
		t.Fatalf("show output = %q, expected 2.00h total duration", output)
	}
}

func TestRunShowEpicDisplaysTotalDuration(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "show", "P1.M1.E1")
	if err != nil {
		t.Fatalf("run show = %v, expected nil", err)
	}
	if !strings.Contains(output, "Total Duration:") {
		t.Fatalf("show output = %q, expected total duration label", output)
	}
	if !strings.Contains(output, "2.00h") {
		t.Fatalf("show output = %q, expected 2.00h total duration", output)
	}
}

func TestRunShowNoTaskCurrentTaskUnavailable(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "show")
	if err != nil {
		t.Fatalf("run show = %v, expected nil", err)
	}
	if !strings.Contains(output, "No task specified and no current working task set.") {
		t.Fatalf("output = %q, expected no current task guidance", output)
	}
}

func TestRunShowInvalidTaskPath(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_, err := runInDir(t, root, "show", "invalid-id")
	if err == nil {
		t.Fatalf("run show expected parse error for invalid task path")
	}
	if !strings.Contains(err.Error(), "Invalid path format: invalid-id") {
		t.Fatalf("err = %q, expected invalid path format error", err)
	}
}

func TestRunShowRejectsUnknownFlag(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_, err := runInDir(t, root, "show", "--unknown")
	if err == nil {
		t.Fatalf("run show expected flag validation error")
	}
	if !strings.Contains(err.Error(), "unexpected flag: --unknown") {
		t.Fatalf("err = %q, expected unexpected flag error", err)
	}
}

func TestRunInitRequiresProject(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	_, err := runInDir(t, root, "init")
	if err == nil {
		t.Fatalf("run init expected project validation error")
	}
	if !strings.Contains(err.Error(), "init requires --project") {
		t.Fatalf("err = %q, expected missing project error", err)
	}
}

func TestRunInitWritesBacklogIndex(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output, err := runInDir(t, root, "init", "--project", "Go Client", "--description", "Parity work", "--timeline-weeks", "12")
	if err != nil {
		t.Fatalf("run init = %v, expected nil", err)
	}
	if !strings.Contains(output, `Initialized project "Go Client" in .backlog/`) {
		t.Fatalf("output = %q, expected init success message", output)
	}

	indexPath := filepath.Join(root, ".backlog", "index.yaml")
	index := readYAMLMap(t, indexPath)
	if index["project"] != "Go Client" {
		t.Fatalf("project = %v, expected Go Client", index["project"])
	}
	if asInt(index["timeline_weeks"]) != 12 {
		t.Fatalf("timeline_weeks = %v, expected 12", index["timeline_weeks"])
	}
	if phases, ok := index["phases"].([]interface{}); !ok || len(phases) != 0 {
		t.Fatalf("phases = %#v, expected empty phase list", index["phases"])
	}
}

func TestRunInitRejectsDuplicateProject(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if _, err := runInDir(t, root, "init", "--project", "Go Client"); err != nil {
		t.Fatalf("run init = %v, expected nil", err)
	}
	_, err := runInDir(t, root, "init", "--project", "Go Client")
	if err == nil {
		t.Fatalf("run init expected duplicate project error")
	}
	if !strings.Contains(err.Error(), "Already initialized (.backlog/index.yaml exists)") {
		t.Fatalf("err = %q, expected duplicate error", err)
	}
}

func TestRunInitRejectsInvalidTimeline(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	_, err := runInDir(t, root, "init", "--project", "Go Client", "--timeline-weeks", "not-a-number")
	if err == nil {
		t.Fatalf("run init expected timeline parse error")
	}
	if !strings.Contains(err.Error(), "invalid --timeline-weeks value") {
		t.Fatalf("err = %q, expected timeline parse error", err)
	}
}

func TestRunListIncludesAuxItemsByDefault(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "list")
	if err != nil {
		t.Fatalf("run list = %v, expected nil", err)
	}
	if !strings.Contains(output, "Primary Phase (P1)") {
		t.Fatalf("output = %q, expected phase name and ID", output)
	}
	if !strings.Contains(output, "Milestone One (P1.M1)") {
		t.Fatalf("output = %q, expected milestone name and ID", output)
	}
	if !strings.Contains(output, "Bugs (0/1 done)") {
		t.Fatalf("output = %q, expected bugs section", output)
	}
	if !strings.Contains(output, "B1: Root bug") {
		t.Fatalf("output = %q, expected bug title in list output", output)
	}
	if !strings.Contains(output, "Ideas (0/1 done)") {
		t.Fatalf("output = %q, expected ideas section", output)
	}
	if !strings.Contains(output, "I1: Root idea") {
		t.Fatalf("output = %q, expected idea title in list output", output)
	}
}

func TestRunListFailsOnTaskDependencyCycle(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	addDependencyForWorkflowTask(t, root, "P1.M1.E1.T001", []string{"P1.M1.E1.T002"})
	addDependencyForWorkflowTask(t, root, "P1.M1.E1.T002", []string{"P1.M1.E1.T001"})

	output, err := runInDir(t, root, "list")
	if err == nil {
		t.Fatalf("run list expected cycle error, got output %q", output)
	}
	if !strings.Contains(output, "task dependency cycle detected") {
		t.Fatalf("output = %q, expected explicit cycle message", output)
	}
	if !strings.Contains(output, "P1.M1.E1.T001 -> P1.M1.E1.T002 -> P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected cycle chain", output)
	}
}

func TestRunListWarnsOnHierarchyCycle(t *testing.T) {
	t.Parallel()

	root := setupHierarchyCycleFixture(t)

	output, err := runInDir(t, root, "list")
	if err != nil {
		t.Fatalf("run list = %v, expected nil", err)
	}
	if !strings.Contains(output, "Warning:") {
		t.Fatalf("output = %q, expected warning", output)
	}
	if !strings.Contains(output, "non-task dependency cycle detected") {
		t.Fatalf("output = %q, expected non-task cycle warning", output)
	}
	if !strings.Contains(output, "P1.M1.E1.T001") || !strings.Contains(output, "P2.M1.E1.T001") {
		t.Fatalf("output = %q, expected hierarchy cycle chain", output)
	}
}

func TestRunListBugsAndIdeasFlags(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)

	bugsOutput, err := runInDir(t, root, "list", "--bugs")
	if err != nil {
		t.Fatalf("run list --bugs = %v, expected nil", err)
	}
	if !strings.Contains(bugsOutput, "Bugs (0/1 done)") {
		t.Fatalf("bugs output = %q, expected bugs section", bugsOutput)
	}
	if !strings.Contains(bugsOutput, "B1: Root bug") {
		t.Fatalf("bugs output = %q, expected bug title", bugsOutput)
	}
	if strings.Contains(bugsOutput, "Ideas (") {
		t.Fatalf("bugs output = %q, expected ideas section hidden", bugsOutput)
	}

	ideasOutput, err := runInDir(t, root, "list", "--ideas")
	if err != nil {
		t.Fatalf("run list --ideas = %v, expected nil", err)
	}
	if !strings.Contains(ideasOutput, "Ideas (0/1 done)") {
		t.Fatalf("ideas output = %q, expected ideas section", ideasOutput)
	}
	if !strings.Contains(ideasOutput, "I1: Root idea") {
		t.Fatalf("ideas output = %q, expected idea title", ideasOutput)
	}
	if strings.Contains(ideasOutput, "Bugs (") {
		t.Fatalf("ideas output = %q, expected bugs section hidden", ideasOutput)
	}
}

func TestRunListListShortAliases(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)

	availableOutput, err := runInDir(t, root, "list", "-a")
	if err != nil {
		t.Fatalf("run list -a = %v, expected nil", err)
	}
	if !strings.Contains(availableOutput, "Available Tasks (") {
		t.Fatalf("available output = %q, expected available header", availableOutput)
	}

	bugsOutput, err := runInDir(t, root, "list", "-b")
	if err != nil {
		t.Fatalf("run list -b = %v, expected nil", err)
	}
	if !strings.Contains(bugsOutput, "Bugs (0/1 done)") {
		t.Fatalf("bugs output = %q, expected bugs section", bugsOutput)
	}
	if strings.Contains(bugsOutput, "Ideas (") {
		t.Fatalf("bugs output = %q, expected ideas section hidden", bugsOutput)
	}

	ideasOutput, err := runInDir(t, root, "list", "-i")
	if err != nil {
		t.Fatalf("run list -i = %v, expected nil", err)
	}
	if !strings.Contains(ideasOutput, "Ideas (0/1 done)") {
		t.Fatalf("ideas output = %q, expected ideas section", ideasOutput)
	}
	if strings.Contains(ideasOutput, "Bugs (") {
		t.Fatalf("ideas output = %q, expected bugs section hidden", ideasOutput)
	}
}

func TestRunListBugsAndIdeasUseDependencyBlockedCheckbox(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	setAuxTaskDependsOn(t, root, "bugs", "B1", []string{"P1.M1.E1.T001"})
	setAuxTaskDependsOn(t, root, "ideas", "I1", []string{"P1.M1.E1.T001"})

	bugsOutput, err := runInDir(t, root, "list", "--bugs")
	if err != nil {
		t.Fatalf("run list --bugs = %v, expected nil", err)
	}
	if !strings.Contains(bugsOutput, "[~]") || !strings.Contains(bugsOutput, "B1: Root bug") {
		t.Fatalf("bugs output = %q, expected dependency-blocked checkbox for bug", bugsOutput)
	}

	ideasOutput, err := runInDir(t, root, "list", "--ideas")
	if err != nil {
		t.Fatalf("run list --ideas = %v, expected nil", err)
	}
	if !strings.Contains(ideasOutput, "[~]") || !strings.Contains(ideasOutput, "I1: Root idea") {
		t.Fatalf("ideas output = %q, expected dependency-blocked checkbox for idea", ideasOutput)
	}
}

func TestRunTreeAndShowUseDependencyBlockedCheckboxForTasks(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	addDependencyForWorkflowTask(t, root, "P1.M1.E1.T002", []string{"P1.M1.E1.T001"})

	treeOutput, err := runInDir(t, root, "tree")
	if err != nil {
		t.Fatalf("run tree = %v, expected nil", err)
	}
	if !strings.Contains(treeOutput, "[~]  P1.M1.E1.T002: b") {
		t.Fatalf("tree output = %q, expected dependency-blocked checkbox for task", treeOutput)
	}

	showOutput, err := runInDir(t, root, "show", "P1.M1.E1")
	if err != nil {
		t.Fatalf("run show P1.M1.E1 = %v, expected nil", err)
	}
	if !strings.Contains(showOutput, "[~]  P1.M1.E1.T002 b") {
		t.Fatalf("show output = %q, expected dependency-blocked checkbox for epic task", showOutput)
	}
}

func TestRunListAuxFallbackTitleFromFilename(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	dataDir := filepath.Join(root, ".tasks")

	// Remove title metadata from indexes so renderer must fallback.
	bugsIndex := readYAMLMap(t, filepath.Join(dataDir, "bugs", "index.yaml"))
	if bugs, ok := bugsIndex["bugs"].([]interface{}); ok && len(bugs) > 0 {
		if first, ok := bugs[0].(map[string]interface{}); ok {
			delete(first, "title")
		}
	}
	writeYAMLMap(t, filepath.Join(dataDir, "bugs", "index.yaml"), bugsIndex)

	ideasIndex := readYAMLMap(t, filepath.Join(dataDir, "ideas", "index.yaml"))
	if ideas, ok := ideasIndex["ideas"].([]interface{}); ok && len(ideas) > 0 {
		if first, ok := ideas[0].(map[string]interface{}); ok {
			delete(first, "title")
		}
	}
	writeYAMLMap(t, filepath.Join(dataDir, "ideas", "index.yaml"), ideasIndex)

	// Remove title from aux todo frontmatter.
	if err := os.WriteFile(
		filepath.Join(dataDir, "bugs", "bug-task.todo"),
		[]byte(strings.Join([]string{
			"---",
			"id: B1",
			"status: pending",
			"estimate_hours: 1",
			"complexity: medium",
			"priority: high",
			"depends_on: []",
			"tags: []",
			"---",
			"# Bug task",
		}, "\n")),
		0o644,
	); err != nil {
		t.Fatalf("rewrite bug task without title: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(dataDir, "ideas", "idea-task.todo"),
		[]byte(strings.Join([]string{
			"---",
			"id: I1",
			"status: pending",
			"estimate_hours: 1",
			"complexity: medium",
			"priority: high",
			"depends_on: []",
			"tags: []",
			"---",
			"# Idea task",
		}, "\n")),
		0o644,
	); err != nil {
		t.Fatalf("rewrite idea task without title: %v", err)
	}

	output, err := runInDir(t, root, "list")
	if err != nil {
		t.Fatalf("run list = %v, expected nil", err)
	}
	if !strings.Contains(output, "B1: bug task") {
		t.Fatalf("output = %q, expected bug fallback title from filename", output)
	}
	if !strings.Contains(output, "I1: idea task") {
		t.Fatalf("output = %q, expected idea fallback title from filename", output)
	}
}

func TestRunListHelpRendersCommandSpecificGuidance(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output, err := runInDir(t, root, "list", "--help")
	if err != nil {
		t.Fatalf("run list --help = %v, expected nil", err)
	}
	assertContainsAll(
		t,
		output,
		"Command Help: backlog list",
		"Usage:",
		"backlog list [<SCOPE> ...] [options]",
		"--status",
	)
}

func TestRunLsHelpRendersCommandSpecificGuidance(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output, err := runInDir(t, root, "ls", "--help")
	if err != nil {
		t.Fatalf("run ls --help = %v, expected nil", err)
	}
	assertContainsAll(
		t,
		output,
		"Command Help: backlog list",
		"Usage:",
		"--help, -h",
	)
}

func TestRunShowHelpRendersCommandSpecificGuidance(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output, err := runInDir(t, root, "show", "--help")
	if err != nil {
		t.Fatalf("run show --help = %v, expected nil", err)
	}
	assertContainsAll(
		t,
		output,
		"Command Help: backlog show",
		"Usage:",
		"backlog show [PATH_ID ...] [--long] [--all]",
	)
}

func TestRunAllCommandsHelpIsNotThin(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	commandsToCheck := []string{
		"howto", "add", "add-epic", "add-milestone", "add-phase", "admin", "agents", "benchmark",
		"blocked", "blockers", "bug", "cat", "check", "claim", "cycle", "dash", "data", "edit", "done", "fixed",
		"grab", "handoff", "help", "idea", "init", "list", "lock", "log", "migrate", "move", "next",
		"preview", "ci", "report", "schema", "search", "session", "set", "show", "skills", "skip", "sync",
		"timeline", "tree", "unclaim", "unclaim-stale", "undone", "unlock", "update", "velocity",
		"version", "why",
		"work",
	}
	for _, command := range commandsToCheck {
		command := command
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			output, err := runInDir(t, root, command, "--help")
			if err != nil {
				t.Fatalf("run %s --help = %v, expected nil", command, err)
			}
			if strings.TrimSpace(output) == "Usage: backlog "+command {
				t.Fatalf("output for %s is thin usage-only help: %q", command, output)
			}
			if !strings.Contains(output, "Usage:") && !strings.Contains(output, "Usage") {
				t.Fatalf("output for %s missing usage guidance: %q", command, output)
			}
		})
	}
}

func TestRunListRejectsUnknownFlag(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	_, err := runInDir(t, root, "list", "--unknown")
	if err == nil {
		t.Fatalf("run list expected unknown flag error")
	}
	if !strings.Contains(err.Error(), "unexpected flag: --unknown") {
		t.Fatalf("err = %q, expected unexpected flag error", err)
	}
}

func TestRunListIncorrectArgsPrintsHelpAndError(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "list", "--unknown")
	if err == nil {
		t.Fatalf("run list --unknown expected error")
	}
	assertContainsAll(t, output, "Command Help: backlog list", "Usage:", "unexpected flag: --unknown")

	output, err = runInDir(t, root, "list", "P1.M1", "P1.M2")
	if err != nil {
		t.Fatalf("run list P1.M1 P1.M2 = %v, expected nil", err)
	}
	assertContainsAll(t, output, "Milestone One", "Milestone Two")

	output, err = runInDir(t, root, "list", "P1.M1", "P9")
	if err == nil {
		t.Fatalf("run list P1.M1 P9 expected error")
	}
	if !strings.Contains(err.Error(), "No list nodes found for path query: P9") {
		t.Fatalf("err = %q, expected missing path query error", err)
	}
}

func TestRunListScopedMilestoneFiltersNestedItems(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "list", "P1.M1")
	if err != nil {
		t.Fatalf("run list P1.M1 = %v, expected nil", err)
	}
	if !strings.Contains(output, "Milestone One (") {
		t.Fatalf("output = %q, expected only milestone one totals", output)
	}
	if strings.Contains(output, "Milestone Two") {
		t.Fatalf("output = %q, expected milestone two to be filtered out", output)
	}
}

func TestRunListScopedEpicFiltersTasksInJSON(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "list", "--json", "P1.M1.E1")
	if err != nil {
		t.Fatalf("run list --json P1.M1.E1 = %v, expected nil", err)
	}
	if !strings.Contains(output, "P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected scoped epic task", output)
	}
	if strings.Contains(output, "P1.M1.E2.T001") {
		t.Fatalf("output = %q, expected scoped task filtering by epic", output)
	}
}

func TestRunListScopedWildcardScopeMatchesAllMilestones(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "list", "P1.*")
	if err != nil {
		t.Fatalf("run list P1.* = %v, expected nil", err)
	}
	if strings.Contains(output, "No list nodes found for path query") {
		t.Fatalf("output = %q, expected scoped wildcard match", output)
	}
	if !strings.Contains(output, "Milestone One (P1.M1)") {
		t.Fatalf("output = %q, expected milestone one in wildcard scope output", output)
	}
	if !strings.Contains(output, "Milestone Two (P1.M2)") {
		t.Fatalf("output = %q, expected milestone two in wildcard scope output", output)
	}
}

func TestRunListScopedRecursiveWildcardScopeMatchesNestedItems(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "list", "P1.**")
	if err != nil {
		t.Fatalf("run list P1.** = %v, expected nil", err)
	}
	if !strings.Contains(output, "Milestone One (P1.M1)") {
		t.Fatalf("output = %q, expected milestone one in recursive wildcard scope output", output)
	}
	if !strings.Contains(output, "Milestone Two (P1.M2)") {
		t.Fatalf("output = %q, expected milestone two in recursive wildcard scope output", output)
	}
	if !strings.Contains(output, "Milestone Two Epic") {
		t.Fatalf("output = %q, expected nested epic from milestone two in recursive wildcard scope output", output)
	}
}

func TestRunListScopedDepthIncludesEpicAndTaskLevel(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "list", "P1.M1")
	if err != nil {
		t.Fatalf("run list P1.M1 = %v, expected nil", err)
	}
	if !strings.Contains(output, "Milestone One") {
		t.Fatalf("output = %q, expected milestone in scoped output", output)
	}
	if !strings.Contains(output, "Epic One") {
		t.Fatalf("output = %q, expected epic level in scoped output", output)
	}
	if !strings.Contains(output, "Epic One Task") {
		t.Fatalf("output = %q, expected task lines in scoped output", output)
	}
}

func TestRunListStatsCountCompletedTasksByDefault(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "list")
	if err != nil {
		t.Fatalf("run list = %v, expected nil", err)
	}
	if !strings.Contains(output, "Primary Phase (P1) (1/4 tasks done)") {
		t.Fatalf("output = %q, expected phase done count to include completed tasks", output)
	}
	if !strings.Contains(output, "Milestone One (P1.M1) (1/3 tasks done)") {
		t.Fatalf("output = %q, expected milestone one done count to include completed tasks", output)
	}
	if strings.Contains(output, "Primary Phase (P1) (0/4 tasks done)") {
		t.Fatalf("output = %q, expected fixed completion counts, not 0/4", output)
	}
}

func TestRunListScopedCountsIncludeCompletedTasks(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "list", "P1.M1")
	if err != nil {
		t.Fatalf("run list P1.M1 = %v, expected nil", err)
	}
	if !strings.Contains(output, "Primary Phase (P1) (1/3 tasks done)") {
		t.Fatalf("output = %q, expected scoped phase done count to include completed tasks", output)
	}
	if !strings.Contains(output, "Milestone One (P1.M1) (1/3 tasks done)") {
		t.Fatalf("output = %q, expected milestone completion to include completed tasks", output)
	}
}

func TestRunListAllDoneShowsCompletedTotals(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "done", "", "")
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T002", "b", "done", "", "")

	output, err := runInDir(t, root, "list", "P1.M1.E1")
	if err != nil {
		t.Fatalf("run list P1.M1.E1 = %v, expected nil", err)
	}
	if !strings.Contains(output, "Phase (P1) (2/2 tasks done)") {
		t.Fatalf("output = %q, expected completed totals in scope", output)
	}
	if !strings.Contains(output, "Milestone (P1.M1) (2/2 tasks done)") {
		t.Fatalf("output = %q, expected completed milestone totals in scope", output)
	}
}

func TestRunLsRejectsUnknownFlag(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_, err := runInDir(t, root, "ls", "--unknown")
	if err == nil {
		t.Fatalf("run ls expected unknown flag error")
	}
	if !strings.Contains(err.Error(), "unexpected flag: --unknown") {
		t.Fatalf("err = %q, expected unexpected flag error", err)
	}
}

func TestRunLsAcceptsMultipleScopesAndFailsStrictly(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "ls", "P1", "P1.M1")
	if err != nil {
		t.Fatalf("run ls P1 P1.M1 = %v, expected nil", err)
	}
	assertContainsAll(t, output, "P1.M1: Milestone One", "P1.M1.E1: Epic One")

	output, err = runInDir(t, root, "ls", "P1", "P9")
	if err == nil {
		t.Fatalf("run ls P1 P9 expected error")
	}
	if !strings.Contains(err.Error(), "Phase not found: P9") {
		t.Fatalf("err = %q, expected phase not found", err)
	}
}

func TestRunLsRootIncludesBugsIdeasAndFixesSummaries(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	if _, err := runInDir(t, root, "fixed", "--title", "restore stale auth token", "--at", "2026-02-20T12:00:00Z"); err != nil {
		t.Fatalf("run fixed = %v, expected nil", err)
	}

	output, err := runInDir(t, root, "ls")
	if err != nil {
		t.Fatalf("run ls = %v, expected nil", err)
	}
	if !strings.Contains(output, "Bugs (0/1)") {
		t.Fatalf("output = %q, expected bug summary", output)
	}
	if !strings.Contains(output, "Ideas (0/1)") {
		t.Fatalf("output = %q, expected idea summary", output)
	}
	if !strings.Contains(output, "Fixes (1/1)") {
		t.Fatalf("output = %q, expected fixed summary", output)
	}
}

func TestRunListWarnsMissingTaskFiles(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	setTaskTodoFilePath(t, root, "P1.M1.E1.T001", filepath.Join("01-phase", "01-ms", "01-epic", "missing.todo"))

	output, err := runInDir(t, root, "list")
	if err != nil {
		t.Fatalf("run list = %v, expected nil", err)
	}
	if !strings.Contains(output, "Warning: 1 task file(s) referenced in index are missing.") {
		t.Fatalf("output = %q, expected missing file warning", output)
	}
	if !strings.Contains(output, "P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected missing task id", output)
	}
}

func TestRunListJSONSkipsMissingTaskFilesWarning(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	setTaskTodoFilePath(t, root, "P1.M1.E1.T001", filepath.Join("01-phase", "01-ms", "01-epic", "missing.todo"))

	output, err := runInDir(t, root, "list", "--json")
	if err != nil {
		t.Fatalf("run list --json = %v, expected nil", err)
	}
	if strings.Contains(output, "Warning:") {
		t.Fatalf("output = %q, expected no missing-file warning for JSON mode", output)
	}
}

type cliListJSONTask struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Status         string   `json:"status"`
	EstimateHours  float64  `json:"estimate_hours"`
	Complexity     string   `json:"complexity"`
	Priority       string   `json:"priority"`
	OnCriticalPath bool     `json:"on_critical_path"`
	FileExists     bool     `json:"file_exists"`
	File           string   `json:"file"`
	GrabAdditional []string `json:"grab_additional"`
}

type cliListJSONMilestone struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Status string         `json:"status"`
	Stats  map[string]int `json:"stats"`
}

type cliListJSONPhase struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Status     string                 `json:"status"`
	Stats      map[string]int         `json:"stats"`
	Milestones []cliListJSONMilestone `json:"milestones"`
}

type cliListJSON struct {
	CriticalPath  []string           `json:"critical_path"`
	NextAvailable string             `json:"next_available"`
	Phases        []cliListJSONPhase `json:"phases"`
	Tasks         []cliListJSONTask  `json:"tasks"`
	Bugs          []cliListJSONTask  `json:"bugs"`
	Ideas         []cliListJSONTask  `json:"ideas"`
	Filter        map[string]string  `json:"filter"`
}

type cliNextJSON struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	File           string   `json:"file"`
	FileExists     bool     `json:"file_exists"`
	EstimateHours  float64  `json:"estimate_hours"`
	Complexity     string   `json:"complexity"`
	Status         string   `json:"status"`
	Priority       string   `json:"priority"`
	OnCriticalPath bool     `json:"on_critical_path"`
	GrabAdditional []string `json:"grab_additional"`
}

type cliPreviewJSON struct {
	CriticalPath  []string          `json:"critical_path"`
	Normal        []cliListJSONTask `json:"normal"`
	Bugs          []cliListJSONTask `json:"bugs"`
	Ideas         []cliListJSONTask `json:"ideas"`
	NextAvailable string            `json:"next_available"`
}

type cliLogJSONEvent struct {
	TaskID    string    `json:"task_id"`
	Title     string    `json:"title"`
	Event     string    `json:"event"`
	Kind      string    `json:"kind"`
	Timestamp time.Time `json:"timestamp"`
	Actor     *string   `json:"actor"`
}

type cliTreeJSONTask struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Status      string     `json:"status"`
	File        string     `json:"file"`
	FileExists  bool       `json:"file_exists"`
	Estimate    float64    `json:"estimate_hours"`
	Complexity  string     `json:"complexity"`
	Priority    string     `json:"priority"`
	DependsOn   []string   `json:"depends_on"`
	ClaimedBy   *string    `json:"claimed_by"`
	ClaimedAt   *time.Time `json:"claimed_at"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	OnCritical  bool       `json:"on_critical_path"`
}

type cliTreeJSONEpic struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Status string            `json:"status"`
	Tasks  []cliTreeJSONTask `json:"tasks"`
}

type cliTreeJSONMilestone struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Status string            `json:"status"`
	Stats  map[string]int    `json:"stats"`
	Epics  []cliTreeJSONEpic `json:"epics"`
}

type cliTreeJSONPhase struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Status     string                 `json:"status"`
	Stats      map[string]int         `json:"stats"`
	Milestones []cliTreeJSONMilestone `json:"milestones"`
}

type cliTreeJSON struct {
	CriticalPath     []string           `json:"critical_path"`
	NextAvailable    string             `json:"next_available"`
	MaxDepth         int                `json:"max_depth"`
	ShowDetails      bool               `json:"show_details"`
	UnfinishedOnly   bool               `json:"unfinished_only"`
	ShowCompletedAux bool               `json:"show_completed_aux"`
	Phases           []cliTreeJSONPhase `json:"phases"`
	Bugs             []cliTreeJSONTask  `json:"bugs"`
	Ideas            []cliTreeJSONTask  `json:"ideas"`
}

type cliDashJSONCurrentTask struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Agent       string `json:"agent"`
	File        string `json:"file,omitempty"`
	FileExists  bool   `json:"file_exists"`
	Found       bool   `json:"found"`
	WorkingTask bool   `json:"working_task"`
}

type cliDashJSONPhase struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Done            int     `json:"done"`
	Total           int     `json:"total"`
	InProgress      int     `json:"in_progress"`
	Blocked         int     `json:"blocked"`
	PercentComplete float64 `json:"percent_complete"`
}

type cliDashJSONCriticalPath struct {
	Tasks          []string `json:"tasks"`
	RemainingCount int      `json:"remaining_count"`
	RemainingHours float64  `json:"remaining_hours"`
	AllComplete    bool     `json:"all_complete"`
	NextID         string   `json:"next_id,omitempty"`
	NextTitle      string   `json:"next_title,omitempty"`
}

type cliDashJSONOverall struct {
	Done       int     `json:"done"`
	Total      int     `json:"total"`
	InProgress int     `json:"in_progress"`
	Blocked    int     `json:"blocked"`
	Percent    float64 `json:"percent_complete"`
}

type cliDashJSONStatus struct {
	InProgress    int `json:"in_progress"`
	Blocked       int `json:"blocked"`
	StaleClaims   int `json:"stale_claims"`
	ActiveSession int `json:"active_sessions"`
}

type cliDashJSON struct {
	Agent           string                  `json:"agent"`
	CurrentTask     *cliDashJSONCurrentTask `json:"current_task"`
	Overall         cliDashJSONOverall      `json:"overall"`
	Phases          []cliDashJSONPhase      `json:"phases"`
	CompletedPhases []string                `json:"completed_phases"`
	CriticalPath    cliDashJSONCriticalPath `json:"critical_path"`
	Status          cliDashJSONStatus       `json:"status"`
}

type cliAdminJSON struct {
	Command     string `json:"command"`
	Implemented bool   `json:"implemented"`
	Message     string `json:"message"`
	Guidance    string `json:"guidance"`
}

func decodeJSONPayload(t *testing.T, raw string, target interface{}) {
	t.Helper()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		t.Fatalf("expected JSON output, got empty")
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		t.Fatalf("decode payload = %v, raw = %q", err, raw)
	}
}

func listTaskIDs(tasks []cliListJSONTask) map[string]bool {
	out := map[string]bool{}
	for _, task := range tasks {
		out[task.ID] = true
	}
	return out
}

func TestRunListJSONMachineContract(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)

	payloadRaw, err := runInDir(t, root, "list", "--json")
	if err != nil {
		t.Fatalf("run list --json = %v, expected nil", err)
	}

	payload := cliListJSON{}
	decodeJSONPayload(t, payloadRaw, &payload)

	if payload.NextAvailable != "P1.M1.E1.T001" {
		t.Fatalf("next_available = %q, expected P1.M1.E1.T001", payload.NextAvailable)
	}
	if len(payload.CriticalPath) == 0 {
		t.Fatalf("critical_path unexpectedly empty")
	}

	if len(payload.Phases) != 1 {
		t.Fatalf("phases length = %d, expected 1", len(payload.Phases))
	}
	phase := payload.Phases[0]
	if phase.ID != "P1" {
		t.Fatalf("phase id = %s, expected P1", phase.ID)
	}
	if len(phase.Milestones) != 2 {
		t.Fatalf("milestones = %d, expected 2", len(phase.Milestones))
	}
	if phase.Milestones[0].ID != "P1.M1" || phase.Milestones[1].ID != "P1.M2" {
		t.Fatalf("milestone ids = %#v, expected P1.M1 and P1.M2", []string{phase.Milestones[0].ID, phase.Milestones[1].ID})
	}
	if phase.Milestones[0].Stats["done"] != 1 {
		t.Fatalf("milestone M1 done = %d, expected 1", phase.Milestones[0].Stats["done"])
	}
	if phase.Milestones[1].Stats["total"] != 1 {
		t.Fatalf("milestone M2 total = %d, expected 1", phase.Milestones[1].Stats["total"])
	}

	if len(payload.Tasks) != 4 {
		t.Fatalf("tasks length = %d, expected 4", len(payload.Tasks))
	}
	ids := listTaskIDs(payload.Tasks)
	for _, id := range []string{"P1.M1.E1.T001", "P1.M1.E1.T002", "P1.M1.E2.T001", "P1.M2.E1.T001"} {
		if !ids[id] {
			t.Fatalf("task list missing %s", id)
		}
	}

	if len(payload.Bugs) != 1 {
		t.Fatalf("bugs length = %d, expected 1", len(payload.Bugs))
	}
	if payload.Bugs[0].ID != "B1" {
		t.Fatalf("bug id = %s, expected B1", payload.Bugs[0].ID)
	}

	if len(payload.Ideas) != 1 {
		t.Fatalf("ideas length = %d, expected 1", len(payload.Ideas))
	}
	if payload.Ideas[0].ID != "I1" {
		t.Fatalf("idea id = %s, expected I1", payload.Ideas[0].ID)
	}
}

func TestRunNextJSONMachineContract(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	payloadRaw, err := runInDir(t, root, "next", "--json")
	if err != nil {
		t.Fatalf("run next --json = %v, expected nil", err)
	}

	payload := cliNextJSON{}
	decodeJSONPayload(t, payloadRaw, &payload)

	if payload.ID != "P1.M1.E1.T001" {
		t.Fatalf("id = %s, expected P1.M1.E1.T001", payload.ID)
	}
	if !strings.HasSuffix(payload.File, "T001-a.todo") {
		t.Fatalf("file = %s, expected suffix T001-a.todo", payload.File)
	}
	if !payload.FileExists {
		t.Fatalf("file_exists = false, expected true")
	}
	if payload.Status != "pending" {
		t.Fatalf("status = %s, expected pending", payload.Status)
	}
	if !payload.OnCriticalPath {
		t.Fatalf("expected task to be on critical path")
	}
	if len(payload.GrabAdditional) != 1 || payload.GrabAdditional[0] != "P1.M1.E1.T002" {
		t.Fatalf("grab_additional = %#v, expected [P1.M1.E1.T002]", payload.GrabAdditional)
	}
}

func TestRunPreviewJSONMachineContract(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)

	payloadRaw, err := runInDir(t, root, "preview", "--json")
	if err != nil {
		t.Fatalf("run preview --json = %v, expected nil", err)
	}

	payload := cliPreviewJSON{}
	decodeJSONPayload(t, payloadRaw, &payload)

	if payload.NextAvailable != "P1.M1.E1.T001" {
		t.Fatalf("next_available = %s, expected P1.M1.E1.T001", payload.NextAvailable)
	}
	if len(payload.Normal) < 2 {
		t.Fatalf("normal length = %d, expected at least 2", len(payload.Normal))
	}
	if len(payload.Bugs) != 1 {
		t.Fatalf("bugs length = %d, expected 1", len(payload.Bugs))
	}
	if len(payload.Ideas) != 1 {
		t.Fatalf("ideas length = %d, expected 1", len(payload.Ideas))
	}

	normalIDs := listTaskIDs(payload.Normal)
	for _, id := range []string{"P1.M1.E1.T001", "P1.M1.E2.T001"} {
		if !normalIDs[id] {
			t.Fatalf("normal payload missing %s", id)
		}
	}
	bugIDs := listTaskIDs(payload.Bugs)
	if !bugIDs["B1"] {
		t.Fatalf("bug payload missing B1")
	}
	ideaIDs := listTaskIDs(payload.Ideas)
	if !ideaIDs["I1"] {
		t.Fatalf("idea payload missing I1")
	}
}

func TestRunLogJSONMachineContract(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFileWithTimes(
		t,
		root,
		"P1.M1.E1.T001",
		"Task One",
		"done",
		"agent-a",
		"2026-01-01T10:00:00Z",
		"2026-01-01T10:30:00Z",
		"2026-01-01T11:00:00Z",
	)
	writeWorkflowTaskFileWithTimes(
		t,
		root,
		"P1.M1.E1.T002",
		"Task Two",
		"in_progress",
		"agent-b",
		"2026-01-01T10:45:00Z",
		"2026-01-01T11:30:00Z",
		"",
	)

	payloadRaw, err := runInDir(t, root, "log", "--json")
	if err != nil {
		t.Fatalf("run log --json = %v, expected nil", err)
	}
	events := []cliLogJSONEvent{}
	decodeJSONPayload(t, payloadRaw, &events)

	if len(events) < 5 {
		t.Fatalf("events = %#v, expected at least 5", events)
	}
	if events[0].TaskID != "P1.M1.E1.T002" {
		t.Fatalf("first event = %s, expected P1.M1.E1.T002", events[0].TaskID)
	}
	if events[0].Event != "started" {
		t.Fatalf("first event = %q, expected started", events[0].Event)
	}
	if events[0].Kind != "started" {
		t.Fatalf("first kind = %q, expected started", events[0].Kind)
	}
	if events[0].Actor == nil || *events[0].Actor != "agent-b" {
		t.Fatalf("first actor = %v, expected agent-b", events[0].Actor)
	}

	if events[1].TaskID != "P1.M1.E1.T001" {
		t.Fatalf("second event = %s, expected P1.M1.E1.T001", events[1].TaskID)
	}
	if events[1].Event != "completed" {
		t.Fatalf("second event = %q, expected completed", events[1].Event)
	}
	if events[1].Kind != "completed" {
		t.Fatalf("second kind = %q, expected completed", events[1].Kind)
	}
	if events[1].Actor == nil || *events[1].Actor != "agent-a" {
		t.Fatalf("second actor = %v, expected agent-a", events[1].Actor)
	}

	foundClaimed := false
	for _, event := range events {
		if event.Event == "claimed" {
			foundClaimed = true
			break
		}
	}
	if !foundClaimed {
		t.Fatalf("expected at least one claimed event, got %#v", events)
	}
}

func TestRunLogJSONIncludesAddedEvents(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	taskOnePath := filepath.Join(root, ".tasks", workflowTaskFilePath("P1.M1.E1.T001"))
	taskTwoPath := filepath.Join(root, ".tasks", workflowTaskFilePath("P1.M1.E1.T002"))
	if err := os.Chtimes(taskOnePath, now, now); err != nil {
		t.Fatalf("set task one mtime = %v, expected nil", err)
	}
	if err := os.Chtimes(taskTwoPath, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatalf("set task two mtime = %v, expected nil", err)
	}

	payloadRaw, err := runInDir(t, root, "log", "--json")
	if err != nil {
		t.Fatalf("run log --json = %v, expected nil", err)
	}
	events := []cliLogJSONEvent{}
	decodeJSONPayload(t, payloadRaw, &events)
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, expected 2", len(events))
	}
	for _, event := range events {
		if event.Event != "added" {
			t.Fatalf("event = %q, expected added", event.Event)
		}
		if event.Kind != "created" {
			t.Fatalf("kind = %q, expected created", event.Kind)
		}
		if event.Actor != nil {
			t.Fatalf("actor = %v, expected nil", event.Actor)
		}
	}
}

func TestRunLogJSONFiltersBugsOnly(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)

	payloadRaw, err := runInDir(t, root, "log", "--json", "--bugs")
	if err != nil {
		t.Fatalf("run log --json --bugs = %v, expected nil", err)
	}
	events := []cliLogJSONEvent{}
	decodeJSONPayload(t, payloadRaw, &events)

	if len(events) != 1 {
		t.Fatalf("len(events) = %d, expected 1", len(events))
	}
	if events[0].TaskID != "B1" {
		t.Fatalf("task_id = %q, expected B1", events[0].TaskID)
	}
	if events[0].Event != "added" {
		t.Fatalf("event = %q, expected added", events[0].Event)
	}
}

func TestRunLogJSONFiltersIdeasOnly(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)

	payloadRaw, err := runInDir(t, root, "log", "--json", "--ideas")
	if err != nil {
		t.Fatalf("run log --json --ideas = %v, expected nil", err)
	}
	events := []cliLogJSONEvent{}
	decodeJSONPayload(t, payloadRaw, &events)

	if len(events) != 1 {
		t.Fatalf("len(events) = %d, expected 1", len(events))
	}
	if events[0].TaskID != "I1" {
		t.Fatalf("task_id = %q, expected I1", events[0].TaskID)
	}
	if events[0].Event != "added" {
		t.Fatalf("event = %q, expected added", events[0].Event)
	}
}

func TestRunLogJSONFiltersBugsAndIdeasTogether(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)

	payloadRaw, err := runInDir(t, root, "log", "--json", "--bugs", "--ideas")
	if err != nil {
		t.Fatalf("run log --json --bugs --ideas = %v, expected nil", err)
	}
	events := []cliLogJSONEvent{}
	decodeJSONPayload(t, payloadRaw, &events)

	if len(events) != 2 {
		t.Fatalf("len(events) = %d, expected 2", len(events))
	}

	foundBug := false
	foundIdea := false
	for _, event := range events {
		if event.TaskID == "B1" {
			foundBug = true
		}
		if event.TaskID == "I1" {
			foundIdea = true
		}
		if strings.HasPrefix(event.TaskID, "P1.") {
			t.Fatalf("task_id = %q, expected aux task only", event.TaskID)
		}
	}
	if !foundBug {
		t.Fatalf("expected bug event in filtered output")
	}
	if !foundIdea {
		t.Fatalf("expected idea event in filtered output")
	}
}

func TestRunTreeJSONMachineContract(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)

	payloadRaw, err := runInDir(t, root, "tree", "--json")
	if err != nil {
		t.Fatalf("run tree --json = %v, expected nil", err)
	}
	payload := cliTreeJSON{}
	decodeJSONPayload(t, payloadRaw, &payload)

	if payload.MaxDepth != 4 {
		t.Fatalf("max_depth = %d, expected 4", payload.MaxDepth)
	}
	if payload.ShowDetails {
		t.Fatalf("show_details = %v, expected false", payload.ShowDetails)
	}
	if payload.UnfinishedOnly {
		t.Fatalf("unfinished_only = %v, expected false", payload.UnfinishedOnly)
	}
	if len(payload.Phases) != 1 {
		t.Fatalf("phases length = %d, expected 1", len(payload.Phases))
	}
	phase := payload.Phases[0]
	if len(phase.Milestones) != 2 {
		t.Fatalf("milestones = %d, expected 2", len(phase.Milestones))
	}
	if phase.Milestones[0].ID != "P1.M1" {
		t.Fatalf("milestone id = %s, expected P1.M1", phase.Milestones[0].ID)
	}
	if phase.Stats["total"] <= 0 {
		t.Fatalf("phase.total = %d, expected > 0", phase.Stats["total"])
	}
	if len(payload.Bugs) != 1 || payload.Bugs[0].ID != "B1" {
		t.Fatalf("bugs payload = %#v, expected one bug B1", payload.Bugs)
	}
	if len(payload.Ideas) != 1 || payload.Ideas[0].ID != "I1" {
		t.Fatalf("ideas payload = %#v, expected one idea I1", payload.Ideas)
	}
}

func TestRunTreeJSONUnfinishedFiltersCompletedTasks(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)

	payloadRaw, err := runInDir(t, root, "tree", "--json", "--unfinished")
	if err != nil {
		t.Fatalf("run tree --json --unfinished = %v, expected nil", err)
	}
	payload := cliTreeJSON{}
	decodeJSONPayload(t, payloadRaw, &payload)
	if !payload.UnfinishedOnly {
		t.Fatalf("unfinished_only = %v, expected true", payload.UnfinishedOnly)
	}
	milestones := payload.Phases[0].Milestones
	if len(milestones) != 2 {
		t.Fatalf("milestones = %d, expected 2", len(milestones))
	}
	if milestones[0].Epics[0].ID != "P1.M1.E1" {
		t.Fatalf("epic id = %s, expected P1.M1.E1", milestones[0].Epics[0].ID)
	}
	if len(milestones[0].Epics[0].Tasks) != 1 {
		t.Fatalf("unfinished tasks in first epic = %d, expected 1", len(milestones[0].Epics[0].Tasks))
	}
}

func TestRunTreeHidesCompletedBugsByDefault(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	setAuxTaskStatus(t, root, "bugs", "B1", "done")

	output, err := runInDir(t, root, "tree")
	if err != nil {
		t.Fatalf("run tree = %v, expected nil", err)
	}
	if strings.Contains(output, "Root bug") {
		t.Fatalf("output = %q, expected completed bug hidden by default", output)
	}

	output, err = runInDir(t, root, "tree", "--show-completed-aux")
	if err != nil {
		t.Fatalf("run tree --show-completed-aux = %v, expected nil", err)
	}
	if !strings.Contains(output, "Root bug") {
		t.Fatalf("output = %q, expected completed bug when show-completed-aux is set", output)
	}
}

func TestRunTreePathQueryHidesAuxItemsFromText(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "tree", "P1.M1.E1")
	if err != nil {
		t.Fatalf("run tree P1.M1.E1 = %v, expected nil", err)
	}

	if strings.Contains(output, "Bugs") {
		t.Fatalf("output = %q, expected Bugs section to be scoped out", output)
	}
	if strings.Contains(output, "Ideas") {
		t.Fatalf("output = %q, expected Ideas section to be scoped out", output)
	}
	if strings.Contains(output, "Root bug") {
		t.Fatalf("output = %q, expected scoped tree to hide bug", output)
	}
	if strings.Contains(output, "Root idea") {
		t.Fatalf("output = %q, expected scoped tree to hide idea", output)
	}
}

func TestRunTreePathQueryNoMatchStillPrintsNoMatchMessageWhenAuxExists(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "tree", "P9")
	if err != nil {
		t.Fatalf("run tree P9 = %v, expected nil", err)
	}
	if !strings.Contains(output, "No tree nodes found for path query: P9") {
		t.Fatalf("output = %q, expected no-match message", output)
	}
}

func TestRunTreeAcceptsMultiplePathQueries(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	output, err := runInDir(t, root, "tree", "P1.M1.E1", "P1.M2.E1")
	if err != nil {
		t.Fatalf("run tree P1.M1.E1 P1.M2.E1 = %v, expected nil", err)
	}
	if strings.Contains(output, "No tree nodes found for path query") {
		t.Fatalf("output = %q, expected matches for multi-query run", output)
	}
	if !strings.Contains(output, "P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected P1.M1.E1.T001", output)
	}
	if !strings.Contains(output, "P1.M1.E1.T002") {
		t.Fatalf("output = %q, expected P1.M1.E1.T002", output)
	}
	if !strings.Contains(output, "P1.M2.E1.T001") {
		t.Fatalf("output = %q, expected P1.M2.E1.T001", output)
	}
}

func TestRunTreePathQueryJSONExcludesAuxItems(t *testing.T) {
	t.Parallel()

	root := setupListAuxAndScopeFixture(t)
	payloadRaw, err := runInDir(t, root, "tree", "P1.M1.E1", "--json")
	if err != nil {
		t.Fatalf("run tree P1.M1.E1 --json = %v, expected nil", err)
	}

	payload := cliTreeJSON{}
	decodeJSONPayload(t, payloadRaw, &payload)

	if len(payload.Bugs) != 0 {
		t.Fatalf("bugs payload = %#v, expected empty for scoped path query", payload.Bugs)
	}
	if len(payload.Ideas) != 0 {
		t.Fatalf("ideas payload = %#v, expected empty for scoped path query", payload.Ideas)
	}
}

func TestRunDashCommandOutputsCurrentTaskAndProgress(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	dataDir := filepath.Join(root, ".tasks")
	if err := taskcontext.SetCurrentTask(dataDir, "P1.M1.E1.T001", "agent-test"); err != nil {
		t.Fatalf("set current task = %v", err)
	}

	output, err := runInDir(t, root, "dash")
	if err != nil {
		t.Fatalf("run dash = %v, expected nil", err)
	}
	if !strings.Contains(output, "Current Task") {
		t.Fatalf("output = %q, expected current task header", output)
	}
	if !strings.Contains(output, "P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected current task id", output)
	}
	if !strings.Contains(output, "Agent: agent-test") {
		t.Fatalf("output = %q, expected current agent", output)
	}
	if !strings.Contains(output, "Progress:") {
		t.Fatalf("output = %q, expected Progress section", output)
	}
	if !strings.Contains(output, "Critical Path:") {
		t.Fatalf("output = %q, expected Critical Path section", output)
	}
	if !strings.Contains(output, "Status:") {
		t.Fatalf("output = %q, expected Status section", output)
	}
}

func TestRunDashJSONCommandMachineContract(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	if err := taskcontext.SetCurrentTask(filepath.Join(root, ".tasks"), "P1.M1.E1.T001", "agent-test"); err != nil {
		t.Fatalf("set current task = %v", err)
	}
	if err := writeTodoTaskFromMap(filepath.Join(root, ".tasks", workflowTaskFilePath("P1.M1.E1.T001")), map[string]interface{}{
		"id":             "P1.M1.E1.T001",
		"title":          "a",
		"status":         "in_progress",
		"estimate_hours": 1,
		"complexity":     "medium",
		"priority":       "medium",
		"depends_on":     []string{},
		"tags":           []string{},
		"claimed_by":     "agent-test",
		"claimed_at":     "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("write todo file = %v", err)
	}
	if err := writeTodoTaskFromMap(filepath.Join(root, ".tasks", workflowTaskFilePath("P1.M1.E1.T002")), map[string]interface{}{
		"id":             "P1.M1.E1.T002",
		"title":          "b",
		"status":         "done",
		"estimate_hours": 1,
		"complexity":     "medium",
		"priority":       "medium",
		"depends_on":     []string{},
		"tags":           []string{},
	}); err != nil {
		t.Fatalf("write todo file = %v", err)
	}

	output, err := runInDir(t, root, "dash", "--json")
	if err != nil {
		t.Fatalf("run dash --json = %v", err)
	}

	payload := cliDashJSON{}
	decodeJSONPayload(t, output, &payload)
	if payload.Agent != "agent-test" {
		t.Fatalf("agent = %q, expected agent-test", payload.Agent)
	}
	if payload.CurrentTask == nil {
		t.Fatalf("current_task = nil, expected payload")
	}
	if payload.CurrentTask.ID != "P1.M1.E1.T001" {
		t.Fatalf("current_task.id = %q, expected P1.M1.E1.T001", payload.CurrentTask.ID)
	}
	if payload.CurrentTask.WorkingTask != true {
		t.Fatalf("current_task.working_task = %v, expected true", payload.CurrentTask.WorkingTask)
	}
	if payload.Overall.Total != 2 {
		t.Fatalf("overall.total = %d, expected 2", payload.Overall.Total)
	}
	if payload.Overall.Done != 1 {
		t.Fatalf("overall.done = %d, expected 1", payload.Overall.Done)
	}
	if payload.Overall.InProgress != 1 {
		t.Fatalf("overall.in_progress = %d, expected 1", payload.Overall.InProgress)
	}
	if payload.CriticalPath.AllComplete {
		t.Fatalf("critical_path.all_complete = true, expected false")
	}
	if payload.CriticalPath.RemainingCount < 1 {
		t.Fatalf("critical_path.remaining_count = %d, expected >=1", payload.CriticalPath.RemainingCount)
	}
	if len(payload.Phases) != 1 {
		t.Fatalf("phases length = %d, expected 1", len(payload.Phases))
	}
}

func TestRunAdminCommandNotImplemented(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "admin")
	if err != nil {
		t.Fatalf("run admin = %v, expected nil", err)
	}
	if !strings.Contains(output, "admin command is not implemented in the Go client.") {
		t.Fatalf("output = %q, expected not implemented message", output)
	}
	if !strings.Contains(output, "Use `backlog dash` to inspect current project status.") {
		t.Fatalf("output = %q, expected usage guidance", output)
	}

	output, err = runInDir(t, root, "admin", "--help")
	if err != nil {
		t.Fatalf("run admin --help = %v, expected nil", err)
	}
	if !strings.Contains(output, "Usage: backlog admin") {
		t.Fatalf("output = %q, expected admin usage", output)
	}
}

func TestRunAdminCommandJSONNotImplemented(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "admin", "--json")
	if err != nil {
		t.Fatalf("run admin --json = %v, expected nil", err)
	}

	payload := cliAdminJSON{}
	decodeJSONPayload(t, output, &payload)
	if payload.Command != "admin" {
		t.Fatalf("command = %q, expected admin", payload.Command)
	}
	if payload.Implemented != false {
		t.Fatalf("implemented = %v, expected false", payload.Implemented)
	}
	if !strings.Contains(payload.Message, "not implemented in the Go client") {
		t.Fatalf("message = %q, expected not implemented message", payload.Message)
	}
	if !strings.Contains(payload.Guidance, "Use `backlog dash`") {
		t.Fatalf("guidance = %q, expected usage guidance", payload.Guidance)
	}
}

func TestRunDoneRejectsMalformedTaskID(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	_, err := runInDir(t, root, "done", "not-a-task")
	if err == nil {
		t.Fatalf("run done expected malformed ID error")
	}
	if !strings.Contains(err.Error(), "malformed task id: not-a-task") {
		t.Fatalf("err = %q, expected malformed ID error", err)
	}
}

func TestRunDoneRejectsMissingStatusValue(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	_, err := runInDir(t, root, "done", "P1.M1.E1.T001", "--status")
	if err == nil {
		t.Fatalf("run done --status expected missing value error")
	}
	if !strings.Contains(err.Error(), "expected value for --status") {
		t.Fatalf("err = %q, expected missing --status value message", err)
	}
}

func TestRunDoneRejectsInvalidStatusValue(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	_, err := runInDir(t, root, "done", "--status", "not-a-status", "P1.M1.E1.T001")
	if err == nil {
		t.Fatalf("run done expected invalid status error")
	}
	if !strings.Contains(err.Error(), "invalid status: not-a-status") {
		t.Fatalf("err = %q, expected invalid status error", err)
	}
}

func TestRunDoneRejectsIncompatibleStatusTransition(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	_, err := runInDir(t, root, "done", "--status", "rejected", "P1.M1.E1.T001")
	if err == nil {
		t.Fatalf("run done expected transition error")
	}
	if !strings.Contains(err.Error(), "cannot transition from 'pending' to 'rejected'") {
		t.Fatalf("err = %q, expected transition error", err)
	}
}

func TestRunDoneCompletesInProgressTaskWithDefaultStatus(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "in_progress", "", "")

	output, err := runInDir(t, root, "done", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run done = %v, expected nil", err)
	}
	if !strings.Contains(output, "Completed: P1.M1.E1.T001 - a") {
		t.Fatalf("output = %q, expected completed confirmation", output)
	}

	taskText := readFile(t, filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "T001-a.todo"))
	if !strings.Contains(taskText, "status: done") {
		t.Fatalf("task file status = %q, expected done", taskText)
	}
}

func TestRunDoneRejectsMissingTaskFile(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "in_progress", "", "")
	setTaskTodoFilePath(t, root, "P1.M1.E1.T001", filepath.Join("01-phase", "01-ms", "01-epic", "missing.todo"))

	_, err := runInDir(t, root, "done", "P1.M1.E1.T001")
	if err == nil {
		t.Fatalf("run done expected missing file error")
	}
	if !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("err = %q, expected no such file error", err)
	}
}

func TestRunDoneShowsDetailedEpicCompletionNotice(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "done", "", "")
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T002", "b", "in_progress", "", "")

	output, err := runInDir(t, root, "done", "P1.M1.E1.T002")
	if err != nil {
		t.Fatalf("run done = %v, expected nil", err)
	}
	if !strings.Contains(output, "Completed: P1.M1.E1.T002 - b") {
		t.Fatalf("output = %q, expected completed confirmation", output)
	}
	if !strings.Contains(output, "EPIC COMPLETE") {
		t.Fatalf("output = %q, expected epic complete heading", output)
	}
	if !strings.Contains(output, "Path: .tasks/01-phase/01-ms/01-epic") {
		t.Fatalf("output = %q, expected epic path", output)
	}
	if !strings.Contains(output, "NEXT_STEPS:") {
		t.Fatalf("output = %q, expected NEXT_STEPS block", output)
	}
	if !strings.Contains(output, "Review the completed epic before moving on.") {
		t.Fatalf("output = %q, expected epic review instruction", output)
	}
	if !strings.Contains(output, "Spawn a review subagent to verify implementation") {
		t.Fatalf("output = %q, expected review subagent suggestion", output)
	}
	if !strings.Contains(output, "Check acceptance criteria in .tasks/01-phase/01-ms/01-epic are met") {
		t.Fatalf("output = %q, expected acceptance criteria check", output)
	}
	if !strings.Contains(output, "Ensure integration between all tasks in the epic") {
		t.Fatalf("output = %q, expected integration check", output)
	}
	if !strings.Contains(output, "Run liter (if available) and fix any warnings") {
		t.Fatalf("output = %q, expected linter guidance", output)
	}
}

func TestRunDoneAutoCommitCreatesBlDoneCommit(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	initializeTestGitRepo(t, root)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "in_progress", "agent-x", "2026-01-01T00:00:00Z")

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(t, root, "done", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run done = %v", err)
	}
	if !strings.Contains(output, "Completed: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected completion output", output)
	}
	if gitCommitCount(t, root) != commitCount+1 {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount+1)
	}
	if message := strings.TrimSpace(runGit(t, root, "log", "-1", "--pretty=%B")); message != "bl done P1.M1.E1.T001: a" {
		t.Fatalf("latest commit = %q, expected %q", message, "bl done P1.M1.E1.T001: a")
	}
}

func TestRunDoneAutoCommitSkipsWhenStagedChangesExist(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	initializeTestGitRepo(t, root)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "in_progress", "agent-x", "2026-01-01T00:00:00Z")

	stagedPath := filepath.Join(root, "staged-change.txt")
	if err := os.WriteFile(stagedPath, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	runGit(t, root, "add", "staged-change.txt")

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(t, root, "done", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run done = %v", err)
	}
	if !strings.Contains(output, "Completed: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected completion output", output)
	}
	if gitCommitCount(t, root) != commitCount {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount)
	}
	if status := runGit(t, root, "status", "--short"); !strings.Contains(status, "A  staged-change.txt") {
		t.Fatalf("status = %q, expected staged change to be preserved", status)
	}
}

func TestRunUndoneAutoCommitCreatesBlUndoneCommit(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFileWithTimes(
		t,
		root,
		"P1.M1.E1.T001",
		"a",
		"done",
		"",
		"",
		"",
		"2026-01-01T00:30:00Z",
	)
	initializeTestGitRepo(t, root)

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(t, root, "undone", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run undone = %v", err)
	}
	if !strings.Contains(output, "Marked not done: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected undone output", output)
	}
	if gitCommitCount(t, root) != commitCount+1 {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount+1)
	}
	if message := strings.TrimSpace(runGit(t, root, "log", "-1", "--pretty=%B")); message != "bl undone P1.M1.E1.T001: a" {
		t.Fatalf("latest commit = %q, expected %q", message, "bl undone P1.M1.E1.T001: a")
	}
}

func TestRunUndoneAutoCommitSkipsWhenStagedChangesExist(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFileWithTimes(
		t,
		root,
		"P1.M1.E1.T001",
		"a",
		"done",
		"",
		"",
		"",
		"2026-01-01T00:30:00Z",
	)
	initializeTestGitRepo(t, root)

	stagedPath := filepath.Join(root, "staged-change.txt")
	if err := os.WriteFile(stagedPath, []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	runGit(t, root, "add", "staged-change.txt")

	commitCount := gitCommitCount(t, root)
	output, err := runInDir(t, root, "undone", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run undone = %v", err)
	}
	if !strings.Contains(output, "Marked not done: P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected undone output", output)
	}
	if gitCommitCount(t, root) != commitCount {
		t.Fatalf("commit count = %d, expected %d", gitCommitCount(t, root), commitCount)
	}
	if status := runGit(t, root, "status", "--short"); !strings.Contains(status, "A  staged-change.txt") {
		t.Fatalf("status = %q, expected staged change to be preserved", status)
	}
}

func TestRunSearchAndWhyCommands(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	addDependencyForWorkflowTask(t, root, "P1.M1.E1.T002", []string{"P1.M1.E1.T001"})

	searchOutput, err := runInDir(t, root, "search", "a")
	if err != nil {
		t.Fatalf("run search = %v, expected nil", err)
	}
	if !strings.Contains(searchOutput, "P1.M1.E1.T001") {
		t.Fatalf("search output = %q, expected matching task", searchOutput)
	}

	whyOutput, err := runInDir(t, root, "why", "P1.M1.E1.T002")
	if err != nil {
		t.Fatalf("run why = %v, expected nil", err)
	}
	if !strings.Contains(whyOutput, "Explicit dependencies:") {
		t.Fatalf("why output = %q, expected dependency section", whyOutput)
	}
}

func TestRunBlockersCommandReportsRootBlocker(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	addDependencyForWorkflowTask(t, root, "P1.M1.E1.T002", []string{"P1.M1.E1.T001"})

	output, err := runInDir(t, root, "blockers")
	if err != nil {
		t.Fatalf("run blockers = %v, expected nil", err)
	}
	if !strings.Contains(output, "task(s) waiting on dependencies") {
		t.Fatalf("output = %q, expected pending blocked summary", output)
	}
	if !strings.Contains(output, "P1.M1.E1.T001") {
		t.Fatalf("output = %q, expected root blocker id", output)
	}
}

func TestRunTimelineJSONContract(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "timeline", "--json")
	if err != nil {
		t.Fatalf("run timeline --json = %v, expected nil", err)
	}

	var payload map[string]interface{}
	decodeJSONPayload(t, output, &payload)
	if _, ok := payload["groups"]; !ok {
		t.Fatalf("timeline payload missing groups: %#v", payload)
	}
}

func TestRunTimelineTextRendersLegendAndGrouping(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "timeline")
	if err != nil {
		t.Fatalf("run timeline = %v, expected nil", err)
	}
	assertContainsAll(t, output, "Project Timeline", "Legend:", "grouped by", "│")
}

func TestRunTimelineSupportsWeeksAndWidth(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "timeline", "--weeks", "4", "--width", "18", "--show-done")
	if err != nil {
		t.Fatalf("run timeline --weeks --width = %v, expected nil", err)
	}

	assertContainsAll(t, output, "4 weeks", "width 18", "Week 0", "Week 4", "Progress:")
}

func TestRunReportProgressJSONContract(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "report", "progress", "--json")
	if err != nil {
		t.Fatalf("run report progress --json = %v, expected nil", err)
	}

	var payload map[string]interface{}
	decodeJSONPayload(t, output, &payload)
	if _, ok := payload["overall"]; !ok {
		t.Fatalf("report payload missing overall: %#v", payload)
	}
}

func TestRunReportProgressTextRendersSections(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "report", "progress")
	if err != nil {
		t.Fatalf("run report progress = %v, expected nil", err)
	}
	assertContainsAll(t, output, "Progress Report", "Overall", "Auxiliary", "Phases", "All Bugs", "All Ideas")
}

func TestRunReportProgressShowsInProgressAndBlockedBarSegments(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "in_progress", "", "")
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T002", "b", "blocked", "", "")

	output, err := runInDir(t, root, "report", "progress", "--by-phase")
	if err != nil {
		t.Fatalf("run report progress = %v, expected nil", err)
	}
	if !strings.Contains(output, "▓") {
		t.Fatalf("report output missing in-progress segment: %q", output)
	}
	if !strings.Contains(output, "▒") {
		t.Fatalf("report output missing blocked segment: %q", output)
	}
}

func TestRunListAvailableTextRendersHeaderAndLegend(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	output, err := runInDir(t, root, "list", "--available")
	if err != nil {
		t.Fatalf("run list --available = %v, expected nil", err)
	}
	assertContainsAll(
		t,
		output,
		"Available Tasks (",
		"Legend:",
		"Use `bl claim TASK_ID` to claim a specific task, or `backlog grab` to auto-claim the best available task.",
	)
}

func TestRunReportVelocityAndEstimateAccuracyJSONContracts(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	doneAt := time.Now().UTC().Format(time.RFC3339)
	writeWorkflowTaskFileWithTimes(
		t,
		root,
		"P1.M1.E1.T001",
		"a",
		"done",
		"",
		"",
		time.Now().Add(-30*time.Minute).UTC().Format(time.RFC3339),
		doneAt,
	)
	setTaskDurationMinutes(t, root, "P1.M1.E1.T001", 30)

	velocityOutput, err := runInDir(t, root, "report", "velocity", "--json")
	if err != nil {
		t.Fatalf("run report velocity --json = %v, expected nil", err)
	}
	var velocityPayload map[string]interface{}
	decodeJSONPayload(t, velocityOutput, &velocityPayload)
	if _, ok := velocityPayload["daily_data"]; !ok {
		t.Fatalf("velocity payload missing daily_data: %#v", velocityPayload)
	}

	accuracyOutput, err := runInDir(t, root, "report", "estimate-accuracy", "--json")
	if err != nil {
		t.Fatalf("run report estimate-accuracy --json = %v, expected nil", err)
	}
	var accuracyPayload map[string]interface{}
	decodeJSONPayload(t, accuracyOutput, &accuracyPayload)
	if _, ok := accuracyPayload["tasks_analyzed"]; !ok {
		t.Fatalf("estimate-accuracy payload missing tasks_analyzed: %#v", accuracyPayload)
	}
}

func TestRunVelocityCommandMatchesReportVelocityJSON(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	doneAt := time.Now().UTC().Format(time.RFC3339)
	writeWorkflowTaskFileWithTimes(
		t,
		root,
		"P1.M1.E1.T001",
		"a",
		"done",
		"",
		"",
		time.Now().Add(-30*time.Minute).UTC().Format(time.RFC3339),
		doneAt,
	)
	setTaskDurationMinutes(t, root, "P1.M1.E1.T001", 30)

	reportVelocityOutput, err := runInDir(t, root, "report", "velocity", "--json")
	if err != nil {
		t.Fatalf("run report velocity --json = %v, expected nil", err)
	}
	velocityOutput, err := runInDir(t, root, "velocity", "--json")
	if err != nil {
		t.Fatalf("run velocity --json = %v, expected nil", err)
	}

	var reportVelocityPayload map[string]interface{}
	decodeJSONPayload(t, reportVelocityOutput, &reportVelocityPayload)
	var velocityPayload map[string]interface{}
	decodeJSONPayload(t, velocityOutput, &velocityPayload)
	if !reflect.DeepEqual(reportVelocityPayload, velocityPayload) {
		t.Fatalf("velocity output mismatch: report=%v velocity=%v", reportVelocityPayload, velocityPayload)
	}
}

func TestRunDataSummaryAndSchemaJSONContracts(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	summaryOutput, err := runInDir(t, root, "data", "summary", "--format", "json")
	if err != nil {
		t.Fatalf("run data summary --format json = %v, expected nil", err)
	}
	var summaryPayload map[string]interface{}
	decodeJSONPayload(t, summaryOutput, &summaryPayload)
	if _, ok := summaryPayload["overall"]; !ok {
		t.Fatalf("data summary payload missing overall: %#v", summaryPayload)
	}

	schemaOutput, err := runInDir(t, root, "schema", "--json")
	if err != nil {
		t.Fatalf("run schema --json = %v, expected nil", err)
	}
	var schemaPayload map[string]interface{}
	decodeJSONPayload(t, schemaOutput, &schemaPayload)
	if _, ok := schemaPayload["schema_version"]; !ok {
		t.Fatalf("schema payload missing schema_version: %#v", schemaPayload)
	}
}

func TestRunSessionLifecycle(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)

	startOutput, err := runInDir(t, root, "session", "start", "--agent", "agent-a", "--task", "P1.M1.E1.T001")
	if err != nil {
		t.Fatalf("run session start = %v, expected nil", err)
	}
	if !strings.Contains(startOutput, "Session started") {
		t.Fatalf("start output = %q, expected session started message", startOutput)
	}

	heartbeatOutput, err := runInDir(t, root, "session", "heartbeat", "--agent", "agent-a", "--progress", "halfway")
	if err != nil {
		t.Fatalf("run session heartbeat = %v, expected nil", err)
	}
	if !strings.Contains(heartbeatOutput, "Heartbeat updated") {
		t.Fatalf("heartbeat output = %q, expected heartbeat update message", heartbeatOutput)
	}

	listOutput, err := runInDir(t, root, "session", "list")
	if err != nil {
		t.Fatalf("run session list = %v, expected nil", err)
	}
	if !strings.Contains(listOutput, "agent-a") {
		t.Fatalf("list output = %q, expected listed session", listOutput)
	}

	endOutput, err := runInDir(t, root, "session", "end", "--agent", "agent-a")
	if err != nil {
		t.Fatalf("run session end = %v, expected nil", err)
	}
	if !strings.Contains(endOutput, "Session ended") {
		t.Fatalf("end output = %q, expected ended message", endOutput)
	}
}

func TestRunCheckJSONContractAndStrictFailure(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	writeYAMLMap(t, filepath.Join(root, ".tasks", ".context.yaml"), map[string]interface{}{
		"current_task": "P9.M9.E9.T999",
		"mode":         "single",
	})

	output, err := runInDir(t, root, "check", "--json")
	if err != nil {
		t.Fatalf("run check --json = %v, expected nil", err)
	}
	var payload map[string]interface{}
	decodeJSONPayload(t, output, &payload)
	if _, ok := payload["summary"]; !ok {
		t.Fatalf("check payload missing summary: %#v", payload)
	}

	_, err = runInDir(t, root, "check", "--strict")
	if err == nil {
		t.Fatalf("run check --strict expected non-nil error for warnings")
	}
}

func TestRunSkipAndHandoffAndUnclaimStale(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	_, err := runInDir(t, root, "claim", "P1.M1.E1.T001", "--agent", "agent-a")
	if err != nil {
		t.Fatalf("claim fixture task = %v", err)
	}

	skipOutput, err := runInDir(t, root, "skip", "P1.M1.E1.T001", "--no-grab")
	if err != nil {
		t.Fatalf("run skip = %v, expected nil", err)
	}
	if !strings.Contains(skipOutput, "Skipped: P1.M1.E1.T001") {
		t.Fatalf("skip output = %q, expected skipped message", skipOutput)
	}

	_, err = runInDir(t, root, "claim", "P1.M1.E1.T001", "--agent", "agent-a")
	if err != nil {
		t.Fatalf("re-claim fixture task = %v", err)
	}
	handoffOutput, err := runInDir(t, root, "handoff", "P1.M1.E1.T001", "--to", "agent-b", "--notes", "test handoff")
	if err != nil {
		t.Fatalf("run handoff = %v, expected nil", err)
	}
	if !strings.Contains(handoffOutput, "Handed off: P1.M1.E1.T001") {
		t.Fatalf("handoff output = %q, expected handoff message", handoffOutput)
	}

	staleAt := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	writeWorkflowTaskFileWithTimes(
		t,
		root,
		"P1.M1.E1.T001",
		"a",
		"in_progress",
		"agent-b",
		staleAt,
		staleAt,
		"",
	)
	dryRunOutput, err := runInDir(t, root, "unclaim-stale", "--threshold", "60", "--dry-run")
	if err != nil {
		t.Fatalf("run unclaim-stale --dry-run = %v, expected nil", err)
	}
	if !strings.Contains(dryRunOutput, "Would unclaim 1 stale task") {
		t.Fatalf("dry-run output = %q, expected stale preview", dryRunOutput)
	}

	applyOutput, err := runInDir(t, root, "unclaim-stale", "--threshold", "60")
	if err != nil {
		t.Fatalf("run unclaim-stale = %v, expected nil", err)
	}
	if !strings.Contains(applyOutput, "Unclaimed 1 stale task") {
		t.Fatalf("apply output = %q, expected stale unclaim message", applyOutput)
	}
}

func asInt(value any) int {
	switch n := value.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func writeWorkflowTaskFile(t *testing.T, root, taskID, title, status, claimedBy, claimedAt string) {
	t.Helper()
	writeWorkflowTaskFileWithTimes(
		t,
		root,
		taskID,
		title,
		status,
		claimedBy,
		claimedAt,
		"",
		"",
	)
}

func writeWorkflowTaskFileWithTimes(
	t *testing.T,
	root string,
	taskID string,
	title string,
	status string,
	claimedBy string,
	claimedAt string,
	startedAt string,
	completedAt string,
) {
	t.Helper()
	payload := map[string]interface{}{
		"id":             taskID,
		"title":          title,
		"status":         status,
		"estimate_hours": 1,
		"complexity":     "medium",
		"priority":       "medium",
		"depends_on":     []string{},
		"tags":           []string{},
	}
	if claimedBy != "" {
		payload["claimed_by"] = claimedBy
	}
	if claimedAt != "" {
		payload["claimed_at"] = claimedAt
	}
	if startedAt != "" {
		payload["started_at"] = startedAt
	}
	if completedAt != "" {
		payload["completed_at"] = completedAt
	}

	taskPath := filepath.Join(root, ".tasks", workflowTaskFilePath(taskID))
	if err := writeTodoTaskFromMap(taskPath, payload); err != nil {
		t.Fatalf("write task file: %v", err)
	}
}

func writeTodoTaskFromMap(taskPath string, frontmatter map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		return err
	}
	payload, err := yaml.Marshal(frontmatter)
	if err != nil {
		return err
	}
	content := fmt.Sprintf("---\n%s---\n", string(payload))
	return os.WriteFile(taskPath, []byte(content), 0o644)
}

func setAuxTaskStatus(t *testing.T, root string, section string, itemID string, status string) {
	t.Helper()
	indexPath := filepath.Join(root, ".tasks", section, "index.yaml")
	index := readYAMLMap(t, indexPath)
	entries, ok := index[section].([]interface{})
	if !ok {
		t.Fatalf("expected %s index to contain %s entries", section, section)
	}
	found := false
	for _, raw := range entries {
		entry := raw.(map[string]interface{})
		if asString(entry["id"]) == itemID {
			entry["status"] = status
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%s not found in %s index", itemID, section)
	}
	writeYAMLMap(t, indexPath, index)
}

func setAuxTaskDependsOn(t *testing.T, root string, section string, itemID string, dependsOn []string) {
	t.Helper()
	indexPath := filepath.Join(root, ".tasks", section, "index.yaml")
	index := readYAMLMap(t, indexPath)
	entries, ok := index[section].([]interface{})
	if !ok {
		t.Fatalf("expected %s index to contain %s entries", section, section)
	}

	found := false
	for _, raw := range entries {
		entry := raw.(map[string]interface{})
		if asString(entry["id"]) == itemID {
			entry["depends_on"] = dependsOn
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%s not found in %s index", itemID, section)
	}
	writeYAMLMap(t, indexPath, index)
}

func workflowTaskFilePath(taskID string) string {
	base := filepath.Base(taskID)
	if strings.HasSuffix(base, "001") {
		return filepath.Join("01-phase", "01-ms", "01-epic", "T001-a.todo")
	}
	if strings.HasSuffix(base, "002") {
		return filepath.Join("01-phase", "01-ms", "01-epic", "T002-b.todo")
	}
	return filepath.Join("01-phase", "01-ms", "01-epic", "T001-a.todo")
}

func setTaskTodoFilePath(t *testing.T, root, fullID, todoFile string) {
	t.Helper()
	indexPath := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml")
	index := readYAMLMap(t, indexPath)
	entries, ok := index["tasks"].([]interface{})
	if !ok {
		t.Fatalf("expected epic tasks entry to be a list")
	}
	for _, raw := range entries {
		entry := raw.(map[string]interface{})
		shortID := asString(entry["id"])
		if strings.HasSuffix(fullID, shortID) {
			entry["file"] = todoFile
			break
		}
	}
	writeYAMLMap(t, indexPath, index)
}

func addDependencyForWorkflowTask(t *testing.T, root, fullID string, dependsOn []string) {
	t.Helper()

	indexPath := filepath.Join(root, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml")
	index := readYAMLMap(t, indexPath)
	entries, ok := index["tasks"].([]interface{})
	if !ok {
		t.Fatalf("expected epic tasks entry to be a list")
	}
	for _, raw := range entries {
		entry := raw.(map[string]interface{})
		shortID := asString(entry["id"])
		if strings.HasSuffix(fullID, shortID) {
			entry["depends_on"] = dependsOn
			break
		}
	}
	writeYAMLMap(t, indexPath, index)

	taskPath := filepath.Join(root, ".tasks", workflowTaskFilePath(fullID))
	frontmatter, body := readTodoTask(t, taskPath)
	frontmatter["depends_on"] = dependsOn
	writeTodoTask(t, taskPath, frontmatter, body)
}

func setTaskDurationMinutes(t *testing.T, root, fullID string, minutes float64) {
	t.Helper()

	taskPath := filepath.Join(root, ".tasks", workflowTaskFilePath(fullID))
	frontmatter, body := readTodoTask(t, taskPath)
	frontmatter["duration_minutes"] = minutes
	writeTodoTask(t, taskPath, frontmatter, body)
}

func readTodoTask(t *testing.T, path string) (map[string]interface{}, string) {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read todo file %s: %v", path, err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	parts := strings.SplitN(text, "---\n", 3)
	if len(parts) < 3 {
		t.Fatalf("todo file missing frontmatter: %s", path)
	}

	frontmatter := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(parts[1]), &frontmatter); err != nil {
		t.Fatalf("parse frontmatter %s: %v", path, err)
	}
	body := parts[2]
	return frontmatter, body
}

func writeTodoTask(t *testing.T, path string, frontmatter map[string]interface{}, body string) {
	t.Helper()

	payload, err := yaml.Marshal(frontmatter)
	if err != nil {
		t.Fatalf("marshal frontmatter %s: %v", path, err)
	}
	content := fmt.Sprintf("---\n%s---\n%s", string(payload), body)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write todo file %s: %v", path, err)
	}
}

func setupWorkflowFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	dataDir := filepath.Join(root, ".tasks")
	writeYAMLMap(t, filepath.Join(dataDir, "index.yaml"), map[string]interface{}{
		"project": "Go Command Workflow Fixtures",
		"phases": []map[string]interface{}{
			{
				"id":   "P1",
				"name": "Phase",
				"path": "01-phase",
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "01-phase", "index.yaml"), map[string]interface{}{
		"milestones": []map[string]interface{}{
			{
				"id":   "M1",
				"name": "Milestone",
				"path": "01-ms",
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "01-phase", "01-ms", "index.yaml"), map[string]interface{}{
		"epics": []map[string]interface{}{
			{
				"id":     "E1",
				"name":   "Epic",
				"path":   "01-epic",
				"status": "pending",
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "01-phase", "01-ms", "01-epic", "index.yaml"), map[string]interface{}{
		"id":     "P1.M1.E1",
		"name":   "Epic",
		"status": "pending",
		"tasks": []map[string]interface{}{
			{
				"id":             "T001",
				"title":          "a",
				"file":           "T001-a.todo",
				"status":         "pending",
				"estimate_hours": 1,
				"complexity":     "medium",
				"priority":       "medium",
				"depends_on":     []string{},
				"tags":           []string{},
			},
			{
				"id":             "T002",
				"title":          "b",
				"file":           "T002-b.todo",
				"status":         "pending",
				"estimate_hours": 1,
				"complexity":     "medium",
				"priority":       "medium",
				"depends_on":     []string{},
				"tags":           []string{},
			},
		},
	})

	writeWorkflowTaskFile(t, root, "P1.M1.E1.T001", "a", "pending", "", "")
	writeWorkflowTaskFile(t, root, "P1.M1.E1.T002", "b", "pending", "", "")
	return root
}

func setupHierarchyCycleFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	dataDir := filepath.Join(root, ".tasks")
	writeYAMLMap(t, filepath.Join(dataDir, "index.yaml"), map[string]interface{}{
		"project": "Hierarchy Cycle Fixtures",
		"phases": []map[string]interface{}{
			{
				"id":         "P1",
				"name":       "Phase One",
				"path":       "01-phase-one",
				"depends_on": []string{"P2"},
			},
			{
				"id":         "P2",
				"name":       "Phase Two",
				"path":       "02-phase-two",
				"depends_on": []string{"P1"},
			},
		},
	})

	for _, phase := range []struct {
		id   string
		name string
		path string
	}{
		{id: "P1", name: "Phase One", path: "01-phase-one"},
		{id: "P2", name: "Phase Two", path: "02-phase-two"},
	} {
		writeYAMLMap(t, filepath.Join(dataDir, phase.path, "index.yaml"), map[string]interface{}{
			"milestones": []map[string]interface{}{
				{
					"id":   "M1",
					"name": phase.name + " Milestone",
					"path": "01-ms",
				},
			},
		})
		writeYAMLMap(t, filepath.Join(dataDir, phase.path, "01-ms", "index.yaml"), map[string]interface{}{
			"epics": []map[string]interface{}{
				{
					"id":     "E1",
					"name":   phase.name + " Epic",
					"path":   "01-epic",
					"status": "pending",
				},
			},
		})
		writeYAMLMap(t, filepath.Join(dataDir, phase.path, "01-ms", "01-epic", "index.yaml"), map[string]interface{}{
			"id":     phase.id + ".M1.E1",
			"name":   phase.name + " Epic",
			"status": "pending",
			"tasks": []map[string]interface{}{
				{
					"id":             "T001",
					"title":          phase.name + " Task",
					"file":           "T001-a.todo",
					"status":         "pending",
					"estimate_hours": 1,
					"complexity":     "medium",
					"priority":       "medium",
					"depends_on":     []string{},
					"tags":           []string{},
				},
			},
		})

		taskPath := filepath.Join(dataDir, phase.path, "01-ms", "01-epic", "T001-a.todo")
		if err := writeTodoTaskFromMap(taskPath, map[string]interface{}{
			"id":             phase.id + ".M1.E1.T001",
			"title":          phase.name + " Task",
			"status":         "pending",
			"estimate_hours": 1,
			"complexity":     "medium",
			"priority":       "medium",
			"depends_on":     []string{},
			"tags":           []string{},
		}); err != nil {
			t.Fatalf("write hierarchy cycle task file: %v", err)
		}
	}

	return root
}

func setupListAuxAndScopeFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	dataDir := filepath.Join(root, ".tasks")
	writeYAMLMap(t, filepath.Join(dataDir, "index.yaml"), map[string]interface{}{
		"project": "List Scoping Fixtures",
		"phases": []map[string]interface{}{
			{
				"id":   "P1",
				"name": "Primary Phase",
				"path": "01-phase",
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "01-phase", "index.yaml"), map[string]interface{}{
		"milestones": []map[string]interface{}{
			{
				"id":   "M1",
				"name": "Milestone One",
				"path": "01-ms1",
			},
			{
				"id":   "M2",
				"name": "Milestone Two",
				"path": "02-ms2",
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "01-phase", "01-ms1", "index.yaml"), map[string]interface{}{
		"epics": []map[string]interface{}{
			{
				"id":     "E1",
				"name":   "Epic One",
				"path":   "01-epic",
				"status": "pending",
				"tasks": []map[string]interface{}{
					{
						"id":             "T001",
						"title":          "Epic One Task",
						"file":           "epic-one.task",
						"status":         "pending",
						"estimate_hours": 1,
						"complexity":     "medium",
						"priority":       "medium",
						"depends_on":     []string{},
						"tags":           []string{},
					},
					{
						"id":             "T002",
						"title":          "Epic One Extra Task",
						"file":           "epic-one.extra",
						"status":         "done",
						"estimate_hours": 1,
						"complexity":     "medium",
						"priority":       "medium",
						"depends_on":     []string{},
						"tags":           []string{},
					},
				},
			},
			{
				"id":     "E2",
				"name":   "Epic Two",
				"path":   "02-epic",
				"status": "pending",
				"tasks": []map[string]interface{}{
					{
						"id":             "T001",
						"title":          "Epic Two Task",
						"file":           "epic-two.task",
						"status":         "pending",
						"estimate_hours": 1,
						"complexity":     "medium",
						"priority":       "medium",
						"depends_on":     []string{},
						"tags":           []string{},
					},
				},
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "01-phase", "02-ms2", "index.yaml"), map[string]interface{}{
		"epics": []map[string]interface{}{
			{
				"id":     "E1",
				"name":   "Milestone Two Epic",
				"path":   "01-epic",
				"status": "pending",
				"tasks": []map[string]interface{}{
					{
						"id":             "T001",
						"title":          "Milestone Two Task",
						"file":           "milestone-two.task",
						"status":         "pending",
						"estimate_hours": 1,
						"complexity":     "medium",
						"priority":       "medium",
						"depends_on":     []string{},
						"tags":           []string{},
					},
				},
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "01-phase", "01-ms1", "01-epic", "index.yaml"), map[string]interface{}{
		"id":   "P1.M1.E1",
		"name": "Epic One",
		"tasks": []map[string]interface{}{
			{
				"id":             "T001",
				"title":          "Epic One Task",
				"file":           "epic-one.task",
				"status":         "pending",
				"estimate_hours": 1,
				"complexity":     "medium",
				"priority":       "medium",
				"depends_on":     []string{},
				"tags":           []string{},
			},
			{
				"id":             "T002",
				"title":          "Epic One Extra Task",
				"file":           "epic-one.extra",
				"status":         "done",
				"estimate_hours": 1,
				"complexity":     "medium",
				"priority":       "medium",
				"depends_on":     []string{},
				"tags":           []string{},
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "01-phase", "01-ms1", "02-epic", "index.yaml"), map[string]interface{}{
		"id":   "P1.M1.E2",
		"name": "Epic Two",
		"tasks": []map[string]interface{}{
			{
				"id":             "T001",
				"title":          "Epic Two Task",
				"file":           "epic-two.task",
				"status":         "pending",
				"estimate_hours": 1,
				"complexity":     "medium",
				"priority":       "medium",
				"depends_on":     []string{},
				"tags":           []string{},
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "01-phase", "02-ms2", "01-epic", "index.yaml"), map[string]interface{}{
		"id":   "P1.M2.E1",
		"name": "Milestone Two Epic",
		"tasks": []map[string]interface{}{
			{
				"id":             "T001",
				"title":          "Milestone Two Task",
				"file":           "milestone-two.task",
				"status":         "pending",
				"estimate_hours": 1,
				"complexity":     "medium",
				"priority":       "medium",
				"depends_on":     []string{},
				"tags":           []string{},
			},
		},
	})

	writeYAMLMap(t, filepath.Join(dataDir, "bugs", "index.yaml"), map[string]interface{}{
		"bugs": []map[string]interface{}{
			{
				"id":             "B1",
				"title":          "Root bug",
				"file":           "bug-task.todo",
				"status":         "pending",
				"estimate_hours": 1,
				"complexity":     "medium",
				"priority":       "high",
				"depends_on":     []string{},
				"tags":           []string{},
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "ideas", "index.yaml"), map[string]interface{}{
		"ideas": []map[string]interface{}{
			{
				"id":             "I1",
				"title":          "Root idea",
				"file":           "idea-task.todo",
				"status":         "pending",
				"estimate_hours": 1,
				"complexity":     "medium",
				"priority":       "high",
				"depends_on":     []string{},
				"tags":           []string{},
			},
		},
	})

	writeTaskFile(t, root, filepath.Join(".tasks", "01-phase", "01-ms1", "01-epic", "epic-one.task"), "P1.M1.E1.T001", "Epic One Task")
	writeTaskFileWithStatus(t, root, filepath.Join(".tasks", "01-phase", "01-ms1", "01-epic", "epic-one.extra"), "P1.M1.E1.T002", "Epic One Extra Task", "done")
	writeTaskFile(t, root, filepath.Join(".tasks", "01-phase", "01-ms1", "02-epic", "epic-two.task"), "P1.M1.E2.T001", "Epic Two Task")
	writeTaskFile(t, root, filepath.Join(".tasks", "01-phase", "02-ms2", "01-epic", "milestone-two.task"), "P1.M2.E1.T001", "Milestone Two Task")
	writeTaskFile(t, root, filepath.Join(".tasks", "bugs", "bug-task.todo"), "B1", "Root bug")
	writeTaskFile(t, root, filepath.Join(".tasks", "ideas", "idea-task.todo"), "I1", "Root idea")

	return root
}

func writeTaskFile(t *testing.T, root, relativePath, taskID, title string) {
	t.Helper()
	writeTaskFileWithStatus(t, root, relativePath, taskID, title, "pending")
}

func writeTaskFileWithStatus(t *testing.T, root, relativePath, taskID, title, status string) {
	t.Helper()

	fullPath := filepath.Join(root, relativePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("create task dir: %v", err)
	}
	content := []string{
		"---",
		fmt.Sprintf("id: %s", taskID),
		fmt.Sprintf("title: %s", title),
		fmt.Sprintf("status: %s", status),
		"estimate_hours: 1",
		"complexity: medium",
		"priority: medium",
		"depends_on: []",
		"tags: []",
		"---",
	}
	if err := os.WriteFile(fullPath, []byte(strings.Join(content, "\n")), 0o644); err != nil {
		t.Fatalf("write task file %s: %v", fullPath, err)
	}
}

func initializeTestGitRepo(t *testing.T, root string) {
	t.Helper()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.name", "backlog-ci")
	runGit(t, root, "config", "user.email", "ci@backlog.local")
	runGit(t, root, "add", ".tasks")
	runGit(t, root, "commit", "-m", "baseline")
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func gitCommitCount(t *testing.T, root string) int {
	t.Helper()
	countText := runGit(t, root, "rev-list", "--count", "HEAD")
	count, err := strconv.Atoi(countText)
	if err != nil {
		t.Fatalf("parse commit count: %q", countText)
	}
	return count
}

func setupAddFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	dataDir := filepath.Join(root, ".tasks")
	writeYAMLMap(t, filepath.Join(dataDir, "index.yaml"), map[string]interface{}{
		"project": "Go Command Fixtures",
		"phases": []map[string]interface{}{
			{
				"id":   "P1",
				"name": "Phase",
				"path": "01-phase",
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "01-phase", "index.yaml"), map[string]interface{}{
		"milestones": []map[string]interface{}{
			{
				"id":   "M1",
				"name": "Milestone",
				"path": "01-ms",
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "01-phase", "01-ms", "index.yaml"), map[string]interface{}{
		"epics": []map[string]interface{}{
			{
				"id":     "E1",
				"name":   "Epic",
				"path":   "01-epic",
				"status": "pending",
			},
		},
	})
	writeYAMLMap(t, filepath.Join(dataDir, "01-phase", "01-ms", "01-epic", "index.yaml"), map[string]interface{}{
		"id":     "P1.M1.E1",
		"name":   "Epic",
		"status": "pending",
		"tasks":  []map[string]interface{}{},
	})
	return root
}

func runInDir(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	return runInDirWithEnv(t, dir, nil, args...)
}

func runInDirWithEnv(t *testing.T, dir string, env map[string]string, args ...string) (string, error) {
	t.Helper()
	runInDirMu.Lock()
	defer runInDirMu.Unlock()

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir to fixture: %v", err)
	}

	oldEnv := []struct {
		name  string
		value string
		ok    bool
	}{}
	for name, value := range env {
		oldValue, ok := os.LookupEnv(name)
		oldEnv = append(oldEnv, struct {
			name  string
			value string
			ok    bool
		}{name: name, value: oldValue, ok: ok})
		if err := os.Setenv(name, value); err != nil {
			t.Fatalf("failed to set env %s=%s: %v", name, value, err)
		}
	}
	defer func() {
		for _, item := range oldEnv {
			if item.ok {
				if err := os.Setenv(item.name, item.value); err != nil {
					t.Fatalf("failed to restore env %s=%s: %v", item.name, item.value, err)
				}
			} else {
				if err := os.Unsetenv(item.name); err != nil {
					t.Fatalf("failed to clear env %s: %v", item.name, err)
				}
			}
		}
	}()

	outPipeR, outPipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	errPipeR, errPipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stderr pipe: %v", err)
	}

	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout = outPipeW
	os.Stderr = errPipeW

	var outBuf, errBuf bytes.Buffer
	outDone := make(chan struct{})
	errDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&outBuf, outPipeR)
		close(outDone)
	}()
	go func() {
		_, _ = io.Copy(&errBuf, errPipeR)
		close(errDone)
	}()

	runErr := Run(args...)

	outPipeW.Close()
	errPipeW.Close()
	<-outDone
	<-errDone

	os.Stdout = oldStdout
	os.Stderr = oldStderr
	if cherr := os.Chdir(previous); cherr != nil {
		t.Fatalf("failed to restore cwd: %v", cherr)
	}

	output := outBuf.String() + errBuf.String()
	if runErr != nil && !strings.Contains(output, runErr.Error()) && strings.TrimSpace(output) == "" {
		output += runErr.Error()
		output += "\n"
	}
	return output, runErr
}

func readYAMLMap(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	out := map[string]interface{}{}
	if err := yaml.Unmarshal(content, &out); err != nil {
		t.Fatalf("failed to parse %s: %v", path, err)
	}
	return out
}

func writeYAMLMap(t *testing.T, path string, data map[string]interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("failed to create fixture dir: %v", err)
	}
	payload, err := yaml.Marshal(data)
	if err != nil {
		t.Fatalf("failed to marshal yaml: %v", err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	return string(raw)
}
