package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

// releaseFetchTimeout bounds each individual GitHub request. Total
// worst-case latency is approximately len(DefaultRepos) * this.
const releaseFetchTimeout = 2 * time.Second

// releaseResponse is the subset of /repos/{owner}/{repo}/releases/latest
// we care about. Extra fields are ignored to keep parsing forgiving.
type releaseResponse struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
}

// Check refreshes the cache when it is missing or older than CacheStaleAfter
// and returns the resulting state. The state is also persisted before
// returning so the caller doesn't have to think about I/O.
//
// currentVersion is the running CLI version; it is recorded into the
// returned state so we can detect when the user has upgraded between runs.
//
// On a hard failure (all repos unreachable, parse errors), the original
// state is returned unchanged and LastCheckedAt is NOT bumped. This makes
// the cache sticky-fresh on a temporary outage.
func Check(ctx context.Context, currentVersion string) (State, error) {
	state, _ := Load()
	if state.CurrentVersion != "" && state.CurrentVersion != currentVersion {
		// The binary was upgraded under us; reset the cadence counter
		// so we don't spam the user with an old banner.
		state.InvocationCount = 0
	}
	state.CurrentVersion = currentVersion

	if !state.LastCheckedAt.IsZero() && time.Since(state.LastCheckedAt) < CacheStaleAfter {
		return state, nil
	}

	repos := DefaultRepos
	if len(repos) == 0 {
		return state, errors.New("updater: no repos configured")
	}

	for _, repo := range repos {
		tag, err := fetchLatestRelease(ctx, repo)
		if err != nil {
			continue
		}
		if tag == "" {
			continue
		}
		state.LatestTag = tag
		state.LatestVersion = StripTag(tag)
		state.SourceRepo = repo
		state.CheckOK = true
		state.LastCheckedAt = time.Now().UTC()
		_ = Save(state)
		return state, nil
	}

	return state, fmt.Errorf("updater: all repos failed to respond")
}

// fetchLatestRelease queries GitHub for the latest release tag in `repo`.
// Returns ("", nil) for 404/410 (repo missing / no releases yet).
func fetchLatestRelease(ctx context.Context, repo string) (string, error) {
	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	client := &http.Client{Timeout: releaseFetchTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "backlog-go-updater")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone:
		return "", nil
	case resp.StatusCode >= 400:
		return "", fmt.Errorf("updater: github returned %s for %s", resp.Status, repo)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB hard cap
	if err != nil {
		return "", err
	}
	var r releaseResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", err
	}
	if r.Draft {
		return "", nil
	}
	return r.TagName, nil
}

// SortedRepos returns the failover list, primarily for `--check` output.
func SortedRepos() []string {
	out := append([]string(nil), DefaultRepos...)
	sort.Strings(out)
	return out
}
