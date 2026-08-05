# hoshi-build

Hoshivel 的開發工具。倉庫放一份 `.hoshi-build.yaml`，不必自己寫任何腳本。

二進位檔叫 **`hoshi`**：

```sh
hoshi build            # 建置，產物 → dist/<name>-<os>-<arch>
hoshi build -package   # 順便壓一包
hoshi build -target linux/amd64,windows/amd64
hoshi test             # gofmt → vet → build → test，前端跑設定的 scripts
hoshi dev              # 前後端一起跑，Ctrl+C 一次全停
hoshi dev -open        # 前端就緒後自動開瀏覽器
```

設定通常只有三行：

```yaml
name: my-service
type: go
output: dist/
```

`test` 與 `dev` 的行為由 `type` 推出來，所以多數倉庫**不必寫**這兩段。

## 為什麼要有這個倉庫

### 建置：規範只寫在文件裡

部署標準規定 Go 產物的建置指令是

```sh
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o <name> ./cmd/<name>
```

在本工具出現之前，這條規則只存在於文件裡，多數倉庫靠人記得。
而漏掉 `CGO_ENABLED=0` 的產物**在建置機器上跑得完全正常**——它動態連結了
建置機的 glibc，要到部署上另一臺機器才會發現。

### 其他指令：每個倉庫各寫一份，或根本沒有

少數有腳本的倉庫累積了上千行 PowerShell，外加一支**會自動下載 pwsh** 的
bootstrap 腳本——只為了跑 build / test / dev / fmt / clean。
其餘倉庫連這些都沒有：怎麼跑測試、怎麼起開發環境，散在各自的 README 裡。

**hoshi 把這兩件事收成一個執行檔和一份設定檔。**
那些旗標不是設定項，是內建的；沒有人需要記得，也沒有人能不小心漏掉。

## 指令

| 指令 | 做什麼 |
|---|---|
| `hoshi build` | 建置產物（預設子指令，可省略） |
| `hoshi test` | 驗證：gofmt / vet / build / test ＋ 前端 scripts ＋ 自訂步驟 |
| `hoshi dev` | 啟動開發環境 |
| `hoshi fmt` | `gofmt -w`（`-check` 只回報不改） |
| `hoshi clean` | 刪產物（`-deps` 連 node_modules、`-all` 全清） |
| `hoshi setup` | 安裝相依（`go mod download` / `npm ci`） |
| `hoshi check` | 只驗設定與倉庫佈局 |
| `hoshi init` | 依倉庫現況產生一份設定檔 |

`hoshi help` 有完整旗標；設定鍵位見 **[`docs/config.md`](docs/config.md)**。

## 三種型別

| `type` | 用在 | 產物 |
|---|---|---|
| `go` | 純後端服務 | `dist/<name>-<os>-<arch>` |
| `go-npm` | 後端 ＋ 隨附前端 | `dist/<name>-<os>-<arch>/`（執行檔 ＋ `web/`） |
| `npm` | 純前端 / 靜態站 | `dist/`（一疊靜態檔） |

**產物是一個檔還是一個目錄**，由「這個 target 是否只有一個檔」決定，不是由 `type`
決定：只有執行檔 → 一個檔；需要隨附內容（前端靜態檔、`include`）→ 一個同名目錄。
這是部署標準 §1.1〈可單檔部署〉的直接後果——沒有隨附內容就不該有目錄。

## 它替你守住的事

- **部署標準的旗標是內建的**。`CGO_ENABLED=0`、`-mod=readonly`、`-trimpath`、
  `-ldflags "-s -w"` 沒有對應的設定鍵；`go.ldflags` 只能**追加**。
- **建置完就驗靜態連結**：讀產物的 ELF 標頭確認沒有 `PT_INTERP`。違規會讓
  **當下這次建置失敗**，而不是等到某次 conformance 掃描。非 ELF 目標
  （windows、darwin）明講「沒檢查」，不假裝綠燈。
- **建置不會改動 `go.mod`**。`-mod=readonly` 明確傳入，環境裡的
  `GOFLAGS=-mod=mod` 蓋不掉它。
- **`gofmt -l` 看的是有沒有列出檔案**，不是結束碼——它列了檔案照樣 exit 0。
- **`-race` 跑不了就明講**，退化成 `-count 2` 並印警告，不假裝跑過。
- **設定檔打錯字會報錯**，並指出該寫的鍵名（`不認得的鍵 go.packge`）。

## `hoshi dev` 的形狀

所有行程都是 `hoshi dev` 的**子行程**，輸出加上前綴多工在同一個終端機：

```
backend  │ listening on :8080
frontend │ vite ready in 412 ms
backend  │ GET /api/me 200
```

- **Ctrl+C 一次全停。** 子行程放進獨立的行程群組，收掉的是整棵樹——
  `npm run dev` 會再長出孫行程，只殺直接子行程會留下它占著埠。
- **任何一個行程結束就整組停下。** 一個只剩一半的開發環境看起來還活著，
  但它服務不了一個請求。
- **啟動前檢查 `ports`**，被占用就直接拒絕，不會讓你去看某個 dev server
  深處丟出來的錯誤訊息。

> **註**：這取代了原本「各開一個終端機視窗（Windows）／背景程序寫日誌檔（Unix）」
> 的作法。那種作法之所以還需要「下次啟動先找出並殺掉占著埠的舊行程」，
> 正是因為行程脫離了啟動它的指令。子行程留在前景，那一整類問題就不存在了。

## 安裝

### 推薦：下載二進位

**`hoshi` 自己也遵守部署標準**——它是一個靜態連結的單檔執行檔，複製到機器上就能跑，
不需要 Go 工具鏈，也不需要這份原始碼。

```sh
# 平臺：linux-amd64 / linux-arm64 / darwin-amd64 / darwin-arm64 / windows-amd64.exe
curl -fsSLO https://github.com/Hoshivel/hoshi-build/releases/latest/download/hoshi-linux-amd64
sudo install -m 0755 hoshi-linux-amd64 /usr/local/bin/hoshi
hoshi version
```

每推一個 `v*` tag，CI 就建出上列各平臺的產物附到 release，見
[`.github/workflows/release.yml`](.github/workflows/release.yml)。檔名是
`hoshi-<os>-<arch>`——和 `hoshi` 為其他倉庫產出的名字**同一條規則**（建置規範 §2.2），
因為它就是同一段程式碼產的。

### 從原始碼建置

沒有合用的 release、或要驗證某個 commit 時：

```sh
git clone https://github.com/Hoshivel/hoshi-build.git
cd hoshi-build
go build -o hoshi ./cmd/hoshi
sudo install -m 0755 hoshi /usr/local/bin/
```

之後可以用它自己升級：`hoshi build && sudo install -m 0755 dist/hoshi-* /usr/local/bin/hoshi`。

> **註**：這一步是**自舉**，所以它是本倉庫唯一手打 `go build` 的地方——
> 還沒有 `hoshi` 的時候沒有別的辦法。它刻意不帶部署標準的那組旗標：那組旗標的正本
> 在 `internal/build/gobuild.go`，在這裡抄一份就是多一份會漂移的副本。
> 用這個方式裝出來的 `hoshi` 拿來跑 `hoshi build` 產出的，才是合規的產物。

## 什麼不進這個倉庫

- **不放任何倉庫的業務知識**。hoshi 認得的是 `type`，不是倉庫名。
  「某個倉庫要帶 `story/`」寫在那個倉庫自己的 `.hoshi-build.yaml`；
  本倉庫出現任何 `if name == …` 就是走錯方向了。
- **不放部署**。它做出產物就結束了；產物怎麼送到機器上、systemd unit 怎麼寫，
  是部署標準的事。
- **不引用平臺的執行期 SDK**。建置工具不該和被建置的服務共用執行期程式碼。
- **相依要有理由**。本專案以二進位檔提供，其他專案不引入原始碼，所以可以用程式庫
  ——但每加一個都要說得出為什麼。目前只有一個：`gopkg.in/yaml.v3`。

> **註**：本倉庫原本堅持零相依並自寫了一個 YAML 子集解析器。設定檔長出
> `test` 與 `dev` 兩個區段之後那個取捨反過來了：需要的語法變多，而自寫解析器
> 拒絕的東西（序列裡的對映）正好是新的設定需要的。使用者確認可以用程式庫之後
> 換成 yaml.v3，嚴格度靠 `KnownFields(true)` 維持不變。

> **註**：以後若有 `hoshi-cli`，本工具可以直接併入其中——`hoshi build` 這些
> 子指令的形狀就是照那個方向取的。

## 開發

```sh
go build ./... && go vet ./... && gofmt -l . && go test -race ./...
go run ./cmd/hoshi build       # 自舉：用它建置它自己
go run ./cmd/hoshi test        # 自舉：用它測試它自己
```
