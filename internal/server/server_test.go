package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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

	// 2. 測試 GET / 首頁 HTML Embed 輸出
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
