package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hoshivel/hoshi-build/internal/userpath"
)

const envUsage = `用法：hoshi env path [status|add|remove]
  status  檢查本執行檔目錄是否已列入使用者 PATH（預設）
  add     加入本執行檔目錄；不更改系統 PATH 或專案設定
  remove  只移除此目錄的 PATH 項目；不刪除安裝檔
`

type pathManager interface {
	Status() (userpath.Result, error)
	Add() (userpath.Result, error)
	Remove() (userpath.Result, error)
}

// cmdEnv manages the host environment without loading any project configuration.
func cmdEnv(args []string) error {
	return runEnv(args, func() (pathManager, error) { return userpath.New() }, os.Stdout)
}

func runEnv(args []string, factory func() (pathManager, error), output io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help")) {
		fmt.Fprint(output, envUsage)
		return nil
	}
	if args[0] != "path" {
		return fmt.Errorf("不認得的 env 子指令 %q；%s", args[0], envUsage)
	}
	if len(args) == 2 && (args[1] == "help" || args[1] == "-h" || args[1] == "--help") {
		fmt.Fprint(output, envUsage)
		return nil
	}
	if len(args) > 2 {
		return fmt.Errorf("env path 不接受額外參數；%s", envUsage)
	}
	action := "status"
	if len(args) == 2 {
		action = args[1]
	}
	if action != "status" && action != "add" && action != "remove" {
		return fmt.Errorf("不認得的 env path 動作 %q；%s", action, envUsage)
	}
	manager, err := factory()
	if err != nil {
		return err
	}
	var result userpath.Result
	switch action {
	case "add":
		result, err = manager.Add()
	case "remove":
		result, err = manager.Remove()
	default:
		result, err = manager.Status()
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "hoshi 所在目錄：%s\n", result.Directory)
	switch {
	case action == "add" && result.Changed:
		fmt.Fprintln(output, "已加入目前使用者的 PATH。")
	case action == "remove" && result.Changed:
		fmt.Fprintln(output, "已從目前使用者的 PATH 移除。")
	case result.Present:
		fmt.Fprintln(output, "使用者 PATH 已包含此目錄（未改動）。")
	default:
		fmt.Fprintln(output, "使用者 PATH 未包含此目錄（未改動）。")
	}
	if result.NotificationError != nil {
		fmt.Fprintf(output, "環境變更已儲存，但通知既有應用程式未完成：%v\n", result.NotificationError)
	}
	if action != "status" {
		fmt.Fprintln(output, "既有終端的環境不會由子行程更新；請關閉並重新開啟終端。")
		if action == "add" {
			directory := strings.ReplaceAll(result.Directory, "'", "''")
			fmt.Fprintf(output, "目前 PowerShell 立即使用（只修改本視窗）：$env:Path += ';' + '%s'\n", directory)
		}
	}
	return nil
}
