package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type MediaItem struct {
	ID           string  `json:"id"`
	ProductURL   string  `json:"product_url"`
	BaseURL      string  `json:"base_url"`
	ThumbPath    string  `json:"thumb_path"`
	Filename     string  `json:"filename"`
	Description  string  `json:"description"`
	MediaType    string  `json:"media_type"`
	TakenAt      int64   `json:"taken_at"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	CameraMake   string  `json:"camera_make"`
	CameraModel  string  `json:"camera_model"`
	FocalLength  float64 `json:"focal_length"`
	Aperture     float64 `json:"aperture"`
	ISO          int     `json:"iso"`
	ShutterSpeed string  `json:"shutter_speed"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	LocationName string  `json:"location_name"`
	RawMetadata  string  `json:"-"`
}

type SearchFilter struct {
	Query     string
	MediaType string
	DateFrom  int64
	DateTo    int64
	Limit     int
	Offset    int
}

type Store struct {
	db *sql.DB
}

func Open(dbPath string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", dbPath)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	store := &Store{db: sqlDB}
	if err := store.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	schema := "CREATE TABLE IF NOT EXISTS media_items (" +
		"id TEXT PRIMARY KEY, " +
		"product_url TEXT NOT NULL, " +
		"base_url TEXT DEFAULT '', " +
		"thumb_path TEXT DEFAULT '', " +
		"filename TEXT DEFAULT '', " +
		"description TEXT DEFAULT '', " +
		"media_type TEXT DEFAULT 'photo', " +
		"taken_at INTEGER DEFAULT 0, " +
		"width INTEGER DEFAULT 0, " +
		"height INTEGER DEFAULT 0, " +
		"camera_make TEXT DEFAULT '', " +
		"camera_model TEXT DEFAULT '', " +
		"focal_length REAL DEFAULT 0, " +
		"aperture REAL DEFAULT 0, " +
		"iso INTEGER DEFAULT 0, " +
		"shutter_speed TEXT DEFAULT '', " +
		"latitude REAL DEFAULT 0, " +
		"longitude REAL DEFAULT 0, " +
		"location_name TEXT DEFAULT '', " +
		"raw_metadata TEXT DEFAULT '', " +
		"created_at INTEGER NOT NULL, " +
		"updated_at INTEGER NOT NULL" +
		");" +
		"CREATE INDEX IF NOT EXISTS idx_media_taken_at ON media_items(taken_at DESC);" +
		"CREATE INDEX IF NOT EXISTS idx_media_type ON media_items(media_type);" +
		"CREATE VIRTUAL TABLE IF NOT EXISTS media_fts USING fts5(" +
		"id UNINDEXED, filename, description, camera_make, camera_model, location_name, tokenize = 'unicode61'" +
		");"
	_, err := s.db.Exec(schema)
	return err
}

func (s *Store) Upsert(item MediaItem) error {
	now := time.Now().Unix()
	if item.ProductURL == "" && item.ID != "" {
		item.ProductURL = "https://photos.google.com/photo/" + item.ID
	}
	if item.MediaType == "" {
		item.MediaType = "photo"
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := "INSERT INTO media_items (" +
		"id, product_url, base_url, thumb_path, filename, description, media_type, " +
		"taken_at, width, height, camera_make, camera_model, focal_length, aperture, " +
		"iso, shutter_speed, latitude, longitude, location_name, raw_metadata, " +
		"created_at, updated_at" +
		") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) " +
		"ON CONFLICT(id) DO UPDATE SET " +
		"base_url      = CASE WHEN excluded.base_url != '' THEN excluded.base_url ELSE media_items.base_url END, " +
		"thumb_path    = CASE WHEN excluded.thumb_path != '' THEN excluded.thumb_path ELSE media_items.thumb_path END, " +
		"filename      = CASE WHEN excluded.filename != '' THEN excluded.filename ELSE media_items.filename END, " +
		"description   = CASE WHEN excluded.description != '' THEN excluded.description ELSE media_items.description END, " +
		"media_type    = CASE WHEN excluded.media_type != 'photo' THEN excluded.media_type ELSE media_items.media_type END, " +
		"taken_at      = CASE WHEN excluded.taken_at > 0 THEN excluded.taken_at ELSE media_items.taken_at END, " +
		"width         = CASE WHEN excluded.width > 0 THEN excluded.width ELSE media_items.width END, " +
		"height        = CASE WHEN excluded.height > 0 THEN excluded.height ELSE media_items.height END, " +
		"camera_make   = CASE WHEN excluded.camera_make != '' THEN excluded.camera_make ELSE media_items.camera_make END, " +
		"camera_model  = CASE WHEN excluded.camera_model != '' THEN excluded.camera_model ELSE media_items.camera_model END, " +
		"focal_length  = CASE WHEN excluded.focal_length > 0 THEN excluded.focal_length ELSE media_items.focal_length END, " +
		"aperture      = CASE WHEN excluded.aperture > 0 THEN excluded.aperture ELSE media_items.aperture END, " +
		"iso           = CASE WHEN excluded.iso > 0 THEN excluded.iso ELSE media_items.iso END, " +
		"shutter_speed = CASE WHEN excluded.shutter_speed != '' THEN excluded.shutter_speed ELSE media_items.shutter_speed END, " +
		"latitude      = CASE WHEN excluded.latitude != 0 THEN excluded.latitude ELSE media_items.latitude END, " +
		"longitude     = CASE WHEN excluded.longitude != 0 THEN excluded.longitude ELSE media_items.longitude END, " +
		"location_name = CASE WHEN excluded.location_name != '' THEN excluded.location_name ELSE media_items.location_name END, " +
		"raw_metadata  = CASE WHEN excluded.raw_metadata != '' THEN excluded.raw_metadata ELSE media_items.raw_metadata END, " +
		"updated_at    = excluded.updated_at;"

	_, err = tx.Exec(query,
		item.ID, item.ProductURL, item.BaseURL, item.ThumbPath, item.Filename, item.Description, item.MediaType,
		item.TakenAt, item.Width, item.Height, item.CameraMake, item.CameraModel, item.FocalLength, item.Aperture,
		item.ISO, item.ShutterSpeed, item.Latitude, item.Longitude, item.LocationName, item.RawMetadata,
		now, now,
	)
	if err != nil {
		return err
	}

	_, _ = tx.Exec("DELETE FROM media_fts WHERE id = ?", item.ID)
	_, err = tx.Exec(
		"INSERT INTO media_fts (id, filename, description, camera_make, camera_model, location_name) "+
			"SELECT id, filename, description, camera_make, camera_model, location_name FROM media_items WHERE id = ?",
		item.ID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) HasThumb(id string) bool {
	var thumb string
	err := s.db.QueryRow("SELECT thumb_path FROM media_items WHERE id = ?", id).Scan(&thumb)
	return err == nil && thumb != ""
}

func (s *Store) Count() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM media_items").Scan(&count)
	return count, err
}

func (s *Store) Search(f SearchFilter) ([]MediaItem, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}

	var where []string
	var args []any

	if strings.TrimSpace(f.Query) != "" {
		q := strings.TrimSpace(f.Query)
		where = append(where, "(" +
			"m.id IN (SELECT id FROM media_fts WHERE media_fts MATCH ?) " +
			"OR m.filename LIKE ? " +
			"OR m.camera_model LIKE ? " +
			"OR m.location_name LIKE ? " +
			"OR m.description LIKE ?" +
			")")
		likeQ := "%" + q + "%"
		ftsQ := "\"" + strings.ReplaceAll(q, "\"", "\"\"") + "\"*"
		args = append(args, ftsQ, likeQ, likeQ, likeQ, likeQ)
	}

	if f.MediaType != "" && f.MediaType != "all" {
		where = append(where, "m.media_type = ?")
		args = append(args, f.MediaType)
	}
	if f.DateFrom > 0 {
		where = append(where, "m.taken_at >= ?")
		args = append(args, f.DateFrom)
	}
	if f.DateTo > 0 {
		where = append(where, "m.taken_at <= ?")
		args = append(args, f.DateTo)
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM media_items m %s", whereClause)
	if err := s.db.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	querySQL := fmt.Sprintf(
		"SELECT id, product_url, base_url, thumb_path, filename, description, media_type, "+
			"taken_at, width, height, camera_make, camera_model, focal_length, aperture, "+
			"iso, shutter_speed, latitude, longitude, location_name "+
			"FROM media_items m %s ORDER BY m.taken_at DESC, m.created_at DESC LIMIT ? OFFSET ?",
		whereClause,
	)

	queryArgs := append(append([]any{}, args...), f.Limit, f.Offset)
	rows, err := s.db.Query(querySQL, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var results []MediaItem
	for rows.Next() {
		var it MediaItem
		if err := rows.Scan(
			&it.ID, &it.ProductURL, &it.BaseURL, &it.ThumbPath, &it.Filename, &it.Description, &it.MediaType,
			&it.TakenAt, &it.Width, &it.Height, &it.CameraMake, &it.CameraModel, &it.FocalLength, &it.Aperture,
			&it.ISO, &it.ShutterSpeed, &it.Latitude, &it.Longitude, &it.LocationName,
		); err != nil {
			return nil, 0, err
		}
		results = append(results, it)
	}
	return results, total, nil
}
