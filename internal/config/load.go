package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Filenames recognised in a repository root, in the order they are tried.
var Filenames = []string{
	".hoshi-build.yaml",
	".hoshi-build.yml",
	".hoshi-build.json",
}

// ErrNotFound is returned by Find when no configuration exists.
var ErrNotFound = errors.New("找不到 .hoshi-build.yaml / .yml / .json")

// Find looks for a configuration in dir and then in each parent, so `hoshi`
// works from anywhere inside a repository the way git does.
func Find(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		for _, name := range Filenames {
			candidate := filepath.Join(abs, name)
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				return candidate, nil
			}
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", ErrNotFound
		}
		abs = parent
	}
}

// Load reads, decodes and validates one configuration file.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}

	c := &Config{}
	if err := decode(abs, data, c); err != nil {
		return nil, fmt.Errorf("%s：%w", filepath.Base(abs), err)
	}

	c.Path = abs
	c.Root = filepath.Dir(abs)

	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s：%w", filepath.Base(abs), err)
	}
	c.applyDefaults()
	return c, nil
}

// LoadFrom finds the configuration starting at dir and loads it.
func LoadFrom(dir string) (*Config, error) {
	path, err := Find(dir)
	if err != nil {
		return nil, err
	}
	return Load(path)
}

// decode dispatches on the extension. Both decoders reject unknown fields:
// a mistyped key that reads as "unset" is the failure mode worth engineering
// against, because its only symptom is output appearing somewhere else.
func decode(path string, data []byte, c *Config) error {
	if filepath.Ext(path) == ".json" {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(c); err != nil {
			return fmt.Errorf("JSON 解析失敗：%w", err)
		}
		if dec.More() {
			return fmt.Errorf("JSON 解析失敗：檔案裡有第二個值")
		}
		return nil
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("設定檔是空的")
		}
		return fmt.Errorf("YAML 解析失敗：%s", tidyYAMLError(err))
	}

	// A second document would be silently ignored, and the half that got
	// ignored is never the half you expected.
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return fmt.Errorf("YAML 解析失敗：檔案裡有第二份文件（`---`）")
	}
	return nil
}

// sectionPrefix maps a decoder type back to the key path a reader would type,
// so "type config.GoConfig" becomes "go.".
var sectionPrefix = map[string]string{
	"config.Config":      "",
	"config.GoConfig":    "go.",
	"config.NpmConfig":   "npm.",
	"config.TestConfig":  "test.",
	"config.DevConfig":   "dev.",
	"config.CleanConfig": "clean.",
	"config.Command":     "test.commands[].",
	"config.Process":     "dev.processes[].",
}

// yamlKind renders go-yaml's `!!seq` shorthand as the word used everywhere
// else in this tool's messages.
var yamlKind = map[string]string{
	"!!seq":   "清單",
	"!!map":   "區段",
	"!!str":   "字串",
	"!!int":   "數字",
	"!!float": "數字",
	"!!bool":  "布林值",
	"!!null":  "空值",
}

var (
	reUnknownField = regexp.MustCompile(`field (\S+) not found in type (\S+)`)
	reCannotUnmar  = regexp.MustCompile("cannot unmarshal (!!\\w+)(?: `[^`]*`)? into (\\S+)")
	reLine         = regexp.MustCompile(`^\s*line (\d+): `)
)

// tidyYAMLError rewrites go-yaml's messages into this tool's voice.
//
// The wording matters more than it looks: an unknown key is the failure this
// parser is strict about in the first place, and "field packge not found in
// type config.GoConfig" does not tell a reader that they should have written
// `go.package`.
func tidyYAMLError(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: unmarshal errors:\n")
	msg = strings.TrimPrefix(msg, "yaml: ")

	lines := strings.Split(msg, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		where := ""
		if m := reLine.FindStringSubmatch(line); m != nil {
			where = "第 " + m[1] + " 行："
			line = reLine.ReplaceAllString(line, "")
		}
		out = append(out, where+translateYAML(strings.TrimSpace(line)))
	}
	if len(out) == 1 {
		return out[0]
	}
	return "\n  - " + strings.Join(out, "\n  - ")
}

func translateYAML(line string) string {
	if m := reUnknownField.FindStringSubmatch(line); m != nil {
		prefix, known := sectionPrefix[m[2]]
		if !known {
			prefix = ""
		}
		return fmt.Sprintf("不認得的鍵 `%s%s`（打錯字的鍵會被當成沒設定，所以這裡不放過）",
			prefix, m[1])
	}
	if m := reCannotUnmar.FindStringSubmatch(line); m != nil {
		got := yamlKind[m[1]]
		if got == "" {
			got = m[1]
		}
		want := "字串"
		switch {
		case strings.HasPrefix(m[2], "[]"):
			want = "清單"
		case sectionPrefix[m[2]] != "" || m[2] == "config.Config":
			want = "區段"
		case m[2] == "int" || m[2] == "*int":
			want = "數字"
		case m[2] == "bool" || m[2] == "*bool":
			want = "布林值"
		}
		return fmt.Sprintf("期望%s，得到%s", want, got)
	}
	return line
}
