# AGENTS.md — Antigravity AI 協作與開發準則

本文件為 AI Coding Agents（如 Antigravity agy、Claude Code、Codex 等）在本專案進行開發、重構與擴充時的最高指導原則。

## 1. 技術棧與環境約束 (Tech Stack Constraints)
- 語言版本：Go 1.23+。
- 零 CGO 原則 (Zero CGO)：
  - SQLite 必須維持使用 modernc.org/sqlite（純 Go 實作），嚴禁引入 github.com/mattn/go-sqlite3，以確保在 macOS (Apple Silicon) 與跨平台編譯時不發生 CGO 連結問題。
  - 縮圖處理優先利用 Google Image Server 的 URL 參數 (=w300-h300-c-rw) 直接取得 WebP，避免在本地引入肥大的 C 語言影像庫。
- 單一執行檔部署 (Single Binary)：前端靜態資源（如 internal/server/index.html）必須透過標準庫 //go:embed 嵌入 Binary 中，不得要求使用者在執行期額外掛載前端目錄。

## 2. 模組職責邊界與防回歸鐵律 (Regression Guards)
1. cmd/gphotos-indexer/main.go：僅負責 CLI Flag 解析、Signal 處理 (SIGINT/SIGTERM) 與子命令路由，不包含業務邏輯。
2. internal/db (sqlite.go & sqlite_test.go)：
   - 嚴禁破壞 Upsert 函數中的 CASE WHEN 非破壞性合併邏輯（詳見 SPEC.md 3.2 節 與 DATABASE.md 第 3 節）。
   - 若日後程式異動修改了 DB Schema（新增、修改或刪除資料表欄位），必須同步更新：
     1. `internal/db/sqlite.go`（包含 Schema 定義、migrate 自動遷移、Upsert 欄位對應、Search 查詢與 Scan）
     2. `internal/db/sqlite_test.go`（單元測試斷言）
     3. 資料庫規格文件 **`DATABASE.md`** 與 **`SPEC.md`**（欄位規格定義、語意表格、兩階段非破壞性 SQL 範本），確保其他讀取本資料庫的 AI Agent 與分析工具隨時掌握正確結構。
3. internal/scraper (browser.go, parser.go & parser_test.go)：
   - parser.go 必須維持純函數 (Pure Functions) 設計，不得在 parser.go 中引入網路或檔案 I/O。
   - 調整任何正規表示式或 JSON 樹狀結構走訪邏輯時，必須在 parser_test.go 新增對應的 Mock Payload 測試案例。
4. internal/server (server.go, index.html & server_test.go)：
   - 維持使用 Go 1.22+ 增強型 net/http.ServeMux 語法 (如 GET /api/photos)。
   - 新增 API Endpoint 或修改 Query 參數時，必須在 server_test.go 新增 httptest 斷言。

## 3. 強制驗證流程 (Mandatory Verification Workflow)
每次完成程式碼修改後，Agent 必須在終端機執行以下單一指令確認編譯、靜態分析與所有單元測試全數通過：
    make check

若 make check 失敗，必須優先修復至全綠 (PASS) 狀態後才可向使用者回報完成。

## 4. 下一階段待辦路線圖 (Next Roadmap for Agents)
- Phase 2 - Takeout ZIP 串流匯入器 (cmd/gphotos-indexer takeout)：實作 archive/zip 記憶體內串流解析 .supplemental-metadata.json，快速補齊數萬張歷史照片的完整檔名與精確 GPS，並撰寫對應的 zip 記憶體測試。
- Phase 3 - 詳情 RPC (fDcn4b) 批次補齊：在 internal/scraper 中提取頁面內的 WIZ_global_data (SNlM0e Action Token)，直接透過 fetch 批次補齊無檔名項目的 EXIF。
- Phase 4 - 本地 AI 視覺搜尋 (Apple Silicon / Ollama / MLX)：新增 analyze 子命令，讀取 ./data/thumbs/*.webp 呼叫本地 VLM 產生圖片描述或標籤寫入 description 欄位。
