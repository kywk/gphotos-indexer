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
           └── media_fts (FTS5 虛擬表，加速檔名、描述、地點、相機型號全文檢索)
           │
           ▼
    [本地 HTTP Server (localhost:8080, 內嵌 index.html)]
           └── 毫秒級複合查詢 ──(點擊縮圖)──► 開啟 https://photos.google.com/photo/<MediaKey>

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
*(詳細資料庫規格、FTS5 檢索與 AI Agent 回寫指南請參閱 [DATABASE.md](DATABASE.md))*

### 3.2 兩階段非破壞性 Upsert 規格 (Two-Phase Non-Destructive Upsert)
由於 Google Photos 的「時間軸列表封包」與「單張詳情封包」是分開載入的：
- 第一階段（列表）：僅含 ID、BaseURL、ThumbPath、TakenAt、Width、Height。
- 第二階段（詳情或 Takeout 匯入）：含 ID、Filename、CameraModel、Latitude、Longitude、LocationName，但可能不含 ThumbPath 或 TakenAt。
因此 SQLite 的 ON CONFLICT(id) DO UPDATE 必須使用 CASE WHEN excluded.<col> != '' (或 > 0) 保留既有非空欄位，嚴禁直接以空值覆蓋舊資料。

## 4. Google Photos 網頁版逆向工程規格
1. 防劫持前綴 (XSSI Prefix)：所有 /_/PhotosUi/data/batchexecute 回應皆以 )]}' 開頭，解析器需先尋找第一個 [ 字元再進行 json.Unmarshal。
2. Envelope 結構：外層為 [["wrb.fr", "<RPC_ID>", "<ESCAPED_JSON_STRING>", null, null, null, "generic"], ...]，需取出索引 2 的字串進行二次 json.Unmarshal。
3. GPS 座標識別守則：遞迴掃描長度為 2 的浮點數陣列 [lat, lng] 時，必須驗證兩者皆具備小數點精度 (lat != float64(int(lat)))，避免將一般整數配對陣列（如 [10, 20]）誤判為經緯度。
4. 圖片伺服器參數 (lh3.googleusercontent.com)：
   - 縮圖參數：=w300-h300-c-rw（300x300 置中裁切，強制輸出 WebP）。
   - 預覽大圖：=w1200-rw。

## 5. 增量同步與風控保護 (Rate Limiting & Watermark)
- Watermark 停損機制：若連續遇到 30 張已存在於本地 SQLite 且已有縮圖的 MediaKey，視為已追上上次同步進度，自動停止捲動（可透過 -force-full 強制全掃）。
- Jitter 延遲：每次捲動與詳情切換皆加入隨機抖動延遲，避免觸發 Google 429 Too Many Requests。

## 6. 測試規格與不變量 (Test Suite & Invariants)
專案內建三大核心測試套件，任何程式碼變更皆須通過驗證：
1. internal/db/sqlite_test.go：
   - TestStore_UpsertPreservesNonEmptyFields：驗證兩階段寫入互不洗掉既有欄位。
   - TestStore_SearchFilters：驗證 FTS5 unicode61 分詞器對中文地名、英文檔名、日期區間與媒體類型的複合過濾。
2. internal/scraper/parser_test.go：
   - TestParseBatchExecute：驗證多段長度前綴解析、BaseURL 參數剝除、.MOV/.MP4 影片類型推斷與 GPS 整數排除邏輯。
   - TestParseBatchExecute_InvalidInput：確保畸形封包不會引發 panic。
3. internal/server/server_test.go：
   - TestServer_APISearchAndIndex：使用 httptest 驗證 /api/photos 日期邊界轉換與 go:embed index.html 渲染。
