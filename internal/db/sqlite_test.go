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

	// 階段 1：模擬從時間軸列表抓到縮圖、拍攝時間、解析度與影片時長（此時沒有檔名與 EXIF）
	stage1 := MediaItem{
		ID:        id,
		BaseURL:   "https://lh3.googleusercontent.com/pw/AP1GczStage1",
		ThumbPath: "thumbs/" + id + ".webp",
		TakenAt:   1769322730,
		Width:     4032,
		Height:    3024,
		Duration:  125,
		MediaType: "video",
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
	if got.TakenAt != 1769322730 || got.Width != 4032 || got.Duration != 125 {
		t.Errorf("階段 1 的 TakenAt/Width/Duration 被洗掉了: TakenAt=%d, Width=%d, Duration=%d", got.TakenAt, got.Width, got.Duration)
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
			Duration:     85,
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

func TestStore_AlbumsAndAlbumItems(t *testing.T) {
	store := newTestStore(t)

	albumID := "AF1QipAlbumTest0000001"

	// 階段 1：從相簿列表頁抓到 ID、封面與初估數量，但尚無標題與起訖時間
	if err := store.UpsertAlbum(Album{
		ID:             albumID,
		CoverBaseURL:   "https://lh3.googleusercontent.com/pw/AP1GczAlbumCover1",
		CoverThumbPath: "thumbs/" + albumID + ".webp",
		ItemCount:      2,
	}); err != nil {
		t.Fatalf("Stage 1 UpsertAlbum 失敗: %v", err)
	}

	if !store.HasAlbumThumb(albumID) {
		t.Errorf("預期 HasAlbumThumb 為 true")
	}

	// 階段 2：進入相簿內頁補齊標題，此時未帶入 CoverThumbPath 與 ItemCount，驗證非破壞性合併
	if err := store.UpsertAlbum(Album{
		ID:    albumID,
		Title: "2026 日本東京滑雪行",
	}); err != nil {
		t.Fatalf("Stage 2 UpsertAlbum 失敗: %v", err)
	}

	// 寫入兩張相片並關聯至該相簿，另有 1 張不屬於該相簿
	items := []MediaItem{
		{
			ID:        "AF1QipAlbumPhoto0001",
			Filename:  "IMG_001.JPG",
			MediaType: "photo",
			TakenAt:   1769900000,
			ThumbPath: "thumbs/AF1QipAlbumPhoto0001.webp",
		},
		{
			ID:        "AF1QipAlbumPhoto0002",
			Filename:  "IMG_002.JPG",
			MediaType: "photo",
			TakenAt:   1770000000,
			ThumbPath: "thumbs/AF1QipAlbumPhoto0002.webp",
		},
		{
			ID:        "AF1QipOtherPhoto0003",
			Filename:  "IMG_003.JPG",
			MediaType: "photo",
			TakenAt:   1770100000,
		},
	}
	for _, it := range items {
		if err := store.Upsert(it); err != nil {
			t.Fatalf("Upsert media item 失敗: %v", err)
		}
	}

	if err := store.AddAlbumItem(albumID, "AF1QipAlbumPhoto0001"); err != nil {
		t.Fatalf("AddAlbumItem 1 失敗: %v", err)
	}
	if err := store.AddAlbumItem(albumID, "AF1QipAlbumPhoto0002"); err != nil {
		t.Fatalf("AddAlbumItem 2 失敗: %v", err)
	}

	if !store.HasAlbumItem(albumID, "AF1QipAlbumPhoto0001") {
		t.Errorf("預期 HasAlbumItem 為 true")
	}
	if store.HasAlbumItem(albumID, "AF1QipOtherPhoto0003") {
		t.Errorf("預期未關聯的相片 HasAlbumItem 為 false")
	}

	if err := store.SyncAlbumStats(albumID); err != nil {
		t.Fatalf("SyncAlbumStats 失敗: %v", err)
	}

	albums, err := store.ListAlbums()
	if err != nil || len(albums) != 1 {
		t.Fatalf("預期 ListAlbums 回傳 1 筆相簿, got %d, err=%v", len(albums), err)
	}
	gotAlb := albums[0]
	if gotAlb.Title != "2026 日本東京滑雪行" {
		t.Errorf("相簿標題錯誤: got %q", gotAlb.Title)
	}
	if gotAlb.CoverThumbPath != "thumbs/"+albumID+".webp" {
		t.Errorf("階段 1 的 CoverThumbPath 被洗掉了: got %q", gotAlb.CoverThumbPath)
	}
	if gotAlb.IndexedCount != 2 || gotAlb.ItemCount != 2 {
		t.Errorf("相簿項目統計錯誤: ItemCount=%d, IndexedCount=%d", gotAlb.ItemCount, gotAlb.IndexedCount)
	}
	if gotAlb.StartTime != 1769900000 || gotAlb.EndTime != 1770000000 {
		t.Errorf("相簿起訖時間統計錯誤: StartTime=%d, EndTime=%d", gotAlb.StartTime, gotAlb.EndTime)
	}

	// 測試透過 AlbumID 過濾相片
	byAlbum, total, err := store.Search(SearchFilter{AlbumID: albumID})
	if err != nil || total != 2 || len(byAlbum) != 2 {
		t.Fatalf("依 AlbumID 過濾錯誤: 預期 2 筆, 實際 total=%d, err=%v", total, err)
	}

	// 測試透過關鍵字搜尋相簿名稱也能找到相簿內相片
	byAlbumTitle, total, err := store.Search(SearchFilter{Query: "東京滑雪"})
	if err != nil || total != 2 || len(byAlbumTitle) != 2 {
		t.Fatalf("依相簿標題關鍵字搜尋相片錯誤: 預期 2 筆, 實際 total=%d, err=%v", total, err)
	}
}
