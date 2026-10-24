# 本地資料庫規格書 (DATABASE.md)

本文件專為 AI Coding Agents、資料分析腳本與外部整合工具設計，詳細定義 `gphotos-indexer` 本地 SQLite 資料庫的檔案結構、欄位語意、全文檢索（FTS5）機制、兩階段更新協定，以及 AI 視覺標籤回寫規範。

---

## 1. 資料庫儲存概觀 (Storage Overview)

- **預設檔案路徑**：`./data/index.db`（可透過 CLI 參數 `-data <目錄>` 自訂）
- **本地縮圖目錄**：`./data/thumbs/<id>.webp`（300×300 WebP 格式，單張約 10~25 KB）
- **SQLite 引擎要求**：SQLite 3.35+（支援 `ON CONFLICT DO UPDATE` 與 `fts5` 模組）
- **連接 Pragma 推薦設定**：
  ```sql
  PRAGMA journal_mode = WAL;        -- 支援多行程高併發讀取，不阻塞爬蟲寫入
  PRAGMA busy_timeout = 5000;       -- 遭遇鎖定時自動等待 5 秒
  PRAGMA foreign_keys = ON;
  PRAGMA synchronous = NORMAL;
  ```

---

## 2. 資料表綱要 (Schema Specification)

資料庫內建四張核心資料表：
1. `media_items`：實體主表，儲存所有相片/影片的屬性、EXIF、拍攝時間與檔案路徑。
2. `media_fts`：FTS5 虛擬全文檢索表，加速檔名、相機、地名與 AI 描述搜尋。
3. `albums`：相簿主表，儲存個人相簿的標題、封面縮圖、項目數量與起訖時間區間。
4. `album_items`：相簿與相片的多對多關聯表（`album_id` ↔ `media_id`）。

### 2.1 `media_items` 主資料表

```sql
CREATE TABLE IF NOT EXISTS media_items (
    id           TEXT PRIMARY KEY,              -- Google Photos MediaKey (如 AF1QipM...)
    product_url  TEXT NOT NULL,                 -- Google Photos 雲端檢視網址
    base_url     TEXT DEFAULT '',               -- Google Fife / Content CDN 基礎網址
    thumb_path   TEXT DEFAULT '',               -- 本地縮圖相對路徑 (thumbs/<id>.webp)
    filename     TEXT DEFAULT '',               -- 原始上傳檔名 (如 IMG_20261018.JPG, video.mp4)
    description  TEXT DEFAULT '',               -- 使用者說明或 AI 視覺標籤 / 摘要
    media_type   TEXT DEFAULT 'photo',          -- 媒體類型: 'photo' 或 'video'
    taken_at     INTEGER DEFAULT 0,             -- 拍攝時間 (Unix Timestamp 秒數, UTC)
    width        INTEGER DEFAULT 0,             -- 原始寬度 (像素 px)
    height       INTEGER DEFAULT 0,             -- 原始高度 (像素 px)
    duration     INTEGER DEFAULT 0,             -- 影片長度 (秒數，相片為 0)
    camera_make  TEXT DEFAULT '',               -- 相機 / 設備製造商 (如 Apple, Sony, DJI)
    camera_model TEXT DEFAULT '',               -- 相機 / 設備型號 (如 iPhone 16 Pro, A7C2)
    focal_length REAL DEFAULT 0,                -- 焦距 (mm)
    aperture     REAL DEFAULT 0,                -- 光圈值 (f-number)
    iso          INTEGER DEFAULT 0,             -- ISO 感光度
    shutter_speed TEXT DEFAULT '',              -- 快門速度 (如 "1/120s")
    latitude     REAL DEFAULT 0,                -- GPS 緯度 (WGS84，浮點數)
    longitude    REAL DEFAULT 0,                -- GPS 經度 (WGS84，浮點數)
    location_name TEXT DEFAULT '',              -- 解析出之地名 / 地標 (如 "喀納斯湖")
    raw_metadata TEXT DEFAULT '',               -- Google Photos 原始 JSON 陣列備份
    created_at   INTEGER NOT NULL,              -- 本地首次索引時間 (Unix Timestamp 秒數)
    updated_at   INTEGER NOT NULL               -- 本地最後更新時間 (Unix Timestamp 秒數)
);

CREATE INDEX IF NOT EXISTS idx_media_taken_at ON media_items(taken_at DESC);
CREATE INDEX IF NOT EXISTS idx_media_type ON media_items(media_type);
```

#### 欄位語意與注意事項

| 欄位名 | 類型 | 範例值 | 備註與 Agent 處理指南 |
| :--- | :--- | :--- | :--- |
| `id` | `TEXT` | `AF1QipNlSMeBs...` | Google 內部不變唯一鍵，長度約 40~60 字元。 |
| `product_url` | `TEXT` | `https://photos.google.com/photo/AF1Qip...` | 直接跳轉該項目的官方雲端網址。 |
| `thumb_path` | `TEXT` | `thumbs/AF1Qip...webp` | 本地相對路徑。絕對路徑為 `filepath.Join(dataDir, thumb_path)`。 |
| `filename` | `TEXT` | `2026-10-15 [RYeX].mkv` | 若為空字串 `""`，代表僅經由時間軸同步，尚未執行 `-fetch-details` 或 Takeout 補齊。 |
| `description` | `TEXT` | `貓咪在陽台曬太陽, 橘貓, 逆光` | **專供 AI Agent 回寫欄位**。後續 VLM 分析標籤請直接寫入此欄位。 |
| `media_type` | `TEXT` | `'photo'` / `'video'` | 嚴格限定為 `'photo'` 或 `'video'`。 |
| `taken_at` | `INTEGER` | `1792019483` | Unix 秒數。格式化為本地時間：`datetime(taken_at, 'unixepoch', 'localtime')`。 |
| `width` / `height` | `INTEGER` | `3840` / `2160` | 原始畫素解析度。可用於計算百萬畫素 `(width * height / 1e6)` 或判定 4K/1080p。 |
| `duration` | `INTEGER` | `217` | 影片長度（秒）。相片固定為 `0`。格式化範例：`217` 秒 -> `03:37`。 |
| `latitude` / `longitude` | `REAL` | `25.0777`, `121.3894` | 均為 `0` 代表無 GPS。只有兩者均不為 0 且具浮點小數才視為有效定位。 |
| `raw_metadata` | `TEXT` | `["AF1Qip...", [...]]` | 保留從 `batchexecute` 攔截到的原始巢狀陣列。若需挖掘未解析私有屬性可從此處 Unmarshal。 |

---

### 2.2 `media_fts` 全文檢索虛擬表 (FTS5)

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS media_fts USING fts5(
    id UNINDEXED,            -- 與 media_items.id 關聯，不進行分詞
    filename,                -- 檔名搜尋 (支援模糊分詞)
    description,             -- 相片描述與 AI 標籤搜尋
    camera_make,             -- 製造商搜尋 (如 Apple, Sony)
    camera_model,            -- 型號搜尋 (如 iPhone, Pocket 3)
    location_name,           -- 地名搜尋 (如 禾木村, 東京)
    tokenize = 'unicode61'   -- 支援中英數字混合分詞
);
```

---

### 2.3 `albums` 相簿主資料表

```sql
CREATE TABLE IF NOT EXISTS albums (
    id               TEXT PRIMARY KEY,          -- Google Photos Album MediaKey (如 AF1Qip...)
    title            TEXT DEFAULT '',           -- 相簿標題 (如 "2026 北海道冬季旅遊")
    product_url      TEXT NOT NULL,             -- 相簿雲端網址 (https://photos.google.com/album/<id>)
    cover_base_url   TEXT DEFAULT '',           -- 封面圖片 Google CDN 基礎網址
    cover_thumb_path TEXT DEFAULT '',           -- 本地封面縮圖相對路徑 (thumbs/<id>.webp)
    item_count       INTEGER DEFAULT 0,         -- 相簿總項目數 (雲端標示或已索引數量取大者)
    start_time       INTEGER DEFAULT 0,         -- 相簿內最早拍攝時間 (Unix Timestamp 秒數, UTC)
    end_time         INTEGER DEFAULT 0,         -- 相簿內最晚拍攝時間 (Unix Timestamp 秒數, UTC)
    raw_metadata     TEXT DEFAULT '',           -- Google Photos 原始相簿 JSON 陣列備份
    created_at       INTEGER NOT NULL,          -- 本地首次索引時間 (Unix Timestamp 秒數)
    updated_at       INTEGER NOT NULL           -- 本地最後更新時間 (Unix Timestamp 秒數)
);

CREATE INDEX IF NOT EXISTS idx_albums_start_time ON albums(start_time DESC);
CREATE INDEX IF NOT EXISTS idx_albums_title ON albums(title);
```

#### `albums` 欄位語意與注意事項

| 欄位名 | 類型 | 範例值 | 備註與 Agent 處理指南 |
| :--- | :--- | :--- | :--- |
| `id` | `TEXT` | `AF1QipAlbumKey...` | Google Photos 相簿唯一鍵。**相片 ID 絕不可寫入此表**：`SyncAlbums` 啟動與 `migrate` 開庫時會自動執行 `DELETE FROM albums WHERE id IN (SELECT id FROM media_items)` 清除誤判髒資料，確保中斷重跑具冪等性。 |
| `title` | `TEXT` | `2026 日本東京滑雪行` | 相簿名稱。錯誤頁文字（`404` / `Not Found` / `Error` 前綴）會被解析器與 `processAlbum` 直接丟棄，避免污染。 |
| `product_url` | `TEXT` | `https://photos.google.com/album/AF1Qip...` | 雲端相簿直接開啟連結。 |
| `cover_thumb_path` | `TEXT` | `thumbs/AF1Qip...webp` | 本地封面縮圖路徑。若相簿列表未抓到封面，`SyncAlbumStats` 會自動回填相簿內最新相片之縮圖。 |
| `item_count` | `INTEGER` | `48` | 相簿包含的相片/影片總數。 |
| `start_time` / `end_time` | `INTEGER` | `1769322730` / `1769904000` | 相簿涵蓋的拍攝時間區間（Unix 秒數）。若列表封包未提供，會在索引完相簿內相片後由 `MIN(taken_at)` / `MAX(taken_at)` 自動推算回填。 |

---

### 2.4 `album_items` 相簿與相片多對多關聯表

```sql
CREATE TABLE IF NOT EXISTS album_items (
    album_id TEXT NOT NULL,                     -- 對應 albums.id
    media_id TEXT NOT NULL,                     -- 對應 media_items.id
    added_at INTEGER NOT NULL,                  -- 本地建立關聯時間戳 (Unix Timestamp 秒數)
    PRIMARY KEY (album_id, media_id)
);

CREATE INDEX IF NOT EXISTS idx_album_items_album_id ON album_items(album_id);
CREATE INDEX IF NOT EXISTS idx_album_items_media_id ON album_items(media_id);
```

- 同一張相片（`media_id`）可同時存在於多本相簿（`album_id`），且與主時間軸共用同一筆 `media_items` 紀錄與 `./data/thumbs/<media_id>.webp` 縮圖檔，不重複佔用空間。

---

## 3. 兩階段非破壞性寫入規範 (Non-Destructive Protocol)

由於 Google Photos 資料由多個階段非同步提供：
1. **時間軸階段（Sync）**：提供 `id`, `base_url`, `thumb_path`, `taken_at`, `width`, `height`, `duration`，但**無檔名與 EXIF**。
2. **詳情階段（-fetch-details 或 Takeout）**：提供 `filename`, `camera_model`, `latitude`, `longitude`，但**可能無縮圖**。
3. **AI 分析階段（Agent VLM / MLX / Ollama）**：讀取 `thumb_path`，產出 `description` 與標籤，但**不應變動原有照片屬性**。

### 鐵律：嚴禁以空值（Empty String / 0）覆蓋既有欄位
若 Agent 要更新資料庫，必須遵守非破壞性合併語法：

```sql
INSERT INTO media_items (
    id, product_url, base_url, thumb_path, filename, description, media_type,
    taken_at, width, height, duration, camera_make, camera_model, focal_length, aperture,
    iso, shutter_speed, latitude, longitude, location_name, raw_metadata,
    created_at, updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT(id) DO UPDATE SET
    base_url      = CASE WHEN excluded.base_url != '' THEN excluded.base_url ELSE media_items.base_url END,
    thumb_path    = CASE WHEN excluded.thumb_path != '' THEN excluded.thumb_path ELSE media_items.thumb_path END,
    filename      = CASE WHEN excluded.filename != '' THEN excluded.filename ELSE media_items.filename END,
    description   = CASE WHEN excluded.description != '' THEN excluded.description ELSE media_items.description END,
    media_type    = CASE WHEN excluded.media_type != 'photo' THEN excluded.media_type ELSE media_items.media_type END,
    taken_at      = CASE WHEN excluded.taken_at > 0 THEN excluded.taken_at ELSE media_items.taken_at END,
    width         = CASE WHEN excluded.width > 0 THEN excluded.width ELSE media_items.width END,
    height        = CASE WHEN excluded.height > 0 THEN excluded.height ELSE media_items.height END,
    duration      = CASE WHEN excluded.duration > 0 THEN excluded.duration ELSE media_items.duration END,
    camera_make   = CASE WHEN excluded.camera_make != '' THEN excluded.camera_make ELSE media_items.camera_make END,
    camera_model  = CASE WHEN excluded.camera_model != '' THEN excluded.camera_model ELSE media_items.camera_model END,
    focal_length  = CASE WHEN excluded.focal_length > 0 THEN excluded.focal_length ELSE media_items.focal_length END,
    aperture      = CASE WHEN excluded.aperture > 0 THEN excluded.aperture ELSE media_items.aperture END,
    iso           = CASE WHEN excluded.iso > 0 THEN excluded.iso ELSE media_items.iso END,
    shutter_speed = CASE WHEN excluded.shutter_speed != '' THEN excluded.shutter_speed ELSE media_items.shutter_speed END,
    latitude      = CASE WHEN excluded.latitude != 0 THEN excluded.latitude ELSE media_items.latitude END,
    longitude     = CASE WHEN excluded.longitude != 0 THEN excluded.longitude ELSE media_items.longitude END,
    location_name = CASE WHEN excluded.location_name != '' THEN excluded.location_name ELSE media_items.location_name END,
    raw_metadata  = CASE WHEN excluded.raw_metadata != '' THEN excluded.raw_metadata ELSE media_items.raw_metadata END,
    updated_at    = excluded.updated_at;
```

同理，更新 `albums` 相簿主表時亦須遵守非破壞性合併語法：

```sql
INSERT INTO albums (
    id, title, product_url, cover_base_url, cover_thumb_path,
    item_count, start_time, end_time, raw_metadata, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    title            = CASE WHEN excluded.title != '' THEN excluded.title ELSE albums.title END,
    product_url      = CASE WHEN excluded.product_url != '' THEN excluded.product_url ELSE albums.product_url END,
    cover_base_url   = CASE WHEN excluded.cover_base_url != '' THEN excluded.cover_base_url ELSE albums.cover_base_url END,
    cover_thumb_path = CASE WHEN excluded.cover_thumb_path != '' THEN excluded.cover_thumb_path ELSE albums.cover_thumb_path END,
    item_count       = CASE WHEN excluded.item_count > 0 THEN excluded.item_count ELSE albums.item_count END,
    start_time       = CASE WHEN excluded.start_time > 0 THEN excluded.start_time ELSE albums.start_time END,
    end_time         = CASE WHEN excluded.end_time > 0 THEN excluded.end_time ELSE albums.end_time END,
    raw_metadata     = CASE WHEN excluded.raw_metadata != '' THEN excluded.raw_metadata ELSE albums.raw_metadata END,
    updated_at       = excluded.updated_at;
```

---

## 4. AI Agent 分析與標籤回寫指南 (AI Vision Pipeline)

其他 Agent（如執行本地 Ollama、Apple Silicon MLX 或 CLIP 視覺模型的腳本）可依循以下標準作業流程：

### 步驟 1：檢索尚無描述但已有縮圖的相片/影片
```sql
SELECT id, thumb_path, filename, media_type, width, height, duration
FROM media_items
WHERE description = '' AND thumb_path != ''
ORDER BY taken_at DESC
LIMIT 50;
```

### 步驟 2：讀取縮圖進行視覺推論
- 圖片實體位置：`filepath.Join(dataDir, thumb_path)`，格式為標準 WebP（300×300）。
- 若為影片（`media_type = 'video'`），縮圖為 Google 官方截取的影片代表幀。
- 呼叫本地 VLM（如 `qwen2.5-vl`, `llava`, `moondream`）取得圖片內容標籤、物體、場景或文字辨識（OCR）。

### 步驟 3：回寫 `description` 並同步 `media_fts`
```sql
-- 1. 更新主表描述與更新時間戳
UPDATE media_items
SET description = ?, updated_at = unixepoch()
WHERE id = ?;

-- 2. 同步更新全文檢索表
DELETE FROM media_fts WHERE id = ?;
INSERT INTO media_fts (id, filename, description, camera_make, camera_model, location_name)
SELECT id, filename, description, camera_make, camera_model, location_name
FROM media_items
WHERE id = ?;
```

---

## 5. 常見分析查詢範例 (Agent Query Cheatsheet)

### 1. 查詢所有 4K 影片與其時長
```sql
SELECT
    id,
    COALESCE(NULLIF(filename, ''), id) AS display_name,
    width || 'x' || height AS resolution,
    duration || 's' AS duration_sec,
    datetime(taken_at, 'unixepoch', 'localtime') AS shot_time
FROM media_items
WHERE media_type = 'video' AND (width >= 3840 OR height >= 3840)
ORDER BY duration DESC;
```

### 2. 統計各相機/設備拍攝數量
```sql
SELECT
    CASE WHEN camera_model != '' THEN camera_model ELSE '未知設備' END AS device,
    count(*) AS total_count,
    sum(CASE WHEN media_type = 'video' THEN 1 ELSE 0 END) AS video_count,
    sum(CASE WHEN media_type = 'photo' THEN 1 ELSE 0 END) AS photo_count
FROM media_items
GROUP BY device
ORDER BY total_count DESC;
```

### 3. 透過 FTS5 全文檢索特定關鍵字
```sql
SELECT m.id, m.filename, m.description, m.camera_model, m.thumb_path
FROM media_items m
WHERE m.id IN (
    SELECT id FROM media_fts WHERE media_fts MATCH '海灘 OR 日落*'
)
ORDER BY m.taken_at DESC;
```

### 4. 尋找尚待補齊檔名與詳細 EXIF 的項目
```sql
SELECT count(*) AS pending_details
FROM media_items
WHERE filename = '';
```

---

## 6. 相簿資料抽取與匯出指南 (Album Extraction Guide for RD / AI Agents)

後續開發「匯出相簿資料 (`export`)」指令或撰寫外部自動化腳本時，可直接透過 `albums`、`album_items` 與 `media_items` 三表關聯抽取完整結構化資料。

### 6.1 列出所有相簿與實際已索引張數
```sql
SELECT
    a.id AS album_id,
    a.title AS album_title,
    a.product_url AS album_url,
    a.cover_thumb_path,
    a.item_count AS expected_count,
    COUNT(ai.media_id) AS indexed_count,
    CASE WHEN a.start_time > 0 THEN datetime(a.start_time, 'unixepoch', 'localtime') ELSE '' END AS start_date,
    CASE WHEN a.end_time > 0 THEN datetime(a.end_time, 'unixepoch', 'localtime') ELSE '' END AS end_date
FROM albums a
LEFT JOIN album_items ai ON a.id = ai.album_id
GROUP BY a.id
ORDER BY a.start_time DESC, a.updated_at DESC;
```

### 6.2 依相簿 ID 或相簿名稱抽取該相簿內所有相片/影片完整清單
```sql
SELECT
    a.id AS album_id,
    a.title AS album_title,
    m.id AS media_id,
    m.filename,
    m.media_type,
    m.taken_at,
    datetime(m.taken_at, 'unixepoch', 'localtime') AS taken_at_local,
    m.width,
    m.height,
    m.duration,
    m.camera_make,
    m.camera_model,
    m.latitude,
    m.longitude,
    m.location_name,
    m.description,
    m.thumb_path,
    m.product_url,
    m.base_url
FROM album_items ai
JOIN albums a ON ai.album_id = a.id
JOIN media_items m ON ai.media_id = m.id
WHERE a.id = ? OR a.title LIKE ?
ORDER BY m.taken_at ASC, m.id ASC;
```

### 6.3 查詢某張相片所屬的所有相簿
```sql
SELECT a.id, a.title, a.product_url
FROM album_items ai
JOIN albums a ON ai.album_id = a.id
WHERE ai.media_id = ?;
```

---

## 7. Python 讀取、AI 回寫與相簿匯出範例代碼 (Agent Script Template)

```python
import sqlite3
import json
from pathlib import Path

DB_PATH = Path("./data/index.db")

def get_db_connection():
    conn = sqlite3.connect(DB_PATH)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode = WAL;")
    conn.execute("PRAGMA busy_timeout = 5000;")
    return conn

def update_ai_description(item_id: str, description: str):
    with get_db_connection() as conn:
        cursor = conn.cursor()
        cursor.execute(
            "UPDATE media_items SET description = ?, updated_at = unixepoch() WHERE id = ?",
            (description, item_id)
        )
        cursor.execute("DELETE FROM media_fts WHERE id = ?", (item_id,))
        cursor.execute(
            """INSERT INTO media_fts (id, filename, description, camera_make, camera_model, location_name)
               SELECT id, filename, description, camera_make, camera_model, location_name
               FROM media_items WHERE id = ?""",
            (item_id,)
        )
        conn.commit()

def export_album_to_dict(album_id: str) -> dict:
    """從本地 index.db 抽取指定相簿及其所有相片/影片 Metadata 供匯出使用"""
    with get_db_connection() as conn:
        cursor = conn.cursor()
        album_row = cursor.execute(
            "SELECT * FROM albums WHERE id = ?", (album_id,)
        ).fetchone()
        if not album_row:
            raise ValueError(f"Album not found: {album_id}")

        items = cursor.execute(
            """SELECT m.*
               FROM album_items ai
               JOIN media_items m ON ai.media_id = m.id
               WHERE ai.album_id = ?
               ORDER BY m.taken_at ASC, m.id ASC""",
            (album_id,)
        ).fetchall()

        return {
            "album": dict(album_row),
            "items": [dict(row) for row in items],
        }
```
