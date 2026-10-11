package version

import "testing"

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v0.2.0", "v0.2.0", 0},
		{"0.2.0", "v0.2.0", 0},
		{"v0.2.0", "v0.3.0", -1},
		{"v0.3.0", "v0.2.0", 1},
		{"v0.2.10", "v0.2.9", 1},
		{"v1.0.0", "v0.99.99", 1},
		{"v0.3.0-rc.1", "v0.3.0", -1},
		{"v0.3.0", "v0.3.0-rc.1", 1},
		{"v0.3.0-rc.1", "v0.3.0-rc.2", -1},
		{"v0.3.0-rc.2", "v0.3.0-rc.10", -1},
		{"v0.3.0-alpha", "v0.3.0-alpha.1", -1},
		{"v0.3.0-alpha.1", "v0.3.0-beta", -1},
		{"v0.3.0-1", "v0.3.0-alpha", -1},
		{"v0.3.0-rc.1", "v0.2.9", 1},
		{"v0.2.0+build.5", "v0.2.0", 0},
		{"dev", "v0.2.0", -1},
		{"v0.2.0", "dev", 1},
		{"dev", "garbage", 0},
	}
	for _, tt := range tests {
		if got := Compare(tt.a, tt.b); got != tt.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseRejectsWhatIsNotAVersion(t *testing.T) {
	for _, s := range []string{"", "dev", "1.2", "1.2.3.4", "v1.2.x", "1.02.3", "1.2.3-", "-1.2.3"} {
		if v, ok := Parse(s); ok {
			t.Errorf("Parse(%q) = %+v, want a rejection", s, v)
		}
	}
	v, ok := Parse("v1.2.3-rc.1+abc")
	if !ok || v != (Semver{Major: 1, Minor: 2, Patch: 3, Prerelease: "rc.1"}) {
		t.Errorf("Parse = %+v, %v", v, ok)
	}
}

func TestParseTellsABuildAfterATagFromARelease(t *testing.T) {
	// What `git describe --tags --always --dirty` says of a working tree
	// that is not exactly at a tag: not a version, like "dev".
	for _, s := range []string{"v0.8.1-3-gabcdef", "v0.8.1-12-g1a2b3c4d", "v0.8.1-3-gabcdef-dirty", "v0.8.1-dirty", "v0.8.1-rc.1-2-gabcdef"} {
		if v, ok := Parse(s); ok {
			t.Errorf("Parse(%q) = %+v, want a development build", s, v)
		}
		if Compare(s, "v0.8.1") != -1 || Compare(s, "dev") != 0 {
			t.Errorf("%q should sort as a build that is not a version", s)
		}
	}
	// Pre-releases that merely look alike stay versions.
	for _, s := range []string{"v0.9.0-rc.1", "v0.9.0-3", "v0.9.0-gabcdef", "v0.9.0-3-grc", "v0.9.0-beta-dirty.1"} {
		if _, ok := Parse(s); !ok {
			t.Errorf("Parse(%q) rejected a pre-release", s)
		}
	}
}
