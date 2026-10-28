package build

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoshivel/hoshi-build/internal/run"
)

func (b *builder) verifyGoCompiler(ctx context.Context, binary string) error {
	out, err := b.runner.Capture(ctx, run.Cmd{Name: "go", Args: []string{"version", "-m", binary}})
	if err != nil {
		return fmt.Errorf("讀取產物編譯器失敗：%w", err)
	}
	first, _, _ := strings.Cut(out, "\n")
	if !strings.HasSuffix(strings.TrimSpace(first), ": "+b.releaseGo) {
		return fmt.Errorf("產物 %s 的編譯器不符合發佈政策 %s", binary, b.releaseGo)
	}
	return nil
}
