package tasks

import (
	"bytes"
	"context"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/dev"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

func TestRunCommandDryRunOnlySelectsNamedTask(t *testing.T) {
	original := config.CmdLine{"missing-task-executable", "original"}
	c := &config.Config{Name: "fixture", Root: t.TempDir(), Type: config.TypeNpm,
		Commands: map[string]config.NamedCommand{"server": {Run: append(config.CmdLine(nil), original...)}},
		Dev:      config.DevConfig{Processes: []config.Process{{Name: "not-selected", Run: config.CmdLine{"missing-dev-executable"}}}}}
	var out bytes.Buffer
	err := RunCommand(context.Background(), c, ui.New(&out, &out, false), "server", dev.Options{DryRun: true, Args: []string{"two words", "世界"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "missing-task-executable") || strings.Contains(out.String(), "missing-dev-executable") || !strings.Contains(out.String(), "two words") {
		t.Fatalf("wrong plan: %s", &out)
	}
	if !reflect.DeepEqual(c.Commands["server"].Run, original) {
		t.Fatalf("configured argv mutated: %v", c.Commands["server"].Run)
	}
}

func TestRunCommandUnknownDoesNotStartAnything(t *testing.T) {
	c := &config.Config{Commands: map[string]config.NamedCommand{"z": {}, "a": {}}}
	var out bytes.Buffer
	err := RunCommand(context.Background(), c, ui.New(&out, &out, false), "missing", dev.Options{})
	if err == nil || !strings.Contains(err.Error(), "a、z") || out.Len() != 0 {
		t.Fatalf("error=%v output=%q", err, &out)
	}
}

func TestRunCommandChecksItsDeclaredPortBeforeStarting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	c := &config.Config{Name: "fixture", Root: t.TempDir(), Commands: map[string]config.NamedCommand{"server": {Run: config.CmdLine{"must-not-be-started"}, Ports: []int{port}}}}
	var out bytes.Buffer
	err = RunCommand(context.Background(), c, ui.New(&out, &out, false), "server", dev.Options{})
	if err == nil || !strings.Contains(err.Error(), "埠") {
		t.Fatalf("expected port refusal, got %v", err)
	}
}
