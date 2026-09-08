# hoshi-build

Hoshivel 的建置工具。每個倉庫以 `.hoshi-build.yaml` 宣告差異，`hoshi` 統一執行
建置、測試、開發與清理流程。

```sh
hoshi build
hoshi build -package
hoshi build -target linux/amd64,windows/amd64
hoshi test
hoshi dev
hoshi dev -open
```

最短設定：

```yaml
name: my-service
type: go
output: dist/
```

`test` 與 `dev` 預設由 `type` 推導。完整鍵位見 [`docs/config.md`](docs/config.md)。

## 指令

| 指令 | 用途 |
|---|---|
| `hoshi build` | 建置產物；預設子指令 |
| `hoshi test` | format、vet、build、test、前端 script 與自訂驗證 |
| `hoshi dev` | 在前景同時執行開發行程 |
| `hoshi fmt` | 執行 `gofmt -w`；`-check` 只檢查 |
| `hoshi clean` | 刪除產物；`-deps` 含相依，`-all` 全清 |
| `hoshi setup` | `go mod download`／`npm ci` |
| `hoshi check` | 只檢查設定與倉庫佈局 |
| `hoshi init` | 依現況產生設定檔 |

完整旗標使用 `hoshi help`。

## 專案型別與產物

| `type` | 用途 | 預設產物 |
|---|---|---|
| `go` | 後端 | `dist/<name>-<os>-<arch>` |
| `go-npm` | 後端與隨附前端 | 同名目錄，含執行檔與 `web/` |
| `npm` | 純前端／靜態站 | `dist/` 靜態檔 |

產物形狀由 target 的檔案數決定：只有執行檔時輸出單檔；含前端或 `include` 時輸出目錄。

每個產物旁邊另有一份 `<產物名>.release.json`——完整 commit 與完整 artifact sha256，
灰度放量時用來判斷「要放的是不是驗過的那一份」。見 [`docs/config.md`](docs/config.md) §4.1。

## 強制規則

- Go 建置固定使用 `CGO_ENABLED=0`、`-mod=readonly`、`-trimpath` 與
  `-ldflags "-s -w"`；`go.ldflags` 只能追加。
- Linux ELF 建置後驗證沒有 `PT_INTERP`；非 ELF 目標明示未檢查。
- `gofmt -l` 有任何輸出即失敗。
- `-race` 不可用時，明示降級並改跑 `-count 2`；不得宣稱已做競態檢查。
- YAML／JSON 未知鍵直接報錯，並顯示設定路徑與行號。

## `hoshi dev`

- 所有行程留在同一終端機，輸出帶名稱前綴。
- Ctrl+C 先送中斷，5 秒後強制終止仍存活的行程群組。
- 任一行程結束時停止整組。
- 啟動前檢查宣告的埠；`-open` 等待目標 URL 的埠就緒後才開瀏覽器。

```text
backend  │ listening on :8080
frontend │ vite ready in 412 ms
```

## 安裝

下載 release 單檔：

```sh
curl -fsSLO https://github.com/Hoshivel/hoshi-build/releases/latest/download/hoshi-linux-amd64
sudo install -m 0755 hoshi-linux-amd64 /usr/local/bin/hoshi
hoshi version
```

支援 `linux`、`darwin`、`windows` 的 release targets。從原始碼自舉：

```sh
git clone https://github.com/Hoshivel/hoshi-build.git
cd hoshi-build
go build -o hoshi ./cmd/hoshi
sudo install -m 0755 hoshi /usr/local/bin/
```

自舉產物只用來執行 `hoshi build`；合規出貨產物由 hoshi 自己建置。

## 責任邊界

- 工具只認 `type`，不認倉庫名稱；業務內容寫在各倉庫設定。
- 本工具只產生產物，不負責部署。
- 不依賴平臺執行期 SDK。
- 可新增有明確必要性的程式庫；目前唯一第三方相依是 `gopkg.in/yaml.v3`。

## 開發

```sh
go build ./... && go vet ./... && gofmt -l . && go test -race ./...
go run ./cmd/hoshi build
go run ./cmd/hoshi test
```
