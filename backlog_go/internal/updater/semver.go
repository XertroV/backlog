package updater

import (
	"regexp"
	"strconv"
	"strings"
)

// semverPattern matches forms like `v0.1.2`, `0.1.2`, `0.1`, `0`.
// Pre-release tags like `-rc1` are captured separately so we can sort them
// before their corresponding final release.
var (
	semverRE = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:[-+]|$)`)
	intRE    = regexp.MustCompile(`\d+`)
)

// StripTag returns the version part of a tag, dropping any leading "v".
// Tags that are not version-shaped are returned as-is (lower-cased) so
// callers can still categorize them without false positives.
func StripTag(tag string) string {
	tag = strings.TrimSpace(tag)
	tag = strings.TrimPrefix(tag, "v")
	if i := strings.IndexAny(tag, "-+"); i >= 0 {
		return tag[:i]
	}
	return tag
}

// Compare returns -1, 0, or +1 like strings.Compare but for version
// numbers. "v" prefix is optional. Missing components are treated as 0
// (so "1.2" == "1.2.0"). Returns 0 when either side is not parseable as a
// version so callers can fall back to equality.
func Compare(a, b string) int {
	pa, ok := parseVersion(a)
	if !ok {
		pa = version{tag: strings.ToLower(strings.TrimSpace(a))}
	}
	pb, ok := parseVersion(b)
	if !ok {
		pb = version{tag: strings.ToLower(strings.TrimSpace(b))}
	}
	if !ok && pa.tag == pb.tag {
		return 0
	}
	if c := compareInt(pa.major, pb.major); c != 0 {
		return c
	}
	if c := compareInt(pa.minor, pb.minor); c != 0 {
		return c
	}
	if c := compareInt(pa.patch, pb.patch); c != 0 {
		return c
	}
	// Pre-release tags sort *before* the corresponding release: "1.2.0-rc1" < "1.2.0".
	if pa.pre != pb.pre {
		if pa.pre == "" {
			return 1
		}
		if pb.pre == "" {
			return -1
		}
		return comparePre(pa.pre, pb.pre)
	}
	if !ok {
		if pa.tag < pb.tag {
			return -1
		}
		if pa.tag > pb.tag {
			return 1
		}
		return 0
	}
	return 0
}

// IsGreater reports whether a is strictly newer than b. Non-version tags
// (e.g. nightly builds) are treated as "always-newer" when at least one
// side is not a recognized version, which favors showing the banner.
func IsGreater(a, b string) bool { return Compare(a, b) > 0 }

type version struct {
	major, minor, patch int
	pre                 string
	tag                 string
}

func parseVersion(s string) (version, bool) {
	s = strings.TrimSpace(s)
	m := semverRE.FindStringSubmatch(s)
	if m == nil {
		return version{}, false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(orZero(m[2]))
	pat, _ := strconv.Atoi(orZero(m[3]))
	// Build metadata (+foo) is ignored for precedence. Pre-release
	// identifies with `-`, and a `+` after that still means build metadata.
	pre := ""
	if i := strings.Index(s, "-"); i >= 0 {
		rest := s[i+1:]
		if plus := strings.Index(rest, "+"); plus >= 0 {
			rest = rest[:plus]
		}
		pre = rest
	}
	return version{major: maj, minor: min, patch: pat, pre: pre}, true
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func compareInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// comparePre handles two pre-release strings by splitting each into
// numeric and non-numeric tokens and comparing left-to-right (numeric
// tokens compare numerically; non-numeric tokens compare lexically).
// "alpha" < "beta" < "rc" matches common convention.
func comparePre(a, b string) int {
	at := tokenize(a)
	bt := tokenize(b)
	n := len(at)
	if len(bt) < n {
		n = len(bt)
	}
	for i := 0; i < n; i++ {
		switch {
		case at[i].num && bt[i].num:
			if c := compareInt(at[i].n, bt[i].n); c != 0 {
				return c
			}
		case at[i].num && !bt[i].num:
			return -1 // numeric < non-numeric per semver spec
		case !at[i].num && bt[i].num:
			return 1
		default:
			if at[i].s < bt[i].s {
				return -1
			}
			if at[i].s > bt[i].s {
				return 1
			}
		}
	}
	return compareInt(len(at), len(bt))
}

type preToken struct {
	num bool
	n   int
	s   string
}

func tokenize(s string) []preToken {
	out := []preToken{}
	parts := strings.Split(strings.ToLower(s), ".")
	for _, p := range parts {
		if p == "" {
			continue
		}
		if intRE.MatchString(p) {
			if len(p) == len(intRE.FindString(p)) {
				n, _ := strconv.Atoi(p)
				out = append(out, preToken{num: true, n: n})
				continue
			}
		}
		out = append(out, preToken{s: p})
	}
	return out
}
