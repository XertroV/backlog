package updater

import (
	"fmt"
	"strings"
)

// BannerEveryNInvocations controls how frequently the banner is shown.
const BannerEveryNInvocations = 5

// shouldSkipForArgs reports whether the banner must NOT be printed for
// this invocation. The rules:
//   - `--json` anywhere in args: machine consumers would break.
//   - This is intentionally NOT skipping on non-TTY: piped output is fine,
//     we just emit plain text (styleWarning respects NO_COLOR/--no-color).
func shouldSkipForArgs(args []string) bool {
	for _, a := range args {
		if a == "--json" {
			return true
		}
	}
	return false
}

// MaybePrintBanner decides whether to print the one-line update banner
// for this invocation. It bumps the invocation counter on every call
// (regardless of whether the banner is shown) so the cadence stays
// monotonic across runs. If the running version differs from what is
// stored in the cache, the counter is reset to 0 first.
//
// Returns true when the banner was emitted.
func MaybePrintBanner(state State, args []string, currentVersion string, printer func(string)) bool {
	if state.CurrentVersion != "" && state.CurrentVersion != currentVersion {
		state.InvocationCount = 0
	}
	state.CurrentVersion = currentVersion
	state.InvocationCount++

	if shouldSkipForArgs(args) {
		_ = Save(state)
		return false
	}
	if !state.CheckOK {
		_ = Save(state)
		return false
	}
	if state.LatestVersion == "" {
		_ = Save(state)
		return false
	}
	if !IsGreater(state.LatestVersion, currentVersion) {
		_ = Save(state)
		return false
	}
	if state.InvocationCount%BannerEveryNInvocations != 0 {
		_ = Save(state)
		return false
	}

	line := BannerLine(state.LatestVersion, currentVersion)
	printer(line)
	_ = Save(state)
	return true
}

// BannerLine builds the one-line message. Exposed for unit tests.
func BannerLine(latestVersion, currentVersion string) string {
	latest := strings.TrimPrefix(strings.TrimSpace(latestVersion), "v")
	current := strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")
	if latest == "" {
		latest = strings.TrimSpace(latestVersion)
	}
	if current == "" {
		current = strings.TrimSpace(currentVersion)
	}
	return fmt.Sprintf("⬆ Update available: v%s (you have v%s) — run `backlog upgrade` to upgrade.", latest, current)
}

// bannerCheckSummary is shown by `backlog upgrade --check` for diagnostics.
func bannerCheckSummary(state State, currentVersion string) string {
	if !state.CheckOK || state.LatestVersion == "" {
		return fmt.Sprintf("current=v%s latest=unknown source=%s last_checked=%s",
			strings.TrimPrefix(currentVersion, "v"),
			state.SourceRepo, lastCheckedAtString(state))
	}
	return fmt.Sprintf("current=v%s latest=v%s source=%s last_checked=%s",
		strings.TrimPrefix(currentVersion, "v"),
		state.LatestVersion, state.SourceRepo, lastCheckedAtString(state))
}

func lastCheckedAtString(s State) string {
	if s.LastCheckedAt.IsZero() {
		return "never"
	}
	return s.LastCheckedAt.Format("2006-01-02T15:04:05Z")
}
