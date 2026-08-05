package build

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hoshivel/hoshi-build/internal/config"
)

func cfg(t *testing.T, mutate func(*config.Config)) *config.Config {
	t.Helper()
	c := &config.Config{
		Name: "demo-api",
		Type: config.TypeGo,
		Go: config.GoConfig{
			Dir:     ".",
			Package: "./cmd/demo-api",
		},
	}
	if mutate != nil {
		mutate(c)
	}
	return c
}

var linux = config.Target{OS: "linux", Arch: "amd64"}

// The exact flags are the product. The deployment standard
// §1.2 spells out three of them, and the whole point of hoshi-build is that no
// repository has to remember them — so something has to remember for it.
func TestGoArgsCarriesTheMandatoryFlags(t *testing.T) {
	got := goArgs(cfg(t, nil), linux, "/out/demo-api-linux-amd64", "v1.2.3")
	want := []string{
		"build",
		"-mod=readonly",
		"-trimpath",
		"-ldflags", "-s -w",
		"-o", "/out/demo-api-linux-amd64",
		"./cmd/demo-api",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("goArgs() =\n  %q\nwant\n  %q", got, want)
	}
}

// Go defaults to -mod=readonly, but GOFLAGS=-mod=mod in the environment
// overrides that — and then a build quietly rewrites go.mod on its way past.
// This was not hypothetical: it happened while verifying the configurations
// for this very tool, and bumped a service's pinned SDK version.
func TestGoArgsPinsModReadonly(t *testing.T) {
	got := goArgs(cfg(t, nil), linux, "/out/x", "v1")
	for _, a := range got {
		if a == "-mod=readonly" {
			return
		}
	}
	t.Errorf("goArgs() = %q，少了 -mod=readonly；建置不得改動 go.mod", got)
}

func TestGoEnvDisablesCgo(t *testing.T) {
	got := goEnv(linux)
	want := []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("goEnv() = %q, want %q", got, want)
	}
}

func TestGoArgsInjectsVersion(t *testing.T) {
	c := cfg(t, func(c *config.Config) { c.Go.VersionVar = "main.version" })
	got := strings.Join(goArgs(c, linux, "/out/x", "v1.2.3"), " ")

	if !strings.Contains(got, "-X main.version=v1.2.3") {
		t.Errorf("goArgs() = %q，少了 -X 注入", got)
	}
	if !strings.Contains(got, "-s -w -X") {
		t.Errorf("goArgs() = %q，-X 應該接在 -s -w 後面", got)
	}
}

// A repository can add linker flags but must not be able to drop the standard
// ones by supplying its own — that would put the escape hatch and the rule in
// the same key.
func TestGoArgsAppendsRatherThanReplaces(t *testing.T) {
	c := cfg(t, func(c *config.Config) { c.Go.Ldflags = "-X main.channel=beta" })
	got := goArgs(c, linux, "/out/x", "v1")

	var ldflags string
	for i, a := range got {
		if a == "-ldflags" {
			ldflags = got[i+1]
		}
	}
	if !strings.HasPrefix(ldflags, "-s -w ") {
		t.Errorf("-ldflags = %q，標準的 -s -w 必須還在最前面", ldflags)
	}
	if !strings.Contains(ldflags, "-X main.channel=beta") {
		t.Errorf("-ldflags = %q，倉庫自己的旗標不見了", ldflags)
	}
}

func TestGoArgsTags(t *testing.T) {
	c := cfg(t, func(c *config.Config) { c.Go.Tags = []string{"netgo", "osusergo"} })
	got := goArgs(c, linux, "/out/x", "v1")

	for i, a := range got {
		if a == "-tags" {
			if got[i+1] != "netgo,osusergo" {
				t.Errorf("-tags = %q, want %q", got[i+1], "netgo,osusergo")
			}
			return
		}
	}
	t.Errorf("goArgs() = %q，沒有 -tags", got)
}

func TestGoArgsOmitsTagsWhenEmpty(t *testing.T) {
	for _, a := range goArgs(cfg(t, nil), linux, "/out/x", "v1") {
		if a == "-tags" {
			t.Fatal("沒有 tags 時不該出現 -tags")
		}
	}
}
