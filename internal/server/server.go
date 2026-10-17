package server

import (
	_ "embed"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/local/gphotos-indexer/internal/db"
)

//go:embed index.html
var indexHTML []byte

type Server struct {
	store   *db.Store
	dataDir string
	port    int
}

func New(store *db.Store, dataDir string, port int) *Server {
	return &Server{
		store:   store,
		dataDir: dataDir,
		port:    port,
	}
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	thumbsDir := filepath.Join(s.dataDir, "thumbs")
	mux.Handle("GET /thumbs/", http.StripPrefix("/thumbs/", http.FileServer(http.Dir(thumbsDir))))
	mux.HandleFunc("GET /api/photos", s.handleSearch)
	mux.HandleFunc("GET /", s.handleIndex)

	addr := fmt.Sprintf(":%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("監聽埠號失敗 %s: %w", addr, err)
	}
	defer ln.Close()

	actualPort := ln.Addr().(*net.TCPAddr).Port
	log.Printf("⚡ 本地照片極速檢索介面已啟動: http://localhost:%d", actualPort)

	httpServer := &http.Server{
		Handler: mux,
	}

	errChan := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Println("\n🛑 接收到中斷訊號 (Ctrl+C)，正在關閉 Web 伺服器...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			return err
		}
		log.Println("👋 Web 伺服器已安全結束。")
		return nil
	case err := <-errChan:
		return err
	}
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	var dateFrom, dateTo int64
	if df := q.Get("date_from"); df != "" {
		if t, err := time.Parse("2006-01-02", df); err == nil {
			dateFrom = t.Unix()
		}
	}
	if dt := q.Get("date_to"); dt != "" {
		if t, err := time.Parse("2006-01-02", dt); err == nil {
			dateTo = t.Add(23*time.Hour + 59*time.Minute + 59*time.Second).Unix()
		}
	}

	items, total, err := s.store.Search(db.SearchFilter{
		Query:     q.Get("q"),
		MediaType: q.Get("type"),
		DateFrom:  dateFrom,
		DateTo:    dateTo,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total": total,
		"items": items,
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}
