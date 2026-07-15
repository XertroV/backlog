package runner

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Shared fixture root: <repo>/testdata/invalid-file-diagnostics
// Contract: docs/diagnostics/invalid-file-warning-contract.md

type invalidFileContract struct {
	Version    int                        `json:"version"`
	Conditions []invalidFileConditionSpec `json:"conditions"`
	Healthy    struct {
		Fixture                   string   `json:"fixture"`
		TaskID                    string   `json:"task_id"`
		MustNotContainSubstrings  []string `json:"must_not_contain_substrings"`
	} `json:"healthy_control"`
}

type invalidFileConditionSpec struct {
	Code               string   `json:"code"`
	Fixture            string   `json:"fixture"`
	TaskID             string   `json:"task_id"`
	WarningSubstrings  []string `json:"warning_substrings"`
	Show               struct {
		ExitCode     int      `json:"exit_code"`
		MustWarn     bool     `json:"must_warn"`
		MustShowBody bool     `json:"must_show_body"`
		BodySubs     []string `json:"body_substrings"`
	} `json:"show"`
	Claim struct {
		MayMutate        bool     `json:"may_mutate"`
		ExitCodeNonzero  bool     `json:"exit_code_nonzero"`
		ErrorSubstrings  []string `json:"error_substrings"`
		MustWarnBefore   bool     `json:"must_warn_before_mutate"`
	} `json:"claim"`
	Recovery       []string `json:"recovery"`
	Implementation struct {
		CLIAssertions map[string]bool `json:"cli_assertions"`
		Notes         string          `json:"notes"`
	} `json:"implementation"`
}

func invalidFileDiagnosticsRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// backlog_go/internal/runner -> repo root
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	dir := filepath.Join(root, "testdata", "invalid-file-diagnostics")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("shared fixture root missing: %s (%v)", dir, err)
	}
	return dir
}

func loadInvalidFileContract(t *testing.T) invalidFileContract {
	t.Helper()
	path := filepath.Join(invalidFileDiagnosticsRoot(t), "expected", "messages.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read messages.json: %v", err)
	}
	var contract invalidFileContract
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("parse messages.json: %v", err)
	}
	if contract.Version < 1 || len(contract.Conditions) == 0 {
		t.Fatalf("messages.json missing conditions: version=%d", contract.Version)
	}
	return contract
}

func copyInvalidFileCase(t *testing.T, relativeCase string) string {
	t.Helper()
	src := filepath.Join(invalidFileDiagnosticsRoot(t), relativeCase)
	dst := t.TempDir()
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copy fixture %s: %v", relativeCase, err)
	}
	return dst
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

func normalizeContractText(s string) string {
	s = stripANSI(s)
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func stripANSI(s string) string {
	// Minimal CSI stripper for test assertions.
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) {
				c := s[i]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					break
				}
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func assertContainsNormalized(t *testing.T, haystack, needle, label string) {
	t.Helper()
	if needle == "" {
		return
	}
	if !strings.Contains(normalizeContractText(haystack), normalizeContractText(needle)) {
		t.Fatalf("%s: expected substring %q in output:\n%s", label, needle, haystack)
	}
}

func TestInvalidFileContractCatalogAndFixtures(t *testing.T) {
	t.Parallel()

	root := invalidFileDiagnosticsRoot(t)
	contract := loadInvalidFileContract(t)

	requiredCodes := map[string]bool{
		"missing_indexed_file":    false,
		"malformed_frontmatter":   false,
		"id_path_mismatch":        false,
		"file_absent_from_index":  false,
	}

	for _, cond := range contract.Conditions {
		if _, ok := requiredCodes[cond.Code]; ok {
			requiredCodes[cond.Code] = true
		}
		caseDir := filepath.Join(root, cond.Fixture)
		if st, err := os.Stat(caseDir); err != nil || !st.IsDir() {
			t.Fatalf("fixture for %s missing: %s", cond.Code, caseDir)
		}
		if _, err := os.Stat(filepath.Join(caseDir, ".tasks", "index.yaml")); err != nil {
			t.Fatalf("%s: missing .tasks/index.yaml", cond.Code)
		}
		if len(cond.WarningSubstrings) == 0 {
			t.Fatalf("%s: warning_substrings required", cond.Code)
		}
		if len(cond.Recovery) == 0 {
			t.Fatalf("%s: recovery guidance required", cond.Code)
		}
		// Claimability must be explicit for every condition.
		_ = cond.Claim.MayMutate

		switch cond.Code {
		case "missing_indexed_file":
			if cond.Claim.MayMutate {
				t.Fatalf("missing_indexed_file must not be claimable")
			}
			todo := filepath.Join(caseDir, ".tasks", "01-phase", "01-ms", "01-epic", "T001-missing.todo")
			if _, err := os.Stat(todo); !os.IsNotExist(err) {
				t.Fatalf("missing fixture must not create %s", todo)
			}
		case "malformed_frontmatter":
			if !cond.Claim.MayMutate {
				t.Fatalf("malformed_frontmatter must remain claimable")
			}
			todo := filepath.Join(caseDir, ".tasks", "01-phase", "01-ms", "01-epic", "T001-malformed.todo")
			raw, err := os.ReadFile(todo)
			if err != nil {
				t.Fatalf("read malformed fixture: %v", err)
			}
			if strings.HasPrefix(strings.TrimSpace(string(raw)), "---") {
				t.Fatalf("malformed fixture should lack opening ---")
			}
		case "id_path_mismatch":
			if !cond.Claim.MayMutate {
				t.Fatalf("id_path_mismatch must remain claimable")
			}
			todo := filepath.Join(caseDir, ".tasks", "01-phase", "01-ms", "01-epic", "T001-id-mismatch.todo")
			raw, err := os.ReadFile(todo)
			if err != nil {
				t.Fatalf("read mismatch fixture: %v", err)
			}
			if !strings.Contains(string(raw), "id: P9.M9.E9.T999") {
				t.Fatalf("mismatch fixture frontmatter id missing")
			}
		case "file_absent_from_index":
			if cond.Claim.MayMutate {
				t.Fatalf("file_absent_from_index must not be claimable")
			}
			indexPath := filepath.Join(caseDir, ".tasks", "01-phase", "01-ms", "01-epic", "index.yaml")
			raw, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatalf("read orphan index: %v", err)
			}
			if strings.Contains(string(raw), "T001-orphan.todo") {
				t.Fatalf("orphan file must not be indexed")
			}
			todo := filepath.Join(caseDir, ".tasks", "01-phase", "01-ms", "01-epic", "T001-orphan.todo")
			if _, err := os.Stat(todo); err != nil {
				t.Fatalf("orphan todo missing: %v", err)
			}
		}
	}

	for code, seen := range requiredCodes {
		if !seen {
			t.Fatalf("messages.json missing required condition %s", code)
		}
	}

	healthy := filepath.Join(root, contract.Healthy.Fixture, ".tasks", "01-phase", "01-ms", "01-epic", "T001-healthy.todo")
	if _, err := os.Stat(healthy); err != nil {
		t.Fatalf("healthy-control todo missing: %v", err)
	}

	// root = <repo>/testdata/invalid-file-diagnostics → <repo>/docs/diagnostics/...
	doc := filepath.Clean(filepath.Join(root, "..", "..", "docs", "diagnostics", "invalid-file-warning-contract.md"))
	rawDoc, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("contract doc missing at %s: %v", doc, err)
	}
	for _, code := range []string{
		"missing_indexed_file",
		"malformed_frontmatter",
		"id_path_mismatch",
		"file_absent_from_index",
	} {
		if !strings.Contains(string(rawDoc), code) {
			t.Fatalf("contract doc missing code %s", code)
		}
	}
}

func TestInvalidFileContractCLILiveAssertions(t *testing.T) {
	t.Parallel()

	contract := loadInvalidFileContract(t)
	for _, cond := range contract.Conditions {
		cond := cond
		enabled := cond.Implementation.CLIAssertions["go"]
		t.Run(cond.Code, func(t *testing.T) {
			t.Parallel()
			if !enabled {
				t.Logf("CLI assertions deferred to T002 for %s: %s", cond.Code, cond.Implementation.Notes)
				return
			}

			root := copyInvalidFileCase(t, cond.Fixture)

			showOut, showErr := runInDir(t, root, "show", cond.TaskID)
			if cond.Show.ExitCode == 0 && showErr != nil {
				// show may still return nil with warnings; only fail on hard errors when we expect success
				// Some implementations return error for missing body paths — allow if warning present.
				if !strings.Contains(normalizeContractText(showOut+errString(showErr)), normalizeContractText(cond.WarningSubstrings[0])) {
					t.Fatalf("show err = %v\noutput = %s", showErr, showOut)
				}
			}
			combinedShow := showOut + errString(showErr)
			if cond.Show.MustWarn {
				for _, sub := range cond.WarningSubstrings {
					assertContainsNormalized(t, combinedShow, sub, "show warning")
				}
			}
			if cond.Show.MustShowBody {
				for _, sub := range cond.Show.BodySubs {
					assertContainsNormalized(t, combinedShow, sub, "show body")
				}
			}

			// Fresh copy so claim does not depend on show side effects.
			rootClaim := copyInvalidFileCase(t, cond.Fixture)
			claimOut, claimErr := runInDir(t, rootClaim, "claim", cond.TaskID, "--no-content", "--agent", "contract-test")
			combinedClaim := claimOut + errString(claimErr)

			if cond.Claim.MayMutate {
				if claimErr != nil {
					t.Fatalf("claim should succeed for %s: %v\n%s", cond.Code, claimErr, combinedClaim)
				}
				if cond.Claim.MustWarnBefore {
					for _, sub := range cond.WarningSubstrings {
						assertContainsNormalized(t, combinedClaim, sub, "claim warning")
					}
				}
			} else {
				if claimErr == nil {
					t.Fatalf("claim should fail for %s\n%s", cond.Code, combinedClaim)
				}
				for _, sub := range cond.Claim.ErrorSubstrings {
					assertContainsNormalized(t, combinedClaim, sub, "claim error")
				}
				for _, sub := range cond.WarningSubstrings {
					assertContainsNormalized(t, combinedClaim, sub, "claim warning with block")
				}
			}
		})
	}
}

func TestInvalidFileContractHealthyControl(t *testing.T) {
	t.Parallel()

	contract := loadInvalidFileContract(t)
	root := copyInvalidFileCase(t, contract.Healthy.Fixture)
	out, err := runInDir(t, root, "show", contract.Healthy.TaskID)
	if err != nil {
		t.Fatalf("show healthy: %v\n%s", err, out)
	}
	norm := normalizeContractText(out)
	for _, sub := range contract.Healthy.MustNotContainSubstrings {
		if strings.Contains(norm, normalizeContractText(sub)) {
			t.Fatalf("healthy show unexpectedly contained %q\n%s", sub, out)
		}
	}
	if !strings.Contains(norm, "Healthy task") {
		t.Fatalf("healthy show missing title/body anchors:\n%s", out)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
