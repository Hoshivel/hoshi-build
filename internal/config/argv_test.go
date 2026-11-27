package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestCmdLinePreservesEmptyArguments(t *testing.T) {
	for _, tc := range []struct{ name, file, body string }{
		{"yaml-list", ".hoshi-build.yaml", "name: fixture\ntype: go\ncommands:\n  server:\n    run: [program, '', '  ', tail]\n"},
		{"yaml-string", ".hoshi-build.yaml", "name: fixture\ntype: go\ncommands:\n  server:\n    run: 'program \"\" \"  \" tail'\n"},
		{"json-list", ".hoshi-build.json", `{"name":"fixture","type":"go","commands":{"server":{"run":["program","","  ","tail"]}}}`},
		{"json-string", ".hoshi-build.json", `{"name":"fixture","type":"go","commands":{"server":{"run":"program \"\" \"  \" tail"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := load(t, tc.file, tc.body)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := c.Commands["server"].Run, (CmdLine{"program", "", "  ", "tail"}); !reflect.DeepEqual(got, want) {
				t.Fatalf("argv=%q, want %q", got, want)
			}
		})
	}
}

func TestCmdLineRejectsAnEmptyExecutable(t *testing.T) {
	for _, body := range []string{"run: ['', program]", "run: ['  ', program]", `run: '"" program'`} {
		_, err := load(t, ".hoshi-build.yaml", "name: fixture\ntype: go\ncommands:\n  server:\n    "+body+"\n")
		if err == nil || !strings.Contains(err.Error(), "run") {
			t.Fatalf("%s: error=%v", body, err)
		}
	}
	_, err := load(t, ".hoshi-build.json", `{"name":"fixture","type":"go","commands":{"server":{"run":["","program"]}}}`)
	if err == nil || !strings.Contains(err.Error(), "run") {
		t.Fatalf("empty JSON executable: %v", err)
	}
}

func TestCmdLineRejectsNonStringArguments(t *testing.T) {
	for _, item := range []string{"null", "42", "true", "{}", "[]"} {
		t.Run("json-"+item, func(t *testing.T) {
			_, err := load(t, ".hoshi-build.json", `{"name":"fixture","type":"go","commands":{"server":{"run":["program",`+item+`]}}}`)
			if err == nil || !strings.Contains(err.Error(), "run") {
				t.Fatalf("item=%s error=%v", item, err)
			}
		})
		t.Run("yaml-"+item, func(t *testing.T) {
			_, err := load(t, ".hoshi-build.yaml", "name: fixture\ntype: go\ncommands:\n  server:\n    run: [program, "+item+"]\n")
			if err == nil || !strings.Contains(err.Error(), "run") {
				t.Fatalf("item=%s error=%v", item, err)
			}
		})
	}
}
