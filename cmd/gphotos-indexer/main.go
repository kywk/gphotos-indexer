package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/local/gphotos-indexer/internal/db"
	"github.com/local/gphotos-indexer/internal/scaffold"
	"github.com/local/gphotos-indexer/internal/scraper"
	"github.com/local/gphotos-indexer/internal/server"
)

const Version = "v0.2.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcmd := os.Args[1]
	if subcmd == "version" || subcmd == "-v" || subcmd == "--version" {
		fmt.Printf("gphotos-indexer %s\n", Version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		stop() // 恢復預設信號行為，若用戶再次按下 Ctrl+C 則強制退出
	}()

	switch subcmd {
	case "init":
		fs := flag.NewFlagSet("init", flag.ExitOnError)
		dir := fs.String("dir", "", "目標工作目錄路徑 (預設為當前目錄或第一個位置參數)")
		force := fs.Bool("force", false, "強制覆寫既有檔案")
		_ = fs.Parse(os.Args[2:])

		targetDir := *dir
		if targetDir == "" && fs.NArg() > 0 {
			targetDir = fs.Arg(0)
		}
		if targetDir == "" {
			targetDir = "."
		}

		if err := scaffold.Run(scaffold.Config{
			TargetDir: targetDir,
			Force:     *force,
		}); err != nil {
			log.Fatalf("Init 失敗: %v", err)
		}

	case "auth":
		fs := flag.NewFlagSet("auth", flag.ExitOnError)
		dataDir := fs.String("data", "./data", "資料與 Chrome Profile 儲存目錄")
		_ = fs.Parse(os.Args[2:])

		store := mustOpenDB(*dataDir)
		defer store.Close()

		runner := scraper.NewRunner(scraper.Config{DataDir: *dataDir, Headless: false}, store)
		if err := runner.Auth(ctx); err != nil {
			log.Fatalf("Auth 失敗: %v", err)
		}

	case "sync":
		fs := flag.NewFlagSet("sync", flag.ExitOnError)
		dataDir := fs.String("data", "./data", "資料與 SQLite 儲存目錄")
		headless := fs.Bool("headless", true, "是否以 Headless 模式背景執行")
		maxItems := fs.Int("max-items", 200, "單次同步最大抓取張數 (0 為不限制)")
		forceFull := fs.Bool("force-full", false, "忽略 Watermark 強制向下全庫掃描")
		fetchDetails := fs.Bool("fetch-details", false, "自動開啟資訊欄巡覽以補齊原始檔名與相機 EXIF")
		_ = fs.Parse(os.Args[2:])

		store := mustOpenDB(*dataDir)
		defer store.Close()

		runner := scraper.NewRunner(scraper.Config{
			DataDir:      *dataDir,
			Headless:     *headless,
			MaxItems:     *maxItems,
			ForceFull:    *forceFull,
			FetchDetails: *fetchDetails,
		}, store)

		if err := runner.Sync(ctx); err != nil {
			log.Fatalf("Sync 失敗: %v", err)
		}

	case "albums":
		fs := flag.NewFlagSet("albums", flag.ExitOnError)
		dataDir := fs.String("data", "./data", "資料與 SQLite 儲存目錄")
		headless := fs.Bool("headless", true, "是否以 Headless 模式背景執行")
		maxAlbums := fs.Int("max-albums", 0, "單次最大抓取相簿數 (0 為不限制)")
		maxItems := fs.Int("max-items", 0, "單本相簿內最大抓取張數 (0 為不限制)")
		forceFull := fs.Bool("force-full", false, "忽略 Watermark 強制對每本相簿全量掃描")
		fetchDetails := fs.Bool("fetch-details", false, "自動開啟資訊欄巡覽以補齊原始檔名與相機 EXIF")
		_ = fs.Parse(os.Args[2:])

		store := mustOpenDB(*dataDir)
		defer store.Close()

		runner := scraper.NewRunner(scraper.Config{
			DataDir:      *dataDir,
			Headless:     *headless,
			MaxAlbums:    *maxAlbums,
			MaxItems:     *maxItems,
			ForceFull:    *forceFull,
			FetchDetails: *fetchDetails,
		}, store)

		if err := runner.SyncAlbums(ctx); err != nil {
			log.Fatalf("Albums 同步失敗: %v", err)
		}

	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		dataDir := fs.String("data", "./data", "資料與 SQLite 儲存目錄")
		port := fs.Int("port", 8080, "本地 Web UI 監聽埠號")
		_ = fs.Parse(os.Args[2:])

		store := mustOpenDB(*dataDir)
		defer store.Close()

		srv := server.New(store, *dataDir, *port)
		if err := srv.Start(ctx); err != nil {
			log.Fatalf("Server 異常終止: %v", err)
		}

	default:
		printUsage()
		os.Exit(1)
	}
}

func mustOpenDB(dataDir string) *db.Store {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("無法建立資料目錄: %v", err)
	}
	dbPath := filepath.Join(dataDir, "index.db")
	store, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("無法開啟 SQLite 資料庫: %v", err)
	}
	return store
}

func printUsage() {
	fmt.Printf("gphotos-indexer %s — Google Photos 本地縮圖與極速檢索工具\n", Version)
	fmt.Println("")
	fmt.Println("用法:")
	fmt.Println("  gphotos-indexer <command> [flags]")
	fmt.Println("")
	fmt.Println("可用子命令:")
	fmt.Println("  init    初始化新的相片索引工作目錄 (產生 Makefile, .gitignore 與 README)")
	fmt.Println("  auth    啟動有頭 Chrome 視窗完成 Google 帳號登入並保存 Session")
	fmt.Println("  sync    攔截 batchexecute 與 DOM，自動抓取 WebP 縮圖與 Metadata 存入 SQLite")
	fmt.Println("  albums  針對所有個人相簿建立索引（包含相簿清單與相簿內相片關聯）")
	fmt.Println("  serve   啟動本地極速搜尋 Web UI (點擊直接跳轉 Google Photos 雲端原圖)")
	fmt.Println("  version 顯示版本資訊")
}
