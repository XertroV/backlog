package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/XertroV/tasks/backlog_go/cmd"
	"github.com/XertroV/tasks/backlog_go/internal/commands"
	"github.com/XertroV/tasks/backlog_go/internal/config"
	taskcontext "github.com/XertroV/tasks/backlog_go/internal/context"
	"github.com/XertroV/tasks/backlog_go/internal/critical_path"
	"github.com/XertroV/tasks/backlog_go/internal/loader"
	"github.com/XertroV/tasks/backlog_go/internal/models"
	"github.com/XertroV/tasks/backlog_go/internal/skills"
	"gopkg.in/yaml.v3"
)

const (
	previewDisplayLimit      = 5
	previewAuxLimit          = 5
	previewBugFanoutCount    = 2
	grabSiblingAdditionalMax = 4
	grabBugAdditionalMax     = 2
	taskFileReadPreviewLines = 12
	taskFileReadEOFMarker    = "-----== EOF ==-----"
)

const migrationComment = "<!-- CLI migrated: 'tasks' -> 'backlog' (alias 'bl' also works). -->\n"

var agentsSnippets = map[string]string{
	"short":  "# AGENTS.md (Short)\n\n# Work Loop & Task Backlog\n\n## Task Workflow\n- Use `backlog grab` to claim work, then `backlog done` or `backlog cycle`.\n- If a command fails to parse args/usage, run exactly one recovery command: `backlog cycle`.\n- For explicit task IDs, use `backlog claim <TASK_ID> [TASK_ID ...]`.\n- Prefer critical-path work, then `critical > high > medium > low` priority.\n- If blocked, run `backlog blocked --reason \"<why>\"` and handoff quickly.\n- Keep each change scoped to one task; update status as soon as state changes.\n- Before done: run targeted tests for changed code.\n- For more see `backlog --help`.\n",
	"medium": "# AGENTS.md (Medium)\n\n# Work Loop & Task Backlog\n\n## Defaults\n- Claim with `backlog grab` (or `backlog grab --single` for focused work).\n- Use `backlog claim <TASK_ID> [TASK_ID ...]` when task IDs are provided.\n- If command argument parsing fails, run `backlog cycle` once to recover.\n- CLI selection order is: critical-path first, then task priority.\n- Use `backlog work <id>` when switching context; use `backlog show` to review details.\n",
	"long":   "# AGENTS.md (Long)\n\n# Work Loop & Task Backlog\n\n## Operating Model\n- Default command: `backlog`. Use local `.backlog/` state as source of truth.\n- Selection strategy: critical-path first, then `critical > high > medium > low`.\n- Treat task files as contracts: requirements + acceptance criteria drive scope.\n",
}

var migrationKnownCommands = []string{
	"list",
	"ls",
	"show",
	"cat",
	"next",
	"claim",
	"grab",
	"done",
	"cycle",
	"work",
	"update",
	"sync",
	"check",
	"unclaim-stale",
	"add",
	"add-epic",
	"add-milestone",
	"add-phase",
	"move",
	"idea",
	"bug",
	"fixed",
	"blocked",
	"skip",
	"unclaim",
	"handoff",
	"why",
	"dash",
	"search",
	"blockers",
	"timeline",
	"tl",
	"session",
	"report",
	"data",
	"schema",
	"edit",
	"howto",
	"skills",
	"migrate",
}

type commandUsageSpec struct {
	summary  string
	usage    string
	options  []string
	examples []string
}

var currentCommandForUsage string

func printUsageForCommand(command string) {
	command = normalizeCommand(command)
	currentCommandForUsage = command
	switch command {
	case commands.CmdAdd:
		printAddHelp()
	case commands.CmdAddEpic:
		printAddEpicHelp()
	case commands.CmdAddMilestone:
		printAddMilestoneHelp()
	case commands.CmdAddPhase:
		printAddPhaseHelp()
	case commands.CmdClaim:
		printClaimHelp()
	case commands.CmdDone:
		printDoneHelp()
	case commands.CmdUpdate:
		printUpdateHelp()
	case commands.CmdMove:
		printMoveHelp()
	case commands.CmdSet:
		printSetHelp()
	case commands.CmdList, commands.CmdLs:
		printListHelp()
	default:
		if command == commands.CmdReportAlias {
			command = commands.CmdReport
		}
		if command == commands.CmdTimelineAlias {
			command = commands.CmdTimeline
		}
		if spec, ok := commandUsageFallbacks[command]; ok {
			printCommandHelp(command, spec.summary, spec.usage, spec.options, spec.examples)
			return
		}
		fmt.Printf("%s backlog %s\n", styleSubHeader("Usage:"), command)
	}
}

func printUsageError(command string, err error) error {
	printUsageForCommand(command)
	if err != nil {
		fmt.Printf("%s\n", styleError(err.Error()))
	}
	printCommandRecoveryHint()
	return err
}

func validateAllowedFlagsForUsage(command string, args []string, allowed map[string]bool) error {
	if err := validateAllowedFlags(args, allowed); err != nil {
		return printUsageError(command, err)
	}
	return nil
}

var commandUsageFallbacks = map[string]commandUsageSpec{
	"search": {
		summary: "Find tasks and milestones by regex pattern.",
		usage:   "backlog search <PATTERN> [options]",
		options: []string{
			"--status             Filter by comma-separated status values",
			"--tags               Filter by comma-separated tags",
			"--complexity         Filter by complexity (low|medium|high|critical)",
			"--priority           Filter by priority (low|medium|high|critical)",
			"--limit              Maximum results",
			"--json               Output JSON",
		},
		examples: []string{
			"backlog search a",
			"backlog search --status pending --limit 5 --json auth",
		},
	},
	"blockers": {
		summary:  "Show tasks currently blocking progress.",
		usage:    "backlog blockers [--deep] [--suggest] [--json]",
		examples: []string{"backlog blockers", "backlog blockers --deep --json"},
	},
	"why": {
		summary:  "Explain blockers and dependency reasons for a task.",
		usage:    "backlog why <TASK_ID> [--json]",
		examples: []string{"backlog why P1.M1.E1.T001"},
	},
	"timeline": {
		summary: "Display timeline view with optional grouping.",
		usage:   "backlog timeline [--scope SCOPE ...] [--weeks N] [--group-by phase|milestone|epic|status] [--show-done] [--width N] [--json]",
		options: []string{
			"--scope",
			"--group-by",
			"--weeks, -w",
			"--width",
			"--show-done",
			"--json",
		},
		examples: []string{"backlog timeline", "backlog timeline --group-by milestone --json"},
	},
	"report": {
		summary: "Generate reports for progress, velocity, and accuracy.",
		usage:   "backlog report [progress|velocity|estimate-accuracy|p|v|ea] [--json] [--format {json,table}]",
		options: []string{
			"progress (alias p)",
			"velocity (alias v)",
			"estimate-accuracy (alias ea)",
			"--json",
			"--format",
		},
		examples: []string{"backlog report progress", "backlog r v --json"},
	},
	"data": {
		summary:  "Summarize or export task data.",
		usage:    "backlog data <summary|export> [--format json|yaml] [--scope SCOPE ...] [--include-content]",
		examples: []string{"backlog data summary --format json", "backlog data export --scope P1.M1 --format yaml"},
	},
	"edit": {
		summary:  "Open a task todo file in your editor.",
		usage:    "backlog edit <TASK_ID>",
		examples: []string{"backlog edit P1.M1.E1.T001"},
	},
	"schema": {
		summary:  "Print backlog file format schema.",
		usage:    "backlog schema [--json] [--compact]",
		examples: []string{"backlog schema", "backlog schema --json --compact"},
	},
	"session": {
		summary: "Manage agent working sessions.",
		usage:   "backlog session <start|heartbeat|list|end|clean> [--agent AGENT] [--timeout MINUTES]",
		options: []string{
			"start --agent AGENT [--task TASK_ID]",
			"heartbeat --agent AGENT [--progress TEXT]",
			"end --agent AGENT [--status STATUS]",
			"list [--stale] [--timeout MINUTES]",
			"clean [--timeout MINUTES]",
		},
		examples: []string{
			"backlog session start --agent agent-a --task P1.M1.E1.T001",
			"backlog session heartbeat --agent agent-a --progress in_progress",
		},
	},
	"check": {
		summary:  "Run consistency checks across backlog metadata.",
		usage:    "backlog check [--json] [--strict]",
		examples: []string{"backlog check", "backlog check --strict"},
	},
	"idea": {
		summary: "Create a new planning idea.",
		usage:   "backlog idea [--title <TITLE> | IDEA_TEXT] [options]",
		options: []string{
			"--title, -T",
			"--estimate, -e",
			"--complexity, -c",
			"--priority, -p",
			"--depends-on, -d",
			"--tags",
			"--simple, -s",
			"--body, -b",
		},
		examples: []string{"backlog idea \"Reduce setup friction in onboarding\"", "backlog idea --title \"Improve docs flow\" --simple"},
	},
	"bug": {
		summary: "Create a bug item for tracking and resolution.",
		usage:   "backlog bug [--title <TITLE> | BUG_TEXT] [options]",
		options: []string{
			"--title, -T",
			"--estimate, -e",
			"--complexity, -c",
			"--priority, -p",
			"--depends-on, -d",
			"--tags",
			"--simple, -s",
			"--body, -b",
		},
		examples: []string{"backlog bug \"Crash when ...\"", "backlog bug --title \"Invalid login\" --simple"},
	},
	"fixed": {
		summary: "Capture completed fix notes and observations.",
		usage:   "backlog fixed [--title <TITLE> | FIX_TEXT] [--description DESC] [--at ISO8601]",
		options: []string{
			"--title, -T",
			"--description, --desc",
			"--at",
			"--tags",
			"--body, -b",
			"--json",
		},
		examples: []string{"backlog fixed \"Prevented deadlock in worker loop\"", "backlog fixed --title \"Fix auth token refresh\" --at 2026-02-01T12:00:00Z"},
	},
	"skip": {
		summary:  "Mark current/context task as skipped and move on.",
		usage:    "backlog skip <TASK_ID> [--agent AGENT] [--no-grab]",
		options:  []string{"--agent", "--no-grab"},
		examples: []string{"backlog skip P1.M1.E1.T001 --agent agent-a"},
	},
	"handoff": {
		summary: "Transfer task ownership.",
		usage:   "backlog handoff <TASK_ID> --to AGENT [--notes \"...\"] [--force]",
		options: []string{
			"--to",
			"--notes",
			"--force",
		},
		examples: []string{"backlog handoff P1.M1.E1.T001 --to agent-b --notes \"Taking over\""},
	},
	"unclaim-stale": {
		summary: "Release old claims for stale tasks.",
		usage:   "backlog unclaim-stale [--threshold MINUTES] [--dry-run]",
		options: []string{
			"--threshold",
			"--dry-run",
		},
		examples: []string{"backlog unclaim-stale --threshold 120", "backlog unclaim-stale --dry-run"},
	},
	"show": {
		summary: "Show detailed information for one or more backlog IDs.",
		usage:   "backlog show [PATH_ID ...] [--long] [--all]",
		options: []string{
			"--long",
			"--all",
			"PATH_ID supports phase/milestone/epic/task IDs (for example P1, P1.M1, P1.M1.E1, P1.M1.E1.T001)",
			"When omitted, falls back to current working task",
		},
		examples: []string{
			"backlog show P1.M1.E1.T001",
			"backlog show P1.M1 P2.M1.E3",
			"backlog show",
		},
	},
	"cat": {
		summary: "Print complete raw task file contents.",
		usage:   "backlog cat <TASK_ID ...>",
		options: []string{
			"TASK_ID supports task IDs such as P1.M1.E1.T001",
			"Pass multiple task IDs to print files separated by headers",
		},
		examples: []string{
			"backlog cat P1.M1.E1.T001",
			"backlog cat P1.M1.E1.T001 P1.M1.E1.T002",
		},
	},
	"tree": {
		summary: "Display the hierarchical backlog tree.",
		usage:   "backlog tree [PATH_QUERY ...] [--json] [--unfinished] [--show-completed-aux] [--details] [--depth N]",
		options: []string{
			"--json",
			"--unfinished",
			"--show-completed-aux",
			"--details",
			"--depth",
		},
		examples: []string{
			"backlog tree",
			"backlog tree P1.M1 --details",
			"backlog tree P1.M1 P2.M2 --depth 3",
			"backlog tree --unfinished --json",
		},
	},
	"next": {
		summary: "Show the next available task on the critical path.",
		usage:   "backlog next [--json]",
		options: []string{
			"--json",
		},
		examples: []string{
			"backlog next",
			"backlog next --json",
		},
	},
	"preview": {
		summary: "Preview upcoming work and grab suggestions.",
		usage:   "backlog preview [--json]",
		options: []string{
			"--json",
		},
		examples: []string{
			"backlog preview",
			"backlog preview --json",
		},
	},
	"grab": {
		summary: "Auto-claim next available work or claim specific IDs.",
		usage:   "backlog grab [TASK_ID ...] [--agent AGENT] [--single] [--json] [--no-content]",
		options: []string{
			"--agent",
			"--single",
			"--json",
			"--no-content",
		},
		examples: []string{
			"backlog grab",
			"backlog grab --single",
			"backlog grab P1.M1.E1.T001 --agent agent-a",
		},
	},
	"cycle": {
		summary: "Complete current task and grab the next available task.",
		usage:   "backlog cycle [--agent AGENT] [--json] [--no-content]",
		options: []string{
			"--agent",
			"--json",
			"--no-content",
		},
		examples: []string{
			"backlog cycle",
			"backlog cycle --agent agent-a",
		},
	},
	"dash": {
		summary: "Show a concise project dashboard.",
		usage:   "backlog dash [--json]",
		options: []string{
			"--json",
		},
		examples: []string{
			"backlog dash",
			"backlog dash --json",
		},
	},
	"admin": {
		summary: "Run administrative checks and diagnostics.",
		usage:   "backlog admin [check-file-sync|check-ids] [--json]",
		options: []string{
			"--json",
		},
		examples: []string{
			"backlog admin check-file-sync",
			"backlog admin check-ids --json",
		},
	},
	"ci": {
		summary: "CI-focused validation helpers for automation.",
		usage:   "backlog ci validate-ids <TASK_ID ...>",
		options: []string{
			"--help",
		},
		examples: []string{
			"backlog ci validate-ids B020 E1.T02",
			"backlog ci validate-ids P1.M1.E1.T001",
		},
	},
	"agents": {
		summary: "Print AGENTS.md snippets for workflow guidance.",
		usage:   "backlog agents [short|medium|full] [--json]",
		options: []string{
			"--json",
		},
		examples: []string{
			"backlog agents",
			"backlog agents medium",
			"backlog agents full --json",
		},
	},
	"benchmark": {
		summary: "Show loader benchmark timings.",
		usage:   "backlog benchmark [--json] [--top N] [--mode metadata|full] [--parse-task-body]",
		options: []string{
			"--json",
			"--top",
			"--mode",
			"--parse-task-body",
		},
		examples: []string{
			"backlog benchmark",
			"backlog benchmark --json --top 10",
		},
	},
	"work": {
		summary: "Set, clear, or show current working task.",
		usage:   "backlog work [TASK_ID|clear] [--agent AGENT]",
		options: []string{
			"--agent",
		},
		examples: []string{
			"backlog work",
			"backlog work P1.M1.E1.T001",
			"backlog work clear",
		},
	},
	"unclaim": {
		summary: "Release a claimed task back to pending.",
		usage:   "backlog unclaim <TASK_ID> [--agent AGENT]",
		options: []string{
			"--agent",
		},
		examples: []string{
			"backlog unclaim P1.M1.E1.T001",
		},
	},
	"blocked": {
		summary: "Mark a task as blocked and optionally grab next work.",
		usage:   "backlog blocked <TASK_ID> --reason REASON [--agent AGENT] [--grab] [--json]",
		options: []string{
			"--reason",
			"--agent",
			"--grab",
			"--json",
		},
		examples: []string{
			"backlog blocked P1.M1.E1.T001 --reason \"waiting on API\"",
			"backlog blocked P1.M1.E1.T001 --reason \"blocked\" --grab",
		},
	},
	"init": {
		summary: "Initialize a backlog project in the current directory.",
		usage:   "backlog init --project NAME [--description TEXT] [--timeline-weeks N]",
		options: []string{
			"--project, -p",
			"--description, -d",
			"--timeline-weeks, -w",
		},
		examples: []string{
			"backlog init --project my-project",
			"backlog init -p my-project -d \"CLI rewrite\" -w 8",
		},
	},
	"log": {
		summary: "Show recent activity log entries.",
		usage:   "backlog log [--bugs, -b] [--ideas, -i] [--limit N] [--json]",
		options: []string{
			"--bugs, -b",
			"--ideas, -i",
			"--limit",
			"--json",
		},
		examples: []string{
			"backlog log",
			"backlog log --limit 20 --json",
		},
	},
	"lock": {
		summary: "Lock a phase, milestone, or epic.",
		usage:   "backlog lock <ITEM_ID>",
		examples: []string{
			"backlog lock P1.M1",
		},
	},
	"unlock": {
		summary: "Unlock a phase, milestone, or epic.",
		usage:   "backlog unlock <ITEM_ID>",
		examples: []string{
			"backlog unlock P1.M1",
		},
	},
	"migrate": {
		summary: "Migrate .tasks data into .backlog format.",
		usage:   "backlog migrate [--force] [--no-symlink]",
		options: []string{
			"--force",
			"--no-symlink",
		},
		examples: []string{
			"backlog migrate",
			"backlog migrate --force",
		},
	},
	"sync": {
		summary: "Recalculate derived metadata in index files.",
		usage:   "backlog sync",
		examples: []string{
			"backlog sync",
		},
	},
	"undone": {
		summary: "Mark an item as not done (pending).",
		usage:   "backlog undone <ITEM_ID>",
		examples: []string{
			"backlog undone P1.M1.E1.T001",
			"backlog undone P1.M1",
		},
	},
	"version": {
		summary: "Show CLI version information.",
		usage:   "backlog version",
		examples: []string{
			"backlog version",
		},
	},
	"velocity": {
		summary: "Generate velocity report data.",
		usage:   "backlog velocity [--days N] [--json] [--format {json,table}]",
		options: []string{
			"--days",
			"--json",
			"--format",
		},
		examples: []string{
			"backlog velocity",
			"backlog velocity --days 30 --json",
		},
	},
	"skills": {
		summary: "Install backlog skills for supported clients.",
		usage:   "backlog skills install <SKILL> [--client codex|claude] [--artifact skills|commands] [--dry-run] [--json]",
		options: []string{
			"--client",
			"--artifact",
			"--dry-run",
			"--json",
		},
		examples: []string{
			"backlog skills install plan-task --client codex",
			"backlog skills install plan-task --client codex --dry-run --json",
		},
	},
	"howto": {
		summary: "Show the backlog how-to guidance for agents.",
		usage:   "backlog howto [--json]",
		options: []string{
			"--json",
		},
		examples: []string{
			"backlog howto",
			"backlog howto --json",
		},
	},
	"help": {
		summary: "Show command overview and command-specific guidance.",
		usage:   "backlog help [COMMAND]",
		examples: []string{
			"backlog help",
			"backlog help show",
		},
	},
}

type previewTaskPayload struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Status         string   `json:"status"`
	File           string   `json:"file"`
	FileExists     bool     `json:"file_exists"`
	EstimateHours  float64  `json:"estimate_hours"`
	Complexity     string   `json:"complexity"`
	Priority       string   `json:"priority"`
	OnCritical     bool     `json:"on_critical_path"`
	GrabAdditional []string `json:"grab_additional"`
	Path           string   `json:"path,omitempty"`
}

type completionNotice struct {
	EpicCompleted      bool
	MilestoneCompleted bool
	PhaseCompleted     bool
}

type logEventPayload struct {
	TaskID    string    `json:"task_id"`
	Title     string    `json:"title"`
	Event     string    `json:"event"`
	Kind      string    `json:"kind"`
	Timestamp time.Time `json:"timestamp"`
	Actor     *string   `json:"actor"`
}

type logEvent struct {
	TaskID    string
	Title     string
	Event     string
	Timestamp time.Time
	Actor     *string
}

type treeTask struct {
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
	Path        string     `json:"path,omitempty"`
}

type treeEpicPayload struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Status string     `json:"status"`
	Tasks  []treeTask `json:"tasks"`
}

type treeMilestonePayload struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Status string            `json:"status"`
	Stats  map[string]int    `json:"stats"`
	Epics  []treeEpicPayload `json:"epics"`
}

type treePhasePayload struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Status     string                 `json:"status"`
	Stats      map[string]int         `json:"stats"`
	Milestones []treeMilestonePayload `json:"milestones"`
}

type treePayload struct {
	CriticalPath     []string           `json:"critical_path"`
	NextAvailable    string             `json:"next_available"`
	MaxDepth         int                `json:"max_depth"`
	ShowDetails      bool               `json:"show_details"`
	UnfinishedOnly   bool               `json:"unfinished_only"`
	ShowCompletedAux bool               `json:"show_completed_aux"`
	Phases           []treePhasePayload `json:"phases"`
	Bugs             []treeTask         `json:"bugs"`
	Ideas            []treeTask         `json:"ideas"`
}

type reportProgressJSON struct {
	Overall struct {
		Total           int     `json:"total"`
		Done            int     `json:"done"`
		InProgress      int     `json:"in_progress"`
		Pending         int     `json:"pending"`
		Blocked         int     `json:"blocked"`
		PercentComplete float64 `json:"percent_complete"`
		RemainingHours  float64 `json:"remaining_hours"`
	} `json:"overall"`
	Auxiliary struct {
		Bugs struct {
			Total          int     `json:"total"`
			Done           int     `json:"done"`
			InProgress     int     `json:"in_progress"`
			Pending        int     `json:"pending"`
			Blocked        int     `json:"blocked"`
			RemainingHours float64 `json:"remaining_hours"`
		} `json:"bugs"`
		Ideas struct {
			Total          int     `json:"total"`
			Done           int     `json:"done"`
			InProgress     int     `json:"in_progress"`
			Pending        int     `json:"pending"`
			Blocked        int     `json:"blocked"`
			RemainingHours float64 `json:"remaining_hours"`
		} `json:"ideas"`
	} `json:"auxiliary"`
	Bugs   []treeTask `json:"bugs"`
	Ideas  []treeTask `json:"ideas"`
	Phases []struct {
		ID          string  `json:"id"`
		Name        string  `json:"name"`
		Total       int     `json:"total"`
		Done        int     `json:"done"`
		InProgress  int     `json:"in_progress"`
		Pending     int     `json:"pending"`
		Blocked     int     `json:"blocked"`
		PercentDone float64 `json:"percent_complete"`
		Remaining   float64 `json:"remaining_hours"`
		Milestones  []struct {
			ID         string  `json:"id"`
			Name       string  `json:"name"`
			Total      int     `json:"total"`
			Done       int     `json:"done"`
			InProgress int     `json:"in_progress"`
			Pending    int     `json:"pending"`
			Blocked    int     `json:"blocked"`
			Percent    float64 `json:"percent_complete"`
			Remaining  float64 `json:"remaining_hours"`
			Epics      []struct {
				ID         string  `json:"id"`
				Name       string  `json:"name"`
				Total      int     `json:"total"`
				Done       int     `json:"done"`
				InProgress int     `json:"in_progress"`
				Pending    int     `json:"pending"`
				Blocked    int     `json:"blocked"`
				Percent    float64 `json:"percent_complete"`
				Remaining  float64 `json:"remaining_hours"`
			} `json:"epics"`
		} `json:"milestones"`
	} `json:"phases"`
}

type dashCurrentTaskPayload struct {
	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Agent       string `json:"agent"`
	File        string `json:"file,omitempty"`
	FileExists  bool   `json:"file_exists"`
	Found       bool   `json:"found"`
	WorkingTask bool   `json:"working_task"`
}

type dashPhaseProgressPayload struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Done            int     `json:"done"`
	Total           int     `json:"total"`
	InProgress      int     `json:"in_progress"`
	Blocked         int     `json:"blocked"`
	PercentComplete float64 `json:"percent_complete"`
}

type dashCriticalPathPayload struct {
	Tasks          []string `json:"tasks"`
	RemainingCount int      `json:"remaining_count"`
	RemainingHours float64  `json:"remaining_hours"`
	AllComplete    bool     `json:"all_complete"`
	NextID         string   `json:"next_id,omitempty"`
	NextTitle      string   `json:"next_title,omitempty"`
}

type dashOverallPayload struct {
	Done       int     `json:"done"`
	Total      int     `json:"total"`
	InProgress int     `json:"in_progress"`
	Blocked    int     `json:"blocked"`
	Percent    float64 `json:"percent_complete"`
}

type dashStatusPayload struct {
	InProgress    int `json:"in_progress"`
	Blocked       int `json:"blocked"`
	StaleClaims   int `json:"stale_claims"`
	ActiveSession int `json:"active_sessions"`
}

type dashJSON struct {
	Agent           string                     `json:"agent"`
	CurrentTask     *dashCurrentTaskPayload    `json:"current_task"`
	Overall         dashOverallPayload         `json:"overall"`
	Phases          []dashPhaseProgressPayload `json:"phases"`
	CompletedPhases []string                   `json:"completed_phases"`
	CriticalPath    dashCriticalPathPayload    `json:"critical_path"`
	Status          dashStatusPayload          `json:"status"`
}

type adminJSONPayload struct {
	Command     string `json:"command"`
	Implemented bool   `json:"implemented"`
	Message     string `json:"message"`
	Guidance    string `json:"guidance"`
}

// Run executes the CLI entrypoint.
// Keeping behavior intentionally explicit and predictable for this milestone.
func Run(rawArgs ...string) error {
	if len(rawArgs) == 0 {
		rawArgs = os.Args[1:]
	}
	filteredArgs := make([]string, 0, len(rawArgs))
	for _, arg := range rawArgs {
		if strings.HasPrefix(arg, "-test.") {
			continue
		}
		filteredArgs = append(filteredArgs, arg)
	}
	rawArgs = filteredArgs
	args := make([]string, len(rawArgs))
	copy(args, rawArgs)
	filtered, err := parseCommandColorFlags(args)
	if err != nil {
		return err
	}
	args = filtered

	root := cmd.NewRootCommand()
	if len(args) == 0 {
		printStartupLogo(3, shouldUseColor())
		fmt.Println()
		fmt.Println(styleHeader(root.Usage()))
		return nil
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == commands.CmdHelp) {
		fmt.Println(styleHeader(root.Usage()))
		return nil
	}
	if len(args) == 1 && (args[0] == "-v" || args[0] == "--version") {
		fmt.Printf("%s version %s\n", styleSuccess(root.Name()), root.Version())
		return nil
	}

	normalized := normalizeCommand(args[0])
	command, aliasUsed := resolveCommandAlias(normalized)
	payload := args[1:]
	currentCommandForUsage = command
	if aliasUsed {
		fmt.Printf("%s %s -> %s\n", styleMuted("Alias:"), styleSuccess(normalized), styleSuccess(command))
	}

	if !root.IsKnownCommand(command) {
		printUnknownCommandSuggestion(normalized, root.Commands())
		return fmt.Errorf("unknown command: %s", normalized)
	}
	if maybeHandleCommandHelp(command, payload) {
		return nil
	}

	switch command {
	case commands.CmdInit:
		return runInit(payload)
	case commands.CmdLog:
		return runLog(payload)
	case commands.CmdTree:
		return runTree(payload)
	case commands.CmdDash:
		return runDash(payload)
	case commands.CmdAdd:
		return runWithAutoCommit("add", payload, runAdd)
	case commands.CmdAddEpic:
		return runAddEpic(payload)
	case commands.CmdAddMilestone:
		return runAddMilestone(payload)
	case commands.CmdAddPhase:
		return runAddPhase(payload)
	case commands.CmdList, commands.CmdLs:
		return runList(command, payload)
	case commands.CmdShow:
		return runShow(
			payload,
			true,
			parseFlag(payload, "--long"),
			parseFlag(payload, "--all"),
		)
	case commands.CmdCat:
		return runCat(payload)
	case commands.CmdGrab:
		return runWithAutoCommit("grab", payload, runGrab)
	case commands.CmdNext:
		return runNext(payload)
	case commands.CmdPreview:
		return runPreview(payload)
	case commands.CmdSkills:
		return runSkills(payload)
	case commands.CmdSearch:
		return runSearch(payload)
	case commands.CmdBlockers:
		return runBlockers(payload)
	case commands.CmdWhy:
		return runWhy(payload)
	case commands.CmdCheck:
		return runCheck(payload)
	case commands.CmdData:
		return runData(payload)
	case commands.CmdSchema:
		return runSchema(payload)
	case commands.CmdSession:
		return runSession(payload)
	case commands.CmdReport, commands.CmdReportAlias:
		return runReport(payload)
	case commands.CmdVelocity:
		return runVelocity(payload)
	case commands.CmdTimeline, commands.CmdTimelineAlias:
		return runTimeline(payload)
	case commands.CmdAdmin:
		return runAdmin(payload)
	case commands.CmdCI:
		return runCI(payload)
	case commands.CmdHowto:
		return runHowto(payload)
	case commands.CmdAgents:
		return runAgents(payload)
	case commands.CmdClaim:
		return runWithAutoCommit("claim", payload, runClaim)
	case commands.CmdEdit:
		return runWithAutoCommit("edit", payload, runEdit)
	case commands.CmdDone:
		return runWithAutoCommit("done", payload, runDone)
	case commands.CmdUnclaim:
		return runWithAutoCommit("unclaim", payload, runUnclaim)
	case commands.CmdBlocked:
		return runBlocked(payload)
	case commands.CmdCycle:
		return runCycle(payload)
	case commands.CmdHelp:
		return runHelp(payload)
	case commands.CmdWork:
		return runWork(payload)
	case commands.CmdVersion:
		return runVersion(payload)
	case commands.CmdSkip:
		return runSkip(payload)
	case commands.CmdHandoff:
		return runHandoff(payload)
	case commands.CmdUnclaimStale:
		return runUnclaimStale(payload)
	case commands.CmdSet:
		return runWithAutoCommit("set", payload, runSet)
	case commands.CmdUpdate:
		return runUpdate(payload)
	case commands.CmdUndone:
		return runWithAutoCommit("undone", payload, runUndone)
	case commands.CmdBenchmark:
		return runBenchmark(payload)
	case commands.CmdSync:
		return runSync()
	case commands.CmdMove:
		return runMove(payload)
	case commands.CmdLock:
		return runLock(payload, true)
	case commands.CmdUnlock:
		return runLock(payload, false)
	case commands.CmdIdea:
		return runWithAutoCommit("idea", payload, runIdea)
	case commands.CmdBug:
		return runWithAutoCommit("bug", payload, runBug)
	case commands.CmdFixed:
		return runFixed(payload)
	case commands.CmdMigrate:
		return runMigrate(payload)
	default:
		printCommandNotImplemented(command)
		return fmt.Errorf("command not implemented: %s", command)
	}
}

const (
	autoCommitAddPrefix     = "bl add"
	autoCommitBugPrefix     = "bl bug"
	autoCommitIdeaPrefix    = "bl idea"
	autoCommitClaimPrefix   = "bl claim"
	autoCommitGrabPrefix    = "bl grab"
	autoCommitDonePrefix    = "bl done"
	autoCommitUnclaimPrefix = "bl unclaim"
	autoCommitUndonePrefix  = "bl undone"
	autoCommitSetPrefix     = "bl set"
	autoCommitEditPrefix    = "bl edit"
)

type gitAutoCommitContext struct {
	hasStaged       bool
	preStatus       map[string]string
	canAmendBlAdd   bool
	prevAddUnpushed bool
}

type gitAutoCommitMetadata struct {
	id    string
	title string
}

type gitAutoCommitHandler func([]string, *gitAutoCommitMetadata) error

func runWithAutoCommit(command string, args []string, handler gitAutoCommitHandler) error {
	context, err := captureAutoCommitContext()
	if err != nil {
		context = nil
	}
	metadata := gitAutoCommitMetadata{}

	err = handler(args, &metadata)
	if err != nil {
		return err
	}

	if context == nil || context.hasStaged {
		return nil
	}

	if err := executeAutoCommit(command, context, metadata); err != nil {
		fmt.Printf("%s: %s\n", styleWarning("Auto-commit skipped"), err)
	}
	return nil
}

func captureAutoCommitContext() (*gitAutoCommitContext, error) {
	preStatus, err := gitStatusSnapshot()
	if err != nil {
		return nil, err
	}

	ctx := &gitAutoCommitContext{
		hasStaged: hasStagedChanges(preStatus),
		preStatus: preStatus,
	}

	if ctx.hasStaged {
		return ctx, nil
	}

	ctx.canAmendBlAdd = isPreviousCommitFromBlAdd()
	ctx.prevAddUnpushed = isPreviousCommitUnpushed()
	return ctx, nil
}

func executeAutoCommit(command string, context *gitAutoCommitContext, metadata gitAutoCommitMetadata) error {
	postStatus, err := gitStatusSnapshot()
	if err != nil {
		return err
	}

	changedFiles := changedTrackedPaths(context.preStatus, postStatus)
	if len(changedFiles) == 0 {
		return nil
	}

	if err := gitAddPaths(changedFiles); err != nil {
		return err
	}

	if command == "add" && context.canAmendBlAdd && context.prevAddUnpushed {
		if err := gitAmendCommit(); err == nil {
			return nil
		}
	}

	return gitCommit(autoCommitMessage(command, metadata))
}

func normalizeAutoCommitMetadata(value string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " ")), " ")
}

func formatAutoCommitMessage(prefix string, metadata gitAutoCommitMetadata) string {
	id := normalizeAutoCommitMetadata(metadata.id)
	title := normalizeAutoCommitMetadata(metadata.title)
	if id != "" && title != "" {
		return fmt.Sprintf("%s %s: %s", prefix, id, title)
	}
	if id != "" {
		return fmt.Sprintf("%s %s", prefix, id)
	}
	if title != "" {
		return fmt.Sprintf("%s %s", prefix, title)
	}
	return prefix
}

func autoCommitMessage(command string, metadata gitAutoCommitMetadata) string {
	id := strings.TrimSpace(metadata.id)
	switch command {
	case "add":
		if id == "" {
			return autoCommitAddPrefix
		}
		return formatAutoCommitMessage(autoCommitAddPrefix, metadata)
	case "bug":
		if id == "" {
			return autoCommitBugPrefix
		}
		return formatAutoCommitMessage(autoCommitBugPrefix, metadata)
	case "idea":
		if id == "" {
			return autoCommitIdeaPrefix
		}
		return formatAutoCommitMessage(autoCommitIdeaPrefix, metadata)
	case "claim":
		if id == "" {
			return autoCommitClaimPrefix
		}
		return formatAutoCommitMessage(autoCommitClaimPrefix, metadata)
	case "grab":
		if id == "" {
			return autoCommitGrabPrefix
		}
		return formatAutoCommitMessage(autoCommitGrabPrefix, metadata)
	case "done":
		if id == "" {
			return autoCommitDonePrefix
		}
		return formatAutoCommitMessage(autoCommitDonePrefix, metadata)
	case "unclaim":
		if id == "" {
			return autoCommitUnclaimPrefix
		}
		return formatAutoCommitMessage(autoCommitUnclaimPrefix, metadata)
	case "undone":
		if id == "" {
			return autoCommitUndonePrefix
		}
		return formatAutoCommitMessage(autoCommitUndonePrefix, metadata)
	case "set":
		if id == "" {
			return autoCommitSetPrefix
		}
		return formatAutoCommitMessage(autoCommitSetPrefix, metadata)
	case "edit":
		if id == "" {
			return autoCommitEditPrefix
		}
		return formatAutoCommitMessage(autoCommitEditPrefix, metadata)
	default:
		return fmt.Sprintf("backlog %s", command)
	}
}

func isPreviousCommitFromBlAdd() bool {
	message, err := gitLatestCommitMessage()
	if err != nil {
		return false
	}
	return strings.HasPrefix(message, autoCommitAddPrefix)
}

func isPreviousCommitUnpushed() bool {
	if _, err := gitCommand("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err != nil {
		return true
	}

	aheadRaw, err := gitCommand("rev-list", "--count", "@{u}..HEAD")
	if err != nil {
		return false
	}
	ahead, err := strconv.Atoi(strings.TrimSpace(aheadRaw))
	if err != nil {
		return false
	}
	return ahead > 0
}

func gitLatestCommitMessage() (string, error) {
	message, err := gitCommand("log", "-1", "--pretty=%B")
	if err != nil {
		return "", err
	}
	firstLine := strings.SplitN(strings.TrimSpace(message), "\n", 2)[0]
	return firstLine, nil
}

func changedTrackedPaths(before, after map[string]string) []string {
	paths := make(map[string]struct{})
	for path, afterStatus := range after {
		beforeStatus, ok := before[path]
		if !ok || beforeStatus != afterStatus {
			paths[path] = struct{}{}
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			paths[path] = struct{}{}
		}
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	return result
}

func hasStagedChanges(status map[string]string) bool {
	for _, state := range status {
		if len(state) < 2 {
			continue
		}
		if state[0] != ' ' && state[0] != '?' {
			return true
		}
	}
	return false
}

func gitStatusSnapshot() (map[string]string, error) {
	statusText, err := gitCommand("status", "--porcelain=v1")
	if err != nil {
		return nil, err
	}
	statusMap := map[string]string{}
	for _, rawLine := range strings.Split(strings.TrimRight(statusText, "\r\n"), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if line == "" {
			continue
		}
		if len(line) < 3 {
			continue
		}
		if len(line) >= 3 {
			status := line[0:2]
			path := strings.TrimRight(line[3:], "\r")
			if path == "" {
				continue
			}
			if strings.HasPrefix(status, "R") {
				parts := strings.SplitN(path, " -> ", 2)
				if len(parts) == 2 {
					statusMap[parts[1]] = status
					continue
				}
			}
			statusMap[path] = status
		}
	}
	return statusMap, nil
}

func gitAddPaths(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := []string{"add", "--"}
	args = append(args, paths...)
	_, err := gitCommand(args...)
	if err != nil {
		return err
	}
	return nil
}

func gitCommit(message string) error {
	_, err := gitCommand("commit", "-m", message)
	return err
}

func gitAmendCommit() error {
	_, err := gitCommand("commit", "--amend", "--no-edit")
	return err
}

func gitCommand(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			trimmed = err.Error()
		}
		return "", fmt.Errorf("%s", trimmed)
	}
	return strings.TrimRight(string(output), "\r\n"), nil
}

func runHelp(args []string) error {
	if len(args) == 0 {
		root := cmd.NewRootCommand()
		fmt.Println(styleHeader(root.Usage()))
		return nil
	}

	target := ""
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			target = normalizeCommand(arg)
			break
		}
	}

	if parseFlag(args, "--help", "-h") {
		if target != "" {
			root := cmd.NewRootCommand()
			normalized := target
			switch normalized {
			case commands.CmdLs:
				normalized = commands.CmdList
			case commands.CmdReportAlias:
				normalized = commands.CmdReport
			case commands.CmdTimelineAlias:
				normalized = commands.CmdTimeline
			}
			if !root.IsKnownCommand(normalized) {
				printUnknownCommandSuggestion(target, root.Commands())
				return fmt.Errorf("unknown command: %s", target)
			}
			printUsageForCommand(normalized)
			return nil
		}
		printUsageForCommand(commands.CmdHelp)
		return nil
	}

	if target == "" {
		root := cmd.NewRootCommand()
		fmt.Println(styleHeader(root.Usage()))
		return nil
	}

	root := cmd.NewRootCommand()
	normalized := target
	switch normalized {
	case commands.CmdLs:
		normalized = commands.CmdList
	case commands.CmdReportAlias:
		normalized = commands.CmdReport
	case commands.CmdTimelineAlias:
		normalized = commands.CmdTimeline
	}
	if !root.IsKnownCommand(normalized) {
		printUnknownCommandSuggestion(target, root.Commands())
		return fmt.Errorf("unknown command: %s", target)
	}

	printUsageForCommand(normalized)
	return nil
}

func maybeHandleCommandHelp(command string, args []string) bool {
	if command == commands.CmdHelp {
		return false
	}
	if !parseFlag(args, "--help", "-h") {
		return false
	}
	printUsageForCommand(command)
	return true
}

func resolveCommandAlias(command string) (string, bool) {
	switch command {
	case commands.CmdGrants:
		return commands.CmdGrab, true
	case commands.CmdSprint:
		return commands.CmdGrab, true
	default:
		return command, false
	}
}

func printUnknownCommandSuggestion(raw string, known []string) {
	fmt.Printf("%s %s\n", styleError("Unknown command:"), styleWarning(raw))
	suggestions := suggestCommands(raw, known, 4)
	if len(suggestions) > 0 {
		fmt.Printf("%s\n", styleSubHeader("Did you mean:"))
		for _, suggestion := range suggestions {
			fmt.Printf("  %s\n", styleSuccess(suggestion))
		}
	}
	fmt.Println(styleMuted("Run 'backlog --help' for available commands."))
	printCommandRecoveryHint()
}

func printCommandRecoveryHint() {
	fmt.Println(styleMuted("If command parsing fails, run 'backlog cycle' once to recover."))
}

func suggestCommands(raw string, candidates []string, limit int) []string {
	target := strings.ToLower(strings.TrimSpace(raw))
	if target == "" {
		return nil
	}

	type suggestion struct {
		command string
		score   int
	}
	candidateScores := make([]suggestion, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = normalizeCommand(candidate)
		if candidate == "" {
			continue
		}
		switch {
		case strings.HasPrefix(candidate, target), strings.HasPrefix(target, candidate):
			candidateScores = append(candidateScores, suggestion{command: candidate, score: 0})
		case strings.Contains(candidate, target), strings.Contains(target, candidate):
			candidateScores = append(candidateScores, suggestion{command: candidate, score: 1})
		default:
			distance := commandDistance(candidate, target)
			if distance > 3 {
				continue
			}
			candidateScores = append(candidateScores, suggestion{command: candidate, score: distance + 1})
		}
	}

	sort.Slice(candidateScores, func(i, j int) bool {
		if candidateScores[i].score == candidateScores[j].score {
			return candidateScores[i].command < candidateScores[j].command
		}
		return candidateScores[i].score < candidateScores[j].score
	})

	if len(candidateScores) > limit {
		candidateScores = candidateScores[:limit]
	}
	out := make([]string, 0, len(candidateScores))
	for _, suggestion := range candidateScores {
		out = append(out, suggestion.command)
	}
	return out
}

func commandDistance(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	cache := make([][]int, len(a)+1)
	for i := range cache {
		cache[i] = make([]int, len(b)+1)
	}
	for i := 0; i <= len(a); i++ {
		cache[i][0] = i
	}
	for j := 0; j <= len(b); j++ {
		cache[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cache[i][j] = min3(
				cache[i-1][j]+1,
				cache[i][j-1]+1,
				cache[i-1][j-1]+cost,
			)
		}
	}
	return cache[len(a)][len(b)]
}

func printCommandHelp(command, summary, usage string, options []string, examples []string) {
	fmt.Printf("\n%s\n", styleHeader("Command Help: backlog "+command))
	fmt.Printf("%s\n\n", styleMuted(summary))
	fmt.Printf("%s %s\n", styleSubHeader("Usage:"), styleSuccess(usage))
	if len(options) > 0 {
		fmt.Printf("\n%s\n", styleSubHeader("Options"))
		for _, option := range options {
			fmt.Printf("  %s\n", styleMuted(option))
		}
	}
	if len(examples) > 0 {
		fmt.Printf("\n%s\n", styleSubHeader("Examples"))
		for _, example := range examples {
			fmt.Printf("  %s\n", styleSuccess(example))
		}
	}
	fmt.Printf("\n%s\n", styleMuted("Tip: Use `backlog list` or `backlog tree` to find valid IDs for parent scopes."))
}

func printAddHelp() {
	printCommandHelp(
		"add",
		"Create a task under an epic.",
		"backlog add <EPIC_ID> --title <TITLE> [options]",
		[]string{
			"--title, -T         Task title (required)",
			"--estimate, -e      Estimate hours (default: 1)",
			"--complexity, -c    low|medium|high",
			"--priority, -p      low|medium|high|critical",
			"--depends-on, -d    Comma-separated dependency IDs",
			"--tags              Comma-separated tags",
			"--body, -b          Optional task body content",
		},
		[]string{
			"backlog add P1.M1.E1 --title \"Implement parser\"",
			"backlog add P1.M1.E1 -T \"Wire API\" -e 3 -c high -p high",
		},
	)
}

func printAddEpicHelp() {
	printCommandHelp(
		"add-epic",
		"Create an epic under a milestone.",
		"backlog add-epic <MILESTONE_ID> --title <TITLE> [options]",
		[]string{
			"--title, -T         Epic title (required)",
			"--name, -n          Alias for --title",
			"--estimate, -e      Estimate hours (default: 4)",
			"--complexity, -c    low|medium|high",
			"--depends-on, -d    Comma-separated dependency IDs",
			"--description       Optional epic description",
		},
		[]string{
			"backlog add-epic P1.M1 --title \"CLI polish\"",
			"backlog add-epic P1.M1 -n \"Reporting\" --depends-on P1.M1.E1",
		},
	)
}

func printAddMilestoneHelp() {
	printCommandHelp(
		"add-milestone",
		"Create a milestone under a phase.",
		"backlog add-milestone <PHASE_ID> --title <TITLE> [options]",
		[]string{
			"--title, -T         Milestone title (required)",
			"--name, -n          Alias for --title",
			"--estimate, -e      Estimate hours (default: 8)",
			"--complexity, -c    low|medium|high",
			"--depends-on, -d    Comma-separated dependency IDs",
			"--description       Optional milestone description",
		},
		[]string{
			"backlog add-milestone P1 --title \"Beta Readiness\"",
			"backlog add-milestone P1 -n \"Hardening\" -e 16",
		},
	)
}

func printAddPhaseHelp() {
	printCommandHelp(
		"add-phase",
		"Create a top-level phase in the backlog.",
		"backlog add-phase --title <TITLE> [options]",
		[]string{
			"--title, -T         Phase title (required)",
			"--name, -n          Alias for --title",
			"--weeks, -w         Timeline weeks (default: 2)",
			"--estimate, -e      Estimate hours (default: 40)",
			"--priority, -p      low|medium|high|critical",
			"--depends-on, -d    Comma-separated dependency IDs",
			"--description       Optional phase description",
		},
		[]string{
			"backlog add-phase --title \"Stabilization\"",
			"backlog add-phase -T \"v2 Launch\" -w 6 -e 120 -p high",
		},
	)
}

func printClaimHelp() {
	printCommandHelp(
		"claim",
		"Claim one or more tasks and mark them in progress.",
		"backlog claim <TASK_ID> [TASK_ID ...] [options]",
		[]string{
			"--agent            Agent name (default: cli-user)",
			"--force            Override existing claim owner",
			"--no-content       Suppress task body preview",
		},
		[]string{
			"backlog claim P1.M1.E1.T001",
			"backlog claim P1.M1.E1.T001 P1.M1.E1.T002 --agent agent-a",
		},
	)
}

func printDoneHelp() {
	printCommandHelp(
		"done",
		"Mark one or more tasks done (or set explicit status).",
		"backlog done <TASK_ID> [TASK_ID ...] [options]",
		[]string{
			"--status           Target status (default: done)",
			"--force            Allow transition even if status checks fail",
			"--verify           Compatibility flag for workflow parity",
		},
		[]string{
			"backlog done P1.M1.E1.T001",
			"backlog done P1.M1.E1.T001 --status blocked --force",
		},
	)
}

func printUpdateHelp() {
	printCommandHelp(
		"update",
		"Update task state and selected metadata in one command.",
		"backlog update <TASK_ID> <STATUS> [options]",
		[]string{
			"--reason           Required for blocked/rejected/cancelled transitions",
			"--title            Update title",
			"--priority         low|medium|high|critical",
			"--complexity       low|medium|high",
			"--estimate         Numeric estimate hours",
		},
		[]string{
			"backlog update P1.M1.E1.T001 blocked --reason \"waiting on API\"",
			"backlog update P1.M1.E1.T001 in_progress --priority high",
		},
	)
}

func printMoveHelp() {
	printCommandHelp(
		"move",
		"Move task/epic/milestone to a new parent and remap IDs safely.",
		"backlog move <SOURCE_ID> --to <DEST_ID>",
		[]string{
			"--to               Destination parent ID (required)",
		},
		[]string{
			"backlog move P1.M1.E1.T001 --to P1.M1.E2",
			"backlog move P1.M1.E2 --to P1.M2",
		},
	)
}

func printSetHelp() {
	printCommandHelp(
		"set",
		"Patch selected task properties without changing unrelated fields.",
		"backlog set <TASK_ID> [property flags]",
		[]string{
			"--status           Target status",
			"--priority         low|medium|high|critical",
			"--complexity       low|medium|high",
			"--estimate         Numeric estimate hours",
			"--title            New title",
			"--depends-on       Comma-separated dependency IDs",
			"--tags             Comma-separated tags",
			"--reason           Reason text for constrained transitions",
			"--body, -b         Replace task body content",
			"--append-body      Append to existing task body content",
		},
		[]string{
			"backlog set P1.M1.E1.T001 --priority high --tags api,auth",
			"backlog set P1.M1.E1.T001 --status blocked --reason \"waiting on backend\"",
		},
	)
}

func printListHelp() {
	printCommandHelp(
		"list",
		"List tasks with filtering and scope controls.",
		"backlog list [<SCOPE> ...] [options]",
		[]string{
			"--status              Filter by comma-separated status values",
			"--critical            Show only critical path tasks",
			"--available, -a       Show all unblocked and available tasks",
			"--complexity          Filter by complexity (low|medium|high|critical)",
			"--priority            Filter by priority (low|medium|high|critical)",
			"--progress            Show progress bars",
			"--json                Output JSON",
			"--all                 Show all milestones (no limit)",
			"--unfinished          Show only unfinished items",
			"--bugs, -b            Show only bug tasks",
			"--ideas, -i           Show only idea tasks",
			"--show-completed-aux  Include completed/cancelled/rejected bugs and ideas",
			"--phase               Filter by phase ID",
			"--milestone           Filter by milestone ID (e.g. M1)",
			"--epic                Filter by epic ID",
			"--help, -h           Show this help message",
		},
		[]string{
			"backlog list",
			"backlog list --json",
			"backlog list P1.M1 --progress",
			"backlog list P1.M1 P2.M1 --json",
			"backlog list --phase P1 --bugs",
		},
	)
}

func printNextCommands(commands ...string) {
	trimmed := []string{}
	for _, command := range commands {
		if strings.TrimSpace(command) == "" {
			continue
		}
		trimmed = append(trimmed, command)
	}
	if len(trimmed) == 0 {
		return
	}
	fmt.Printf("%s\n", styleSubHeader("Next:"))
	for _, command := range trimmed {
		fmt.Printf("  %s\n", styleSuccess(command))
	}
}

func shellQuotePath(raw string) string {
	return strconv.Quote(raw)
}

func printTaskFileReadCommand(taskPath string, showPreview bool, previewLines int) {
	taskPath = strings.TrimSpace(taskPath)
	if taskPath == "" {
		return
	}
	quoted := shellQuotePath(taskPath)
	command := "cat " + quoted
	if showPreview && previewLines > 0 {
		command = command + fmt.Sprintf(" | head -n %d", previewLines)
	}
	fmt.Printf("  %s\n", styleSuccess(fmt.Sprintf("$ %s", command)))
}

func printTaskFileReadEOF() {
	fmt.Printf("  %s\n", styleMuted(taskFileReadEOFMarker))
}

func printTaskFileReadCommandsForTask(dataDir string, task models.Task, showPreview bool) {
	taskPath := filepath.Join(dataDir, task.File)
	printTaskFileReadCommand(taskPath, showPreview, taskFileReadPreviewLines)
	printTaskFileReadEOF()
}

func taskFileStats(taskFilePath string) (int64, int, error) {
	f, err := os.Open(taskFilePath)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}

	lineCount := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lineCount++
	}
	if err := scanner.Err(); err != nil {
		return info.Size(), lineCount, err
	}
	return info.Size(), lineCount, nil
}

func normalizeCommand(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}

type initOptions struct {
	project       string
	description   string
	timelineWeeks int
}

func runInit(args []string) error {
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdInit)
		return nil
	}
	opts, err := parseInit(args)
	if err != nil {
		return printUsageError(commands.CmdInit, err)
	}
	if opts.project == "" {
		return printUsageError(commands.CmdInit, errors.New("init requires --project"))
	}

	if err := ensureBacklogDataAvailable(); err != nil {
		return err
	}
	indexPath := filepath.Join(config.BacklogDir, "index.yaml")
	if _, err := os.Stat(indexPath); err == nil {
		return errors.New("Already initialized (.backlog/index.yaml exists)")
	}
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		return fmt.Errorf("failed to create .backlog: %w", err)
	}
	content := fmt.Sprintf(
		`project: %q
description: %q
timeline_weeks: %d
phases: []
`,
		opts.project,
		opts.description,
		opts.timelineWeeks,
	)
	if err := os.WriteFile(indexPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", indexPath, err)
	}
	fmt.Printf("%s %s in %s/\n", styleSuccess("Initialized project"), styleMuted(fmt.Sprintf("%q", opts.project)), filepath.Dir(indexPath))
	return nil
}

func runBenchmark(args []string) error {
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdBenchmark)
		return nil
	}
	if err := validateAllowedFlagsForUsage(
		commands.CmdBenchmark,
		args,
		map[string]bool{
			"--json":          true,
			"--top":           true,
			"--mode":          true,
			"--parse-body":    true,
			"--no-parse-body": true,
			"--help":          true,
			"-h":              true,
		},
	); err != nil {
		return err
	}

	rawMode := strings.TrimSpace(parseOption(args, "--mode"))
	mode := "full"
	if rawMode != "" {
		mode = rawMode
	}
	if mode != "full" && mode != "metadata" && mode != "index" {
		return printUsageError(commands.CmdBenchmark, fmt.Errorf("--mode must be one of: full, metadata, index"))
	}

	parseTaskBody := true
	if parseFlag(args, "--no-parse-body") {
		parseTaskBody = false
	}
	if parseFlag(args, "--parse-body") {
		parseTaskBody = true
	}
	effectiveParseTaskBody := parseTaskBody && mode == "full"

	top, err := parseIntOptionWithDefault(args, 10, "--top")
	if err != nil {
		return err
	}
	if top <= 0 {
		return printUsageError(commands.CmdBenchmark, fmt.Errorf("--top must be a positive integer"))
	}

	_, benchmark, err := loader.New().LoadWithBenchmark(mode, effectiveParseTaskBody, true, true)
	if err != nil {
		return err
	}

	taskTotal := benchmark.Counts["tasks"]
	missingTaskFiles := benchmark.MissingTaskFiles
	foundTaskFiles := taskTotal - missingTaskFiles
	indexParseMs := benchmark.IndexParseMs
	taskFrontmatterParseMs := benchmark.TaskFrontmatterParse
	taskBodyParseMs := benchmark.TaskBodyParse
	otherParseMs := benchmark.OverallMs - indexParseMs - taskFrontmatterParseMs - taskBodyParseMs
	if otherParseMs < 0 {
		otherParseMs = 0
	}

	slowestPhases := sortedTimings(benchmark.PhaseTimings, top)
	slowestMilestones := sortedTimings(benchmark.MilestoneTimings, top)
	slowestEpics := sortedTimings(benchmark.EpicTimings, top)

	totalFilesParsed := 0
	for _, count := range benchmark.Files {
		totalFilesParsed += count
	}

	if parseFlag(args, "--json") {
		output := map[string]interface{}{
			"overall_ms":                benchmark.OverallMs,
			"index_parse_ms":            indexParseMs,
			"task_frontmatter_parse_ms": taskFrontmatterParseMs,
			"task_body_parse_ms":        taskBodyParseMs,
			"files": map[string]interface{}{
				"total":      totalFilesParsed,
				"by_type":    benchmark.Files,
				"by_type_ms": benchmark.FilesByTypeMs,
			},
			"counts": map[string]int{
				"phases":     benchmark.Counts["phases"],
				"milestones": benchmark.Counts["milestones"],
				"epics":      benchmark.Counts["epics"],
				"tasks":      taskTotal,
			},
			"missing_task_files": benchmark.MissingTaskFiles,
			"phase_timings":      benchmark.PhaseTimings,
			"milestone_timings":  benchmark.MilestoneTimings,
			"epic_timings":       benchmark.EpicTimings,
			"task_timings":       benchmark.TaskTimings,
			"parse_mode":         mode,
			"parse_task_body":    effectiveParseTaskBody,
			"summary": map[string]interface{}{
				"overall_ms":                benchmark.OverallMs,
				"files_parsed":              totalFilesParsed,
				"task_files_total":          taskTotal,
				"task_files_found":          foundTaskFiles,
				"task_files_missing":        missingTaskFiles,
				"index_parse_ms":            indexParseMs,
				"task_frontmatter_parse_ms": taskFrontmatterParseMs,
				"task_body_parse_ms":        taskBodyParseMs,
				"task_parse_other_ms":       otherParseMs,
				"parse_mode":                mode,
				"parse_task_body":           effectiveParseTaskBody,
				"node_counts": map[string]int{
					"phases":     benchmark.Counts["phases"],
					"milestones": benchmark.Counts["milestones"],
					"epics":      benchmark.Counts["epics"],
				},
			},
			"slowest": map[string]interface{}{
				"phases":     slowestPhases,
				"milestones": slowestMilestones,
				"epics":      slowestEpics,
			},
		}
		raw, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}

	fmt.Println(styleHeader("Task Tree Benchmark"))
	parseBodyStatus := styleError("disabled")
	if effectiveParseTaskBody {
		parseBodyStatus = styleSuccess("enabled")
	}
	fmt.Printf("%s: %s\n", styleSubHeader("Overall parse time"), formatMs(benchmark.OverallMs))
	fmt.Printf("%s: %s\n", styleSubHeader("Parse mode"), styleSuccess(mode))
	fmt.Printf("%s: %s\n", styleSubHeader("Task body parsing"), parseBodyStatus)
	fmt.Printf("%s: %s\n", styleSubHeader("Index parse time"), formatMs(indexParseMs))
	fmt.Printf("%s: %s\n", styleSubHeader("Task frontmatter parse time"), formatMs(taskFrontmatterParseMs))
	fmt.Printf("%s: %s\n", styleSubHeader("Task body parse time"), formatMs(taskBodyParseMs))
	fmt.Printf("%s: %s\n", styleSubHeader("Other parse time"), formatMs(otherParseMs))
	fmt.Printf("%s: %d\n", styleSubHeader("Total files parsed"), totalFilesParsed)
	fmt.Printf(
		"%s: %d (%s, %s)\n",
		styleSubHeader("Task files (leaves)"),
		taskTotal,
		styleSuccess(fmt.Sprintf("%d found", foundTaskFiles)),
		styleError(fmt.Sprintf("%d missing", missingTaskFiles)),
	)
	fmt.Printf("%s: %d\n", styleSubHeader("Phases parsed"), benchmark.Counts["phases"])
	fmt.Printf("%s: %d\n", styleSubHeader("Milestones parsed"), benchmark.Counts["milestones"])
	fmt.Printf("%s: %d\n", styleSubHeader("Epics parsed"), benchmark.Counts["epics"])
	fmt.Println("")

	fileTypes := make([]string, 0, len(benchmark.Files))
	for fileType := range benchmark.Files {
		fileTypes = append(fileTypes, fileType)
	}
	sort.Strings(fileTypes)
	if len(fileTypes) > 0 {
		fmt.Println(styleSubHeader("Files by type:"))
		for _, fileType := range fileTypes {
			count := benchmark.Files[fileType]
			if count == 0 {
				continue
			}
			fileTypeMs := benchmark.FilesByTypeMs[fileType]
			fmt.Printf("  %s: %d files (%s)\n", styleSubHeader(fileType), count, formatMs(fileTypeMs))
		}
	}

	if len(slowestPhases) > 0 {
		fmt.Println(styleSubHeader("\nSlowest phases"))
		for _, item := range slowestPhases {
			fmt.Printf("  %s (%s): %s\n", styleSuccess(asString(item["id"])), styleMuted(asString(item["path"])), styleMuted(formatMs(timingMS(item))))
		}
	}

	if len(slowestMilestones) > 0 {
		fmt.Println(styleSubHeader("\nSlowest milestones"))
		for _, item := range slowestMilestones {
			fmt.Printf("  %s (%s): %s\n", styleSuccess(asString(item["id"])), styleMuted(asString(item["path"])), styleMuted(formatMs(timingMS(item))))
		}
	}

	if len(slowestEpics) > 0 {
		fmt.Println(styleSubHeader("\nSlowest epics"))
		for _, item := range slowestEpics {
			fmt.Printf("  %s (%s): %s\n", styleSuccess(asString(item["id"])), styleMuted(asString(item["path"])), styleMuted(formatMs(timingMS(item))))
		}
	}

	return nil
}

func formatMs(ms float64) string {
	return fmt.Sprintf("%.2fms", ms)
}

func boolLabel(value bool, whenTrue, whenFalse string) string {
	if value {
		return whenTrue
	}
	return whenFalse
}

func timingMS(record map[string]any) float64 {
	raw, ok := record["ms"]
	if !ok {
		return 0
	}
	switch value := raw.(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	default:
		return 0
	}
}

func sortedTimings(items []map[string]any, top int) []map[string]any {
	out := make([]map[string]any, len(items))
	copy(out, items)
	sort.Slice(out, func(i, j int) bool {
		return timingMS(out[i]) > timingMS(out[j])
	})
	if top >= len(out) {
		return out
	}
	return out[:top]
}

func parseInit(args []string) (initOptions, error) {
	opts := initOptions{timelineWeeks: 0}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--project", "-p":
			if i+1 >= len(args) {
				return initOptions{}, errors.New("expected value for --project")
			}
			opts.project = args[i+1]
			i++
		case "--description", "-d":
			if i+1 >= len(args) {
				return initOptions{}, errors.New("expected value for --description")
			}
			opts.description = args[i+1]
			i++
		case "--timeline-weeks", "-w":
			if i+1 >= len(args) {
				return initOptions{}, errors.New("expected value for --timeline-weeks")
			}
			value, err := strconv.Atoi(args[i+1])
			if err != nil {
				return initOptions{}, errors.New("invalid --timeline-weeks value")
			}
			opts.timelineWeeks = value
			i++
		default:
			return initOptions{}, fmt.Errorf("invalid argument: %s", arg)
		}
	}
	return opts, nil
}

func runAdd(args []string, metadata *gitAutoCommitMetadata) error {
	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	if parseFlag(args, "--help", "-h") {
		printAddHelp()
		return nil
	}
	allowed := map[string]bool{
		"--title":      true,
		"-T":           true,
		"--estimate":   true,
		"-e":           true,
		"--complexity": true,
		"-c":           true,
		"--priority":   true,
		"-p":           true,
		"--depends-on": true,
		"-d":           true,
		"--tags":       true,
		"--body":       true,
		"-b":           true,
		"--help":       true,
		"-h":           true,
	}
	if len(args) == 0 {
		return printUsageError(commands.CmdAdd, errors.New("add requires EPIC_ID"))
	}
	if err := validateAllowedFlagsForUsage(commands.CmdAdd, args, allowed); err != nil {
		return err
	}
	positional := positionalArgs(args, allowed)
	if len(positional) == 0 {
		return printUsageError(commands.CmdAdd, errors.New("add requires EPIC_ID"))
	}
	if len(positional) > 1 {
		return printUsageError(commands.CmdAdd, fmt.Errorf("unexpected argument(s): %s", strings.Join(positional[1:], " ")))
	}
	epicID := positional[0]
	if epicID == "" {
		return printUsageError(commands.CmdAdd, errors.New("add requires EPIC_ID"))
	}
	title := parseOption(args, "--title", "-T")
	if strings.TrimSpace(title) == "" {
		return printUsageError(commands.CmdAdd, errors.New("add requires --title"))
	}
	estimate, err := parseFloatOptionWithDefault(args, 1, "--estimate", "-e")
	if err != nil {
		return err
	}
	complexity, err := parseComplexityOption(args, "--complexity", "-c")
	if err != nil {
		return err
	}
	priority, err := parsePriorityOption(args, "--priority", "-p")
	if err != nil {
		return err
	}
	dependsOn, err := parseDependencyIDs(parseOption(args, "--depends-on", "-d"))
	if err != nil {
		return err
	}
	rawTags := strings.TrimSpace(parseOption(args, "--tags"))
	tags := []string{"idea", "planning"}
	if rawTags != "" {
		tags = parseCSV(rawTags)
	}
	body := parseOption(args, "--body", "-b")

	parsedEpicID, err := models.ParseTaskPath(epicID)
	if err != nil || !parsedEpicID.IsEpic() {
		return fmt.Errorf("invalid epic id: %s", epicID)
	}

	l := loader.New()
	tree, err := l.Load("metadata", true, true)
	if err != nil {
		return err
	}

	epic := tree.FindEpic(parsedEpicID.FullID())
	if epic == nil {
		return fmt.Errorf("Epic not found: %s", parsedEpicID.FullID())
	}

	phase := tree.FindPhase(parsedEpicID.PhaseID())
	if phase == nil {
		return fmt.Errorf("Epic not found: %s", parsedEpicID.FullID())
	}
	milestone := tree.FindMilestone(parsedEpicID.MilestoneID())
	if milestone == nil {
		return fmt.Errorf("Epic not found: %s", parsedEpicID.FullID())
	}
	if phase.Locked {
		return fmt.Errorf(
			"Phase %s has been closed and cannot accept new tasks. The agent should create a new epic.",
			phase.ID,
		)
	}
	if milestone.Locked {
		return fmt.Errorf(
			"Milestone %s has been closed and cannot accept new tasks. The agent should create a new epic.",
			milestone.ID,
		)
	}
	if epic.Locked {
		return fmt.Errorf(
			"Epic %s has been closed and cannot accept new tasks. The agent should create a new epic.",
			epic.ID,
		)
	}

	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	shortTaskIDs := make([]string, 0, len(epic.Tasks))
	for _, task := range epic.Tasks {
		shortTaskIDs = append(shortTaskIDs, strings.TrimPrefix(task.ID, parsedEpicID.FullID()+"."))
	}
	nextTaskID := models.NextTaskID(shortTaskIDs)

	epicDir := filepath.Join(dataDir, phase.Path, milestone.Path, epic.Path)
	if err := os.MkdirAll(epicDir, 0o755); err != nil {
		return fmt.Errorf("failed to create epic directory: %w", err)
	}
	slug := models.Slugify(title, models.DirectoryNameWidth*15)
	taskFile := fmt.Sprintf("%s-%s.todo", nextTaskID, slug)
	taskPath := filepath.Join(epicDir, taskFile)

	frontmatter := map[string]any{
		"id":             parsedEpicID.FullID() + "." + nextTaskID,
		"title":          title,
		"status":         string(models.StatusPending),
		"estimate_hours": estimate,
		"complexity":     complexity,
		"priority":       priority,
		"depends_on":     dependsOn,
		"tags":           tags,
	}
	bodyText := body
	if bodyText == "" {
		bodyText = fmt.Sprintf(
			"\n# %s\n\n## Requirements\n\n- TODO: Add requirements\n\n## Acceptance Criteria\n\n- TODO: Add acceptance criteria\n",
			title,
		)
	}
	payload, err := yaml.Marshal(frontmatter)
	if err != nil {
		return fmt.Errorf("failed to build task frontmatter: %w", err)
	}
	content := fmt.Sprintf("---\n%s---\n%s", string(payload), bodyText)
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", taskPath, err)
	}

	epicIndexPath := filepath.Join(epicDir, "index.yaml")
	epicIndex, err := readYAMLMapFile(epicIndexPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if epicIndex == nil {
		epicIndex = map[string]interface{}{
			"tasks": []map[string]interface{}{},
		}
	}
	appendToList(epicIndex, "tasks", map[string]interface{}{
		"id":             nextTaskID,
		"file":           taskFile,
		"title":          title,
		"status":         string(models.StatusPending),
		"estimate_hours": estimate,
		"complexity":     complexity,
		"priority":       priority,
		"depends_on":     dependsOn,
	})
	if err := writeYAMLMapFile(epicIndexPath, epicIndex); err != nil {
		return err
	}

	newTaskID := parsedEpicID.FullID() + "." + nextTaskID
	fmt.Printf("%s %s\n", styleSuccess("Created task:"), styleSuccess(newTaskID))
	relTaskPath, err := filepath.Rel(dataDir, taskPath)
	if err != nil {
		return fmt.Errorf("failed to compute task relative path: %w", err)
	}
	fmt.Printf("%s %s/%s\n", styleSubHeader("File:"), styleMuted(filepath.Base(dataDir)), styleMuted(filepath.ToSlash(relTaskPath)))
	if body == "" {
		fmt.Println(styleWarning("IMPORTANT: You MUST fill in the .todo file that was created."))
	}
	*metadata = gitAutoCommitMetadata{
		id:    newTaskID,
		title: title,
	}
	printNextCommands(
		"backlog show "+newTaskID,
		"backlog claim "+newTaskID,
	)
	return nil
}

func runAddEpic(args []string) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdAddEpic)
		return nil
	}
	allowed := map[string]bool{
		"--title":       true,
		"-T":            true,
		"--name":        true,
		"-n":            true,
		"--estimate":    true,
		"-e":            true,
		"--complexity":  true,
		"-c":            true,
		"--depends-on":  true,
		"-d":            true,
		"--description": true,
		"--help":        true,
		"-h":            true,
	}
	if len(args) == 0 {
		return printUsageError(commands.CmdAddEpic, errors.New("add-epic requires MILESTONE_ID"))
	}
	if err := validateAllowedFlagsForUsage(commands.CmdAddEpic, args, allowed); err != nil {
		return err
	}
	positional := positionalArgs(args, allowed)
	if len(positional) == 0 {
		return printUsageError(commands.CmdAddEpic, errors.New("add-epic requires MILESTONE_ID"))
	}
	if len(positional) > 1 {
		return printUsageError(commands.CmdAddEpic, fmt.Errorf("unexpected argument(s): %s", strings.Join(positional[1:], " ")))
	}
	milestoneID := positional[0]
	if milestoneID == "" {
		return printUsageError(commands.CmdAddEpic, errors.New("add-epic requires MILESTONE_ID"))
	}
	title := parseOption(args, "--title", "-T", "--name", "-n")
	if strings.TrimSpace(title) == "" {
		return printUsageError(commands.CmdAddEpic, errors.New("add-epic requires --title"))
	}
	estimate, err := parseFloatOptionWithDefault(args, 4, "--estimate", "-e")
	if err != nil {
		return err
	}
	complexity, err := parseComplexityOption(args, "--complexity", "-c")
	if err != nil {
		return err
	}
	dependsOn, err := parseDependencyIDs(parseOption(args, "--depends-on", "-d"))
	if err != nil {
		return err
	}
	description := parseOption(args, "--description")

	parsedMilestoneID, err := models.ParseTaskPath(milestoneID)
	if err != nil || !parsedMilestoneID.IsMilestone() {
		return fmt.Errorf("invalid milestone id: %s", milestoneID)
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	milestone := tree.FindMilestone(parsedMilestoneID.FullID())
	if milestone == nil {
		return fmt.Errorf("Milestone not found: %s", parsedMilestoneID.FullID())
	}
	phase := tree.FindPhase(parsedMilestoneID.PhaseID())
	if phase == nil {
		return fmt.Errorf("Milestone not found: %s", parsedMilestoneID.FullID())
	}
	if phase.Locked {
		return fmt.Errorf(
			"Phase %s has been closed and cannot accept new epics. Create a new phase.",
			phase.ID,
		)
	}
	if milestone.Locked {
		return fmt.Errorf(
			"Milestone %s has been closed and cannot accept new epics. The agent should create a new epic.",
			milestone.ID,
		)
	}

	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	existingEpicIDs := make([]string, 0, len(milestone.Epics))
	for _, epicRef := range milestone.Epics {
		short := strings.TrimPrefix(epicRef.ID, parsedMilestoneID.FullID()+".")
		if short == epicRef.ID {
			short = epicRef.ID
		}
		existingEpicIDs = append(existingEpicIDs, short)
	}
	nextEpicID := models.NextEpicID(existingEpicIDs)
	nextEpicNumber := idSuffixNumber(nextEpicID, "E")
	dirName := fmt.Sprintf("%02d-%s", nextEpicNumber, models.Slugify(title, models.DirectoryNameWidth*15))
	epicDir := filepath.Join(dataDir, phase.Path, milestone.Path, dirName)
	if err := os.MkdirAll(epicDir, 0o755); err != nil {
		return fmt.Errorf("failed to create epic directory: %w", err)
	}

	epicIndexPath := filepath.Join(epicDir, "index.yaml")
	epicData := map[string]any{
		"id":             fmt.Sprintf("%s.%s", parsedMilestoneID.FullID(), nextEpicID),
		"name":           title,
		"status":         string(models.StatusPending),
		"locked":         false,
		"estimate_hours": estimate,
		"complexity":     complexity,
		"depends_on":     dependsOn,
		"tasks":          []any{},
	}
	if err := writeYAMLMapFile(epicIndexPath, epicData); err != nil {
		return err
	}

	milestoneIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, "index.yaml")
	milestoneIndex, err := readYAMLMapFile(milestoneIndexPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if milestoneIndex == nil {
		milestoneIndex = map[string]interface{}{}
	}
	appendToList(milestoneIndex, "epics", map[string]interface{}{
		"id":             nextEpicID,
		"name":           title,
		"path":           dirName,
		"status":         string(models.StatusPending),
		"locked":         false,
		"estimate_hours": estimate,
		"complexity":     complexity,
		"depends_on":     dependsOn,
		"description":    description,
	})
	if err := writeYAMLMapFile(milestoneIndexPath, milestoneIndex); err != nil {
		return err
	}

	fmt.Printf("%s %s\n", styleSuccess("Created epic:"), styleSuccess(parsedMilestoneID.FullID()+"."+nextEpicID))
	epicRelPath := filepath.ToSlash(filepath.Join(phase.Path, milestone.Path, dirName, "index.yaml"))
	fmt.Printf("%s %s/%s\n", styleSubHeader("File:"), styleMuted(filepath.Base(dataDir)), styleMuted(epicRelPath))
	newEpicID := parsedMilestoneID.FullID() + "." + nextEpicID
	printNextCommands(
		"backlog show "+newEpicID,
		"backlog add "+newEpicID+" --title \"<task title>\"",
	)
	return nil
}

func runAddMilestone(args []string) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	allowed := map[string]bool{
		"--title":       true,
		"-T":            true,
		"--name":        true,
		"-n":            true,
		"--estimate":    true,
		"-e":            true,
		"--complexity":  true,
		"-c":            true,
		"--depends-on":  true,
		"-d":            true,
		"--description": true,
		"--help":        true,
		"-h":            true,
	}
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdAddMilestone)
		return nil
	}
	if len(args) == 0 {
		return printUsageError(commands.CmdAddMilestone, errors.New("add-milestone requires PHASE_ID"))
	}
	if err := validateAllowedFlagsForUsage(commands.CmdAddMilestone, args, allowed); err != nil {
		return err
	}
	positional := positionalArgs(args, allowed)
	if len(positional) == 0 {
		return printUsageError(commands.CmdAddMilestone, errors.New("add-milestone requires PHASE_ID"))
	}
	if len(positional) > 1 {
		return printUsageError(commands.CmdAddMilestone, fmt.Errorf("unexpected argument(s): %s", strings.Join(positional[1:], " ")))
	}
	phaseID := positional[0]
	if phaseID == "" {
		return printUsageError(commands.CmdAddMilestone, errors.New("add-milestone requires PHASE_ID"))
	}
	title := parseOption(args, "--title", "-T", "--name", "-n")
	if strings.TrimSpace(title) == "" {
		return printUsageError(commands.CmdAddMilestone, errors.New("add-milestone requires --title"))
	}
	estimate, err := parseFloatOptionWithDefault(args, 8, "--estimate", "-e")
	if err != nil {
		return err
	}
	complexity, err := parseComplexityOption(args, "--complexity", "-c")
	if err != nil {
		return err
	}
	dependsOn, err := parseDependencyIDs(parseOption(args, "--depends-on", "-d"))
	if err != nil {
		return err
	}
	description := parseOption(args, "--description")

	parsedPhaseID, err := models.ParseTaskPath(phaseID)
	if err != nil || !parsedPhaseID.IsPhase() {
		return fmt.Errorf("invalid phase id: %s", phaseID)
	}
	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	phase := tree.FindPhase(parsedPhaseID.FullID())
	if phase == nil {
		return fmt.Errorf("Phase not found: %s", parsedPhaseID.FullID())
	}
	if phase.Locked {
		return fmt.Errorf("Phase %s has been closed and cannot accept new milestones. Create a new phase.", phase.ID)
	}

	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	existingMilestoneIDs := make([]string, 0, len(phase.Milestones))
	for _, milestoneRef := range phase.Milestones {
		short := strings.TrimPrefix(milestoneRef.ID, parsedPhaseID.FullID()+".")
		if short == milestoneRef.ID {
			short = milestoneRef.ID
		}
		existingMilestoneIDs = append(existingMilestoneIDs, short)
	}
	nextMilestoneID := models.NextMilestoneID(existingMilestoneIDs)
	nextMilestoneNumber := idSuffixNumber(nextMilestoneID, "M")
	dirName := fmt.Sprintf("%02d-%s", nextMilestoneNumber, models.Slugify(title, models.DirectoryNameWidth*15))
	msDir := filepath.Join(dataDir, phase.Path, dirName)
	if err := os.MkdirAll(msDir, 0o755); err != nil {
		return fmt.Errorf("failed to create milestone directory: %w", err)
	}

	msIndexPath := filepath.Join(msDir, "index.yaml")
	msIndex := map[string]any{
		"id":             fmt.Sprintf("%s.%s", phase.ID, nextMilestoneID),
		"name":           title,
		"status":         string(models.StatusPending),
		"locked":         false,
		"estimate_hours": estimate,
		"complexity":     complexity,
		"depends_on":     dependsOn,
		"epics":          []any{},
	}
	if err := writeYAMLMapFile(msIndexPath, msIndex); err != nil {
		return err
	}

	phaseIndexPath := filepath.Join(dataDir, phase.Path, "index.yaml")
	phaseIndex, err := readYAMLMapFile(phaseIndexPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if phaseIndex == nil {
		phaseIndex = map[string]interface{}{}
	}
	appendToList(phaseIndex, "milestones", map[string]interface{}{
		"id":             nextMilestoneID,
		"name":           title,
		"path":           dirName,
		"status":         string(models.StatusPending),
		"locked":         false,
		"estimate_hours": estimate,
		"complexity":     complexity,
		"depends_on":     dependsOn,
		"description":    description,
	})
	if err := writeYAMLMapFile(phaseIndexPath, phaseIndex); err != nil {
		return err
	}

	fmt.Printf("%s %s\n", styleSuccess("Created milestone:"), styleSuccess(fmt.Sprintf("%s.%s", phase.ID, nextMilestoneID)))
	milestoneRelPath := filepath.ToSlash(filepath.Join(phase.Path, dirName, "index.yaml"))
	fmt.Printf("%s %s/%s\n", styleSubHeader("File:"), styleMuted(filepath.Base(dataDir)), styleMuted(milestoneRelPath))
	newMilestoneID := fmt.Sprintf("%s.%s", phase.ID, nextMilestoneID)
	printNextCommands(
		"backlog show "+newMilestoneID,
		"backlog add-epic "+newMilestoneID+" --title \"<epic title>\"",
	)
	return nil
}

func runAddPhase(args []string) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	allowed := map[string]bool{
		"--title":       true,
		"-T":            true,
		"--name":        true,
		"-n":            true,
		"--weeks":       true,
		"-w":            true,
		"--estimate":    true,
		"-e":            true,
		"--priority":    true,
		"-p":            true,
		"--depends-on":  true,
		"-d":            true,
		"--description": true,
		"--help":        true,
		"-h":            true,
	}
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdAddPhase)
		return nil
	}
	if len(args) == 0 {
		return printUsageError(commands.CmdAddPhase, errors.New("add-phase requires --title"))
	}
	if err := validateAllowedFlagsForUsage(commands.CmdAddPhase, args, allowed); err != nil {
		return err
	}
	positional := positionalArgs(args, allowed)
	if len(positional) > 0 {
		return printUsageError(commands.CmdAddPhase, fmt.Errorf("unexpected argument(s): %s", strings.Join(positional, " ")))
	}
	title := parseOption(args, "--title", "-T", "--name", "-n")
	if strings.TrimSpace(title) == "" {
		return printUsageError(commands.CmdAddPhase, errors.New("add-phase requires --title"))
	}
	weeks, err := parseIntOptionWithDefault(args, 2, "--weeks", "-w")
	if err != nil {
		return err
	}
	estimate, err := parseFloatOptionWithDefault(args, 40, "--estimate", "-e")
	if err != nil {
		return err
	}
	priority, err := parsePriorityOption(args, "--priority", "-p")
	if err != nil {
		return err
	}
	dependsOn, err := parseDependencyIDs(parseOption(args, "--depends-on", "-d"))
	if err != nil {
		return err
	}
	description := parseOption(args, "--description")

	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	rootIndexPath := filepath.Join(dataDir, "index.yaml")
	rootIndex, err := readYAMLMapFile(rootIndexPath)
	if err != nil {
		return err
	}

	phaseIDs := []string{}
	phases, _ := rootIndex["phases"].([]interface{})
	for _, raw := range phases {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		idValue, ok := entry["id"].(string)
		if !ok || strings.TrimSpace(idValue) == "" {
			continue
		}
		phaseIDs = append(phaseIDs, idValue)
	}
	nextPhaseID := models.NextPhaseID(phaseIDs)
	nextPhaseNumber := idSuffixNumber(nextPhaseID, "P")
	phaseDirName := fmt.Sprintf("%s-%s", models.NumberedDirectoryName(nextPhaseNumber), models.Slugify(title, models.DirectoryNameWidth*15))
	phaseDir := filepath.Join(dataDir, phaseDirName)
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		return fmt.Errorf("failed to create phase directory: %w", err)
	}

	phaseIndexPath := filepath.Join(phaseDir, "index.yaml")
	phaseIndexData := map[string]any{
		"id":             nextPhaseID,
		"name":           title,
		"status":         string(models.StatusPending),
		"locked":         false,
		"weeks":          weeks,
		"estimate_hours": estimate,
		"complexity":     "medium",
		"depends_on":     dependsOn,
		"milestones":     []any{},
	}
	if err := writeYAMLMapFile(phaseIndexPath, phaseIndexData); err != nil {
		return err
	}

	appendToList(rootIndex, "phases", map[string]interface{}{
		"id":             nextPhaseID,
		"name":           title,
		"path":           phaseDirName,
		"status":         string(models.StatusPending),
		"weeks":          weeks,
		"estimate_hours": estimate,
		"priority":       priority,
		"depends_on":     dependsOn,
		"description":    description,
		"locked":         false,
	})
	if err := writeYAMLMapFile(rootIndexPath, rootIndex); err != nil {
		return err
	}
	fmt.Printf("%s %s\n", styleSuccess("Created phase:"), styleSuccess(nextPhaseID))
	phaseRelPath := filepath.ToSlash(filepath.Join(phaseDirName, "index.yaml"))
	fmt.Printf("%s %s/%s\n", styleSubHeader("File:"), styleMuted(filepath.Base(dataDir)), styleMuted(phaseRelPath))
	printNextCommands(
		"backlog show "+nextPhaseID,
		"backlog add-milestone "+nextPhaseID+" --title \"<milestone title>\"",
	)
	return nil
}

func runSet(args []string, metadata *gitAutoCommitMetadata) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdSet)
		return nil
	}

	allowed := map[string]bool{
		"--status":      true,
		"--priority":    true,
		"--complexity":  true,
		"--estimate":    true,
		"--title":       true,
		"--depends-on":  true,
		"--tags":        true,
		"--reason":      true,
		"--body":        true,
		"-b":            true,
		"--append-body": true,
		"--help":        true,
		"-h":            true,
	}
	if err := validateAllowedFlagsForUsage(commands.CmdSet, args, allowed); err != nil {
		return err
	}

	taskID := firstPositionalArg(args, map[string]bool{
		"--status":      true,
		"--priority":    true,
		"--complexity":  true,
		"--estimate":    true,
		"--title":       true,
		"--depends-on":  true,
		"--tags":        true,
		"--reason":      true,
		"--body":        true,
		"-b":            true,
		"--append-body": true,
	})
	if taskID == "" {
		return printUsageError(commands.CmdSet, errors.New("set requires TASK_ID"))
	}
	if err := validateTaskID(taskID); err != nil {
		return printUsageError(commands.CmdSet, err)
	}

	statusRaw, hasStatus := parseOptionWithPresence(args, "--status")
	priorityRaw, hasPriority := parseOptionWithPresence(args, "--priority")
	complexityRaw, hasComplexity := parseOptionWithPresence(args, "--complexity")
	estimateRaw, hasEstimate := parseOptionWithPresence(args, "--estimate")
	title, hasTitle := parseOptionWithPresence(args, "--title")
	dependsOnRaw, hasDependsOn := parseOptionWithPresence(args, "--depends-on")
	tagsRaw, hasTags := parseOptionWithPresence(args, "--tags")
	reason := parseOption(args, "--reason")
	body, hasBody := parseOptionWithPresence(args, "--body")
	if !hasBody {
		body, hasBody = parseOptionWithPresence(args, "-b")
	}
	_, hasAppendBody := parseOptionWithPresence(args, "--append-body")
	if hasAppendBody && !hasBody {
		return printUsageError(commands.CmdSet, errors.New("--append-body requires --body"))
	}

	hasAny := hasStatus || hasPriority || hasComplexity || hasEstimate || hasTitle || hasDependsOn || hasTags || hasBody || hasAppendBody
	if !hasAny {
		return printUsageError(commands.CmdSet, errors.New("set requires at least one property flag"))
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	task := tree.FindTask(taskID)
	if task == nil {
		return fmt.Errorf("Task not found: %s", taskID)
	}
	if hasPriority {
		priority, err := models.ParsePriority(priorityRaw)
		if err != nil {
			return printUsageError(commands.CmdSet, err)
		}
		task.Priority = priority
	}
	if hasComplexity {
		complexity, err := models.ParseComplexity(complexityRaw)
		if err != nil {
			return printUsageError(commands.CmdSet, err)
		}
		task.Complexity = complexity
	}
	if hasEstimate {
		raw, err := strconv.ParseFloat(estimateRaw, 64)
		if err != nil {
			return printUsageError(commands.CmdSet, fmt.Errorf("invalid --estimate: %s", estimateRaw))
		}
		task.EstimateHours = raw
	}
	if hasTitle {
		task.Title = title
	}
	if hasDependsOn {
		dependsOn, err := parseDependencyIDs(dependsOnRaw)
		if err != nil {
			return printUsageError(commands.CmdSet, err)
		}
		task.DependsOn = dependsOn
	}
	if hasTags {
		task.Tags = parseCSV(tagsRaw)
	}
	if hasStatus {
		next, err := models.ParseStatus(statusRaw)
		if err != nil {
			return printUsageError(commands.CmdSet, err)
		}
		if err := applyTaskStatusTransition(task, next, reason); err != nil {
			return err
		}
	}

	bodyToWrite := body
	if hasBody && hasAppendBody {
		_, existingBody, warnings, missing, err := readTodoFrontmatter(task.ID, task.File)
		if err != nil {
			return printUsageError(commands.CmdSet, err)
		}
		printTodoFileWarnings(warnings)
		if missing {
			return printUsageError(
				commands.CmdSet,
				fmt.Errorf("Cannot append body for %s because the task file is missing.", task.ID),
			)
		}
		if existingBody == "" {
			bodyToWrite = body
		} else if strings.HasSuffix(existingBody, "\n\n") {
			bodyToWrite = existingBody + body
		} else if strings.HasSuffix(existingBody, "\n") {
			bodyToWrite = existingBody + "\n" + body
		} else {
			bodyToWrite = existingBody + "\n\n" + body
		}
	}

	if metadata != nil && metadata.id == "" {
		metadata.id = task.ID
		metadata.title = task.Title
	}

	if hasBody {
		if err := saveTaskState(*task, tree, bodyToWrite); err != nil {
			return err
		}
	} else if err := saveTaskState(*task, tree); err != nil {
		return err
	}
	fmt.Printf("%s %s\n", styleSuccess("Updated:"), styleSuccess(task.ID))
	printNextCommands("backlog show " + task.ID)
	return nil
}

func runUpdate(args []string) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdUpdate)
		return nil
	}
	if err := validateAllowedFlagsForUsage(commands.CmdUpdate, args, map[string]bool{
		"--reason": true,
		"--help":   true,
		"-h":       true,
	}); err != nil {
		return err
	}
	pos := []string{}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		pos = append(pos, arg)
	}
	if len(pos) == 0 {
		return printUsageError(commands.CmdUpdate, errors.New("update requires TASK_ID"))
	}
	if len(pos) == 1 {
		return printUsageError(commands.CmdUpdate, errors.New("update requires TASK_ID STATUS"))
	}
	taskID := pos[0]
	rawStatus := pos[1]
	reason := parseOption(args, "--reason")

	if err := validateTaskID(taskID); err != nil {
		return printUsageError(commands.CmdUpdate, err)
	}
	nextStatus, err := models.ParseStatus(rawStatus)
	if err != nil {
		return printUsageError(commands.CmdUpdate, err)
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	task := tree.FindTask(taskID)
	if task == nil {
		return fmt.Errorf("Task not found: %s", taskID)
	}
	if err := applyTaskStatusTransition(task, nextStatus, reason); err != nil {
		return err
	}
	if err := saveTaskState(*task, tree); err != nil {
		return err
	}
	fmt.Printf("%s %s -> %s\n", styleSuccess("Updated:"), styleSuccess(task.ID), styleStatusText(string(task.Status)))
	switch task.Status {
	case models.StatusDone:
		printNextCommands("backlog cycle")
	case models.StatusBlocked:
		printNextCommands("backlog why "+task.ID, "backlog blockers --suggest")
	default:
		printNextCommands("backlog show " + task.ID)
	}
	return nil
}

func runUndone(args []string, metadata *gitAutoCommitMetadata) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	itemID := firstPositionalArg(args, map[string]bool{})
	if itemID == "" {
		return errors.New("undone requires ITEM_ID")
	}
	if err := validateTaskID(itemID); err != nil {
		return err
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}

	if task := tree.FindTask(itemID); task != nil {
		resetTaskToPending(task)
		if err := saveTaskState(*task, tree); err != nil {
			return err
		}
		if metadata.id == "" {
			metadata.id = task.ID
			metadata.title = task.Title
		}
		fmt.Printf("%s %s\n", styleWarning("Marked not done:"), styleSuccess(task.ID))
		fmt.Printf("%s 1\n", styleWarning("Reset tasks:"))
		return nil
	}

	path, err := models.ParseTaskPath(itemID)
	if err != nil {
		return errors.New("undone supports only task, phase, milestone, or epic IDs")
	}

	switch path.Depth() {
	case 1:
		metadata.id = path.FullID()
		metadata.title = ""
		return setPhaseNotDone(path, tree)
	case 2:
		metadata.id = path.FullID()
		metadata.title = ""
		return setMilestoneNotDone(path, tree)
	case 3:
		metadata.id = path.FullID()
		metadata.title = ""
		return setEpicNotDone(path, tree)
	default:
		return errors.New("undone supports only task, phase, milestone, or epic IDs")
	}
}

func resetTaskToPending(task *models.Task) {
	task.Status = models.StatusPending
	task.ClaimedBy = ""
	task.ClaimedAt = nil
	task.StartedAt = nil
	task.CompletedAt = nil
	task.DurationMinutes = nil
	task.Reason = ""
}

func setPhaseNotDone(path models.TaskPath, tree models.TaskTree) error {
	phase := tree.FindPhase(path.FullID())
	if phase == nil {
		return fmt.Errorf("Phase not found: %s", path.FullID())
	}
	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}

	rootPath := filepath.Join(dataDir, "index.yaml")
	rootIndex, err := readYAMLMapFile(rootPath)
	if err != nil {
		return err
	}
	phases, ok := rootIndex["phases"].([]interface{})
	if ok {
		for _, entryRaw := range phases {
			entry, ok := entryRaw.(map[string]interface{})
			if !ok {
				continue
			}
			idValue := asString(entry["id"])
			if idValue == phase.ID {
				entry["status"] = string(models.StatusPending)
				break
			}
		}
	}
	if err := writeYAMLMapFile(rootPath, rootIndex); err != nil {
		return err
	}

	phaseIndexPath := filepath.Join(dataDir, phase.Path, "index.yaml")
	phaseIndex, err := readYAMLMapFile(phaseIndexPath)
	if err != nil {
		return err
	}
	phaseIndex["status"] = string(models.StatusPending)
	if milestoneEntries, ok := phaseIndex["milestones"].([]interface{}); ok {
		for _, raw := range milestoneEntries {
			msEntry, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			msEntry["status"] = string(models.StatusPending)
		}
	}
	if err := writeYAMLMapFile(phaseIndexPath, phaseIndex); err != nil {
		return err
	}

	resetCount := 0
	for _, milestone := range phase.Milestones {
		milestoneIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, "index.yaml")
		milestoneIndex, err := readYAMLMapFile(milestoneIndexPath)
		if err != nil {
			return err
		}
		milestoneIndex["status"] = string(models.StatusPending)
		if err := writeYAMLMapFile(milestoneIndexPath, milestoneIndex); err != nil {
			return err
		}
		milestoneEntry, ok := milestoneIndex["epics"].([]interface{})
		if ok {
			for _, raw := range milestoneEntry {
				epicEntry, ok := raw.(map[string]interface{})
				if !ok {
					continue
				}
				epicEntry["status"] = string(models.StatusPending)
			}
		}
		epicIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path)
		for _, epic := range milestone.Epics {
			epicIndex, err := readYAMLMapFile(filepath.Join(epicIndexPath, epic.Path, "index.yaml"))
			if err != nil {
				return err
			}
			epicIndex["status"] = string(models.StatusPending)
			if err := writeYAMLMapFile(filepath.Join(epicIndexPath, epic.Path, "index.yaml"), epicIndex); err != nil {
				return err
			}
			for i := range epic.Tasks {
				task := &epic.Tasks[i]
				resetTaskToPending(task)
				if err := saveTaskState(*task, tree); err != nil {
					return err
				}
				resetCount++
			}
		}
	}
	fmt.Printf("%s %s\n", styleWarning("Marked not done:"), styleSuccess(path.FullID()))
	fmt.Printf("%s %d\n", styleWarning("Reset tasks:"), resetCount)
	return nil
}

func setMilestoneNotDone(path models.TaskPath, tree models.TaskTree) error {
	milestone := tree.FindMilestone(path.FullID())
	if milestone == nil {
		return fmt.Errorf("Milestone not found: %s", path.FullID())
	}
	phase := tree.FindPhase(path.Phase)
	if phase == nil {
		return fmt.Errorf("Phase not found: %s", path.Phase)
	}
	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	phaseIndexPath := filepath.Join(dataDir, phase.Path, "index.yaml")
	phaseIndex, err := readYAMLMapFile(phaseIndexPath)
	if err != nil {
		return err
	}
	if entries, ok := phaseIndex["milestones"].([]interface{}); ok {
		for _, raw := range entries {
			entry, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if asString(entry["id"]) == path.Milestone {
				entry["status"] = string(models.StatusPending)
				break
			}
		}
	}
	if err := writeYAMLMapFile(phaseIndexPath, phaseIndex); err != nil {
		return err
	}

	milestoneIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, "index.yaml")
	milestoneIndex, err := readYAMLMapFile(milestoneIndexPath)
	if err != nil {
		return err
	}
	milestoneIndex["status"] = string(models.StatusPending)
	entries, ok := milestoneIndex["epics"].([]interface{})
	if ok {
		for _, raw := range entries {
			if entry, ok := raw.(map[string]interface{}); ok {
				entry["status"] = string(models.StatusPending)
			}
		}
	}
	if err := writeYAMLMapFile(milestoneIndexPath, milestoneIndex); err != nil {
		return err
	}

	resetCount := 0
	for _, epic := range milestone.Epics {
		epicIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, epic.Path, "index.yaml")
		epicIndex, err := readYAMLMapFile(epicIndexPath)
		if err != nil {
			return err
		}
		epicIndex["status"] = string(models.StatusPending)
		if err := writeYAMLMapFile(epicIndexPath, epicIndex); err != nil {
			return err
		}
		for i := range epic.Tasks {
			task := &epic.Tasks[i]
			resetTaskToPending(task)
			if err := saveTaskState(*task, tree); err != nil {
				return err
			}
			resetCount++
		}
	}
	fmt.Printf("%s %s\n", styleWarning("Marked not done:"), styleSuccess(path.FullID()))
	fmt.Printf("%s %d\n", styleWarning("Reset tasks:"), resetCount)
	return nil
}

func setEpicNotDone(path models.TaskPath, tree models.TaskTree) error {
	epic := tree.FindEpic(path.FullID())
	if epic == nil {
		return fmt.Errorf("Epic not found: %s", path.FullID())
	}
	phase := tree.FindPhase(path.Phase)
	if phase == nil {
		return fmt.Errorf("Phase not found: %s", path.Phase)
	}
	milestone := tree.FindMilestone(path.MilestoneID())
	if milestone == nil {
		return fmt.Errorf("Milestone not found: %s", path.MilestoneID())
	}

	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	milestoneIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, "index.yaml")
	milestoneIndex, err := readYAMLMapFile(milestoneIndexPath)
	if err != nil {
		return err
	}
	if entries, ok := milestoneIndex["epics"].([]interface{}); ok {
		for _, raw := range entries {
			entry, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if asString(entry["id"]) == path.Epic {
				entry["status"] = string(models.StatusPending)
				break
			}
		}
	}
	if err := writeYAMLMapFile(milestoneIndexPath, milestoneIndex); err != nil {
		return err
	}

	epicIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, epic.Path, "index.yaml")
	epicIndex, err := readYAMLMapFile(epicIndexPath)
	if err != nil {
		return err
	}
	epicIndex["status"] = string(models.StatusPending)
	if err := writeYAMLMapFile(epicIndexPath, epicIndex); err != nil {
		return err
	}

	for i := range epic.Tasks {
		task := &epic.Tasks[i]
		resetTaskToPending(task)
		if err := saveTaskState(*task, tree); err != nil {
			return err
		}
	}
	fmt.Printf("%s %s\n", styleWarning("Marked not done:"), styleSuccess(path.FullID()))
	fmt.Printf("%s %d\n", styleWarning("Reset tasks:"), len(epic.Tasks))
	return nil
}

func applyTaskStatusTransition(task *models.Task, nextStatus models.Status, reason string) error {
	if err := models.ValidateStatusTransition(task.Status, nextStatus); err != nil {
		return err
	}

	switch nextStatus {
	case models.StatusBlocked, models.StatusRejected, models.StatusCancelled:
		if reason == "" {
			return fmt.Errorf("reason required when marking task %s as %s", task.ID, nextStatus)
		}
		task.Reason = reason
	case models.StatusPending:
		task.ClaimedBy = ""
		task.ClaimedAt = nil
		task.Reason = ""
	default:
		task.Reason = ""
	}

	task.Status = nextStatus

	if nextStatus == models.StatusDone && task.CompletedAt == nil {
		now := time.Now().UTC()
		task.CompletedAt = &now
	}

	return nil
}

func saveTaskState(task models.Task, tree models.TaskTree, bodyOverride ...string) error {
	if task.File == "" {
		return fmt.Errorf("Task %s has no file path", task.ID)
	}
	taskPath, err := resolveTaskFilePath(task.File)
	if err != nil {
		return err
	}

	frontmatter, body, warnings, missing, err := readTodoFrontmatter(task.ID, taskPath)
	if err != nil {
		return err
	}
	if missing {
		return fmt.Errorf("Task file missing for %s: %s", task.ID, taskPath)
	}
	printTodoFileWarnings(warnings)
	frontmatter["title"] = task.Title
	frontmatter["status"] = string(task.Status)
	frontmatter["estimate_hours"] = task.EstimateHours
	frontmatter["complexity"] = string(task.Complexity)
	frontmatter["priority"] = string(task.Priority)
	frontmatter["depends_on"] = task.DependsOn
	frontmatter["tags"] = task.Tags
	if strings.TrimSpace(task.ClaimedBy) != "" {
		frontmatter["claimed_by"] = task.ClaimedBy
	} else {
		delete(frontmatter, "claimed_by")
	}
	if task.ClaimedAt != nil {
		frontmatter["claimed_at"] = formatTimeForTodo(task.ClaimedAt)
	} else {
		delete(frontmatter, "claimed_at")
	}
	frontmatter["started_at"] = formatTimeForTodo(task.StartedAt)
	frontmatter["completed_at"] = formatTimeForTodo(task.CompletedAt)
	if task.Reason != "" {
		frontmatter["reason"] = task.Reason
	} else {
		delete(frontmatter, "reason")
	}
	if task.DurationMinutes != nil {
		frontmatter["duration_minutes"] = *task.DurationMinutes
	} else {
		delete(frontmatter, "duration_minutes")
	}
	if len(bodyOverride) > 0 {
		body = bodyOverride[0]
	}

	serialized, err := yaml.Marshal(frontmatter)
	if err != nil {
		return err
	}
	if err := os.WriteFile(taskPath, []byte(fmt.Sprintf("---\n%s---\n%s", string(serialized), body)), 0o644); err != nil {
		return err
	}
	return writeTaskIndex(task, tree)
}

func readTodoFrontmatter(taskID, taskFile string) (map[string]interface{}, string, []string, bool, error) {
	taskFilePath := taskFile
	if !filepath.IsAbs(taskFilePath) {
		dataDir, err := ensureDataRoot()
		if err != nil {
			return map[string]interface{}{}, "", nil, false, err
		}
		taskFilePath = filepath.Join(dataDir, taskFilePath)
	}
	raw, err := os.ReadFile(taskFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]interface{}{}, "", []string{
				fmt.Sprintf("Task file missing for %s: %s", taskID, taskFilePath),
			}, true, nil
		}
		return map[string]interface{}{}, "", []string{
			fmt.Sprintf("Task file for %s is not readable: %s", taskID, err),
		}, false, nil
	}

	warnings := []string{}
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		warnings = append(warnings, fmt.Sprintf("Invalid frontmatter format in %s: missing opening `---` marker.", taskID))
		return map[string]interface{}{}, string(raw), warnings, false, nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		warnings = append(warnings, fmt.Sprintf("Invalid frontmatter format in %s: missing closing `---` marker.", taskID))
		return map[string]interface{}{}, string(raw), warnings, false, nil
	}
	frontmatterText := strings.TrimSpace(strings.Join(lines[1:end], "\n"))
	body := ""
	if end+1 < len(lines) {
		body = strings.Join(lines[end+1:], "\n")
	}

	frontmatter := map[string]interface{}{}
	if frontmatterText != "" {
		parsed := map[string]interface{}{}
		if err := yaml.Unmarshal([]byte(frontmatterText), &parsed); err != nil {
			warnings = append(warnings, fmt.Sprintf("Invalid task file YAML in %s: %s", taskID, err))
			return map[string]interface{}{}, body, warnings, false, nil
		}
		frontmatter = parsed
	} else if taskID == "" {
		frontmatter = map[string]interface{}{}
	}

	// If frontmatter has an id field, it should match the task identifier when known.
	if taskID != "" {
		parsedID := asString(frontmatter["id"])
		if parsedID != "" && strings.TrimSpace(parsedID) != strings.TrimSpace(taskID) {
			warnings = append(warnings, fmt.Sprintf("Frontmatter ID mismatch in %s: %s", taskID, parsedID))
		}
	}
	return frontmatter, body, warnings, false, nil
}

func printTodoFileWarnings(warnings []string) {
	for _, warning := range warnings {
		if strings.TrimSpace(warning) == "" {
			continue
		}
		fmt.Printf("%s %s\n", styleWarning("Warning:"), styleMuted(warning))
	}
}

func writeTaskIndex(task models.Task, tree models.TaskTree) error {
	shortID := task.ID
	if strings.Count(task.ID, ".") >= 1 {
		shortID = task.ID[strings.LastIndex(task.ID, ".")+1:]
	}
	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}

	if strings.HasPrefix(task.File, "bugs/") {
		path := filepath.Join(dataDir, "bugs/index.yaml")
		index, err := readYAMLMapFile(path)
		if err != nil {
			return err
		}
		updateTaskIndexEntry(index["bugs"], shortID, task)
		return writeYAMLMapFile(path, index)
	}
	if strings.HasPrefix(task.File, "ideas/") {
		path := filepath.Join(dataDir, "ideas/index.yaml")
		index, err := readYAMLMapFile(path)
		if err != nil {
			return err
		}
		updateTaskIndexEntry(index["ideas"], shortID, task)
		return writeYAMLMapFile(path, index)
	}

	phase := tree.FindPhase(task.PhaseID)
	if phase == nil {
		return fmt.Errorf("Phase not found: %s", task.PhaseID)
	}
	milestone := tree.FindMilestone(task.MilestoneID)
	if milestone == nil {
		return fmt.Errorf("Milestone not found: %s", task.MilestoneID)
	}
	epic := tree.FindEpic(task.EpicID)
	if epic == nil {
		return fmt.Errorf("Epic not found: %s", task.EpicID)
	}
	phaseDir := filepath.Join(dataDir, phase.Path)
	if _, err := os.Stat(phaseDir); err != nil {
		return err
	}
	epicIndexPath := filepath.Join(phaseDir, milestone.Path, epic.Path, "index.yaml")
	index, err := readYAMLMapFile(epicIndexPath)
	if err != nil {
		return err
	}
	updateTaskIndexEntry(index["tasks"], shortID, task)
	return writeYAMLMapFile(epicIndexPath, index)
}

func updateTaskIndexEntry(raw any, taskShortID string, task models.Task) {
	taskEntries := toMapList(raw)
	for _, entry := range taskEntries {
		if asString(entry["id"]) != taskShortID {
			continue
		}
		entry["title"] = task.Title
		entry["status"] = string(task.Status)
		entry["estimate_hours"] = task.EstimateHours
		entry["complexity"] = string(task.Complexity)
		entry["priority"] = string(task.Priority)
		entry["depends_on"] = task.DependsOn
		entry["tags"] = task.Tags
		entry["file"] = filepath.Base(task.File)
	}
}

func toMapList(raw any) []map[string]interface{} {
	switch entries := raw.(type) {
	case []map[string]interface{}:
		return entries
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(entries))
		for _, item := range entries {
			entry, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			out = append(out, entry)
		}
		return out
	default:
		return []map[string]interface{}{}
	}
}

func formatTimeForTodo(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Format(time.RFC3339)
}

func asString(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func parseOptionWithPresence(args []string, key string) (string, bool) {
	for i, arg := range args {
		if arg == key {
			if i+1 >= len(args) {
				return "", true
			}
			return args[i+1], true
		}
		if strings.HasPrefix(arg, key+"=") {
			return strings.TrimPrefix(arg, key+"="), true
		}
	}
	return "", false
}

func parseFlag(args []string, flags ...string) bool {
	found := false
	for _, arg := range args {
		for _, key := range flags {
			if arg == key {
				found = true
				continue
			}
			if strings.HasPrefix(arg, key+"=") {
				rawValue := strings.TrimSpace(strings.ToLower(strings.TrimPrefix(arg, key+"=")))
				if rawValue == "" {
					found = true
					continue
				}
				parsed, err := parseBooleanFlag(rawValue, key)
				if err != nil {
					found = true
					continue
				}
				found = parsed
			}
		}
	}
	return found
}

func parseIntOptionWithDefault(args []string, fallback int, keys ...string) (int, error) {
	keyName := keys[0]
	raw, found := "", false
	for _, key := range keys {
		value, ok := parseOptionWithPresence(args, key)
		if ok {
			raw = value
			found = true
			break
		}
	}
	if !found || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, printUsageError(currentCommandForUsage, fmt.Errorf("invalid %s: %s", keyName, raw))
	}
	return value, nil
}

func runList(command string, args []string) error {
	if parseFlag(args, "--help", "-h") {
		printListHelp()
		return nil
	}
	return runListCore(command, args)
}

func runListCore(command string, args []string) error {
	printListUsageError := func(err error) error {
		printListHelp()
		fmt.Printf("%s\n", styleError(err.Error()))
		return err
	}

	if err := validateAllowedFlags(
		args,
		map[string]bool{
			"--status":             true,
			"--critical":           true,
			"--available":          true,
			"-a":                   true,
			"--complexity":         true,
			"--priority":           true,
			"--progress":           true,
			"--json":               true,
			"--all":                true,
			"--unfinished":         true,
			"--bugs":               true,
			"-b":                   true,
			"--ideas":              true,
			"-i":                   true,
			"--show-completed":     true,
			"--show-completed-aux": true,
			"--phase":              true,
			"--milestone":          true,
			"--epic":               true,
			"-h":                   true,
			"--help":               true,
		},
	); err != nil {
		return printListUsageError(err)
	}

	if parseFlag(args, "--help", "-h") {
		printListHelp()
		return nil
	}

	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	if command == commands.CmdLs {
		return runLsCore(args, dataDir)
	}

	scopeArgs := positionalArgs(args, map[string]bool{
		"--status":     true,
		"--available":  true,
		"--complexity": true,
		"--priority":   true,
		"--phase":      true,
		"--milestone":  true,
		"--epic":       true,
	})

	if parseFlag(args, "--critical") {
		// Reserved for historical parity with Python CLI; progress output remains explicit via --progress.
	}

	outputJSON := parseFlag(args, "--json")
	statusFilterRaw := parseOption(args, "--status")
	showAll := parseFlag(args, "--all")
	unfinished := parseFlag(args, "--unfinished")
	bugsOnly := parseFlag(args, "--bugs", "-b")
	ideasOnly := parseFlag(args, "--ideas", "-i")
	availableOnly := parseFlag(args, "--available", "-a")
	showCompletedAux := parseFlag(args, "--show-completed-aux")
	showProgress := parseFlag(args, "--progress")
	phaseScope := parseOption(args, "--phase")
	milestoneScope := parseOption(args, "--milestone")
	epicScope := parseOption(args, "--epic")
	complexityRaw := strings.TrimSpace(parseOption(args, "--complexity"))
	priorityRaw := strings.TrimSpace(parseOption(args, "--priority"))

	statusFilter := []string{}
	for _, item := range parseCSV(statusFilterRaw) {
		status, err := models.ParseStatus(item)
		if err != nil {
			return printListUsageError(err)
		}
		statusFilter = append(statusFilter, string(status))
	}

	var complexityFilter models.Complexity
	hasComplexityFilter := false
	if complexityRaw != "" {
		value, err := models.ParseComplexity(complexityRaw)
		if err != nil {
			return printListUsageError(err)
		}
		complexityFilter = value
		hasComplexityFilter = true
	}

	var priorityFilter models.Priority
	hasPriorityFilter := false
	if priorityRaw != "" {
		value, err := models.ParsePriority(priorityRaw)
		if err != nil {
			return printListUsageError(err)
		}
		priorityFilter = value
		hasPriorityFilter = true
	}

	scopeType := ""
	scopeInputs := []string{}
	if phaseScope != "" {
		scopeInputs = []string{phaseScope}
		scopeType = "phase"
	} else if milestoneScope != "" {
		scopeInputs = []string{milestoneScope}
		scopeType = "milestone"
	} else if epicScope != "" {
		scopeInputs = []string{epicScope}
		scopeType = "epic"
	} else {
		scopeInputs = scopeArgs
	}
	scoped := len(scopeInputs) > 0

	includeNormal := !bugsOnly && !ideasOnly
	includeBugs := bugsOnly || (!bugsOnly && !ideasOnly)
	includeIdeas := ideasOnly || (!bugsOnly && !ideasOnly)
	if scoped {
		includeNormal = true
		includeBugs = false
		includeIdeas = false
	}

	effectiveShowCompletedAux := showCompletedAux || (showAll && (bugsOnly || ideasOnly))
	if scoped {
		effectiveShowCompletedAux = false
	}

	tree, err := loader.New().Load("metadata", includeBugs, includeIdeas)
	if err != nil {
		return err
	}
	if !outputJSON {
		warnMissingTaskFiles(tree, dataDir)
	}
	calculator := critical_path.NewCriticalPathCalculator(tree, map[string]float64{})
	criticalPath, nextAvailable, err := calculator.CalculateForTaskDependencies()
	if err != nil {
		return err
	}
	if !outputJSON {
		warnNonTaskCycle(calculator)
	}
	availableTaskIDs := taskIDSet(calculator.FindAllAvailable())
	_ = nextAvailable

	var scopedPhases []models.Phase
	var scopedTasks []string
	scopeDepth := 0
	if scoped {
		scopedTaskSet := map[string]struct{}{}
		scopedPhaseSets := [][]models.Phase{}
		for _, rawScope := range scopeInputs {
			resolvedPhases, resolvedTasks, depth, scopeID := resolveListScope(tree, rawScope, scopeType)
			if len(resolvedPhases) == 0 {
				if strings.TrimSpace(scopeID) == "" {
					scopeID = rawScope
				}
				switch scopeType {
				case "phase":
					return fmt.Errorf("Phase not found: %s", scopeID)
				case "milestone":
					return fmt.Errorf("Milestone not found: %s", scopeID)
				case "epic":
					return fmt.Errorf("Epic not found: %s", scopeID)
				default:
					return fmt.Errorf("No list nodes found for path query: %s", scopeID)
				}
			}
			scopedPhaseSets = append(scopedPhaseSets, resolvedPhases)
			if depth > scopeDepth {
				scopeDepth = depth
			}
			for _, taskID := range resolvedTasks {
				if strings.TrimSpace(taskID) == "" {
					continue
				}
				scopedTaskSet[taskID] = struct{}{}
			}
		}
		scopedPhases = mergeScopedPhases(scopedPhaseSets)
		for _, taskID := range allTaskIDs(tree) {
			if _, ok := scopedTaskSet[taskID]; ok {
				scopedTasks = append(scopedTasks, taskID)
			}
		}
	}
	if !scoped {
		scopedTasks = allTaskIDs(tree)
	}

	taskMatches := func(task models.Task) bool {
		if len(statusFilter) > 0 && !containsString(statusFilter, string(task.Status)) {
			return false
		}
		if hasComplexityFilter && task.Complexity != complexityFilter {
			return false
		}
		if hasPriorityFilter && task.Priority != priorityFilter {
			return false
		}
		if scoped && !scopedTaskSetContains(task.ID, scopedTasks) {
			return false
		}
		if unfinished && isCompletedStatus(task.Status) {
			return false
		}
		return true
	}

	if showProgress {
		return renderListProgress(tree, criticalPath, scoped, scopedPhases, phaseScope, milestoneScope, epicScope, scopeType, scopeDepth, taskMatches)
	}

	if availableOnly {
		return renderListAvailable(tree, calculator, outputJSON, scopedTasks, taskMatches, criticalPath, availableTaskIDs, includeNormal, includeBugs, includeIdeas, effectiveShowCompletedAux)
	}

	if outputJSON {
		return renderListJSON(tree, scoped, scopedPhases, includeNormal, includeBugs, includeIdeas, showAll, unfinished, effectiveShowCompletedAux, taskMatches, criticalPath, nextAvailable, hasComplexityFilter, hasPriorityFilter, complexityFilter, priorityFilter, scopedTasks, statusFilter)
	}

	return renderListText(command, tree, scoped, scopedPhases, scopedTasks, scopeType, scopeDepth, taskMatches, criticalPath, showAll, availableTaskIDs)
}

func summarizeFixes(dataDir string) (int, int, error) {
	fixesDir := filepath.Join(dataDir, "fixes")
	indexPath := filepath.Join(fixesDir, "index.yaml")
	index, err := readYAMLMapFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	rawFixes, ok := index["fixes"].([]interface{})
	if !ok {
		return 0, 0, nil
	}

	total := 0
	done := 0
	for _, rawFix := range rawFixes {
		entry, ok := rawFix.(map[string]interface{})
		if !ok {
			continue
		}
		file := strings.TrimSpace(asString(entry["file"]))
		if file == "" {
			continue
		}

		fixPath := filepath.Join(fixesDir, file)
		frontmatter, _, warnings, _, err := readTodoFrontmatter("", fixPath)
		if err != nil || (len(frontmatter) == 0 && len(warnings) > 0) {
			continue
		}

		total++
		status, err := models.ParseStatus(asString(frontmatter["status"]))
		if err == nil && status == models.StatusDone {
			done++
		}
	}

	return done, total, nil
}

func runLsCore(args []string, dataDir string) error {
	if err := validateAllowedFlags(args, map[string]bool{}); err != nil {
		return err
	}

	positional := positionalArgs(args, map[string]bool{})
	includeAux := len(positional) == 0
	tree, err := loader.New().Load("metadata", includeAux, includeAux)
	if err != nil {
		return err
	}

	if len(positional) == 0 {
		if len(tree.Phases) == 0 {
			fmt.Println(styleWarning("No phases found."))
		} else {
			for _, phase := range tree.Phases {
				stats := getTaskStatsForPhase(phase)
				fmt.Printf(
					"%s: %s [%s] %d/%d tasks done (in_progress=%d, blocked=%d)\n",
					phase.ID,
					styleSubHeader(phase.Name),
					styleStatusText(string(phase.Status)),
					stats.done,
					stats.total,
					stats.inProgress,
					stats.blocked,
				)
			}
		}

		bugsDone := 0
		for _, bug := range tree.Bugs {
			if bug.Status == models.StatusDone {
				bugsDone++
			}
		}
		ideasDone := 0
		for _, idea := range tree.Ideas {
			if idea.Status == models.StatusDone {
				ideasDone++
			}
		}
		fixesDone, fixesTotal, err := summarizeFixes(dataDir)
		if err != nil {
			return err
		}

		fmt.Printf("Bugs (%d/%d)\n", bugsDone, len(tree.Bugs))
		fmt.Printf("Ideas (%d/%d)\n", ideasDone, len(tree.Ideas))
		fmt.Printf("Fixes (%d/%d)\n", fixesDone, fixesTotal)
		return nil
	}

	for _, scope := range positional {
		if err := validateLsScope(tree, scope); err != nil {
			return err
		}
	}
	for i, scope := range positional {
		if i > 0 {
			fmt.Println("")
		}
		if err := renderLsScope(tree, scope, dataDir); err != nil {
			return err
		}
	}
	return nil
}

func validateLsScope(tree models.TaskTree, scope string) error {
	if isBugLikeID(scope) || isIdeaLikeID(scope) {
		return fmt.Errorf("ls does not support bug/idea IDs. Use: backlog show %s", scope)
	}
	phasePath, err := models.ParseTaskPath(scope)
	if err != nil {
		if strings.Contains(scope, "--") {
			return validateScopeOrID(scope)
		}
		return fmt.Errorf("Invalid path format: %s", scope)
	}
	if phasePath.IsPhase() && tree.FindPhase(phasePath.FullID()) == nil {
		return formatNotFoundError(tree, "Phase", scope, "")
	}
	if phasePath.IsMilestone() {
		milestone := findMilestone(tree, scope)
		if milestone == nil {
			parent := phasePath.Parent()
			parentID := ""
			if parent != nil {
				parentID = parent.FullID()
			}
			return formatNotFoundError(tree, "Milestone", scope, parentID)
		}
	}
	if phasePath.IsEpic() {
		epic := findEpic(tree, scope)
		if epic == nil {
			parent := phasePath.Parent()
			parentID := ""
			if parent != nil {
				parentID = parent.FullID()
			}
			return formatNotFoundError(tree, "Epic", scope, parentID)
		}
	}
	if phasePath.IsTask() {
		task := tree.FindTask(scope)
		if task == nil {
			parent := phasePath.Parent()
			parentID := ""
			if parent != nil {
				parentID = parent.FullID()
			}
			return formatNotFoundError(tree, "Task", scope, parentID)
		}
	}
	return nil
}

func renderLsScope(tree models.TaskTree, scope string, dataDir string) error {
	phasePath, err := models.ParseTaskPath(scope)
	if err != nil {
		return err
	}
	if phasePath.IsPhase() {
		phase := tree.FindPhase(phasePath.FullID())
		if phase == nil {
			return nil
		}
		if len(phase.Milestones) == 0 {
			fmt.Printf("%s %s\n", styleWarning("Phase"), styleMuted(fmt.Sprintf("%s has no milestones.", scope)))
			return nil
		}
		for _, milestone := range phase.Milestones {
			stats := getTaskStatsForMilestone(milestone)
			fmt.Printf(
				"%s: %s [%s] %d/%d tasks done (in_progress=%d, blocked=%d)\n",
				milestone.ID,
				styleSubHeader(milestone.Name),
				styleStatusText(string(milestone.Status)),
				stats.done,
				stats.total,
				stats.inProgress,
				stats.blocked,
			)
		}
		return nil
	}

	if phasePath.IsMilestone() {
		milestone := findMilestone(tree, scope)
		if milestone == nil {
			return nil
		}
		if len(milestone.Epics) == 0 {
			fmt.Printf("%s %s\n", styleWarning("Milestone"), styleMuted(fmt.Sprintf("%s has no epics.", scope)))
			return nil
		}
		for _, epic := range milestone.Epics {
			stats := getTaskStatsForEpic(epic)
			fmt.Printf(
				"%s: %s [%s] %d/%d tasks done (in_progress=%d, blocked=%d)\n",
				epic.ID,
				styleSubHeader(epic.Name),
				styleStatusText(string(epic.Status)),
				stats.done,
				stats.total,
				stats.inProgress,
				stats.blocked,
			)
		}
		return nil
	}

	if phasePath.IsEpic() {
		epic := findEpic(tree, scope)
		if epic == nil {
			return nil
		}
		if len(epic.Tasks) == 0 {
			fmt.Printf("%s %s\n", styleWarning("Epic"), styleMuted(fmt.Sprintf("%s has no tasks.", scope)))
			return nil
		}
		for _, task := range epic.Tasks {
			fmt.Printf(
				"%s: %s [%s] %.0f\n",
				task.ID,
				task.Title,
				styleStatusText(string(task.Status)),
				task.EstimateHours,
			)
		}
		return nil
	}

	task := tree.FindTask(scope)
	if task != nil {
		return printTaskSummary(task, dataDir)
	}
	return nil
}

func warnMissingTaskFiles(tree models.TaskTree, dataDir string) {
	const warningLimit = 5
	missing := []models.Task{}
	for _, task := range findAllTasksInTree(tree) {
		if strings.TrimSpace(task.File) == "" {
			continue
		}
		taskFile := filepath.Join(dataDir, task.File)
		if _, err := os.Stat(taskFile); err != nil {
			missing = append(missing, task)
		}
	}
	if len(missing) == 0 {
		return
	}

	fmt.Printf("%s %d task file(s) referenced in index are missing.\n", styleWarning("Warning:"), len(missing))
	for i := 0; i < len(missing) && i < warningLimit; i++ {
		task := missing[i]
		fmt.Printf("  - %s: %s\n", styleSuccess(task.ID), filepath.Join(dataDir, task.File))
	}
	if len(missing) > warningLimit {
		fmt.Printf("  %s %d more\n", styleWarning("... and"), len(missing)-warningLimit)
	}
	fmt.Println()
}

func warnNonTaskCycle(calc *critical_path.CriticalPathCalculator) {
	cycle, err := calc.FindAnyCycle(false)
	if err != nil {
		return
	}
	if len(cycle) > 0 {
		fmt.Printf("%s non-task dependency cycle detected: %s\n", styleWarning("Warning:"), strings.Join(cycle, " -> "))
	}
}

func runShow(args []string, showNext bool, showLong bool, showAll bool) error {
	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	if err := validateAllowedFlagsForUsage(commands.CmdShow, args, map[string]bool{
		"--long": true,
		"--all":  true,
	}); err != nil {
		return err
	}
	ids := positionalArgs(args, map[string]bool{
		"--long": true,
		"--all":  true,
	})
	if len(ids) == 0 {
		ctx, err := taskcontext.GetCurrentTask(dataDir)
		if err != nil {
			return err
		}
		if strings.TrimSpace(ctx) == "" {
			fmt.Println(styleWarning("No task specified and no current working task set."))
			fmt.Println(styleMuted("Use 'work <task-id>' to set a working task."))
			return nil
		}
		ids = []string{ctx}
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}

	for idx, id := range ids {
		if idx > 0 {
			fmt.Println()
			fmt.Println(styleMuted(strings.Repeat("═", 60)))
		}

		if isBugLikeID(id) || isIdeaLikeID(id) {
			auxTask, err := findAuxiliaryTask(tree, id)
			if err != nil {
				return err
			}
			renderBugOrIdeaDetail(*auxTask, isIdeaLikeID(id) && auxTask.Status == models.StatusPending, dataDir, showNext)
			continue
		}
		scopePath, err := models.ParseTaskPath(id)
		if err != nil {
			fmt.Printf("%s %s\n", styleError("Invalid path format:"), styleMuted(id))
			printIDSuggestions(tree, id, "", 5)
			return fmt.Errorf("Invalid path format: %s", id)
		}

		if err := showScopedItem(tree, id, &scopePath, dataDir, showNext, showLong, showAll); err != nil {
			return err
		}
	}

	return nil
}

func taskFileHeader(taskID string) string {
	return fmt.Sprintf("%s [ %s ] %s", strings.Repeat("-", 20), taskID, strings.Repeat("-", 20))
}

func separatorPadding(previous []byte) string {
	if bytes.HasSuffix(previous, []byte("\n\n")) {
		return ""
	}
	if bytes.HasSuffix(previous, []byte("\n")) {
		return "\n"
	}
	return "\n\n"
}

func findFixedTaskByID(fixedID string) (*models.Task, error) {
	dataDir, err := ensureDataRoot()
	if err != nil {
		return nil, err
	}
	fixesDir := filepath.Join(dataDir, "fixes")
	indexPath := filepath.Join(fixesDir, "index.yaml")
	index, err := readYAMLMapFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	for _, raw := range asSlice(index["fixes"]) {
		entry, ok := raw.(map[string]interface{})
		if !ok || asString(entry["id"]) != fixedID {
			continue
		}
		filename := asString(entry["file"])
		if filename == "" {
			return nil, nil
		}
		fixPath := filepath.Join(fixesDir, filename)
		if _, err := os.Stat(fixPath); err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
		return &models.Task{
			ID:   fixedID,
			File: filepath.ToSlash(filepath.Join("fixes", filename)),
		}, nil
	}
	return nil, nil
}

func findIndexEntryByID(index map[string]interface{}, key string, ids ...string) (map[string]interface{}, bool) {
	for _, raw := range asSlice(index[key]) {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		entryID := asString(entry["id"])
		for _, id := range ids {
			if id != "" && entryID == id {
				return entry, true
			}
		}
	}
	return nil, false
}

func findCatNormalTaskByID(taskID string) (*models.Task, error) {
	taskPath, err := models.ParseTaskPath(taskID)
	if err != nil {
		return nil, err
	}
	if !taskPath.IsTask() {
		return nil, nil
	}

	dataDir, err := ensureDataRoot()
	if err != nil {
		return nil, err
	}
	rootIndex, err := readYAMLMapFile(filepath.Join(dataDir, "index.yaml"))
	if err != nil {
		return nil, err
	}
	phaseEntry, ok := findIndexEntryByID(rootIndex, "phases", taskPath.Phase)
	if !ok {
		return nil, nil
	}
	phaseDir := filepath.Join(dataDir, asString(phaseEntry["path"]))

	phaseIndex, err := readYAMLMapFile(filepath.Join(phaseDir, "index.yaml"))
	if err != nil {
		return nil, err
	}
	milestoneEntry, ok := findIndexEntryByID(phaseIndex, "milestones", taskPath.MilestoneID(), taskPath.Milestone)
	if !ok {
		return nil, nil
	}
	milestoneDir := filepath.Join(phaseDir, asString(milestoneEntry["path"]))

	milestoneIndex, err := readYAMLMapFile(filepath.Join(milestoneDir, "index.yaml"))
	if err != nil {
		return nil, err
	}
	epicEntry, ok := findIndexEntryByID(milestoneIndex, "epics", taskPath.EpicID(), taskPath.Epic)
	if !ok {
		return nil, nil
	}
	epicDir := filepath.Join(milestoneDir, asString(epicEntry["path"]))

	epicIndex, err := readYAMLMapFile(filepath.Join(epicDir, "index.yaml"))
	if err != nil {
		return nil, err
	}
	taskEntry, ok := findIndexEntryByID(epicIndex, "tasks", taskPath.TaskID(), taskPath.Task)
	if !ok {
		return nil, nil
	}
	filename := asString(taskEntry["file"])
	if filename == "" {
		return nil, nil
	}
	relativeFile, err := filepath.Rel(dataDir, filepath.Join(epicDir, filename))
	if err != nil {
		return nil, err
	}
	return &models.Task{
		ID:   taskID,
		File: filepath.ToSlash(relativeFile),
	}, nil
}

func runCat(args []string) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdCat)
		return nil
	}
	if err := validateAllowedFlagsForUsage(commands.CmdCat, args, map[string]bool{}); err != nil {
		return err
	}
	ids := positionalArgs(args, map[string]bool{})
	if len(ids) == 0 {
		return printUsageError(commands.CmdCat, errors.New("cat requires at least one TASK_ID"))
	}

	var tree *models.TaskTree
	loadTree := func() (models.TaskTree, error) {
		if tree != nil {
			return *tree, nil
		}
		loaded, err := loader.New().Load("metadata", true, true)
		if err != nil {
			return models.TaskTree{}, err
		}
		tree = &loaded
		return loaded, nil
	}

	var previousRaw []byte
	for index, id := range ids {
		scopePath, parseErr := models.ParseTaskPath(id)
		if parseErr != nil && !isBugLikeID(id) && !isIdeaLikeID(id) && !isFixedLikeID(id) {
			tree, err := loadTree()
			if err != nil {
				return err
			}
			fmt.Printf("%s %s\n", styleError("Invalid path format:"), styleMuted(id))
			printIDSuggestions(tree, id, "", 5)
			return fmt.Errorf("Invalid path format: %s", id)
		}
		scopeHint := ""
		if parseErr == nil {
			parent := scopePath.Parent()
			if parent != nil {
				scopeHint = parent.FullID()
			}
			if !scopePath.IsTask() {
				tree, err := loadTree()
				if err != nil {
					return err
				}
				return showNotFound(tree, "Task", id, scopeHint)
			}
		}

		var task *models.Task
		var err error
		if isFixedLikeID(id) {
			task, err = findFixedTaskByID(id)
			if err != nil {
				return err
			}
		} else if isBugLikeID(id) || isIdeaLikeID(id) {
			tree, err := loadTree()
			if err != nil {
				return err
			}
			task = findTask(tree, id)
		} else {
			task, err = findCatNormalTaskByID(id)
			if err != nil {
				return err
			}
		}
		if task == nil {
			tree, err := loadTree()
			if err != nil {
				return err
			}
			return showNotFound(tree, "Task", id, scopeHint)
		}
		taskPath, err := resolveTaskFilePath(task.File)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(taskPath)
		if err != nil {
			return err
		}

		if index > 0 {
			fmt.Print(separatorPadding(previousRaw))
			fmt.Println(taskFileHeader(task.ID))
		}
		fmt.Print(string(raw))
		previousRaw = raw
	}

	return nil
}

func runNext(args []string) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if err := validateAllowedFlags(args, map[string]bool{"--json": true}); err != nil {
		return err
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	cfg := map[string]float64{}
	calculator := critical_path.NewCriticalPathCalculator(tree, cfg)
	criticalPath, nextAvailable, err := calculator.Calculate()
	if err != nil {
		return err
	}
	if strings.TrimSpace(nextAvailable) == "" {
		fmt.Println(styleWarning("No available tasks found."))
		return nil
	}

	task := tree.FindTask(nextAvailable)
	if task == nil {
		return fmt.Errorf("Task not found: %s", nextAvailable)
	}

	if parseFlag(args, "--json") {
		payload := map[string]interface{}{
			"id":               task.ID,
			"title":            task.Title,
			"file":             task.File,
			"file_exists":      taskFileExists(task.File),
			"estimate_hours":   task.EstimateHours,
			"complexity":       string(task.Complexity),
			"status":           string(task.Status),
			"priority":         string(task.Priority),
			"on_critical_path": false,
			"grab_additional":  []string{},
		}
		for _, id := range criticalPath {
			if id == task.ID {
				payload["on_critical_path"] = true
				break
			}
		}
		grabbable, err := findGrabCandidates(*task, calculator, tree)
		if err == nil {
			additional := make([]string, 0, len(grabbable))
			for _, id := range grabbable {
				candidate := tree.FindTask(id)
				if candidate == nil || !taskFileExists(candidate.File) {
					continue
				}
				additional = append(additional, id)
			}
			payload["grab_additional"] = additional
		}
		raw, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}

	fmt.Printf("%s: %s\n", styleSuccess(task.ID), task.Title)
	return nil
}

func runLog(args []string) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if err := validateAllowedFlags(args, map[string]bool{
		"--limit": true,
		"--json":  true,
		"--bugs":  true,
		"-b":      true,
		"--ideas": true,
		"-i":      true,
	}); err != nil {
		return err
	}

	bugsOnly := parseFlag(args, "--bugs", "-b")
	ideasOnly := parseFlag(args, "--ideas", "-i")
	includeNormal := !bugsOnly && !ideasOnly
	includeBugs := bugsOnly || (!bugsOnly && !ideasOnly)
	includeIdeas := ideasOnly || (!bugsOnly && !ideasOnly)

	limit, err := parseIntOptionWithDefault(args, 20, "--limit")
	if err != nil {
		return err
	}
	if limit <= 0 {
		return fmt.Errorf("--limit must be a positive integer")
	}

	tree, err := loader.New().Load("metadata", includeBugs, includeIdeas)
	if err != nil {
		return err
	}

	events := collectLogEvents(tree, includeNormal, includeBugs, includeIdeas)
	if len(events) > limit {
		events = events[:limit]
	}

	if parseFlag(args, "--json") {
		payload := make([]logEventPayload, 0, len(events))
		for _, event := range events {
			payload = append(payload, logEventPayload{
				TaskID:    event.TaskID,
				Title:     event.Title,
				Event:     event.Event,
				Kind:      logEventKind(event.Event),
				Timestamp: event.Timestamp,
				Actor:     event.Actor,
			})
		}
		raw, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}

	if len(events) == 0 {
		fmt.Println(styleWarning("No recent activity found."))
		return nil
	}

	fmt.Println(styleHeader("Recent Activity Log"))
	for _, event := range events {
		actor := ""
		if event.Actor != nil {
			actor = " (" + *event.Actor + ")"
		}
		age := formatRelativeTime(event.Timestamp)
		fmt.Printf("%s [%s] %s %s%s\n", logEventIcon(event.Event), styleSubHeader(logEventKind(event.Event)), styleMuted(event.Event), event.TaskID, actor)
		fmt.Printf("  %s\n", styleMuted(event.Title))
		fmt.Printf("  %s (%s)\n\n", styleMuted(event.Timestamp.Format(time.RFC3339)), styleMuted(age))
	}
	return nil
}

func logEventKind(eventType string) string {
	if eventType == "added" {
		return "created"
	}
	return eventType
}

func logEventIcon(eventType string) string {
	return logEventIconStyled(eventType)
}

func formatRelativeTime(ts time.Time) string {
	delta := time.Since(ts)
	seconds := int(delta.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds ago", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%dm ago", minutes)
	}
	hours := minutes / 60
	if hours < 24 {
		return fmt.Sprintf("%dh ago", hours)
	}
	days := hours / 24
	if days < 7 {
		return fmt.Sprintf("%dd ago", days)
	}
	return fmt.Sprintf("%dw ago", days/7)
}

func shouldIncludeLogEvent(taskID string, includeNormal bool, includeBugs bool, includeIdeas bool) bool {
	if isBugLikeID(taskID) {
		return includeBugs && !includeNormal
	}
	if isIdeaLikeID(taskID) {
		return includeIdeas && !includeNormal
	}
	return includeNormal
}

func collectLogEvents(tree models.TaskTree, includeNormal bool, includeBugs bool, includeIdeas bool) []logEvent {
	events := []logEvent{}
	for _, task := range findAllTasksInTree(tree) {
		if !shouldIncludeLogEvent(task.ID, includeNormal, includeBugs, includeIdeas) {
			continue
		}

		taskFile, err := resolveTaskFilePath(task.File)
		if err != nil {
			taskFile = ""
		}

		if task.CompletedAt != nil {
			events = append(events, logEvent{
				TaskID:    task.ID,
				Title:     task.Title,
				Event:     "completed",
				Timestamp: *task.CompletedAt,
				Actor:     logEventActor(task),
			})
		}
		if task.StartedAt != nil {
			events = append(events, logEvent{
				TaskID:    task.ID,
				Title:     task.Title,
				Event:     "started",
				Timestamp: *task.StartedAt,
				Actor:     logEventActor(task),
			})
		}
		if task.ClaimedAt != nil {
			events = append(events, logEvent{
				TaskID:    task.ID,
				Title:     task.Title,
				Event:     "claimed",
				Timestamp: *task.ClaimedAt,
				Actor:     logEventActor(task),
			})
		}

		if task.ClaimedAt == nil && task.StartedAt == nil && task.CompletedAt == nil && taskFile != "" {
			info, err := os.Stat(taskFile)
			if err == nil {
				events = append(events, logEvent{
					TaskID:    task.ID,
					Title:     task.Title,
					Event:     "added",
					Timestamp: info.ModTime().UTC(),
					Actor:     nil,
				})
			}
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].Timestamp.Equal(events[j].Timestamp) {
			return events[i].Timestamp.After(events[j].Timestamp)
		}
		order := map[string]int{"added": 0, "claimed": 1, "started": 2, "completed": 3}
		return order[events[i].Event] > order[events[j].Event]
	})
	return events
}

func logEventActor(task models.Task) *string {
	if strings.TrimSpace(task.ClaimedBy) == "" {
		return nil
	}
	actor := task.ClaimedBy
	return &actor
}

func runTree(args []string) error {
	if err := validateAllowedFlagsForUsage(
		commands.CmdTree,
		args,
		map[string]bool{"--json": true, "--unfinished": true, "--show-completed-aux": true, "--details": true, "--depth": true},
	); err != nil {
		return err
	}

	depth, err := parseIntOptionWithDefault(args, 4, "--depth")
	if err != nil {
		return err
	}
	if depth <= 0 {
		return fmt.Errorf("--depth must be a positive integer")
	}

	outputJSON := parseFlag(args, "--json")
	unfinished := parseFlag(args, "--unfinished")
	showCompletedAux := parseFlag(args, "--show-completed-aux")
	showDetails := parseFlag(args, "--details")

	pathArgs := positionalArgs(args, map[string]bool{"--depth": true})
	pathQueries := []models.PathQuery{}
	for _, pathArg := range pathArgs {
		parsed, err := models.ParsePathQuery(pathArg)
		if err != nil {
			return printUsageError(commands.CmdTree, err)
		}
		pathQueries = append(pathQueries, parsed)
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}

	cfg := map[string]float64{}
	calculator := critical_path.NewCriticalPathCalculator(tree, cfg)
	criticalPath, nextAvailable, err := calculator.Calculate()
	if err != nil {
		return err
	}
	availableTaskIDs := taskIDSet(calculator.FindAllAvailable())
	isScopedPathQuery := len(pathQueries) > 0

	filteredPhases := tree.Phases
	if len(pathQueries) > 0 {
		scopedPhaseSets := make([][]models.Phase, 0, len(pathQueries))
		for _, pathQuery := range pathQueries {
			scopedPhaseSets = append(scopedPhaseSets, filterPhasesByPathQuery(tree.Phases, pathQuery))
		}
		filteredPhases = mergeScopedPhases(scopedPhaseSets)
	}

	if outputJSON {
		if unfinished {
			filteredPhases = filterUnfinishedPhases(filteredPhases)
		}
		output := mapTreePayload(filteredPhases, criticalPath, nextAvailable, depth, showDetails, unfinished, showCompletedAux)
		if !isScopedPathQuery {
			for _, bug := range tree.Bugs {
				if includeCompletionAux(bug.Status, unfinished, showCompletedAux) {
					output.Bugs = append(output.Bugs, treeTaskFromTask(bug, criticalPath))
				}
			}
			for _, idea := range tree.Ideas {
				if includeCompletionAux(idea.Status, unfinished, showCompletedAux) {
					output.Ideas = append(output.Ideas, treeTaskFromTask(idea, criticalPath))
				}
			}
		}
		raw, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}

	if unfinished {
		filteredPhases = filterUnfinishedPhases(filteredPhases)
	}

	bugsToShow := []models.Task{}
	ideasToShow := []models.Task{}
	if !isScopedPathQuery {
		for _, bug := range tree.Bugs {
			if includeCompletionAux(bug.Status, unfinished, showCompletedAux) {
				bugsToShow = append(bugsToShow, bug)
			}
		}
		for _, idea := range tree.Ideas {
			if includeCompletionAux(idea.Status, unfinished, showCompletedAux) {
				ideasToShow = append(ideasToShow, idea)
			}
		}
	}
	hasAux := len(bugsToShow) > 0 || len(ideasToShow) > 0

	for i, phase := range filteredPhases {
		isLast := i == len(filteredPhases)-1 && !hasAux
		lines := renderTreePhase(phase, isLast, "", criticalPath, availableTaskIDs, unfinished, showDetails, depth, 1)
		for _, line := range lines {
			fmt.Println(line)
		}
	}

	if len(pathQueries) > 0 && len(filteredPhases) == 0 {
		rawQueries := make([]string, 0, len(pathQueries))
		for _, query := range pathQueries {
			rawQueries = append(rawQueries, query.Raw)
		}
		fmt.Printf("%s: %s\n", styleError("No tree nodes found for path query"), strings.Join(rawQueries, ", "))
	}

	if len(bugsToShow) > 0 {
		bugsDone := 0
		for _, bug := range bugsToShow {
			if bug.Status == models.StatusDone {
				bugsDone++
			}
		}
		branch := "└── "
		continuation := "    "
		if len(ideasToShow) > 0 {
			branch = "├── "
			continuation = "│   "
		}
		fmt.Printf("%s%s\n", branch, styleSubHeader(fmt.Sprintf("Bugs (%d/%d)", bugsDone, len(bugsToShow))))
		for i, bug := range bugsToShow {
			isLast := i == len(bugsToShow)-1 && len(ideasToShow) == 0
			fmt.Printf("%s\n", renderTreeTaskLine(bug, continuation, isLast, criticalPath, availableTaskIDs, showDetails))
		}
	}

	if len(ideasToShow) > 0 {
		ideasDone := 0
		for _, idea := range ideasToShow {
			if idea.Status == models.StatusDone {
				ideasDone++
			}
		}
		fmt.Printf("└── %s\n", styleSubHeader(fmt.Sprintf("Ideas (%d/%d)", ideasDone, len(ideasToShow))))
		for i, idea := range ideasToShow {
			isLast := i == len(ideasToShow)-1
			fmt.Printf("  %s\n", strings.TrimSuffix(renderTreeTaskLine(idea, "", isLast, criticalPath, availableTaskIDs, showDetails), "\n"))
		}
	}

	return nil
}

func runDash(args []string) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if err := validateAllowedFlags(args, map[string]bool{"--json": true}); err != nil {
		return err
	}
	outputJSON := parseFlag(args, "--json")

	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	ctx, err := taskcontext.LoadContext(dataDir)
	if err != nil {
		return err
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}

	cfg := map[string]float64{}
	calculator := critical_path.NewCriticalPathCalculator(tree, cfg)
	criticalPath, nextAvailable, err := calculator.Calculate()
	if err != nil {
		return err
	}

	currentTaskID := strings.TrimSpace(ctx.CurrentTask)
	if currentTaskID == "" {
		currentTaskID = strings.TrimSpace(ctx.PrimaryTask)
	}
	currentTask := tree.FindTask(currentTaskID)
	currentAgent := strings.TrimSpace(ctx.Agent)
	if currentAgent == "" {
		currentAgent = "unknown"
	}

	allTasks := findAllTasksInTree(tree)
	normalTasks := findNormalTasksInTree(tree)

	totalInProgress, totalBlocked := 0, 0
	for _, task := range allTasks {
		switch task.Status {
		case models.StatusInProgress:
			totalInProgress++
		case models.StatusBlocked:
			totalBlocked++
		}
	}
	dashboardDone, dashboardTasks := 0, 0
	for _, task := range normalTasks {
		if task.Status == models.StatusDone {
			dashboardDone++
		}
	}
	for _, phase := range tree.Phases {
		stats := getTaskStatsForPhase(phase)
		dashboardTasks += stats.total
	}
	totalPct := 0.0
	if dashboardTasks > 0 {
		totalPct = (float64(dashboardDone) / float64(dashboardTasks)) * 100
	}

	phases := []dashPhaseProgressPayload{}
	completedPhases := []string{}
	for _, phase := range tree.Phases {
		stats := getTaskStatsForPhase(phase)
		if stats.total <= 0 {
			continue
		}
		pct := 0.0
		if stats.total > 0 {
			pct = (float64(stats.done) / float64(stats.total)) * 100
		}
		phases = append(phases, dashPhaseProgressPayload{
			ID:              phase.ID,
			Name:            phase.Name,
			Done:            stats.done,
			Total:           stats.total,
			InProgress:      stats.inProgress,
			Blocked:         stats.blocked,
			PercentComplete: pct,
		})
		if stats.done == stats.total && stats.total > 0 {
			completedPhases = append(completedPhases, fmt.Sprintf("%s (%d)", phase.ID, stats.total))
		}
	}

	remainingOnPath := []string{}
	remainingHours := 0.0
	for _, id := range criticalPath {
		task := tree.FindTask(id)
		if task == nil || task.Status == models.StatusDone {
			continue
		}
		remainingOnPath = append(remainingOnPath, id)
		remainingHours += task.EstimateHours
	}

	criticalPathPayload := dashCriticalPathPayload{
		Tasks:          remainingOnPath,
		RemainingCount: len(remainingOnPath),
		RemainingHours: remainingHours,
		AllComplete:    len(remainingOnPath) == 0,
	}
	if strings.TrimSpace(nextAvailable) != "" {
		criticalPathPayload.NextID = nextAvailable
		if nextTask := tree.FindTask(nextAvailable); nextTask != nil {
			criticalPathPayload.NextTitle = nextTask.Title
		}
	}

	activeSessions := 0
	sessions, sessionsErr := taskcontext.LoadSessions(dataDir)
	if sessionsErr == nil {
		now := time.Now().UTC()
		for _, session := range sessions {
			if session.Agent == "" || session.LastHeartbeat == "" {
				continue
			}
			lastSeen, parseErr := time.Parse(time.RFC3339, session.LastHeartbeat)
			if parseErr != nil {
				continue
			}
			if now.Sub(lastSeen) <= 15*time.Minute {
				activeSessions++
			}
		}
	}

	staleClaimsCount := len(staleClaims(allTasks, 60, 120))

	currentTaskPayload := (*dashCurrentTaskPayload)(nil)
	if strings.TrimSpace(currentTaskID) != "" {
		currentTaskPayload = &dashCurrentTaskPayload{
			ID:          currentTaskID,
			Agent:       currentAgent,
			WorkingTask: true,
			Found:       currentTask != nil,
		}
		if currentTask != nil {
			currentTaskPayload.Title = currentTask.Title
			currentTaskPayload.File = currentTask.File
			currentTaskPayload.FileExists = taskFileExists(currentTask.File)
		}
	}

	if outputJSON {
		payload := dashJSON{
			Agent:           currentAgent,
			CurrentTask:     currentTaskPayload,
			Overall:         dashOverallPayload{Done: dashboardDone, Total: dashboardTasks, InProgress: totalInProgress, Blocked: totalBlocked, Percent: totalPct},
			Phases:          phases,
			CompletedPhases: completedPhases,
			CriticalPath:    criticalPathPayload,
			Status: dashStatusPayload{
				InProgress:    totalInProgress,
				Blocked:       totalBlocked,
				StaleClaims:   staleClaimsCount,
				ActiveSession: activeSessions,
			},
		}
		raw, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}

	fmt.Println()
	fmt.Println(styleHeader("Current Task"))
	if currentTask != nil {
		fmt.Printf("  %s: %s\n", styleSuccess(currentTask.ID), currentTask.Title)
		fmt.Printf("  %s: %s\n", styleSubHeader("Agent"), currentAgent)
		if !taskFileExists(currentTask.File) {
			fmt.Printf("  %s: %s\n", styleWarning("Task file missing"), currentTask.File)
		}
	} else if strings.TrimSpace(currentTaskID) != "" {
		fmt.Printf("  Working task %s not found\n", styleWarning(currentTaskID))
	} else {
		fmt.Println(styleMuted("  No current working task set."))
	}
	fmt.Println()

	fmt.Println(styleSubHeader("Progress:"))
	fmt.Printf("  Total: %s %5.1f%% (%d/%d)\n", makeProgressBar(dashboardDone, dashboardTasks), totalPct, dashboardDone, dashboardTasks)

	for _, phase := range phases {
		fmt.Printf("  %s: %s %5.1f%% (%d/%d)\n", styleSuccess(phase.ID), makeProgressBarWithStatus(phase.Done, phase.InProgress, phase.Blocked, phase.Total), phase.PercentComplete, phase.Done, phase.Total)
	}
	if len(completedPhases) > 0 {
		fmt.Printf("  %s: %s\n", styleSubHeader("Completed Phases"), strings.Join(completedPhases, ", "))
	}

	fmt.Println()
	fmt.Println(styleSubHeader("Critical Path:"))
	if len(remainingOnPath) == 0 {
		fmt.Println("  " + styleSuccess("✓ All critical path tasks complete"))
	} else {
		maxDisplay := 5
		show := len(remainingOnPath)
		if show > maxDisplay {
			show = maxDisplay
		}
		fmt.Printf("  %s", strings.Join(remainingOnPath[:show], styleMuted(" -> ")))
		if len(remainingOnPath) > maxDisplay {
			fmt.Println(" -> ...")
		} else {
			fmt.Println()
		}
		fmt.Printf("  %s\n", styleMuted(fmt.Sprintf("%d tasks, ~%.0fh remaining", len(remainingOnPath), remainingHours)))
	}

	if strings.TrimSpace(nextAvailable) != "" {
		if nextTask := tree.FindTask(nextAvailable); nextTask != nil {
			fmt.Printf("  %s %s - %s\n", styleSuccess("Next:"), styleSuccess(nextTask.ID), styleSuccess(nextTask.Title))
		}
	}

	fmt.Println()
	fmt.Println(styleSubHeader("Status:"))
	fmt.Printf("  %s: %d\n", styleWarning("In progress"), totalInProgress)
	if totalBlocked > 0 {
		fmt.Printf("  %s: %d\n", styleError("Blocked"), totalBlocked)
	}
	if staleClaimsCount > 0 {
		fmt.Printf("  %s: %s\n", styleWarning("Stale claims"), styleSuccess(fmt.Sprintf("%d", staleClaimsCount)))
	}
	if activeSessions > 0 {
		fmt.Printf("  %s: %s\n", styleSubHeader("Active Sessions"), styleSuccess(fmt.Sprintf("%d", activeSessions)))
	}
	fmt.Println()

	return nil
}

func runAdmin(args []string) error {
	if err := validateAllowedFlags(args, map[string]bool{"--help": true, "--json": true}); err != nil {
		return err
	}
	positional := positionalArgs(args, map[string]bool{"--json": true})
	if parseFlag(args, "--help") {
		printUsageForCommand(commands.CmdAdmin)
		fmt.Println(styleWarning("The admin command is not implemented in the Go client."))
		fmt.Println(styleMuted("Available checks: check-file-sync, check-ids"))
		return nil
	}

	if len(positional) > 1 {
		return fmt.Errorf("admin accepts at most one action: check-file-sync | check-ids")
	}

	if len(positional) == 1 {
		action := normalizeCommand(positional[0])
		switch action {
		case "check-file-sync":
			return runAdminCheckFileSync(parseFlag(args, "--json"))
		case "check-ids":
			return runAdminCheckIDs(parseFlag(args, "--json"))
		default:
			return fmt.Errorf("unknown admin action: %s", action)
		}
	}

	if parseFlag(args, "--json") {
		payload := adminJSONPayload{
			Command:     "admin",
			Implemented: false,
			Message:     "admin command is not implemented in the Go client.",
			Guidance:    "Use `backlog dash` to inspect current project status.",
		}
		raw, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}
	fmt.Println(styleWarning("admin command is not implemented in the Go client."))
	fmt.Println(styleMuted("Available checks: check-file-sync, check-ids"))
	fmt.Println(styleMuted("Use `backlog admin check-file-sync` or `backlog admin check-ids` for available checks."))
	fmt.Println(styleMuted("Use `backlog admin --json` for machine-readable status."))
	fmt.Println(styleMuted("Use `backlog dash` to inspect current project status."))
	return nil
}

func runCI(args []string) error {
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdCI)
		return nil
	}
	if err := validateAllowedFlags(args, map[string]bool{"--help": true, "-h": true}); err != nil {
		return err
	}

	action := firstPositionalArg(args, map[string]bool{"--help": false, "-h": false})
	if action == "" {
		return errors.New("ci requires an action")
	}
	switch normalizeCommand(action) {
	case "validate-ids":
		return runCIValidateIDs(args)
	default:
		return fmt.Errorf("unknown ci action: %s", action)
	}
}

func runCIValidateIDs(args []string) error {
	ids := positionalArgs(args, map[string]bool{"--help": false, "-h": false})
	if len(ids) < 2 {
		return errors.New("validate-ids requires at least one TASK_ID")
	}
	ids = ids[1:] // skip action
	for _, id := range ids {
		if err := validateTaskIDForCI(id); err != nil {
			return err
		}
	}
	return nil
}

func runAdminCheckFileSync(jsonOutput bool) error {
	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	missing := []string{}
	for _, task := range findAllTasksInTree(tree) {
		if strings.TrimSpace(task.File) == "" {
			missing = append(missing, task.ID+" (missing path)")
			continue
		}
		taskPath := filepath.Join(dataDir, task.File)
		if _, err := os.Stat(taskPath); err != nil {
			missing = append(missing, task.ID)
		}
	}
	if jsonOutput {
		payload := map[string]any{
			"command":           "admin",
			"action":            "check-file-sync",
			"implemented":       true,
			"message":           "admin check-file-sync completed",
			"missing_task_ids":  missing,
			"all_files_present": len(missing) == 0,
			"guidance":          "Run 'backlog check' or open tasks to resolve missing files.",
		}
		raw, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}
	if len(missing) == 0 {
		fmt.Println(styleSuccess("All referenced task files are present."))
		return nil
	}
	fmt.Printf("%s %d task file(s) missing from disk.\n", styleWarning("Issue:"), len(missing))
	for _, taskID := range missing {
		fmt.Printf("  - %s\n", styleWarning(taskID))
	}
	return nil
}

func runAdminCheckIDs(jsonOutput bool) error {
	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	duplicates := []string{}
	emptyIDs := []string{}
	for _, task := range findAllTasksInTree(tree) {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			emptyIDs = append(emptyIDs, "<missing-id>")
			continue
		}
		if seen[id] {
			duplicates = append(duplicates, id)
			continue
		}
		seen[id] = true
	}
	issues := len(duplicates) + len(emptyIDs)
	if jsonOutput {
		payload := map[string]any{
			"command":     "admin",
			"action":      "check-ids",
			"implemented": true,
			"message":     "admin check-ids completed",
			"duplicates":  duplicates,
			"missing_ids": emptyIDs,
			"issue_count": issues,
			"task_count":  len(seen),
			"guidance":    "Resolve duplicated or missing IDs in index files before continuing.",
		}
		raw, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}
	if issues == 0 {
		fmt.Println(styleSuccess("Task IDs are consistent and unique."))
		return nil
	}
	if len(duplicates) > 0 {
		fmt.Printf("%s duplicate task IDs detected:\n", styleWarning("Issue:"))
		for _, value := range duplicates {
			fmt.Printf("  - %s\n", styleWarning(value))
		}
	}
	if len(emptyIDs) > 0 {
		fmt.Printf("%s missing task IDs detected (%d).\n", styleWarning("Issue:"), len(emptyIDs))
	}
	return nil
}

func runAgents(args []string) error {
	if err := validateAllowedFlags(args, map[string]bool{"--profile": true}); err != nil {
		return err
	}

	profile := strings.TrimSpace(parseOption(args, "--profile"))
	if profile == "" {
		profile = "all"
	}

	order := []string{}
	switch profile {
	case "all":
		order = []string{"short", "medium", "long"}
	case "short", "medium", "long":
		order = []string{profile}
	default:
		return fmt.Errorf("Invalid profile: %s", profile)
	}

	for i, key := range order {
		snippet, ok := agentsSnippets[key]
		if !ok {
			return fmt.Errorf("Invalid profile: %s", profile)
		}
		if i > 0 {
			fmt.Printf("\n%s\n\n", strings.Repeat("=", 72))
		}
		fmt.Print(snippet)
	}
	return nil
}

func runHowto(args []string) error {
	if err := validateAllowedFlags(args, map[string]bool{"--help": true, "--json": true}); err != nil {
		return err
	}

	if parseFlag(args, "--json") {
		payload := map[string]any{
			"name":          "backlog-howto",
			"skill_version": skills.BacklogHowtoSkillVersion,
			"content":       skills.BacklogHowtoSkillMD,
		}
		raw, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}

	fmt.Print(skills.BacklogHowtoSkillMD)
	return nil
}

func runLock(args []string, locked bool) error {
	if err := validateAllowedFlags(args, map[string]bool{}); err != nil {
		return err
	}

	commandName := "lock"
	if !locked {
		commandName = "unlock"
	}
	itemID := firstPositionalArg(args, map[string]bool{})
	if itemID == "" {
		return fmt.Errorf("%s requires ITEM_ID", commandName)
	}

	parts := strings.Split(itemID, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return errors.New("lock/unlock supports only phase, milestone, or epic IDs")
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	desired := locked

	canonicalID := ""
	switch len(parts) {
	case 1:
		phase := tree.FindPhase(parts[0])
		if phase == nil {
			return fmt.Errorf("Phase not found: %s", itemID)
		}
		canonicalID = phase.ID

		rootPath := filepath.Join(dataDir, "index.yaml")
		rootIndex, err := readYAMLMapFile(rootPath)
		if err != nil {
			return err
		}
		for _, raw := range asSlice(rootIndex["phases"]) {
			entry, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if tree.IDsMatch(asString(entry["id"]), phase.ID) || asString(entry["id"]) == parts[0] {
				entry["locked"] = desired
				break
			}
		}
		if err := writeYAMLMapFile(rootPath, rootIndex); err != nil {
			return err
		}

		phaseIndexPath := filepath.Join(dataDir, phase.Path, "index.yaml")
		if _, err := os.Stat(phaseIndexPath); err == nil {
			phaseIndex, err := readYAMLMapFile(phaseIndexPath)
			if err != nil {
				return err
			}
			phaseIndex["locked"] = desired
			if err := writeYAMLMapFile(phaseIndexPath, phaseIndex); err != nil {
				return err
			}
		}
	case 2:
		milestoneID := parts[0] + "." + parts[1]
		milestone := tree.FindMilestone(milestoneID)
		if milestone == nil {
			return fmt.Errorf("Milestone not found: %s", itemID)
		}
		phase := tree.FindPhase(milestone.PhaseID)
		if phase == nil {
			return fmt.Errorf("Phase not found for milestone: %s", itemID)
		}
		canonicalID = milestone.ID

		phaseIndexPath := filepath.Join(dataDir, phase.Path, "index.yaml")
		phaseIndex, err := readYAMLMapFile(phaseIndexPath)
		if err != nil {
			return err
		}
		for _, raw := range asSlice(phaseIndex["milestones"]) {
			entry, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if tree.IDsMatch(asString(entry["id"]), milestone.ID) || asString(entry["id"]) == parts[1] {
				entry["locked"] = desired
				break
			}
		}
		if err := writeYAMLMapFile(phaseIndexPath, phaseIndex); err != nil {
			return err
		}

		msIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, "index.yaml")
		if _, err := os.Stat(msIndexPath); err == nil {
			msIndex, err := readYAMLMapFile(msIndexPath)
			if err != nil {
				return err
			}
			msIndex["locked"] = desired
			if err := writeYAMLMapFile(msIndexPath, msIndex); err != nil {
				return err
			}
		}
	case 3:
		epicID := parts[0] + "." + parts[1] + "." + parts[2]
		epic := tree.FindEpic(epicID)
		if epic == nil {
			return fmt.Errorf("Epic not found: %s", itemID)
		}
		milestone := tree.FindMilestone(epic.MilestoneID)
		phase := tree.FindPhase(epic.PhaseID)
		if milestone == nil || phase == nil {
			return fmt.Errorf("Could not resolve parent paths for epic: %s", itemID)
		}
		canonicalID = epic.ID

		msIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, "index.yaml")
		msIndex, err := readYAMLMapFile(msIndexPath)
		if err != nil {
			return err
		}
		for _, raw := range asSlice(msIndex["epics"]) {
			entry, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if tree.IDsMatch(asString(entry["id"]), epic.ID) || asString(entry["id"]) == parts[2] {
				entry["locked"] = desired
				break
			}
		}
		if err := writeYAMLMapFile(msIndexPath, msIndex); err != nil {
			return err
		}

		epicIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, epic.Path, "index.yaml")
		if _, err := os.Stat(epicIndexPath); err == nil {
			epicIndex, err := readYAMLMapFile(epicIndexPath)
			if err != nil {
				return err
			}
			epicIndex["locked"] = desired
			if err := writeYAMLMapFile(epicIndexPath, epicIndex); err != nil {
				return err
			}
		}
	default:
		return errors.New("lock/unlock supports only phase, milestone, or epic IDs")
	}

	action := "Locked"
	if !locked {
		action = "Unlocked"
	}
	fmt.Printf("%s: %s\n", styleSuccess(action), styleSuccess(canonicalID))
	return nil
}

func runIdea(args []string, metadata *gitAutoCommitMetadata) error {
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdIdea)
		return nil
	}
	if err := validateAllowedFlagsForUsage(commands.CmdIdea, args, map[string]bool{
		"--title":      true,
		"-T":           true,
		"--estimate":   true,
		"-e":           true,
		"--complexity": true,
		"-c":           true,
		"--priority":   true,
		"-p":           true,
		"--depends-on": true,
		"-d":           true,
		"--tags":       true,
		"--simple":     true,
		"-s":           true,
		"--body":       true,
		"-b":           true,
		"--help":       true,
		"-h":           true,
	}); err != nil {
		return err
	}

	title := strings.TrimSpace(parseOption(args, "--title", "-T"))
	optionNamesWithValue := map[string]bool{
		"--title":      true,
		"-T":           true,
		"--priority":   true,
		"-p":           true,
		"--estimate":   true,
		"-e":           true,
		"--complexity": true,
		"-c":           true,
		"--depends-on": true,
		"-d":           true,
		"--tags":       true,
		"--body":       true,
		"-b":           true,
	}
	positional := positionalArgs(args, optionNamesWithValue)
	positionalTitle := strings.TrimSpace(strings.Join(positional, " "))

	estimate, err := parseFloatOptionWithDefault(args, 10, "--estimate", "-e")
	if err != nil {
		return err
	}
	complexity, err := parseComplexityOption(args, "--complexity", "-c")
	if err != nil {
		return err
	}

	priority := models.PriorityMedium
	if rawPriority := strings.TrimSpace(parseOption(args, "--priority", "-p")); rawPriority != "" {
		parsedPriority, err := models.ParsePriority(rawPriority)
		if err != nil {
			return err
		}
		priority = parsedPriority
	}

	dependsOn, err := parseDependencyIDs(parseOption(args, "--depends-on", "-d"))
	if err != nil {
		return err
	}
	tags := []string{"idea", "planning"}
	if rawTags, hasTags := parseOptionWithPresence(args, "--tags"); hasTags {
		tags = parseCSV(rawTags)
	}
	simple := parseFlag(args, "--simple", "-s")
	body := parseOption(args, "--body", "-b")

	if title == "" && positionalTitle != "" {
		title = positionalTitle
	}
	if title == "" {
		return printUsageError(commands.CmdIdea, errors.New("idea requires --title or IDEA_TEXT"))
	}
	if err := validateAtLeastTwoWords(commands.CmdIdea, title); err != nil {
		return err
	}

	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	ideasDir := filepath.Join(dataDir, "ideas")
	indexPath := filepath.Join(ideasDir, "index.yaml")
	next, err := nextAuxNumber(indexPath, "ideas", "I")
	if err != nil {
		return err
	}

	ideaID := fmt.Sprintf("I%03d", next)
	slug := models.Slugify(title, models.DirectoryNameWidth*15)
	if slug == "" {
		slug = "idea"
	}
	filename := fmt.Sprintf("%s-%s.todo", ideaID, slug)
	relFile := filepath.ToSlash(filepath.Join("ideas", filename))
	filePath := filepath.Join(ideasDir, filename)

	bodyText := strings.TrimSpace(body)
	if bodyText == "" {
		if simple {
			bodyText = fmt.Sprintf("\n%s\n", title)
		} else {
			bodyText = fmt.Sprintf(
				"\n# Original Idea\n\n%s\n\n## Planning Task (Equivalent of /plan-task)\n\n- Run `/plan-task %s` to decompose this idea into actionable work.\n- Confirm placement in the current backlog hierarchy before creating work items.\n\n## Ingest Plan Into backlog\n\n- Create implementation items with `backlog add` and related hierarchy commands (`backlog add-epic`, `backlog add-milestone`, `backlog add-phase`) as needed.\n- Create follow-up defects with `backlog bug` when bug-style work is identified.\n- Record all created IDs below and wire dependencies.\n\n## Created Work Items\n\n- Add created task IDs\n- Add created bug IDs (if any)\n\n## Completion Criteria\n\n- Idea has been decomposed into concrete backlog work items.\n- New items include clear acceptance criteria and dependencies.\n- This idea intake is updated with created IDs and marked done.\n",
				title,
				ideaID,
			)
		}
	}

	frontmatter := map[string]interface{}{
		"id":             ideaID,
		"title":          title,
		"status":         "pending",
		"estimate_hours": estimate,
		"complexity":     string(complexity),
		"priority":       string(priority),
		"depends_on":     dependsOn,
		"tags":           tags,
	}
	if err := writeTodoWithFrontmatter(filePath, frontmatter, bodyText); err != nil {
		return err
	}

	index, err := readYAMLMapFile(indexPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if index == nil {
		index = map[string]interface{}{"ideas": []interface{}{}}
	}
	appendToList(index, "ideas", map[string]interface{}{
		"id":   ideaID,
		"file": filename,
	})
	if err := writeYAMLMapFile(indexPath, index); err != nil {
		return err
	}

	fmt.Printf("%s %s\n", styleSuccess("Created idea:"), styleSuccess(ideaID))
	fmt.Printf("%s %s/%s\n", styleSubHeader("File:"), styleMuted(filepath.Base(dataDir)), styleMuted(relFile))
	*metadata = gitAutoCommitMetadata{
		id:    ideaID,
		title: title,
	}
	fmt.Println(styleWarning("IMPORTANT: This intake tracks planning work; run `/plan-task` on the idea and ingest resulting items with tasks commands."))
	printNextCommands(
		"backlog show "+ideaID,
		"backlog add-phase --title \"<phase title>\"",
		"backlog add-milestone <PHASE_ID> --title \"<milestone title>\"",
		"backlog add-epic <MILESTONE_ID> --title \"<epic title>\"",
		"backlog add <EPIC_ID> --title \"<task title>\"",
		"backlog bug --title \"<bug title>\"",
	)
	return nil
}

func runBug(args []string, metadata *gitAutoCommitMetadata) error {
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdBug)
		return nil
	}
	if err := validateAllowedFlagsForUsage(commands.CmdBug, args, map[string]bool{
		"--title":      true,
		"-T":           true,
		"--estimate":   true,
		"-e":           true,
		"--complexity": true,
		"-c":           true,
		"--priority":   true,
		"-p":           true,
		"--depends-on": true,
		"-d":           true,
		"--tags":       true,
		"--simple":     true,
		"-s":           true,
		"--body":       true,
		"-b":           true,
		"--help":       true,
		"-h":           true,
	}); err != nil {
		return err
	}

	title := strings.TrimSpace(parseOption(args, "--title", "-T"))
	optionNamesWithValue := map[string]bool{
		"--title":      true,
		"-T":           true,
		"--priority":   true,
		"-p":           true,
		"--estimate":   true,
		"-e":           true,
		"--complexity": true,
		"-c":           true,
		"--depends-on": true,
		"-d":           true,
		"--tags":       true,
		"--body":       true,
		"-b":           true,
	}
	positional := positionalArgs(args, optionNamesWithValue)
	positionalTitle := strings.TrimSpace(strings.Join(positional, " "))

	estimate, err := parseFloatOptionWithDefault(args, 1, "--estimate", "-e")
	if err != nil {
		return err
	}
	complexity, err := parseComplexityOption(args, "--complexity", "-c")
	if err != nil {
		return err
	}

	priority := models.PriorityHigh
	if rawPriority := strings.TrimSpace(parseOption(args, "--priority", "-p")); rawPriority != "" {
		parsedPriority, err := models.ParsePriority(rawPriority)
		if err != nil {
			return err
		}
		priority = parsedPriority
	}

	dependsOn, err := parseDependencyIDs(parseOption(args, "--depends-on", "-d"))
	if err != nil {
		return err
	}
	tags := parseCSV(parseOption(args, "--tags"))
	simple := parseFlag(args, "--simple", "-s")
	body := parseOption(args, "--body", "-b")

	if title == "" && positionalTitle != "" {
		title = positionalTitle
		simple = true
	}
	if title == "" {
		return printUsageError(commands.CmdBug, errors.New("bug requires --title or description text"))
	}
	if err := validateAtLeastTwoWords(commands.CmdBug, title); err != nil {
		return err
	}

	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	bugsDir := filepath.Join(dataDir, "bugs")
	indexPath := filepath.Join(bugsDir, "index.yaml")
	next, err := nextAuxNumber(indexPath, "bugs", "B")
	if err != nil {
		return err
	}

	bugID := fmt.Sprintf("B%03d", next)
	slug := models.Slugify(title, models.DirectoryNameWidth*15)
	if slug == "" {
		slug = "bug"
	}
	filename := fmt.Sprintf("%s-%s.todo", bugID, slug)
	filePath := filepath.Join(bugsDir, filename)

	bodyText := body
	if bodyText == "" {
		if simple {
			bodyText = fmt.Sprintf("\n%s\n", title)
		} else {
			bodyText = fmt.Sprintf(
				"\n# %s\n\n## Steps to Reproduce\n\n1. TODO: Add steps\n\n## Expected Behavior\n\nTODO: Describe expected behavior\n\n## Actual Behavior\n\nTODO: Describe actual behavior\n",
				title,
			)
		}
	}

	frontmatter := map[string]interface{}{
		"id":             bugID,
		"title":          title,
		"status":         "pending",
		"estimate_hours": estimate,
		"complexity":     string(complexity),
		"priority":       string(priority),
		"depends_on":     dependsOn,
		"tags":           tags,
	}
	if err := writeTodoWithFrontmatter(filePath, frontmatter, bodyText); err != nil {
		return err
	}

	index, err := readYAMLMapFile(indexPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if index == nil {
		index = map[string]interface{}{"bugs": []interface{}{}}
	}
	appendToList(index, "bugs", map[string]interface{}{
		"file": filename,
	})
	if err := writeYAMLMapFile(indexPath, index); err != nil {
		return err
	}

	fmt.Printf("%s %s\n", styleSuccess("Created bug:"), styleSuccess(bugID))
	relBugPath, err := filepath.Rel(dataDir, filePath)
	if err != nil {
		return fmt.Errorf("failed to compute bug relative path: %w", err)
	}
	fmt.Printf("%s %s/%s\n", styleSubHeader("File:"), styleMuted(filepath.Base(dataDir)), styleMuted(filepath.ToSlash(relBugPath)))
	if !simple && strings.TrimSpace(body) == "" {
		fmt.Println(styleWarning("IMPORTANT: You MUST fill in the .todo file that was created."))
	}
	*metadata = gitAutoCommitMetadata{
		id:    bugID,
		title: title,
	}
	printNextCommands(
		"backlog show "+bugID,
		"backlog claim "+bugID,
	)
	return nil
}

func runFixed(args []string) error {
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdFixed)
		return nil
	}
	if err := validateAllowedFlagsForUsage(commands.CmdFixed, args, map[string]bool{
		"--title":       true,
		"-T":            true,
		"--description": true,
		"--desc":        true,
		"--at":          true,
		"--tags":        true,
		"--body":        true,
		"-b":            true,
		"--help":        true,
		"-h":            true,
	}); err != nil {
		return err
	}

	title := strings.TrimSpace(parseOption(args, "--title", "-T"))
	optionNamesWithValue := map[string]bool{
		"--title":       true,
		"-T":            true,
		"--description": true,
		"--desc":        true,
		"--at":          true,
		"--tags":        true,
		"--body":        true,
		"-b":            true,
	}
	positional := positionalArgs(args, optionNamesWithValue)
	positionalTitle := strings.TrimSpace(strings.Join(positional, " "))
	if title == "" && positionalTitle != "" {
		title = positionalTitle
	}
	if title == "" {
		return printUsageError(commands.CmdFixed, errors.New("fixed requires --title or FIX_TEXT"))
	}
	if err := validateAtLeastTwoWords(commands.CmdFixed, title); err != nil {
		return err
	}

	description := strings.TrimSpace(parseOption(args, "--description", "--desc"))
	if description == "" {
		description = title
	}
	atRaw := strings.TrimSpace(parseOption(args, "--at"))
	timestamp, err := parseFixedTimestamp(atRaw)
	if err != nil {
		return err
	}

	tags := parseCSV(parseOption(args, "--tags"))
	body := parseOption(args, "--body", "-b")

	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	fixesDir := filepath.Join(dataDir, "fixes")
	indexPath := filepath.Join(fixesDir, "index.yaml")
	next, err := nextAuxNumber(indexPath, "fixes", "F")
	if err != nil {
		return err
	}

	fixedID := fmt.Sprintf("F%03d", next)
	monthDir := timestamp.UTC().Format("2006-01")
	slug := models.Slugify(title, models.DirectoryNameWidth*15)
	if slug == "" {
		slug = "fixed"
	}
	filename := fmt.Sprintf("%s-%s.todo", fixedID, slug)
	monthPath := filepath.Join(fixesDir, monthDir)
	filePath := filepath.Join(monthPath, filename)
	relativeFile := filepath.ToSlash(filepath.Join("fixes", monthDir, filename))

	frontmatter := map[string]interface{}{
		"id":             fixedID,
		"type":           "fixed",
		"title":          title,
		"description":    description,
		"status":         "done",
		"estimate_hours": 0.0,
		"complexity":     "low",
		"priority":       "low",
		"depends_on":     []string{},
		"tags":           tags,
		"created_at":     timestamp.UTC().Format(time.RFC3339),
		"completed_at":   timestamp.UTC().Format(time.RFC3339),
	}
	if err := writeTodoWithFrontmatter(filePath, frontmatter, body); err != nil {
		return err
	}

	index, err := readYAMLMapFile(indexPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if index == nil {
		index = map[string]interface{}{"fixes": []interface{}{}}
	}
	appendToList(index, "fixes", map[string]interface{}{
		"id":   fixedID,
		"file": filepath.ToSlash(filepath.Join(monthDir, filename)),
	})
	if err := writeYAMLMapFile(indexPath, index); err != nil {
		return err
	}

	fmt.Printf("%s %s\n", styleSuccess("Created fixed:"), styleSuccess(fixedID))
	fmt.Printf("%s %s/%s\n", styleSubHeader("File:"), styleMuted(filepath.Base(dataDir)), styleMuted(relativeFile))
	if len(tags) > 0 {
		fmt.Printf("%s %s\n", styleSubHeader("Tags:"), styleMuted(strings.Join(tags, ", ")))
	}
	return nil
}

func runMigrate(args []string) error {
	if err := validateAllowedFlags(args, map[string]bool{
		"--force":      true,
		"-f":           true,
		"--no-symlink": true,
	}); err != nil {
		return err
	}

	force := parseFlag(args, "--force", "-f")
	createSymlink := !parseFlag(args, "--no-symlink")

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	tasksPath := filepath.Join(cwd, config.TasksDir)
	backlogPath := filepath.Join(cwd, config.BacklogDir)

	if info, err := os.Stat(backlogPath); err == nil && info.IsDir() {
		if isSymlinkTo(tasksPath, backlogPath) {
			fmt.Println(styleSuccess("✓ Already migrated (.tasks is symlink to .backlog)"))
			return nil
		}
		if tasksInfo, err := os.Lstat(tasksPath); err == nil && tasksInfo.Mode()&os.ModeSymlink == 0 {
			if !force {
				return errors.New("Both .tasks/ and .backlog/ exist. Use --force to proceed.")
			}
			fmt.Println(styleSuccess("✓ Both directories exist (force mode - using .backlog/)"))
			return nil
		}
		fmt.Println(styleSuccess("✓ Already migrated (.backlog/ exists)"))
		return nil
	}

	if _, err := os.Stat(tasksPath); err != nil {
		if os.IsNotExist(err) {
			return errors.New("No .tasks/ directory found to migrate")
		}
		return err
	}

	if err := os.Rename(tasksPath, backlogPath); err != nil {
		return fmt.Errorf("Failed to rename .tasks/ to .backlog/: %w", err)
	}
	if createSymlink {
		if err := os.Symlink(config.BacklogDir, tasksPath); err != nil {
			return fmt.Errorf("Migrated but failed to create symlink: %w", err)
		}
	}

	updated := []string{}
	for _, mdPath := range migrationMarkdownFiles(cwd) {
		changed, err := updateMarkdownForMigration(mdPath)
		if err != nil {
			continue
		}
		if changed {
			updated = append(updated, filepath.Base(mdPath))
		}
	}
	sort.Strings(updated)

	message := "Migrated .tasks/ -> .backlog/"
	if createSymlink {
		message += " (with symlink)"
	}
	if len(updated) > 0 {
		message += "\nUpdated doc files: " + strings.Join(updated, ", ")
	}
	fmt.Printf("%s %s\n", styleSuccess("✓"), styleSuccess(message))
	return nil
}

func writeTodoWithFrontmatter(path string, frontmatter map[string]interface{}, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", filepath.Dir(path), err)
	}
	payload, err := yaml.Marshal(frontmatter)
	if err != nil {
		return fmt.Errorf("failed to serialize frontmatter for %s: %w", path, err)
	}
	content := fmt.Sprintf("---\n%s---\n%s", string(payload), body)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

func nextAuxNumber(indexPath, listKey, prefix string) (int, error) {
	index, err := readYAMLMapFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 1, nil
		}
		return 0, err
	}
	maxID := 0
	for _, raw := range asSlice(index[listKey]) {
		switch entry := raw.(type) {
		case map[string]interface{}:
			if n := prefixedNumber(asString(entry["id"]), prefix); n > maxID {
				maxID = n
			}
			if n := prefixedNumber(filepath.Base(asString(entry["file"])), prefix); n > maxID {
				maxID = n
			}
		case string:
			if n := prefixedNumber(filepath.Base(entry), prefix); n > maxID {
				maxID = n
			}
		}
	}
	return maxID + 1, nil
}

func prefixedNumber(value, prefix string) int {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return 0
	}
	upperRaw := strings.ToUpper(raw)
	upperPrefix := strings.ToUpper(prefix)
	if !strings.HasPrefix(upperRaw, upperPrefix) {
		return 0
	}
	digits := strings.Builder{}
	for _, r := range raw[len(prefix):] {
		if r < '0' || r > '9' {
			break
		}
		digits.WriteRune(r)
	}
	if digits.Len() == 0 {
		return 0
	}
	n, err := strconv.Atoi(digits.String())
	if err != nil {
		return 0
	}
	return n
}

func parseFixedTimestamp(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Now().UTC(), nil
	}
	normalized := strings.TrimSpace(raw)
	hasTimezone := regexp.MustCompile(`[zZ]|[+-]\d{2}:\d{2}$`).MatchString(normalized)
	if !hasTimezone {
		normalized += "Z"
	}
	timestamp, err := time.Parse(time.RFC3339, normalized)
	if err != nil {
		return time.Time{}, errors.New("fixed --at must be an ISO 8601 timestamp")
	}
	return timestamp.UTC(), nil
}

func migrationMarkdownFiles(root string) []string {
	candidates := []string{
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(root, "CLAUDE.md"),
	}
	if topLevel, err := filepath.Glob(filepath.Join(root, "*.md")); err == nil {
		candidates = append(candidates, topLevel...)
	}

	skip := map[string]bool{
		"README.md":       true,
		"PARITY_DIFFS.md": true,
	}
	seen := map[string]bool{}
	out := []string{}
	for _, candidate := range candidates {
		base := filepath.Base(candidate)
		if skip[base] {
			continue
		}
		if seen[candidate] {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	sort.Strings(out)
	return out
}

func updateMarkdownForMigration(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, nil
	}
	content := string(raw)
	if strings.Contains(content, migrationComment) {
		return false, nil
	}

	updated := content
	for _, command := range migrationKnownCommands {
		updated = strings.ReplaceAll(updated, "`tasks "+command, "`backlog "+command)
		updated = strings.ReplaceAll(updated, "    tasks "+command, "    backlog "+command)
		updated = strings.ReplaceAll(updated, "- tasks "+command, "- backlog "+command)
	}
	updated = strings.ReplaceAll(updated, "`tasks --", "`backlog --")
	updated = strings.ReplaceAll(updated, "`tasks [", "`backlog [")
	updated = strings.ReplaceAll(updated, "python -m tasks", "python -m backlog")
	updated = strings.ReplaceAll(updated, "./tasks.py", "./backlog.py")
	updated = strings.ReplaceAll(updated, "`tasks/`", "`backlog/`")
	updated = strings.ReplaceAll(updated, "\"tasks/", "\"backlog/")

	if updated == content {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(migrationComment+updated), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func isSymlinkTo(path string, target string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	linkTarget, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if linkTarget == target {
		return true
	}

	resolvedLink := linkTarget
	if !filepath.IsAbs(resolvedLink) {
		resolvedLink = filepath.Join(filepath.Dir(path), resolvedLink)
	}
	absLink, err := filepath.Abs(resolvedLink)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	return filepath.Clean(absLink) == filepath.Clean(absTarget)
}

func staleClaims(tasks []models.Task, warnAfterMinutes int, errorAfterMinutes int) []models.Task {
	now := time.Now().UTC()
	out := []models.Task{}
	for _, task := range tasks {
		if task.Status != models.StatusInProgress || task.ClaimedAt == nil {
			continue
		}
		minutes := now.Sub(*task.ClaimedAt).Minutes()
		if minutes >= float64(errorAfterMinutes) || minutes >= float64(warnAfterMinutes) {
			out = append(out, task)
		}
	}
	return out
}

func makeProgressBar(done, total int) string {
	return styleProgressBar(done, total)
}

func makeProgressBarWithStatus(done, inProgress, blocked, total int) string {
	return styleProgressBarWithStatus(done, inProgress, blocked, total)
}

func filterPhasesByPathQuery(phases []models.Phase, query models.PathQuery) []models.Phase {
	filtered := []models.Phase{}
	for _, phase := range phases {
		if filteredPhase := filterPhaseByPathQuery(phase, query); filteredPhase != nil {
			filtered = append(filtered, *filteredPhase)
		}
	}
	return filtered
}

func filterPhaseByPathQuery(phase models.Phase, query models.PathQuery) *models.Phase {
	milestones := []models.Milestone{}
	for _, milestone := range phase.Milestones {
		if filteredMilestone := filterMilestoneByPathQuery(milestone, query); filteredMilestone != nil {
			milestones = append(milestones, *filteredMilestone)
		}
	}
	if !query.Matches(phase.ID) && len(milestones) == 0 {
		return nil
	}
	out := phase
	out.Milestones = milestones
	return &out
}

func filterMilestoneByPathQuery(milestone models.Milestone, query models.PathQuery) *models.Milestone {
	epics := []models.Epic{}
	for _, epic := range milestone.Epics {
		if filteredEpic := filterEpicByPathQuery(epic, query); filteredEpic != nil {
			epics = append(epics, *filteredEpic)
		}
	}
	if !query.Matches(milestone.ID) && len(epics) == 0 {
		return nil
	}
	out := milestone
	out.Epics = epics
	return &out
}

func filterEpicByPathQuery(epic models.Epic, query models.PathQuery) *models.Epic {
	tasks := make([]models.Task, 0, len(epic.Tasks))
	for _, task := range epic.Tasks {
		if query.Matches(task.ID) {
			tasks = append(tasks, task)
		}
	}
	if !query.Matches(epic.ID) && len(tasks) == 0 {
		return nil
	}
	out := epic
	out.Tasks = tasks
	return &out
}

func filterUnfinishedPhases(phases []models.Phase) []models.Phase {
	filtered := []models.Phase{}
	for _, phase := range phases {
		if !hasUnfinishedMilestones(phase) {
			continue
		}
		filteredMilestones := []models.Milestone{}
		for _, milestone := range phase.Milestones {
			if !hasUnfinishedEpics(milestone) {
				continue
			}
			filteredMilestones = append(filteredMilestones, milestone)
		}
		if len(filteredMilestones) == 0 {
			continue
		}
		filteredPhase := phase
		filteredPhase.Milestones = filteredMilestones
		filtered = append(filtered, filteredPhase)
	}
	return filtered
}

func mapTreePayload(phases []models.Phase, criticalPath []string, nextAvailable string, maxDepth int, showDetails bool, unfinished bool, showCompletedAux bool) treePayload {
	_ = showDetails
	output := treePayload{
		CriticalPath:     criticalPath,
		NextAvailable:    nextAvailable,
		MaxDepth:         maxDepth,
		ShowDetails:      showDetails,
		UnfinishedOnly:   unfinished,
		ShowCompletedAux: showCompletedAux,
	}

	for _, phase := range phases {
		filteredMilestones := []treeMilestonePayload{}
		for _, milestone := range phase.Milestones {
			if unfinished && !hasUnfinishedEpics(milestone) {
				continue
			}
			filteredEpics := []treeEpicPayload{}
			for _, epic := range milestone.Epics {
				if unfinished && !hasUnfinishedTasks(epic.Tasks) {
					continue
				}
				filteredTasks := epic.Tasks
				if unfinished {
					filteredTasks = []models.Task{}
					for _, task := range epic.Tasks {
						if task.Status == models.StatusDone || task.Status == models.StatusCancelled || task.Status == models.StatusRejected {
							continue
						}
						filteredTasks = append(filteredTasks, task)
					}
				}
				treeEpic := treeEpicPayloadFromEpic(epic, filteredTasks, criticalPath)
				treeEpic.Tasks = filteredTasksPayload(filteredTasks, criticalPath)
				filteredEpics = append(filteredEpics, *treeEpic)
			}
			filteredMilestone := treeMilestonePayloadFromMilestone(milestone, filteredEpics)
			filteredMilestones = append(filteredMilestones, *filteredMilestone)
		}
		output.Phases = append(output.Phases, *treePhasePayloadFromPhase(phase, filteredMilestones))
	}

	return output
}

func treePhasePayloadFromPhase(phase models.Phase, milestones []treeMilestonePayload) *treePhasePayload {
	stats := getTaskStatsForPhase(phase)
	phaseMilestones := []treeMilestonePayload{}
	for _, milestone := range milestones {
		phaseMilestones = append(phaseMilestones, milestone)
	}
	return &treePhasePayload{
		ID:         phase.ID,
		Name:       phase.Name,
		Status:     string(phase.Status),
		Stats:      map[string]int{"done": stats.done, "total": stats.total, "in_progress": stats.inProgress, "blocked": stats.blocked},
		Milestones: phaseMilestones,
	}
}

func treeMilestonePayloadFromMilestone(milestone models.Milestone, epics []treeEpicPayload) *treeMilestonePayload {
	stats := getTaskStatsForMilestone(milestone)
	milestoneEpics := []treeEpicPayload{}
	for _, epic := range epics {
		milestoneEpics = append(milestoneEpics, epic)
	}
	return &treeMilestonePayload{
		ID:     milestone.ID,
		Name:   milestone.Name,
		Status: string(milestone.Status),
		Stats:  map[string]int{"done": stats.done, "total": stats.total, "in_progress": stats.inProgress, "blocked": stats.blocked},
		Epics:  milestoneEpics,
	}
}

func treeEpicPayloadFromEpic(epic models.Epic, tasks []models.Task, _ []string) *treeEpicPayload {
	items := []treeTask{}
	for _, task := range tasks {
		items = append(items, treeTaskFromTask(task, nil))
	}
	return &treeEpicPayload{
		ID:     epic.ID,
		Name:   epic.Name,
		Status: string(epic.Status),
		Tasks:  items,
	}
}

func filteredTasksPayload(tasks []models.Task, criticalPath []string) []treeTask {
	out := []treeTask{}
	for _, task := range tasks {
		out = append(out, treeTaskFromTask(task, criticalPath))
	}
	return out
}

func treeTaskFromTask(task models.Task, criticalPath []string) treeTask {
	claimedBy := task.ClaimedBy
	if claimedBy == "" {
		claimedBy = ""
	}
	pathTask := treeTask{
		ID:          task.ID,
		Title:       task.Title,
		Status:      string(task.Status),
		File:        task.File,
		FileExists:  taskFileExists(task.File),
		Estimate:    task.EstimateHours,
		Complexity:  string(task.Complexity),
		Priority:    string(task.Priority),
		DependsOn:   append([]string{}, task.DependsOn...),
		ClaimedBy:   nil,
		ClaimedAt:   task.ClaimedAt,
		StartedAt:   task.StartedAt,
		CompletedAt: task.CompletedAt,
		OnCritical:  false,
	}
	if claimedBy != "" {
		value := claimedBy
		pathTask.ClaimedBy = &value
	}
	if criticalPath != nil {
		for _, id := range criticalPath {
			if id == task.ID {
				pathTask.OnCritical = true
				break
			}
		}
	}
	return pathTask
}

func renderTreePhase(phase models.Phase, isLast bool, prefix string, criticalPath []string, availableTaskIDs map[string]struct{}, unfinished bool, showDetails bool, maxDepth int, currentDepth int) []string {
	stats := getTaskStatsForPhase(phase)
	branch := "├── "
	continuation := "│   "
	if isLast {
		branch = "└── "
		continuation = "    "
	}
	lines := []string{fmt.Sprintf("%s%s%s (%d/%d) [%s]", prefix, branch, styleSubHeader(phase.Name), stats.done, stats.total, styleStatusText(string(phase.Status)))}
	if currentDepth >= maxDepth {
		return lines
	}

	milestones := phase.Milestones
	if unfinished {
		milestones = []models.Milestone{}
		for _, milestone := range phase.Milestones {
			if hasUnfinishedEpics(milestone) {
				milestones = append(milestones, milestone)
			}
		}
	}

	for i, milestone := range milestones {
		milestoneIsLast := i == len(milestones)-1
		lines = append(lines, renderTreeMilestone(milestone, milestoneIsLast, prefix+continuation, criticalPath, availableTaskIDs, unfinished, showDetails, maxDepth, currentDepth+1)...)
	}
	return lines
}

func renderTreeMilestone(milestone models.Milestone, isLast bool, prefix string, criticalPath []string, availableTaskIDs map[string]struct{}, unfinished bool, showDetails bool, maxDepth int, currentDepth int) []string {
	stats := getTaskStatsForMilestone(milestone)
	branch := "├── "
	continuation := "│   "
	if isLast {
		branch = "└── "
		continuation = "    "
	}
	lines := []string{fmt.Sprintf("%s%s%s (%d/%d) [%s]", prefix, branch, styleSubHeader(milestone.Name), stats.done, stats.total, styleStatusText(string(milestone.Status)))}
	if currentDepth >= maxDepth {
		return lines
	}

	epics := milestone.Epics
	if unfinished {
		epics = []models.Epic{}
		for _, epic := range milestone.Epics {
			if hasUnfinishedTasks(epic.Tasks) {
				epics = append(epics, epic)
			}
		}
	}
	for i, epic := range epics {
		isLastEpic := i == len(epics)-1
		lines = append(lines, renderTreeEpic(epic, isLastEpic, prefix+continuation, criticalPath, availableTaskIDs, unfinished, showDetails, maxDepth, currentDepth+1)...)
	}
	return lines
}

func renderTreeEpic(epic models.Epic, isLast bool, prefix string, criticalPath []string, availableTaskIDs map[string]struct{}, unfinished bool, showDetails bool, maxDepth int, currentDepth int) []string {
	stats := getTaskStatsForEpic(epic)
	branch := "├── "
	continuation := "│   "
	if isLast {
		branch = "└── "
		continuation = "    "
	}
	lines := []string{fmt.Sprintf("%s%s%s (%d/%d) [%s]", prefix, branch, styleSubHeader(epic.Name), stats.done, stats.total, styleStatusText(string(epic.Status)))}
	if currentDepth >= maxDepth {
		return lines
	}

	tasks := epic.Tasks
	if unfinished {
		tasks = filterUnfinishedTasks(epic.Tasks)
	}
	for i, task := range tasks {
		isLastTask := i == len(tasks)-1
		lines = append(lines, renderTreeTaskLine(task, prefix+continuation, isLastTask, criticalPath, availableTaskIDs, showDetails))
	}
	return lines
}

func renderTreeTaskLine(task models.Task, prefix string, isLast bool, criticalPath []string, availableTaskIDs map[string]struct{}, showDetails bool) string {
	branch := "├── "
	if isLast {
		branch = "└── "
	}
	line := fmt.Sprintf("%s%s%s %s: %s", prefix, branch, checkboxIconForTask(task, availableTaskIDs), styleSuccess(task.ID), task.Title)
	if !showDetails {
		if isTaskOnCriticalPath(task.ID, criticalPath) {
			line += " " + styleCritical("★")
		}
		return line
	}
	details := []string{}
	if task.EstimateHours > 0 {
		details = append(details, fmt.Sprintf("(%.2fh)", task.EstimateHours))
	}
	if task.Status != "" {
		details = append(details, fmt.Sprintf("[%s]", styleStatusText(string(task.Status))))
	}
	if task.ClaimedBy != "" {
		details = append(details, "@"+task.ClaimedBy)
	}
	if len(task.DependsOn) > 0 {
		details = append(details, "depends:"+strings.Join(task.DependsOn, ","))
	}
	if isTaskOnCriticalPath(task.ID, criticalPath) {
		details = append(details, styleCritical("★"))
	}
	if len(details) > 0 {
		line += " " + strings.Join(details, " ")
	}
	return line
}

func formatTaskSummary(task models.Task, criticalPath []string, availableTaskIDs map[string]struct{}) string {
	critical := styleMuted("·")
	if isTaskOnCriticalPath(task.ID, criticalPath) {
		critical = styleCritical("★")
	}
	return fmt.Sprintf("%s %s %s %s", critical, checkboxIconForTask(task, availableTaskIDs), styleSuccess(task.ID), task.Title)
}

func formatTaskDetails(task models.Task) []string {
	details := []string{}
	stats := []string{}
	if task.EstimateHours > 0 {
		stats = append(stats, fmt.Sprintf("est %.2fh", task.EstimateHours))
	}
	if strings.TrimSpace(string(task.Complexity)) != "" {
		stats = append(stats, "complexity "+string(task.Complexity))
	}
	if strings.TrimSpace(string(task.Priority)) != "" {
		stats = append(stats, "priority "+string(task.Priority))
	}
	if len(stats) > 0 {
		details = append(details, strings.Join(stats, " | "))
	}
	if len(task.Tags) > 0 {
		details = append(details, fmt.Sprintf("Tags: %s", strings.Join(task.Tags, ", ")))
	}
	if strings.TrimSpace(task.ClaimedBy) != "" {
		details = append(details, fmt.Sprintf("Claimed by: %s", task.ClaimedBy))
	}
	if len(task.DependsOn) > 0 {
		details = append(details, fmt.Sprintf("Depends on: %s", strings.Join(task.DependsOn, ", ")))
	}

	formatted := make([]string, 0, len(details))
	for _, item := range details {
		formatted = append(formatted, styleMuted(item))
	}
	return formatted
}

func renderTaskActionCard(action string, task models.Task, agent, dataDir string, showContent bool) {
	fmt.Printf("\n%s %s - %s\n", styleSuccess(action), styleSuccess(task.ID), task.Title)
	fmt.Printf("  %s: %s\n", styleSubHeader("Status"), styleStatusText(string(task.Status)))
	fmt.Printf("  %s: %s\n", styleSubHeader("Agent"), agent)
	if task.ClaimedAt != nil {
		fmt.Printf("  %s: %s\n", styleSubHeader("Claimed At"), task.ClaimedAt.Format(time.RFC3339))
	}
	if task.EstimateHours > 0 {
		fmt.Printf("  %s: %.2fh\n", styleSubHeader("Estimate"), task.EstimateHours)
	}
	if strings.TrimSpace(string(task.Complexity)) != "" || strings.TrimSpace(string(task.Priority)) != "" {
		fmt.Printf("  %s: %s / %s\n", styleSubHeader("Sizing"), styleMuted(string(task.Priority)), styleMuted(string(task.Complexity)))
	}
	fmt.Printf("  %s: %s\n", styleSubHeader("File"), filepath.Join(dataDir, task.File))

	if !showContent {
		fmt.Printf("  %s\n", styleMuted("Task body preview suppressed via --no-content"))
		printTaskFileReadCommandsForTask(dataDir, task, false)
		printClaimCompletionGuidance(task.ID)
		return
	}

	_, body, warnings, missing, err := readTodoFrontmatter(task.ID, task.File)
	if err != nil {
		fmt.Printf("  %s\n", styleWarning("Unable to preview task body."))
		printTaskFileReadCommandsForTask(dataDir, task, false)
		printClaimCompletionGuidance(task.ID)
		return
	}
	printTodoFileWarnings(warnings)
	if missing {
		fmt.Printf("  %s\n", styleWarning("Unable to preview task body."))
		printTaskFileReadCommandsForTask(dataDir, task, false)
		printClaimCompletionGuidance(task.ID)
		return
	}
	body = strings.TrimSpace(body)
	if body == "" {
		fmt.Printf("  %s\n", styleMuted("Task body is empty."))
		printTaskFileReadCommandsForTask(dataDir, task, false)
		printClaimCompletionGuidance(task.ID)
		return
	}

	fmt.Printf("\n  %s\n", styleSubHeader("Task Body Preview"))
	taskPath := filepath.Join(dataDir, task.File)
	printTaskFileReadCommand(taskPath, true, taskFileReadPreviewLines)
	lines := strings.Split(body, "\n")
	maxLines := min(12, len(lines))
	for i := 0; i < maxLines; i++ {
		fmt.Printf("    %s\n", lines[i])
	}
	if len(lines) > maxLines {
		fmt.Printf("    %s\n", styleMuted(fmt.Sprintf("... (%d more lines)", len(lines)-maxLines)))
	}
	printTaskFileReadEOF()
	printClaimCompletionGuidance(task.ID)
}

func printClaimCompletionGuidance(taskID string) {
	fmt.Printf("  %s\n", styleMuted("-----=====-----"))
	fmt.Printf("  %s\n", styleSuccess("When you complete this task, mark it done by either:"))
	fmt.Printf("  - %s\n", styleSuccess("`bl cycle "+taskID+"`"))
	fmt.Printf("    %s\n", styleMuted("to mark done + claim next task in backlog (global),"))
	fmt.Printf("  - %s\n", styleSuccess("`bl done "+taskID+"`"))
	fmt.Printf("    %s\n", styleMuted("to mark as done and stop"))
	fmt.Printf("  %s\n", styleSuccess(fmt.Sprintf("To release this task: `bl unclaim %s`", taskID)))
}

func isTaskOnCriticalPath(taskID string, criticalPath []string) bool {
	for _, id := range criticalPath {
		if id == taskID {
			return true
		}
	}
	return false
}

func filterUnfinishedTasks(tasks []models.Task) []models.Task {
	filtered := []models.Task{}
	for _, task := range tasks {
		if isCompletedStatus(task.Status) {
			continue
		}
		filtered = append(filtered, task)
	}
	return filtered
}

func runPreview(args []string) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if err := validateAllowedFlags(args, map[string]bool{"--json": true}); err != nil {
		return err
	}

	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	cfg := map[string]float64{}
	calculator := critical_path.NewCriticalPathCalculator(tree, cfg)
	criticalPath, _, err := calculator.Calculate()
	if err != nil {
		return err
	}
	available := calculator.FindAllAvailable()
	if len(available) == 0 {
		fmt.Println(styleWarning("No available tasks found."))
		return nil
	}
	ordered := prioritizeTaskIDs(tree, criticalPath, available)

	normal := []previewTaskPayload{}
	bugs := []previewTaskPayload{}
	ideas := []previewTaskPayload{}
	for _, taskID := range ordered {
		task := tree.FindTask(taskID)
		if task == nil {
			continue
		}
		payload := buildPreviewTaskPayload(*task, criticalPath, calculator, tree, !parseFlag(args, "--json"), dataDir)
		if isBugLikeID(task.ID) {
			if len(bugs) < previewAuxLimit {
				bugs = append(bugs, payload)
			}
			continue
		}
		if isIdeaLikeID(task.ID) {
			if len(ideas) < previewAuxLimit {
				ideas = append(ideas, payload)
			}
			continue
		}
		if len(normal) < previewDisplayLimit {
			normal = append(normal, payload)
		}
		if len(normal) >= previewDisplayLimit && len(bugs) >= previewAuxLimit && len(ideas) >= previewAuxLimit {
			break
		}
	}

	if parseFlag(args, "--json") {
		output := map[string]interface{}{
			"critical_path":  criticalPath,
			"normal":         normal,
			"bugs":           bugs,
			"ideas":          ideas,
			"next_available": firstPlannedTask(ordered),
		}
		raw, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}

	printPreviewItems := func(label string, items []previewTaskPayload) {
		if len(items) == 0 {
			return
		}
		fmt.Printf("\n%s (%d)\n", styleSubHeader(label), len(items))
		for _, item := range items {
			critical := "  "
			if item.OnCritical {
				critical = "★ "
			}
			path := item.Path
			if strings.TrimSpace(path) == "" {
				path = item.File
			}
			criticalMarker := styleMuted(critical)
			fmt.Printf("%s%s %s: %s\n", criticalMarker, styleSuccess(item.ID), criticalMarker, item.Title)
			fmt.Printf("  %s: %s | %s: %.2fh | %s / %s\n", styleSubHeader("File"), styleMuted(path), styleSubHeader("Estimate"), item.EstimateHours, styleMuted(item.Priority), styleMuted(item.Complexity))
			if len(item.GrabAdditional) > 0 {
				fmt.Printf("  %s %s\n", styleMuted("If you run `backlog grab`, you would also get:"), styleMuted(strings.Join(item.GrabAdditional, ", ")))
			} else {
				fmt.Println(styleMuted("  If you run `backlog grab`, you get this task only."))
			}
		}
	}

	fmt.Println()
	fmt.Println(styleSubHeader("Preview available work:"))
	printPreviewItems("Normal Tasks", normal)
	printPreviewItems("Bugs", bugs)
	printPreviewItems("Ideas", ideas)
	if len(normal) > 0 || len(bugs) > 0 || len(ideas) > 0 {
		fmt.Println(styleMuted("\n★ = On critical path"))
	}
	return nil
}

func firstPlannedTask(taskIDs []string) string {
	if len(taskIDs) == 0 {
		return ""
	}
	for _, id := range taskIDs {
		if !isBugLikeID(id) && !isIdeaLikeID(id) {
			return id
		}
	}
	return taskIDs[0]
}

func buildPreviewTaskPayload(task models.Task, criticalPath []string, calc *critical_path.CriticalPathCalculator, tree models.TaskTree, includePath bool, dataDir string) previewTaskPayload {
	payload := previewTaskPayload{
		ID:            task.ID,
		Title:         task.Title,
		Status:        string(task.Status),
		File:          task.File,
		FileExists:    taskFileExists(task.File),
		EstimateHours: task.EstimateHours,
		Complexity:    string(task.Complexity),
		Priority:      string(task.Priority),
		OnCritical:    false,
	}
	for _, id := range criticalPath {
		if id == task.ID {
			payload.OnCritical = true
			break
		}
	}
	if includePath {
		payload.Path = filepath.Join(dataDir, task.File)
	}
	ids, err := findGrabCandidates(task, calc, tree)
	if err != nil {
		return payload
	}
	for _, id := range ids {
		candidate := tree.FindTask(id)
		if candidate == nil {
			continue
		}
		if !taskFileExists(candidate.File) {
			continue
		}
		payload.GrabAdditional = append(payload.GrabAdditional, id)
	}
	return payload
}

func findGrabCandidates(task models.Task, calc *critical_path.CriticalPathCalculator, _ models.TaskTree) ([]string, error) {
	if isBugLikeID(task.ID) {
		return calc.FindAdditionalBugs(task.ID, grabBugAdditionalMax)
	}
	return calc.FindSiblingTasks(task.ID, grabSiblingAdditionalMax)
}

func taskFileExists(raw string) bool {
	taskPath, err := resolveTaskFilePath(raw)
	if err != nil {
		return false
	}
	_, err = os.Stat(taskPath)
	return err == nil
}

func runGrab(args []string, metadata *gitAutoCommitMetadata) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if err := validateAllowedFlags(
		args,
		map[string]bool{
			"--agent":       true,
			"--scope":       true,
			"--single":      true,
			"--multi":       true,
			"--siblings":    true,
			"--no-siblings": true,
			"--count":       true,
			"--no-content":  true,
		},
	); err != nil {
		return err
	}

	taskIDs := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--agent" || arg == "--scope" || arg == "--count" {
			i++
			continue
		}
		if arg == "--single" || arg == "--multi" || arg == "--siblings" || arg == "--no-siblings" || arg == "--no-content" {
			continue
		}
		if strings.HasPrefix(arg, "--agent=") || strings.HasPrefix(arg, "--scope=") || strings.HasPrefix(arg, "--count=") {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		taskIDs = append(taskIDs, arg)
	}

	agent := parseOption(args, "--agent")
	if strings.TrimSpace(agent) == "" {
		agent = "cli-user"
	}
	single := parseFlag(args, "--single")
	multi := parseFlag(args, "--multi")
	includeSiblings := !parseFlag(args, "--no-siblings")
	noContent := parseFlag(args, "--no-content")
	scopeValues := []string{}
	for _, scope := range parseOptions(args, "--scope") {
		value := strings.TrimSpace(scope)
		if value != "" {
			scopeValues = append(scopeValues, value)
		}
	}
	count := grabSiblingAdditionalMax
	if rawCount := strings.TrimSpace(parseOption(args, "--count")); rawCount != "" {
		parsed, err := parseIntOptionWithDefault(args, grabSiblingAdditionalMax, "--count")
		if err != nil {
			return err
		}
		if parsed > 0 {
			count = parsed
		}
	}
	if single && len(taskIDs) == 0 {
		fmt.Println(styleWarning("`--single` is less efficient for agent flow; it only claims one task at a time. Consider dropping `--single` and using default `backlog grab` to grab a few tasks at once."))
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}

	if len(taskIDs) > 0 {
		claimed := []models.Task{}
		for _, id := range taskIDs {
			if err := validateTaskID(id); err != nil {
				return err
			}
			task := findTask(tree, id)
			if task == nil {
				return fmt.Errorf("Task not found: %s", id)
			}
			if _, err := resolveTaskFilePath(task.File); err != nil || !taskFileExists(task.File) {
				return fmt.Errorf("Cannot claim %s because the task file is missing.", task.ID)
			}
			if task.Status == models.StatusDone {
				return claimDoneError(*task)
			}
			if task.Status != models.StatusPending {
				return fmt.Errorf("Cannot claim task %s: task is %s, not pending", task.ID, task.Status)
			}
			if task.ClaimedBy != "" {
				return fmt.Errorf("Task %s is already claimed by %s", task.ID, task.ClaimedBy)
			}
			if err := claimTaskInTree(task, agent, time.Now().UTC(), tree); err != nil {
				return err
			}
			if metadata.id == "" {
				metadata.id = task.ID
				metadata.title = task.Title
			}
			if len(taskIDs) == 1 {
				renderTaskActionCard("✓ Claimed", *task, agent, dataDir, !noContent)
			} else {
				fmt.Printf("%s %s - %s\n", styleSuccess("✓ Claimed:"), task.ID, task.Title)
				if !noContent {
					for _, detail := range formatTaskDetails(*task) {
						fmt.Printf("  %s\n", detail)
					}
				}
				printTaskFileReadCommandsForTask(dataDir, *task, !noContent)
			}
			claimed = append(claimed, *task)
		}
		if len(claimed) > 1 {
			additional := make([]string, 0, len(claimed)-1)
			for _, t := range claimed[1:] {
				additional = append(additional, t.ID)
			}
			if err := taskcontext.SetMultiTaskContext(dataDir, agent, claimed[0].ID, additional); err != nil {
				return err
			}
		} else {
			if err := taskcontext.SetCurrentTask(dataDir, claimed[0].ID, agent); err != nil {
				return err
			}
		}
		fmt.Printf("%s %s\n", styleSubHeader("Working on:"), styleSuccess(claimed[0].ID))
		return nil
	}

	cfg := map[string]float64{}
	calculator := critical_path.NewCriticalPathCalculator(tree, cfg)
	criticalPath, nextAvailable, err := calculator.Calculate()
	if err != nil {
		return err
	}
	if strings.TrimSpace(nextAvailable) == "" {
		fmt.Println(styleWarning("No available tasks found."))
		return nil
	}

	if len(scopeValues) > 0 {
		matchesTreeScope := func(scope string) bool {
			if tree.FindPhase(scope) != nil {
				return true
			}
			if findMilestone(tree, scope) != nil {
				return true
			}
			if findEpic(tree, scope) != nil {
				return true
			}
			for _, task := range findAllTasksInTree(tree) {
				if strings.HasPrefix(task.ID, scope) {
					return true
				}
			}
			return false
		}
		for _, scope := range scopeValues {
			if !matchesTreeScope(scope) {
				return fmt.Errorf("No list nodes found for path query: %s", scope)
			}
		}

		available := calculator.FindAllAvailable()
		filtered := []string{}
		filteredSet := map[string]struct{}{}
		for _, id := range available {
			for _, scope := range scopeValues {
				if strings.HasPrefix(id, scope) {
					if _, ok := filteredSet[id]; !ok {
						filtered = append(filtered, id)
						filteredSet[id] = struct{}{}
					}
					break
				}
			}
		}
		filtered = prioritizeTaskIDs(tree, criticalPath, filtered)
		if len(filtered) == 0 {
			fmt.Printf("%s '%s'\n", styleWarning("No available tasks in scope"), styleMuted(strings.Join(scopeValues, ", ")))
			return nil
		}
		nextAvailable = filtered[0]
	}

	primary := tree.FindTask(nextAvailable)
	if primary == nil {
		return fmt.Errorf("Task not found: %s", nextAvailable)
	}
	if _, err := resolveTaskFilePath(primary.File); err != nil || !taskFileExists(primary.File) {
		return fmt.Errorf("Cannot claim %s because the task file is missing.", primary.ID)
	}
	if err := claimTaskInTree(primary, agent, time.Now().UTC(), tree); err != nil {
		return err
	}
	if metadata.id == "" {
		metadata.id = primary.ID
		metadata.title = primary.Title
	}

	additional := []models.Task{}
	if !single {
		candidateIDs := []string{}
		if multi {
			candidateIDs, err = findIndependentCandidates(tree, calculator, *primary, count)
		} else if includeSiblings {
			candidateIDs, err = findGrabCandidates(*primary, calculator, tree)
		}
		if err != nil {
			return err
		}

		additionalLimit := count
		if additionalLimit < 0 {
			additionalLimit = 0
		}
		for _, id := range candidateIDs {
			if len(additional) >= additionalLimit {
				break
			}
			task := findTask(tree, id)
			if task == nil {
				continue
			}
			if task.Status != models.StatusPending || task.ClaimedBy != "" {
				continue
			}
			if !taskFileExists(task.File) {
				continue
			}
			if err := claimTaskInTree(task, agent, time.Now().UTC(), tree); err != nil {
				return err
			}
			additional = append(additional, *task)
		}
	}

	if len(additional) > 0 {
		additionalIDs := make([]string, len(additional))
		for i, task := range additional {
			additionalIDs[i] = task.ID
		}
		if multi || isBugLikeID(primary.ID) {
			if err := taskcontext.SetMultiTaskContext(dataDir, agent, primary.ID, additionalIDs); err != nil {
				return err
			}
		} else {
			if err := taskcontext.SetSiblingTaskContext(dataDir, agent, primary.ID, additionalIDs); err != nil {
				return err
			}
		}
		fmt.Printf("%s %s - %s\n", styleSuccess("Grabbed:"), primary.ID, primary.Title)
		if !noContent {
			for _, detail := range formatTaskDetails(*primary) {
				fmt.Printf("  %s\n", detail)
			}
			for _, task := range additional {
				for _, detail := range formatTaskDetails(task) {
					fmt.Printf("    %s %s\n", styleMuted(task.ID), detail)
				}
			}
		}
		printTaskFileReadCommandsForTask(dataDir, *primary, !noContent)
		for _, task := range additional {
			printTaskFileReadCommandsForTask(dataDir, task, !noContent)
		}
		fmt.Printf("%s %s additional task(s): %s\n", styleSubHeader("Also grabbed"), styleSuccess(fmt.Sprintf("%d", len(additional))), styleMuted(strings.Join(additionalIDs, ", ")))
		return nil
	}

	if err := taskcontext.SetCurrentTask(dataDir, primary.ID, agent); err != nil {
		return err
	}
	fmt.Printf("%s %s - %s\n", styleSuccess("Grabbed:"), primary.ID, primary.Title)
	if !noContent {
		for _, detail := range formatTaskDetails(*primary) {
			fmt.Printf("  %s\n", detail)
		}
	}
	printTaskFileReadCommandsForTask(dataDir, *primary, !noContent)
	return nil
}

func prioritizeTaskIDs(tree models.TaskTree, criticalPath []string, taskIDs []string) []string {
	cpPos := map[string]int{}
	for i, id := range criticalPath {
		cpPos[id] = i
	}

	maxPos := int(^uint(0) >> 1)
	type rankedTask struct {
		id           string
		originalPos  int
		typeRank     int
		priorityRank int
		onCritical   int
		cpPos        int
	}

	ranked := make([]rankedTask, 0, len(taskIDs))
	for idx, id := range taskIDs {
		task := tree.FindTask(id)
		if task == nil {
			continue
		}
		ranked = append(ranked, rankedTask{
			id:           task.ID,
			originalPos:  idx,
			typeRank:     prioritizeTaskType(task.ID),
			priorityRank: prioritizeTaskPriority(task.Priority),
			onCritical:   boolToInt(containsTaskID(criticalPath, task.ID)),
			cpPos: func() int {
				if value, ok := cpPos[task.ID]; ok {
					return value
				}
				return maxPos
			}(),
		})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.typeRank != b.typeRank {
			return a.typeRank < b.typeRank
		}
		if a.priorityRank != b.priorityRank {
			return a.priorityRank < b.priorityRank
		}
		if a.onCritical != b.onCritical {
			return a.onCritical < b.onCritical
		}
		if a.cpPos != b.cpPos {
			return a.cpPos < b.cpPos
		}
		return a.originalPos < b.originalPos
	})

	out := make([]string, 0, len(ranked))
	for _, item := range ranked {
		out = append(out, item.id)
	}
	return out
}

func prioritizeTaskType(id string) int {
	if isBugLikeID(id) {
		return 0
	}
	if isIdeaLikeID(id) {
		return 2
	}
	return 1
}

func prioritizeTaskPriority(priority models.Priority) int {
	switch priority {
	case models.PriorityCritical:
		return 0
	case models.PriorityHigh:
		return 1
	case models.PriorityMedium:
		return 2
	case models.PriorityLow:
		return 3
	default:
		return 4
	}
}

func boolToInt(v bool) int {
	if v {
		return 0
	}
	return 1
}

func containsString(items []string, value string) bool {
	return containsTaskID(items, value)
}

func containsTaskID(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func findIndependentCandidates(tree models.TaskTree, calc *critical_path.CriticalPathCalculator, primary models.Task, count int) ([]string, error) {
	if count <= 0 {
		return []string{}, nil
	}
	available := calc.FindAllAvailable()
	criticalPath, _, err := calc.Calculate()
	if err != nil {
		return nil, err
	}
	filtered := []string{}
	for _, id := range available {
		if id == primary.ID {
			continue
		}
		task := tree.FindTask(id)
		if task == nil {
			continue
		}
		if strings.TrimSpace(task.EpicID) == "" || task.EpicID == primary.EpicID {
			continue
		}
		filtered = append(filtered, id)
	}
	return prioritizeTaskIDs(tree, criticalPath, filtered), nil
}

func claimTaskInTree(task *models.Task, agent string, now time.Time, tree models.TaskTree) error {
	task.Status = models.StatusInProgress
	task.ClaimedBy = agent
	task.ClaimedAt = &now
	task.StartedAt = &now
	return saveTaskState(*task, tree)
}

func positionalArgs(args []string, valueTakingFlags map[string]bool) []string {
	out := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			out = append(out, arg)
			continue
		}
		if strings.Contains(arg, "=") {
			continue
		}
		if valueTakingFlags[arg] && i+1 < len(args) {
			i++
		}
	}
	return out
}

func validateAtLeastTwoWords(command string, title string) error {
	if len(strings.Fields(strings.TrimSpace(title))) < 2 {
		return printUsageError(command, errors.New(command+" requires at least two words"))
	}
	return nil
}

func validateAllowedFlags(args []string, allowed map[string]bool) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == "--" {
			break
		}
		flag, hasValue := splitOption(arg)
		if flag == "" {
			continue
		}
		if !allowed[flag] {
			return fmt.Errorf("unexpected flag: %s", flag)
		}
		if hasValue {
			rawValue := strings.TrimSpace(strings.TrimPrefix(arg, flag+"="))
			if rawValue == "" {
				return fmt.Errorf("missing value for %s", flag)
			}
		}
	}
	return nil
}

type taskStats struct {
	done       int
	total      int
	inProgress int
	blocked    int
	totalHours float64
}

type syncTaskStats struct {
	done       int
	total      int
	inProgress int
	blocked    int
	pending    int
}

func allTaskIDs(tree models.TaskTree) []string {
	ids := []string{}
	for _, phase := range tree.Phases {
		ids = append(ids, phase.ID)
		for _, milestone := range phase.Milestones {
			ids = append(ids, milestone.ID)
			for _, epic := range milestone.Epics {
				ids = append(ids, epic.ID)
				for _, task := range epic.Tasks {
					ids = append(ids, task.ID)
				}
			}
		}
	}
	for _, bug := range tree.Bugs {
		ids = append(ids, bug.ID)
	}
	for _, idea := range tree.Ideas {
		ids = append(ids, idea.ID)
	}
	return ids
}

func scopedTaskSetContains(candidate string, scope []string) bool {
	if len(scope) == 0 {
		return false
	}
	for _, item := range scope {
		if item == candidate {
			return true
		}
	}
	return false
}

func isCompletedStatus(status models.Status) bool {
	return status == models.StatusDone || status == models.StatusRejected || status == models.StatusCancelled
}

func includeCompletionAux(status models.Status, unfinished bool, showCompletedAux bool) bool {
	if unfinished {
		return !isCompletedStatus(status)
	}
	if showCompletedAux {
		return true
	}
	return !isCompletedStatus(status)
}

func isBugLikeID(value string) bool {
	return regexp.MustCompile(`^B\d+$`).MatchString(value)
}

func isIdeaLikeID(value string) bool {
	return regexp.MustCompile(`^I\d+$`).MatchString(value)
}

func isFixedLikeID(value string) bool {
	return regexp.MustCompile(`^F\d+$`).MatchString(value)
}

func collectSyncTaskStats(tasks []models.Task) syncTaskStats {
	stats := syncTaskStats{}
	for _, task := range tasks {
		stats.total++
		switch task.Status {
		case models.StatusDone:
			stats.done++
		case models.StatusInProgress:
			stats.inProgress++
		case models.StatusBlocked:
			stats.blocked++
		default:
			stats.pending++
		}
	}
	return stats
}

func getSyncTaskStatsForPhase(phase models.Phase) syncTaskStats {
	tasks := []models.Task{}
	for _, milestone := range phase.Milestones {
		for _, epic := range milestone.Epics {
			tasks = append(tasks, epic.Tasks...)
		}
	}
	return collectSyncTaskStats(tasks)
}

func getSyncTaskStatsForMilestone(milestone models.Milestone) syncTaskStats {
	tasks := []models.Task{}
	for _, epic := range milestone.Epics {
		tasks = append(tasks, epic.Tasks...)
	}
	return collectSyncTaskStats(tasks)
}

func getSyncTaskStatsForEpic(epic models.Epic) syncTaskStats {
	return collectSyncTaskStats(epic.Tasks)
}

func syncTaskStatsPayloadWithTotalTasks(stats syncTaskStats) map[string]interface{} {
	return map[string]interface{}{
		"total_tasks": stats.total,
		"done":        stats.done,
		"in_progress": stats.inProgress,
		"blocked":     stats.blocked,
		"pending":     stats.pending,
	}
}

func syncTaskStatsPayload(stats syncTaskStats) map[string]interface{} {
	return map[string]interface{}{
		"total":       stats.total,
		"done":        stats.done,
		"in_progress": stats.inProgress,
		"blocked":     stats.blocked,
		"pending":     stats.pending,
	}
}

func writeSyncDerivedStats(dataDir string, tree models.TaskTree) error {
	for _, phase := range tree.Phases {
		phaseIndexPath := filepath.Join(dataDir, phase.Path, "index.yaml")
		phaseIndex, err := readYAMLMapFile(phaseIndexPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		phaseIndex["stats"] = syncTaskStatsPayloadWithTotalTasks(getSyncTaskStatsForPhase(phase))
		if err := writeYAMLMapFile(phaseIndexPath, phaseIndex); err != nil {
			return err
		}

		for _, milestone := range phase.Milestones {
			milestoneIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, "index.yaml")
			milestoneIndex, err := readYAMLMapFile(milestoneIndexPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			milestoneIndex["stats"] = syncTaskStatsPayloadWithTotalTasks(getSyncTaskStatsForMilestone(milestone))
			if err := writeYAMLMapFile(milestoneIndexPath, milestoneIndex); err != nil {
				return err
			}

			for _, epic := range milestone.Epics {
				epicIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, epic.Path, "index.yaml")
				epicIndex, err := readYAMLMapFile(epicIndexPath)
				if err != nil {
					if os.IsNotExist(err) {
						continue
					}
					return err
				}
				epicIndex["stats"] = syncTaskStatsPayload(getSyncTaskStatsForEpic(epic))
				if err := writeYAMLMapFile(epicIndexPath, epicIndex); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func parsePhaseTaskStats(items []models.Task) taskStats {
	stats := taskStats{}
	for _, task := range items {
		stats.total++
		switch task.Status {
		case models.StatusDone:
			stats.done++
		case models.StatusInProgress:
			stats.inProgress++
		case models.StatusBlocked:
			stats.blocked++
		}
		stats.totalHours += task.EstimateHours
	}
	return stats
}

func getTaskStatsForPhase(phase models.Phase) taskStats {
	tasks := []models.Task{}
	for _, milestone := range phase.Milestones {
		for _, epic := range milestone.Epics {
			tasks = append(tasks, epic.Tasks...)
		}
	}
	return parsePhaseTaskStats(tasks)
}

func getTaskStatsForMilestone(milestone models.Milestone) taskStats {
	tasks := []models.Task{}
	for _, epic := range milestone.Epics {
		tasks = append(tasks, epic.Tasks...)
	}
	return parsePhaseTaskStats(tasks)
}

func getTaskStatsForEpic(epic models.Epic) taskStats {
	return parsePhaseTaskStats(epic.Tasks)
}

func resolveListScope(tree models.TaskTree, rawScope string, scopeType string) ([]models.Phase, []string, int, string) {
	if strings.TrimSpace(rawScope) == "" {
		return nil, nil, 0, ""
	}

	if scopeType == "phase" {
		return resolveListScopeByPhase(tree, rawScope)
	}
	if scopeType == "milestone" {
		return resolveListScopeByMilestone(tree, rawScope)
	}
	if scopeType == "epic" {
		return resolveListScopeByEpic(tree, rawScope)
	}

	parsedQuery, parseErr := models.ParsePathQuery(rawScope)
	if parseErr == nil {
		if filtered := filterPhasesByPathQuery(tree.Phases, parsedQuery); len(filtered) > 0 {
			return resolveListScopeByPathQuery(tree, parsedQuery)
		}
	}

	taskPath, err := models.ParseTaskPath(rawScope)
	if err != nil {
		if phase := tree.FindPhase(rawScope); phase != nil {
			return resolveListScopeByPhase(tree, phase.ID)
		}
		if milestone := findMilestone(tree, rawScope); milestone != nil {
			return resolveListScopeByMilestone(tree, milestone.ID)
		}
		if epic := findEpic(tree, rawScope); epic != nil {
			return resolveListScopeByEpic(tree, epic.ID)
		}
		return nil, nil, 0, rawScope
	}

	if !taskPath.IsPhase() && !taskPath.IsMilestone() && !taskPath.IsEpic() && !taskPath.IsTask() {
		return nil, nil, 0, rawScope
	}

	switch taskPath.Depth() {
	case 1:
		return resolveListScopeByPhase(tree, taskPath.FullID())
	case 2:
		return resolveListScopeByMilestone(tree, taskPath.FullID())
	case 3:
		return resolveListScopeByEpic(tree, taskPath.FullID())
	default:
		return nil, nil, 0, rawScope
	}
}

func resolveListScopeByPathQuery(tree models.TaskTree, query models.PathQuery) ([]models.Phase, []string, int, string) {
	filteredPhases := filterPhasesByPathQuery(tree.Phases, query)
	if len(filteredPhases) == 0 {
		return nil, nil, len(query.Segments), query.Raw
	}

	filteredTaskIDs := []string{}
	for _, phase := range filteredPhases {
		filteredTaskIDs = append(filteredTaskIDs, collectPhaseTaskIDs(phase)...)
	}
	return filteredPhases, filteredTaskIDs, len(query.Segments), query.Raw
}

func resolveListScopeByPhase(tree models.TaskTree, rawScope string) ([]models.Phase, []string, int, string) {
	phaseNode := tree.FindPhase(rawScope)
	if phaseNode == nil {
		return nil, nil, 1, rawScope
	}
	phase := *phaseNode
	return []models.Phase{phase}, collectPhaseTaskIDs(phase), 1, phase.ID
}

func resolveListScopeByMilestone(tree models.TaskTree, rawScope string) ([]models.Phase, []string, int, string) {
	milestoneNode := findMilestone(tree, rawScope)
	if milestoneNode == nil {
		return nil, nil, 2, rawScope
	}
	phase := tree.FindPhase(milestoneNode.PhaseID)
	if phase == nil {
		return nil, nil, 2, milestoneNode.ID
	}
	filteredPhase := *phase
	filteredMilestones := []models.Milestone{}
	for _, milestone := range phase.Milestones {
		if tree.IDsMatch(milestone.ID, milestoneNode.ID) {
			filteredMilestones = append(filteredMilestones, milestone)
		}
	}
	filteredPhase.Milestones = filteredMilestones
	if len(filteredPhase.Milestones) == 0 {
		return nil, nil, 2, milestoneNode.ID
	}
	return []models.Phase{filteredPhase}, collectMilestoneTaskIDs(*milestoneNode), 2, milestoneNode.ID
}

func resolveListScopeByEpic(tree models.TaskTree, rawScope string) ([]models.Phase, []string, int, string) {
	epic := findEpic(tree, rawScope)
	if epic == nil {
		return nil, nil, 3, rawScope
	}
	phase := tree.FindPhase(epic.PhaseID)
	if phase == nil {
		return nil, nil, 3, epic.ID
	}
	filteredPhase := *phase
	filteredMilestones := []models.Milestone{}
	filteredTaskIDs := []string{}
	for _, milestone := range phase.Milestones {
		if milestone.ID == "" || (!tree.IDsMatch(milestone.ID, epic.MilestoneID) && milestone.ID != epic.MilestoneID) {
			continue
		}
		filteredMilestone := milestone
		filteredEpics := []models.Epic{}
		for _, scopedEpic := range milestone.Epics {
			if !tree.IDsMatch(scopedEpic.ID, epic.ID) {
				continue
			}
			filteredEpics = append(filteredEpics, scopedEpic)
			filteredTaskIDs = append(filteredTaskIDs, collectEpicTaskIDs(scopedEpic)...)
		}
		if len(filteredEpics) > 0 {
			filteredMilestone.Epics = filteredEpics
			filteredMilestones = append(filteredMilestones, filteredMilestone)
		}
	}
	filteredPhase.Milestones = filteredMilestones
	if len(filteredPhase.Milestones) == 0 {
		return nil, nil, 3, epic.ID
	}
	return []models.Phase{filteredPhase}, filteredTaskIDs, 3, epic.ID
}

func mergeScopedPhases(scopedPhaseSets [][]models.Phase) []models.Phase {
	merged := map[string]models.Phase{}
	order := []string{}
	for _, phases := range scopedPhaseSets {
		for _, phase := range phases {
			current, ok := merged[phase.ID]
			if !ok {
				merged[phase.ID] = phase
				order = append(order, phase.ID)
				continue
			}
			milestoneIndex := map[string]int{}
			for i, milestone := range current.Milestones {
				milestoneIndex[milestone.ID] = i
			}
			for _, milestone := range phase.Milestones {
				idx, hasMilestone := milestoneIndex[milestone.ID]
				if !hasMilestone {
					current.Milestones = append(current.Milestones, milestone)
					milestoneIndex[milestone.ID] = len(current.Milestones) - 1
					continue
				}
				currentMilestone := current.Milestones[idx]
				epicIndex := map[string]int{}
				for i, epic := range currentMilestone.Epics {
					epicIndex[epic.ID] = i
				}
				for _, epic := range milestone.Epics {
					epicIdx, hasEpic := epicIndex[epic.ID]
					if !hasEpic {
						currentMilestone.Epics = append(currentMilestone.Epics, epic)
						epicIndex[epic.ID] = len(currentMilestone.Epics) - 1
						continue
					}
					currentEpic := currentMilestone.Epics[epicIdx]
					taskSet := map[string]struct{}{}
					for _, task := range currentEpic.Tasks {
						taskSet[task.ID] = struct{}{}
					}
					for _, task := range epic.Tasks {
						if _, exists := taskSet[task.ID]; exists {
							continue
						}
						currentEpic.Tasks = append(currentEpic.Tasks, task)
						taskSet[task.ID] = struct{}{}
					}
					currentMilestone.Epics[epicIdx] = currentEpic
				}
				current.Milestones[idx] = currentMilestone
			}
			merged[phase.ID] = current
		}
	}
	out := make([]models.Phase, 0, len(order))
	for _, phaseID := range order {
		out = append(out, merged[phaseID])
	}
	return out
}

func collectPhaseTaskIDs(phase models.Phase) []string {
	out := []string{}
	for _, milestone := range phase.Milestones {
		out = append(out, collectMilestoneTaskIDs(milestone)...)
	}
	return out
}

func collectMilestoneTaskIDs(milestone models.Milestone) []string {
	out := []string{}
	for _, epic := range milestone.Epics {
		out = append(out, collectEpicTaskIDs(epic)...)
	}
	return out
}

func collectEpicTaskIDs(epic models.Epic) []string {
	out := []string{}
	for _, task := range epic.Tasks {
		out = append(out, task.ID)
	}
	return out
}

func hasUnfinishedMilestones(phase models.Phase) bool {
	for _, milestone := range phase.Milestones {
		if hasUnfinishedEpics(milestone) {
			return true
		}
	}
	return false
}

func hasUnfinishedEpics(milestone models.Milestone) bool {
	for _, epic := range milestone.Epics {
		if hasUnfinishedTasks(epic.Tasks) {
			return true
		}
	}
	return false
}

func hasUnfinishedTasks(tasks []models.Task) bool {
	for _, task := range tasks {
		if !isCompletedStatus(task.Status) {
			return true
		}
	}
	return false
}

func renderListProgress(tree models.TaskTree, criticalPath []string, scoped bool, scopedPhases []models.Phase, _ string, _ string, _ string, _ string, _ int, taskMatches func(models.Task) bool) error {
	phases := tree.Phases
	if scoped {
		phases = scopedPhases
	}
	if len(phases) == 0 {
		fmt.Println(styleError("No list nodes found for path query: unknown"))
		return nil
	}

	fmt.Println(styleHeader("Project Progress"))
	allTasks := findAllTasksInTree(tree)
	for _, phase := range phases {
		phaseTasks := collectPhaseTaskIDs(phase)
		counts := parsePhaseTaskStats(filterTasksByIDs(phaseTasks, allTasks, taskMatches))
		fmt.Printf("%s (%d/%d done, %s=%d, %s=%d)\n", styleSuccess(phase.ID), counts.done, counts.total, styleSubHeader("in_progress"), counts.inProgress, styleError("blocked"), counts.blocked)
		bugMarker := ""
		for _, id := range criticalPath {
			if strings.HasPrefix(id, phase.ID) {
				bugMarker = string(styleCritical("★"))
				break
			}
		}
		if bugMarker != "" {
			fmt.Printf("%s %s\n", bugMarker, styleMuted("on critical path"))
		}
	}
	return nil
}

func filterTasksByIDs(taskIDs []string, allTasks []models.Task, taskMatches func(models.Task) bool) []models.Task {
	set := map[string]struct{}{}
	for _, id := range taskIDs {
		if strings.TrimSpace(id) != "" {
			set[id] = struct{}{}
		}
	}
	if taskMatches == nil {
		taskMatches = func(_ models.Task) bool {
			return true
		}
	}
	out := []models.Task{}
	for _, task := range allTasks {
		if _, ok := set[task.ID]; !ok {
			continue
		}
		if !taskMatches(task) {
			continue
		}
		out = append(out, task)
	}
	return out
}

func renderListAvailable(tree models.TaskTree, calculator *critical_path.CriticalPathCalculator, outputJSON bool, scopedTasks []string, taskMatches func(models.Task) bool, criticalPath []string, availableTaskIDs map[string]struct{}, includeNormal, includeBugs, includeIdeas bool, _ bool) error {
	scoped := map[string]struct{}{}
	for _, id := range scopedTasks {
		scoped[id] = struct{}{}
	}

	filtered := []models.Task{}
	for _, taskID := range calculator.FindAllAvailable() {
		task := findTask(tree, taskID)
		if task == nil {
			continue
		}
		if !taskMatches(*task) {
			continue
		}
		if !includeNormal {
			if isBugLikeID(task.ID) && !includeBugs {
				continue
			}
			if isIdeaLikeID(task.ID) && !includeIdeas {
				continue
			}
			if !isBugLikeID(task.ID) && !isIdeaLikeID(task.ID) {
				continue
			}
		}
		if len(scoped) > 0 {
			if _, ok := scoped[task.ID]; !ok {
				continue
			}
		}
		filtered = append(filtered, *task)
	}

	if outputJSON {
		type availableJSON struct {
			ID            string  `json:"id"`
			Title         string  `json:"title"`
			EstimateHours float64 `json:"estimate_hours"`
			Complexity    string  `json:"complexity"`
			Priority      string  `json:"priority"`
			OnCritical    bool    `json:"on_critical_path"`
		}
		out := make([]availableJSON, 0, len(filtered))
		for _, task := range filtered {
			out = append(out, availableJSON{
				ID:            task.ID,
				Title:         task.Title,
				EstimateHours: task.EstimateHours,
				Complexity:    string(task.Complexity),
				Priority:      string(task.Priority),
				OnCritical:    containsString(criticalPath, task.ID),
			})
		}
		raw, err := json.MarshalIndent(map[string]any{
			"available": out,
		}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}

	if len(filtered) == 0 {
		fmt.Println(styleError("No available tasks found."))
		return nil
	}
	grouped := map[string][]models.Task{}
	bugs := []models.Task{}
	ideas := []models.Task{}
	for _, task := range filtered {
		if isBugLikeID(task.ID) {
			bugs = append(bugs, task)
			continue
		}
		if isIdeaLikeID(task.ID) {
			ideas = append(ideas, task)
			continue
		}
		phaseID := ""
		p, err := task.TaskPath()
		if err == nil {
			phaseID = p.Phase
		}
		grouped[phaseID] = append(grouped[phaseID], task)
	}

	fmt.Printf("\n%s\n", styleHeader(fmt.Sprintf("Available Tasks (%d)", len(filtered))))
	fmt.Printf("%s\n\n", styleMuted("Legend: ★ critical path, status icon indicates current state"))

	for _, phase := range tree.Phases {
		tasks := grouped[phase.ID]
		if len(tasks) == 0 {
			continue
		}
		sort.Slice(tasks, func(i, j int) bool {
			return tasks[i].ID < tasks[j].ID
		})
		fmt.Printf("%s\n", styleSubHeader(fmt.Sprintf("%s (%s) (%d available)", phase.Name, phase.ID, len(tasks))))
		for _, task := range tasks {
			fmt.Println(formatTaskSummary(task, criticalPath, availableTaskIDs))
			for _, detail := range formatTaskDetails(task) {
				fmt.Printf("    %s\n", detail)
			}
		}
		fmt.Println("")
	}
	if len(bugs) > 0 {
		sort.Slice(bugs, func(i, j int) bool {
			return bugs[i].ID < bugs[j].ID
		})
		fmt.Printf("%s\n", styleSubHeader(fmt.Sprintf("Bugs (%d available)", len(bugs))))
		for _, task := range bugs {
			fmt.Println(formatTaskSummary(task, criticalPath, availableTaskIDs))
			for _, detail := range formatTaskDetails(task) {
				fmt.Printf("    %s\n", detail)
			}
		}
		fmt.Println("")
	}
	if len(ideas) > 0 {
		sort.Slice(ideas, func(i, j int) bool {
			return ideas[i].ID < ideas[j].ID
		})
		fmt.Printf("%s\n", styleSubHeader(fmt.Sprintf("Ideas (%d available)", len(ideas))))
		for _, task := range ideas {
			fmt.Println(formatTaskSummary(task, criticalPath, availableTaskIDs))
			for _, detail := range formatTaskDetails(task) {
				fmt.Printf("    %s\n", detail)
			}
		}
		fmt.Println("")
	}
	fmt.Printf("%s\n", styleMuted("Use `bl claim TASK_ID` to claim a specific task, or `backlog grab` to auto-claim the best available task."))
	return nil
}

func renderListJSON(tree models.TaskTree, scoped bool, scopedPhases []models.Phase, includeNormal, includeBugs, includeIdeas, showAll, unfinished, showCompletedAux bool, taskMatches func(models.Task) bool, criticalPath []string, nextAvailable string, hasComplexityFilter, hasPriorityFilter bool, complexityFilter models.Complexity, priorityFilter models.Priority, scopedTasks []string, statusFilter []string) error {
	_ = showAll
	phasesSource := scopedPhases
	if phasesSource == nil {
		if includeNormal {
			phasesSource = tree.Phases
		} else {
			phasesSource = []models.Phase{}
		}
	}

	scopedSet := map[string]struct{}{}
	for _, id := range scopedTasks {
		scopedSet[id] = struct{}{}
	}

	filteredNormal := []models.Task{}
	for _, phase := range phasesSource {
		for _, milestone := range phase.Milestones {
			for _, epic := range milestone.Epics {
				for _, task := range epic.Tasks {
					if !taskMatches(task) {
						continue
					}
					if len(scopedSet) > 0 {
						if _, ok := scopedSet[task.ID]; !ok {
							continue
						}
					}
					filteredNormal = append(filteredNormal, task)
				}
			}
		}
	}
	type taskJSON struct {
		ID            string  `json:"id"`
		Title         string  `json:"title"`
		Status        string  `json:"status"`
		EstimateHours float64 `json:"estimate_hours"`
		Complexity    string  `json:"complexity"`
		Priority      string  `json:"priority"`
		OnCritical    bool    `json:"on_critical_path"`
	}

	output := map[string]any{
		"critical_path":  criticalPath,
		"next_available": nextAvailable,
	}
	if hasComplexityFilter {
		output["filter"] = map[string]any{"complexity": string(complexityFilter)}
	}
	if hasPriorityFilter {
		if _, ok := output["filter"]; !ok {
			output["filter"] = map[string]any{}
		}
		filter := output["filter"].(map[string]any)
		filter["priority"] = string(priorityFilter)
	}

	phasesOut := []map[string]any{}
	for _, phase := range phasesSource {
		phaseTasks := []taskJSON{}
		milestonesOut := []map[string]any{}
		for _, milestone := range phase.Milestones {
			if unfinished {
				unfinishedEpic := false
				for _, epic := range milestone.Epics {
					if hasUnfinishedTasks(epic.Tasks) {
						unfinishedEpic = true
						break
					}
				}
				if !unfinishedEpic {
					continue
				}
			}
			for _, epic := range milestone.Epics {
				if unfinished && !hasUnfinishedTasks(epic.Tasks) {
					continue
				}
				for _, task := range epic.Tasks {
					if !taskMatches(task) {
						continue
					}
					phaseTasks = append(phaseTasks, taskJSON{
						ID:            task.ID,
						Title:         task.Title,
						Status:        string(task.Status),
						EstimateHours: task.EstimateHours,
						Complexity:    string(task.Complexity),
						Priority:      string(task.Priority),
						OnCritical:    containsString(criticalPath, task.ID),
					})
				}
			}
			stats := getTaskStatsForMilestone(milestone)
			milestonesOut = append(milestonesOut, map[string]any{
				"id":     milestone.ID,
				"name":   milestone.Name,
				"status": string(milestone.Status),
				"stats":  map[string]int{"done": stats.done, "total": stats.total, "in_progress": stats.inProgress, "blocked": stats.blocked},
			})
		}
		stats := getTaskStatsForPhase(phase)
		ph := map[string]any{
			"id":     phase.ID,
			"name":   phase.Name,
			"status": string(phase.Status),
			"stats": map[string]int{
				"done":        stats.done,
				"total":       stats.total,
				"in_progress": stats.inProgress,
				"blocked":     stats.blocked,
			},
			"milestones": milestonesOut,
		}
		phasesOut = append(phasesOut, ph)
	}
	output["phases"] = phasesOut

	outputTasks := []taskJSON{}
	for _, task := range filteredNormal {
		if len(statusFilter) > 0 && !containsString(statusFilter, string(task.Status)) {
			continue
		}
		outputTasks = append(outputTasks, taskJSON{
			ID:            task.ID,
			Title:         task.Title,
			Status:        string(task.Status),
			EstimateHours: task.EstimateHours,
			Complexity:    string(task.Complexity),
			Priority:      string(task.Priority),
			OnCritical:    containsString(criticalPath, task.ID),
		})
	}
	output["tasks"] = outputTasks

	bugs := []taskJSON{}
	for _, bug := range tree.Bugs {
		if !includeBugs {
			continue
		}
		if !includeCompletionAux(bug.Status, unfinished, showCompletedAux) {
			continue
		}
		bugs = append(bugs, taskJSON{
			ID:            bug.ID,
			Title:         bug.Title,
			Status:        string(bug.Status),
			EstimateHours: bug.EstimateHours,
			Priority:      string(bug.Priority),
			OnCritical:    containsString(criticalPath, bug.ID),
		})
	}
	output["bugs"] = bugs

	ideas := []taskJSON{}
	for _, idea := range tree.Ideas {
		if !includeIdeas {
			continue
		}
		if !includeCompletionAux(idea.Status, unfinished, showCompletedAux) {
			continue
		}
		ideas = append(ideas, taskJSON{
			ID:            idea.ID,
			Title:         idea.Title,
			Status:        string(idea.Status),
			EstimateHours: idea.EstimateHours,
			Priority:      string(idea.Priority),
			OnCritical:    containsString(criticalPath, idea.ID),
		})
	}
	output["ideas"] = ideas

	out, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func renderListText(command string, tree models.TaskTree, scoped bool, scopedPhases []models.Phase, scopedTasks []string, _ string, scopeDepth int, taskMatches func(models.Task) bool, criticalPath []string, showAll bool, availableTaskIDs map[string]struct{}) error {
	_ = showAll
	fmt.Printf("%s %s\n", styleSubHeader("Critical Path:"), strings.Join(criticalPath[:min(len(criticalPath), 10)], " -> "))

	phases := tree.Phases
	if scoped {
		phases = scopedPhases
	}
	if scoped && scopeDepth > 0 {
		scopedMaxDepth := min(4, scopeDepth+2)
		for _, phase := range phases {
			for _, line := range renderListScopePhase(phase, taskMatches, criticalPath, availableTaskIDs, scopedMaxDepth, 1) {
				fmt.Println(line)
			}
		}
		return nil
	}

	for _, phase := range phases {
		stats := parsePhaseTaskStats([]models.Task{})
		for _, milestone := range phase.Milestones {
			for _, epic := range milestone.Epics {
				for _, task := range epic.Tasks {
					if !taskMatches(task) {
						continue
					}
					stats.total++
					switch task.Status {
					case models.StatusDone:
						stats.done++
					case models.StatusInProgress:
						stats.inProgress++
					case models.StatusBlocked:
						stats.blocked++
					}
				}
			}
		}
		if scopedTasks != nil && stats.total == 0 && len(scopedTasks) > 0 {
			continue
		}
		milestoneLines := []string{}
		for _, milestone := range phase.Milestones {
			milestoneDone := 0
			milestoneTotal := 0
			for _, epic := range milestone.Epics {
				for _, task := range epic.Tasks {
					if !taskMatches(task) {
						continue
					}
					milestoneTotal++
					if task.Status == models.StatusDone {
						milestoneDone++
					}
				}
			}
			if milestoneTotal == 0 {
				continue
			}
			milestoneLines = append(
				milestoneLines,
				fmt.Sprintf(
					"%s (%s) (%d/%d tasks done)",
					styleSubHeader("  "+milestone.Name),
					styleSubHeader(milestone.ID),
					milestoneDone,
					milestoneTotal,
				),
			)
		}
		fmt.Printf("%s (%s) (%d/%d tasks done)\n", styleSuccess(phase.Name), styleSuccess(phase.ID), stats.done, stats.total)
		limit := 5
		for i, line := range milestoneLines {
			if i >= limit {
				fmt.Printf("%s\n", styleMuted(fmt.Sprintf("  ... and %d more milestones", len(milestoneLines)-limit)))
				break
			}
			fmt.Println(line)
		}
		fmt.Println()
	}
	bugs := []models.Task{}
	for _, bug := range tree.Bugs {
		if bug.ID == "" {
			continue
		}
		if taskMatches(bug) {
			bugs = append(bugs, bug)
		}
	}
	if len(bugs) > 0 {
		done := 0
		for _, bug := range bugs {
			if bug.Status == models.StatusDone {
				done++
			}
		}
		fmt.Printf("%s\n", styleSubHeader(fmt.Sprintf("Bugs (%d/%d done)", done, len(bugs))))
		for i, bug := range bugs {
			prefix := "  ├── "
			if i == len(bugs)-1 {
				prefix = "  └── "
			}
			icon := checkboxIconForTask(bug, availableTaskIDs)
			critical := ""
			if containsString(criticalPath, bug.ID) {
				critical = string(styleCritical("★ "))
			}
			fmt.Printf("%s%s %s%s: %s\n", prefix, icon, critical, styleSuccess(bug.ID), bug.Title)
		}
	}

	ideas := []models.Task{}
	for _, idea := range tree.Ideas {
		if idea.ID == "" {
			continue
		}
		if taskMatches(idea) {
			ideas = append(ideas, idea)
		}
	}
	if len(ideas) > 0 {
		done := 0
		for _, idea := range ideas {
			if idea.Status == models.StatusDone {
				done++
			}
		}
		fmt.Printf("%s\n", styleSubHeader(fmt.Sprintf("Ideas (%d/%d done)", done, len(ideas))))
		for i, idea := range ideas {
			prefix := "  ├── "
			if i == len(ideas)-1 {
				prefix = "  └── "
			}
			icon := checkboxIconForTask(idea, availableTaskIDs)
			critical := ""
			if containsString(criticalPath, idea.ID) {
				critical = string(styleCritical("★ "))
			}
			fmt.Printf("%s%s %s%s: %s\n", prefix, icon, critical, styleSuccess(idea.ID), idea.Title)
		}
	}
	return nil
}

func collectFilteredMilestoneTasks(milestone models.Milestone, taskMatches func(models.Task) bool) []models.Task {
	filtered := []models.Task{}
	for _, epic := range milestone.Epics {
		filtered = append(filtered, collectFilteredEpicTasks(epic, taskMatches)...)
	}
	return filtered
}

func collectFilteredEpicTasks(epic models.Epic, taskMatches func(models.Task) bool) []models.Task {
	out := []models.Task{}
	for _, task := range epic.Tasks {
		if taskMatches(task) {
			out = append(out, task)
		}
	}
	return out
}

func renderListScopePhase(phase models.Phase, taskMatches func(models.Task) bool, criticalPath []string, availableTaskIDs map[string]struct{}, maxDepth int, currentDepth int) []string {
	lines := []string{}
	phaseTasks := []models.Task{}
	for _, milestone := range phase.Milestones {
		phaseTasks = append(phaseTasks, collectFilteredMilestoneTasks(milestone, taskMatches)...)
	}
	stats := parsePhaseTaskStats(phaseTasks)
	if stats.total == 0 {
		return lines
	}

	lines = append(
		lines,
		fmt.Sprintf("%s (%s) (%d/%d tasks done)", styleSuccess(phase.Name), styleSuccess(phase.ID), stats.done, stats.total),
	)
	if currentDepth >= maxDepth {
		return lines
	}

	for _, milestone := range phase.Milestones {
		lines = append(lines, renderListScopeMilestone(milestone, taskMatches, criticalPath, availableTaskIDs, maxDepth, currentDepth+1, "  ")...)
	}
	return lines
}

func renderListScopeMilestone(milestone models.Milestone, taskMatches func(models.Task) bool, criticalPath []string, availableTaskIDs map[string]struct{}, maxDepth int, currentDepth int, prefix string) []string {
	_ = criticalPath
	_ = availableTaskIDs
	lines := []string{}
	milestoneTasks := collectFilteredMilestoneTasks(milestone, taskMatches)
	stats := parsePhaseTaskStats(milestoneTasks)
	if stats.total == 0 {
		return lines
	}
	lines = append(
		lines,
		fmt.Sprintf(
			"%s%s (%s) (%d/%d tasks done)",
			prefix,
			styleSubHeader(milestone.Name),
			styleSubHeader(milestone.ID),
			stats.done,
			stats.total,
		),
	)
	if currentDepth >= maxDepth {
		return lines
	}

	nextPrefix := prefix + "  "
	for _, epic := range milestone.Epics {
		lines = append(lines, renderListScopeEpic(epic, taskMatches, criticalPath, availableTaskIDs, maxDepth, currentDepth+1, nextPrefix)...)
	}
	return lines
}

func renderListScopeEpic(epic models.Epic, taskMatches func(models.Task) bool, criticalPath []string, availableTaskIDs map[string]struct{}, maxDepth int, currentDepth int, prefix string) []string {
	lines := []string{}
	epicTasks := collectFilteredEpicTasks(epic, taskMatches)
	stats := parsePhaseTaskStats(epicTasks)
	if stats.total == 0 {
		return lines
	}
	lines = append(
		lines,
		fmt.Sprintf("%s%s (%s) (%d/%d tasks done)", prefix, styleSubHeader(epic.Name), styleSubHeader(epic.ID), stats.done, stats.total),
	)
	if currentDepth >= maxDepth {
		return lines
	}

	taskPrefix := prefix + "  "
	for _, task := range epicTasks {
		lines = append(lines, fmt.Sprintf("%s%s", taskPrefix, formatTaskSummary(task, criticalPath, availableTaskIDs)))
	}
	return lines
}

func renderBugOrIdeaDetail(task models.Task, showInstructions bool, dataDir string, showNext bool) {
	fmt.Printf("%s: %s\n", styleSuccess(task.ID), task.Title)
	fmt.Printf("%s: %s\n", styleSubHeader("Status"), styleStatusText(string(task.Status)))
	fmt.Printf("%s: %.2fh\n", styleSubHeader("Estimate"), task.EstimateHours)
	fmt.Printf("%s: %s\n", styleSubHeader("Priority"), styleMuted(string(task.Priority)))
	if len(task.Tags) > 0 {
		fmt.Printf("%s: %s\n", styleSubHeader("Tags"), strings.Join(task.Tags, ", "))
	}
	if showInstructions {
		fmt.Println(styleWarning("Idea instructions: evaluate feasibility and create concrete tasks in backlog."))
	}
	file := filepath.Join(dataDir, task.File)
	if task.File != "" {
		fmt.Printf("%s: %s\n", styleSubHeader("File"), file)
	}
	if showNext {
		printNextCommands("backlog show "+task.ID, "backlog claim "+task.ID)
	}
}

func showScopedItem(
	tree models.TaskTree,
	id string,
	scopePath *models.TaskPath,
	dataDir string,
	showNext bool,
	showLong bool,
	showAll bool,
) error {
	calculator := critical_path.NewCriticalPathCalculator(tree, map[string]float64{})
	availableTaskIDs := taskIDSet(calculator.FindAllAvailable())

	if scopePath == nil {
		parsed, err := models.ParseTaskPath(id)
		if err != nil {
			return fmt.Errorf("Invalid path format: %s", id)
		}
		scopePath = &parsed
	}
	parent := scopePath.Parent()
	scopeHint := ""
	if parent != nil {
		scopeHint = parent.FullID()
	}
	switch {
	case scopePath.IsPhase():
		phase := tree.FindPhase(scopePath.FullID())
		if phase == nil {
			return showNotFound(tree, "Phase", id, scopeHint)
		}
		stats := getTaskStatsForPhase(*phase)
		fmt.Printf("%s: %s\n", styleSuccess(phase.ID), styleSubHeader(phase.Name))
		fmt.Printf("%s: %s\n", styleSubHeader("Status"), styleStatusText(string(phase.Status)))
		fmt.Printf("%s: %s %d/%d\n", styleSubHeader("Progress"), makeProgressBarWithStatus(stats.done, stats.inProgress, stats.blocked, stats.total), stats.done, stats.total)
		fmt.Printf("%s: %.2fh\n", styleSubHeader("Total Duration"), stats.totalHours)
		if len(phase.Milestones) > 0 {
			fmt.Printf("%s\n", styleSubHeader("Milestones"))
			limit := min(8, len(phase.Milestones))
			for i := 0; i < limit; i++ {
				milestone := phase.Milestones[i]
				mStats := getTaskStatsForMilestone(milestone)
				fmt.Printf("  %s %s (%d/%d)\n", auxStatusMarker(mStats.total, mStats.done, mStats.inProgress), styleSuccess(milestone.ID), mStats.done, mStats.total)
			}
			if len(phase.Milestones) > limit {
				fmt.Printf("  %s\n", styleMuted(fmt.Sprintf("... and %d more", len(phase.Milestones)-limit)))
			}
		}
		if showNext {
			printNextCommands("backlog ls "+phase.ID, "backlog list --phase "+phase.ID)
		}
		return nil
	case scopePath.IsMilestone():
		milestone := findMilestone(tree, scopePath.FullID())
		if milestone == nil {
			return showNotFound(tree, "Milestone", id, scopeHint)
		}
		stats := getTaskStatsForMilestone(*milestone)
		fmt.Printf("%s: %s\n", styleSuccess(milestone.ID), styleSubHeader(milestone.Name))
		fmt.Printf("%s: %s\n", styleSubHeader("Status"), styleStatusText(string(milestone.Status)))
		fmt.Printf("%s: %s %d/%d\n", styleSubHeader("Progress"), makeProgressBarWithStatus(stats.done, stats.inProgress, stats.blocked, stats.total), stats.done, stats.total)
		fmt.Printf("%s: %.2fh\n", styleSubHeader("Total Duration"), stats.totalHours)
		if len(milestone.Epics) > 0 {
			fmt.Printf("%s\n", styleSubHeader("Epics"))
			limit := min(8, len(milestone.Epics))
			for i := 0; i < limit; i++ {
				epic := milestone.Epics[i]
				eStats := getTaskStatsForEpic(epic)
				fmt.Printf("  %s %s (%d/%d)\n", auxStatusMarker(eStats.total, eStats.done, eStats.inProgress), styleSuccess(epic.ID), eStats.done, eStats.total)
			}
			if len(milestone.Epics) > limit {
				fmt.Printf("  %s\n", styleMuted(fmt.Sprintf("... and %d more", len(milestone.Epics)-limit)))
			}
		}
		if showNext {
			printNextCommands("backlog ls "+milestone.ID, "backlog list --milestone "+milestone.ID)
		}
		return nil
	case scopePath.IsEpic():
		epic := findEpic(tree, scopePath.FullID())
		if epic == nil {
			return showNotFound(tree, "Epic", id, scopeHint)
		}
		stats := getTaskStatsForEpic(*epic)
		fmt.Printf("%s: %s\n", styleSuccess(epic.ID), styleSubHeader(epic.Name))
		fmt.Printf("%s: %s\n", styleSubHeader("Status"), styleStatusText(string(epic.Status)))
		fmt.Printf("%s: %s %d/%d\n", styleSubHeader("Progress"), makeProgressBarWithStatus(stats.done, stats.inProgress, stats.blocked, stats.total), stats.done, stats.total)
		fmt.Printf("%s: %.2fh\n", styleSubHeader("Total Duration"), stats.totalHours)
		if len(epic.Tasks) > 0 {
			fmt.Printf("%s\n", styleSubHeader("Tasks"))
			limit := min(10, len(epic.Tasks))
			for i := 0; i < limit; i++ {
				task := epic.Tasks[i]
				fmt.Printf("  %s %s %s\n", checkboxIconForTask(task, availableTaskIDs), styleSuccess(task.ID), task.Title)
			}
			if len(epic.Tasks) > limit {
				fmt.Printf("  %s\n", styleMuted(fmt.Sprintf("... and %d more", len(epic.Tasks)-limit)))
			}
		}
		if showNext {
			printNextCommands("backlog ls "+epic.ID, "backlog add "+epic.ID+" --title \"<task title>\"")
		}
		return nil
	case scopePath.IsTask():
		task := findTask(tree, scopePath.FullID())
		if task == nil {
			return showNotFound(tree, "Task", id, scopeHint)
		}
		renderTaskDetail(*task, dataDir, showNext, showLong, showAll, tree)
		return nil
	default:
		return showNotFound(tree, "Task", id, scopeHint)
	}
}

func showNotFound(tree models.TaskTree, itemType, itemID, scopeHint string) error {
	fmt.Printf("%s %s not found: %s\n", styleError("Error:"), styleSubHeader(itemType), itemID)
	printIDSuggestions(tree, itemID, scopeHint, 5)
	if scopeHint != "" {
		fmt.Printf("%s Use 'backlog tree %s' to verify available IDs.\n", styleWarning("Tip:"), scopeHint)
	} else {
		fmt.Println(styleWarning("Tip: Use 'backlog tree' to list available IDs."))
	}
	return fmt.Errorf("%s not found", itemType)
}

func formatNotFoundError(tree models.TaskTree, itemType, itemID, scopeHint string) error {
	base := fmt.Sprintf("%s not found: %s", itemType, itemID)
	suggestions := suggestItemIDs(tree, itemID, scopeHint, 5)
	if len(suggestions) == 0 {
		return errors.New(base)
	}
	return fmt.Errorf("%s\nDid you mean: %s", base, strings.Join(suggestions, ", "))
}

func printIDSuggestions(tree models.TaskTree, query, scopeHint string, limit int) {
	suggestions := suggestItemIDs(tree, query, scopeHint, limit)
	if len(suggestions) == 0 {
		return
	}
	fmt.Printf("%s\n", styleSubHeader("Did you mean:"))
	for _, candidate := range suggestions {
		fmt.Printf("  %s\n", styleSuccess(candidate))
	}
}

func suggestItemIDs(tree models.TaskTree, query, scopeHint string, limit int) []string {
	if limit <= 0 {
		limit = 5
	}
	all := allTaskIDs(tree)
	if len(all) == 0 {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	scopeHint = strings.TrimSpace(scopeHint)
	seen := map[string]struct{}{}
	out := []string{}
	add := func(candidate string) {
		if strings.TrimSpace(candidate) == "" {
			return
		}
		if _, ok := seen[candidate]; ok {
			return
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}

	if scopeHint != "" {
		prefix := scopeHint + "."
		for _, candidate := range all {
			if strings.HasPrefix(candidate, prefix) {
				add(candidate)
				if len(out) >= limit {
					return out
				}
			}
		}
	}

	for _, candidate := range all {
		lower := strings.ToLower(candidate)
		if query != "" && (strings.HasPrefix(lower, query) || strings.HasPrefix(query, lower)) {
			add(candidate)
			if len(out) >= limit {
				return out
			}
		}
	}
	for _, candidate := range all {
		lower := strings.ToLower(candidate)
		if query != "" && strings.Contains(lower, query) {
			add(candidate)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func phasePathFromID(raw string) *models.TaskPath {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return nil
	}
	if strings.HasPrefix(candidate, ".") {
		candidate = strings.TrimPrefix(candidate, ".")
	}
	if !strings.Contains(candidate, ".") {
		return nil
	}
	path, err := models.ParseTaskPath(candidate)
	if err != nil {
		return nil
	}
	return &path
}

func findMilestone(tree models.TaskTree, id string) *models.Milestone {
	return tree.FindMilestone(id)
}

func findEpic(tree models.TaskTree, id string) *models.Epic {
	return tree.FindEpic(id)
}

func findTask(tree models.TaskTree, id string) *models.Task {
	if tree.IDsMatch(id, "") {
		return nil
	}
	if task := tree.FindTask(id); task != nil {
		return task
	}
	for i := range tree.Phases {
		phase := &tree.Phases[i]
		for j := range phase.Milestones {
			milestone := &phase.Milestones[j]
			for k := range milestone.Epics {
				epic := &milestone.Epics[k]
				for l := range epic.Tasks {
					task := &epic.Tasks[l]
					if tree.IDsMatch(task.ID, id) {
						return task
					}
				}
			}
		}
	}
	for i := range tree.Bugs {
		task := &tree.Bugs[i]
		if tree.IDsMatch(task.ID, id) {
			return task
		}
	}
	for i := range tree.Ideas {
		task := &tree.Ideas[i]
		if tree.IDsMatch(task.ID, id) {
			return task
		}
	}
	return nil
}

func findAuxiliaryTask(tree models.TaskTree, id string) (*models.Task, error) {
	if task := findTask(tree, id); task != nil {
		return task, nil
	}
	return nil, fmt.Errorf("Task not found: %s", id)
}

func findAllTasksInTree(tree models.TaskTree) []models.Task {
	out := []models.Task{}
	for _, phase := range tree.Phases {
		for _, milestone := range phase.Milestones {
			for _, epic := range milestone.Epics {
				out = append(out, epic.Tasks...)
			}
		}
	}
	out = append(out, tree.Bugs...)
	out = append(out, tree.Ideas...)
	return out
}

func findNormalTasksInTree(tree models.TaskTree) []models.Task {
	out := []models.Task{}
	for _, phase := range tree.Phases {
		for _, milestone := range phase.Milestones {
			for _, epic := range milestone.Epics {
				out = append(out, epic.Tasks...)
			}
		}
	}
	return out
}

func printTaskSummary(task *models.Task, dataDir string) error {
	taskPath := filepath.Join(dataDir, task.File)
	frontmatter, body, warnings, missing, err := readTodoFrontmatter(task.ID, task.File)
	if err != nil {
		return err
	}
	if missing {
		fmt.Println(styleError("Task file missing"))
		fmt.Printf("%s: %s - %s\n", styleError("Task file missing"), styleSuccess(task.ID), task.Title)
		fmt.Printf("%s\n", styleSubHeader("Frontmatter:"))
		fmt.Println(styleMuted("  (unavailable)"))
		if strings.TrimSpace(taskPath) != "" {
			fmt.Printf("%s: %s\n", styleSubHeader("Missing"), taskPath)
		}
		return nil
	}
	printTodoFileWarnings(warnings)
	fmt.Printf("%s: %s - %s\n", styleSuccess("Task"), styleSuccess(task.ID), task.Title)
	fmt.Printf("%s\n", styleSubHeader("Frontmatter:"))
	if len(frontmatter) > 0 {
		out, err := yaml.Marshal(frontmatter)
		if err != nil {
			return err
		}
		fmt.Println(strings.TrimSpace(string(out)))
	} else {
		fmt.Println(styleMuted("  (unavailable)"))
	}
	fmt.Printf("%s: %d\n", styleSubHeader("Body length"), len(body))
	fmt.Printf("%s\n", styleMuted(fmt.Sprintf("Run 'backlog show %s' for full details.", task.ID)))
	return nil
}

func implicitDependencyID(task models.Task, tree models.TaskTree) *models.Task {
	if len(task.DependsOn) > 0 || strings.TrimSpace(task.EpicID) == "" {
		return nil
	}

	epic := tree.FindEpic(task.EpicID)
	if epic == nil {
		return nil
	}

	for idx := range epic.Tasks {
		if epic.Tasks[idx].ID == task.ID {
			if idx == 0 {
				return nil
			}
			return &epic.Tasks[idx-1]
		}
	}
	return nil
}

func renderTaskDependencySummary(task models.Task, tree models.TaskTree) bool {
	if len(task.DependsOn) > 0 {
		fmt.Printf("%s\n", styleSubHeader("Explicit dependencies:"))
		for _, depID := range task.DependsOn {
			depTask := tree.FindTask(depID)
			if depTask == nil {
				fmt.Printf("  %s %s (%s)\n", styleError("?"), styleSuccess(depID), styleError("not found"))
				continue
			}

			marker := styleError("✗")
			if depTask.Status == models.StatusDone {
				marker = styleSuccess("✓")
			}
			fmt.Printf("  %s %s (%s)\n", marker, styleSuccess(depTask.ID), styleStatusText(string(depTask.Status)))
		}
		fmt.Printf("%s\n", styleMuted("Legend: ✓ done | ✗ not done | ? not found"))
		return true
	}

	prevTask := implicitDependencyID(task, tree)
	if prevTask == nil {
		return false
	}

	marker := styleError("✗")
	if prevTask.Status == models.StatusDone {
		marker = styleSuccess("✓")
	}

	fmt.Printf("%s\n", styleSubHeader("Implicit dependency (previous in epic):"))
	fmt.Printf("  %s %s (%s)\n", marker, styleSuccess(prevTask.ID), styleStatusText(string(prevTask.Status)))
	fmt.Printf("%s\n", styleMuted("Legend: ✓ done | ✗ not done | ? not found"))
	return true
}

func renderTaskDetail(task models.Task, dataDir string, showNext bool, showLong bool, showAll bool, tree models.TaskTree) {
	fmt.Printf("%s: %s\n", styleSuccess("Task"), task.ID)
	fmt.Printf("%s: %s\n", styleSubHeader("Title"), task.Title)
	fmt.Printf("%s: %s\n", styleSubHeader("Status"), styleStatusText(string(task.Status)))
	fmt.Printf("%s: %.2f\n", styleSubHeader("Estimate"), task.EstimateHours)
	fmt.Printf("%s: %s\n", styleSubHeader("Complexity"), task.Complexity)
	fmt.Printf("%s: %s\n", styleSubHeader("Priority"), task.Priority)
	if renderTaskDependencySummary(task, tree) {
		fmt.Println()
	}
	if len(task.Tags) > 0 {
		fmt.Printf("%s: %s\n", styleSubHeader("Tags"), strings.Join(task.Tags, ", "))
	}
	if task.ClaimedBy != "" {
		fmt.Printf("%s: %s\n", styleSubHeader("Claimed by"), task.ClaimedBy)
	}
	if task.ClaimedAt != nil {
		fmt.Printf("%s: %s\n", styleSubHeader("Claimed at"), task.ClaimedAt.Format(time.RFC3339))
	}
	if task.StartedAt != nil {
		fmt.Printf("%s: %s\n", styleSubHeader("Started at"), task.StartedAt.Format(time.RFC3339))
	}
	if task.CompletedAt != nil {
		fmt.Printf("%s: %s\n", styleSubHeader("Completed at"), task.CompletedAt.Format(time.RFC3339))
	}
	if task.DurationMinutes != nil {
		fmt.Printf("%s: %d minutes\n", styleSubHeader("Duration"), int(*task.DurationMinutes))
	}
	filePath := filepath.Join(dataDir, task.File)
	fmt.Printf("%s: %s\n", styleSubHeader("File"), filePath)
	if fileSize, fileLines, err := taskFileStats(filePath); err == nil {
		fmt.Printf("%s: %d bytes, %d lines\n", styleSubHeader("File stats"), fileSize, fileLines)
	}
	_, body, warnings, missing, err := readTodoFrontmatter(task.ID, task.File)
	if err == nil {
		printTodoFileWarnings(warnings)
		if !missing {
			if showAll {
				raw, rawErr := os.ReadFile(filePath)
				fmt.Printf("%s\n", styleSubHeader("Task File"))
				if rawErr == nil {
					fmt.Printf("%s", string(raw))
				} else {
					fmt.Printf("  %s\n", styleWarning(fmt.Sprintf("Could not read full file: %s", rawErr)))
				}
			} else {
				body = strings.TrimSpace(body)
				if body != "" {
					lines := strings.Split(body, "\n")
					fmt.Printf("%s\n", styleSubHeader("Preview"))
					limit := len(lines)
					if !showLong {
						limit = min(taskFileReadPreviewLines, len(lines))
					}
					for i := 0; i < limit; i++ {
						fmt.Printf("  %s\n", lines[i])
					}
					if !showLong && len(lines) > limit {
						fmt.Printf("  %s\n", styleMuted(fmt.Sprintf("... (%d more lines)", len(lines)-limit)))
						fmt.Printf("  %s\n", styleMuted(fmt.Sprintf("To view the full file, run: cat %s", filePath)))
					}
				}
			}
		}
	}
	switch task.Status {
	case models.StatusPending:
		if showNext {
			printNextCommands("backlog claim "+task.ID, "backlog why "+task.ID)
		}
	case models.StatusInProgress:
		printClaimCompletionGuidance(task.ID)
		fmt.Printf("  %s\n", styleSuccess("If this task becomes blocked:"))
		fmt.Printf("  %s\n", styleSuccess("`bl blocked "+task.ID+" --reason \"<reason>\"`"))
	case models.StatusBlocked:
		if showNext {
			printNextCommands("backlog why "+task.ID, "backlog blockers --suggest")
		}
	default:
		if showNext {
			printNextCommands("backlog show " + task.ID)
		}
	}
}

func statusIcon(status models.Status) string {
	return statusIconStyled(status)
}

func taskIDSet(taskIDs []string) map[string]struct{} {
	set := make(map[string]struct{}, len(taskIDs))
	for _, id := range taskIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		set[id] = struct{}{}
	}
	return set
}

func checkboxIconForTask(task models.Task, availableTaskIDs map[string]struct{}) string {
	if task.Status == models.StatusPending && strings.TrimSpace(task.ClaimedBy) == "" && len(availableTaskIDs) > 0 {
		if _, available := availableTaskIDs[task.ID]; !available {
			return dependencyBlockedIconStyled()
		}
	}
	return statusIcon(task.Status)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func min3(a, b, c int) int {
	return min(min(a, b), c)
}

func runClaim(args []string, metadata *gitAutoCommitMetadata) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdClaim)
		return nil
	}
	if err := validateAllowedFlagsForUsage(commands.CmdClaim, args, map[string]bool{
		"--agent":      true,
		"--force":      true,
		"--no-content": true,
		"--help":       true,
		"-h":           true,
	}); err != nil {
		return err
	}

	taskIDs := positionalArgs(args, map[string]bool{
		"--agent":      true,
		"--force":      false,
		"--no-content": false,
	})
	if len(taskIDs) == 0 {
		return printUsageError(commands.CmdClaim, errors.New("claim requires at least one TASK_ID"))
	}
	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	agent := parseOption(args, "--agent")
	if strings.TrimSpace(agent) == "" {
		agent = "cli-user"
	}
	force := parseFlag(args, "--force")
	noContent := parseFlag(args, "--no-content")

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}

	hasContext := false
	for _, id := range taskIDs {
		if err := validateTaskID(id); err != nil {
			return printUsageError(commands.CmdClaim, err)
		}
		task := tree.FindTask(id)
		isPrimaryTask := task != nil
		if isPrimaryTask {
			taskFilePath, err := resolveTaskFilePath(task.File)
			if err != nil {
				return err
			}
			_, _, warnings, missing, err := readTodoFrontmatter(task.ID, taskFilePath)
			if err != nil {
				return err
			}
			printTodoFileWarnings(warnings)
			if missing {
				return fmt.Errorf("Cannot claim %s because the task file is missing.", task.ID)
			}
			if task.Status == models.StatusDone {
				return claimDoneError(*task)
			}
			if task.ClaimedBy != "" && !force {
				return fmt.Errorf("Task %s is already claimed by %s", task.ID, task.ClaimedBy)
			}
			if task.Status != models.StatusPending {
				return fmt.Errorf("Cannot claim task %s: task is %s, not pending", task.ID, task.Status)
			}

			task.Status = models.StatusInProgress
			task.ClaimedBy = agent
			now := time.Now().UTC()
			task.ClaimedAt = &now
			task.StartedAt = &now

			if err := saveTaskState(*task, tree); err != nil {
				return err
			}
			if metadata.id == "" {
				metadata.id = task.ID
				metadata.title = task.Title
			}

			if !hasContext {
				if err := taskcontext.SetCurrentTask(dataDir, task.ID, agent); err != nil {
					return err
				}
				hasContext = true
			}

			if len(taskIDs) == 1 {
				renderTaskActionCard("✓ Claimed", *task, agent, dataDir, !noContent)
			} else {
				fmt.Printf("%s %s - %s\n", styleSuccess("✓ Claimed:"), task.ID, task.Title)
				if !noContent {
					for _, detail := range formatTaskDetails(*task) {
						fmt.Printf("  %s\n", detail)
					}
				}
				printTaskFileReadCommandsForTask(dataDir, *task, !noContent)
			}
			continue
		}
		fmt.Println(styleWarning("Warning: claim only works with task IDs."))
		fmt.Printf("Showing `backlog show %s` for context.\n", id)
		if err := runShow([]string{id}, false, false, false); err != nil {
			return err
		}
	}
	return nil
}

func runEdit(args []string, metadata *gitAutoCommitMetadata) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdEdit)
		return nil
	}
	if err := validateAllowedFlagsForUsage(commands.CmdEdit, args, map[string]bool{
		"--help": true,
		"-h":     true,
	}); err != nil {
		return err
	}

	taskIDs := positionalArgs(args, map[string]bool{
		"--help": true,
		"-h":     true,
	})
	if len(taskIDs) == 0 || len(taskIDs) > 1 {
		return printUsageError(commands.CmdEdit, errors.New("edit requires exactly one TASK_ID"))
	}

	taskID := taskIDs[0]
	if err := validateTaskID(taskID); err != nil {
		return printUsageError(commands.CmdEdit, err)
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	task := tree.FindTask(taskID)
	if task == nil {
		fmt.Println(styleWarning("Warning: edit only works with task IDs."))
		fmt.Printf("Showing `backlog show %s` for context.\n", taskID)
		if err := runShow([]string{taskID}, false, false, false); err != nil {
			return err
		}
		return nil
	}

	taskFilePath, err := resolveTaskFilePath(task.File)
	if err != nil {
		return err
	}
	if _, err := os.Stat(taskFilePath); err != nil {
		return fmt.Errorf("Cannot edit %s because the task file is missing.", task.ID)
	}

	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		return fmt.Errorf("No editor configured. Set EDITOR or VISUAL.")
	}

	editorParts := strings.Fields(editor)
	if len(editorParts) == 0 {
		return fmt.Errorf("No editor configured. Set EDITOR or VISUAL.")
	}
	command := editorParts[0]
	commandArgs := append(editorParts[1:], taskFilePath)

	editCmd := exec.Command(command, commandArgs...)
	editCmd.Stdin = os.Stdin
	editCmd.Stdout = os.Stdout
	editCmd.Stderr = os.Stderr
	if err := editCmd.Run(); err != nil {
		return err
	}

	metadata.id = task.ID
	metadata.title = task.Title
	return nil
}

func claimDoneError(task models.Task) error {
	if task.CompletedAt != nil {
		return fmt.Errorf("Cannot claim task %s: task is done (completed_at: %s)", task.ID, task.CompletedAt.Format(time.RFC3339))
	}
	return fmt.Errorf("Cannot claim task %s: task is done", task.ID)
}

func runCycle(args []string) error {
	if _, err := ensureDataDir(); err != nil {
		return err
	}
	if err := validateAllowedFlags(args, map[string]bool{"--agent": true, "--no-content": true}); err != nil {
		return err
	}
	taskID := firstPositionalArg(args, map[string]bool{
		"--agent":      true,
		"--no-content": true,
	})
	agent := strings.TrimSpace(parseOption(args, "--agent"))
	if strings.TrimSpace(agent) == "" {
		agent = "cli-user"
	}
	dataDir, err := ensureDataDir()
	if err != nil {
		return err
	}
	if strings.TrimSpace(taskID) == "" {
		ctx, err := taskcontext.LoadContext(dataDir)
		if err != nil {
			return err
		}
		if ctx.Agent != "" && ctx.Agent != agent {
			return errors.New("No task ID provided and no current working task set.")
		}
		taskID = ctx.CurrentTask
		if taskID == "" {
			taskID = ctx.PrimaryTask
		}
		if taskID == "" {
			return errors.New("No task ID provided and no current working task set.")
		}
	}
	if err := validateTaskID(taskID); err != nil {
		return err
	}
	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	task := findTask(tree, taskID)
	if task == nil {
		return fmt.Errorf("Task not found: %s", taskID)
	}

	if task.Status != models.StatusDone {
		if task.StartedAt != nil {
			duration := time.Since(*task.StartedAt).Minutes()
			task.DurationMinutes = &duration
		}
		if err := applyTaskStatusTransition(task, models.StatusDone, ""); err != nil {
			return err
		}
		if err := saveTaskState(*task, tree); err != nil {
			return err
		}
	}

	completion, err := setItemDone(*task, tree)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s - %s\n", styleSuccess("Completed:"), styleSuccess(task.ID), styleSuccess(task.Title))
	if task.DurationMinutes != nil {
		fmt.Printf("%s: %d minutes\n", styleSubHeader("Duration"), int(*task.DurationMinutes))
	}
	printCompletionNotice(tree, *task, completion)

	if completion.EpicCompleted || completion.MilestoneCompleted || completion.PhaseCompleted {
		if err := taskcontext.ClearContext(dataDir); err != nil {
			return err
		}
		fmt.Println(styleWarning("Review Required"))
		fmt.Println(styleMuted("Please review the completed work before continuing."))
		fmt.Println(styleMuted("Run 'backlog grab' after review."))
		return nil
	}

	if handled, err := advanceCycleContext(task.ID, agent, dataDir); err != nil {
		return err
	} else if handled {
		return nil
	}

	cfg := map[string]float64{}
	refreshedTree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	calculator := critical_path.NewCriticalPathCalculator(refreshedTree, cfg)
	_, nextAvailable, err := calculator.Calculate()
	if err != nil {
		return err
	}
	if strings.TrimSpace(nextAvailable) == "" {
		dataDir, err := ensureDataDir()
		if err != nil {
			return err
		}
		if err := taskcontext.ClearContext(dataDir); err != nil {
			return err
		}
		fmt.Println(styleWarning("No more available tasks."))
		return nil
	}
	return grabTaskByID(refreshedTree, *calculator, nextAvailable, dataDirFromContext(), agent)
}

func advanceCycleContext(taskID string, agent string, dataDir string) (bool, error) {
	ctx, err := taskcontext.LoadContext(dataDir)
	if err != nil {
		return false, err
	}
	if ctx.Agent != "" && ctx.Agent != agent {
		return false, nil
	}

	switch ctx.Mode {
	case "siblings":
		if strings.TrimSpace(ctx.PrimaryTask) == "" {
			return false, nil
		}
		if taskID != ctx.PrimaryTask && !taskIDInList(ctx.SiblingTasks, taskID) {
			return false, nil
		}

		primary := ctx.PrimaryTask
		siblings := append([]string{}, ctx.SiblingTasks...)
		if taskID == primary {
			if len(siblings) == 0 {
				return false, nil
			}
			newPrimary := siblings[0]
			remaining := siblings[1:]
			if len(remaining) > 0 {
				if err := taskcontext.SetSiblingTaskContext(dataDir, agent, newPrimary, remaining); err != nil {
					return false, err
				}
			} else {
				if err := taskcontext.SetCurrentTask(dataDir, newPrimary, agent); err != nil {
					return false, err
				}
			}
			fmt.Printf("%s %s\n", styleSuccess("Primary sibling completed. Next sibling:"), styleSuccess(newPrimary))
			return true, nil
		}

		remaining := removeTaskFromList(siblings, taskID)
		if len(remaining) > 0 {
			if err := taskcontext.SetSiblingTaskContext(dataDir, agent, primary, remaining); err != nil {
				return false, err
			}
		} else {
			if err := taskcontext.SetCurrentTask(dataDir, primary, agent); err != nil {
				return false, err
			}
		}
		fmt.Printf("%s %s\n", styleSuccess("Sibling task completed. Returning to primary:"), styleSuccess(primary))
		return true, nil

	case "multi":
		if strings.TrimSpace(ctx.PrimaryTask) == "" {
			return false, nil
		}
		if taskID != ctx.PrimaryTask && !taskIDInList(ctx.AdditionalTasks, taskID) {
			return false, nil
		}

		primary := ctx.PrimaryTask
		additional := append([]string{}, ctx.AdditionalTasks...)
		if taskID == primary {
			if len(additional) == 0 {
				return false, nil
			}
			newPrimary := additional[0]
			remaining := additional[1:]
			if len(remaining) > 0 {
				if err := taskcontext.SetMultiTaskContext(dataDir, agent, newPrimary, remaining); err != nil {
					return false, err
				}
			} else {
				if err := taskcontext.SetCurrentTask(dataDir, newPrimary, agent); err != nil {
					return false, err
				}
			}
			fmt.Printf("%s\n", styleSuccess("Primary task completed. Additional tasks still active."))
			fmt.Printf("%s %s\n", styleSuccess("New primary task:"), styleSuccess(newPrimary))
			return true, nil
		}

		remaining := removeTaskFromList(additional, taskID)
		if len(remaining) > 0 {
			if err := taskcontext.SetMultiTaskContext(dataDir, agent, primary, remaining); err != nil {
				return false, err
			}
		} else {
			if err := taskcontext.SetCurrentTask(dataDir, primary, agent); err != nil {
				return false, err
			}
		}
		fmt.Printf("%s %s\n", styleSuccess("Additional task completed. Returning to primary:"), styleSuccess(primary))
		return true, nil
	}
	return false, nil
}

func taskIDInList(list []string, target string) bool {
	for _, value := range list {
		if value == target {
			return true
		}
	}
	return false
}

func removeTaskFromList(list []string, target string) []string {
	out := make([]string, 0, len(list))
	for _, value := range list {
		if value == target {
			continue
		}
		out = append(out, value)
	}
	return out
}

func grabTaskByID(tree models.TaskTree, calc critical_path.CriticalPathCalculator, taskID string, dataDir string, agent string) error {
	primary := tree.FindTask(taskID)
	if primary == nil {
		return fmt.Errorf("Task not found: %s", taskID)
	}
	if _, err := resolveTaskFilePath(primary.File); err != nil || !taskFileExists(primary.File) {
		return fmt.Errorf("Cannot claim %s because the task file is missing.", primary.ID)
	}
	if err := claimTaskInTree(primary, agent, time.Now().UTC(), tree); err != nil {
		return err
	}

	candidateIDs, err := findGrabCandidates(*primary, &calc, tree)
	if err != nil {
		return err
	}
	additional := []models.Task{}
	for _, candidateID := range candidateIDs {
		if len(additional) >= grabSiblingAdditionalMax {
			break
		}
		candidate := findTask(tree, candidateID)
		if candidate == nil {
			continue
		}
		if candidate.Status != models.StatusPending || candidate.ClaimedBy != "" {
			continue
		}
		if !taskFileExists(candidate.File) {
			continue
		}
		if err := claimTaskInTree(candidate, agent, time.Now().UTC(), tree); err != nil {
			return err
		}
		additional = append(additional, *candidate)
	}

	if len(additional) > 0 {
		additionalIDs := make([]string, len(additional))
		for i, item := range additional {
			additionalIDs[i] = item.ID
		}
		if isBugLikeID(primary.ID) {
			if err := taskcontext.SetMultiTaskContext(dataDir, agent, primary.ID, additionalIDs); err != nil {
				return err
			}
		} else {
			if err := taskcontext.SetSiblingTaskContext(dataDir, agent, primary.ID, additionalIDs); err != nil {
				return err
			}
		}
		fmt.Printf("%s %s - %s\n", styleSuccess("Grabbed:"), styleSuccess(primary.ID), styleSuccess(primary.Title))
		fmt.Printf("%s %d additional task(s): %s\n", styleSuccess("Also grabbed"), len(additional), strings.Join(additionalIDs, ", "))
		printTaskFileReadCommandsForTask(dataDir, *primary, true)
		for _, task := range additional {
			printTaskFileReadCommandsForTask(dataDir, task, true)
		}
		return nil
	}

	if err := taskcontext.SetCurrentTask(dataDir, primary.ID, agent); err != nil {
		return err
	}
	fmt.Printf("%s %s - %s\n", styleSuccess("Grabbed:"), styleSuccess(primary.ID), styleSuccess(primary.Title))
	printTaskFileReadCommandsForTask(dataDir, *primary, true)
	return nil
}

func setItemDone(task models.Task, tree models.TaskTree) (completionNotice, error) {
	if strings.TrimSpace(task.EpicID) == "" || strings.TrimSpace(task.MilestoneID) == "" || strings.TrimSpace(task.PhaseID) == "" {
		return completionNotice{}, nil
	}

	epic := tree.FindEpic(task.EpicID)
	milestone := tree.FindMilestone(task.MilestoneID)
	phase := tree.FindPhase(task.PhaseID)
	if epic == nil {
		return completionNotice{}, fmt.Errorf("Epic not found: %s", task.EpicID)
	}
	if milestone == nil {
		return completionNotice{}, fmt.Errorf("Milestone not found: %s", task.MilestoneID)
	}
	if phase == nil {
		return completionNotice{}, fmt.Errorf("Phase not found: %s", task.PhaseID)
	}

	epicCompleted := true
	for _, child := range epic.Tasks {
		if child.Status != models.StatusDone {
			epicCompleted = false
			break
		}
	}

	milestoneCompleted := true
	for _, childEpic := range milestone.Epics {
		for _, childTask := range childEpic.Tasks {
			if childTask.Status != models.StatusDone {
				milestoneCompleted = false
				break
			}
		}
		if !milestoneCompleted {
			break
		}
	}

	phaseCompleted := true
	for _, childMilestone := range phase.Milestones {
		for _, childEpic := range childMilestone.Epics {
			for _, childTask := range childEpic.Tasks {
				if childTask.Status != models.StatusDone {
					phaseCompleted = false
					break
				}
			}
			if !phaseCompleted {
				break
			}
		}
		if !phaseCompleted {
			break
		}
	}

	completion := completionNotice{
		EpicCompleted:      epicCompleted,
		MilestoneCompleted: milestoneCompleted,
		PhaseCompleted:     phaseCompleted,
	}

	dataDir, err := ensureDataDir()
	if err != nil {
		return completionNotice{}, err
	}

	rootPath := filepath.Join(dataDir, "index.yaml")
	rootIndex, err := readYAMLMapFile(rootPath)
	if err != nil {
		return completionNotice{}, err
	}
	phasesRaw := asSlice(rootIndex["phases"])
	for _, raw := range phasesRaw {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if !tree.IDsMatch(asString(entry["id"]), phase.ID) {
			continue
		}
		if phaseCompleted {
			entry["status"] = string(models.StatusDone)
			entry["locked"] = true
		}
		break
	}
	if err := writeYAMLMapFile(rootPath, rootIndex); err != nil {
		return completionNotice{}, err
	}

	phaseIndexPath := filepath.Join(dataDir, phase.Path, "index.yaml")
	phaseIndex, err := readYAMLMapFile(phaseIndexPath)
	if err != nil {
		return completionNotice{}, err
	}
	if phaseCompleted {
		phaseIndex["status"] = string(models.StatusDone)
		phaseIndex["locked"] = true
	}
	if milestoneCompleted {
		for _, raw := range asSlice(phaseIndex["milestones"]) {
			entry, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if tree.IDsMatch(asString(entry["id"]), milestone.ID) {
				entry["status"] = string(models.StatusDone)
				break
			}
		}
	}
	if err := writeYAMLMapFile(phaseIndexPath, phaseIndex); err != nil {
		return completionNotice{}, err
	}

	milestoneIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, "index.yaml")
	milestoneIndex, err := readYAMLMapFile(milestoneIndexPath)
	if err != nil {
		return completionNotice{}, err
	}
	if milestoneCompleted {
		milestoneIndex["status"] = string(models.StatusDone)
	}
	if epicCompleted {
		for _, raw := range asSlice(milestoneIndex["epics"]) {
			entry, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if tree.IDsMatch(asString(entry["id"]), epic.ID) {
				entry["status"] = string(models.StatusDone)
				break
			}
		}
	}
	if err := writeYAMLMapFile(milestoneIndexPath, milestoneIndex); err != nil {
		return completionNotice{}, err
	}

	epicIndexPath := filepath.Join(dataDir, phase.Path, milestone.Path, epic.Path, "index.yaml")
	epicIndex, err := readYAMLMapFile(epicIndexPath)
	if err != nil {
		return completionNotice{}, err
	}
	if epicCompleted {
		epicIndex["status"] = string(models.StatusDone)
	}
	if err := writeYAMLMapFile(epicIndexPath, epicIndex); err != nil {
		return completionNotice{}, err
	}

	return completion, nil
}

func printCompletionNotice(tree models.TaskTree, task models.Task, notice completionNotice) {
	if !notice.EpicCompleted && !notice.MilestoneCompleted && !notice.PhaseCompleted {
		return
	}

	dataDir := dataDirFromContext()
	dataDirBase := ".tasks"
	if dataDir != "" {
		dataDirBase = filepath.Base(dataDir)
	}

	printNextSteps := func(title, scopeType string, scopeName string, scopeID string, scopePath string, nextSteps ...string) {
		fmt.Println(styleMuted("════════════════════════════════════════════════════════════════════"))
		fmt.Printf("%s %s (%s)\n", styleSuccess(fmt.Sprintf("%s COMPLETE:", scopeType)), styleSubHeader(scopeName), styleSuccess(scopeID))
		if scopePath != "" {
			fmt.Printf("%s: %s\n", styleSubHeader("Path"), styleSuccess(scopePath))
		}
		fmt.Println(strings.Repeat("=", 60))
		fmt.Println()
		fmt.Println(styleSubHeader("NEXT_STEPS:"))
		fmt.Println(styleSubHeader(title))
		for i, step := range nextSteps {
			fmt.Printf("  %d. %s\n", i+1, step)
		}
		fmt.Println()
	}

	if notice.EpicCompleted {
		epic := findEpic(tree, task.EpicID)
		if epic != nil {
			phase := tree.FindPhase(epic.PhaseID)
			milestone := tree.FindMilestone(epic.MilestoneID)
			epicPath := ".tasks"
			if phase != nil && milestone != nil {
				epicPath = filepath.Join(dataDirBase, phase.Path, milestone.Path, epic.Path)
			} else if epic.Path != "" {
				epicPath = filepath.Join(dataDirBase, epic.Path)
			}
			printNextSteps(
				"Review the completed epic before moving on.",
				"EPIC",
				epic.Name,
				epic.ID,
				filepath.ToSlash(epicPath),
				"Spawn a review subagent to verify implementation",
				fmt.Sprintf("Check acceptance criteria in %s are met", filepath.ToSlash(epicPath)),
				"Ensure integration between all tasks in the epic",
				"Run liter (if available) and fix any warnings",
			)
			fmt.Println()
		}
	}

	if notice.MilestoneCompleted {
		milestone := findMilestone(tree, task.MilestoneID)
		if milestone != nil && len(milestone.Epics) > 1 {
			phase := tree.FindPhase(task.PhaseID)
			milestonePath := "milestone"
			if phase != nil {
				milestonePath = filepath.ToSlash(filepath.Join(dataDirBase, phase.Path, milestone.Path))
			}
			printNextSteps(
				"Review the completed milestone before moving on.",
				"MILESTONE",
				milestone.Name,
				milestone.ID,
				milestonePath,
				"Spawn a comprehensive review subagent (in addition to the epic review agent)",
				"Verify all epics integrate correctly together",
				"Run full test suite",
				"Run linter (if available) and fix any warnings",
			)
			fmt.Println()
		}
	}

	if notice.PhaseCompleted {
		phase := tree.FindPhase(task.PhaseID)
		if phase != nil {
			phasePath := "phase"
			if phase.Path != "" {
				phasePath = filepath.ToSlash(filepath.Join(dataDirBase, phase.Path))
			}
			printNextSteps(
				"Review the completed phase before moving on.",
				"PHASE",
				phase.Name,
				phase.ID,
				phasePath,
				"Run project-level verification checks",
				"Confirm phase lock and mark follow-up items",
			)
			fmt.Println()
		}
	}
}

func runWork(args []string) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if err := validateAllowedFlagsForUsage(commands.CmdWork, args, map[string]bool{
		"--agent": true,
		"--clear": true,
		"--help":  true,
		"-h":      true,
	}); err != nil {
		return err
	}
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdWork)
		return nil
	}

	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	clearContext := parseFlag(args, "--clear")
	if clearContext {
		if len(positionalArgs(args, map[string]bool{
			"--agent": true,
		})) > 0 {
			return printUsageError(commands.CmdWork, errors.New("work --clear does not accept a TASK_ID"))
		}
		if err := taskcontext.ClearContext(dataDir); err != nil {
			return err
		}
		fmt.Println(styleSuccess("Cleared working task context."))
		return nil
	}

	targetTasks := positionalArgs(args, map[string]bool{
		"--agent": true,
	})
	if len(targetTasks) > 1 {
		return printUsageError(commands.CmdWork, errors.New("work accepts at most one TASK_ID"))
	}

	targetTask := ""
	if len(targetTasks) == 1 {
		targetTask = targetTasks[0]
	}

	agent := strings.TrimSpace(parseOption(args, "--agent"))

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}

	if targetTask != "" {
		if err := validateTaskID(targetTask); err != nil {
			return printUsageError(commands.CmdWork, err)
		}
		task := tree.FindTask(targetTask)
		if task == nil {
			return fmt.Errorf("Task not found: %s", targetTask)
		}
		if err := taskcontext.SetCurrentTask(dataDir, task.ID, agent); err != nil {
			return err
		}
		fmt.Printf("%s %s - %s\n", styleSuccess("Working task set:"), styleSuccess(task.ID), task.Title)
		return nil
	}

	ctx, err := taskcontext.LoadContext(dataDir)
	if err != nil {
		return err
	}
	currentTask := ctx.CurrentTask
	if currentTask == "" {
		currentTask = ctx.PrimaryTask
	}
	if currentTask == "" {
		fmt.Println(styleWarning("No current working task set."))
		return nil
	}

	task := tree.FindTask(currentTask)
	if task == nil {
		fmt.Printf("%s '%s' not found in tree.\n", styleWarning("Working task"), currentTask)
		return nil
	}

	fmt.Println(styleHeader("Current Working Task"))
	fmt.Printf("%s %s\n", styleSubHeader("ID:"), task.ID)
	fmt.Printf("%s %s\n", styleSubHeader("Title:"), task.Title)
	fmt.Printf("%s %s\n", styleSubHeader("Status:"), styleStatusText(string(task.Status)))
	fmt.Printf("%s %.2f hours\n", styleSubHeader("Estimate:"), task.EstimateHours)
	fmt.Printf("%s %s\n", styleSubHeader("File:"), filepath.Join(dataDir, task.File))
	return nil
}

func runVersion(args []string) error {
	if err := validateAllowedFlagsForUsage(commands.CmdVersion, args, map[string]bool{
		"--help": true,
		"-h":     true,
	}); err != nil {
		return err
	}
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdVersion)
		return nil
	}
	if len(positionalArgs(args, nil)) > 0 {
		return printUsageError(commands.CmdVersion, errors.New("version accepts no TASK_ID arguments"))
	}

	root := cmd.NewRootCommand()
	fmt.Printf("%s version %s\n", styleSuccess(root.Name()), root.Version())
	return nil
}

func runVelocity(args []string) error {
	return runReportVelocity(args)
}

func runMove(args []string) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdMove)
		return nil
	}
	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	if err := validateAllowedFlagsForUsage(commands.CmdMove, args, map[string]bool{
		"--to":   true,
		"--help": true,
		"-h":     true,
	}); err != nil {
		return err
	}

	source := firstPositionalArg(args, map[string]bool{
		"--to": true,
	})
	if source == "" {
		return printUsageError(commands.CmdMove, errors.New("move requires SOURCE_ID"))
	}
	if err := validateTaskID(source); err != nil {
		return printUsageError(commands.CmdMove, err)
	}

	dest := parseOption(args, "--to")
	if dest == "" {
		return printUsageError(commands.CmdMove, errors.New("move requires --to DEST_ID"))
	}
	if err := validateTaskID(dest); err != nil {
		return printUsageError(commands.CmdMove, err)
	}

	sourcePath, err := models.ParseTaskPath(source)
	if err != nil {
		return printUsageError(commands.CmdMove, fmt.Errorf("invalid task id: %s", source))
	}
	destPath, err := models.ParseTaskPath(dest)
	if err != nil {
		return printUsageError(commands.CmdMove, fmt.Errorf("invalid task id: %s", dest))
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}

	remap := map[string]string{}
	switch {
	case sourcePath.IsTask() && destPath.IsEpic():
		task := tree.FindTask(source)
		if task == nil {
			return fmt.Errorf("Task not found: %s", source)
		}
		destEpic := tree.FindEpic(dest)
		if destEpic == nil {
			return fmt.Errorf("Epic not found: %s", dest)
		}

		srcEpic := tree.FindEpic(task.EpicID)
		srcMilestone := tree.FindMilestone(task.MilestoneID)
		srcPhase := tree.FindPhase(task.PhaseID)
		dstMilestone := tree.FindMilestone(destEpic.MilestoneID)
		dstPhase := tree.FindPhase(destEpic.PhaseID)
		if srcEpic == nil || srcMilestone == nil || srcPhase == nil || dstMilestone == nil || dstPhase == nil {
			return errors.New("Could not resolve source/destination hierarchy paths")
		}

		oldTaskPath, err := resolveTaskFilePath(task.File)
		if err != nil {
			return err
		}
		if _, err := os.Stat(oldTaskPath); err != nil {
			return fmt.Errorf("Task file not found: %s", oldTaskPath)
		}

		srcEpicDir := filepath.Join(dataDir, srcPhase.Path, srcMilestone.Path, srcEpic.Path)
		dstEpicDir := filepath.Join(dataDir, dstPhase.Path, dstMilestone.Path, destEpic.Path)
		srcEpicIndexPath := filepath.Join(srcEpicDir, "index.yaml")
		dstEpicIndexPath := filepath.Join(dstEpicDir, "index.yaml")

		srcEpicIndex, err := readYAMLMapFile(srcEpicIndexPath)
		if err != nil {
			return err
		}
		dstEpicIndex, err := readYAMLMapFile(dstEpicIndexPath)
		if err != nil {
			return err
		}

		nextTask := nextIndexID(extractTaskLeafIDs(dstEpicIndex["tasks"]), "T")
		newTaskShort := fmt.Sprintf("T%03d", nextTask)
		newTaskID := fmt.Sprintf("%s.%s", destEpic.ID, newTaskShort)
		newFilename := fmt.Sprintf("%s-%s.todo", newTaskShort, models.Slugify(task.Title, models.DirectoryNameWidth*15))
		if err := os.Rename(oldTaskPath, filepath.Join(dstEpicDir, newFilename)); err != nil {
			return err
		}

		oldFilename := filepath.Base(oldTaskPath)
		srcTasks := asSlice(srcEpicIndex["tasks"])
		filteredTasks := make([]interface{}, 0, len(srcTasks))
		for _, entry := range srcTasks {
			value, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}
			file := asString(value["file"])
			if file == "" {
				file = asString(value["path"])
			}
			if asString(value["id"]) == sourcePath.Task || asString(value["id"]) == task.ID || file == oldFilename {
				continue
			}
			filteredTasks = append(filteredTasks, entry)
		}
		srcEpicIndex["tasks"] = filteredTasks
		if err := writeYAMLMapFile(srcEpicIndexPath, srcEpicIndex); err != nil {
			return err
		}

		dstTasks := asSlice(dstEpicIndex["tasks"])
		dstTasks = append(dstTasks, map[string]interface{}{
			"id":             newTaskShort,
			"file":           newFilename,
			"title":          task.Title,
			"status":         string(task.Status),
			"estimate_hours": task.EstimateHours,
			"complexity":     string(task.Complexity),
			"priority":       string(task.Priority),
			"depends_on":     task.DependsOn,
		})
		dstEpicIndex["tasks"] = dstTasks
		if err := writeYAMLMapFile(dstEpicIndexPath, dstEpicIndex); err != nil {
			return err
		}

		remap[source] = newTaskID

	case sourcePath.IsEpic() && destPath.IsMilestone():
		srcEpic := tree.FindEpic(source)
		if srcEpic == nil {
			return fmt.Errorf("Epic not found: %s", source)
		}
		dstMilestone := tree.FindMilestone(dest)
		if dstMilestone == nil {
			return fmt.Errorf("Milestone not found: %s", dest)
		}

		srcMilestone := tree.FindMilestone(srcEpic.MilestoneID)
		srcPhase := tree.FindPhase(srcEpic.PhaseID)
		dstPhase := tree.FindPhase(dstMilestone.PhaseID)
		if srcMilestone == nil || srcPhase == nil || dstPhase == nil {
			return errors.New("Could not resolve source/destination hierarchy paths")
		}

		srcEpicDir := filepath.Join(dataDir, srcPhase.Path, srcMilestone.Path, srcEpic.Path)
		srcMsIndexPath := filepath.Join(dataDir, srcPhase.Path, srcMilestone.Path, "index.yaml")
		dstMsDir := filepath.Join(dataDir, dstPhase.Path, dstMilestone.Path)
		dstMsIndexPath := filepath.Join(dstMsDir, "index.yaml")

		dstMilestoneIndex, err := readYAMLMapFile(dstMsIndexPath)
		if err != nil {
			return err
		}
		nextEpic := nextIndexID(extractEpicOrMilestoneLeafIDs(dstMilestoneIndex["epics"], "E"), "E")
		newEpicShort := fmt.Sprintf("E%d", nextEpic)
		newEpicID := fmt.Sprintf("%s.%s", dstMilestone.ID, newEpicShort)
		newEpicDirName := fmt.Sprintf("%02d-%s", nextEpic, models.Slugify(srcEpic.Name, models.DirectoryNameWidth*15))
		if err := os.Rename(srcEpicDir, filepath.Join(dstMsDir, newEpicDirName)); err != nil {
			return err
		}

		srcMsIndex, err := readYAMLMapFile(srcMsIndexPath)
		if err != nil {
			return err
		}
		srcEpics := asSlice(srcMsIndex["epics"])
		filteredEpics := make([]interface{}, 0, len(srcEpics))
		for _, entry := range srcEpics {
			value, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}
			if asString(value["id"]) == source || asString(value["path"]) == srcEpic.Path {
				continue
			}
			filteredEpics = append(filteredEpics, entry)
		}
		srcMsIndex["epics"] = filteredEpics
		if err := writeYAMLMapFile(srcMsIndexPath, srcMsIndex); err != nil {
			return err
		}

		dstMilestones := asSlice(dstMilestoneIndex["epics"])
		dstMilestones = append(dstMilestones, map[string]interface{}{
			"id":             newEpicShort,
			"name":           srcEpic.Name,
			"path":           newEpicDirName,
			"status":         string(srcEpic.Status),
			"estimate_hours": srcEpic.EstimateHours,
			"complexity":     string(srcEpic.Complexity),
			"depends_on":     srcEpic.DependsOn,
			"description":    srcEpic.Description,
		})
		dstMilestoneIndex["epics"] = dstMilestones
		if err := writeYAMLMapFile(dstMsIndexPath, dstMilestoneIndex); err != nil {
			return err
		}

		remap[source] = newEpicID
		for _, task := range srcEpic.Tasks {
			remap[task.ID] = strings.Replace(task.ID, source+".", newEpicID+".", 1)
		}

	case sourcePath.IsMilestone() && destPath.IsPhase():
		srcMilestone := tree.FindMilestone(source)
		if srcMilestone == nil {
			return fmt.Errorf("Milestone not found: %s", source)
		}
		dstPhase := tree.FindPhase(dest)
		if dstPhase == nil {
			return fmt.Errorf("Phase not found: %s", dest)
		}

		srcPhase := tree.FindPhase(srcMilestone.PhaseID)
		if srcPhase == nil {
			return errors.New("Could not resolve source phase")
		}

		srcMsDir := filepath.Join(dataDir, srcPhase.Path, srcMilestone.Path)
		srcPhaseIndexPath := filepath.Join(dataDir, srcPhase.Path, "index.yaml")
		dstPhaseDir := filepath.Join(dataDir, dstPhase.Path)
		dstPhaseIndexPath := filepath.Join(dstPhaseDir, "index.yaml")

		dstPhaseIndex, err := readYAMLMapFile(dstPhaseIndexPath)
		if err != nil {
			return err
		}
		nextMilestone := nextIndexID(extractEpicOrMilestoneLeafIDs(dstPhaseIndex["milestones"], "M"), "M")
		newMilestoneShort := fmt.Sprintf("M%d", nextMilestone)
		newMilestoneID := fmt.Sprintf("%s.%s", dstPhase.ID, newMilestoneShort)
		newMsDirName := fmt.Sprintf("%02d-%s", nextMilestone, models.Slugify(srcMilestone.Name, models.DirectoryNameWidth*15))
		if err := os.Rename(srcMsDir, filepath.Join(dstPhaseDir, newMsDirName)); err != nil {
			return err
		}

		srcPhaseIndex, err := readYAMLMapFile(srcPhaseIndexPath)
		if err != nil {
			return err
		}
		srcMilestones := asSlice(srcPhaseIndex["milestones"])
		filteredMilestones := make([]interface{}, 0, len(srcMilestones))
		for _, entry := range srcMilestones {
			value, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}
			if asString(value["id"]) == source || asString(value["path"]) == srcMilestone.Path {
				continue
			}
			filteredMilestones = append(filteredMilestones, entry)
		}
		srcPhaseIndex["milestones"] = filteredMilestones
		if err := writeYAMLMapFile(srcPhaseIndexPath, srcPhaseIndex); err != nil {
			return err
		}

		dstMilestones := asSlice(dstPhaseIndex["milestones"])
		dstMilestones = append(dstMilestones, map[string]interface{}{
			"id":             newMilestoneShort,
			"name":           srcMilestone.Name,
			"path":           newMsDirName,
			"status":         string(srcMilestone.Status),
			"estimate_hours": srcMilestone.EstimateHours,
			"complexity":     string(srcMilestone.Complexity),
			"depends_on":     srcMilestone.DependsOn,
			"description":    srcMilestone.Description,
		})
		dstPhaseIndex["milestones"] = dstMilestones
		if err := writeYAMLMapFile(dstPhaseIndexPath, dstPhaseIndex); err != nil {
			return err
		}

		remap[source] = newMilestoneID
		for _, epic := range srcMilestone.Epics {
			newEpicID := strings.Replace(epic.ID, source+".", newMilestoneID+".", 1)
			remap[epic.ID] = newEpicID
			for _, task := range epic.Tasks {
				remap[task.ID] = strings.Replace(task.ID, epic.ID+".", newEpicID+".", 1)
			}
		}

	default:
		return printUsageError(commands.CmdMove, errors.New("invalid move: supported moves are task->epic, epic->milestone, milestone->phase"))
	}

	if err := applyIdRemap(remap, dataDir); err != nil {
		return err
	}

	fmt.Printf("%s %s\n", styleSuccess("Moved:"), styleSuccess(source))
	fmt.Printf("%s %s\n", styleSuccess("To:"), styleSuccess(dest))
	fmt.Printf("%s %s\n", styleSuccess("New ID:"), styleSuccess(remap[source]))
	printNextCommands(
		"backlog show "+remap[source],
		"backlog check",
	)
	return nil
}

func runSync() error {
	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	cfg := map[string]float64{}
	calculator := critical_path.NewCriticalPathCalculator(tree, cfg)
	criticalPath, nextAvailable, err := calculator.CalculateForTaskDependencies()
	if err != nil {
		return err
	}
	warnNonTaskCycle(calculator)

	allTasks := tree.AllTasks()
	totalTasks := 0
	doneTasks := 0
	inProgressTasks := 0
	blockedTasks := 0
	pendingTasks := 0
	for _, task := range allTasks {
		totalTasks++
		switch task.Status {
		case models.StatusDone:
			doneTasks++
		case models.StatusInProgress:
			inProgressTasks++
		case models.StatusBlocked:
			blockedTasks++
		case models.StatusPending:
			pendingTasks++
		}
	}

	rootPath := filepath.Join(dataDir, "index.yaml")
	root, err := readYAMLMapFile(rootPath)
	if err != nil {
		return err
	}
	root["critical_path"] = criticalPath
	if strings.TrimSpace(nextAvailable) == "" {
		root["next_available"] = nil
	} else {
		root["next_available"] = nextAvailable
	}
	root["stats"] = map[string]interface{}{
		"total_tasks": totalTasks,
		"done":        doneTasks,
		"in_progress": inProgressTasks,
		"blocked":     blockedTasks,
		"pending":     pendingTasks,
	}
	if err := writeYAMLMapFile(rootPath, root); err != nil {
		return err
	}
	if err := writeSyncDerivedStats(dataDir, tree); err != nil {
		return err
	}
	fmt.Println(styleSuccess("Synced"))
	return nil
}

func runUnclaim(args []string, metadata *gitAutoCommitMetadata) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	taskID := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		taskID = arg
		break
	}
	if taskID == "" {
		dataDir, err := ensureDataRoot()
		if err != nil {
			return err
		}
		ctx, err := taskcontext.LoadContext(dataDir)
		if err != nil {
			return err
		}
		taskID = ctx.CurrentTask
		if taskID == "" {
			taskID = ctx.PrimaryTask
		}
	}
	if taskID == "" {
		return errors.New("No task ID provided and no current working task set.")
	}
	if err := validateTaskID(taskID); err != nil {
		return err
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	task := tree.FindTask(taskID)
	if task == nil {
		return fmt.Errorf("Task not found: %s", taskID)
	}

	if task.Status != models.StatusInProgress && task.Status != models.StatusPending {
		fmt.Printf("%s %s\n", styleError("Task is not in progress:"), task.Status)
		return nil
	}
	if task.Status == models.StatusInProgress {
		if err := applyTaskStatusTransition(task, models.StatusPending, "unclaim"); err != nil {
			return err
		}
	} else if task.ClaimedBy == "" && task.ClaimedAt == nil {
		fmt.Printf("%s %s\n", styleError("Task is not in progress:"), task.Status)
		return nil
	} else {
		task.ClaimedBy = ""
		task.ClaimedAt = nil
	}

	if err := saveTaskState(*task, tree); err != nil {
		return err
	}
	if metadata.id == "" {
		metadata.id = task.ID
		metadata.title = task.Title
	}
	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	if err := taskcontext.ClearContext(dataDir); err != nil {
		return err
	}
	fmt.Printf("%s %s - %s\n", styleSuccess("Unclaimed:"), styleSuccess(task.ID), styleSuccess(task.Title))
	printNextCommands(
		"backlog grab",
		"backlog show "+task.ID,
	)
	return nil
}

func runBlocked(args []string) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if parseFlag(args, "--help", "-h") {
		printUsageForCommand(commands.CmdBlocked)
		return nil
	}
	if err := validateAllowedFlagsForUsage(
		commands.CmdBlocked,
		args,
		map[string]bool{
			"--reason": true,
			"-r":       true,
			"--agent":  true,
			"--grab":   true,
			"--help":   true,
			"-h":       true,
		},
	); err != nil {
		return err
	}

	taskID := firstPositionalArg(args, map[string]bool{
		"--reason": true,
		"-r":       true,
		"--agent":  true,
		"--grab":   true,
	})
	reason := strings.TrimSpace(parseOption(args, "--reason", "-r"))
	if reason == "" {
		return printUsageError(commands.CmdBlocked, errors.New("blocked requires --reason"))
	}

	if taskID == "" {
		dataDir, err := ensureDataRoot()
		if err != nil {
			return err
		}
		ctx, err := taskcontext.LoadContext(dataDir)
		if err != nil {
			return err
		}
		taskID = ctx.CurrentTask
		if taskID == "" {
			taskID = ctx.PrimaryTask
		}
		if taskID == "" {
			return errors.New("No task ID provided and no current working task set.")
		}
	}
	if err := validateTaskID(taskID); err != nil {
		return printUsageError(commands.CmdBlocked, err)
	}

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	task := tree.FindTask(taskID)
	if task == nil {
		return fmt.Errorf("Task not found: %s", taskID)
	}
	if err := applyTaskStatusTransition(task, models.StatusBlocked, reason); err != nil {
		return err
	}
	if err := saveTaskState(*task, tree); err != nil {
		return err
	}

	dataDir, err := ensureDataRoot()
	if err != nil {
		return err
	}
	if err := taskcontext.ClearContext(dataDir); err != nil {
		return err
	}

	fmt.Printf("%s %s (%s)\n", styleWarning("Blocked:"), styleSuccess(task.ID), styleWarning(reason))
	if !parseFlag(args, "--grab") {
		fmt.Println(styleWarning("Tip: Run `backlog grab` to claim the next available task."))
		printNextCommands(
			"backlog why "+task.ID,
			"backlog blockers --suggest",
		)
		return nil
	}

	tree, err = loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	cfg := map[string]float64{}
	calculator := critical_path.NewCriticalPathCalculator(tree, cfg)
	_, nextAvailable, err := calculator.Calculate()
	if err != nil {
		return err
	}
	if strings.TrimSpace(nextAvailable) == "" {
		fmt.Println(styleWarning("No available tasks found."))
		return nil
	}
	next := tree.FindTask(nextAvailable)
	if next == nil {
		fmt.Println(styleWarning("No available tasks found."))
		return nil
	}
	if _, err := resolveTaskFilePath(next.File); err != nil || !taskFileExists(next.File) {
		fmt.Printf("%s: %s has no task file.\n", styleWarning("Skipping auto-grab"), styleMuted(next.ID))
		return nil
	}

	agent := strings.TrimSpace(parseOption(args, "--agent"))
	if agent == "" {
		agent = "cli-user"
	}
	if err := grabTaskByID(tree, *calculator, next.ID, dataDirFromContext(), agent); err != nil {
		return err
	}
	printNextCommands("backlog show " + next.ID)
	return nil
}

func runDone(args []string, metadata *gitAutoCommitMetadata) error {
	if _, err := ensureDataRoot(); err != nil {
		return err
	}
	if err := validateAllowedFlags(args, map[string]bool{
		"--status": true,
		"--force":  true,
		"--verify": true,
	}); err != nil {
		return err
	}

	taskIDs := positionalArgs(args, map[string]bool{
		"--status": true,
		"--force":  false,
		"--verify": false,
	})
	if len(taskIDs) == 0 {
		dataDir, err := ensureDataRoot()
		if err != nil {
			return err
		}
		ctx, err := taskcontext.LoadContext(dataDir)
		if err != nil {
			return err
		}
		taskID := ctx.CurrentTask
		if taskID == "" {
			taskID = ctx.PrimaryTask
		}
		if taskID == "" {
			return errors.New("No task ID provided and no current working task set.")
		}
		taskIDs = []string{taskID}
	}

	for _, taskID := range taskIDs {
		if err := validateTaskID(taskID); err != nil {
			return err
		}
	}

	statusRaw, hasStatus := parseOptionWithPresence(args, "--status")
	if hasStatus {
		if strings.TrimSpace(statusRaw) == "" {
			return errors.New("expected value for --status")
		}
	} else {
		statusRaw = string(models.StatusDone)
	}
	if strings.TrimSpace(statusRaw) == "" {
		statusRaw = string(models.StatusDone)
	}
	status, err := models.ParseStatus(statusRaw)
	if err != nil {
		return err
	}

	force := parseFlag(args, "--force")
	_ = parseFlag(args, "--verify")

	tree, err := loader.New().Load("metadata", true, true)
	if err != nil {
		return err
	}
	for _, taskID := range taskIDs {
		task := findTask(tree, taskID)
		if task == nil {
			return fmt.Errorf("Task not found: %s", taskID)
		}
		if _, err := resolveTaskFilePath(task.File); err != nil || !taskFileExists(task.File) {
			return fmt.Errorf("no such file: %s", task.File)
		}

		if task.Status == models.StatusDone && status == models.StatusDone {
			fmt.Printf("%s %s - %s\n", styleMuted("Already done:"), styleSuccess(task.ID), styleSuccess(task.Title))
			continue
		}
		if status == models.StatusDone && task.StartedAt != nil {
			duration := time.Since(*task.StartedAt).Minutes()
			task.DurationMinutes = &duration
		}
		if err := applyTaskStatusTransition(task, status, ""); err != nil {
			if force {
				task.Status = status
			} else {
				return err
			}
		}
		if err := saveTaskState(*task, tree); err != nil {
			return err
		}
		if metadata.id == "" {
			metadata.id = task.ID
			metadata.title = task.Title
		}

		if status == models.StatusDone {
			completion := completionNotice{}
			completeNotice, err := setItemDone(*task, tree)
			if err != nil {
				return err
			}
			completion = completeNotice
			printCompletionNotice(tree, *task, completion)
		}

		if status == models.StatusDone {
			fmt.Printf("%s %s - %s\n", styleSuccess("Completed:"), styleSuccess(task.ID), styleSuccess(task.Title))
		} else {
			fmt.Printf("%s %s - %s\n", styleSuccess("Updated:"), styleSuccess(task.ID), styleStatusText(string(status)))
		}
		if status == models.StatusDone && task.DurationMinutes != nil {
			fmt.Printf("%s: %d minutes\n", styleSubHeader("Duration"), int(*task.DurationMinutes))
		}
	}
	return nil
}

func printCommandNotImplemented(name string) error {
	fmt.Printf("%s command is not implemented yet\n", name)
	fmt.Printf("%s %s\n", styleMuted("Tip:"), styleSuccess("Run 'backlog --help' to see supported commands."))
	return nil
}

func applyIdRemap(remap map[string]string, dataDir string) error {
	if len(remap) == 0 {
		return nil
	}

	if err := filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if base == "index.yaml" {
			if err := replaceIDsInYamlFile(path, remap); err != nil {
				return err
			}
			return nil
		}
		if filepath.Ext(path) != ".todo" {
			return nil
		}
		if err := replaceIDsInTodoFrontmatter(path, remap); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	for _, runtimeName := range []string{config.ContextFileName, config.SessionsFileName} {
		runtimePath := filepath.Join(dataDir, runtimeName)
		if _, err := os.Stat(runtimePath); err != nil {
			continue
		}
		if err := replaceIDsInYamlFile(runtimePath, remap); err != nil {
			return err
		}
	}
	return nil
}

func replaceIDsInYamlFile(path string, remap map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var payload any
	if err := yaml.Unmarshal(raw, &payload); err != nil {
		return err
	}
	updated := replaceValues(payload, remap)
	out, err := yaml.Marshal(updated)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func replaceIDsInTodoFrontmatter(path string, remap map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil
	}
	frontmatterRaw := strings.TrimSpace(strings.Join(lines[1:end], "\n"))
	frontmatter := map[string]interface{}{}
	if frontmatterRaw != "" {
		if err := yaml.Unmarshal([]byte(frontmatterRaw), &frontmatter); err != nil {
			return err
		}
	}
	body := ""
	if len(lines) > end+1 {
		body = strings.Join(lines[end+1:], "\n")
	}
	updated := replaceValues(frontmatter, remap).(map[string]interface{})
	out, err := yaml.Marshal(updated)
	if err != nil {
		return err
	}
	payload := fmt.Sprintf("---\n%s---\n%s", string(out), body)
	return os.WriteFile(path, []byte(payload), 0o644)
}

func replaceValues(value any, remap map[string]string) any {
	if value == nil {
		return nil
	}
	switch cast := value.(type) {
	case string:
		if replaced, ok := remap[cast]; ok {
			return replaced
		}
		return cast
	case map[string]interface{}:
		out := map[string]interface{}{}
		for key, item := range cast {
			out[key] = replaceValues(item, remap)
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(cast))
		for _, item := range cast {
			out = append(out, replaceValues(item, remap))
		}
		return out
	default:
		return value
	}
}

func nextIndexID(ids []string, prefix string) int {
	max := 0
	prefix = strings.ToUpper(prefix)
	if prefix == "" {
		return max + 1
	}
	for _, id := range ids {
		value := strings.TrimSpace(id)
		if !strings.HasPrefix(value, prefix) {
			continue
		}
		numeric := strings.TrimPrefix(value, prefix)
		if numeric == "" {
			continue
		}
		number, err := strconv.Atoi(numeric)
		if err != nil {
			continue
		}
		if number > max {
			max = number
		}
	}
	return max + 1
}

func asSlice(raw any) []interface{} {
	switch values := raw.(type) {
	case []interface{}:
		return values
	case []map[string]interface{}:
		out := make([]interface{}, 0, len(values))
		for _, value := range values {
			out = append(out, value)
		}
		return out
	default:
		return []interface{}{}
	}
}

func extractTaskLeafIDs(raw any) []string {
	out := []string{}
	for _, entry := range asSlice(raw) {
		if value, ok := entry.(map[string]interface{}); ok {
			if id := asString(value["id"]); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

func extractEpicOrMilestoneLeafIDs(raw any, prefix string) []string {
	out := []string{}
	for _, entry := range asSlice(raw) {
		if value, ok := entry.(map[string]interface{}); ok {
			id := asString(value["id"])
			if strings.HasPrefix(id, prefix) {
				out = append(out, id)
			}
		}
	}
	return out
}

func leafID(value string) string {
	parts := strings.Split(value, ".")
	if len(parts) == 0 {
		return value
	}
	return parts[len(parts)-1]
}

func resolveTaskFilePath(taskFile string) (string, error) {
	if taskFile == "" {
		return "", errors.New("Task file path is empty")
	}
	if filepath.IsAbs(taskFile) {
		return taskFile, nil
	}
	dataDir, err := ensureDataRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, taskFile), nil
}

func validateScopeOrID(value string) error {
	if strings.HasPrefix(value, "--") {
		return fmt.Errorf("unexpected flag: %s", value)
	}
	if value == "" {
		return errors.New("invalid identifier")
	}
	return nil
}

var taskIDRe = regexp.MustCompile(`^(?:P\d+(?:\.M\d+(?:\.E\d+(?:\.T\d+)?)?)?|[BI]\d+)$`)
var dependencyIDRe = regexp.MustCompile(`^(?:[BEI]\d+|P\d+(?:\.M\d+(?:\.E\d+(?:\.T\d+)?)?)?|M\d+|E\d+|T\d+)$`)
var taskIDShorthandForCIRe = regexp.MustCompile(`^E\d+\.T\d+$`)
var auxTaskIDForCIRe = regexp.MustCompile(`^[BIF]\d+$`)

func validateTaskID(raw string) error {
	if raw == "" {
		return errors.New("id is required")
	}
	if strings.HasPrefix(raw, "P") {
		if _, err := models.ParseTaskPath(raw); err != nil {
			return fmt.Errorf("malformed task id: %s", raw)
		}
		return nil
	}
	if taskIDRe.MatchString(raw) {
		return nil
	}
	return fmt.Errorf("malformed task id: %s", raw)
}

func validateTaskIDForCI(raw string) error {
	if raw == "" {
		return errors.New("id is required")
	}
	if strings.HasPrefix(raw, "P") {
		if _, err := models.ParseTaskPath(raw); err != nil {
			return fmt.Errorf("malformed task id: %s", raw)
		}
		return nil
	}
	if auxTaskIDForCIRe.MatchString(raw) || taskIDShorthandForCIRe.MatchString(raw) {
		return nil
	}
	return fmt.Errorf("malformed task id: %s", raw)
}

func ensureDataRoot() (string, error) {
	dataDir, err := config.DetectDataDir()
	if err != nil {
		return "", err
	}
	return dataDir, nil
}

func ensureDataDir() (string, error) {
	return ensureDataRoot()
}

func dataDirFromContext() string {
	dataDir, err := ensureDataDir()
	if err != nil {
		return ""
	}
	return dataDir
}

func ensureBacklogDataAvailable() error {
	_, err := os.Stat(config.BacklogDir)
	if err == nil {
		return nil
	}
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return nil
}

func firstPositionalArg(args []string, valueTakingFlags map[string]bool) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
		flag, hasValue := splitOption(arg)
		if hasValue {
			continue
		}
		if valueTakingFlags[flag] && i+1 < len(args) {
			i++
		}
	}
	return ""
}

func splitOption(arg string) (string, bool) {
	if strings.HasPrefix(arg, "--") {
		if idx := strings.Index(arg, "="); idx >= 0 {
			return arg[:idx], true
		}
		return arg, false
	}
	if strings.HasPrefix(arg, "-") && len(arg) > 1 {
		if idx := strings.Index(arg, "="); idx >= 0 {
			return arg[:idx], true
		}
		return arg, false
	}
	return "", false
}

func parseOption(args []string, keys ...string) string {
	for i, arg := range args {
		for _, key := range keys {
			if arg == key {
				if i+1 >= len(args) {
					return ""
				}
				return args[i+1]
			}
			if strings.HasPrefix(arg, key+"=") {
				return strings.TrimPrefix(arg, key+"=")
			}
		}
	}
	return ""
}

func parseOptions(args []string, key string) []string {
	out := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == key {
			if i+1 < len(args) {
				out = append(out, args[i+1])
			}
			i++
			continue
		}
		if strings.HasPrefix(arg, key+"=") {
			out = append(out, strings.TrimPrefix(arg, key+"="))
		}
	}
	return out
}

func parseFloatOptionWithDefault(args []string, fallback float64, keys ...string) (float64, error) {
	keyName := keys[0]
	raw := parseOption(args, keys...)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, printUsageError(currentCommandForUsage, fmt.Errorf("invalid %s: %s", keyName, raw))
	}
	return value, nil
}

func parseComplexityOption(args []string, names ...string) (models.Complexity, error) {
	raw := parseOption(args, names...)
	if strings.TrimSpace(raw) == "" {
		return models.ComplexityMedium, nil
	}
	value, err := models.ParseComplexity(raw)
	if err != nil {
		return models.ComplexityMedium, printUsageError(currentCommandForUsage, err)
	}
	return value, nil
}

func parsePriorityOption(args []string, names ...string) (models.Priority, error) {
	raw := parseOption(args, names...)
	if strings.TrimSpace(raw) == "" {
		return models.PriorityMedium, nil
	}
	value, err := models.ParsePriority(raw)
	if err != nil {
		return models.PriorityMedium, printUsageError(currentCommandForUsage, err)
	}
	return value, nil
}

func parseCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func parseDependencyIDs(raw string) ([]string, error) {
	ids := parseCSV(raw)
	if len(ids) == 0 {
		return []string{}, nil
	}

	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !dependencyIDRe.MatchString(id) {
			return nil, fmt.Errorf("invalid dependency id: %s", id)
		}
		out = append(out, id)
	}
	return out, nil
}

func appendToList(data map[string]interface{}, key string, item map[string]interface{}) {
	if data == nil {
		return
	}
	raw := data[key]
	out := make([]interface{}, 0)
	switch existing := raw.(type) {
	case []interface{}:
		out = append(out, existing...)
	case []map[string]interface{}:
		for _, value := range existing {
			out = append(out, value)
		}
	}
	out = append(out, item)
	data[key] = out
}

func readYAMLMapFile(path string) (map[string]interface{}, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return out, nil
}

func writeYAMLMapFile(path string, value map[string]interface{}) error {
	payload, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to serialize %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", filepath.Dir(path), err)
	}
	return os.WriteFile(path, payload, 0o644)
}

func idSuffixNumber(id string, prefix string) int {
	value := strings.TrimPrefix(id, prefix)
	result, err := strconv.Atoi(value)
	if err != nil || result <= 0 {
		return 1
	}
	return result
}

type skillInstallOperation struct {
	Client   string
	Artifact string
	Path     string
	Skill    string
}

func runSkills(args []string) error {
	if len(args) == 0 || parseFlag(args, "--help", "-h") {
		fmt.Println(styleMuted("Usage: backlog skills install [skill_names...] [--scope local|global] [--client codex|claude|opencode|common]"))
		return nil
	}

	sub := strings.TrimSpace(args[0])
	if sub != "install" {
		return fmt.Errorf("Unknown skills subcommand: %s", sub)
	}

	rest := args[1:]
	if parseFlag(rest, "--help", "-h") {
		fmt.Println(styleMuted("Usage: backlog skills install [skill_names...] [--scope local|global] [--client codex|claude|opencode|common]"))
		return nil
	}
	if err := validateAllowedFlags(rest, map[string]bool{
		"--scope":    true,
		"--client":   true,
		"--artifact": true,
		"--dir":      true,
		"--force":    true,
		"--dry-run":  true,
		"--json":     true,
		"--help":     true,
		"-h":         true,
	}); err != nil {
		return err
	}

	skillNames := positionalArgs(rest, map[string]bool{
		"--scope":    true,
		"--client":   true,
		"--artifact": true,
		"--dir":      true,
	})
	selectedSkills, err := resolveSkillNames(skillNames)
	if err != nil {
		return err
	}

	scope := strings.TrimSpace(parseOption(rest, "--scope"))
	if scope == "" {
		scope = "local"
	}
	if scope != "local" && scope != "global" {
		return fmt.Errorf("Invalid scope: %s", scope)
	}

	clientName := strings.TrimSpace(parseOption(rest, "--client"))
	if clientName == "" {
		clientName = "common"
	}
	clients, err := resolveSkillClients(clientName)
	if err != nil {
		return err
	}

	artifact := strings.TrimSpace(parseOption(rest, "--artifact"))
	if artifact == "" {
		artifact = "skills"
	}
	artifacts, err := resolveSkillArtifacts(artifact)
	if err != nil {
		return err
	}

	outputDir := strings.TrimSpace(parseOption(rest, "--dir"))
	force := parseFlag(rest, "--force")
	dryRun := parseFlag(rest, "--dry-run")
	outputJSON := parseFlag(rest, "--json")

	warnings := []string{}
	ops := make([]skillInstallOperation, 0)
	for _, client := range clients {
		for _, selectedArtifact := range artifacts {
			if client == "codex" && selectedArtifact == "commands" {
				warnings = append(warnings, "codex does not support 'commands' artifacts; skipping.")
				continue
			}

			root, err := resolveSkillTargetRoot(client, scope, selectedArtifact, outputDir)
			if err != nil {
				return err
			}
			for _, skill := range selectedSkills {
				target := ""
				if selectedArtifact == "skills" {
					target = filepath.Join(root, skill, "SKILL.md")
				} else {
					target = filepath.Join(root, skill+".md")
				}
				ops = append(ops, skillInstallOperation{
					Client:   client,
					Artifact: selectedArtifact,
					Path:     target,
					Skill:    skill,
				})
			}
		}
	}

	sort.Slice(ops, func(i, j int) bool {
		return ops[i].Path < ops[j].Path
	})

	if !dryRun && !force {
		conflicts := make([]string, 0)
		for _, op := range ops {
			if _, err := os.Stat(op.Path); err == nil {
				conflicts = append(conflicts, op.Path)
			}
		}
		if len(conflicts) > 0 {
			return fmt.Errorf(
				"Refusing to overwrite existing files (use --force):\n%s",
				strings.Join(conflicts, "\n"),
			)
		}
	}

	writtenCount := 0
	if !dryRun {
		for _, op := range ops {
			if err := os.MkdirAll(filepath.Dir(op.Path), 0o755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", filepath.Dir(op.Path), err)
			}
			content := ""
			if op.Artifact == "skills" {
				content = skillTemplate(op.Skill)
			} else {
				content = commandTemplate(op.Skill)
			}
			if err := os.WriteFile(op.Path, []byte(content), 0o644); err != nil {
				return fmt.Errorf("failed to write %s: %w", op.Path, err)
			}
			writtenCount++
		}
	}

	operationPayload := make([]map[string]any, 0, len(ops))
	for _, op := range ops {
		action := "written"
		if dryRun {
			action = "planned"
		}
		operationPayload = append(operationPayload, map[string]any{
			"client":   op.Client,
			"artifact": op.Artifact,
			"path":     op.Path,
			"action":   action,
		})
	}
	outputDirValue := any(nil)
	if outputDir != "" {
		outputDirValue = outputDir
	}
	result := map[string]any{
		"skills":     selectedSkills,
		"scope":      scope,
		"client":     clientName,
		"artifact":   artifact,
		"output_dir": outputDirValue,
		"dry_run":    dryRun,
		"force":      force,
		"warnings":   warnings,
		"operations": operationPayload,
		"written_count": func() int {
			if dryRun {
				return 0
			}
			return writtenCount
		}(),
	}

	if outputJSON {
		raw, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}

	if dryRun {
		fmt.Println(styleWarning("Dry run: no files written."))
	} else {
		fmt.Printf("%s\n", styleSuccess(fmt.Sprintf("Installed %d file(s).", writtenCount)))
	}
	for _, warning := range warnings {
		fmt.Printf("%s %s\n", styleWarning("Warning:"), styleMuted(warning))
	}
	return nil
}

func resolveSkillNames(names []string) ([]string, error) {
	valid := []string{"plan-task", "plan-ingest", "start-tasks", "backlog-howto"}
	normalized := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(strings.ToLower(name))
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 || containsString(normalized, "all") {
		return append([]string{}, valid...), nil
	}
	out := make([]string, 0, len(normalized))
	seen := map[string]bool{}
	for _, name := range normalized {
		if !containsString(valid, name) {
			return nil, fmt.Errorf("Invalid skill name: %s", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

func resolveSkillClients(clientName string) ([]string, error) {
	switch clientName {
	case "common":
		return []string{"codex", "claude", "opencode"}, nil
	case "codex", "claude", "opencode":
		return []string{clientName}, nil
	default:
		return nil, fmt.Errorf("Invalid client: %s", clientName)
	}
}

func resolveSkillArtifacts(artifact string) ([]string, error) {
	switch artifact {
	case "both":
		return []string{"skills", "commands"}, nil
	case "skills", "commands":
		return []string{artifact}, nil
	default:
		return nil, fmt.Errorf("Invalid artifact: %s", artifact)
	}
}

func resolveSkillTargetRoot(client string, scope string, artifact string, outputDir string) (string, error) {
	if outputDir != "" {
		return filepath.Join(outputDir, artifact, client), nil
	}
	home := os.Getenv("HOME")
	if strings.TrimSpace(home) == "" {
		derived, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = derived
	}

	switch client {
	case "codex":
		if artifact != "skills" {
			return "", fmt.Errorf("codex does not support commands artifacts")
		}
		if scope == "local" {
			return filepath.Join(".agents", "skills"), nil
		}
		codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if codexHome != "" {
			return filepath.Join(codexHome, "skills"), nil
		}
		return filepath.Join(home, ".agents", "skills"), nil
	case "claude":
		if scope == "local" {
			return filepath.Join(".claude", artifact), nil
		}
		return filepath.Join(home, ".claude", artifact), nil
	case "opencode":
		if scope == "local" {
			return filepath.Join(".opencode", artifact), nil
		}
		return filepath.Join(home, ".config", "opencode", artifact), nil
	default:
		return "", fmt.Errorf("Unknown client: %s", client)
	}
}

func skillTemplate(name string) string {
	if name == "plan-task" {
		return "---\nname: plan-task\ndescription: plan task\n---\n# plan-task\n"
	}
	if name == "plan-ingest" {
		return "---\nname: plan-ingest\ndescription: plan ingest\n---\n# plan-ingest\n"
	}
	if name == "backlog-howto" {
		return skills.BacklogHowtoSkillMD
	}
	return "---\nname: start-tasks\ndescription: tasks grab and cycle\n---\n# start-tasks\n"
}

func commandTemplate(name string) string {
	if name == "backlog-howto" {
		return fmt.Sprintf(
			"---\ndescription: Backlog overview/manual skill with common workflows and command usage.\n---\n\nLoad the installed `backlog-howto` skill instructions.\n\nSource of truth: `bl_skills/backlog-howto/SKILL.md`\nSkill-Version: %s\n",
			skills.BacklogHowtoSkillVersion,
		)
	}
	return fmt.Sprintf("---\ndescription: %s\n---\n%s\n", name, name)
}
