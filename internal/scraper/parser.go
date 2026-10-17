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
		if id, ok := arr[0].(string); ok && reMediaKey.MatchString(id) {
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
		if id, ok := arr[0].(string); ok && reMediaKey.MatchString(id) {
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
