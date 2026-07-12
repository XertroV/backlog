package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	BacklogDir       = ".backlog"
	TasksDir         = ".tasks"
	ContextFileName  = ".context.yaml"
	SessionsFileName = ".sessions.yaml"
	ConfigFileName   = "config.yaml"
)

// MissingDataDirError reports absence of an expected task data directory.
type MissingDataDirError struct {
	BaseDir string
}

func (e *MissingDataDirError) Error() string {
	if e == nil {
		return "data directory not found"
	}
	if e.BaseDir == "" {
		return "data directory not found"
	}
	return fmt.Sprintf("no data directory found from %s (.backlog/ or .tasks/)", e.BaseDir)
}

// DetectDataDir searches the working directory and nearby parents for a backlog
// data directory. It prefers .backlog over .tasks for compatibility with current
// project conventions.
func DetectDataDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return DetectDataDirFrom(cwd)
}

// DetectDataDirFrom mirrors DetectDataDir but with a caller-supplied base path.
// Tests can use this to inspect parent traversal behavior.
func DetectDataDirFrom(basePath string) (string, error) {
	start := filepath.Clean(basePath)
	firstTasks := ""

	for current := start; ; current = filepath.Dir(current) {
		backlogCandidate := filepath.Join(current, BacklogDir)
		if info, err := os.Stat(backlogCandidate); err == nil && info.IsDir() {
			return backlogCandidate, nil
		}

		if firstTasks == "" {
			tasksCandidate := filepath.Join(current, TasksDir)
			if info, err := os.Stat(tasksCandidate); err == nil && info.IsDir() {
				firstTasks = tasksCandidate
			}
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}

	if firstTasks != "" {
		return firstTasks, nil
	}

	return "", &MissingDataDirError{BaseDir: basePath}
}

// MustDataDir wraps DetectDataDir and returns an empty string on failure.
func MustDataDir() string {
	dataDir, err := DetectDataDir()
	if err != nil {
		return ""
	}
	return dataDir
}

// DataDirFilePath formats a path under the provided data directory.
func DataDirFilePath(dataDir, fileName string) string {
	return filepath.Join(dataDir, fileName)
}

// ContextFilePath returns the absolute path to the active context file for a root.
func ContextFilePath(dataDir string) string {
	return DataDirFilePath(dataDir, ContextFileName)
}

// SessionsFilePath returns the absolute path to the active sessions file for a root.
func SessionsFilePath(dataDir string) string {
	return DataDirFilePath(dataDir, SessionsFileName)
}

// ValidateDataDir validates that dataDir is non-empty and exists.
func ValidateDataDir(dataDir string) error {
	if dataDir == "" {
		return errors.New("data directory must not be empty")
	}
	if _, err := os.Stat(dataDir); err != nil {
		return fmt.Errorf("data directory %s does not exist", dataDir)
	}
	return nil
}

// WorktreeInfo describes a detected git worktree.
//
// In a worktree, the .git "file" is a text file containing
//
//	gitdir: /abs/path/to/main/.git/worktrees/<name>
//
// pointing at the main checkout's .git/worktrees/ tree. In the main
// checkout, .git is a directory. DetectWorktree reads that file directly
// (no `git` invocation) so it never acquires git's index lock and is safe
// to call from hot paths.
type WorktreeInfo struct {
	// InWorktree is true when cwd is inside a git worktree (not the main
	// checkout). False when cwd is in the main checkout, not in a git repo,
	//// or when detection cannot conclusively determine the state.
	InWorktree bool

	// MainRepoDir is the absolute path to the main checkout (the parent of
	// the .git directory referenced by the worktree's .git file). Empty if
	// InWorktree is false or the path could not be derived.
	MainRepoDir string
}

// DetectWorktree reports whether cwd is inside a git worktree. It walks up
// from cwd looking for a `.git` entry; on hit it inspects whether the entry
// is a directory (main checkout) or a `gitdir: ...` file (worktree).
//
// Performance: O(depth) os.Stat calls + at most one small os.ReadFile. No
// git invocation, no .git/index.lock interaction. Safe to call on every Run.
func DetectWorktree() (WorktreeInfo, error) {
	return detectWorktreeFrom("")
}

func detectWorktreeFrom(basePath string) (WorktreeInfo, error) {
	start := basePath
	if start == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return WorktreeInfo{}, err
		}
		start = cwd
	}
	start = filepath.Clean(start)

	for current := start; ; current = filepath.Dir(current) {
		entry := filepath.Join(current, ".git")
		info, err := os.Stat(entry)
		if err == nil {
			if info.IsDir() {
				// .git is a directory — this is the main checkout (or a
				// standalone repo with no worktrees). Either way, not a
				// worktree.
				return WorktreeInfo{InWorktree: false}, nil
			}
			// .git is a file — could be a worktree pointer or a .git
			// file used by submodules / older workflows. Read a small
			// prefix to confirm.
			f, openErr := os.Open(entry)
			if openErr != nil {
				// Cannot read — treat conservatively as "not a worktree"
				// rather than blocking the user.
				return WorktreeInfo{InWorktree: false}, nil
			}
			defer f.Close()
			buf := make([]byte, 256)
			n, _ := f.Read(buf)
			head := strings.TrimSpace(string(buf[:n]))
			const prefix = "gitdir:"
			if !strings.HasPrefix(head, prefix) {
				// Some other .git file we don't recognise — assume main
				// checkout rather than blocking.
				return WorktreeInfo{InWorktree: false}, nil
			}
			return worktreeInfoFromGitdir(head, prefix)
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Walked to the filesystem root without finding .git — not a
			// git checkout at all.
			return WorktreeInfo{InWorktree: false}, nil
		}
	}
}

// worktreeInfoFromGitdir parses a "gitdir: <path>" line and resolves the
// main checkout directory. The gitdir path for a worktree is of the form
//
//	/abs/path/to/main/.git/worktrees/<name>
//
// so the main checkout is two directories up from the trailing path.
func worktreeInfoFromGitdir(line, prefix string) (WorktreeInfo, error) {
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if gitdir == "" {
		return WorktreeInfo{InWorktree: false}, nil
	}
	// Resolve relative paths against the worktree's parent directory.
	if !filepath.IsAbs(gitdir) {
		// The .git file's location is the worktree root; resolve relative
		// to its parent (the directory containing .git). os.Getwd() is the
		// caller's cwd, which may be deeper than the worktree root, so we
		// intentionally don't use it here — relative gitdirs are evaluated
		// against the .git file's own directory by git's own rules.
		// For simplicity we treat unresolved relative paths as "worktree,
		// but main path unknown".
		return WorktreeInfo{InWorktree: true}, nil
	}
	// <gitdir> = <main>/.git/worktrees/<name>
	// -> main = <gitdir>/../..
	mainGit := filepath.Dir(filepath.Dir(gitdir))
	if filepath.Base(mainGit) != ".git" {
		return WorktreeInfo{InWorktree: true}, nil
	}
	mainRepo := filepath.Dir(mainGit)
	return WorktreeInfo{InWorktree: true, MainRepoDir: mainRepo}, nil
}
