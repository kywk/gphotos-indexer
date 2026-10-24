package scraper

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/local/gphotos-indexer/internal/db"
)

var (
	reMediaKey = regexp.MustCompile("^AF1Qip[A-Za-z0-9_-]{10,60}$")
	reFilename = regexp.MustCompile("(?i)^[^/\\\\:*?\"<>|]{1,120}\\.(jpg|jpeg|png|heic|webp|gif|mp4|mov|avi|dng|raw|mkv|webm|m4v|3gp)$")
)

func isGoogleMediaURL(u string) bool {
	if !strings.HasPrefix(u, "https://") {
		return false
	}
	return strings.Contains(u, "googleusercontent.com/") ||
		strings.Contains(u, "usercontent.google.com/") ||
		strings.Contains(u, "ggpht.com/")
}

func isVideoFilename(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".mov") ||
		strings.HasSuffix(lower, ".mkv") || strings.HasSuffix(lower, ".webm") ||
		strings.HasSuffix(lower, ".m4v") || strings.HasSuffix(lower, ".3gp") ||
		strings.HasSuffix(lower, ".avi")
}

func ParseBatchExecute(rawBody string) []db.MediaItem {
	idx := strings.Index(rawBody, "[")
	if idx == -1 {
		return nil
	}
	cleaned := rawBody[idx:]

	itemsMap := make(map[string]*db.MediaItem)

	dec := json.NewDecoder(strings.NewReader(cleaned))
	for {
		var envelope any
		if err := dec.Decode(&envelope); err != nil {
			firstNewline := strings.Index(cleaned, "\n")
			if firstNewline == -1 {
				break
			}
			cleaned = cleaned[firstNewline+1:]
			nextBracket := strings.Index(cleaned, "[")
			if nextBracket == -1 {
				break
			}
			cleaned = cleaned[nextBracket:]
			dec = json.NewDecoder(strings.NewReader(cleaned))
			continue
		}
		walkEnvelope(envelope, itemsMap)
	}

	var results []db.MediaItem
	for _, v := range itemsMap {
		results = append(results, *v)
	}
	return results
}

func walkEnvelope(node any, out map[string]*db.MediaItem) {
	arr, ok := node.([]any)
	if !ok {
		return
	}

	if len(arr) >= 3 {
		if tag, ok := arr[0].(string); ok && tag == "wrb.fr" {
			if innerStr, ok := arr[2].(string); ok && innerStr != "" {
				var innerNode any
				if err := json.Unmarshal([]byte(innerStr), &innerNode); err == nil {
					walkPayload(innerNode, out)
				}
			}
			return
		}
	}

	if len(arr) >= 2 {
		if id, ok := arr[0].(string); ok && reMediaKey.MatchString(id) && !hasAlbumExtension(arr) {
			item, exists := out[id]
			if !exists {
				item = &db.MediaItem{
					ID:         id,
					ProductURL: "https://photos.google.com/photo/" + id,
					MediaType:  "photo",
				}
				out[id] = item
			}
			extractFieldsFromMediaArray(arr, item)
			return
		}
	}

	for _, child := range arr {
		walkEnvelope(child, out)
	}
}

func walkPayload(node any, out map[string]*db.MediaItem) {
	arr, ok := node.([]any)
	if !ok {
		return
	}

	if len(arr) >= 2 {
		if id, ok := arr[0].(string); ok && reMediaKey.MatchString(id) && !hasAlbumExtension(arr) {
			item, exists := out[id]
			if !exists {
				item = &db.MediaItem{
					ID:         id,
					ProductURL: "https://photos.google.com/photo/" + id,
					MediaType:  "photo",
				}
				out[id] = item
			}
			extractFieldsFromMediaArray(arr, item)
		}
	}

	for _, child := range arr {
		walkPayload(child, out)
	}
}

func extractFieldsFromMediaArray(arr []any, item *db.MediaItem) {
	rawBytes, _ := json.Marshal(arr)
	if len(rawBytes) < 4096 {
		item.RawMetadata = string(rawBytes)
	}

	if len(arr) > 2 {
		if tsMs, ok := toFloat64(arr[2]); ok && tsMs > 946684800000 && tsMs < 4102444800000 {
			item.TakenAt = int64(tsMs / 1000)
		}
	}

	scanSubtree(arr, item)
}

func scanSubtree(node any, item *db.MediaItem) {
	switch v := node.(type) {
	case string:
		if isGoogleMediaURL(v) && item.BaseURL == "" {
			parts := strings.Split(v, "=")
			item.BaseURL = parts[0]
		} else if reFilename.MatchString(v) && item.Filename == "" {
			item.Filename = v
			if isVideoFilename(v) {
				item.MediaType = "video"
			}
		}
	case []any:
		if len(v) >= 3 {
			if u, ok := v[0].(string); ok && isGoogleMediaURL(u) {
				parts := strings.Split(u, "=")
				item.BaseURL = parts[0]
				if w, ok := toInt(v[1]); ok && w > 0 && w < 50000 {
					item.Width = w
				}
				if h, ok := toInt(v[2]); ok && h > 0 && h < 50000 {
					item.Height = h
				}
			}
		}
		if len(v) == 2 && item.Latitude == 0 && item.Longitude == 0 {
			lat, ok1 := v[0].(float64)
			lng, ok2 := v[1].(float64)
			if ok1 && ok2 && lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180 {
				if lat != float64(int(lat)) && lng != float64(int(lng)) {
					item.Latitude = lat
					item.Longitude = lng
				}
			}
		}
		for _, child := range v {
			scanSubtree(child, item)
		}
	case map[string]any:
		if vArr, ok := v["76647426"].([]any); ok {
			item.MediaType = "video"
			if len(vArr) > 0 {
				if durMs, ok := toInt(vArr[0]); ok && durMs > 0 {
					sec := durMs / 1000
					if sec == 0 && durMs > 0 {
						sec = 1
					}
					item.Duration = sec
				}
			}
			if len(vArr) > 3 {
				if w, ok := toInt(vArr[2]); ok && w > 0 {
					item.Width = w
				}
				if h, ok := toInt(vArr[3]); ok && h > 0 {
					item.Height = h
				}
			}
		} else if _, ok := v["76647426"]; ok {
			item.MediaType = "video"
		}
		for _, child := range v {
			scanSubtree(child, item)
		}
	}
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}

func toInt(v any) (int, bool) {
	if f, ok := toFloat64(v); ok {
		return int(f), true
	}
	return 0, false
}

func hasAlbumExtension(arr []any) bool {
	for _, child := range arr {
		if m, ok := child.(map[string]any); ok {
			if _, exists := m["72930366"]; exists {
				return true
			}
		}
	}
	return false
}

func isValidTimestampMs(tsMs float64) bool {
	return tsMs > 946684800000 && tsMs < 4102444800000
}

func isCandidateAlbumTitle(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || s == "wrb.fr" || s == "generic" {
		return false
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return false
	}
	if reMediaKey.MatchString(s) || reFilename.MatchString(s) {
		return false
	}
	// 排除錯誤頁面或破圖提示文字，避免誤判為相簿標題
	// 例如進入無效相簿 URL 時會出現 "Error 404 (Not Found)!!1"
	lower := strings.ToLower(s)
	if strings.Contains(s, "404") || strings.Contains(lower, "not found") || strings.HasPrefix(lower, "error") {
		return false
	}
	if strings.Contains(s, "!!") {
		return false
	}
	return true
}

func ParseAlbumsBatchExecute(rawBody string) []db.Album {
	idx := strings.Index(rawBody, "[")
	if idx == -1 {
		return nil
	}
	cleaned := rawBody[idx:]

	albumsMap := make(map[string]*db.Album)

	dec := json.NewDecoder(strings.NewReader(cleaned))
	for {
		var envelope any
		if err := dec.Decode(&envelope); err != nil {
			firstNewline := strings.Index(cleaned, "\n")
			if firstNewline == -1 {
				break
			}
			cleaned = cleaned[firstNewline+1:]
			nextBracket := strings.Index(cleaned, "[")
			if nextBracket == -1 {
				break
			}
			cleaned = cleaned[nextBracket:]
			dec = json.NewDecoder(strings.NewReader(cleaned))
			continue
		}
		walkAlbumEnvelope(envelope, albumsMap)
	}

	var results []db.Album
	for _, v := range albumsMap {
		results = append(results, *v)
	}
	return results
}

func walkAlbumEnvelope(node any, out map[string]*db.Album) {
	arr, ok := node.([]any)
	if !ok {
		return
	}

	if len(arr) >= 3 {
		if tag, ok := arr[0].(string); ok && tag == "wrb.fr" {
			if innerStr, ok := arr[2].(string); ok && innerStr != "" {
				var innerNode any
				if err := json.Unmarshal([]byte(innerStr), &innerNode); err == nil {
					walkAlbumPayload(innerNode, out)
				}
			}
			return
		}
	}

	if len(arr) >= 2 {
		if id, ok := arr[0].(string); ok && reMediaKey.MatchString(id) {
			candidate := db.Album{
				ID:         id,
				ProductURL: "https://photos.google.com/album/" + id,
			}
			if existing, exists := out[id]; exists {
				candidate = *existing
			}
			if extractFieldsFromAlbumArray(arr, &candidate) {
				out[id] = &candidate
				return
			}
		}
	}

	for _, child := range arr {
		walkAlbumEnvelope(child, out)
	}
}

func walkAlbumPayload(node any, out map[string]*db.Album) {
	arr, ok := node.([]any)
	if !ok {
		return
	}

	if len(arr) >= 2 {
		if id, ok := arr[0].(string); ok && reMediaKey.MatchString(id) {
			candidate := db.Album{
				ID:         id,
				ProductURL: "https://photos.google.com/album/" + id,
			}
			if existing, exists := out[id]; exists {
				candidate = *existing
			}
			if extractFieldsFromAlbumArray(arr, &candidate) {
				out[id] = &candidate
			}
		}
	}

	for _, child := range arr {
		walkAlbumPayload(child, out)
	}
}

func extractFieldsFromAlbumArray(arr []any, album *db.Album) bool {
	hasExt := hasAlbumExtension(arr)
	hasVideoExt := false
	for _, child := range arr {
		if m, ok := child.(map[string]any); ok {
			if _, exists := m["76647426"]; exists {
				hasVideoExt = true
			}
		}
	}
	if hasVideoExt && !hasExt {
		return false
	}

	// 相片節點的 arr[2] 為單一拍攝毫秒時間戳；相簿元組的 arr[2] 通常為封面陣列或標題字串。
	// 若為疑似單張相片且無相簿擴充欄位，直接拒絕，避免將相片誤判為相簿
	// （例如相簿內頁 payload 中每張相片陣列旁夾帶父相簿標題字串時）。
	isLikelySinglePhoto := false
	if len(arr) > 2 && !hasExt {
		if tsMs, ok := toFloat64(arr[2]); ok && isValidTimestampMs(tsMs) {
			isLikelySinglePhoto = true
		}
	}
	if isLikelySinglePhoto {
		return false
	}

	topHasTitle := false
	topHasCount := false
	for i := 1; i < len(arr); i++ {
		switch v := arr[i].(type) {
		case string:
			if isCandidateAlbumTitle(v) && album.Title == "" {
				album.Title = strings.TrimSpace(v)
				topHasTitle = true
			}
		case float64, int, int64:
			if n, ok := toInt(v); ok && n > 0 && n < 100000 && album.ItemCount == 0 && !isLikelySinglePhoto {
				album.ItemCount = n
				topHasCount = true
			}
		}
	}

	scanAlbumSubtree(arr, album)

	// 相簿內頁相片節點夾帶父相簿上下文時，arr[1] 為 [url, w, h] 相片元組，
	// 且標題與數量皆僅來自 72930366 擴充（頂層自帶皆無）：一律視為相片拒絕，
	// 否則每張相片都會產生一筆同名同數量的重複相簿。
	if isPhotoMediaTuple(firstElem(arr)) && !topHasTitle && !topHasCount {
		return false
	}

	// 驗收門檻：必須具備相簿擴充欄位，或同時具備標題與（數量或時間區間）。
	// 僅有標題（例如相片陣列旁夾帶的父相簿標題、錯誤頁文字）一律拒絕，
	// 避免中斷後重跑時每張相片都產生一筆同名相簿造成重複。
	if hasExt || (album.Title != "" && (album.ItemCount > 0 || (album.StartTime > 0 && album.EndTime > 0))) {
		rawBytes, _ := json.Marshal(arr)
		if len(rawBytes) < 4096 {
			album.RawMetadata = string(rawBytes)
		}
		return true
	}
	return false
}

func firstElem(arr []any) any {
	if len(arr) > 1 {
		return arr[1]
	}
	return nil
}

// isPhotoMediaTuple 判斷是否為相片元組 [url, w, h, ...]（相簿內頁相片節點的 arr[1] 形狀）。
// 真實相簿節點的 arr[1] 為封面巢狀陣列 [[url, w, h]]、純網址 [url] 或標題字串，不符合此形狀。
func isPhotoMediaTuple(v any) bool {
	t, ok := v.([]any)
	if !ok || len(t) < 3 {
		return false
	}
	u, ok := t[0].(string)
	if !ok || !isGoogleMediaURL(u) {
		return false
	}
	w, ok1 := toInt(t[1])
	h, ok2 := toInt(t[2])
	return ok1 && ok2 && w > 0 && w < 50000 && h > 0 && h < 50000
}

func scanAlbumSubtree(node any, album *db.Album) {
	switch v := node.(type) {
	case string:
		if isGoogleMediaURL(v) && album.CoverBaseURL == "" {
			parts := strings.Split(v, "=")
			album.CoverBaseURL = parts[0]
		}
	case []any:
		if len(v) == 2 && album.StartTime == 0 && album.EndTime == 0 {
			t1, ok1 := toFloat64(v[0])
			t2, ok2 := toFloat64(v[1])
			if ok1 && ok2 && isValidTimestampMs(t1) && isValidTimestampMs(t2) {
				start := int64(t1 / 1000)
				end := int64(t2 / 1000)
				if start > end {
					start, end = end, start
				}
				album.StartTime = start
				album.EndTime = end
			}
		}
		for _, child := range v {
			scanAlbumSubtree(child, album)
		}
	case map[string]any:
		if albArr, ok := v["72930366"].([]any); ok {
			for _, elem := range albArr {
				switch ev := elem.(type) {
				case string:
					if isCandidateAlbumTitle(ev) && album.Title == "" {
						album.Title = strings.TrimSpace(ev)
					}
				case float64, int, int64:
					if n, ok := toInt(ev); ok && n > 0 && n < 100000 && album.ItemCount == 0 {
						album.ItemCount = n
					}
				}
			}
		}
		for _, child := range v {
			scanAlbumSubtree(child, album)
		}
	}
}
