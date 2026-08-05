package build

import (
	"context"
	"testing"
)

func TestSanitiseVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{"v1.2.3", "v1.2.3"},
		{"v1.2.3-4-gabc1234", "v1.2.3-4-gabc1234"},
		{"v1.2.3-dirty", "v1.2.3-dirty"},
		{"abc1234", "abc1234"},
		{"  v1.0.0  ", "v1.0.0"},
		{"v1.0.0\nrubbish", "v1.0.0"},
		// A version ends up in filenames and linker flags, so anything that
		// would break either gets flattened rather than passed through.
		{"feature/branch", "feature-branch"},
		{"v1 2", "v1-2"},
		{"", DefaultVersion},
		{"///", DefaultVersion},
	}
	for _, tc := range tests {
		if got := sanitiseVersion(tc.in); got != tc.want {
			t.Errorf("sanitiseVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveVersionPrefersTheConfiguredValue(t *testing.T) {
	r := &fakeRunner{captures: map[string]string{
		"git describe --tags --always --dirty": "v9.9.9",
	}}
	if got := resolveVersion(context.Background(), r, "/repo", "v1.0.0"); got != "v1.0.0" {
		t.Errorf("resolveVersion() = %q, want v1.0.0", got)
	}
	if r.ran("git") {
		t.Error("設定了版本就不必問 git")
	}
}

func TestResolveVersionAsksGit(t *testing.T) {
	r := &fakeRunner{captures: map[string]string{
		"git describe --tags --always --dirty": "v1.2.3-dirty",
	}}
	if got := resolveVersion(context.Background(), r, "/repo", ""); got != "v1.2.3-dirty" {
		t.Errorf("resolveVersion() = %q, want v1.2.3-dirty", got)
	}
}

func TestResolveVersionFallsBackWithoutGit(t *testing.T) {
	r := &fakeRunner{missing: map[string]bool{"git": true}}
	if got := resolveVersion(context.Background(), r, "/repo", ""); got != DefaultVersion {
		t.Errorf("resolveVersion() = %q, want %q", got, DefaultVersion)
	}
}
