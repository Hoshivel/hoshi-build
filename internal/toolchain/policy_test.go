package toolchain

import (
	"strings"
	"testing"
)

func TestReleasePolicyRejectsUnknownOrUnstableSelections(t *testing.T) {
	p, err := Release()
	if err != nil || p.Toolchain == "" {
		t.Fatal(p, err)
	}
	for _, body := range []string{
		`{"schema":"other","toolchain":"go1.26.10"}`,
		`{"schema":"hoshi.go-release-toolchain/v1","toolchain":"local"}`,
		`{"schema":"hoshi.go-release-toolchain/v1","toolchain":"auto"}`,
		`{"schema":"hoshi.go-release-toolchain/v1","toolchain":"go1.26.10+auto"}`,
		`{"schema":"hoshi.go-release-toolchain/v1","toolchain":"go1.27rc1"}`,
		`{"schema":"hoshi.go-release-toolchain/v1","toolchain":"go1.26.10","other":true}`,
		`{"schema":"hoshi.go-release-toolchain/v1","toolchain":"go1.26.10"} {}`,
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatal("accepted invalid policy", body)
		}
	}
	if strings.Contains(p.Toolchain, "+auto") {
		t.Fatal("release compiler may not resolve to an unpinned version")
	}
}
