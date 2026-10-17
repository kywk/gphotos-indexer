package scraper

import (
	"bufio"
	"context"
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

	chromedp.ListenTarget(taskCtx, func(ev any) {
		if res, ok := ev.(*network.EventResponseReceived); ok {
			if strings.Contains(res.Response.URL, "batchexecute") {
				reqID := res.RequestID
				go func() {
					var body []byte
					err := chromedp.Run(taskCtx, chromedp.ActionFunc(func(c context.Context) error {
						var err error
						body, err = network.GetResponseBody(reqID).Do(c)
						return err
					}))
					if err == nil && len(body) > 0 {
						items := ParseBatchExecute(string(body))
						for _, it := range items {
							r.processItem(it)
						}
					}
				}()
			}
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
			r.processItem(it)
		}
	}

	for step := 0; step < 300; step++ {
		if r.shouldStop() {
			break
		}

		r.extractVisibleDOM(taskCtx)

		_ = chromedp.Run(taskCtx, chromedp.Evaluate(
			"window.scrollBy({ top: window.innerHeight * 0.85, behavior: 'smooth' });",
			nil,
		))

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
}

func (r *Runner) extractVisibleDOM(ctx context.Context) {
	var domItems []domPhoto
	js := "(() => {" +
		"const links = Array.from(document.querySelectorAll('a[href*=\"/photo/AF1Qip\"]'));" +
		"return links.map(a => {" +
		"  let thumb = '';" +
		"  const img = a.querySelector('img');" +
		"  if (img && img.src && img.src.includes('googleusercontent.com')) {" +
		"    thumb = img.src;" +
		"  } else {" +
		"    const bg = a.querySelector('[style*=\"googleusercontent.com\"]');" +
		"    if (bg) {" +
		"      const m = bg.style.backgroundImage.match(/url\\([\"']?(https:\\/\\/[^\"')]+)[\"']?\\)/);" +
		"      if (m) thumb = m[1];" +
		"    }" +
		"  }" +
		"  return {" +
		"    href: a.getAttribute('href') || ''," +
		"    thumbUrl: thumb," +
		"    ariaLabel: a.getAttribute('aria-label') || ''" +
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
		r.processItem(db.MediaItem{
			ID:          id,
			ProductURL:  "https://photos.google.com/photo/" + id,
			BaseURL:     base,
			Description: d.AriaLabel,
			MediaType:   "photo",
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
	if limit > 50 {
		limit = 50
	}

	jsFilename := "(() => {" +
		"const labels = Array.from(document.querySelectorAll('[aria-label]')).map(el => el.getAttribute('aria-label'));" +
		"const fn = labels.find(l => l && (l.startsWith('Filename:') || l.startsWith('檔案名稱：'))) || '';" +
		"return fn.replace(/^(Filename:|檔案名稱：)\\s*/, '');" +
		"})()"

	jsCamera := "(() => {" +
		"const labels = Array.from(document.querySelectorAll('[aria-label]')).map(el => el.getAttribute('aria-label'));" +
		"const cam = labels.find(l => l && (l.startsWith('Camera:') || l.startsWith('相機：'))) || '';" +
		"return cam.replace(/^(Camera:|相機：)\\s*/, '');" +
		"})()"

	for i := 0; i < limit; i++ {
		var currentURL, filename, camera string
		_ = chromedp.Run(ctx,
			chromedp.Location(&currentURL),
			chromedp.Evaluate(jsFilename, &filename),
			chromedp.Evaluate(jsCamera, &camera),
		)

		if idx := strings.Index(currentURL, "/photo/"); idx != -1 {
			id := strings.TrimPrefix(currentURL[idx:], "/photo/")
			if qIdx := strings.Index(id, "?"); qIdx != -1 {
				id = id[:qIdx]
			}
			if reMediaKey.MatchString(id) && (filename != "" || camera != "") {
				_ = r.store.Upsert(db.MediaItem{
					ID:          id,
					Filename:    filename,
					CameraModel: camera,
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

func (r *Runner) processItem(item db.MediaItem) {
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
	}

	if !hasThumb && item.BaseURL != "" {
		relPath, err := r.downloadThumb(item.ID, item.BaseURL)
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

func (r *Runner) downloadThumb(id, baseURL string) (string, error) {
	thumbURL := baseURL + "=w300-h300-c-rw"
	req, err := http.NewRequest("GET", thumbURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bad status: %d", resp.StatusCode)
	}

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
