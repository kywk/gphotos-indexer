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
	MaxAlbums    int
	MaxItems     int
	ForceFull    bool
	FetchDetails bool
}

type Runner struct {
	cfg            Config
	store          *db.Store
	httpClient     *http.Client
	mu             sync.Mutex
	seenIDs        map[string]bool
	savedCount     int
	hitCount       int
	seenAlbums     map[string]db.Album
	albumOrder     []string
	currentAlbumID string
}

func NewRunner(cfg Config, store *db.Store) *Runner {
	return &Runner{
		cfg:   cfg,
		store: store,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		seenIDs:    make(map[string]bool),
		seenAlbums: make(map[string]db.Album),
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

func (r *Runner) SyncAlbums(ctx context.Context) error {
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
						raw := string(body)
						r.mu.Lock()
						curAlb := r.currentAlbumID
						r.mu.Unlock()

						if curAlb == "" {
							for _, alb := range ParseAlbumsBatchExecute(raw) {
								r.processAlbum(taskCtx, alb)
							}
						} else {
							for _, alb := range ParseAlbumsBatchExecute(raw) {
								if alb.ID == curAlb {
									r.processAlbum(taskCtx, alb)
								}
							}
							for _, it := range ParseBatchExecute(raw) {
								if it.ID != curAlb {
									r.processItem(taskCtx, it)
								}
							}
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

	log.Println("🚀 啟動相簿索引程序，正在載入 https://photos.google.com/albums ...")
	if err := chromedp.Run(taskCtx,
		network.Enable(),
		chromedp.Navigate("https://photos.google.com/albums"),
		chromedp.Sleep(4*time.Second),
	); err != nil {
		return fmt.Errorf("initial navigate to albums: %w", err)
	}

	var initScripts []string
	_ = chromedp.Run(taskCtx, chromedp.Evaluate(
		"Array.from(document.querySelectorAll('script')).map(s => s.textContent).filter(t => t.includes('AF1Qip'))",
		&initScripts,
	))
	for _, scriptTxt := range initScripts {
		for _, alb := range ParseAlbumsBatchExecute(scriptTxt) {
			r.processAlbum(taskCtx, alb)
		}
	}

	type scrollResult struct {
		AtBottom bool `json:"atBottom"`
	}

	consecutiveBottom := 0
	lastAlbumCount := 0
	stalledSteps := 0

	for step := 0; step < 150; step++ {
		r.extractVisibleAlbumsDOM(taskCtx)

		r.mu.Lock()
		currentAlbums := len(r.albumOrder)
		r.mu.Unlock()

		if r.cfg.MaxAlbums > 0 && currentAlbums >= r.cfg.MaxAlbums {
			log.Printf("⏹ 已達到設定的相簿抓取上限 (%d 本)，停止捲動相簿列表。", r.cfg.MaxAlbums)
			break
		}

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

		if currentAlbums == lastAlbumCount {
			stalledSteps++
		} else {
			stalledSteps = 0
			lastAlbumCount = currentAlbums
		}

		if sRes.AtBottom {
			consecutiveBottom++
			if consecutiveBottom >= 4 && stalledSteps >= 4 {
				log.Printf("⏹ 已抵達相簿列表最底部，共發現 %d 本相簿。", currentAlbums)
				break
			}
		} else {
			consecutiveBottom = 0
		}

		jitter := time.Duration(650+rand.Intn(550)) * time.Millisecond
		time.Sleep(jitter)
	}

	r.mu.Lock()
	albums := make([]db.Album, 0, len(r.albumOrder))
	for _, id := range r.albumOrder {
		if r.cfg.MaxAlbums > 0 && len(albums) >= r.cfg.MaxAlbums {
			break
		}
		albums = append(albums, r.seenAlbums[id])
	}
	r.mu.Unlock()

	log.Printf("📚 共收集到 %d 本相簿，開始逐一巡覽建立相簿內相片索引...", len(albums))

	totalLinkedItems := 0
	for idx, alb := range albums {
		if ctx.Err() != nil {
			break
		}

		r.mu.Lock()
		r.currentAlbumID = alb.ID
		r.seenIDs = make(map[string]bool)
		r.savedCount = 0
		r.hitCount = 0
		r.mu.Unlock()

		albumURL := "https://photos.google.com/album/" + alb.ID
		titleDisplay := alb.Title
		if titleDisplay == "" {
			titleDisplay = alb.ID[:14] + "..."
		}
		log.Printf("📂 [%d/%d] 正在進入相簿《%s》(%s)...", idx+1, len(albums), titleDisplay, albumURL)

		if err := chromedp.Run(taskCtx,
			chromedp.Navigate(albumURL),
			chromedp.Sleep(3500*time.Millisecond),
		); err != nil {
			log.Printf("⚠️ 無法開啟相簿 %s: %v", alb.ID, err)
			continue
		}

		r.extractAlbumHeaderDOM(taskCtx, alb.ID)

		var albScripts []string
		_ = chromedp.Run(taskCtx, chromedp.Evaluate(
			"Array.from(document.querySelectorAll('script')).map(s => s.textContent).filter(t => t.includes('AF1Qip'))",
			&albScripts,
		))
		for _, scriptTxt := range albScripts {
			for _, parsedAlb := range ParseAlbumsBatchExecute(scriptTxt) {
				if parsedAlb.ID == alb.ID {
					r.processAlbum(taskCtx, parsedAlb)
				}
			}
			for _, it := range ParseBatchExecute(scriptTxt) {
				if it.ID != alb.ID {
					r.processItem(taskCtx, it)
				}
			}
		}

		albBottom := 0
		albLastSaved := 0
		albStalled := 0

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

			if currentSaved == albLastSaved {
				albStalled++
			} else {
				albStalled = 0
				albLastSaved = currentSaved
			}

			if sRes.AtBottom {
				albBottom++
				if albBottom >= 4 && albStalled >= 4 {
					break
				}
			} else {
				albBottom = 0
			}

			jitter := time.Duration(600+rand.Intn(500)) * time.Millisecond
			time.Sleep(jitter)
		}

		if r.cfg.FetchDetails {
			log.Printf("🔍 正在補齊相簿《%s》內相片的原始檔名與 EXIF...", titleDisplay)
			r.enrichDetailsViaUI(taskCtx)
		}

		_ = r.store.SyncAlbumStats(alb.ID)

		r.mu.Lock()
		totalLinkedItems += r.savedCount
		r.mu.Unlock()
	}

	totalAlbums, _ := r.store.CountAlbums()
	totalPhotos, _ := r.store.Count()
	log.Printf("🎉 相簿索引完成！本次處理 %d 本相簿（關聯 %d 筆相片項目），本地資料庫共索引 %d 本相簿、%d 張相片。",
		len(albums), totalLinkedItems, totalAlbums, totalPhotos)
	return nil
}

type domAlbum struct {
	Href      string `json:"href"`
	ThumbURL  string `json:"thumbUrl"`
	Title     string `json:"title"`
	ItemCount int    `json:"itemCount"`
}

func (r *Runner) extractVisibleAlbumsDOM(ctx context.Context) {
	var domAlbums []domAlbum
	js := `(() => {
		const links = Array.from(document.querySelectorAll('a[href*="/album/AF1Qip"]')).filter(a => {
			const h = a.getAttribute('href') || '';
			return !h.includes('/photo/');
		});
		return links.map(a => {
			const container = a.closest('[role="listitem"]') || a.parentElement || a;
			let thumb = '';
			const img = container.querySelector('img');
			if (img && img.src && (img.src.includes('googleusercontent.com') || img.src.includes('usercontent.google.com') || img.src.includes('ggpht.com'))) {
				thumb = img.src;
			} else {
				const bgEl = container.querySelector('[data-latest-bg]');
				if (bgEl && bgEl.getAttribute('data-latest-bg')) {
					thumb = bgEl.getAttribute('data-latest-bg');
				} else {
					const all = Array.from(container.querySelectorAll('*'));
					for (const el of all) {
						const bg = window.getComputedStyle(el).backgroundImage;
						if (bg && (bg.includes('googleusercontent.com') || bg.includes('usercontent.google.com'))) {
							const m = bg.match(/url\(["']?(https:\/\/[^"')]+)["']?\)/);
							if (m) { thumb = m[1]; break; }
						}
					}
				}
			}

			let title = '';
			let itemCount = 0;
			const rawText = (container.innerText || a.innerText || a.getAttribute('aria-label') || '').trim();
			const lines = rawText.split(/\r?\n/).map(s => s.trim()).filter(Boolean);

			for (const line of lines) {
				const countMatch = line.match(/(\d[\d,]*)\s*(?:個項目|項|張相片|items?|photos?)/i);
				if (countMatch && itemCount === 0) {
					itemCount = parseInt(countMatch[1].replace(/,/g, ''), 10) || 0;
					continue;
				}
				if (!title && !/^(?:已共享|共享|Shared)$/i.test(line)) {
					title = line;
				}
			}

			return {
				href: a.getAttribute('href') || '',
				thumbUrl: thumb,
				title: title,
				itemCount: itemCount
			};
		});
	})()`

	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &domAlbums)); err != nil {
		return
	}

	for _, d := range domAlbums {
		idx := strings.Index(d.Href, "/album/")
		if idx == -1 {
			continue
		}
		id := strings.TrimPrefix(d.Href[idx:], "/album/")
		if qIdx := strings.Index(id, "?"); qIdx != -1 {
			id = id[:qIdx]
		}
		id = strings.TrimSuffix(id, "/")
		if !reMediaKey.MatchString(id) {
			continue
		}

		base := ""
		if d.ThumbURL != "" {
			base = strings.Split(d.ThumbURL, "=")[0]
		}
		r.processAlbum(ctx, db.Album{
			ID:           id,
			Title:        d.Title,
			ProductURL:   "https://photos.google.com/album/" + id,
			CoverBaseURL: base,
			ItemCount:    d.ItemCount,
		})
	}
}

func (r *Runner) extractAlbumHeaderDOM(ctx context.Context, albumID string) {
	var headerInfo struct {
		Title     string `json:"title"`
		ItemCount int    `json:"itemCount"`
	}
	js := `(() => {
		let title = '';
		const titleInput = document.querySelector('input[aria-label*="標題"], input[aria-label*="title" i], textarea[aria-label*="標題"], textarea[aria-label*="title" i]');
		if (titleInput && titleInput.value) {
			title = titleInput.value.trim();
		}
		if (!title) {
			const h1 = document.querySelector('h1, [role="heading"][aria-level="1"]');
			if (h1 && h1.innerText) {
				title = h1.innerText.trim();
			}
		}
		if (!title && document.title) {
			const cleaned = document.title.replace(/\s*[-–—]\s*Google\s*(?:相簿|Photos).*$/i, '').trim();
			if (cleaned && !/^Google\s*(?:相簿|Photos)$/i.test(cleaned)) {
				title = cleaned;
			}
		}
		let itemCount = 0;
		const bodyText = document.body ? (document.body.innerText || '') : '';
		const m = bodyText.match(/(\d[\d,]*)\s*(?:個項目|項|items?)/i);
		if (m) {
			itemCount = parseInt(m[1].replace(/,/g, ''), 10) || 0;
		}
		return { title: title, itemCount: itemCount };
	})()`

	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &headerInfo)); err != nil {
		return
	}
	if headerInfo.Title != "" || headerInfo.ItemCount > 0 {
		r.processAlbum(ctx, db.Album{
			ID:         albumID,
			Title:      headerInfo.Title,
			ProductURL: "https://photos.google.com/album/" + albumID,
			ItemCount:  headerInfo.ItemCount,
		})
	}
}

func (r *Runner) processAlbum(ctx context.Context, album db.Album) {
	if album.ID == "" {
		return
	}

	r.mu.Lock()
	existing, seen := r.seenAlbums[album.ID]
	if !seen {
		if r.cfg.MaxAlbums > 0 && len(r.albumOrder) >= r.cfg.MaxAlbums {
			r.mu.Unlock()
			return
		}
		r.albumOrder = append(r.albumOrder, album.ID)
	} else {
		if album.Title == "" {
			album.Title = existing.Title
		}
		if album.CoverBaseURL == "" {
			album.CoverBaseURL = existing.CoverBaseURL
		}
		if album.CoverThumbPath == "" {
			album.CoverThumbPath = existing.CoverThumbPath
		}
		if album.ItemCount == 0 {
			album.ItemCount = existing.ItemCount
		}
		if album.StartTime == 0 {
			album.StartTime = existing.StartTime
		}
		if album.EndTime == 0 {
			album.EndTime = existing.EndTime
		}
	}
	r.seenAlbums[album.ID] = album
	r.mu.Unlock()

	hasThumb := r.store.HasAlbumThumb(album.ID)
	if !hasThumb && album.CoverBaseURL != "" {
		relPath, err := r.downloadThumb(ctx, album.ID, album.CoverBaseURL)
		if err == nil {
			album.CoverThumbPath = relPath
			r.mu.Lock()
			r.seenAlbums[album.ID] = album
			r.mu.Unlock()
		}
	}

	if err := r.store.UpsertAlbum(album); err != nil {
		log.Printf("⚠️ 寫入相簿資料庫失敗 (%s): %v", album.ID, err)
		return
	}

	if !seen {
		log.Printf("📚 發現相簿: 《%s》 (ID: %s, 預估項目: %d)", album.Title, album.ID[:14]+"...", album.ItemCount)
	}
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
	curAlbumID := r.currentAlbumID
	if curAlbumID != "" && item.ID == curAlbumID {
		r.mu.Unlock()
		return
	}
	if r.cfg.MaxItems > 0 && r.savedCount >= r.cfg.MaxItems {
		r.mu.Unlock()
		return
	}

	alreadySeen := r.seenIDs[item.ID]
	r.seenIDs[item.ID] = true
	r.mu.Unlock()

	hasThumb := r.store.HasThumb(item.ID)
	isFullyIndexed := hasThumb
	if curAlbumID != "" {
		isFullyIndexed = hasThumb && r.store.HasAlbumItem(curAlbumID, item.ID)
	}

	if isFullyIndexed && !r.cfg.ForceFull && !alreadySeen {
		r.mu.Lock()
		r.hitCount++
		r.mu.Unlock()
	} else if !isFullyIndexed && !alreadySeen {
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

	if curAlbumID != "" {
		if err := r.store.AddAlbumItem(curAlbumID, item.ID); err != nil {
			log.Printf("⚠️ 寫入相簿關聯失敗 (%s -> %s): %v", curAlbumID, item.ID, err)
		}
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
