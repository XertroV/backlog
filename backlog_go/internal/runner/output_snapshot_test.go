package runner

import (
	"regexp"
	"strings"
	"testing"
)

var snapshotANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func normalizeSnapshotOutput(raw string) string {
	withoutANSI := snapshotANSI.ReplaceAllString(raw, "")
	return strings.ReplaceAll(strings.TrimSpace(withoutANSI), "\r\n", "\n")
}

func assertSnapshotAnchorsInOrder(t *testing.T, output string, anchors []string) {
	t.Helper()
	searchStart := 0
	for _, anchor := range anchors {
		index := strings.Index(output[searchStart:], anchor)
		if index < 0 {
			t.Fatalf("snapshot anchor %q missing from output:\n%s", anchor, output)
		}
		searchStart += index + len(anchor)
	}
}

func TestSemanticSnapshotsForCriticalTextCommands(t *testing.T) {
	t.Parallel()

	root := setupWorkflowFixture(t)
	cases := []struct {
		name    string
		args    []string
		anchors []string
	}{
		{
			name: "show-without-context-guidance",
			args: []string{"show"},
			anchors: []string{
				"No task specified and no current working task set.",
				"Use 'work <task-id>' to set a working task.",
			},
		},
		{
			name: "list-available-header-legend-hint",
			args: []string{"list", "--available"},
			anchors: []string{
				"Available Tasks (",
				"Legend: ★ critical path, status icon indicates current state",
				"Use `bl claim TASK_ID` to claim a specific task, or `backlog grab` to auto-claim the best available task.",
			},
		},
		{
			name: "report-progress-sections",
			args: []string{"report", "progress", "--by-epic"},
			anchors: []string{
				"Progress Report",
				"Overall",
				"Auxiliary",
				"Phases",
				"Legend: ✓ complete | → in progress | ▒ blocked | · pending",
				"Active milestones",
				"Active epics",
			},
		},
		{
			name: "timeline-header-legend-grouping",
			args: []string{"timeline"},
			anchors: []string{
				"Project Timeline",
				"grouped by",
				"Legend:",
			},
		},
		{
			name: "claim-next-step-guidance",
			args: []string{"claim", "P1.M1.E1.T001", "--agent", "agent-snap", "--no-content"},
			anchors: []string{
				"✓ Claimed",
				"Task body preview suppressed via --no-content",
				"/01-phase/01-ms/01-epic/T001-a.todo",
				"-----== EOF ==-----",
				"When you complete this task, mark it done by either:",
				"bl cycle P1.M1.E1.T001",
				"bl done P1.M1.E1.T001",
			},
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			output, err := runInDir(t, root, testCase.args...)
			if err != nil {
				t.Fatalf("run %q = %v, expected nil", strings.Join(testCase.args, " "), err)
			}
			assertSnapshotAnchorsInOrder(t, normalizeSnapshotOutput(output), testCase.anchors)
		})
	}
}
