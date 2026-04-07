package dualmem

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// StoreTags persists parsed tags for a file, replacing any previously cached entry.
func (s *SQLiteStore) StoreTags(namespace, filePath, lang string, tags []Tag, mtime int64) error {
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return fmt.Errorf("store tags: marshal: %w", err)
	}

	now := time.Now().Unix()
	_, err = s.db.Exec(`
		INSERT INTO codemap_tags (namespace, file_path, file_mtime, language, tags_json, scanned_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(namespace, file_path) DO UPDATE SET
			file_mtime = excluded.file_mtime,
			language   = excluded.language,
			tags_json  = excluded.tags_json,
			scanned_at = excluded.scanned_at`,
		namespace, filePath, mtime, lang, string(tagsJSON), now,
	)
	return err
}

// LoadTags retrieves cached tags for a file. Returns nil tags and zero mtime
// if the file is not cached (no error in that case).
func (s *SQLiteStore) LoadTags(namespace, filePath string) ([]Tag, int64, error) {
	var tagsJSON string
	var mtime int64
	err := s.db.QueryRow(
		`SELECT tags_json, file_mtime FROM codemap_tags WHERE namespace = ? AND file_path = ?`,
		namespace, filePath,
	).Scan(&tagsJSON, &mtime)

	if err == sql.ErrNoRows {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("load tags: %w", err)
	}

	var tags []Tag
	if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
		return nil, 0, fmt.Errorf("load tags: unmarshal: %w", err)
	}
	return tags, mtime, nil
}

// DeleteTags removes cached tags for a single file.
func (s *SQLiteStore) DeleteTags(namespace, filePath string) error {
	_, err := s.db.Exec(
		`DELETE FROM codemap_tags WHERE namespace = ? AND file_path = ?`,
		namespace, filePath,
	)
	return err
}

// LoadAllTagMtimes returns a map of file_path → file_mtime for every cached file
// in a namespace. Used to detect which files have changed since the last scan.
func (s *SQLiteStore) LoadAllTagMtimes(namespace string) (map[string]int64, error) {
	rows, err := s.db.Query(
		`SELECT file_path, file_mtime FROM codemap_tags WHERE namespace = ?`,
		namespace,
	)
	if err != nil {
		return nil, fmt.Errorf("load tag mtimes: %w", err)
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var path string
		var mtime int64
		if err := rows.Scan(&path, &mtime); err != nil {
			return nil, fmt.Errorf("load tag mtimes: scan: %w", err)
		}
		result[path] = mtime
	}
	return result, rows.Err()
}

// ClearTagCache removes all cached tags for a namespace.
func (s *SQLiteStore) ClearTagCache(namespace string) error {
	_, err := s.db.Exec(
		`DELETE FROM codemap_tags WHERE namespace = ?`,
		namespace,
	)
	return err
}
