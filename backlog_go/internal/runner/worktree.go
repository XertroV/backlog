package runner

import (
	"fmt"
	"strings"

	"github.com/XertroV/tasks/backlog_go/internal/commands"
	"github.com/XertroV/tasks/backlog_go/internal/config"
)

// mutatingCommands is the set of backlog commands that mutate .backlog/ or
// .tasks/ state. Read-only commands (list/show/next/check/schema/help/howto/
// etc.) are intentionally NOT in this set so read workflows keep working in
// a worktree — agents often need to inspect state from a worktree before
// merging back.
//
// Keep this list aligned with shell-side guards in
// /home/xertrov/.local/bin/_bl_worktree_guard.sh so the two stay in sync.
var mutatingCommands = map[string]struct{}{
	commands.CmdInit:         {},
	commands.CmdMigrate:      {},
	commands.CmdUpgrade:      {},
	commands.CmdAdd:          {},
	commands.CmdAddEpic:      {},
	commands.CmdAddMilestone: {},
	commands.CmdAddPhase:     {},
	commands.CmdBug:          {},
	commands.CmdFixed:        {},
	commands.CmdIdea:         {},
	commands.CmdEdit:         {},
	commands.CmdSet:          {},
	commands.CmdAppend:       {},
	commands.CmdUpdate:       {},
	commands.CmdDone:         {},
	commands.CmdUndone:       {},
	commands.CmdMove:         {},
	commands.CmdLock:         {},
	commands.CmdUnlock:       {},
	commands.CmdClaim:        {},
	commands.CmdUnclaim:      {},
	commands.CmdUnclaimStale: {},
	commands.CmdCycle:        {},
	commands.CmdSkip:         {},
	commands.CmdHandoff:      {},
	commands.CmdWork:         {},
	commands.CmdGrab:         {},
	commands.CmdBlocked:      {},
	commands.CmdSync:         {},
	commands.CmdSession:      {}, // start/end mutate .sessions.yaml
}

// IsMutatingCommand reports whether the given (normalized) command name
// mutates backlog state. Aliases are resolved by the caller before this.
func IsMutatingCommand(command string) bool {
	_, ok := mutatingCommands[command]
	return ok
}

// worktreeGuardError is returned by CheckWorktreeGuard when a mutating
// command is invoked from inside a git worktree. The user is given the path
// to the main checkout so they can re-run from there.
type worktreeGuardError struct {
	command string
	args    []string
	mainDir string
}

func (e *worktreeGuardError) Error() string {
	retryCmd := "bl " + e.command
	if len(e.args) > 0 {
		retryCmd += " " + strings.Join(e.args, " ")
	}
	if e.mainDir != "" {
		return fmt.Sprintf(
			"ERROR: 'bl %s' is a mutating backlog command and cannot be run from a git worktree.\n\n"+
				"Backlog mutations (add/edit/claim/done/etc.) must happen in the main repository\n"+
				"checkout so .backlog/ and .tasks/ state stays in sync with the canonical source.\n\n"+
				"cd to the main repo checkout and re-run:\n\n"+
				"    cd %s\n"+
				"    %s\n",
			e.command, e.mainDir, retryCmd,
		)
	}
	return fmt.Sprintf(
		"ERROR: 'bl %s' is a mutating backlog command and cannot be run from a git worktree.\n\n"+
			"Backlog mutations (add/edit/claim/done/etc.) must happen in the main repository\n"+
			"checkout so .backlog/ and .tasks/ state stays in sync with the canonical source.\n",
		e.command,
	)
}

// CheckWorktreeGuard refuses mutating commands when cwd is inside a git
// worktree. It is a no-op for read-only commands or when cwd is not a git
// checkout. The detection uses only os.Stat / os.ReadFile — no `git`
// invocation, no .git lock acquisition. Called from Run() before dispatch.
func CheckWorktreeGuard(command string, args []string) error {
	if !IsMutatingCommand(command) {
		return nil
	}
	info, err := config.DetectWorktree()
	if err != nil {
		// Detection failure — be conservative and let the command proceed
		// rather than blocking the user on an environmental quirk.
		return nil
	}
	if !info.InWorktree {
		return nil
	}
	return &worktreeGuardError{command: command, args: args, mainDir: info.MainRepoDir}
}