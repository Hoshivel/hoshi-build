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
hoshi run
hoshi run server
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
| `hoshi run [名稱]` | 列出或單獨執行 `commands` 中的額外命令 |
| `hoshi env path` | 檢查 Windows 使用者 PATH；`add` 加入、`remove` 移除本執行檔目錄 |
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

- Go 建置固定使用 `CGO_ENABLED=0`、`-mod=readonly`、`-buildvcs=true`、`-trimpath` 與
  `-ldflags "-s -w"`；`go.ldflags` 只能追加。
- 發佈編譯器由 `internal/toolchain/policy.json` 固定，只套用到本次建置子行程；
  會在寫輸出前解析並核對版本，且核對每個執行檔內的實際版本後才寫描述子。
  首次取得工具鏈需要網路，之後沿用 Go 已驗證快取；不修改主機或呼叫端環境。
  `hoshi test` 仍使用明示的測試工具鏈，語言相容測試與發佈版本分開記錄。
- 新 Go 產物須有可讀 Git 身分，且組裝完成後的實際執行檔 VCS revision／modified
  必須與捕獲的來源相符。未知、不一致或來源在建置中改變時拒絕新描述子；
  `-no-verify` 只影響靜態連結檢查。Go 未嵌入 worktree 來源時應改用普通 Git checkout，
  不由工具補造 revision。
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

### Windows：複製貼上安裝（PowerShell）

適用 Windows x64，無需系統管理員或 Go。開啟 PowerShell，把下方整段複製貼上。
它會從最新 release 下載，核對 SHA-256，直接安裝為
`%LOCALAPPDATA%\Programs\hoshi\bin\hoshi.exe`，再由 hoshi 設定使用者 PATH。
`env path` 需要包含此功能的新版本；發布舊版時會在替換現有安裝前停止。

<!-- hoshi-windows-install:start -->
```powershell
& {
  $ErrorActionPreference = 'Stop'
  $bin = Join-Path $env:LOCALAPPDATA 'Programs\hoshi\bin'
  $release = Invoke-RestMethod 'https://api.github.com/repos/Hoshivel/hoshi-build/releases/latest'
  $asset = $release.assets | Where-Object name -eq 'hoshi-windows-amd64.exe'
  $descriptor = $release.assets | Where-Object name -eq 'hoshi-windows-amd64.exe.release.json'
  if (!$asset -or !$descriptor) { throw '此版本沒有 Windows x64 安裝檔或完整性描述子。' }
  New-Item -ItemType Directory -Force -Path $bin | Out-Null
  $exe = Join-Path $bin 'hoshi.exe'
  $download = Join-Path $bin ('hoshi-download-' + [guid]::NewGuid().ToString('N') + '.exe')
  try {
    Invoke-WebRequest -UseBasicParsing -Uri $asset.browser_download_url -OutFile $download
    $sha = [string](Invoke-RestMethod $descriptor.browser_download_url).artifact.sha256
    if ($sha -notmatch '^[0-9a-fA-F]{64}$' -or (Get-FileHash -Algorithm SHA256 -LiteralPath $download).Hash -ine $sha) {
      throw '下載內容的 SHA-256 不符，現有安裝未替換。'
    }
    try { & $download env path --help *> $null } catch { throw '請先使用支援 env path 的新版 hoshi。' }
    if ($LASTEXITCODE -ne 0) { throw '請先使用支援 env path 的新版 hoshi。' }
    if (Test-Path -LiteralPath $exe) { [IO.File]::Replace($download, $exe, [NullString]::Value) }
    else { [IO.File]::Move($download, $exe) }
    & $exe env path add
    if ($LASTEXITCODE -ne 0) { throw 'hoshi 已下載；使用者 PATH 設定失敗，請按上方訊息處理。' }
    if (($env:Path -split ';') -notcontains $bin) { $env:Path = $bin + ';' + $env:Path }
    hoshi version
  } finally {
    if (Test-Path -LiteralPath $download) { Remove-Item -LiteralPath $download -Force }
  }
}
```
<!-- hoshi-windows-install:end -->

下載時的 `-OutFile` 已指定名稱，不必手動改名。重新執行同一段即可更新；下載或雜湊核對
失敗時保留舊安裝，替換時若檔案正在使用會報錯，先結束正在執行的 hoshi 再重試。

這段同時更新目前 PowerShell 的 PATH，所以結尾可直接執行 `hoshi version`。
其他已開啟的終端不會繼承子行程的環境；請完全關閉終端程式後重新開啟。
若版本與剛安裝的不同，用 `Get-Command hoshi -All` 檢查是否還有其他安裝路徑。

已自行下載到固定目錄時，也可直接使用原生環境命令，不需要專案設定檔：

```powershell
$exe = "$env:LOCALAPPDATA\Programs\hoshi\bin\hoshi.exe"
& $exe env path add
& $exe env path status
```

它只管理目前使用者的 PATH，保留其他項目及原登錄型別；`remove` 只移除本執行檔所在
目錄，不刪除檔案。新增加的目錄不會改動既有項目的排序。

需要移除此安裝目錄的 PATH 項目時，才執行：

```powershell
& "$env:LOCALAPPDATA\Programs\hoshi\bin\hoshi.exe" env path remove
```

### Linux x64

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
