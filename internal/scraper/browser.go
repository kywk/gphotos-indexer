package scraper

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/local/gphotos-indexer/internal/db"
)

type Config struct {
	DataDir      string
	Headless     bool
	MaxItems     int
	ForceFull    bool
	FetchDetails bool
}

type Runner struct {
	cfg        Config
	store      *db.Store
	httpClient *http.Client
	mu         sync.Mutex
	seenIDs    map[string]bool
	savedCount int
	hitCount   int
}

func NewRunner(cfg Config, store *db.Store) *Runner {
	return &Runner{
		cfg:   cfg,
		store: store,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		seenIDs: make(map[string]bool),
	}
}

func (r *Runner) profileDir() string {
	return filepath.Join(r.cfg.DataDir, "chrome-profile")
}

func (r *Runner) thumbsDir() string {
	return filepath.Join(r.cfg.DataDir, "thumbs")
}

func (r *Runner) Auth(ctx context.Context) error {
	if err := os.MkdirAll(r.profileDir(), 0755); err != nil {
		return err
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.UserDataDir(r.profileDir()),
		chromedp.Flag("headless", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1280, 900),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()

	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()

	log.Println("🌐 正在啟動 Chrome 視窗，請在視窗中登入 Google Photos...")
	if err := chromedp.Run(taskCtx, chromedp.Navigate("https://photos.google.com")); err != nil {
		return fmt.Errorf("navigate failed: %w", err)
	}

	fmt.Println("\n========================================================")
	fmt.Println("👉 請在彈出的 Chrome 視窗完成 Google 登入。")
	fmt.Println("👉 確認畫面出現你的照片時間軸後，回到這裡按下 [Enter] 鍵結束並保存狀態...")
	fmt.Println("========================================================")
	_, _ = bufio.NewReader(os.Stdin).ReadBytes('\n')

	log.Println("✅ 登入 Session 已成功保存至:", r.profileDir())
	return nil
}

func (r *Runner) Sync(ctx context.Context) error {
	if err := os.MkdirAll(r.thumbsDir(), 0755); err != nil {
		return err
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.UserDataDir(r.profileDir()),
		chromedp.Flag("headless", r.cfg.Headless),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1440, 1000),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()

	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()

	var batchMu sync.Mutex
	batchReqs := make(map[network.RequestID]bool)

	chromedp.ListenTarget(taskCtx, func(ev any) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			if strings.Contains(e.Request.URL, "batchexecute") {
				batchMu.Lock()
				batchReqs[e.RequestID] = true
				batchMu.Unlock()
			}
		case *network.EventLoadingFinished:
			batchMu.Lock()
			isBatch := batchReqs[e.RequestID]
			delete(batchReqs, e.RequestID)
			batchMu.Unlock()

			if isBatch {
				go func(reqID network.RequestID) {
					var body []byte
					err := chromedp.Run(taskCtx, chromedp.ActionFunc(func(c context.Context) error {
						var err error
						body, err = network.GetResponseBody(reqID).Do(c)
						return err
					}))
					if err == nil && len(body) > 0 {
						items := ParseBatchExecute(string(body))
						for _, it := range items {
							r.processItem(taskCtx, it)
						}
					}
				}(e.RequestID)
			}
		case *network.EventLoadingFailed:
			batchMu.Lock()
			delete(batchReqs, e.RequestID)
			batchMu.Unlock()
		}
	})

	log.Println("🚀 啟動同步程序，正在載入 https://photos.google.com ...")
	if err := chromedp.Run(taskCtx,
		network.Enable(),
		chromedp.Navigate("https://photos.google.com"),
		chromedp.Sleep(4*time.Second),
	); err != nil {
		return fmt.Errorf("initial navigate: %w", err)
	}

	var initScripts []string
	_ = chromedp.Run(taskCtx, chromedp.Evaluate(
		"Array.from(document.querySelectorAll('script')).map(s => s.textContent).filter(t => t.includes('AF1Qip'))",
		&initScripts,
	))
	for _, scriptTxt := range initScripts {
		for _, it := range ParseBatchExecute(scriptTxt) {
			r.processItem(taskCtx, it)
		}
	}

	type scrollResult struct {
		AtBottom bool `json:"atBottom"`
	}

	consecutiveBottom := 0
	lastSavedCount := 0
	stalledSteps := 0

	for step := 0; step < 300; step++ {
		if r.shouldStop() {
			break
		}

		r.extractVisibleDOM(taskCtx)

		var sRes scrollResult
		_ = chromedp.Run(taskCtx, chromedp.Evaluate(`(() => {
			const candidates = Array.from(document.querySelectorAll('*')).filter(el => {
				const s = window.getComputedStyle(el);
				return (s.overflowY === 'scroll' || s.overflowY === 'auto') && el.scrollHeight > el.clientHeight;
			});
			candidates.sort((a, b) => b.scrollHeight - a.scrollHeight);
			let atBottom = false;
			if (candidates.length > 0) {
				const target = candidates[0];
				target.scrollBy({ top: target.clientHeight * 0.85, behavior: 'smooth' });
				atBottom = (target.scrollTop + target.clientHeight >= target.scrollHeight - 50);
			}
			window.scrollBy({ top: window.innerHeight * 0.85, behavior: 'smooth' });
			return { atBottom: atBottom };
		})()`, &sRes))

		r.mu.Lock()
		currentSaved := r.savedCount
		r.mu.Unlock()

		if currentSaved == lastSavedCount {
			stalledSteps++
		} else {
			stalledSteps = 0
			lastSavedCount = currentSaved
		}

		if sRes.AtBottom {
			consecutiveBottom++
			if consecutiveBottom >= 5 && stalledSteps >= 5 {
				log.Printf("⏹ 已抵達相簿時間軸最底部，無更多照片。")
				break
			}
		} else {
			consecutiveBottom = 0
		}

		if (step+1)%20 == 0 && stalledSteps >= 10 {
			log.Printf("⏳ 正在向下捲動時間軸 (第 %d 次捲動，已索引 %d 張)...", step+1, currentSaved)
		}

		jitter := time.Duration(650+rand.Intn(550)) * time.Millisecond
		time.Sleep(jitter)
	}

	if r.cfg.FetchDetails {
		log.Println("🔍 正在執行單張詳情巡覽以補齊原始檔名與 EXIF...")
		r.enrichDetailsViaUI(taskCtx)
	}

	total, _ := r.store.Count()
	log.Printf("🎉 同步完成！本次新增/更新 %d 張照片，本地資料庫共索引 %d 張照片。", r.savedCount, total)
	return nil
}

type domPhoto struct {
	Href      string `json:"href"`
	ThumbURL  string `json:"thumbUrl"`
	AriaLabel string `json:"ariaLabel"`
	MediaType string `json:"mediaType"`
	TakenAt   int64  `json:"takenAt"`
}

func (r *Runner) extractVisibleDOM(ctx context.Context) {
	var domItems []domPhoto
	js := "(() => {" +
		"const links = Array.from(document.querySelectorAll('a[href*=\"/photo/AF1Qip\"]'));" +
		"return links.map(a => {" +
		"  let thumb = '';" +
		"  const img = a.querySelector('img');" +
		"  if (img && img.src && (img.src.includes('googleusercontent.com') || img.src.includes('usercontent.google.com') || img.src.includes('ggpht.com'))) {" +
		"    thumb = img.src;" +
		"  } else {" +
		"    const bgEl = a.querySelector('[data-latest-bg]');" +
		"    if (bgEl && bgEl.getAttribute('data-latest-bg')) {" +
		"      thumb = bgEl.getAttribute('data-latest-bg');" +
		"    } else {" +
		"      const all = Array.from(a.querySelectorAll('*'));" +
		"      for (const el of all) {" +
		"        const bg = window.getComputedStyle(el).backgroundImage;" +
		"        if (bg && (bg.includes('googleusercontent.com') || bg.includes('usercontent.google.com'))) {" +
		"          const m = bg.match(/url\\([\"']?(https:\\/\\/[^\"')]+)[\"']?\\)/);" +
		"          if (m) { thumb = m[1]; break; }" +
		"        }" +
		"      }" +
		"    }" +
		"  }" +
		"  const ariaLabel = a.getAttribute('aria-label') || '';" +
		"  let mediaType = 'photo';" +
		"  const lowerLabel = ariaLabel.toLowerCase();" +
		"  if (lowerLabel.includes('video') || ariaLabel.includes('影片') || ariaLabel.includes('視頻')) {" +
		"    mediaType = 'video';" +
		"  }" +
		"  let takenAt = 0;" +
		"  const cleanLabel = ariaLabel.replace(/\\u202f/g, ' ');" +
		"  const enMatch = cleanLabel.match(/([A-Z][a-z]{2}\\s+\\d{1,2},\\s+\\d{4},?\\s+\\d{1,2}:\\d{2}:\\d{2}\\s*[AP]M)/i);" +
		"  if (enMatch) {" +
		"    const t = Date.parse(enMatch[1]);" +
		"    if (!isNaN(t) && t > 0) takenAt = Math.floor(t / 1000);" +
		"  }" +
		"  const zhMatch = cleanLabel.match(/(\\d{4})年(\\d{1,2})月(\\d{1,2})日(?:\\s*(上午|下午)?(\\d{1,2}):(\\d{2}):(\\d{2}))?/);" +
		"  if (zhMatch && takenAt === 0) {" +
		"    let year = parseInt(zhMatch[1]);" +
		"    let month = parseInt(zhMatch[2]) - 1;" +
		"    let day = parseInt(zhMatch[3]);" +
		"    let hour = zhMatch[5] ? parseInt(zhMatch[5]) : 0;" +
		"    let min = zhMatch[6] ? parseInt(zhMatch[6]) : 0;" +
		"    let sec = zhMatch[7] ? parseInt(zhMatch[7]) : 0;" +
		"    if (zhMatch[4] === '下午' && hour < 12) hour += 12;" +
		"    if (zhMatch[4] === '上午' && hour === 12) hour = 0;" +
		"    const d = new Date(year, month, day, hour, min, sec);" +
		"    if (!isNaN(d.getTime())) takenAt = Math.floor(d.getTime() / 1000);" +
		"  }" +
		"  return {" +
		"    href: a.getAttribute('href') || ''," +
		"    thumbUrl: thumb," +
		"    ariaLabel: ariaLabel," +
		"    mediaType: mediaType," +
		"    takenAt: takenAt" +
		"  };" +
		"});" +
		"})()"

	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &domItems)); err != nil {
		return
	}

	for _, d := range domItems {
		idx := strings.Index(d.Href, "/photo/")
		if idx == -1 {
			continue
		}
		id := strings.TrimPrefix(d.Href[idx:], "/photo/")
		if qIdx := strings.Index(id, "?"); qIdx != -1 {
			id = id[:qIdx]
		}
		if !reMediaKey.MatchString(id) {
			continue
		}

		base := ""
		if d.ThumbURL != "" {
			base = strings.Split(d.ThumbURL, "=")[0]
		}
		r.processItem(ctx, db.MediaItem{
			ID:          id,
			ProductURL:  "https://photos.google.com/photo/" + id,
			BaseURL:     base,
			Description: d.AriaLabel,
			MediaType:   d.MediaType,
			TakenAt:     d.TakenAt,
		})
	}
}

func (r *Runner) enrichDetailsViaUI(ctx context.Context) {
	_ = chromedp.Run(ctx,
		chromedp.Evaluate("window.scrollTo(0, 0)", nil),
		chromedp.Sleep(1*time.Second),
		chromedp.Click("a[href*=\"/photo/AF1Qip\"]", chromedp.ByQuery),
		chromedp.Sleep(1500*time.Millisecond),
		chromedp.KeyEvent("i"),
		chromedp.Sleep(1*time.Second),
	)

	limit := r.cfg.MaxItems
	if limit <= 0 {
		limit = 200
	}

	jsExtract := `(() => {
		const info = { filename: '', camera: '', width: 0, height: 0 };
		const labels = Array.from(document.querySelectorAll('[aria-label]')).map(el => el.getAttribute('aria-label')).filter(Boolean);

		for (const l of labels) {
			const fnMatch = l.match(/^(?:Filename|檔案名稱|文件名|檔名)\s*[:：]\s*(.+)/i);
			if (fnMatch && !info.filename) info.filename = fnMatch[1].trim();

			const camMatch = l.match(/^(?:Camera|相機|設備)\s*[:：]\s*(.+)/i);
			if (camMatch && !info.camera) info.camera = camMatch[1].trim();

			const sizeMatch = l.match(/(?:Size|尺寸|大小)?\s*[:：]?\s*(\d+)\s*[×x]\s*(\d+)\s*(?:pixels|像素)?/i);
			if (sizeMatch && !info.width) {
				info.width = parseInt(sizeMatch[1], 10);
				info.height = parseInt(sizeMatch[2], 10);
			}
		}

		if (!info.filename) {
			const reFile = /([^\s/\\:*?"<>|]+\.(mp4|mov|mkv|webm|m4v|3gp|avi|jpg|jpeg|png|heic|webp))/i;
			const m = document.body.innerText.match(reFile);
			if (m) info.filename = m[1].trim();
		}

		return JSON.stringify(info);
	})()`

	type detailResult struct {
		Filename string `json:"filename"`
		Camera   string `json:"camera"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
	}

	for i := 0; i < limit; i++ {
		var currentURL, rawJSON string
		_ = chromedp.Run(ctx,
			chromedp.Location(&currentURL),
			chromedp.Evaluate(jsExtract, &rawJSON),
		)

		var res detailResult
		_ = json.Unmarshal([]byte(rawJSON), &res)

		if idx := strings.Index(currentURL, "/photo/"); idx != -1 {
			id := strings.TrimPrefix(currentURL[idx:], "/photo/")
			if qIdx := strings.Index(id, "?"); qIdx != -1 {
				id = id[:qIdx]
			}
			if reMediaKey.MatchString(id) && (res.Filename != "" || res.Camera != "" || res.Width > 0) {
				mediaType := ""
				if isVideoFilename(res.Filename) {
					mediaType = "video"
				}
				_ = r.store.Upsert(db.MediaItem{
					ID:          id,
					Filename:    res.Filename,
					CameraModel: res.Camera,
					Width:       res.Width,
					Height:      res.Height,
					MediaType:   mediaType,
				})
			}
		}

		_ = chromedp.Run(ctx,
			chromedp.KeyEvent("\u001b[C"),
			chromedp.Evaluate(
				"document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', code: 'ArrowRight', keyCode: 39, bubbles: true }));",
				nil,
			),
			chromedp.Sleep(time.Duration(450+rand.Intn(350))*time.Millisecond),
		)
	}
}

func (r *Runner) processItem(ctx context.Context, item db.MediaItem) {
	if item.ID == "" {
		return
	}

	r.mu.Lock()
	if r.cfg.MaxItems > 0 && r.savedCount >= r.cfg.MaxItems {
		r.mu.Unlock()
		return
	}

	alreadySeen := r.seenIDs[item.ID]
	r.seenIDs[item.ID] = true
	r.mu.Unlock()

	hasThumb := r.store.HasThumb(item.ID)
	if hasThumb && !r.cfg.ForceFull && !alreadySeen {
		r.mu.Lock()
		r.hitCount++
		r.mu.Unlock()
	} else if !hasThumb && !alreadySeen {
		r.mu.Lock()
		r.hitCount = 0
		r.mu.Unlock()
	}

	if !hasThumb && item.BaseURL != "" {
		relPath, err := r.downloadThumb(ctx, item.ID, item.BaseURL)
		if err == nil {
			item.ThumbPath = relPath
		}
	}

	if err := r.store.Upsert(item); err != nil {
		log.Printf("⚠️ 寫入資料庫失敗 (%s): %v", item.ID, err)
		return
	}

	if !alreadySeen {
		r.mu.Lock()
		r.savedCount++
		count := r.savedCount
		r.mu.Unlock()
		if count%10 == 0 || count == 1 {
			log.Printf("📥 已索引 %d 張相片 (最新 ID: %s, 檔名: %s)", count, item.ID[:14]+"...", item.Filename)
		}
	}
}

func (r *Runner) shouldStop() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cfg.MaxItems > 0 && r.savedCount >= r.cfg.MaxItems {
		log.Printf("⏹ 已達到設定的抓取上限 (%d 張)，停止捲動。", r.cfg.MaxItems)
		return true
	}
	if !r.cfg.ForceFull && r.hitCount >= 30 {
		log.Printf("⏹ 連續遇到 %d 張已索引的歷史相片 (Watermark 命中)，增量同步完成。", r.hitCount)
		return true
	}
	return false
}

func (r *Runner) downloadThumb(ctx context.Context, id, baseURL string) (string, error) {
	if strings.Contains(baseURL, "usercontent.google.com") && !strings.Contains(baseURL, "lh3.googleusercontent.com") {
		return r.downloadThumbViaBrowser(ctx, id, baseURL)
	}

	thumbURL := baseURL + "=w300-h300-c-rw"
	req, err := http.NewRequest("GET", thumbURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := r.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return r.downloadThumbViaBrowser(ctx, id, baseURL)
	}
	defer resp.Body.Close()

	filename := id + ".webp"
	fullPath := filepath.Join(r.thumbsDir(), filename)
	f, err := os.Create(fullPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join("thumbs", filename)), nil
}

func (r *Runner) downloadThumbViaBrowser(ctx context.Context, id, baseURL string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("no browser context available")
	}
	thumbURL := baseURL + "=w300-h300-c-rw?authuser=0"
	js := fmt.Sprintf(`new Promise((resolve) => {
		fetch("%s", { credentials: "include" })
			.then(r => {
				if (!r.ok) return resolve("");
				return r.blob();
			})
			.then(b => {
				if (!b) return resolve("");
				const reader = new FileReader();
				reader.onloadend = () => resolve(reader.result || "");
				reader.onerror = () => resolve("");
				reader.readAsDataURL(b);
			})
			.catch(() => resolve(""));
	})`, thumbURL)

	var dataURL string
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		val, _, err := runtime.Evaluate(js).WithAwaitPromise(true).Do(c)
		if err != nil {
			return err
		}
		if val != nil && val.Value != nil {
			_ = json.Unmarshal(val.Value, &dataURL)
		}
		return nil
	}))
	if err != nil {
		return "", err
	}

	comma := strings.Index(dataURL, ",")
	if comma == -1 {
		return "", fmt.Errorf("failed to fetch thumbnail via browser: %s", baseURL)
	}

	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil {
		return "", fmt.Errorf("decode thumbnail base64: %w", err)
	}

	filename := id + ".webp"
	fullPath := filepath.Join(r.thumbsDir(), filename)
	if err := os.WriteFile(fullPath, raw, 0644); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join("thumbs", filename)), nil
}
