<!-- hoshivel:agent-rules v1 -> https://github.com/Hoshivel/workspace -->

# AGENTS.md — hoshi-build

> **代理執行規範的正本不在這裡**，在
> [workspace](https://github.com/Hoshivel/workspace) 的 `AGENTS.md`：
> 會話日誌、中斷復原流程、跨倉庫協作流程、分支與 PR 規則全在那裡。
> 本檔只補上**這個倉庫自己的**東西。

## 0. 開工前

**先取得 workspace，並讀它的 `AGENTS.md`。**

```sh
ls ../workspace/sessions/                                          # 本機：就在旁邊
git clone https://github.com/Hoshivel/workspace.git ../workspace   # 雲端：自己補上
```

- 取不到就**停下來告訴使用者**，不要退回在本倉庫自建 `sessions/`。
- **本倉庫沒有 `sessions/`，也不得新增。** 會話日誌的正本在 `workspace/sessions/`。
- 續接既有任務時**沿用日誌記的分支與 PR**，不要另開新分支
  （workspace `AGENTS.md` §4.3）。

## 1. 入場閱讀順序

1. `README.md` —— 本工具為什麼存在、有哪些子指令、三種型別、什麼不進這個倉庫。
2. **組織的部署標準** —— 產物形態的規範正文。**本工具是它的下游執法者**，
   產物形態那三條與建置指令是本倉庫大部分程式碼的理由。動建置旗標前必讀。
3. **組織的建置規範** —— 規定各倉庫必須有 `.hoshi-build.*` 與鍵位的規範性來源。
4. `docs/config.md` —— 設定檔的完整參考（build / test / dev / clean 四個面向）。

## 2. 驗證

改碼後執行；綠燈再把會話日誌的 `Editing` 設回 `idle`：

```sh
go build ./... && go vet ./... && gofmt -l . && go test -race ./...
go run ./cmd/hoshi build     # 自舉：本工具建置它自己
go run ./cmd/hoshi test      # 自舉：本工具測試它自己
GOOS=windows go build ./...  # dev 有平臺相依的檔案，兩邊都要編得過
```

`gofmt -l .` 須無輸出。自舉那兩步是最便宜的煙霧測試——`.hoshi-build.yaml`
是本倉庫自己的設定，它壞掉時最先發現的應該是本倉庫的維護者。

**`go test -race` 不是可選的**：`internal/dev` 每個子行程有兩個 goroutine
寫同一個 `ui.Printer`，這個競態就是 `-race` 抓出來的。

## 3. 這個倉庫的特殊規則

- **部署標準的旗標是內建的，不得變成設定項**。`CGO_ENABLED=0`、`-trimpath`、
  `-ldflags "-s -w"` 沒有對應的鍵，`go.ldflags` 只能**追加**。把逃生口和規則
  放進同一個鍵等於沒有規則——而漏掉 `CGO_ENABLED=0` 的產物在建置機上跑得完全
  正常，要到換一臺機器才會發現。由 `internal/build/gobuild_test.go` 釘住。
- **相依要有理由，但不必為零**。本專案以二進位檔提供，其他專案不引入原始碼，
  所以**可以**用程式庫——目前只有 `gopkg.in/yaml.v3`。要再加一個，先說得出
  它解決了什麼自己寫會做錯的事。
- **未知的鍵一律報錯**，包含區段內的（`KnownFields(true)` ＋
  `DisallowUnknownFields`）。打錯字的鍵若被當成「沒設定，用預設值」，
  症狀是產物默默出現在別的地方，而使用者以為自己設定過了。
- **錯誤訊息要講得出使用者該寫什麼**。`internal/config/load.go` 把 yaml.v3 的
  英文訊息翻回本工具的用語：「不認得的鍵 `go.packge`」才有用，
  「field packge not found in type config.GoConfig」沒有。改動結構時
  順手看一下 `sectionPrefix` 那張表還對不對。
- **預設值要照 `type` 推得**。`test` 與 `dev` 沒設定時必須自己推出合理的行為——
  多數倉庫的設定檔只有三行，就是靠這個。新增區段時比照辦理：
  **能推出來的就不要逼人寫**。
- **不收倉庫的業務知識**。本工具認得 `type`，不認得倉庫名。「某個倉庫要帶
  story/」這種事寫在那個倉庫自己的 `.hoshi-build.yaml`；本倉庫出現任何
  `if name == …` 就是走錯方向了。
- **`hoshi dev` 的子行程必須留在前景**。行程放進獨立的行程群組，Ctrl+C 收掉的是
  整棵樹——`npm run dev` 會再長出孫行程，只殺直接子行程會留下它占著埠。
  **不要**改回「開新視窗／背景程序 ＋ 下次啟動先殺掉占埠的行程」：
  那一整類問題正是因為行程脫離了啟動它的指令才存在。
- **改動設定鍵位是跨倉庫的破壞性變更**。每個使用本工具的倉庫都有 `.hoshi-build.*`，
  改鍵名要全部一起改，順序寫進 workspace 的會話日誌。**規範先改**（組織的建置
  規範），再改本倉庫，最後才各倉庫。
- **平臺規範的位置**：**被 import 的**進共用 SDK，**被遵守的**進規範倉庫，
  **會過期的**（會話、目標、代理規範）進
  [workspace](https://github.com/Hoshivel/workspace)。本工具**不引用**共用 SDK：
  建置工具不該和被建置的服務共用執行期程式碼。
- 文件與註解沿用倉庫既有風格：**正體中文為主**（程式碼註解英文），
  規範關鍵字（必須／不得／應／不宜／可）依 RFC 2119 使用，理由寫進 `> **註**` 區塊。
  狀態關鍵字（`Editing` / `editing` / `idle`）保持原樣以利機器辨識。
