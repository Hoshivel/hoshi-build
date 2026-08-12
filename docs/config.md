# `.hoshi-build` 設定參考

> 規範關鍵字（**必須**／**不得**／**應**／**不宜**／**可**）依 RFC 2119 使用。
> 各倉庫**必須**有這份設定檔，見組織的建置規範。

## 0. 檔名與尋找方式

`hoshi` 在倉庫根依序尋找，取第一個找到的：

1. `.hoshi-build.yaml`（慣例）
2. `.hoshi-build.yml`
3. `.hoshi-build.json`

從倉庫的任何子目錄執行都會往上找，和 git 一樣。
`-config <路徑>` 指定檔案，`-C <目錄>` 先切換目錄。

**一個倉庫應只放一份。** 三種副檔名同時存在時後兩份會被安靜忽略；
`check-build-config.py` 會抓出這件事。

## 1. 最短的設定

```yaml
name: my-service
type: go
output: dist/
```

**其餘所有鍵都有預設值，而且預設值是照著三種型別推出來的。**
多數倉庫的設定就只有上面這三行——`test` 與 `dev` 完全不必寫。

**只寫和預設不同的東西**：一份把預設值重寫一遍的設定檔沒有人會讀，
而且每一條重寫的預設值都是一個會和工具漂移的東西。

## 2. 鍵位總表

### 2.1 頂層

| 鍵 | 型別 | 預設 | 說明 |
|---|---|---|---|
| `name` | 字串 | **必填** | 產物名。只允許小寫英數與 `.`、`_`、`-`，須以英數開頭 |
| `type` | 字串 | **必填** | `go` / `go-npm` / `npm` |
| `output` | 字串 | `dist` | 產物根目錄，相對倉庫根。**必須**是相對路徑且不得指向倉庫之外 |
| `version` | 字串 | 見 §6 | 留空＝問 git |
| `targets` | 清單 | 本機平臺 | 交叉編譯目標，形如 `linux/amd64`。`type: npm` **不得**設定 |
| `archive` | 字串 | `none` | `none` / `zip` / `tar.gz` |
| `include` | 清單 | 空 | 額外隨附的檔案／目錄。`type: npm` **不得**設定 |

### 2.2 `go:` —— 建置後端

`type: npm` **不得**有這個區段：設定了讀不到的東西，等於在檔案裡寫一句假話。

| 鍵 | 預設 | 說明 |
|---|---|---|
| `dir` | `.` | `go.mod` 所在目錄 |
| `package` | `./cmd/<name>` | main 套件，相對 `dir` |
| `tags` | 空 | build tags，會變成 `-tags a,b` |
| `ldflags` | 空 | **追加**在標準的 `-s -w` 之後，見 §3 |
| `version_var` | 空 | 例 `main.version`；有值時以 `-X` 把版本注入產物 |

### 2.3 `npm:` —— 建置前端

`type: go` **不得**有這個區段。

| 鍵 | 預設 | 說明 |
|---|---|---|
| `dir` | `.` | `package.json` 所在目錄 |
| `script` | `build` | 建置時跑 `npm run <script>` |
| `output` | `dist` | 該腳本的產物目錄，相對 `dir` |
| `web_dir` | `web` | `go-npm` 的產物裡，前端靜態檔放在哪個目錄名 |
| `install` | `auto` | `auto`（`node_modules` 不存在才裝）／ `always` ／ `never` |

> **註**：安裝相依時，有 `package-lock.json` 就用 `npm ci`，沒有才用 `npm install`。
> `npm ci` 裝的是鎖檔裡那一版，而且 `package.json` 與鎖檔對不上時會失敗；
> `npm install` 可能安靜地解析到新版本，而**一個會改變自己輸入的建置不算建置**。
> 同一條理由讓 `go build` 一律帶 `-mod=readonly`（見 §3）。

### 2.4 `test:` —— `hoshi test` 跑什麼

| 鍵 | 預設 | 說明 |
|---|---|---|
| `lint` | `true` | 跑 `gofmt -l .` 與 `go vet ./...` |
| `race` | `true` | `go test -race` |
| `packages` | `./...` | `go test` 的範圍 |
| `flags` | 空 | 額外傳給 `go test` 的旗標 |
| `scripts` | `[build]` | 依序跑的 npm script（只有含 npm 的型別會用到） |
| `commands` | 空 | 內建步驟跑完之後的自訂步驟，見 §5 |

預設的流程是：

```
gofmt -l .  →  go vet ./...  →  go build ./...  →  go test -race ./...   （含 go 的型別）
npm run build                                                            （含 npm 的型別）
自訂 commands
```

**第一個失敗就停。** 繼續跑下去只會列出一串後果，而第一行才是原因。

> **註**：`lint` 與 `race` 的預設是 `true`，而且是那種「關掉之後沒有人會發現」的
> 設定——所以要關必須明寫。`gofmt -l` 的判準是**它有沒有列出檔案**，不是結束碼：
> 它列了檔案照樣 exit 0，看結束碼會在未格式化的樹上回報通過。
>
> `-race` 需要 cgo 與 C 編譯器。找不到時**不會假裝跑過**——會印出警告並退化成
> `-count 2`（重跑以逼出順序相依），因為一個從來沒檢查過的綠燈比一個看得見的
> 缺口更糟。

### 2.5 `dev:` —— `hoshi dev` 啟動什麼

| 鍵 | 預設 | 說明 |
|---|---|---|
| `port` | 無 | **服務監聽的埠**——`-open` 要打的那一個 |
| `open` | `http://localhost:<port>` | `-open` 要開的網址，需要路徑或別的主機時才寫 |
| `processes` | 依 `type` 推得 | 要一起跑的行程，見下 |

**要用 `hoshi dev -open`，多數倉庫只需要這兩行**：

```yaml
dev:
  port: 8095
```

`port` 同時做兩件事：它是 `-open` 的目標，也會餵給推得的那個行程當作埠占用
檢查的對象（**只有在推得的行程剛好一個時**——`go-npm` 推出前後端兩個，
哪一個持有那個埠是猜的，而猜錯會把占用回報指到錯的行程上）。

`processes` 的每一項：

| 鍵 | 預設 | 說明 |
|---|---|---|
| `name` | `proc<N>` | 輸出前綴用的名字，**不得**重複 |
| `dir` | `.` | 工作目錄 |
| `run` | **必填** | 命令，字串或清單，見 §5 |
| `env` | 空 | `KEY=value` |
| `ports` | 空 | 這個行程會監聽的埠；啟動前會檢查有沒有被占用 |
| `ready` | 見下 | `-open` 之前要等哪個埠就緒 |

#### `-open` 開什麼、等什麼

規則是一句話：**開 `dev.open`（或由 `dev.port` 推得的網址），
並且等那個網址自己的埠。**

| 就緒埠的來源 | 順序 |
|---|---|
| 某個行程明寫的 `ready` | 1（它字面上就是「等這個」） |
| 開啟網址自己的埠（`localhost` / `127.0.0.1` / `::1` 才算） | 2 |
| `dev.port` | 3 |
| 第一個宣告了 `ports` 的行程 | 4 |

`dev.port` 與 `dev.open` **同時設定但指向不同的埠會被擋下來**：那種寫法
必然有一個鍵不生效，而它不生效的時候沒有任何輸出會說。

開啟網址自己提供就緒埠時，檢查會沿用它的 loopback 主機：
`localhost` 依系統解析結果與可用的 address family 探測，明寫
`127.0.0.1` 或 `::1` 時則探測那個位址。就緒埠來自其他設定時使用
`localhost`。
這不會假設 `localhost` 一定是 IPv4；Windows 上的 Vite／Astro 可能只監聽
IPv6 loopback。啟動前的埠占用檢查同樣會檢查兩種 loopback。

> **註**：就緒埠原本取的是「第一個宣告了埠的行程」，與 `-open` 實際要開的
> 網址無關。SR 的 backend 宣告 `[8080, 8081]`、frontend 宣告 `[5173]`，
> 而 `open` 指向 5173——於是它**等 8080、開 5173**。實測 backend 起來後
> 瀏覽器就開了，而 vite 還要六秒才在聽，使用者拿到的是一個連不上的分頁。
> 平常看起來正常只是因為 vite 通常比 `go run`（含編譯）快，那是運氣。
> 綁在網址上是把這個可能性消掉，不是把順序調一調。

**沒設定 `dev.processes` 時的預設值**：

| `type` | 預設啟動 |
|---|---|
| `go` | `go run <go.package>`（在 `go.dir`） |
| `npm` | `npm run dev`（在 `npm.dir`） |
| `go-npm` | 兩個都跑 |

### 2.6 `clean:`

| 鍵 | 預設 | 說明 |
|---|---|---|
| `extra` | 空 | 除了 `output` 與 npm 產物之外，還要刪掉的路徑 |

`hoshi clean` 預設刪 `output` 與 npm 的產物目錄；`-deps` 加上 `node_modules`，
`-caches` 加上 Go 的建置／測試快取，`-all` 全部。

> **註**：預設**不刪** `node_modules`——重裝它是整條工具鏈裡最慢的一件事，
> 而 `clean` 不是人們想等的時候會打的指令。

## 3. 不能設定的東西

Go 的建置一定帶這幾樣，**沒有**對應的設定鍵：

```
CGO_ENABLED=0    -mod=readonly    -trimpath    -ldflags "-s -w"
```

前三個是部署標準的規定，也是產物形態那三條硬性規則之所以成立的原因。`go.ldflags` 是**追加**：

```yaml
go:
  ldflags: -X main.channel=beta
# → -ldflags "-s -w -X main.channel=beta"
```

> **註**：把逃生口和規則放進同一個鍵，等於沒有規則。倉庫**可以**加自己的連結器
> 旗標，但**不得**把標準的那組拿掉——漏掉 `CGO_ENABLED=0` 的產物在建置機上跑得
> 完全正常，要到換一臺機器才會發現它動態連結了 glibc。
>
> `-mod=readonly` 是 Go 的預設值，但環境裡的 `GOFLAGS=-mod=mod` 蓋得掉它，
> 之後一次建置就會順手改寫 `go.mod`。這不是假想的：導入本工具時就發生過，
> 某個服務釘的 SDK 版本被 build 往前挪了一版。

## 4. 產物形態

由**這個 target 是否只有一個檔**決定，不是由 `type` 決定。

```
type: go（無 include）
  dist/my-service-linux-amd64                     ← 一個檔

type: go（有 include）／type: go-npm
  dist/my-app-linux-amd64/                        ← 一個目錄
      my-app                                      ← 執行檔（目錄已帶平臺，檔名不重複）
      web/                                        ← npm.output 的內容（go-npm）
      story/                                      ← include 的內容

type: npm
  dist/                                           ← 就是那疊靜態檔
```

Windows 目標的執行檔會加 `.exe`。

`archive` 開啟時（或 `hoshi build -package`），目錄產物在壓縮檔內保留自己的名字
（解開不會把檔案灑進當前目錄），單一執行檔則直接放在壓縮檔根部。
壓縮檔命名為 `<name>-<version>[-<os>-<arch>].<副檔名>`。

## 5. 命令的寫法

`run` 接受兩種形式：

```yaml
run: go run ./cmd/server            # 字串：以空白切開，引號會被尊重
run: [go, run, ./cmd/server]        # 清單：一字不動
```

參數含空白時用清單形式，或在字串裡加引號：`run: go run . --db "sqlite://a b.db"`。

**這是切字串，不是 shell**：沒有萬用字元展開、沒有管線、沒有 `&&`。
需要 shell 的步驟自己講出來：

```yaml
run: [sh, -c, "a && b"]
```

> **註**：隱式的 shell 會在 Unix 上出現、在 Windows 上不出現，於是同一份設定在兩臺
> 機器上是兩個意思。要 shell 就明寫，這樣它在哪裡都是同一件事。

自訂測試步驟：

```yaml
test:
  commands:
    - name: 資料庫測試
      dir: backend
      run: go test -tags db ./...
      env: [TEST_MYSQL_URL=mysql://localhost/test]
```

## 6. 版本

`version` 留空時跑 `git describe --tags --always --dirty`；取不到（沒有 git、
沒有 commit、從壓縮檔展開的樹）就用 `dev`。

會被寫進：壓縮檔名、以及 `go.version_var` 指定的變數。
不安全的字元（斜線、空白）會被換成 `-`。

`--dirty` 後綴是刻意保留的：**從有未提交改動的樹建出來的產物無法從倉庫重現**，
而唯一還能發現這件事的時刻就是建置當下。

## 7. 檔案格式

YAML 走 `gopkg.in/yaml.v3`，JSON 走 `encoding/json`，兩者都是**完整**的解析器，
兩邊解出同一組結構。

**未知的鍵一律報錯**，包含區段內的。`outupt:` 若被當成「沒設定，用預設值」，
症狀是產物默默出現在別的地方，而使用者以為自己設定過了。
錯誤訊息會指出行號與**該寫的鍵名**（`不認得的鍵 go.packge`）。

同一個檔案裡有第二份 YAML 文件（第二個 `---`）也會報錯——被忽略的那一半
永遠不是你以為的那一半。

## 8. `hoshi check` 會抓什麼

不建置，只比對設定與倉庫實況：

- `go.dir` 底下有沒有 `go.mod`
- `go.package` 指的目錄存不存在
- `npm.dir` 底下有沒有 `package.json`
- `include` 列的東西存不存在
- `dev.processes[].dir` 存不存在
- `output` 有沒有進 `.gitignore`（部署標準 §2：產物不進版控）

設定檔本身的問題（未知鍵、型別錯、值不合法）在載入時就會報，且**一次報完全部**。

## 9. 完整範例

這是最複雜的一種形狀；多數倉庫都短得多。

```yaml
name: my-app
type: go-npm
output: dist/

targets:
  - linux/amd64
  - windows/amd64

include:
  - story/

go:
  dir: backend
  package: ./cmd/server

npm:
  dir: frontend

test:
  scripts: [typecheck, build]

dev:
  open: http://localhost:5173
  processes:
    - name: backend
      dir: backend
      run: go run ./cmd/server
      ports: [8080, 8081]
    - name: frontend
      dir: frontend
      run: npm run dev
      ports: [5173]

clean:
  extra:
    - backend/bin
```
