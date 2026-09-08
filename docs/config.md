# `.hoshi-build` 設定參考

## 1. 檔案解析

`hoshi` 從目前目錄向上尋找，依序取第一個：

1. `.hoshi-build.yaml`
2. `.hoshi-build.yml`
3. `.hoshi-build.json`

`-config <path>` 指定檔案；`-C <dir>` 先切換目錄。一個倉庫只應提交一份設定。

最短設定：

```yaml
name: my-service
type: go
output: dist/
```

只寫與預設不同的值。

## 2. 鍵位

### 頂層

| 鍵 | 型別 | 預設 | 說明 |
|---|---|---|---|
| `name` | string | 必填 | 產物名；小寫英數及 `.`、`_`、`-`，以英數開頭 |
| `type` | string | 必填 | `go`、`go-npm`、`npm` |
| `output` | path | `dist` | 倉庫內的相對輸出路徑 |
| `version` | string | git | 留空時見〈版本〉 |
| `targets` | list | 本機 | `os/arch`；`npm` 禁用 |
| `archive` | string | `none` | `none`、`zip`、`tar.gz` |
| `include` | list | 空 | 隨附檔案／目錄；`npm` 禁用 |

### `go`

`npm` 型別禁用。

| 鍵 | 預設 | 說明 |
|---|---|---|
| `dir` | `.` | `go.mod` 所在目錄 |
| `package` | `./cmd/<name>` | main package，相對 `dir` |
| `tags` | 空 | build tags |
| `ldflags` | 空 | 追加在 `-s -w` 後 |
| `version_var` | 空 | 以 `-X` 注入版本的變數，如 `main.version` |

### `npm`

`go` 型別禁用。

| 鍵 | 預設 | 說明 |
|---|---|---|
| `dir` | `.` | `package.json` 所在目錄 |
| `script` | `build` | 建置 script |
| `output` | `dist` | script 產物目錄，相對 `dir` |
| `web_dir` | `web` | `go-npm` 產物中的前端目錄名 |
| `install` | `auto` | `auto`、`always`、`never` |

安裝時有 lockfile 使用 `npm ci`，否則使用 `npm install`。

### `test`

| 鍵 | 預設 | 說明 |
|---|---|---|
| `lint` | `true` | `gofmt -l .` 與 `go vet ./...` |
| `race` | `true` | `go test -race` |
| `packages` | `./...` | Go 測試範圍 |
| `flags` | 空 | 額外 Go test flags |
| `scripts` | `[build]` | npm scripts，依序執行 |
| `commands` | 空 | 內建步驟後的自訂命令 |

預設順序：

```text
gofmt → vet → go build → go test → npm scripts → custom commands
```

第一個失敗即停止。`gofmt -l` 有輸出即失敗；缺少 race 所需 C toolchain 時明示降級
為 `-count 2`。

### `dev`

| 鍵 | 預設 | 說明 |
|---|---|---|
| `port` | 無 | 服務埠與 `-open` 的預設埠 |
| `open` | `http://localhost:<port>` | `-open` URL |
| `processes` | 依 `type` | 開發行程 |

每個 process：

| 鍵 | 預設 | 說明 |
|---|---|---|
| `name` | `proc<N>` | 唯一輸出前綴 |
| `dir` | `.` | 工作目錄 |
| `run` | 必填 | 命令字串或 argv 清單 |
| `env` | 空 | `KEY=value` 清單 |
| `ports` | 空 | 啟動前檢查的監聽埠 |
| `ready` | 自動 | `-open` 等待的埠 |

未設定 processes 時：`go` 執行 `go run <go.package>`；`npm` 執行 `npm run dev`；
`go-npm` 同時執行兩者。

`-open` 的就緒埠優先序：

1. process `ready`
2. `dev.open` 的 loopback 埠
3. `dev.port`
4. 第一個 process port

`dev.port` 與 `dev.open` 同時存在時埠必須一致。loopback 探測同時支援 IPv4 與 IPv6。

### `clean`

| 鍵 | 預設 | 說明 |
|---|---|---|
| `extra` | 空 | 額外刪除的倉庫內路徑 |

預設刪除 `output` 與 npm 產物；`-deps` 加入 `node_modules`，`-caches` 加入 Go cache，
`-all` 全部刪除。

## 3. 固定建置規則

Go 建置固定套用，無對應設定鍵：

```text
CGO_ENABLED=0  -mod=readonly  -trimpath  -ldflags "-s -w"
```

`go.ldflags` 只能追加：

```yaml
go:
  ldflags: -X main.channel=beta
```

## 4. 產物形狀

```text
go，無 include:
  dist/my-service-linux-amd64

go + include 或 go-npm:
  dist/my-app-linux-amd64/
    my-app
    web/
    <include...>

npm:
  dist/<static files>
```

Windows 執行檔加 `.exe`。archive 命名為
`<name>-<version>[-<os>-<arch>].<ext>`；目錄產物在壓縮檔中保留頂層目錄名。

### 4.1 發佈描述子

每個產物旁邊多一份 `<產物名>.release.json`，形狀由發佈標準的
`hoshi.release/v1` 定義：服務、`type`、版本、完整 commit、`dirty`、建立時間、
目標平臺、完整 artifact sha256 與 build provenance。

```text
dist/
  my-service-linux-amd64
  my-service-linux-amd64.release.json
```

- 目錄產物的描述子在目錄**旁邊**，不在裡面——它記的就是那個目錄的雜湊。
- 不進 archive，理由同上。
- `type: npm` 不輸出：產物就是輸出目錄本身，沒有旁邊可放。
- 設定雜湊、節點與 slot 由部署工具在綁定時補進 `deployment` 段，建置端不寫。
- 認不出 commit 時留空，不猜；問不出工作樹狀態時記為 `dirty`。

## 5. 命令

```yaml
run: go run ./cmd/server
run: [go, run, ./cmd/server]
```

字串支援引號但不經 shell，不展開 wildcard、pipe 或 `&&`。需要 shell 時明示：

```yaml
run: [sh, -c, "a && b"]
```

自訂測試：

```yaml
test:
  commands:
    - name: 資料庫測試
      dir: backend
      run: go test -tags db ./...
      env: [TEST_MYSQL_URL=mysql://localhost/test]
```

## 6. 版本

`version` 留空時執行 `git describe --tags --always --dirty`；無 git 資訊時使用 `dev`。
版本寫入 archive 名稱、`go.version_var` 與描述子的 `version`；不安全字元替換為 `-`。

描述子的 `commit` 另外取自 `git rev-parse HEAD`，是完整 40 位——短 commit 會碰撞，
而發佈是它唯一被用來判等的場合。

## 7. 格式與驗證

YAML 使用 `gopkg.in/yaml.v3`，JSON 使用 `encoding/json`。兩者都嚴格拒絕未知鍵、
第二份 YAML document、型別錯誤與非法值。

`hoshi check` 另驗證：

- `go.mod`、Go package、`package.json` 是否存在；
- `include` 與 process 工作目錄是否存在；
- `output` 是否被 `.gitignore` 排除。

## 8. 完整範例

```yaml
name: my-app
type: go-npm
output: dist/
targets: [linux/amd64, windows/amd64]
include: [story/]

go:
  dir: backend
  package: ./cmd/server

npm:
  dir: frontend

test:
  scripts: [typecheck, build]

dev:
  open: http://localhost:26603
  processes:
    - name: backend
      dir: backend
      run: go run ./cmd/server
      ports: [26600, 26601]
    - name: frontend
      dir: frontend
      run: npm run dev
      ports: [26603]

clean:
  extra: [backend/bin]
```
