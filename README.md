# gphotos-indexer (v0.2.0)

針對 Google Photos 2025 API 權限收緊後的本地端輕量檢索方案。透過 Headless Browser (chromedp) 掛載本地授權 Session，自動攔截 Google Photos 網頁版內部 batchexecute RPC 封包與 DOM，抓取原生 WebP 縮圖（w300-h300-c-rw，單張約 10KB）與相片 Metadata（ID、拍攝時間、解析度、檔名、EXIF、GPS），存入本地 SQLite (FTS5)，並提供零延遲的本地搜尋 Web 介面，點擊後直接 Deep Link 跳轉回 Google Photos 檢視原圖。

## 專案目錄結構

    gphotos-indexer/
    ├── AGENTS.md                        # Antigravity / AI Agent 開發行為準則與防回歸鐵律
    ├── SPEC.md                          # 系統規格書 (架構設計、RPC 解析、測試不變量)
    ├── DATABASE.md                      # 本地資料庫規格書 (專供 Agent 讀寫分析之 Schema 與指南)
    ├── README.md                        # 快速上手與 CLI 參數說明
    ├── CHANGELOG.md                     # 版本演進紀錄
    ├── Makefile                         # 編譯、測試 (make check) 與常用指令
    ├── .gitignore                       # Git 忽略規則 (保護本地 Chrome Profile 與 DB)
    ├── go.mod                           # Go Module 定義 (純 Go SQLite + chromedp)
    ├── cmd/
    │   └── gphotos-indexer/
    │       └── main.go                  # CLI 入口 (auth, sync, serve)
    └── internal/
        ├── db/
        │   ├── sqlite.go                # SQLite + FTS5 全文檢索與兩階段非破壞性 Upsert
        │   └── sqlite_test.go           # 資料庫欄位合併與複合過濾單元測試
        ├── scraper/
        │   ├── browser.go               # Chromedp 封包攔截、自動捲動與 WebP 縮圖下載
        │   ├── parser.go                # batchexecute 封包剝殼與遞迴特徵提取器
        │   └── parser_test.go           # 封包解析與邊界條件單元測試
        └── server/
            ├── index.html               # 透過 go:embed 內嵌的 Dark Mode 極速搜尋介面
            ├── server.go                # 本地 HTTP 與 REST API 伺服器
            └── server_test.go           # API 路由與 HTML Embed 整合測試

## 快速開始

### 1. 執行完整驗證與編譯
    make check

此指令會依序執行 go mod tidy、go vet、go test -v -race ./... 與 go build，並將執行檔輸出至 ./bin/gphotos-indexer。

### 2. 首次登入 Google 帳號 (建立專屬 Chrome Profile)
    make auth

執行後會跳出有頭模式 (Headful) 的 Chrome 視窗並開啟 https://photos.google.com 。請手動完成 Google 帳號與 2FA 登入，確認看到相片時間軸後，回到終端機按下 Enter 即可保存登入狀態至 ./data/chrome-profile。

### 3. 執行增量同步 (抓取縮圖與 Metadata)
    # 預設無頭模式抓取最新 200 張
    make sync

    # 進階：顯示瀏覽器畫面、抓取 500 張，並自動開啟右側資訊欄巡覽補齊原始檔名與相機 EXIF
    ./bin/gphotos-indexer sync -data ./data -headless=false -max-items=500 -fetch-details=true

### 4. 啟動本地極速搜尋 Web UI
    make serve

打開瀏覽器造訪 http://localhost:8080 即可毫秒級檢索照片，點擊照片直接跳轉回 Google Photos 雲端原圖。

## CLI 命令與參數一覽

### gphotos-indexer auth
- -data (預設 "./data")：資料與 Chrome Profile 儲存目錄。

### gphotos-indexer sync
- -data (預設 "./data")：資料與 SQLite 儲存目錄。
- -headless (預設 true)：是否以 Headless 模式背景執行。
- -max-items (預設 200)：單次同步最大抓取張數 (0 為不限制)。
- -force-full (預設 false)：忽略 Watermark 強制向下全庫掃描。
- -fetch-details (預設 false)：自動開啟資訊欄並模擬方向鍵巡覽，補齊原始檔名與相機型號。

### gphotos-indexer serve
- -data (預設 "./data")：資料與 SQLite 儲存目錄。
- -port (預設 8080)：本地 Web UI 監聽埠號。
