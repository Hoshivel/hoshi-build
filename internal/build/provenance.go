package build

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hoshivel/hoshi-build/internal/run"
)

// verifyGoIdentity reads the actual assembled executable. A successful build
// flag or a Git-derived descriptor is not evidence that the binary carries it.
func (b *builder) verifyGoIdentity(ctx context.Context, binary string, source gitIdentity) error {
	out, err := b.runner.Capture(ctx, run.Cmd{Name: "go", Args: []string{"version", "-m", binary}})
	if err != nil {
		return fmt.Errorf("讀取產物 %s 的實際 VCS 身分失敗：%w", binary, err)
	}
	first, rest, _ := strings.Cut(out, "\n")
	if !strings.HasSuffix(strings.TrimSpace(first), ": "+b.releaseGo) {
		return fmt.Errorf("產物 %s 的編譯器不符合發佈政策 %s", binary, b.releaseGo)
	}
	settings := make(map[string]string)
	for _, line := range strings.Split(rest, "\n") {
		value, found := strings.CutPrefix(line, "\tbuild\t")
		if !found {
			continue
		}
		key, value, found := strings.Cut(value, "=")
		if !found || (key != "vcs" && key != "vcs.revision" && key != "vcs.modified") {
			continue
		}
		if _, duplicate := settings[key]; duplicate {
			return fmt.Errorf("產物 %s 的 %s 重複；未寫入發佈描述子", binary, key)
		}
		settings[key] = strings.TrimSuffix(value, "\r")
	}
	if settings["vcs"] != "git" || settings["vcs.revision"] != source.commit ||
		settings["vcs.modified"] != strconv.FormatBool(source.dirty) {
		return fmt.Errorf("產物 %s 的實際 VCS 身分缺失或不符：vcs=%q revision=%q modified=%q；"+
			"預期 Git %s modified=%t。請使用能讓 Go 嵌入來源的 Git checkout；未寫入發佈描述子",
			binary, settings["vcs"], settings["vcs.revision"], settings["vcs.modified"], source.commit, source.dirty)
	}
	return nil
}
