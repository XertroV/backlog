package cmd

import (
	"github.com/XertroV/tasks/backlog_go/internal/commands"

	"fmt"
	"sort"
	"strings"
)

// BuildVersion can be injected at build time with:
//
//	go build -ldflags "-X github.com/XertroV/tasks/backlog_go/cmd.BuildVersion=<version>"
var BuildVersion = "0.1.0"

// RootCommand captures shared CLI metadata and the supported command list.
type RootCommand struct {
	name                string
	version             string
	commands            []string
	commandDescriptions map[string]string
}

func NewRootCommand() *RootCommand {
	commands := []string{
		commands.CmdAdmin,
		commands.CmdAdd,
		commands.CmdAddEpic,
		commands.CmdAddMilestone,
		commands.CmdAddPhase,
		commands.CmdAgents,
		commands.CmdAppend,
		commands.CmdBug,
		commands.CmdBlockers,
		commands.CmdBlocked,
		commands.CmdCheck,
		commands.CmdCat,
		commands.CmdCI,
		commands.CmdClaim,
		commands.CmdCycle,
		commands.CmdDash,
		commands.CmdData,
		commands.CmdBenchmark,
		commands.CmdEdit,
		commands.CmdDone,
		commands.CmdFixed,
		commands.CmdGrab,
		commands.CmdHandoff,
		commands.CmdHelp,
		commands.CmdHowto,
		commands.CmdIdea,
		commands.CmdInit,
		commands.CmdList,
		commands.CmdLock,
		commands.CmdLs,
		commands.CmdLog,
		commands.CmdMigrate,
		commands.CmdMove,
		commands.CmdNext,
		commands.CmdPreview,
		commands.CmdReport,
		commands.CmdReportAlias,
		commands.CmdSchema,
		commands.CmdVelocity,
		commands.CmdSkills,
		commands.CmdSearch,
		commands.CmdSession,
		commands.CmdSet,
		commands.CmdShow,
		commands.CmdSkip,
		commands.CmdSync,
		commands.CmdTimeline,
		commands.CmdTimelineAlias,
		commands.CmdTree,
		commands.CmdUnclaim,
		commands.CmdUnclaimStale,
		commands.CmdUnlock,
		commands.CmdUpdate,
		commands.CmdUpgrade,
		commands.CmdUpgradeAlias,
		commands.CmdUndone,
		commands.CmdVersion,
		commands.CmdWork,
		commands.CmdWhy,
	}
	sort.Strings(commands)
	descriptions := defaultCommandDescriptions()

	return &RootCommand{
		name:                "backlog",
		version:             resolveBuildVersion(),
		commands:            commands,
		commandDescriptions: descriptions,
	}
}

func resolveBuildVersion() string {
	version := strings.TrimSpace(BuildVersion)
	if version == "" {
		return "0.1.0"
	}
	return version
}

func (r *RootCommand) Name() string {
	return r.name
}

func (r *RootCommand) Version() string {
	return r.version
}

func (r *RootCommand) Commands() []string {
	out := append([]string{}, r.commands...)
	sort.Strings(out)
	return out
}

func (r *RootCommand) IsKnownCommand(candidate string) bool {
	for _, command := range r.commands {
		if command == candidate {
			return true
		}
	}
	return false
}

func (r *RootCommand) Usage() string {
	width := 0
	ordered := usageCommandOrder(r.commands)
	for _, command := range ordered {
		label := usageCommandLabel(command)
		if len(label) > width {
			width = len(label)
		}
	}
	lines := make([]string, 0, len(ordered))
	for _, command := range ordered {
		description := r.commandDescriptions[command]
		if description == "" {
			description = "No description available."
		}
		label := usageCommandLabel(command)
		lines = append(lines, fmt.Sprintf("  %-*s  %s", width, label, description))
	}

	return fmt.Sprintf(`Usage: %s <command> [options]

Commands:
%s

Quick rules:
  - Prefer '%s claim <TASK_ID> [TASK_ID ...]' for explicit IDs.
  - Use '%s grab' for automatic selection.
  - If command parsing fails, run '%s cycle' once.
  - Run '%s --help' to see this overview.`, r.name, strings.Join(lines, "\n"), r.name, r.name, r.name, r.name)
}

func usageCommandOrder(names []string) []string {
	ordered := make([]string, 0, len(names))
	if hasCommand(names, commands.CmdHowto) {
		ordered = append(ordered, commands.CmdHowto)
	}
	for _, command := range names {
		if command == commands.CmdHowto {
			continue
		}
		if isAliasOnlyUsageCommand(command) {
			continue
		}
		ordered = append(ordered, command)
	}
	return ordered
}

func isAliasOnlyUsageCommand(name string) bool {
	switch name {
	case commands.CmdLs, commands.CmdTimelineAlias, commands.CmdReportAlias:
		return true
	default:
		return false
	}
}

func usageCommandLabel(name string) string {
	switch name {
	case commands.CmdList:
		return fmt.Sprintf("%s (alias: %s)", commands.CmdList, commands.CmdLs)
	case commands.CmdTimeline:
		return fmt.Sprintf("%s (alias: %s)", commands.CmdTimeline, commands.CmdTimelineAlias)
	case commands.CmdReport:
		return fmt.Sprintf("%s (alias: %s)", commands.CmdReport, commands.CmdReportAlias)
	default:
		return name
	}
}

func hasCommand(commands []string, target string) bool {
	for _, command := range commands {
		if command == target {
			return true
		}
	}
	return false
}

func defaultCommandDescriptions() map[string]string {
	return map[string]string{
		commands.CmdAdd:           "Add a new task to an epic.",
		commands.CmdAddEpic:       "Add a new epic to a milestone.",
		commands.CmdAddMilestone:  "Add a new milestone to a phase.",
		commands.CmdAddPhase:      "Add a new phase to the project.",
		commands.CmdAdmin:         "Administrative checks and diagnostics.",
		commands.CmdAgents:        "Print AGENTS.md snippets.",
		commands.CmdBenchmark:     "Show task tree load-time benchmark metrics.",
		commands.CmdBlocked:       "Mark a task as blocked (optionally auto-grab next).",
		commands.CmdBlockers:      "Show blocking tasks and dependency chains.",
		commands.CmdBug:           "Create a new bug report.",
		commands.CmdCat:           "Print complete raw task file contents.",
		commands.CmdCheck:         "Run consistency checks across backlog files.",
		commands.CmdCI:            "Validate IDs and support CI-focused helper workflows.",
		commands.CmdClaim:         "Claim specific task ID(s).",
		commands.CmdEdit:          "Open a task todo file in your editor.",
		commands.CmdCycle:         "Complete current task and grab next.",
		commands.CmdDash:          "Show a quick project dashboard.",
		commands.CmdData:          "Export or summarize task data.",
		commands.CmdDone:          "Mark task(s) as complete.",
		commands.CmdFixed:         "Capture an ad-hoc completed fix note.",
		commands.CmdGrab:          "Auto-claim next task (or claim IDs).",
		commands.CmdHandoff:       "Transfer task ownership to another agent.",
		commands.CmdHelp:          "Show command overview and guidance.",
		commands.CmdHowto:         "Show agent how-to guide and recommended workflow.",
		commands.CmdIdea:          "Capture an idea as planning intake.",
		commands.CmdInit:          "Initialize a new backlog project directory.",
		commands.CmdList:          "List tasks with filtering options.",
		commands.CmdLock:          "Lock a phase/milestone/epic.",
		commands.CmdLog:           "Show recent activity events.",
		commands.CmdLs:            "Alias of list for scoped summaries.",
		commands.CmdMigrate:       "Migrate .backlog/ data to .backlog/ layout.",
		commands.CmdMove:          "Move a task/epic/milestone to a new parent.",
		commands.CmdNext:          "Show next available task on critical path.",
		commands.CmdPreview:       "Preview upcoming work with grab suggestions.",
		commands.CmdReport:        "Generate reports (progress/velocity/accuracy).",
		commands.CmdReportAlias:   "Alias for report.",
		commands.CmdVelocity:      "Generate a velocity report.",
		commands.CmdSchema:        "Show file schema information.",
		commands.CmdSearch:        "Search tasks by pattern.",
		commands.CmdSession:       "Manage agent sessions.",
		commands.CmdSet:           "Set task properties (status/priority/etc).",
		commands.CmdAppend:        "Append text to a task body (from arguments or stdin).",
		commands.CmdShow:          "Show detailed task/phase/milestone/epic info.",
		commands.CmdSkills:        "Install skill files for supported clients.",
		commands.CmdSkip:          "Skip current task and move on.",
		commands.CmdSync:          "Sync derived metadata in index files.",
		commands.CmdTimeline:      "Display project timeline.",
		commands.CmdTimelineAlias: "Alias for timeline.",
		commands.CmdTree:          "Display full hierarchical task tree.",
		commands.CmdUnclaim:       "Release a claimed task.",
		commands.CmdUnclaimStale:  "Release stale claims older than threshold.",
		commands.CmdUndone:        "Mark task/epic/milestone/phase as not done.",
		commands.CmdUnlock:        "Unlock a phase/milestone/epic.",
		commands.CmdUpdate:        "Update task status.",
		commands.CmdUpgrade:       "Download and install a newer backlog CLI release from GitHub.",
		commands.CmdUpgradeAlias:  "Alias for `backlog upgrade`.",
		commands.CmdVersion:       "Show CLI version.",
		commands.CmdWhy:           "Explain why a task is blocked/unavailable.",
		commands.CmdWork:          "Set or show current working task context.",
	}
}
