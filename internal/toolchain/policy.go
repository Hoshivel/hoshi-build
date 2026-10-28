// Package toolchain owns the compiler selected for release artifacts.
package toolchain

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

//go:embed policy.json
var policyJSON []byte

type Policy struct {
	Schema    string `json:"schema"`
	Toolchain string `json:"toolchain"`
}

var stableVersion = regexp.MustCompile(`^go[1-9][0-9]*\.[0-9]+\.[0-9]+$`)

func Parse(body []byte) (Policy, error) {
	var p Policy
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || p.Schema != "hoshi.go-release-toolchain/v1" || !stableVersion.MatchString(p.Toolchain) {
		return Policy{}, errors.New("無效的發佈 Go 編譯器政策")
	}
	return p, nil
}

func Release() (Policy, error) { return Parse(policyJSON) }
