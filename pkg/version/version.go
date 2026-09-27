// Package version holds the build version shared by the agent and the CLI.
package version

import (
	"strconv"
	"strings"
)

// Version is overridden at build time with:
//
//	-ldflags "-X github.com/shipwick/shipwick/pkg/version.Version=v1.2.3"
var Version = "dev"

// Semver is a parsed version. Build metadata (after "+") is dropped: two
// builds of the same version are the same version.
type Semver struct {
	Major, Minor, Patch int
	Prerelease          string
}

// Parse reads "v1.2.3", "1.2.3" or "v1.2.3-rc.1". A development build ("dev")
// and anything else that is not a version returns false.
func Parse(s string) (Semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v Semver
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.Prerelease = s[i+1:]
		s = s[:i]
		if v.Prerelease == "" {
			return Semver{}, false
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Semver{}, false
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p != strconv.Itoa(n) {
			return Semver{}, false
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, true
}

// Compare orders two versions the way semver.org does: by major, minor and
// patch, and a pre-release sorts before the release it precedes. It returns
// -1, 0 or +1. A string that is not a version sorts before every one that is,
// and equal to another that is not.
func Compare(a, b string) int {
	va, oka := Parse(a)
	vb, okb := Parse(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return -1
	case !okb:
		return 1
	}
	for _, d := range [3]int{va.Major - vb.Major, va.Minor - vb.Minor, va.Patch - vb.Patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case va.Prerelease == vb.Prerelease:
		return 0
	case va.Prerelease == "":
		return 1
	case vb.Prerelease == "":
		return -1
	}
	return comparePrerelease(va.Prerelease, vb.Prerelease)
}

// comparePrerelease is semver's rule 11: dot-separated identifiers, numeric
// ones compared as numbers and ranked before alphanumeric ones, the shorter
// list first when everything before is equal.
func comparePrerelease(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		na, erra := strconv.Atoi(as[i])
		nb, errb := strconv.Atoi(bs[i])
		switch {
		case erra == nil && errb == nil:
			if na != nb {
				return sign(na - nb)
			}
		case erra == nil:
			return -1
		case errb == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return sign(len(as) - len(bs))
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
