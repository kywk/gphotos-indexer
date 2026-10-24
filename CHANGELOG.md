# Changelog

## [v0.3.0] - 2026-10-24
### Added
- 新增 `doctor` 子命令：唯讀健檢，檢查重複相簿（同標題多列）、跨表髒列、空/錯誤標題、零索引相簿、孤兒關聯、遺失縮圖與缺檔名/解析度統計。
- 新增 `rebuild` 子命令：離線修復，清除跨表髒列與零索引重複相簿、清空錯誤標題、刪除孤兒關聯並為全體相簿重算起訖時間與封面回填（不動相片本體）。
### Fixed
- 修復相簿內頁相片節點夾帶父相簿上下文（`arr[1]` 為 `[url, w, h]` 元組）被誤判為相簿的根因，中斷重跑不再產生大量同名重複相簿；錯誤頁文字永不視為標題。
- `albums` 改為兩階段保證：先建完所有相簿並補齊封面，再逐本收錄相片；已完成相簿自動跳過（斷點續跑冪等）；捲動結束後最終補掃 DOM 與腳本，縮圖/拍攝時間/解析度無需另跑 sync 即入庫（檔名/EXIF 仍需 `-fetch-details`）。
- 相簿封面改以連結自身 `<img>` 優先歸屬，並在封面 URL 變更時強制重抓，舊錯封面不再永久殘留。

## [v0.2.0] - 2026-10-18
### Added
- 新增 `init` 子命令：快速初始化獨立相片索引專案目錄，自動生成開箱即用的 `Makefile`、`.gitignore` 與引導文件。
- 支援相簿索引 (`albums`)：自動抽取 Google Photos 個人相簿清單與相簿內相片關聯 (`albums` / `album_items`)，並於 Web UI 新增相簿下拉篩選。
- 支援影片解析與播放資訊：自動抓取影片長度 (duration) 與解析度 (4K / 1080p / 720p)，並於介面顯示時長膠囊。
- 照片與影片詳情檢視視窗 (Detail Modal)：點擊卡片跳出 Dialog 檢視大縮圖、完整檔名、EXIF、相機資訊與 Google Photos 雲端連結。
- 支援一鍵複製連結與鍵盤導航：支援方向鍵（← 上一張、→ 下一張）與 Esc 關閉，提供複製 Deep Link 功能避免預設瀏覽器帳號衝突。
- 右側時間軸快速跳轉軌道 (Timeline Scrubber)：仿 Google Photos 互動體驗，向下捲動時自動呼叫年/月軌道，支援點擊滑順跳轉與滾動指示徽章。
- 優雅停機 (Graceful Shutdown)：`serve` 伺服器支援 SIGINT (Ctrl+C) 與 SIGTERM 信號平順關閉與釋放 Port。
- 本地資料庫規格書 (`DATABASE.md`) 與 AI 協作開發準則 (`AGENTS.md`)。
- 遵循 Impeccable 設計規範：重構前端介面為純粹 Darkroom 極簡風格，全面導入 SVG 矢量圖標，消除巢狀卡片與視覺反模式。

## [v0.1.0] - 2026-10-18
### Added
- 初始版本發布：支援 auth、sync、serve 三大核心子命令。
- 實作基於 chromedp 的 Google Photos 網頁版封包攔截器 (batchexecute) 與 DOM 輔助提取器。
- 自動透過 Google Image Server 參數 (=w300-h300-c-rw) 下載輕量 WebP 縮圖至本地快取。
- 實作零 CGO 的 SQLite (modernc.org/sqlite) 儲存層，內建 FTS5 (unicode61) 全文檢索與兩階段非破壞性 Upsert。
- 內嵌 (go:embed) Dark Mode 本地極速檢索 Web UI，支援關鍵字、日期範圍、媒體類型過濾與一鍵跳轉 Google Photos 原圖。
- 建立完整的離線單元測試套件 (sqlite_test.go, parser_test.go, server_test.go) 與 Makefile 自動化驗證流程 (make check)。
