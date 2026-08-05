package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Target is one cross-compilation target, written `os/arch` in the file.
type Target struct {
	OS   string
	Arch string
}

// targetRe matches `os/arch`. The values are not checked against Go's list of
// known platforms — `go build` reports an unknown one better than a table in
// here would, and that table would go stale.
var targetRe = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)

// ParseTarget reads an `os/arch` string.
func ParseTarget(s string) (Target, error) {
	s = strings.TrimSpace(s)
	if !targetRe.MatchString(s) {
		return Target{}, fmt.Errorf("%q 不是 `os/arch`（例：linux/amd64）", s)
	}
	os, arch, _ := strings.Cut(s, "/")
	return Target{OS: os, Arch: arch}, nil
}

// UnmarshalYAML lets `targets:` hold plain `linux/amd64` strings.
func (t *Target) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("targets 的項目必須是字串")
	}
	parsed, err := ParseTarget(s)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// UnmarshalJSON is the same for .hoshi-build.json.
func (t *Target) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("targets 的項目必須是字串")
	}
	parsed, err := ParseTarget(s)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

func (t Target) MarshalYAML() (any, error) { return t.String(), nil }

// CmdLine is a command as argv. It accepts either a list, which is exact, or a
// string, which is split on whitespace with quoting honoured.
//
// The list form is the one to reach for when an argument contains spaces. The
// string form exists because `run: npm run dev` is what people write, and
// refusing it would buy nothing.
//
// Note this is a split, not a shell: no globbing, no pipes, no `&&`. A step
// that needs a shell should say so — `run: [sh, -c, "a && b"]` — rather than
// have one appear implicitly on Unix and not on Windows.
type CmdLine []string

func (c *CmdLine) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		var list []string
		if err := node.Decode(&list); err != nil {
			return fmt.Errorf("`run` 的清單項目必須是字串")
		}
		return c.set(list)
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return fmt.Errorf("`run` 必須是字串或清單")
		}
		fields, err := SplitArgs(s)
		if err != nil {
			return err
		}
		return c.set(fields)
	default:
		return fmt.Errorf("`run` 必須是字串或清單")
	}
}

func (c *CmdLine) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		return c.set(list)
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("`run` 必須是字串或清單")
	}
	fields, err := SplitArgs(s)
	if err != nil {
		return err
	}
	return c.set(fields)
}

func (c *CmdLine) set(fields []string) error {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return fmt.Errorf("`run` 是空的")
	}
	*c = out
	return nil
}

func (c CmdLine) String() string { return strings.Join(c, " ") }

// SplitArgs splits a command line on whitespace, honouring single and double
// quotes so a path with a space survives.
func SplitArgs(s string) ([]string, error) {
	var (
		out   []string
		cur   strings.Builder
		quote byte
		open  bool // cur holds something, even if empty ("" is an argument)
	)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
				continue
			}
			cur.WriteByte(ch)
		case ch == '\'' || ch == '"':
			quote = ch
			open = true
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			if cur.Len() > 0 || open {
				out = append(out, cur.String())
				cur.Reset()
				open = false
			}
		default:
			cur.WriteByte(ch)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("命令 %q 的引號沒有收尾", s)
	}
	if cur.Len() > 0 || open {
		out = append(out, cur.String())
	}
	return out, nil
}
