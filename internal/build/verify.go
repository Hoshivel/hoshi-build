package build

import (
	"debug/elf"
	"fmt"
	"strings"
)

// VerifyResult describes what the static-link check could establish.
type VerifyResult struct {
	Checked bool   // false means the format was not one we can read
	Reason  string // why it was skipped, when Checked is false
	Detail  string // what was confirmed, when Checked is true
}

// verifyStatic confirms an ELF artifact needs no dynamic loader at runtime.
//
// The deployment standard requires artifacts to be fully
// statically linked, and §5 names this exact test: the absence of a PT_INTERP
// program header. That is the only check for the rule that does not infer the
// answer from the dependency list — the header says what the kernel will
// actually do with the file.
//
// Running it here rather than in a later conformance sweep means a violation
// fails the build that produced it, while the change that caused it is still
// on screen.
func verifyStatic(path string) (VerifyResult, error) {
	f, err := elf.Open(path)
	if err != nil {
		// Not ELF: a PE (windows) or Mach-O (darwin) artifact. Those platforms
		// have their own linking rules and are not what §1.1 is about — it
		// governs what gets deployed, and deployment targets Linux.
		return VerifyResult{
			Checked: false,
			Reason:  "非 ELF 產物（windows / darwin），靜態連結檢查不適用",
		}, nil
	}
	defer f.Close()

	for _, prog := range f.Progs {
		if prog.Type == elf.PT_INTERP {
			return VerifyResult{}, fmt.Errorf(
				"產物是動態連結的（ELF 帶 PT_INTERP，執行時需要動態載入器）。\n" +
					"      這違反部署標準中「產物必須靜態連結」那一條。\n" +
					"      最常見的原因是相依需要 cgo；SQLite 請用 modernc.org/sqlite，" +
					"不要用 mattn/go-sqlite3")
		}
	}

	// A statically linked Go binary has no dynamic section at all. If one
	// survives without PT_INTERP the file still runs unaided, but it is worth
	// naming in the output rather than silently calling it clean.
	libs, err := f.ImportedLibraries()
	if err == nil && len(libs) > 0 {
		return VerifyResult{
			Checked: true,
			Detail:  "無 PT_INTERP，但仍列有共享函式庫：" + strings.Join(libs, "、"),
		}, nil
	}

	return VerifyResult{Checked: true, Detail: "靜態連結（無 PT_INTERP）"}, nil
}
