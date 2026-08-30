<!-- hoshivel:agent-rules v1 -> https://github.com/Hoshivel/workspace -->

# AGENTS.md — hoshi-build

> 共通流程以 [workspace](https://github.com/Hoshivel/workspace) 的 `AGENTS.md`
> 為準；本檔只列本倉庫規則。

## 0. 開工前

1. 確認 `../workspace` 在場；缺少時執行
   `git clone https://github.com/Hoshivel/workspace.git ../workspace`。
2. 讀 `../workspace/focus.md` 與 `../workspace/AGENTS.md`；取不到就停止並說明。
3. 待辦與日誌分別放在 `workspace/todo/hoshi-build/`、
   `workspace/logs/hoshi-build/`；不得在本倉庫另建副本。
4. 續接事項時沿用其分支與 PR。

## 1. 入場閱讀順序

1. `README.md`：子指令、專案型別與責任邊界。
2. `../hoshi-platform-standards/engineering/deployment.md`：產物規範。
3. `../hoshi-platform-standards/engineering/build.md`：設定檔與建置規範。
4. `docs/config.md`：完整設定參考。

## 2. 驗證

```sh
go build ./... && go vet ./... && gofmt -l . && go test -race ./...
go run ./cmd/hoshi build
go run ./cmd/hoshi test
GOOS=windows go build ./...
```

`gofmt -l .` 必須無輸出；`go test -race` 不得省略。

## 3. 特殊規則

- `CGO_ENABLED=0`、`-trimpath`、`-ldflags "-s -w"` 是內建規則，不得改成設定鍵；
  `go.ldflags` 只能追加。
- 可新增有明確必要性的程式庫；本工具以二進位檔交付，不要求零相依。
- YAML 與 JSON 的未知鍵一律報錯；錯誤訊息必須使用設定檔中的鍵名與路徑。
- 未設定的 `test`、`dev` 等行為須由 `type` 推導；可推導的值不得改為必填。
- 工具只認專案型別，不認倉庫名稱；業務差異寫入各倉庫的 `.hoshi-build.*`。
- `hoshi dev` 的子行程留在前景與獨立行程群組；中斷時終止整棵子行程樹。
- 設定鍵變更須先改建置規範，再改本倉庫與所有使用者。
- 本工具不得依賴 hoshi-platform-sdk。
- 文件用正體中文，程式碼註解用英文；規範關鍵字依 RFC 2119 使用。
