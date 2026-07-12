package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DownloadTimeout is the per-binary timeout for the asset download.
const DownloadTimeout = 30 * time.Second

// UpgradePlan is the result of resolving a target release.
type UpgradePlan struct {
	Repo   string // "XertroV/backlog"
	Tag    string // "v0.2.1"
	Asset  string // "backlog-linux-amd64"
	URL    string // direct asset download URL
	Exe    string // absolute path of the current executable
	Backup string // absolute path of the backup we will create
}

// SourceRepo returns the GitHub "owner/name" hosting the release.
func (p UpgradePlan) SourceRepo() string { return p.Repo }

// Version returns the version string (without leading 'v') for comparison
// against the running version.
func (p UpgradePlan) Version() string { return strings.TrimPrefix(p.Tag, "v") }

// ResolvePlan fills in the UpgradePlan for target == "" (latest) or a
// pinned tag like "v0.2.1". currentExe is os.Executable(); tests can pass
// a temp path.
func ResolvePlan(ctx context.Context, currentVersion, target string, currentExe string) (UpgradePlan, error) {
	state, err := Check(ctx, currentVersion)
	if err != nil && !state.CheckOK {
		return UpgradePlan{}, err
	}
	tag := target
	sourceRepo := state.SourceRepo
	if tag == "" {
		tag = state.LatestTag
	} else {
		// Pinned target: try each repo until we get a 200.
		for _, repo := range DefaultRepos {
			url := releaseAssetURL(repo, tag, assetName())
			if ok, err := headOK(ctx, url); err == nil && ok {
				sourceRepo = repo
				break
			}
		}
	}
	if tag == "" {
		return UpgradePlan{}, errors.New("no target version available (network/cache state insufficient)")
	}
	if sourceRepo == "" {
		sourceRepo = DefaultRepos[0]
	}
	exe, err := filepath.Abs(currentExe)
	if err != nil {
		return UpgradePlan{}, err
	}
	backup := fmt.Sprintf("%s.old.%d", exe, time.Now().Unix())
	return UpgradePlan{
		Repo:   sourceRepo,
		Tag:    tag,
		Asset:  assetName(),
		URL:    releaseAssetURL(sourceRepo, tag, assetName()),
		Exe:    exe,
		Backup: backup,
	}, nil
}

// releaseAssetURL is the canonical asset download URL.
func releaseAssetURL(repo, tag, asset string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)
}

// assetName produces the asset filename for the current OS/arch.
// Matches the naming convention used by .github/workflows/release.yml.
func assetName() string {
	name := "backlog-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// ChecksumsURL returns the URL for the SHA256SUMS file of a release.
func ChecksumsURL(repo, tag string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/SHA256SUMS", repo, tag)
}

func headOK(ctx context.Context, url string) (bool, error) {
	client := &http.Client{Timeout: releaseFetchTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "backlog-go-updater")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// DownloadAndApply fetches the asset for plan, verifies SHA256 against
// SHA256SUMS, and atomically replaces plan.Exe. The previous binary is
// preserved at plan.Backup.
func DownloadAndApply(ctx context.Context, plan UpgradePlan) error {
	if plan.Exe == "" {
		return errors.New("plan.Exe is empty")
	}
	expected, err := fetchChecksumFor(ctx, plan)
	if err != nil {
		return fmt.Errorf("fetch checksums: %w", err)
	}
	if expected == "" {
		return fmt.Errorf("checksum for %s not found in SHA256SUMS", plan.Asset)
	}

	// Stream to a temp file in the same directory so the final rename
	// is atomic on the same filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(plan.Exe), ".backlog-upgrade-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if err := streamDownload(ctx, plan.URL, tmp); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("download asset: %w", err)
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

	actual, err := fileSHA256(tmpPath)
	if err != nil {
		cleanup()
		return err
	}
	if !strings.EqualFold(actual, expected) {
		cleanup()
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actual)
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		cleanup()
		return err
	}

	if err := os.Rename(plan.Exe, plan.Backup); err != nil {
		// If the binary is locked (Windows or read-only mount), report
		// a clear error and instruct the user to install manually.
		cleanup()
		return fmt.Errorf("could not back up %s: %w", plan.Exe, err)
	}
	if err := os.Rename(tmpPath, plan.Exe); err != nil {
		// Attempt to restore the backup before failing.
		_ = os.Rename(plan.Backup, plan.Exe)
		return fmt.Errorf("could not install upgrade at %s: %w", plan.Exe, err)
	}
	return nil
}

func streamDownload(ctx context.Context, url string, dst *os.File) error {
	client := &http.Client{Timeout: DownloadTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "backlog-go-updater")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	_, err = io.Copy(dst, resp.Body)
	return err
}

// fetchChecksumFor downloads SHA256SUMS and returns the hash for asset,
// or "" if not found.
func fetchChecksumFor(ctx context.Context, plan UpgradePlan) (string, error) {
	url := ChecksumsURL(plan.Repo, plan.Tag)
	client := &http.Client{Timeout: releaseFetchTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "backlog-go-updater")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download SHA256SUMS: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		hash := strings.ToLower(fields[0])
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name == plan.Asset {
			return hash, nil
		}
	}
	return "", nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
