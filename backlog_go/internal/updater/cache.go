// Package updater detects new backlog CLI releases on GitHub, caches the
// result, and offers self-upgrade by downloading the matching release asset.
//
// The package is self-contained (stdlib only) and has no dependency on the
// runner so it can be unit-tested in isolation.
package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// State is the per-user state persisted to the cache file. The struct is
// deliberately small so the JSON file stays easy to inspect by hand.
type State struct {
	// LastCheckedAt is when the GitHub release endpoint was last queried
	// successfully. Zero value triggers an immediate refresh on next run.
	LastCheckedAt time.Time `json:"last_checked_at"`
	// LatestTag is the raw tag_name returned by GitHub (e.g. "v0.2.1").
	LatestTag string `json:"latest_tag,omitempty"`
	// LatestVersion is LatestTag with any leading "v" stripped.
	LatestVersion string `json:"latest_version,omitempty"`
	// SourceRepo records which GitHub repo the latest tag came from
	// (e.g. "XertroV/backlog"). Empty when no successful check yet.
	SourceRepo string `json:"source_repo,omitempty"`
	// CheckOK is true when the most recent fetch (background or initial)
	// returned a usable tag. Useful for diagnostics via `backlog upgrade --check`.
	CheckOK bool `json:"check_ok"`
	// CurrentVersion is the running CLI version recorded into the cache
	// after a successful check. Used to detect when the user has actually
	// upgraded and reset the invocation counter.
	CurrentVersion string `json:"current_version,omitempty"`
	// InvocationCount is a monotonically increasing counter, persisted
	// across runs. The CLI emits the update banner only when this value
	// is a positive multiple of 5.
	InvocationCount int `json:"invocation_count"`
}

// FileName is the on-disk name of the cache file (basename only).
const FileName = "update-check.json"

// EnvCacheFile overrides the cache file path entirely.
const EnvCacheFile = "BACKLOG_UPDATE_CACHE_FILE"

// DefaultRepos is the failover list of GitHub repos consulted in order.
// The first entry is the canonical home; subsequent entries are failovers
// used when the upstream returns 404, 5xx, or a network error.
var DefaultRepos = []string{
	"XertroV/backlog",
	"clankercode/backlog",
}

// CacheStaleAfter is how long a cached check is considered fresh.
const CacheStaleAfter = 24 * time.Hour

// CacheMaxAge is the absolute age ceiling after which stale data must not
// be used to emit a banner (fail-safe against a permanent network outage
// caching a stale "v999" forever).
const CacheMaxAge = 7 * 24 * time.Hour

// CachePath returns the on-disk path for the cache file, creating the
// enclosing directory with 0700 perms on demand. Honors $BACKLOG_UPDATE_CACHE_FILE
// then $XDG_CACHE_HOME, then $HOME/.cache, then $TMPDIR.
func CachePath() (string, error) {
	if override := os.Getenv(EnvCacheFile); override != "" {
		if err := os.MkdirAll(filepath.Dir(override), 0o700); err != nil {
			return "", err
		}
		return override, nil
	}
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

func cacheDir() (string, error) {
	candidates := []string{}
	if env := os.Getenv("XDG_CACHE_HOME"); env != "" {
		candidates = append(candidates, filepath.Join(env, "backlog"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, ".cache", "backlog"))
	}
	if tmp := os.TempDir(); tmp != "" {
		candidates = append(candidates, filepath.Join(tmp, "backlog"))
	}
	for _, candidate := range candidates {
		if err := os.MkdirAll(candidate, 0o700); err == nil {
			// Probe writability with a tiny unique file under the dir.
			probe, probeErr := os.CreateTemp(candidate, ".probe-*")
			if probeErr != nil {
				continue
			}
			name := probe.Name()
			_ = probe.Close()
			_ = os.Remove(name)
			return candidate, nil
		}
	}
	return "", errors.New("updater: no writable cache directory available")
}

// Load reads the cache file. A missing file returns a zero-value State
// rather than an error. Corrupt JSON returns a zero-value State and a
// non-nil error so the caller can decide to overwrite or surface.
func Load() (State, error) {
	path, err := CachePath()
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return State{}, nil
		}
		return State{}, err
	}
	if len(data) == 0 {
		return State{}, nil
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("updater: decode cache: %w", err)
	}
	return s, nil
}

// Save writes state to the cache file atomically (tmp + rename) with 0600
// perms. The directory is created on demand.
func Save(s State) error {
	path, err := CachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-check-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
