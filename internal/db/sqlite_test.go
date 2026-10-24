package db

import (
	"os"
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

func TestStore_CleanupSpuriousAlbums(t *testing.T) {
	store := newTestStore(t)

	// 模擬誤判髒資料：相片 ID 同時存在於 media_items 與 albums（同名重複相簿根因）
	photoID := "AF1QipSpuriousPhoto0001"
	realAlbumID := "AF1QipRealAlbum0000001"
	if err := store.Upsert(MediaItem{ID: photoID, ThumbPath: "thumbs/" + photoID + ".webp"}); err != nil {
		t.Fatalf("Upsert 相片失敗: %v", err)
	}
	if err := store.UpsertAlbum(Album{ID: photoID, Title: "17.02.12 Brussels (orig)", ItemCount: 194}); err != nil {
		t.Fatalf("Upsert 誤判相簿失敗: %v", err)
	}
	if err := store.UpsertAlbum(Album{ID: realAlbumID, Title: "17.02.12 Brussels (orig)", ItemCount: 194}); err != nil {
		t.Fatalf("Upsert 真實相簿失敗: %v", err)
	}
	if err := store.AddAlbumItem(photoID, photoID); err != nil {
		t.Fatalf("AddAlbumItem 髒關聯失敗: %v", err)
	}

	if !store.HasMedia(photoID) || !store.HasAlbum(photoID) {
		t.Fatalf("前置狀態錯誤：預期相片與誤判相簿同時存在")
	}

	deleted, err := store.CleanupSpuriousAlbums()
	if err != nil {
		t.Fatalf("CleanupSpuriousAlbums 失敗: %v", err)
	}
	if deleted != 1 {
		t.Errorf("預期清理 1 筆誤判相簿，實際 %d 筆", deleted)
	}
	if store.HasAlbum(photoID) {
		t.Errorf("誤判相簿列應已被刪除")
	}
	if !store.HasMedia(photoID) {
		t.Errorf("相片列不應被刪除")
	}
	if !store.HasAlbum(realAlbumID) {
		t.Errorf("真實相簿不應被刪除")
	}
	if store.HasAlbumItem(photoID, photoID) {
		t.Errorf("孤兒關聯應已被清理")
	}

	// 相片為準：DeleteAlbum 不得影響相片列
	if err := store.UpsertAlbum(Album{ID: photoID, Title: "junk"}); err != nil {
		t.Fatalf("重建髒資料失敗: %v", err)
	}
	if err := store.DeleteAlbum(photoID); err != nil {
		t.Fatalf("DeleteAlbum 失敗: %v", err)
	}
	if store.HasAlbum(photoID) {
		t.Errorf("DeleteAlbum 後相簿列應消失")
	}
	if !store.HasMedia(photoID) {
		t.Errorf("DeleteAlbum 不得刪除相片列")
	}
}

func TestStore_GetAlbum(t *testing.T) {
	store := newTestStore(t)
	if err := store.UpsertAlbum(Album{ID: "AF1QipGetAlbum00001", Title: "測試相簿", CoverBaseURL: "https://lh3.googleusercontent.com/pw/cover1", ItemCount: 5}); err != nil {
		t.Fatalf("UpsertAlbum 失敗: %v", err)
	}
	got, err := store.GetAlbum("AF1QipGetAlbum00001")
	if err != nil {
		t.Fatalf("GetAlbum 失敗: %v", err)
	}
	if got.Title != "測試相簿" || got.CoverBaseURL != "https://lh3.googleusercontent.com/pw/cover1" || got.ItemCount != 5 {
		t.Errorf("GetAlbum 回傳不符: %+v", got)
	}
	if _, err := store.GetAlbum("AF1QipNonExistent00001"); err == nil {
		t.Errorf("預期查詢不存在相簿應回傳錯誤")
	}
}

func TestStore_Doctor(t *testing.T) {
	store := newTestStore(t)

	// 2 筆同名零索引重複 + 1 筆已收錄同名 + 1 筆跨表 + 1 筆空標題
	for _, id := range []string{"AF1QipDoctorDup000001", "AF1QipDoctorDup000002"} {
		if err := store.UpsertAlbum(Album{ID: id, Title: "重複相簿", ItemCount: 10}); err != nil {
			t.Fatalf("UpsertAlbum 失敗: %v", err)
		}
	}
	if err := store.UpsertAlbum(Album{ID: "AF1QipDoctorReal00001", Title: "重複相簿", ItemCount: 10}); err != nil {
		t.Fatalf("UpsertAlbum 失敗: %v", err)
	}
	if err := store.Upsert(MediaItem{ID: "AF1QipDoctorPhoto0001", ThumbPath: "thumbs/AF1QipDoctorPhoto0001.webp"}); err != nil {
		t.Fatalf("Upsert 相片失敗: %v", err)
	}
	if err := store.AddAlbumItem("AF1QipDoctorReal00001", "AF1QipDoctorPhoto0001"); err != nil {
		t.Fatalf("AddAlbumItem 失敗: %v", err)
	}
	if err := store.UpsertAlbum(Album{ID: "AF1QipDoctorPhoto0001", Title: "跨表髒列"}); err != nil {
		t.Fatalf("UpsertAlbum 失敗: %v", err)
	}
	if err := store.UpsertAlbum(Album{ID: "AF1QipDoctorEmpty00001"}); err != nil {
		t.Fatalf("UpsertAlbum 失敗: %v", err)
	}

	rep, err := store.Doctor("")
	if err != nil {
		t.Fatalf("Doctor 失敗: %v", err)
	}
	if rep.AlbumsTotal != 5 || rep.MediaTotal != 1 || rep.LinksTotal != 1 {
		t.Errorf("總數不符: %+v", rep)
	}
	if len(rep.CrossTableIDs) != 1 {
		t.Errorf("應偵測 1 筆跨表，實際 %d", len(rep.CrossTableIDs))
	}
	if len(rep.EmptyTitleIDs) != 1 {
		t.Errorf("應偵測 1 筆空標題，實際 %d", len(rep.EmptyTitleIDs))
	}
	if len(rep.DuplicateTitles) != 1 || rep.DuplicateTitles[0].Count != 3 {
		t.Errorf("應偵測 1 組 3 列同名，實際 %+v", rep.DuplicateTitles)
	}
	if rep.DuplicateTitles[0].IndexedTotal != 1 {
		t.Errorf("同名群組已收錄應為 1 筆，實際 %d", rep.DuplicateTitles[0].IndexedTotal)
	}
	if rep.DupZeroIndexRows != 2 {
		t.Errorf("零索引重複列應為 2，實際 %d", rep.DupZeroIndexRows)
	}
	if !rep.Rebuildable() {
		t.Errorf("預期 Rebuildable 為 true")
	}
	if !rep.HasIssues() {
		t.Errorf("預期 HasIssues 為 true")
	}
}

func TestStore_Rebuild(t *testing.T) {
	store := newTestStore(t)

	// 同名群組：2 筆零索引髒列 + 1 筆已收錄真列；另有跨表髒列與錯誤標題
	for _, id := range []string{"AF1QipRebuildDup000001", "AF1QipRebuildDup000002"} {
		if err := store.UpsertAlbum(Album{ID: id, Title: "重複相簿", ItemCount: 8}); err != nil {
			t.Fatalf("UpsertAlbum 失敗: %v", err)
		}
	}
	if err := store.UpsertAlbum(Album{ID: "AF1QipRebuildReal0001", Title: "重複相簿", ItemCount: 8}); err != nil {
		t.Fatalf("UpsertAlbum 失敗: %v", err)
	}
	if err := store.Upsert(MediaItem{ID: "AF1QipRebuildPhoto001", ThumbPath: "thumbs/x.webp", TakenAt: 1769900000}); err != nil {
		t.Fatalf("Upsert 相片失敗: %v", err)
	}
	if err := store.AddAlbumItem("AF1QipRebuildReal0001", "AF1QipRebuildPhoto001"); err != nil {
		t.Fatalf("AddAlbumItem 失敗: %v", err)
	}
	if err := store.UpsertAlbum(Album{ID: "AF1QipRebuildPhoto001", Title: "跨表"}); err != nil {
		t.Fatalf("UpsertAlbum 失敗: %v", err)
	}
	if err := store.UpsertAlbum(Album{ID: "AF1QipRebuildErr000001", Title: "Error 404 (Not Found)!!1"}); err != nil {
		t.Fatalf("UpsertAlbum 失敗: %v", err)
	}
	if err := store.AddAlbumItem("AF1QipNoSuchAlbum00001", "AF1QipRebuildPhoto001"); err != nil {
		t.Fatalf("AddAlbumItem 失敗: %v", err)
	}

	res, err := store.Rebuild()
	if err != nil {
		t.Fatalf("Rebuild 失敗: %v", err)
	}
	if res.SpuriousDeleted != 1 {
		t.Errorf("預期清除 1 筆跨表，實際 %d", res.SpuriousDeleted)
	}
	if res.ZeroIndexedDupes != 2 {
		t.Errorf("預期刪除 2 筆零索引重複，實際 %d", res.ZeroIndexedDupes)
	}
	if res.ErrorTitlesCleared != 1 {
		t.Errorf("預期清除 1 筆錯誤標題，實際 %d", res.ErrorTitlesCleared)
	}
	// 孤兒關聯已由 CleanupSpuriousAlbums 順手清除，此處應為 0
	if res.OrphanLinks != 0 {
		t.Errorf("預期孤兒關聯已清零，實際 %d", res.OrphanLinks)
	}
	if !store.HasAlbum("AF1QipRebuildReal0001") {
		t.Errorf("已收錄真列不得刪除")
	}
	if store.HasAlbum("AF1QipRebuildPhoto001") {
		t.Errorf("跨表髒列應刪除")
	}
	got, _ := store.GetAlbum("AF1QipRebuildErr000001")
	if got.Title != "" {
		t.Errorf("錯誤標題應被清空，實際 %q", got.Title)
	}
	// 真列應已重算起訖時間（相片 taken_at 回填）
	gotReal, _ := store.GetAlbum("AF1QipRebuildReal0001")
	if gotReal.StartTime != 1769900000 || gotReal.EndTime != 1769900000 {
		t.Errorf("Stats 應已重算，實際 Start=%d End=%d", gotReal.StartTime, gotReal.EndTime)
	}
	// 收尾健檢：跨表、孤兒、同名重複應全數消除
	rep, err := store.Doctor("")
	if err != nil {
		t.Fatalf("Doctor 失敗: %v", err)
	}
	if len(rep.CrossTableIDs) != 0 || rep.OrphanLinksAlbum != 0 || rep.OrphanLinksMedia != 0 || len(rep.DuplicateTitles) != 0 {
		t.Errorf("Rebuild 後仍有殘留問題: %+v", rep)
	}
}

func TestStore_DoctorDuplicateCovers(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "test_index.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("無法開啟測試用 SQLite: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	writeThumb := func(rel string, content []byte) {
		p := filepath.Join(dataDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatalf("建立縮圖目錄失敗: %v", err)
		}
		if err := os.WriteFile(p, content, 0644); err != nil {
			t.Fatalf("寫入縮圖失敗: %v", err)
		}
	}

	sameCover := []byte("fake-webp-same-cover-bytes")
	otherCover := []byte("fake-webp-other-cover-bytes")
	memberOther := []byte("fake-webp-member-other-bytes")

	// 相簿 A：封面與其成員 P 位元組相同（正確），但與相簿 B 共用同一封面（可疑）
	// 相簿 B：封面與其成員 Q 不同（錯誤）且與 A 撞圖
	// 相簿 C：獨立封面且無成員，不應被標記
	albums := []Album{
		{ID: "AF1QipDoctorCoverA00001", Title: "相簿A", CoverThumbPath: "thumbs/AF1QipDoctorCoverA00001.webp"},
		{ID: "AF1QipDoctorCoverB00002", Title: "相簿B", CoverThumbPath: "thumbs/AF1QipDoctorCoverB00002.webp"},
		{ID: "AF1QipDoctorCoverC00003", Title: "相簿C", CoverThumbPath: "thumbs/AF1QipDoctorCoverC00003.webp"},
	}
	writeThumb(albums[0].CoverThumbPath, sameCover)
	writeThumb(albums[1].CoverThumbPath, sameCover)
	writeThumb(albums[2].CoverThumbPath, otherCover)
	for _, a := range albums {
		if err := store.UpsertAlbum(a); err != nil {
			t.Fatalf("UpsertAlbum 失敗: %v", err)
		}
	}

	members := []MediaItem{
		{ID: "AF1QipDoctorCoverP00001", ThumbPath: "thumbs/AF1QipDoctorCoverP00001.webp"},
		{ID: "AF1QipDoctorCoverQ00002", ThumbPath: "thumbs/AF1QipDoctorCoverQ00002.webp"},
	}
	writeThumb(members[0].ThumbPath, sameCover)
	writeThumb(members[1].ThumbPath, memberOther)
	for _, m := range members {
		if err := store.Upsert(m); err != nil {
			t.Fatalf("Upsert 相片失敗: %v", err)
		}
	}
	if err := store.AddAlbumItem(albums[0].ID, members[0].ID); err != nil {
		t.Fatalf("AddAlbumItem 失敗: %v", err)
	}
	if err := store.AddAlbumItem(albums[1].ID, members[1].ID); err != nil {
		t.Fatalf("AddAlbumItem 失敗: %v", err)
	}

	rep, err := store.Doctor(dataDir)
	if err != nil {
		t.Fatalf("Doctor 失敗: %v", err)
	}
	if len(rep.DuplicateCovers) != 1 {
		t.Fatalf("應偵測 1 組同圖封面，實際 %d 組: %+v", len(rep.DuplicateCovers), rep.DuplicateCovers)
	}
	g := rep.DuplicateCovers[0]
	if g.Count != 2 {
		t.Errorf("同圖封面群組應為 2 本，實際 %d", g.Count)
	}
	if len(rep.CoverNotInMembers) != 1 || rep.CoverNotInMembers[0] != albums[1].ID {
		t.Errorf("僅相簿 B 封面不在成員內，實際為 %v", rep.CoverNotInMembers)
	}
	if !rep.HasIssues() {
		t.Errorf("同圖封面應被視為問題，HasIssues 應為 true")
	}
}
