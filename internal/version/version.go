// Package version is the single source of truth for the panel's version and
// the rules for comparing versions.
//
// BlockPanel uses semantic versioning: MAJOR.MINOR.PATCH.
//
//   - MAJOR — breaking changes: data-dir or config format changes that need
//     manual migration, removed features, incompatible API changes.
//   - MINOR — new features and improvements; always safe to update to.
//   - PATCH — bug and security fixes only; always safe to update to.
//
// Releases are published on GitHub with the tag "v<MAJOR>.<MINOR>.<PATCH>"
// (for example v1.2.0). The release assets keep the names produced by
// scripts/build-release.sh: the all-platform zip "blockpanel-<version>.zip",
// the standalone binaries "blockpanel-<os>-<arch>", and "SHA256SUMS".
// The built-in updater understands exactly that layout.
package version

// Current is the version of this build. Bump it (and tag the release
// commit "v"+Current) as the release step.
const Current = "1.0.0"

type parsed struct {
	major, minor, patch int
	pre                 string // pre-release tag: "1.2.0-rc1" -> "rc1"
	ok                  bool
}

// parse accepts "1.2.3", "v1.2.3" and "v1.2.3-rc1". Missing minor/patch
// numbers are treated as zero ("v1.2" == "v1.2.0").
func parse(s string) parsed {
	if s == "" {
		return parsed{}
	}
	if s[0] == 'v' || s[0] == 'V' {
		s = s[1:]
	}
	main := s
	var pre string
	for i := 0; i < len(s); i++ {
		if s[i] == '-' || s[i] == '+' {
			main, pre = s[:i], s[i+1:]
			break
		}
	}
	var nums [3]int
	field := 0
	sawDigit := false
	for i := 0; i < len(main); i++ {
		c := main[i]
		switch {
		case c >= '0' && c <= '9':
			nums[field] = nums[field]*10 + int(c-'0')
			sawDigit = true
			if nums[field] > 1<<30 {
				return parsed{}
			}
		case c == '.' && field < 2 && sawDigit:
			field++
			sawDigit = false
		default:
			return parsed{}
		}
	}
	if !sawDigit {
		return parsed{}
	}
	return parsed{nums[0], nums[1], nums[2], pre, true}
}

// Compare returns -1, 0 or 1 as a is older than, equal to, or newer than b.
// Pre-releases sort before the release they precede (1.2.0-rc1 < 1.2.0).
// Unparseable versions sort before everything.
func Compare(a, b string) int {
	pa, pb := parse(a), parse(b)
	if !pa.ok || !pb.ok {
		if pa.ok == pb.ok {
			return 0
		}
		if pa.ok {
			return 1
		}
		return -1
	}
	cmp := func(x, y int) int {
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
		return 0
	}
	if c := cmp(pa.major, pb.major); c != 0 {
		return c
	}
	if c := cmp(pa.minor, pb.minor); c != 0 {
		return c
	}
	if c := cmp(pa.patch, pb.patch); c != 0 {
		return c
	}
	// Same numbers: a pre-release is older than the plain release.
	switch {
	case pa.pre == pb.pre:
		return 0
	case pa.pre == "":
		return 1
	case pb.pre == "":
		return -1
	case pa.pre < pb.pre:
		return -1
	default:
		return 1
	}
}

// Newer reports whether candidate is strictly newer than current.
func Newer(candidate, current string) bool { return Compare(candidate, current) > 0 }

// Valid reports whether s parses as a version this package understands.
func Valid(s string) bool { return parse(s).ok }
