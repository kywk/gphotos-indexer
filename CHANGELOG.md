# Changelog

## [v0.1.0] - 2026-10-18
### Added
- 初始版本發布：支援 auth、sync、serve 三大核心子命令。
- 實作基於 chromedp 的 Google Photos 網頁版封包攔截器 (batchexecute) 與 DOM 輔助提取器。
- 自動透過 Google Image Server 參數 (=w300-h300-c-rw) 下載輕量 WebP 縮圖至本地快取。
- 實作零 CGO 的 SQLite (modernc.org/sqlite) 儲存層，內建 FTS5 (unicode61) 全文檢索與兩階段非破壞性 Upsert。
- 內嵌 (go:embed) Dark Mode 本地極速檢索 Web UI，支援關鍵字、日期範圍、媒體類型過濾與一鍵跳轉 Google Photos 原圖。
- 建立完整的離線單元測試套件 (sqlite_test.go, parser_test.go, server_test.go) 與 Makefile 自動化驗證流程 (make check)。
