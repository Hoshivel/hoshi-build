package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestNamedCommandsDecode(t *testing.T) {
	for _, file := range []string{".hoshi-build.yaml", ".hoshi-build.json"} {
		t.Run(file, func(t *testing.T) {
			body := `name: fixture
type: npm
commands:
  server:
    description: Independent service
    run: [npm, run, server]
    env: [PORT=8080]
    ports: [8080]
  data:check:
    run: go test ./data
    dir: tools
`
			if strings.HasSuffix(file, ".json") {
				body = `{"name":"fixture","type":"npm","commands":{"server":{"description":"Independent service","run":["npm","run","server"],"env":["PORT=8080"],"ports":[8080]},"data:check":{"run":"go test ./data","dir":"tools"}}}`
			}
			c, err := load(t, file, body)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(c.CommandNames(), []string{"data:check", "server"}) {
				t.Fatalf("names=%v", c.CommandNames())
			}
			if got := c.Commands["server"]; got.Dir != "." || !reflect.DeepEqual(got.Run, CmdLine{"npm", "run", "server"}) || got.Ports[0] != 8080 || got.Env[0] != "PORT=8080" {
				t.Fatalf("server=%+v", got)
			}
			if got := c.Commands["data:check"]; got.Dir != "tools" || !reflect.DeepEqual(got.Run, CmdLine{"go", "test", "./data"}) {
				t.Fatalf("task=%+v", got)
			}
			if len(c.Dev.Processes) != 1 || c.Dev.Processes[0].Name != "frontend" {
				t.Fatalf("custom commands entered dev: %+v", c.Dev.Processes)
			}
		})
	}
}

func TestNamedCommandsInvalidFields(t *testing.T) {
	for _, tc := range []struct{ name, body, key string }{
		{"missing-run", "commands:\n  server: {}\n", "commands.server"},
		{"absolute-dir", "commands:\n  server:\n    run: echo yes\n    dir: C:/outside\n", "commands.server.dir"},
		{"escaping-dir", "commands:\n  server:\n    run: echo yes\n    dir: ../outside\n", "commands.server.dir"},
		{"bad-env", "commands:\n  server:\n    run: echo yes\n    env: [BROKEN]\n", "commands.server.env"},
		{"empty-env-key", "commands:\n  server:\n    run: echo yes\n    env: ['=value']\n", "commands.server.env"},
		{"bad-port", "commands:\n  server:\n    run: echo yes\n    ports: [70000]\n", "commands.server.ports"},
		{"flag-name", "commands:\n  -bad:\n    run: echo yes\n", "commands"},
		{"unknown-yaml", "commands:\n  server:\n    run: echo yes\n    typo: true\n", "typo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, ".hoshi-build.yaml", "name: fixture\ntype: go\n"+tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error=%v, want %s", err, tc.key)
			}
		})
	}
	_, err := load(t, ".hoshi-build.json", `{"name":"fixture","type":"go","commands":{"server":{"run":["echo","yes"],"typo":true}}}`)
	if err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("unknown JSON key: %v", err)
	}
}

func TestOmittedNamedCommandsPreservesDefaults(t *testing.T) {
	c := mustLoad(t, "name: fixture\ntype: go\n")
	if len(c.Commands) != 0 || len(c.CommandNames()) != 0 || len(c.Dev.Processes) != 1 || c.Dev.Processes[0].Name != "backend" {
		t.Fatalf("defaults changed: %+v", c)
	}
}
