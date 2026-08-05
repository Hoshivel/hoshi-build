package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/run"
)

func TestVerifyStaticSkipsNonELF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-an-elf")
	if err := os.WriteFile(path, []byte("MZ this is not an ELF file"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := verifyStatic(path)
	if err != nil {
		t.Fatalf("verifyStatic() error = %v", err)
	}
	if res.Checked {
		t.Error("Checked = true；非 ELF 應該是「沒檢查」而不是「通過」")
	}
	if res.Reason == "" {
		t.Error("跳過時必須說明原因，否則讀的人會以為它綠燈了")
	}
}

// The end-to-end proof that hoshi-build produces what the deployment standard
// requires: build a real module with the real toolchain and read the ELF
// header of what comes out.
func TestBuildProducesAStaticallyLinkedBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("靜態連結驗證只在 Linux 產物（ELF）上成立")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("環境裡沒有 go")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")

	root := testRepo(t, "name: fixture\ntype: go\ntargets:\n  - linux/"+runtime.GOARCH+"\n",
		map[string]string{
			"go.mod":              "module fixture\n\ngo 1.24\n",
			"cmd/fixture/main.go": "package main\n\nfunc main() { println(\"ok\") }\n",
		})
	cfg := loadRepo(t, root)

	res, err := Run(context.Background(), cfg, quietPrinter(), run.NewExecRunner(), Options{Verify: true})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	art := res.Artifacts[0]
	if !art.Verify.Checked {
		t.Fatalf("靜態連結沒有被檢查：%+v", art.Verify)
	}
	if !strings.Contains(art.Verify.Detail, "PT_INTERP") {
		t.Errorf("Detail = %q，預期是無 PT_INTERP 的結論", art.Verify.Detail)
	}

	// And confirm the artifact really is where the naming rule says.
	want := filepath.Join(root, "dist", "fixture-linux-"+runtime.GOARCH)
	if art.Path != want {
		t.Errorf("產物在 %s，want %s", art.Path, want)
	}
	if st, err := os.Stat(want); err != nil || st.Mode()&0o111 == 0 {
		t.Errorf("產物不可執行：%v", err)
	}
}

// --no-verify has to actually skip, so the flag means what it says.
func TestVerifyCanBeSkipped(t *testing.T) {
	root := testRepo(t, "name: x\ntype: go\ntargets:\n  - linux/amd64\n", nil)
	cfg := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), cfg, quietPrinter(), r, Options{Verify: false})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Artifacts[0].Verify != (VerifyResult{}) {
		t.Errorf("Verify = %+v，--no-verify 之下不該有結果", res.Artifacts[0].Verify)
	}
}

func TestHostTargetParsesGoEnv(t *testing.T) {
	r := &fakeRunner{captures: map[string]string{
		"go env GOOS GOARCH": "linux\namd64",
	}}
	got, err := hostTarget(context.Background(), r)
	if err != nil {
		t.Fatalf("hostTarget() error = %v", err)
	}
	if got != (config.Target{OS: "linux", Arch: "amd64"}) {
		t.Errorf("hostTarget() = %v", got)
	}
}

func TestHostTargetRejectsGarbage(t *testing.T) {
	r := &fakeRunner{captures: map[string]string{"go env GOOS GOARCH": "linux"}}
	if _, err := hostTarget(context.Background(), r); err == nil {
		t.Fatal("預期報錯")
	}
}
