package db

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test_index.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("無法開啟測試用 SQLite: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func TestStore_UpsertPreservesNonEmptyFields(t *testing.T) {
	store := newTestStore(t)
	id := "AF1QipTestMerge000001"

	// 階段 1：模擬從時間軸列表抓到縮圖、拍攝時間與解析度（此時沒有檔名與 EXIF）
	stage1 := MediaItem{
		ID:        id,
		BaseURL:   "https://lh3.googleusercontent.com/pw/AP1GczStage1",
		ThumbPath: "thumbs/" + id + ".webp",
		TakenAt:   1769322730,
		Width:     4032,
		Height:    3024,
		MediaType: "photo",
	}
	if err := store.Upsert(stage1); err != nil {
		t.Fatalf("Stage 1 Upsert 失敗: %v", err)
	}

	if !store.HasThumb(id) {
		t.Errorf("預期 HasThumb 為 true")
	}

	// 階段 2：模擬從詳情面板補抓到檔名、相機型號與地點（此時傳入的結構沒有 ThumbPath 與 TakenAt）
	stage2 := MediaItem{
		ID:           id,
		Filename:     "IMG_20260125_Kanas.HEIC",
		CameraMake:   "Apple",
		CameraModel:  "iPhone 16 Pro",
		Latitude:     48.7051,
		Longitude:    87.0145,
		LocationName: "喀納斯湖",
	}
	if err := store.Upsert(stage2); err != nil {
		t.Fatalf("Stage 2 Upsert 失敗: %v", err)
	}

	// 驗證兩階段資料是否完整合併，未互相覆蓋為空值
	items, total, err := store.Search(SearchFilter{Query: "喀納斯"})
	if err != nil {
		t.Fatalf("Search 失敗: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("預期透過 FTS5 搜尋『喀納斯』找到 1 筆，實際找到 %d 筆", total)
	}

	got := items[0]
	if got.ThumbPath != "thumbs/"+id+".webp" {
		t.Errorf("階段 1 的 ThumbPath 被洗掉了: got %q", got.ThumbPath)
	}
	if got.TakenAt != 1769322730 || got.Width != 4032 {
		t.Errorf("階段 1 的 TakenAt/Width 被洗掉了: TakenAt=%d, Width=%d", got.TakenAt, got.Width)
	}
	if got.Filename != "IMG_20260125_Kanas.HEIC" {
		t.Errorf("階段 2 的 Filename 未寫入: got %q", got.Filename)
	}
	if got.CameraModel != "iPhone 16 Pro" {
		t.Errorf("階段 2 的 CameraModel 未寫入: got %q", got.CameraModel)
	}
}

func TestStore_SearchFilters(t *testing.T) {
	store := newTestStore(t)

	seed := []MediaItem{
		{
			ID:           "AF1QipItem0000000001",
			Filename:     "DSC00123.JPG",
			CameraModel:  "Sony A7C2",
			LocationName: "聖地牙哥",
			MediaType:    "photo",
			TakenAt:      1759000000, // 2025-09-27
		},
		{
			ID:           "AF1QipItem0000000002",
			Filename:     "VID_20260201_Snow.MP4",
			CameraModel:  "DJI Osmo Pocket 3",
			LocationName: "禾木村",
			Description:  "滑雪影片",
			MediaType:    "video",
			TakenAt:      1769904000, // 2026-02-01
		},
	}

	for _, it := range seed {
		if err := store.Upsert(it); err != nil {
			t.Fatalf("Seed Upsert 失敗: %v", err)
		}
	}

	// 1. 測試媒體類型過濾 (只找 video)
	videos, total, err := store.Search(SearchFilter{MediaType: "video"})
	if err != nil || total != 1 || videos[0].ID != "AF1QipItem0000000002" {
		t.Errorf("影片類型過濾錯誤: total=%d, err=%v", total, err)
	}

	// 2. 測試日期區間過濾 (只找 2026 年以後)
	recent, total, err := store.Search(SearchFilter{DateFrom: 1767225600})
	if err != nil || total != 1 || recent[0].Filename != "VID_20260201_Snow.MP4" {
		t.Errorf("日期區間過濾錯誤: total=%d, err=%v", total, err)
	}

	// 3. 測試相機型號關鍵字搜尋
	sony, total, err := store.Search(SearchFilter{Query: "A7C2"})
	if err != nil || total != 1 || sony[0].ID != "AF1QipItem0000000001" {
		t.Errorf("相機型號搜尋錯誤: total=%d, err=%v", total, err)
	}
}
