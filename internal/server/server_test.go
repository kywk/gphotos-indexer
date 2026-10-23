package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/local/gphotos-indexer/internal/db"
)

func TestServer_APISearchAndIndex(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := db.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	_ = store.Upsert(db.MediaItem{
		ID:          "AF1QipServerTest0001",
		Filename:    "IMG_20261018_Dog.JPG",
		Description: "可愛的狗狗照片",
		MediaType:   "photo",
		TakenAt:     1792281600, // 2026-10-18
	})
	_ = store.Upsert(db.MediaItem{
		ID:          "AF1QipServerTest0002",
		Filename:    "IMG_20261019_Cat.JPG",
		Description: "可愛的貓咪照片",
		MediaType:   "photo",
		TakenAt:     1792368000, // 2026-10-19
	})
	_ = store.UpsertAlbum(db.Album{
		ID:        "AF1QipServerAlbum001",
		Title:     "寵物精選相簿",
		ItemCount: 1,
	})
	_ = store.AddAlbumItem("AF1QipServerAlbum001", "AF1QipServerTest0001")

	srv := New(store, tmpDir, 0)

	// 1. 測試 GET /api/photos 搜尋 API
	req := httptest.NewRequest(http.MethodGet, "/api/photos?q=Dog&type=photo&date_from=2026-10-01&date_to=2026-10-31", nil)
	rec := httptest.NewRecorder()
	srv.handleSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("預期 HTTP 200, 實際為 %d", rec.Code)
	}

	var resp struct {
		Total int            `json:"total"`
		Items []db.MediaItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析 API 回應 JSON 失敗: %v", err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 {
		t.Fatalf("預期 API 回傳 1 筆結果, 實際 Total=%d", resp.Total)
	}
	if resp.Items[0].Filename != "IMG_20261018_Dog.JPG" {
		t.Errorf("檔名不符: got %q", resp.Items[0].Filename)
	}

	// 2. 測試 GET /api/albums 相簿列表 API
	reqAlbums := httptest.NewRequest(http.MethodGet, "/api/albums", nil)
	recAlbums := httptest.NewRecorder()
	srv.handleAlbums(recAlbums, reqAlbums)

	if recAlbums.Code != http.StatusOK {
		t.Fatalf("預期 /api/albums HTTP 200, 實際為 %d", recAlbums.Code)
	}

	var albumsResp struct {
		Total int        `json:"total"`
		Items []db.Album `json:"items"`
	}
	if err := json.Unmarshal(recAlbums.Body.Bytes(), &albumsResp); err != nil {
		t.Fatalf("解析 /api/albums JSON 失敗: %v", err)
	}
	if albumsResp.Total != 1 || len(albumsResp.Items) != 1 || albumsResp.Items[0].Title != "寵物精選相簿" {
		t.Errorf("/api/albums 回傳內容不符預期: %+v", albumsResp)
	}

	// 3. 測試 GET /api/photos?album_id=... 依相簿過濾 API
	reqByAlbum := httptest.NewRequest(http.MethodGet, "/api/photos?album_id=AF1QipServerAlbum001", nil)
	recByAlbum := httptest.NewRecorder()
	srv.handleSearch(recByAlbum, reqByAlbum)

	var albumPhotosResp struct {
		Total int            `json:"total"`
		Items []db.MediaItem `json:"items"`
	}
	if err := json.Unmarshal(recByAlbum.Body.Bytes(), &albumPhotosResp); err != nil {
		t.Fatalf("解析依相簿過濾 JSON 失敗: %v", err)
	}
	if albumPhotosResp.Total != 1 || len(albumPhotosResp.Items) != 1 || albumPhotosResp.Items[0].ID != "AF1QipServerTest0001" {
		t.Errorf("依 album_id 過濾結果不符預期: %+v", albumPhotosResp)
	}

	// 4. 測試 GET / 首頁 HTML Embed 輸出
	reqHTML := httptest.NewRequest(http.MethodGet, "/", nil)
	recHTML := httptest.NewRecorder()
	srv.handleIndex(recHTML, reqHTML)

	if recHTML.Code != http.StatusOK {
		t.Fatalf("首頁預期 HTTP 200, 實際為 %d", recHTML.Code)
	}
	if !strings.Contains(recHTML.Body.String(), "gphotos-indexer") {
		t.Errorf("首頁 HTML 內容缺少預期標題")
	}
}

func TestServer_StartGracefulShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := db.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	srv := New(store, tmpDir, 0)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// 等待伺服器完成監聽並啟動
	time.Sleep(100 * time.Millisecond)
	// 模擬收到中斷信號 (Ctrl+C)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("預期伺服器優雅關閉 (nil), 實際錯誤: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("伺服器未能於指定時間內響應 Context 關閉")
	}
}
