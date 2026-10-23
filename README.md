# gphotos-indexer (v0.2.0)

針對 Google Photos 2025 API 權限收緊後的本地端輕量檢索方案。透過 Headless Browser (`chromedp`) 掛載本地授權 Session，自動攔截 Google Photos 網頁版內部 `batchexecute` RPC 封包與 DOM，抓取原生 WebP 縮圖（`w300-h300-c-rw`，單張約 10KB）、相片/影片 Metadata（ID、拍攝時間、解析度、影片時長、檔名、EXIF、GPS）與個人相簿結構（相簿標題、封面、項目關聯），存入本地零 CGO 的 SQLite (`FTS5`) 資料庫，並提供零延遲的本地搜尋 Web 介面，點擊後可檢視詳情或直接透過 Deep Link 跳轉回 Google Photos 檢視原圖。

## 核心特色

- **零雲端 API 依賴**：直接重用本地 Chrome Profile 登入態，突破 `photoslibrary.readonly` 權限廢除限制。
- **極致輕量縮圖快取**：利用 Google Image Server 參數 (`=w300-h300-c-rw`) 下載 300×300 WebP 縮圖，10,000 張照片僅佔約 100~200 MB。
- **完整時間軸與個人相簿索引**：
  - `sync`：自動滾動主時間軸，支援 Watermark 增量停損與 `-fetch-details` 自動巡覽補齊原始檔名與相機 EXIF。
  - `albums`：自動探索所有個人相簿 (`/albums`)，建立 `albums` 與 `album_items` 多對多關聯，與主時間軸共用縮圖不重複佔用空間。
- **兩階段非破壞性合併 (Two-Phase Non-Destructive Upsert)**：列表快速同步、詳情巡覽與 AI 視覺標籤回寫互不覆蓋既有非空欄位。
- **極速本地 Web UI (`go:embed` 單一執行檔)**：
  - 支援 FTS5 全文檢索（檔名、相機型號、地名、相簿名稱、AI 描述）、媒體類型（相片/影片）、個人相簿下拉篩選與日期區間複合查詢。
  - 內建相片/影片詳情檢視視窗 (Detail Modal)、方向鍵（`←` / `→`）快速切換、一鍵複製雲端連結與右側時間軸快速跳轉軌道 (Timeline Scrubber)。
- **零 CGO 純 Go 編譯**：採用 `modernc.org/sqlite`，在 macOS (Apple Silicon) 與跨平台環境下皆可單一指令編譯為獨立執行檔。

## 專案目錄結構

```text
gphotos-indexer/
├── AGENTS.md                        # Antigravity / AI Agent 開發行為準則與防回歸鐵律
├── SPEC.md                          # 系統規格書 (架構設計、RPC 解析、測試不變量)
├── DATABASE.md                      # 本地資料庫規格書 (專供 Agent 讀寫、相簿抽取與 AI 回寫之指南)
├── README.md                        # 快速上手與 CLI 參數說明
├── CHANGELOG.md                     # 版本演進紀錄
├── Makefile                         # 編譯、測試 (make check) 與常用指令
├── .gitignore                       # Git 忽略規則 (保護本地 Chrome Profile 與 DB)
├── go.mod                           # Go Module 定義 (純 Go SQLite + chromedp)
├── cmd/
│   └── gphotos-indexer/
│       └── main.go                  # CLI 入口 (auth, sync, albums, serve, version)
└── internal/
    ├── db/
    │   ├── sqlite.go                # SQLite + FTS5 全文檢索、相簿關聯與兩階段非破壞性 Upsert
    │   └── sqlite_test.go           # 資料庫欄位合併、相簿關聯與複合過濾單元測試
    ├── scraper/
    │   ├── browser.go               # Chromedp 封包攔截、時間軸/相簿自動捲動與 WebP 縮圖下載
    │   ├── parser.go                # batchexecute 封包剝殼與相片/影片/相簿遞迴特徵提取器
    │   └── parser_test.go           # 封包解析與邊界條件單元測試
    └── server/
        ├── index.html               # 透過 go:embed 內嵌的 Darkroom 極速搜尋介面
        ├── server.go                # 本地 HTTP 與 REST API 伺服器 (/api/photos, /api/albums)
        └── server_test.go           # API 路由與 HTML Embed 整合測試
```

## 快速開始

### 1. 執行完整驗證與編譯

```bash
make check
```

此指令會依序執行 `go mod tidy`、`go vet ./...`、`go test -v -race ./...` 與 `go build`，並將執行檔輸出至 `./bin/gphotos-indexer`。

### 2. 首次登入 Google 帳號 (建立專屬 Chrome Profile)

```bash
make auth
```

執行後會跳出有頭模式 (Headful) 的 Chrome 視窗並開啟 `https://photos.google.com`。請手動完成 Google 帳號與 2FA 登入，確認看到相片時間軸後，回到終端機按下 `Enter` 即可保存登入狀態至 `./data/chrome-profile`。

### 3. 執行主時間軸增量同步 (抓取縮圖與 Metadata)

```bash
# 預設無頭模式抓取最新 200 張
make sync

# 進階：顯示瀏覽器畫面、抓取 500 張，並自動開啟右側資訊欄巡覽補齊原始檔名與相機 EXIF
./bin/gphotos-indexer sync -data ./data -headless=false -max-items=500 -fetch-details=true
```

### 4. 執行個人相簿索引同步 (建立相簿清單與相片關聯)

```bash
# 預設無頭模式掃描所有個人相簿與相簿內相片
make albums

# 進階：顯示瀏覽器畫面、限制掃描前 10 本相簿，並同時開啟資訊欄補齊 EXIF
./bin/gphotos-indexer albums -data ./data -headless=false -max-albums=10 -fetch-details=true
```

### 5. 啟動本地極速搜尋 Web UI

```bash
make serve
```

打開瀏覽器造訪 `http://localhost:8080` 即可毫秒級檢索相片與個人相簿：
- 點擊縮圖可開啟詳情視窗（檢視 1200px 預覽圖、解析度、拍攝時間、EXIF 與複製連結）。
- 支援鍵盤 `←` / `→` 切換上下張、`Esc` 關閉視窗，或點擊「在 Google Photos 開啟」直接跳轉雲端原圖。

## CLI 命令與參數一覽

### `gphotos-indexer auth`
啟動有頭 Chrome 視窗完成 Google 帳號登入並保存 Session。
- `-data` (預設 `"./data"`)：資料與 Chrome Profile 儲存目錄。

### `gphotos-indexer sync`
攔截主時間軸 `batchexecute` 與 DOM，自動抓取 WebP 縮圖與 Metadata 存入 SQLite。
- `-data` (預設 `"./data"`)：資料與 SQLite 儲存目錄。
- `-headless` (預設 `true`)：是否以 Headless 模式背景執行。
- `-max-items` (預設 `200`)：單次同步最大抓取張數 (`0` 為不限制)。
- `-force-full` (預設 `false`)：忽略 Watermark（連續 30 張已索引停損機制），強制向下全庫掃描。
- `-fetch-details` (預設 `false`)：自動開啟資訊欄並模擬方向鍵巡覽，攔截 `fDcn4b` RPC 補齊原始檔名、相機型號與 GPS。

### `gphotos-indexer albums`
針對所有個人相簿建立索引（包含相簿清單、封面縮圖與相簿內相片多對多關聯）。
- `-data` (預設 `"./data"`)：資料與 SQLite 儲存目錄。
- `-headless` (預設 `true`)：是否以 Headless 模式背景執行。
- `-max-albums` (預設 `0`)：單次最大抓取相簿數 (`0` 為不限制)。
- `-max-items` (預設 `0`)：單本相簿內最大抓取張數 (`0` 為不限制)。
- `-force-full` (預設 `false`)：忽略相簿內 Watermark，強制對每本相簿全量掃描。
- `-fetch-details` (預設 `false`)：在相簿內自動開啟資訊欄巡覽以補齊原始檔名與相機 EXIF。

### `gphotos-indexer serve`
啟動本地極速搜尋 Web UI 與 REST API 伺服器（支援 `SIGINT` / `SIGTERM` 優雅停機）。
- `-data` (預設 `"./data"`)：資料與 SQLite 儲存目錄。
- `-port` (預設 `8080`)：本地 Web UI 監聽埠號。

### `gphotos-indexer version`
顯示目前執行檔版本資訊（亦支援 `-v` 或 `--version`）。

## 本地 HTTP API 端點

啟動 `serve` 後，除了 Web 介面外，亦可直接呼叫本地 JSON API：

- `GET /api/photos`：複合條件檢索相片與影片
  - `q`：全文檢索關鍵字（比對檔名、描述、相機品牌/型號、地點名稱或所屬相簿標題）
  - `type`：媒體類型過濾（`photo` 或 `video`）
  - `album_id`：限定特定相簿 ID 內的項目
  - `date_from` / `date_to`：拍攝日期區間（格式 `YYYY-MM-DD`）
  - `limit` / `offset`：分頁筆數與偏移量（預設 `limit=200`）
- `GET /api/albums`：取得所有已索引之個人相簿清單、封面路徑、起訖時間與實際已索引張數 (`indexed_count`)
- `GET /thumbs/<MediaKey>.webp`：讀取本地快取之 WebP 縮圖

## 進階文件與 AI Agent 整合

- **[系統規格書 (SPEC.md)](SPEC.md)**：系統架構資料流、Google Photos `batchexecute` 逆向工程規格與測試不變量。
- **[本地資料庫規格書 (DATABASE.md)](DATABASE.md)**：完整 SQLite Schema（`media_items`、`media_fts`、`albums`、`album_items`）、兩階段非破壞性 SQL 範本、相簿匯出查詢與本地 AI 視覺模型（Ollama / MLX VLM）標籤回寫指南。
- **[AI 協作與開發準則 (AGENTS.md)](AGENTS.md)**：模組職責邊界、防回歸鐵律與後續開發路線圖。
