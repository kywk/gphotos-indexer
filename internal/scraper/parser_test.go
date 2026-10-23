package scraper

import (
	"testing"

	"github.com/local/gphotos-indexer/internal/db"
)

func TestParseBatchExecute(t *testing.T) {
	mockResponse := ")]}'\n\n" +
		"185\n" +
		"[" +
		"  [\"wrb.fr\", \"sy123\", \"[[\\\"AF1QipMockPhotoKey0001\\\",null,1769322730000,null,[\\\"https://lh3.googleusercontent.com/pw/AP1GczMockBaseUrl1=w800-h600-no\\\",4032,3024,[10,20]]]]\", null, null, null, \"generic\"]" +
		"]\n" +
		"210\n" +
		"[" +
		"  [\"wrb.fr\", \"fDcn4b\", \"[[\\\"AF1QipMockVideoKey0002\\\",null,1769409130000,null,[\\\"https://lh3.googleusercontent.com/pw/AP1GczMockBaseUrl2=w400-h400-c\\\",1920,1080],[\\\"IMG_20260126_0912.MOV\\\",[25.0777,121.3894]]]]\", null, null, null, \"generic\"]" +
		"]\n" +
		"230\n" +
		"[" +
		"  [\"wrb.fr\", \"fDcn4b\", \"[[\\\"AF1QipMockVideoKey0003\\\",null,1769409200000,null,[\\\"https://photos.fife.usercontent.google.com/pw/AP1GczMockFifeUrl=w124-h220-k-no\\\",1080,1920],[\\\"VID_20261016_0251.mkv\\\",null]]]\", null, null, null, \"generic\"]" +
		"]\n"

	items := ParseBatchExecute(mockResponse)
	if len(items) != 3 {
		t.Fatalf("預期解析出 3 筆項目，實際取得 %d 筆", len(items))
	}

	byID := make(map[string]db.MediaItem)
	for _, it := range items {
		byID[it.ID] = it
	}

	// 斷言 1：驗證一般照片列表項目，且整數陣列 [10,20] 不應被誤判為 GPS 座標
	photo, ok := byID["AF1QipMockPhotoKey0001"]
	if !ok {
		t.Fatalf("找不到預期的照片 ID: AF1QipMockPhotoKey0001")
	}
	if photo.TakenAt != 1769322730 {
		t.Errorf("拍攝時間解析錯誤: 預期 1769322730, 實際為 %d", photo.TakenAt)
	}
	if photo.BaseURL != "https://lh3.googleusercontent.com/pw/AP1GczMockBaseUrl1" {
		t.Errorf("BaseURL 剝除參數錯誤: 實際為 %s", photo.BaseURL)
	}
	if photo.Width != 4032 || photo.Height != 3024 {
		t.Errorf("解析度錯誤: 預期 4032x3024, 實際為 %dx%d", photo.Width, photo.Height)
	}
	if photo.Latitude != 0 || photo.Longitude != 0 {
		t.Errorf("整數陣列 [10,20] 不應被誤判為 GPS 經緯度: got (%f, %f)", photo.Latitude, photo.Longitude)
	}

	// 斷言 2：驗證含檔名、影片副檔名與浮點數 GPS 座標的詳情項目
	video, ok := byID["AF1QipMockVideoKey0002"]
	if !ok {
		t.Fatalf("找不到預期的影片 ID: AF1QipMockVideoKey0002")
	}
	if video.Filename != "IMG_20260126_0912.MOV" {
		t.Errorf("檔名解析錯誤: 預期 IMG_20260126_0912.MOV, 實際為 %s", video.Filename)
	}
	if video.MediaType != "video" {
		t.Errorf("媒體類型應根據 .MOV 自動識別為 video, 實際為 %s", video.MediaType)
	}
	if video.Latitude != 25.0777 || video.Longitude != 121.3894 {
		t.Errorf("GPS 座標解析錯誤: 預期 (25.0777, 121.3894), 實際為 (%f, %f)", video.Latitude, video.Longitude)
	}

	// 斷言 3：驗證 .mkv 格式與 photos.fife.usercontent.google.com BaseURL
	mkvVideo, ok := byID["AF1QipMockVideoKey0003"]
	if !ok {
		t.Fatalf("找不到預期的影片 ID: AF1QipMockVideoKey0003")
	}
	if mkvVideo.Filename != "VID_20261016_0251.mkv" {
		t.Errorf("檔名解析錯誤: 預期 VID_20261016_0251.mkv, 實際為 %s", mkvVideo.Filename)
	}
	if mkvVideo.MediaType != "video" {
		t.Errorf("媒體類型應根據 .mkv 自動識別為 video, 實際為 %s", mkvVideo.MediaType)
	}
	if mkvVideo.BaseURL != "https://photos.fife.usercontent.google.com/pw/AP1GczMockFifeUrl" {
		t.Errorf("BaseURL 剝除錯誤: 實際為 %s", mkvVideo.BaseURL)
	}
	if mkvVideo.Width != 1080 || mkvVideo.Height != 1920 {
		t.Errorf("解析度解析錯誤: 預期 1080x1920, 實際為 %dx%d", mkvVideo.Width, mkvVideo.Height)
	}
}

func TestParseBatchExecute_InitDataCallback(t *testing.T) {
	scriptPayload := `AF_initDataCallback({key: 'ds:8', hash: '9', data:[[["AF1QipMockInitVideo0004",["https://photos.fife.usercontent.google.com/pw/AP1GczInitUrl",1080,1920],1792090318000,null,null,null,null,null,null,{"76647426":[217961,null,1080,1920]}]]]});`
	items := ParseBatchExecute(scriptPayload)
	if len(items) != 1 {
		t.Fatalf("預期解析出 1 筆項目，實際為 %d", len(items))
	}
	it := items[0]
	if it.ID != "AF1QipMockInitVideo0004" {
		t.Errorf("ID 不符: %s", it.ID)
	}
	if it.TakenAt != 1792090318 {
		t.Errorf("TakenAt 不符: %d", it.TakenAt)
	}
	if it.BaseURL != "https://photos.fife.usercontent.google.com/pw/AP1GczInitUrl" {
		t.Errorf("BaseURL 不符: %s", it.BaseURL)
	}
	if it.Width != 1080 || it.Height != 1920 {
		t.Errorf("解析度不符: %dx%d", it.Width, it.Height)
	}
	if it.MediaType != "video" {
		t.Errorf("媒體類型應為 video，實際為 %s", it.MediaType)
	}
	if it.Duration != 217 {
		t.Errorf("影片長度解析錯誤: 預期 217 秒, 實際為 %d", it.Duration)
	}
}

func TestParseBatchExecute_InvalidInput(t *testing.T) {
	for _, bad := range []string{"", "not json", ")]}'\n\ninvalid", "[[123]]"} {
		if items := ParseBatchExecute(bad); len(items) != 0 {
			t.Errorf("無效輸入 %q 應回傳空切片，實際取得 %d 筆", bad, len(items))
		}
	}
}

func TestParseAlbumsBatchExecute(t *testing.T) {
	mockAlbumsResponse := ")]}'\n\n" +
		"240\n" +
		"[" +
		"  [\"wrb.fr\", \"Z5xsfc\", \"[[[\\\"AF1QipMockAlbumKey0001\\\",[[\\\"https://lh3.googleusercontent.com/pw/AP1GczAlbumCover1=w400-h400-c\\\",1200,900]],null,null,{\\\"72930366\\\":[48,\\\"2026 北海道冬季旅遊\\\",[1769322730000,1769904000000]]}]]]\", null, null, null, \"generic\"]" +
		"]\n" +
		"210\n" +
		"[" +
		"  [\"wrb.fr\", \"Z5xsfc\", \"[[[\\\"AF1QipMockAlbumKey0002\\\",[\\\"https://photos.fife.usercontent.google.com/pw/AP1GczAlbumCover2=w300-h300\\\"],\\\"家庭聚餐相簿\\\",15,[1759000000000,1759086400000]]]]\", null, null, null, \"generic\"]" +
		"]\n" +
		"185\n" +
		"[" +
		"  [\"wrb.fr\", \"sy123\", \"[[\\\"AF1QipMockPhotoKey0001\\\",null,1769322730000,null,[\\\"https://lh3.googleusercontent.com/pw/AP1GczMockBaseUrl1=w800-h600-no\\\",4032,3024,[10,20]]]]\", null, null, null, \"generic\"]" +
		"]\n"

	albums := ParseAlbumsBatchExecute(mockAlbumsResponse)
	if len(albums) != 2 {
		t.Fatalf("預期解析出 2 筆相簿（排除一般相片節點），實際取得 %d 筆", len(albums))
	}

	byID := make(map[string]db.Album)
	for _, a := range albums {
		byID[a.ID] = a
	}

	alb1, ok := byID["AF1QipMockAlbumKey0001"]
	if !ok {
		t.Fatalf("找不到預期的相簿 ID: AF1QipMockAlbumKey0001")
	}
	if alb1.Title != "2026 北海道冬季旅遊" {
		t.Errorf("相簿標題解析錯誤: got %q", alb1.Title)
	}
	if alb1.ItemCount != 48 {
		t.Errorf("相簿項目數解析錯誤: 預期 48, 實際為 %d", alb1.ItemCount)
	}
	if alb1.CoverBaseURL != "https://lh3.googleusercontent.com/pw/AP1GczAlbumCover1" {
		t.Errorf("封面 BaseURL 剝除參數錯誤: got %q", alb1.CoverBaseURL)
	}
	if alb1.StartTime != 1769322730 || alb1.EndTime != 1769904000 {
		t.Errorf("相簿時間範圍解析錯誤: Start=%d, End=%d", alb1.StartTime, alb1.EndTime)
	}

	alb2, ok := byID["AF1QipMockAlbumKey0002"]
	if !ok {
		t.Fatalf("找不到預期的相簿 ID: AF1QipMockAlbumKey0002")
	}
	if alb2.Title != "家庭聚餐相簿" || alb2.ItemCount != 15 {
		t.Errorf("相簿 2 解析錯誤: Title=%q, ItemCount=%d", alb2.Title, alb2.ItemCount)
	}
	if alb2.CoverBaseURL != "https://photos.fife.usercontent.google.com/pw/AP1GczAlbumCover2" {
		t.Errorf("相簿 2 封面 BaseURL 錯誤: got %q", alb2.CoverBaseURL)
	}

	// 同時確認 ParseBatchExecute 不會將含 72930366 的相簿節點誤判為一般相片
	mediaItems := ParseBatchExecute(mockAlbumsResponse)
	for _, m := range mediaItems {
		if m.ID == "AF1QipMockAlbumKey0001" {
			t.Errorf("ParseBatchExecute 不應將含 72930366 的相簿節點解析為相片")
		}
	}
}
