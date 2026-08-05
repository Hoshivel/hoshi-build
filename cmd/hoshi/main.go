// Command hoshi is Hoshivel's development tool: one binary that builds, tests,
// runs and cleans every repository from a declarative .hoshi-build config.
//
// It exists so that no repository has to hand-write a build script, and so that
// none of them can quietly drift off the flags the deployment standard requires.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hoshivel/hoshi-build/internal/ui"
)

// version is stamped in at build time with
// -ldflags "-X main.version=…" (go.version_var in .hoshi-build.yaml).
var version = "dev"

const usage = `hoshi —— Hoshivel 開發工具

用法：
  hoshi build [選項]     依 .hoshi-build.yaml 建置（預設子指令）
  hoshi test  [選項]     驗證：gofmt / vet / build / test，前端跑設定的 scripts
  hoshi dev   [選項]     啟動開發環境（前後端一起跑，Ctrl+C 一次全停）
  hoshi fmt   [-check]   gofmt -w（-check 只回報不改）
  hoshi clean [選項]     刪除產物（-deps 連 node_modules、-all 全清）
  hoshi setup            安裝相依（go mod download / npm ci）
  hoshi check            只驗設定與倉庫佈局
  hoshi init             依倉庫現況產生一份設定檔
  hoshi version

共通選項：
  -C <路徑>          先切換到這個目錄（預設：目前目錄）
  -config <路徑>     指定設定檔（預設：由目前目錄往上尋找）
  -v                 印出實際執行的指令

build：
  -target <清單>     覆寫 targets，逗號分隔（例：linux/amd64,windows/amd64）
  -package           每個目標另外壓一包（等同 -archive zip）
  -archive <格式>    none / zip / tar.gz
  -output <路徑>     覆寫 output
  -clean             建置前清空輸出目錄
  -skip-go           不建後端
  -skip-npm          不建前端，沿用既有產物
  -set-version <字串>  覆寫版本（預設 git describe --tags --always --dirty）
  -no-verify         跳過靜態連結驗證（不建議：那是部署標準 §1.1 第三條的驗法）

test：
  -race / -no-race   強制開啟 / 關閉競態偵測（預設看設定的 test.race）
  -short  -v  -count <n>  -run <樣式>  -pkg <樣式>
  -no-lint           跳過 gofmt 與 go vet
  -go / -npm         只跑其中一邊

dev：
  -open              前端就緒後自動開瀏覽器
  -only <清單>       只啟動指定的行程，逗號分隔
  -args "<字串>"     附加參數給第一個行程
  -no-check          不檢查埠是否被占用
  -dry-run           只印出要跑什麼

設定檔的完整說明見 docs/config.md。
`

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		if !errors.Is(err, errAlreadyReported) {
			ui.New(os.Stdout, os.Stderr, false).Error("%v", err)
		}
		os.Exit(1)
	}
}

// errAlreadyReported marks failures whose output the subcommand already wrote,
// so main does not print them a second time.
var errAlreadyReported = errors.New("已回報")

func dispatch(args []string) error {
	// Signals have to reach the children: a half-written executable left behind
	// by an interrupted `go build` is worse than no executable, and `hoshi dev`
	// relies on this to tear down every process it started.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := "build"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}

	switch cmd {
	case "build":
		return cmdBuild(ctx, args)
	case "test":
		return cmdTest(ctx, args)
	case "dev":
		return cmdDev(ctx, args)
	case "fmt":
		return cmdFmt(ctx, args)
	case "clean":
		return cmdClean(ctx, args)
	case "setup":
		return cmdSetup(ctx, args)
	case "check":
		return cmdCheck(ctx, args)
	case "init":
		return cmdInit(ctx, args)
	case "version":
		fmt.Println("hoshi " + version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("不認得的子指令 %q", cmd)
	}
}
