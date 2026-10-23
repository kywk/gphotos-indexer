# 系統規格書 (SPEC.md) — gphotos-indexer

## 1. 系統目標與背景
自 2025 年 3 月 31 日起，Google 廢除了 photoslibrary.readonly 全域讀取權限。本專案旨在不佔用本地大量磁碟空間的前提下，建立一個本地縮圖與 Metadata 索引庫，解決 Google Photos 原生介面複合搜尋體驗不佳的問題。

## 2. 系統架構與資料流 (Data Pipeline)

    [Chrome Profile (已登入 Session)]
           │
           ▼ (chromedp 啟動並導向 photos.google.com)
    [時間軸自動捲動器 (Timeline Scroller)] + [網路封包攔截器 (CDP Network Listener)]
           │
           ├─► 1. 攔截 /_/PhotosUi/data/batchexecute 回應與 DOM a[href*="/photo/"]
           ├─► 2. 剝除 )]}' 防護前綴，遞迴解析嵌套 JSON Array 提取 MediaKey (AF1Qip...)、縮圖 BaseURL、Timestamp、寬高
           ├─► 3. (選配 -fetch-details) 模擬開啟資訊面板 (i 鍵) + ArrowRight，攔截 fDcn4b RPC 取得原始檔名、相機 EXIF 與 GPS
           ├─► 4. 透過 BaseURL + =w300-h300-c-rw 下載 ~10KB WebP 縮圖至 ./data/thumbs/<MediaKey>.webp
           ▼
    [SQLite 資料庫 (./data/index.db)]
           ├── media_items (主表，含 EXIF / GPS / Timestamp / MediaKey)
           ├── media_fts (FTS5 虛擬表，加速檔名、描述、地點、相機型號全文檢索)
           ├── albums (相簿主表，含相簿 ID、標題、封面縮圖、項目數、時間區間)
           └── album_items (相簿與相片多對多關聯表: album_id <-> media_id)
           │
           ▼
    [本地 HTTP Server (localhost:8080, 內嵌 index.html)]
           ├── GET /api/photos (支援 q, type, album_id, date_from, date_to 複合查詢)
           ├── GET /api/albums (回傳相簿清單與已索引統計)
           └── 毫秒級複合查詢 ──(點擊縮圖)──► 開啟 https://photos.google.com/photo/<MediaKey> 或 /album/<AlbumKey>

## 3. 資料庫結構設計 (SQLite Schema)

### 3.1 media_items 表
- id (TEXT PRIMARY KEY): Google Photos MediaKey (如 AF1QipM...)
- product_url (TEXT NOT NULL): https://photos.google.com/photo/<id>
- base_url (TEXT): 擷取當下的 lh3.googleusercontent.com 網址
- thumb_path (TEXT): 本地縮圖相對路徑 (thumbs/<id>.webp)
- filename (TEXT): 原始檔名 (如 IMG_20260125.JPG)
- description (TEXT): 相片說明或 AI 產生之標籤
- media_type (TEXT): photo 或 video
- taken_at (INTEGER): 拍攝時間 Unix Timestamp (秒)
- width (INTEGER): 原始寬度 (px)
- height (INTEGER): 原始高度 (px)
- duration (INTEGER): 影片長度 (秒，相片為 0)
- camera_make (TEXT): 相機製造商
- camera_model (TEXT): 相機型號
- focal_length (REAL): 焦距
- aperture (REAL): 光圈值
- iso (INTEGER): ISO 感光度
- shutter_speed (TEXT): 快門速度
- latitude (REAL): GPS 緯度
- longitude (REAL): GPS 經度
- location_name (TEXT): 解析出之地標名稱
- raw_metadata (TEXT): 原始擷取 JSON 備份
- created_at (INTEGER): 本地入庫時間
- updated_at (INTEGER): 本地最後更新時間

### 3.2 albums 表與 album_items 關聯表
- `albums` 表：
  - id (TEXT PRIMARY KEY): Google Photos Album MediaKey (如 AF1Qip...)
  - title (TEXT): 相簿標題
  - product_url (TEXT NOT NULL): https://photos.google.com/album/<id>
  - cover_base_url (TEXT): 相簿封面 lh3 / Fife 基礎網址
  - cover_thumb_path (TEXT): 本地封面縮圖相對路徑 (thumbs/<id>.webp)
  - item_count (INTEGER): 相簿項目總數
  - start_time (INTEGER): 相簿最早項目時間戳 (Unix 秒數)
  - end_time (INTEGER): 相簿最晚項目時間戳 (Unix 秒數)
  - raw_metadata (TEXT): 原始擷取 JSON 備份
  - created_at (INTEGER): 本地入庫時間
  - updated_at (INTEGER): 本地最後更新時間
- `album_items` 表：
  - album_id (TEXT NOT NULL): 關聯 `albums.id`
  - media_id (TEXT NOT NULL): 關聯 `media_items.id`
  - added_at (INTEGER NOT NULL): 本地建立關聯時間戳
  - PRIMARY KEY (album_id, media_id)
*(詳細資料庫規格、FTS5 檢索、相簿資料抽取與 AI Agent 回寫指南請參閱 [DATABASE.md](DATABASE.md))*

### 3.3 兩階段非破壞性 Upsert 規格 (Two-Phase Non-Destructive Upsert)
由於 Google Photos 的「時間軸/相簿列表封包」與「單張詳情/相簿內頁封包」是分開載入的：
- 第一階段（列表）：`media_items` 僅含 ID、BaseURL、ThumbPath、TakenAt、Width、Height；`albums` 可能僅含 ID、封面、初估項目數或標題。
- 第二階段（詳情、相簿內頁或 Takeout 匯入）：`media_items` 補齊 Filename、CameraModel、Latitude、Longitude、LocationName；`albums` 補齊完整 Title、起訖時間與統計。
因此 SQLite 的 ON CONFLICT(id) DO UPDATE 必須使用 CASE WHEN excluded.<col> != '' (或 > 0) 保留既有非空欄位，嚴禁直接以空值覆蓋舊資料。

## 4. Google Photos 網頁版逆向工程規格
1. 防劫持前綴 (XSSI Prefix)：所有 /_/PhotosUi/data/batchexecute 回應皆以 )]}' 開頭，解析器需先尋找第一個 [ 字元再進行 json.Unmarshal。
2. Envelope 結構：外層為 [["wrb.fr", "<RPC_ID>", "<ESCAPED_JSON_STRING>", null, null, null, "generic"], ...]，需取出索引 2 的字串進行二次 json.Unmarshal。
3. GPS 座標識別守則：遞迴掃描長度為 2 的浮點數陣列 [lat, lng] 時，必須驗證兩者皆具備小數點精度 (lat != float64(int(lat)))，避免將一般整數配對陣列（如 [10, 20]）誤判為經緯度。
4. 相簿與影片 Protobuf Extension Key：
   - `"76647426"`：影片專用擴充欄位，索引 0 為毫秒時長，索引 2、3 為影片寬高。
   - `"72930366"`：相簿專用擴充欄位，包含項目數量、相簿標題與 `[startTimeMs, endTimeMs]` 時間區間；`ParseBatchExecute` 遇到此欄位時自動排除避免將相簿誤判為單張相片。
5. 圖片伺服器參數 (lh3.googleusercontent.com)：
   - 縮圖參數：=w300-h300-c-rw（300x300 置中裁切，強制輸出 WebP）。
   - 預覽大圖：=w1200-rw。

## 5. 增量同步與風控保護 (Rate Limiting & Watermark)
- Watermark 停損機制：
  - 主時間軸 (`sync`)：若連續遇到 30 張已存在於本地 SQLite 且已有縮圖的 MediaKey，自動停止捲動。
  - 相簿內頁 (`albums`)：僅當相片已有縮圖 **且** 已存在於該相簿的 `album_items` 關聯中時才計入 Watermark，確保先前已被 `sync` 下載過縮圖的相片在首次執行 `albums` 時仍能完整寫入 `album_items` 關聯而不重抓縮圖。
- Jitter 延遲：每次捲動與詳情切換皆加入隨機抖動延遲，避免觸發 Google 429 Too Many Requests。

## 6. 測試規格與不變量 (Test Suite & Invariants)
專案內建三大核心測試套件，任何程式碼變更皆須通過驗證：
1. internal/db/sqlite_test.go：
   - TestStore_UpsertPreservesNonEmptyFields：驗證兩階段寫入互不洗掉既有欄位。
   - TestStore_SearchFilters：驗證 FTS5 unicode61 分詞器對中文地名、英文檔名、日期區間與媒體類型的複合過濾。
   - TestStore_AlbumsAndAlbumItems：驗證相簿非破壞性合併、`album_items` 關聯、`SyncAlbumStats` 起訖時間自動推算，以及依 `AlbumID` 與相簿標題搜尋相片。
2. internal/scraper/parser_test.go：
   - TestParseBatchExecute：驗證多段長度前綴解析、BaseURL 參數剝除、.MOV/.MP4 影片類型推斷與 GPS 整數排除邏輯。
   - TestParseBatchExecute_InvalidInput：確保畸形封包不會引發 panic。
   - TestParseAlbumsBatchExecute：驗證相簿封包 (`72930366` 擴充與直接元組) 解析，並確認與一般相片節點互不誤判。
3. internal/server/server_test.go：
   - TestServer_APISearchAndIndex：使用 httptest 驗證 `/api/photos`（含 `album_id` 過濾）、`/api/albums` 與 `go:embed index.html` 渲染。
