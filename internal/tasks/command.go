package tasks

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/dev"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

// RunCommand executes only the named task, using the managed foreground runner.
func RunCommand(ctx context.Context, c *config.Config, p *ui.Printer, name string, opts dev.Options) error {
	command, ok := c.Commands[name]
	if !ok {
		return fmt.Errorf("`commands` 裡沒有 %q；可用命令：%s", name, strings.Join(c.CommandNames(), "、"))
	}
	process := config.Process{Name: name, Dir: command.Dir, Run: command.Run, Env: command.Env, Ports: command.Ports}
	if len(process.Run) == 0 {
		return fmt.Errorf("`commands.%s` 少了 `run`", name)
	}
	return dev.RunProcesses(ctx, c, p, []config.Process{process}, opts, "命令 "+name)
}
