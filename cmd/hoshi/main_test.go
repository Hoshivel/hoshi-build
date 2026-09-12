package main

import "testing"

// What this tool reports as its own version ends up in every release
// descriptor's `builder.tool_version`, so each of these cases is a different
// answer to "which hoshi-build made this artifact".
func TestResolveVersion(t *testing.T) {
	cases := []struct {
		name    string
		stamped string
		module  string
		want    string
	}{
		{
			// Building this repository with itself: `git describe` says more
			// than a tag can, so it wins even when a module version exists.
			name:    "stamped wins over module",
			stamped: "v0.3.0-4-gabc123-dirty",
			module:  "v0.3.0",
			want:    "v0.3.0-4-gabc123-dirty",
		},
		{
			// The path every repository takes: `go install …@latest` applies
			// none of this repository's ldflags, so the module version is the
			// only thing that knows.
			name:    "installed binary reports the module version",
			stamped: "dev",
			module:  "v0.3.0",
			want:    "v0.3.0",
		},
		{
			// `go build` from a working tree records this. It looks like an
			// answer and is not one, so it must not reach a descriptor.
			name:    "devel is refused",
			stamped: "dev",
			module:  "(devel)",
			want:    "dev",
		},
		{
			// Neither source knows. `dev` has to survive: a reader who sees it
			// knows the artifact cannot name its builder, and an empty string
			// would read as a missing field instead.
			name:    "neither source knows",
			stamped: "dev",
			module:  "",
			want:    "dev",
		},
		{
			// An ldflags typo can stamp an empty string. That is not an
			// answer either, so it falls through like an absent stamp.
			name:    "empty stamp falls through",
			stamped: "",
			module:  "v0.3.0",
			want:    "v0.3.0",
		},
		{
			name:    "empty stamp and no module",
			stamped: "",
			module:  "",
			want:    "dev",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveVersion(c.stamped, c.module); got != c.want {
				t.Errorf("resolveVersion(%q, %q) = %q，預期 %q",
					c.stamped, c.module, got, c.want)
			}
		})
	}
}
