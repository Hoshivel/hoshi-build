package build

import (
	"context"
	"regexp"
	"strings"

	"github.com/hoshivel/hoshi-build/internal/run"
)

// DefaultVersion is used when the version cannot be derived from git — an
// export tarball, a fresh repository with no commits, or no git at all.
const DefaultVersion = "dev"

// versionSafe keeps the version usable inside filenames and linker flags. Git
// tags are usually tame, but a branch name can carry a slash and `--dirty`
// output is only as clean as the tag it decorates.
var versionSafe = regexp.MustCompile(`[^A-Za-z0-9._+-]+`)

// resolveVersion returns the version string for this build: the configured
// value if there is one, otherwise `git describe --tags --always --dirty`.
//
// The `--dirty` suffix is deliberate. An artifact built from an uncommitted
// tree is not reproducible from the repository, and the only moment anyone can
// still tell is while it is being built.
func resolveVersion(ctx context.Context, r run.Runner, root, configured string) string {
	if configured != "" {
		return configured
	}
	if _, err := r.Look("git"); err != nil {
		return DefaultVersion
	}
	out, err := r.Capture(ctx, run.Cmd{
		Dir:  root,
		Name: "git",
		Args: []string{"describe", "--tags", "--always", "--dirty"},
	})
	if err != nil {
		return DefaultVersion
	}
	return sanitiseVersion(out)
}

func sanitiseVersion(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = versionSafe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return DefaultVersion
	}
	return s
}
