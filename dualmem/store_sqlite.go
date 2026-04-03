package dualmem

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore implements Store using a local SQLite database.
// Suitable for development and single-user scenarios.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) the SQLite database and runs migrations.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("dualmem: mkdir %s: %w", filepath.Dir(path), err)
	}

	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("dualmem: open db: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("dualmem: migrate: %w", err)
	}
	return s, nil
}

func (s *SQLiteStore) migrate() error {
	s.db.Exec(`CREATE TABLE IF NOT EXISTS dualmem_schema_version (version INTEGER NOT NULL)`)

	var version int
	s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM dualmem_schema_version`).Scan(&version)

	if version < 1 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS detail_memories (
				id                TEXT PRIMARY KEY,
				user_id           TEXT NOT NULL,
				text              TEXT NOT NULL,
				embedding         BLOB NOT NULL,
				importance_score  REAL NOT NULL,
				sector            TEXT,
				entities_json     TEXT DEFAULT '[]',
				session_id        TEXT DEFAULT '',
				parent_id         TEXT DEFAULT '',
				embedding_model   TEXT NOT NULL DEFAULT 'text-embedding-004',
				salience          REAL NOT NULL DEFAULT 0.5,
				created_at        TEXT NOT NULL DEFAULT (datetime('now')),
				last_accessed_at  TEXT NOT NULL DEFAULT (datetime('now')),
				access_count      INTEGER NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_detail_user ON detail_memories(user_id);
			CREATE INDEX IF NOT EXISTS idx_detail_importance ON detail_memories(user_id, importance_score);

			CREATE TABLE IF NOT EXISTS sketch_raw (
				id          TEXT PRIMARY KEY,
				user_id     TEXT NOT NULL,
				content     TEXT NOT NULL,
				sector      TEXT DEFAULT '',
				session_id  TEXT DEFAULT '',
				embedding   BLOB,
				created_at  TEXT NOT NULL DEFAULT (datetime('now')),
				processed   INTEGER NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_raw_user_proc ON sketch_raw(user_id, processed, created_at);

			CREATE TABLE IF NOT EXISTS episodes (
				id              TEXT PRIMARY KEY,
				user_id         TEXT NOT NULL,
				summary_text    TEXT NOT NULL,
				embedding       BLOB NOT NULL,
				entities_json   TEXT DEFAULT '[]',
				emotional_tone  TEXT DEFAULT '',
				source_raw_ids  TEXT DEFAULT '[]',
				embedding_model TEXT NOT NULL DEFAULT 'text-embedding-004',
				created_at      TEXT NOT NULL DEFAULT (datetime('now')),
				expires_at      TEXT
			);
			CREATE INDEX IF NOT EXISTS idx_ep_user_created ON episodes(user_id, created_at);

			CREATE TABLE IF NOT EXISTS arcs (
				id                 TEXT PRIMARY KEY,
				user_id            TEXT NOT NULL,
				summary_text       TEXT NOT NULL,
				sketched_embedding BLOB NOT NULL,
				entities_json      TEXT DEFAULT '[]',
				episode_ids        TEXT DEFAULT '[]',
				embedding_model    TEXT NOT NULL DEFAULT 'text-embedding-004',
				projection_seed    INTEGER NOT NULL,
				created_at         TEXT NOT NULL DEFAULT (datetime('now')),
				expires_at         TEXT
			);
			CREATE INDEX IF NOT EXISTS idx_arc_user_created ON arcs(user_id, created_at);

			CREATE TABLE IF NOT EXISTS profiles (
				id                 TEXT PRIMARY KEY,
				user_id            TEXT UNIQUE NOT NULL,
				profile_json       TEXT NOT NULL,
				sketched_embedding BLOB NOT NULL,
				embedding_model    TEXT NOT NULL DEFAULT 'text-embedding-004',
				projection_seed    INTEGER NOT NULL,
				updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
			);

			CREATE TABLE IF NOT EXISTS dualmem_config (
				key        TEXT PRIMARY KEY,
				value      TEXT NOT NULL,
				updated_at TEXT NOT NULL DEFAULT (datetime('now'))
			);
		`); err != nil {
			return err
		}
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (1)`)
	}

	if version < 2 {
		s.db.Exec(`ALTER TABLE detail_memories ADD COLUMN files_json TEXT DEFAULT '[]'`)
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (2)`)
	}

	if version < 3 {
		s.db.Exec(`ALTER TABLE detail_memories ADD COLUMN type TEXT DEFAULT ''`)
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (3)`)
	}

	if version < 4 {
		s.db.Exec(`CREATE TABLE IF NOT EXISTS code_maps (
			namespace    TEXT PRIMARY KEY,
			root_dir     TEXT NOT NULL,
			zoom1        TEXT NOT NULL DEFAULT '',
			zoom2_json   TEXT NOT NULL DEFAULT '[]',
			generated_at TEXT NOT NULL DEFAULT (datetime('now')),
			git_commit   TEXT DEFAULT ''
		)`)
		s.db.Exec(`CREATE TABLE IF NOT EXISTS session_markers (
			id          TEXT PRIMARY KEY,
			namespace   TEXT NOT NULL,
			branch      TEXT NOT NULL DEFAULT '',
			commit_hash TEXT NOT NULL DEFAULT '',
			created_at  TEXT NOT NULL DEFAULT (datetime('now'))
		)`)
		s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_session_marker_ns ON session_markers(namespace, created_at DESC)`)
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (4)`)
	}

	if version < 5 {
		s.db.Exec(`CREATE TABLE IF NOT EXISTS code_map_embeddings (
			namespace       TEXT NOT NULL,
			module_path     TEXT NOT NULL,
			summary         TEXT NOT NULL,
			embedding       BLOB NOT NULL,
			embedding_model TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (namespace, module_path)
		)`)
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (5)`)
	}

	if version < 6 {
		s.db.Exec(`CREATE TABLE IF NOT EXISTS knowledge_docs (
			id              TEXT PRIMARY KEY,
			namespace       TEXT NOT NULL,
			topic           TEXT NOT NULL,
			content         TEXT NOT NULL,
			files_json      TEXT DEFAULT '[]',
			source_ids_json TEXT DEFAULT '[]',
			embedding       BLOB,
			token_count     INTEGER DEFAULT 0,
			created_at      TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(namespace, topic)
		)`)
		s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_kdocs_ns ON knowledge_docs(namespace)`)
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (6)`)
	}

	if version < 7 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS entity_nodes (
				id            TEXT PRIMARY KEY,
				name          TEXT NOT NULL,
				type          TEXT NOT NULL,
				namespace     TEXT NOT NULL,
				mention_count INTEGER NOT NULL DEFAULT 1,
				created_at    TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE UNIQUE INDEX IF NOT EXISTS idx_entity_canonical
				ON entity_nodes(namespace, type, name COLLATE NOCASE);

			CREATE TABLE IF NOT EXISTS entity_edges (
				id          TEXT PRIMARY KEY,
				source_id   TEXT NOT NULL REFERENCES entity_nodes(id),
				target_id   TEXT NOT NULL REFERENCES entity_nodes(id),
				relation    TEXT NOT NULL,
				strength    REAL NOT NULL DEFAULT 1.0,
				mentions    INTEGER NOT NULL DEFAULT 1,
				namespace   TEXT NOT NULL,
				created_at  TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_edge_source ON entity_edges(source_id);
			CREATE INDEX IF NOT EXISTS idx_edge_target ON entity_edges(target_id);
			CREATE UNIQUE INDEX IF NOT EXISTS idx_edge_unique
				ON entity_edges(source_id, target_id, relation);

			CREATE TABLE IF NOT EXISTS memory_entity_links (
				memory_id TEXT NOT NULL,
				entity_id TEXT NOT NULL REFERENCES entity_nodes(id),
				namespace TEXT NOT NULL,
				PRIMARY KEY (memory_id, entity_id)
			);
			CREATE INDEX IF NOT EXISTS idx_mel_entity ON memory_entity_links(entity_id);
			CREATE INDEX IF NOT EXISTS idx_mel_memory ON memory_entity_links(memory_id);
		`); err != nil {
			return fmt.Errorf("migrate v7 (entity graph): %w", err)
		}
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (7)`)
	}

	if version < 8 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS file_cochange (
				source_path TEXT NOT NULL,
				target_path TEXT NOT NULL,
				namespace   TEXT NOT NULL,
				strength    REAL NOT NULL DEFAULT 1.0,
				co_count    INTEGER NOT NULL DEFAULT 1,
				memory_ids  TEXT DEFAULT '[]',
				concepts    TEXT DEFAULT '[]',
				created_at  TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
				PRIMARY KEY (namespace, source_path, target_path)
			);
			CREATE INDEX IF NOT EXISTS idx_cochange_source ON file_cochange(namespace, source_path);
			CREATE INDEX IF NOT EXISTS idx_cochange_target ON file_cochange(namespace, target_path);
		`); err != nil {
			return fmt.Errorf("migrate v8 (co-change graph): %w", err)
		}
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (8)`)
	}

	return nil
}

// --- Vector encoding (matches engram's format) ---

func encodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func decodeVector(b []byte) []float32 {
	if len(b) == 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func encodeEntities(entities []Entity) string {
	b, _ := json.Marshal(entities)
	return string(b)
}

func decodeEntities(s string) []Entity {
	var e []Entity
	json.Unmarshal([]byte(s), &e)
	return e
}

func encodeStringSlice(ss []string) string {
	b, _ := json.Marshal(ss)
	return string(b)
}

func decodeStringSlice(s string) []string {
	var ss []string
	json.Unmarshal([]byte(s), &ss)
	return ss
}

func parseTime(s string) time.Time {
	t, _ := time.Parse("2006-01-02 15:04:05", s)
	return t
}

// --- Detail Path ---

func (s *SQLiteStore) InsertDetail(dm *DetailMemory, embedding []float32, userID string) error {
	_, err := s.db.Exec(`
		INSERT INTO detail_memories (id, user_id, text, embedding, importance_score, sector, entities_json, session_id, parent_id, salience, type, files_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		dm.ID, userID, dm.Text, encodeVector(embedding), dm.ImportanceScore,
		dm.Sector, encodeEntities(dm.Entities), dm.SessionID, "",
		dm.Salience, dm.Type, encodeStringSlice(dm.Files),
	)
	return err
}

func (s *SQLiteStore) GetDetailMemories(userID string) ([]detailWithVector, error) {
	rows, err := s.db.Query(`
		SELECT id, user_id, text, embedding, importance_score, sector, entities_json, session_id, salience, created_at, last_accessed_at, access_count, type, files_json
		FROM detail_memories WHERE user_id = ? ORDER BY importance_score DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []detailWithVector
	for rows.Next() {
		var d detailWithVector
		var vecBlob []byte
		var entitiesJSON, filesJSON, createdAt, lastAccessed string
		if err := rows.Scan(&d.ID, &d.UserID, &d.Text, &vecBlob, &d.ImportanceScore,
			&d.Sector, &entitiesJSON, &d.SessionID, &d.Salience,
			&createdAt, &lastAccessed, &d.AccessCount, &d.Type, &filesJSON); err != nil {
			return nil, err
		}
		d.Vector = decodeVector(vecBlob)
		d.Entities = decodeEntities(entitiesJSON)
		d.Files = decodeStringSlice(filesJSON)
		d.CreatedAt = parseTime(createdAt)
		d.LastAccessedAt = parseTime(lastAccessed)
		results = append(results, d)
	}
	return results, rows.Err()
}

func (s *SQLiteStore) GetDetailsByType(userID, memType string) ([]detailWithVector, error) {
	rows, err := s.db.Query(`
		SELECT id, user_id, text, embedding, importance_score, sector, entities_json, session_id, salience, created_at, last_accessed_at, access_count, type, files_json
		FROM detail_memories WHERE user_id = ? AND type = ? ORDER BY created_at DESC`, userID, memType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []detailWithVector
	for rows.Next() {
		var d detailWithVector
		var vecBlob []byte
		var entitiesJSON, filesJSON, createdAt, lastAccessed string
		if err := rows.Scan(&d.ID, &d.UserID, &d.Text, &vecBlob, &d.ImportanceScore,
			&d.Sector, &entitiesJSON, &d.SessionID, &d.Salience,
			&createdAt, &lastAccessed, &d.AccessCount, &d.Type, &filesJSON); err != nil {
			return nil, err
		}
		d.Vector = decodeVector(vecBlob)
		d.Entities = decodeEntities(entitiesJSON)
		d.Files = decodeStringSlice(filesJSON)
		d.CreatedAt = parseTime(createdAt)
		d.LastAccessedAt = parseTime(lastAccessed)
		results = append(results, d)
	}
	return results, rows.Err()
}

func (s *SQLiteStore) GetDetailsByFiles(userID, filename string, types []string, limit int) ([]detailWithVector, error) {
	if limit <= 0 {
		limit = 5
	}
	if len(types) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(types))
	args := make([]interface{}, 0, len(types)+2)
	args = append(args, userID)
	for i, t := range types {
		placeholders[i] = "?"
		args = append(args, t)
	}
	escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(filename)
	pattern := "%" + escaped + "%"
	args = append(args, pattern)

	query := fmt.Sprintf(`
		SELECT id, user_id, text, embedding, importance_score, sector, entities_json, session_id, salience, created_at, last_accessed_at, access_count, type, files_json
		FROM detail_memories WHERE user_id = ? AND type IN (%s) AND files_json LIKE ? ESCAPE '\'
		ORDER BY salience DESC LIMIT %d`, strings.Join(placeholders, ","), limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []detailWithVector
	for rows.Next() {
		var d detailWithVector
		var vecBlob []byte
		var entitiesJSON, filesJSON, createdAt, lastAccessed string
		if err := rows.Scan(&d.ID, &d.UserID, &d.Text, &vecBlob, &d.ImportanceScore,
			&d.Sector, &entitiesJSON, &d.SessionID, &d.Salience,
			&createdAt, &lastAccessed, &d.AccessCount, &d.Type, &filesJSON); err != nil {
			return nil, err
		}
		d.Vector = decodeVector(vecBlob)
		d.Entities = decodeEntities(entitiesJSON)
		d.Files = decodeStringSlice(filesJSON)
		d.CreatedAt = parseTime(createdAt)
		d.LastAccessedAt = parseTime(lastAccessed)
		results = append(results, d)
	}
	return results, rows.Err()
}

func (s *SQLiteStore) GetFilesWithMemories(userID string, types []string) ([]string, error) {
	if len(types) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(types))
	args := make([]interface{}, 0, len(types)+1)
	args = append(args, userID)
	for i, t := range types {
		placeholders[i] = "?"
		args = append(args, t)
	}

	query := fmt.Sprintf(`
		SELECT files_json FROM detail_memories
		WHERE user_id = ? AND type IN (%s) AND files_json != '[]'`,
		strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[string]bool)
	for rows.Next() {
		var filesJSON string
		if err := rows.Scan(&filesJSON); err != nil {
			return nil, err
		}
		files := decodeStringSlice(filesJSON)
		for _, f := range files {
			base := filepath.Base(f)
			seen[base] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]string, 0, len(seen))
	for f := range seen {
		result = append(result, f)
	}
	sort.Strings(result)
	return result, nil
}

func (s *SQLiteStore) GetDetailCount(userID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM detail_memories WHERE user_id = ?`, userID).Scan(&count)
	return count, err
}

func (s *SQLiteStore) GetLowestImportanceDetail(userID string) (*detailWithVector, error) {
	row := s.db.QueryRow(`
		SELECT id, user_id, text, embedding, importance_score, sector, entities_json, session_id, salience, created_at, last_accessed_at, access_count, type, files_json
		FROM detail_memories WHERE user_id = ? ORDER BY importance_score ASC LIMIT 1`, userID)

	var d detailWithVector
	var vecBlob []byte
	var entitiesJSON, filesJSON, createdAt, lastAccessed string
	if err := row.Scan(&d.ID, &d.UserID, &d.Text, &vecBlob, &d.ImportanceScore,
		&d.Sector, &entitiesJSON, &d.SessionID, &d.Salience,
		&createdAt, &lastAccessed, &d.AccessCount, &d.Type, &filesJSON); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	d.Vector = decodeVector(vecBlob)
	d.Entities = decodeEntities(entitiesJSON)
	d.Files = decodeStringSlice(filesJSON)
	d.CreatedAt = parseTime(createdAt)
	d.LastAccessedAt = parseTime(lastAccessed)
	return &d, nil
}

func (s *SQLiteStore) DeleteDetail(id string) error {
	_, err := s.db.Exec(`DELETE FROM detail_memories WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) UpdateDetailImportance(id string, score float64) error {
	_, err := s.db.Exec(`UPDATE detail_memories SET importance_score = ? WHERE id = ?`, score, id)
	return err
}

func (s *SQLiteStore) TouchDetail(id string) error {
	_, err := s.db.Exec(`
		UPDATE detail_memories SET last_accessed_at = datetime('now'), access_count = access_count + 1 WHERE id = ?`, id)
	return err
}

// --- Sketch Raw ---

func (s *SQLiteStore) InsertSketchRaw(userID, content, sector, sessionID string, embedding []float32) error {
	id := generateID()
	var embBlob []byte
	if len(embedding) > 0 {
		embBlob = encodeVector(embedding)
	}
	_, err := s.db.Exec(`
		INSERT INTO sketch_raw (id, user_id, content, sector, session_id, embedding)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id, userID, content, sector, sessionID, embBlob)
	return err
}

func (s *SQLiteStore) GetUnprocessedRaw(userID string, limit int) ([]sketchRaw, error) {
	var rows *sql.Rows
	var err error
	if userID == "" {
		// Empty userID = query all users
		rows, err = s.db.Query(`
			SELECT id, user_id, content, sector, session_id, embedding, created_at
			FROM sketch_raw WHERE processed = 0
			ORDER BY created_at ASC LIMIT ?`, limit)
	} else {
		rows, err = s.db.Query(`
			SELECT id, user_id, content, sector, session_id, embedding, created_at
			FROM sketch_raw WHERE user_id = ? AND processed = 0
			ORDER BY created_at ASC LIMIT ?`, userID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []sketchRaw
	for rows.Next() {
		var r sketchRaw
		var embBlob []byte
		var createdAt string
		if err := rows.Scan(&r.ID, &r.UserID, &r.Content, &r.Sector, &r.SessionID, &embBlob, &createdAt); err != nil {
			return nil, err
		}
		r.Embedding = decodeVector(embBlob)
		r.CreatedAt = parseTime(createdAt)
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *SQLiteStore) GetAllSketchRaw(userID string, limit int) ([]sketchRaw, error) {
	rows, err := s.db.Query(`
		SELECT id, user_id, content, sector, session_id, embedding, created_at
		FROM sketch_raw WHERE user_id = ?
		ORDER BY created_at ASC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []sketchRaw
	for rows.Next() {
		var r sketchRaw
		var embBlob []byte
		var createdAt string
		if err := rows.Scan(&r.ID, &r.UserID, &r.Content, &r.Sector, &r.SessionID, &embBlob, &createdAt); err != nil {
			return nil, err
		}
		r.Embedding = decodeVector(embBlob)
		r.CreatedAt = parseTime(createdAt)
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *SQLiteStore) GetSketchRawByID(id string) (*sketchRaw, error) {
	var r sketchRaw
	var embBlob []byte
	var createdAt string
	err := s.db.QueryRow(`
		SELECT id, user_id, content, sector, session_id, embedding, created_at
		FROM sketch_raw WHERE id = ?`, id).
		Scan(&r.ID, &r.UserID, &r.Content, &r.Sector, &r.SessionID, &embBlob, &createdAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	r.Embedding = decodeVector(embBlob)
	r.CreatedAt = parseTime(createdAt)
	return &r, nil
}

func (s *SQLiteStore) DeleteSketchRaw(id string) error {
	_, err := s.db.Exec(`DELETE FROM sketch_raw WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) MarkRawProcessed(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	_, err := s.db.Exec(
		`UPDATE sketch_raw SET processed = 1 WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		args...)
	return err
}

// --- Episodes ---

func (s *SQLiteStore) InsertEpisode(ep *Episode, embedding []float32, userID, embeddingModel string, sourceRawIDs []string) error {
	expiresAt := time.Now().AddDate(0, 0, 30).Format("2006-01-02 15:04:05")
	_, err := s.db.Exec(`
		INSERT INTO episodes (id, user_id, summary_text, embedding, entities_json, emotional_tone, source_raw_ids, embedding_model, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ep.ID, userID, ep.SummaryText, encodeVector(embedding),
		encodeEntities(ep.Entities), ep.EmotionalTone,
		encodeStringSlice(sourceRawIDs), embeddingModel, expiresAt)
	return err
}

func (s *SQLiteStore) GetEpisodes(userID string, after *time.Time) ([]episodeWithVector, error) {
	query := `SELECT id, user_id, summary_text, embedding, entities_json, emotional_tone, created_at
		FROM episodes WHERE user_id = ?`
	args := []any{userID}

	if after != nil {
		query += ` AND created_at >= ?`
		args = append(args, after.Format("2006-01-02 15:04:05"))
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []episodeWithVector
	for rows.Next() {
		var e episodeWithVector
		var vecBlob []byte
		var entitiesJSON, createdAt string
		if err := rows.Scan(&e.ID, &e.UserID, &e.SummaryText, &vecBlob, &entitiesJSON, &e.EmotionalTone, &createdAt); err != nil {
			return nil, err
		}
		e.Vector = decodeVector(vecBlob)
		e.Entities = decodeEntities(entitiesJSON)
		e.CreatedAt = parseTime(createdAt)
		results = append(results, e)
	}
	return results, rows.Err()
}

func (s *SQLiteStore) GetEpisodeByID(id string) (*episodeWithVector, error) {
	var e episodeWithVector
	var vecBlob []byte
	var entitiesJSON, createdAt string
	err := s.db.QueryRow(`
		SELECT id, user_id, summary_text, embedding, entities_json, emotional_tone, created_at
		FROM episodes WHERE id = ?`, id).
		Scan(&e.ID, &e.UserID, &e.SummaryText, &vecBlob, &entitiesJSON, &e.EmotionalTone, &createdAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	e.Vector = decodeVector(vecBlob)
	e.Entities = decodeEntities(entitiesJSON)
	e.CreatedAt = parseTime(createdAt)
	return &e, nil
}

func (s *SQLiteStore) GetExpiredEpisodes(before time.Time) ([]episodeWithVector, error) {
	rows, err := s.db.Query(`
		SELECT id, user_id, summary_text, embedding, entities_json, emotional_tone, created_at
		FROM episodes WHERE expires_at IS NOT NULL AND expires_at <= ?
		ORDER BY created_at ASC`,
		before.Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []episodeWithVector
	for rows.Next() {
		var e episodeWithVector
		var vecBlob []byte
		var entitiesJSON, createdAt string
		if err := rows.Scan(&e.ID, &e.UserID, &e.SummaryText, &vecBlob, &entitiesJSON, &e.EmotionalTone, &createdAt); err != nil {
			return nil, err
		}
		e.Vector = decodeVector(vecBlob)
		e.Entities = decodeEntities(entitiesJSON)
		e.CreatedAt = parseTime(createdAt)
		results = append(results, e)
	}
	return results, rows.Err()
}

func (s *SQLiteStore) DeleteEpisode(id string) error {
	_, err := s.db.Exec(`DELETE FROM episodes WHERE id = ?`, id)
	return err
}

// --- Arcs ---

func (s *SQLiteStore) InsertArc(arc *Arc, sketchedEmbedding []float32, userID, embeddingModel string, projectionSeed int64) error {
	expiresAt := time.Now().AddDate(0, 0, 90).Format("2006-01-02 15:04:05")
	_, err := s.db.Exec(`
		INSERT INTO arcs (id, user_id, summary_text, sketched_embedding, entities_json, episode_ids, embedding_model, projection_seed, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		arc.ID, userID, arc.SummaryText, encodeVector(sketchedEmbedding),
		encodeEntities(arc.Entities), encodeStringSlice(arc.EpisodeIDs),
		embeddingModel, projectionSeed, expiresAt)
	return err
}

func (s *SQLiteStore) GetArcs(userID string) ([]arcWithVector, error) {
	rows, err := s.db.Query(`
		SELECT id, user_id, summary_text, sketched_embedding, entities_json, episode_ids, created_at
		FROM arcs WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []arcWithVector
	for rows.Next() {
		var a arcWithVector
		var vecBlob []byte
		var entitiesJSON, episodeIDsJSON, createdAt string
		if err := rows.Scan(&a.ID, &a.UserID, &a.SummaryText, &vecBlob, &entitiesJSON, &episodeIDsJSON, &createdAt); err != nil {
			return nil, err
		}
		a.SketchedVector = decodeVector(vecBlob)
		a.Entities = decodeEntities(entitiesJSON)
		a.EpisodeIDs = decodeStringSlice(episodeIDsJSON)
		a.CreatedAt = parseTime(createdAt)
		results = append(results, a)
	}
	return results, rows.Err()
}

func (s *SQLiteStore) GetExpiredArcs(before time.Time) ([]arcWithVector, error) {
	rows, err := s.db.Query(`
		SELECT id, user_id, summary_text, sketched_embedding, entities_json, episode_ids, created_at
		FROM arcs WHERE expires_at IS NOT NULL AND expires_at <= ?
		ORDER BY created_at ASC`,
		before.Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []arcWithVector
	for rows.Next() {
		var a arcWithVector
		var vecBlob []byte
		var entitiesJSON, episodeIDsJSON, createdAt string
		if err := rows.Scan(&a.ID, &a.UserID, &a.SummaryText, &vecBlob, &entitiesJSON, &episodeIDsJSON, &createdAt); err != nil {
			return nil, err
		}
		a.SketchedVector = decodeVector(vecBlob)
		a.Entities = decodeEntities(entitiesJSON)
		a.EpisodeIDs = decodeStringSlice(episodeIDsJSON)
		a.CreatedAt = parseTime(createdAt)
		results = append(results, a)
	}
	return results, rows.Err()
}

func (s *SQLiteStore) DeleteArc(id string) error {
	_, err := s.db.Exec(`DELETE FROM arcs WHERE id = ?`, id)
	return err
}

// --- Profiles ---

func (s *SQLiteStore) UpsertProfile(profile *ProfileSketch, sketchedEmbedding []float32, embeddingModel string, projectionSeed int64) error {
	profileJSON, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	id := generateID()
	_, err = s.db.Exec(`
		INSERT INTO profiles (id, user_id, profile_json, sketched_embedding, embedding_model, projection_seed, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(user_id) DO UPDATE SET
			profile_json = excluded.profile_json,
			sketched_embedding = excluded.sketched_embedding,
			embedding_model = excluded.embedding_model,
			projection_seed = excluded.projection_seed,
			updated_at = datetime('now')`,
		id, profile.UserID, string(profileJSON), encodeVector(sketchedEmbedding),
		embeddingModel, projectionSeed)
	return err
}

func (s *SQLiteStore) GetProfile(userID string) (*profileWithVector, error) {
	var p profileWithVector
	var profileJSON string
	var vecBlob []byte
	var updatedAt string
	err := s.db.QueryRow(`
		SELECT user_id, profile_json, sketched_embedding, updated_at
		FROM profiles WHERE user_id = ?`, userID).Scan(&p.UserID, &profileJSON, &vecBlob, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(profileJSON), &p.ProfileSketch); err != nil {
		return nil, err
	}
	p.SketchedVector = decodeVector(vecBlob)
	p.UpdatedAt = parseTime(updatedAt)
	return &p, nil
}

func (s *SQLiteStore) GetUsersNeedingProfileUpdate() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT a.user_id FROM arcs a
		LEFT JOIN profiles p ON a.user_id = p.user_id
		WHERE p.user_id IS NULL OR a.created_at > p.updated_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []string
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		users = append(users, userID)
	}
	return users, rows.Err()
}

// --- Config ---

func (s *SQLiteStore) GetConfigValue(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM dualmem_config WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (s *SQLiteStore) SetConfigValue(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO dualmem_config (key, value, updated_at) VALUES (?, ?, datetime('now'))
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = datetime('now')`,
		key, value)
	return err
}

// --- Code maps ---

func (s *SQLiteStore) UpsertCodeMap(namespace, rootDir, zoom1, zoom2JSON, gitCommit string) error {
	_, err := s.db.Exec(`
		INSERT INTO code_maps (namespace, root_dir, zoom1, zoom2_json, generated_at, git_commit)
		VALUES (?, ?, ?, ?, datetime('now'), ?)
		ON CONFLICT(namespace) DO UPDATE SET
			root_dir = excluded.root_dir,
			zoom1 = excluded.zoom1,
			zoom2_json = excluded.zoom2_json,
			generated_at = datetime('now'),
			git_commit = excluded.git_commit`,
		namespace, rootDir, zoom1, zoom2JSON, gitCommit)
	return err
}

func (s *SQLiteStore) GetCodeMap(namespace string) (*StoredCodeMap, error) {
	var cm StoredCodeMap
	var genAt string
	err := s.db.QueryRow(`SELECT namespace, root_dir, zoom1, zoom2_json, generated_at, git_commit FROM code_maps WHERE namespace = ?`, namespace).
		Scan(&cm.Namespace, &cm.RootDir, &cm.Zoom1, &cm.Zoom2JSON, &genAt, &cm.GitCommit)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cm.GeneratedAt, _ = time.Parse("2006-01-02 15:04:05", genAt)
	return &cm, nil
}

// --- Code map embeddings ---

func (s *SQLiteStore) UpsertCodeMapEmbeddings(namespace string, embeddings map[string]ModuleEmbedding, embeddingModel string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tx.Exec(`DELETE FROM code_map_embeddings WHERE namespace = ?`, namespace)

	stmt, err := tx.Prepare(`INSERT INTO code_map_embeddings (namespace, module_path, summary, embedding, embedding_model) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for path, me := range embeddings {
		_, err := stmt.Exec(namespace, path, me.Summary, encodeVector(me.Embedding), embeddingModel)
		if err != nil {
			return fmt.Errorf("insert embedding %s: %w", path, err)
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetCodeMapEmbeddings(namespace string) (map[string][]float32, string, error) {
	rows, err := s.db.Query(`SELECT module_path, embedding, embedding_model FROM code_map_embeddings WHERE namespace = ?`, namespace)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	result := make(map[string][]float32)
	var model string
	for rows.Next() {
		var path string
		var embBlob []byte
		var m string
		if err := rows.Scan(&path, &embBlob, &m); err != nil {
			return nil, "", err
		}
		result[path] = decodeVector(embBlob)
		model = m
	}
	return result, model, rows.Err()
}

func (s *SQLiteStore) DeleteCodeMapEmbeddings(namespace string) error {
	_, err := s.db.Exec(`DELETE FROM code_map_embeddings WHERE namespace = ?`, namespace)
	return err
}

// --- Session markers ---

func (s *SQLiteStore) InsertSessionMarker(marker *SessionMarker) error {
	_, err := s.db.Exec(`
		INSERT INTO session_markers (id, namespace, branch, commit_hash, created_at)
		VALUES (?, ?, ?, ?, datetime('now'))`,
		marker.ID, marker.Namespace, marker.Branch, marker.Commit)
	return err
}

func (s *SQLiteStore) GetLatestSessionMarker(namespace string) (*SessionMarker, error) {
	var sm SessionMarker
	var createdAt string
	err := s.db.QueryRow(`
		SELECT id, namespace, branch, commit_hash, created_at
		FROM session_markers WHERE namespace = ?
		ORDER BY created_at DESC LIMIT 1`, namespace).
		Scan(&sm.ID, &sm.Namespace, &sm.Branch, &sm.Commit, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sm.Timestamp, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return &sm, nil
}

// --- Namespaces ---

func (s *SQLiteStore) ListNamespaces() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT user_id FROM detail_memories ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var namespaces []string
	for rows.Next() {
		var ns string
		if err := rows.Scan(&ns); err != nil {
			return nil, err
		}
		namespaces = append(namespaces, ns)
	}
	return namespaces, rows.Err()
}

// --- Knowledge documents ---

func (s *SQLiteStore) UpsertKnowledgeDoc(doc *KnowledgeDoc, embedding []float32) error {
	filesJSON, _ := json.Marshal(doc.Files)
	sourceJSON, _ := json.Marshal(doc.SourceIDs)
	var embBlob []byte
	if len(embedding) > 0 {
		embBlob = encodeVector(embedding)
	}
	_, err := s.db.Exec(`
		INSERT INTO knowledge_docs (id, namespace, topic, content, files_json, source_ids_json, embedding, token_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(namespace, topic) DO UPDATE SET
			content = excluded.content,
			files_json = excluded.files_json,
			source_ids_json = excluded.source_ids_json,
			embedding = excluded.embedding,
			token_count = excluded.token_count,
			updated_at = excluded.updated_at`,
		doc.ID, doc.Namespace, doc.Topic, doc.Content, string(filesJSON), string(sourceJSON),
		embBlob, doc.TokenCount, doc.CreatedAt.Format(time.RFC3339), doc.UpdatedAt.Format(time.RFC3339))
	return err
}

func (s *SQLiteStore) GetKnowledgeDocs(namespace string) ([]KnowledgeDoc, error) {
	rows, err := s.db.Query(`
		SELECT id, namespace, topic, content, files_json, source_ids_json, embedding, token_count, created_at, updated_at
		FROM knowledge_docs WHERE namespace = ? ORDER BY updated_at DESC`, namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []KnowledgeDoc
	for rows.Next() {
		doc, err := scanKnowledgeDoc(rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

func (s *SQLiteStore) GetKnowledgeDocByTopic(namespace, topic string) (*KnowledgeDoc, error) {
	row := s.db.QueryRow(`
		SELECT id, namespace, topic, content, files_json, source_ids_json, embedding, token_count, created_at, updated_at
		FROM knowledge_docs WHERE namespace = ? AND topic = ?`, namespace, topic)

	var doc KnowledgeDoc
	var filesJSON, sourceJSON string
	var embBlob []byte
	var createdStr, updatedStr string
	err := row.Scan(&doc.ID, &doc.Namespace, &doc.Topic, &doc.Content, &filesJSON, &sourceJSON,
		&embBlob, &doc.TokenCount, &createdStr, &updatedStr)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(filesJSON), &doc.Files)
	json.Unmarshal([]byte(sourceJSON), &doc.SourceIDs)
	if len(embBlob) > 0 {
		doc.Embedding = decodeVector(embBlob)
	}
	doc.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	doc.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return &doc, nil
}

func (s *SQLiteStore) DeleteKnowledgeDoc(namespace, topic string) error {
	_, err := s.db.Exec(`DELETE FROM knowledge_docs WHERE namespace = ? AND topic = ?`, namespace, topic)
	return err
}

func (s *SQLiteStore) GetUncoveredMemories(namespace string) ([]detailWithVector, error) {
	// Get all source IDs from knowledge docs
	rows, err := s.db.Query(`SELECT source_ids_json FROM knowledge_docs WHERE namespace = ?`, namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	coveredIDs := make(map[string]bool)
	for rows.Next() {
		var sourceJSON string
		if err := rows.Scan(&sourceJSON); err != nil {
			return nil, err
		}
		var ids []string
		json.Unmarshal([]byte(sourceJSON), &ids)
		for _, id := range ids {
			coveredIDs[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Get all detail memories for the namespace, filter out covered ones
	all, err := s.GetDetailMemories(namespace)
	if err != nil {
		return nil, err
	}

	var uncovered []detailWithVector
	for _, dm := range all {
		if !coveredIDs[dm.ID] {
			uncovered = append(uncovered, dm)
		}
	}
	return uncovered, nil
}

// scanKnowledgeDoc reads a KnowledgeDoc from a sql.Rows cursor.
func scanKnowledgeDoc(rows *sql.Rows) (KnowledgeDoc, error) {
	var doc KnowledgeDoc
	var filesJSON, sourceJSON string
	var embBlob []byte
	var createdStr, updatedStr string
	err := rows.Scan(&doc.ID, &doc.Namespace, &doc.Topic, &doc.Content, &filesJSON, &sourceJSON,
		&embBlob, &doc.TokenCount, &createdStr, &updatedStr)
	if err != nil {
		return doc, err
	}
	json.Unmarshal([]byte(filesJSON), &doc.Files)
	json.Unmarshal([]byte(sourceJSON), &doc.SourceIDs)
	if len(embBlob) > 0 {
		doc.Embedding = decodeVector(embBlob)
	}
	doc.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	doc.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return doc, nil
}

// --- Lifecycle ---

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// --- Seed memory operations ---

func (s *SQLiteStore) GetDetailCountExcludingSeeds(userID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM detail_memories WHERE user_id = ? AND (type IS NULL OR type != 'seed')`, userID).Scan(&count)
	return count, err
}

func (s *SQLiteStore) CountSeedMemories(userID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM detail_memories WHERE user_id = ? AND type = 'seed'`, userID).Scan(&count)
	return count, err
}

func (s *SQLiteStore) DeleteSeedMemories(userID string) error {
	_, err := s.db.Exec(`DELETE FROM detail_memories WHERE user_id = ? AND type = 'seed'`, userID)
	return err
}

// --- Entity graph methods ---

func (s *SQLiteStore) UpsertEntity(node *EntityNode) (string, error) {
	// Try to find existing entity by canonical match
	var existingID string
	err := s.db.QueryRow(
		`SELECT id FROM entity_nodes WHERE namespace = ? AND type = ? AND name = ? COLLATE NOCASE`,
		node.Namespace, node.Type, node.Name,
	).Scan(&existingID)

	if err == nil {
		// Exists — increment mention count
		s.db.Exec(
			`UPDATE entity_nodes SET mention_count = mention_count + 1, updated_at = datetime('now') WHERE id = ?`,
			existingID,
		)
		return existingID, nil
	}

	// New entity
	id := generateID()
	_, err = s.db.Exec(
		`INSERT INTO entity_nodes (id, name, type, namespace, mention_count) VALUES (?, ?, ?, ?, 1)`,
		id, node.Name, node.Type, node.Namespace,
	)
	if err != nil {
		return "", fmt.Errorf("insert entity: %w", err)
	}
	return id, nil
}

func (s *SQLiteStore) UpsertEdge(edge *EntityEdge) error {
	// Try to find existing edge
	var existingID string
	var mentions int
	var strength float64
	err := s.db.QueryRow(
		`SELECT id, mentions, strength FROM entity_edges WHERE source_id = ? AND target_id = ? AND relation = ?`,
		edge.SourceID, edge.TargetID, edge.Relation,
	).Scan(&existingID, &mentions, &strength)

	if err == nil {
		// Exists — update with running average
		newStrength := (strength*float64(mentions) + 1.0) / float64(mentions+1)
		_, err = s.db.Exec(
			`UPDATE entity_edges SET mentions = ?, strength = ?, updated_at = datetime('now') WHERE id = ?`,
			mentions+1, newStrength, existingID,
		)
		return err
	}

	// New edge
	id := generateID()
	_, err = s.db.Exec(
		`INSERT INTO entity_edges (id, source_id, target_id, relation, strength, mentions, namespace) VALUES (?, ?, ?, ?, ?, 1, ?)`,
		id, edge.SourceID, edge.TargetID, edge.Relation, 1.0, edge.Namespace,
	)
	return err
}

func (s *SQLiteStore) LinkMemoryToEntity(memoryID, entityID, namespace string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO memory_entity_links (memory_id, entity_id, namespace) VALUES (?, ?, ?)`,
		memoryID, entityID, namespace,
	)
	return err
}

func (s *SQLiteStore) ExpandEntities(namespace string, entityIDs []string, maxNeighbors int) ([]string, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}

	placeholders := strings.Repeat("?,", len(entityIDs))
	placeholders = placeholders[:len(placeholders)-1]

	args := make([]interface{}, 0, len(entityIDs)*3+1)
	for _, id := range entityIDs {
		args = append(args, id)
	}
	for _, id := range entityIDs {
		args = append(args, id)
	}
	for _, id := range entityIDs {
		args = append(args, id)
	}
	args = append(args, maxNeighbors)

	query := fmt.Sprintf(`
		SELECT DISTINCT neighbor_id FROM (
			SELECT target_id AS neighbor_id FROM entity_edges WHERE source_id IN (%s)
			UNION
			SELECT source_id AS neighbor_id FROM entity_edges WHERE target_id IN (%s)
		) WHERE neighbor_id NOT IN (%s)
		LIMIT ?`, placeholders, placeholders, placeholders)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("expand entities: %w", err)
	}
	defer rows.Close()

	var neighbors []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		neighbors = append(neighbors, id)
	}
	return neighbors, rows.Err()
}

func (s *SQLiteStore) GetMemoryIDsByEntities(entityIDs []string) ([]string, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}

	placeholders := strings.Repeat("?,", len(entityIDs))
	placeholders = placeholders[:len(placeholders)-1]

	args := make([]interface{}, len(entityIDs))
	for i, id := range entityIDs {
		args[i] = id
	}

	query := fmt.Sprintf(
		`SELECT DISTINCT memory_id FROM memory_entity_links WHERE entity_id IN (%s) LIMIT 200`,
		placeholders,
	)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get memory ids by entities: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *SQLiteStore) GetEntitiesByName(namespace, name string, limit int) ([]EntityNode, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(
		`SELECT id, name, type, namespace, mention_count, created_at, updated_at
		 FROM entity_nodes
		 WHERE namespace = ? AND name LIKE '%' || ? || '%' COLLATE NOCASE
		 ORDER BY mention_count DESC
		 LIMIT ?`,
		namespace, name, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get entities by name: %w", err)
	}
	defer rows.Close()

	var nodes []EntityNode
	for rows.Next() {
		var n EntityNode
		var createdAt, updatedAt string
		if err := rows.Scan(&n.ID, &n.Name, &n.Type, &n.Namespace, &n.MentionCount, &createdAt, &updatedAt); err != nil {
			continue
		}
		n.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		n.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAt)
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func (s *SQLiteStore) GetEntityStats(namespace string) (totalNodes int, totalEdges int, totalLinks int, err error) {
	s.db.QueryRow(`SELECT COUNT(*) FROM entity_nodes WHERE namespace = ?`, namespace).Scan(&totalNodes)
	s.db.QueryRow(`SELECT COUNT(*) FROM entity_edges WHERE namespace = ?`, namespace).Scan(&totalEdges)
	s.db.QueryRow(`SELECT COUNT(*) FROM memory_entity_links WHERE namespace = ?`, namespace).Scan(&totalLinks)
	return totalNodes, totalEdges, totalLinks, nil
}

func (s *SQLiteStore) GetTopEntities(namespace string, limit int) ([]EntityNode, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(
		`SELECT id, name, type, namespace, mention_count, created_at, updated_at
		 FROM entity_nodes
		 WHERE namespace = ?
		 ORDER BY mention_count DESC
		 LIMIT ?`,
		namespace, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get top entities: %w", err)
	}
	defer rows.Close()

	var nodes []EntityNode
	for rows.Next() {
		var n EntityNode
		var createdAt, updatedAt string
		if err := rows.Scan(&n.ID, &n.Name, &n.Type, &n.Namespace, &n.MentionCount, &createdAt, &updatedAt); err != nil {
			continue
		}
		n.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		n.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAt)
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func (s *SQLiteStore) GetEntityEdges(entityID string) ([]EntityEdge, error) {
	rows, err := s.db.Query(
		`SELECT id, source_id, target_id, relation, strength, mentions, namespace, created_at, updated_at
		 FROM entity_edges
		 WHERE source_id = ? OR target_id = ?
		 ORDER BY strength DESC`,
		entityID, entityID,
	)
	if err != nil {
		return nil, fmt.Errorf("get entity edges: %w", err)
	}
	defer rows.Close()

	var edges []EntityEdge
	for rows.Next() {
		var e EntityEdge
		var createdAt, updatedAt string
		if err := rows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.Relation, &e.Strength, &e.Mentions, &e.Namespace, &createdAt, &updatedAt); err != nil {
			continue
		}
		e.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		e.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAt)
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// --- Co-change graph ---

func (s *SQLiteStore) UpsertCoChange(namespace, source, target, memoryID string) error {
	// Normalize: always store with lexicographically smaller path first
	if source > target {
		source, target = target, source
	}

	// Try to load existing edge to merge memory IDs
	var existing string
	var coCount int
	err := s.db.QueryRow(
		`SELECT memory_ids, co_count FROM file_cochange WHERE namespace = ? AND source_path = ? AND target_path = ?`,
		namespace, source, target,
	).Scan(&existing, &coCount)

	if err == sql.ErrNoRows {
		// New edge
		ids, _ := json.Marshal([]string{memoryID})
		_, err = s.db.Exec(
			`INSERT INTO file_cochange (source_path, target_path, namespace, strength, co_count, memory_ids)
			 VALUES (?, ?, ?, 1.0, 1, ?)`,
			source, target, namespace, string(ids),
		)
		return err
	}
	if err != nil {
		return fmt.Errorf("upsert co-change: %w", err)
	}

	// Merge memory ID into existing list
	var ids []string
	json.Unmarshal([]byte(existing), &ids)
	found := false
	for _, id := range ids {
		if id == memoryID {
			found = true
			break
		}
	}
	if !found {
		ids = append(ids, memoryID)
	}
	idsJSON, _ := json.Marshal(ids)
	newCount := coCount + 1

	_, err = s.db.Exec(
		`UPDATE file_cochange SET co_count = ?, strength = ?, memory_ids = ?, updated_at = datetime('now')
		 WHERE namespace = ? AND source_path = ? AND target_path = ?`,
		newCount, float64(newCount), string(idsJSON), namespace, source, target,
	)
	return err
}

func (s *SQLiteStore) GetCoChangeNeighbors(namespace, path string, minStrength float64, limit int) ([]CoChangeEdge, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(
		`SELECT source_path, target_path, namespace, strength, co_count, memory_ids, concepts, created_at, updated_at
		 FROM file_cochange
		 WHERE namespace = ? AND (source_path = ? OR target_path = ?) AND strength >= ?
		 ORDER BY strength DESC
		 LIMIT ?`,
		namespace, path, path, minStrength, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get co-change neighbors: %w", err)
	}
	defer rows.Close()
	return scanCoChangeEdges(rows)
}

func (s *SQLiteStore) GetCoChangePaths(namespace string, paths []string, limit int) ([]CoChangeEdge, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}

	// Build placeholders for source and target matching
	placeholders := strings.Repeat("?,", len(paths))
	placeholders = placeholders[:len(placeholders)-1]

	args := make([]interface{}, 0, len(paths)*2+2)
	args = append(args, namespace)
	for _, p := range paths {
		args = append(args, p)
	}
	for _, p := range paths {
		args = append(args, p)
	}
	args = append(args, limit)

	query := fmt.Sprintf(
		`SELECT source_path, target_path, namespace, strength, co_count, memory_ids, concepts, created_at, updated_at
		 FROM file_cochange
		 WHERE namespace = ? AND (source_path IN (%s) OR target_path IN (%s))
		 ORDER BY strength DESC
		 LIMIT ?`,
		placeholders, placeholders,
	)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get co-change paths: %w", err)
	}
	defer rows.Close()
	return scanCoChangeEdges(rows)
}

func (s *SQLiteStore) GetCoChangeAll(namespace string, limit int) ([]CoChangeEdge, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT source_path, target_path, namespace, strength, co_count, memory_ids, concepts, created_at, updated_at
		 FROM file_cochange
		 WHERE namespace = ?
		 ORDER BY strength DESC
		 LIMIT ?`,
		namespace, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get all co-change: %w", err)
	}
	defer rows.Close()
	return scanCoChangeEdges(rows)
}

func (s *SQLiteStore) UpdateCoChangeConcepts(namespace, source, target, conceptsJSON string) error {
	// Normalize ordering
	if source > target {
		source, target = target, source
	}
	_, err := s.db.Exec(
		`UPDATE file_cochange SET concepts = ?, updated_at = datetime('now')
		 WHERE namespace = ? AND source_path = ? AND target_path = ?`,
		conceptsJSON, namespace, source, target,
	)
	return err
}

func (s *SQLiteStore) DecayCoChange(namespace string, halfLifeDays int) error {
	if halfLifeDays <= 0 {
		halfLifeDays = 90
	}
	// λ = ln(2) / halfLife
	lambda := 0.693147 / float64(halfLifeDays)

	// Apply exponential decay: strength = co_count * exp(-λ * days_since_update)
	_, err := s.db.Exec(
		`UPDATE file_cochange
		 SET strength = co_count * exp(? * (julianday('now') - julianday(updated_at)))
		 WHERE namespace = ?`,
		-lambda, namespace,
	)
	if err != nil {
		return fmt.Errorf("decay co-change: %w", err)
	}

	// Clean up edges that decayed below threshold
	_, err = s.db.Exec(
		`DELETE FROM file_cochange WHERE namespace = ? AND strength < 0.1`,
		namespace,
	)
	return err
}

func scanCoChangeEdges(rows *sql.Rows) ([]CoChangeEdge, error) {
	var edges []CoChangeEdge
	for rows.Next() {
		var e CoChangeEdge
		var memoryIDsJSON, conceptsJSON, createdAt, updatedAt string
		if err := rows.Scan(&e.SourcePath, &e.TargetPath, &e.Namespace, &e.Strength, &e.CoCount,
			&memoryIDsJSON, &conceptsJSON, &createdAt, &updatedAt); err != nil {
			continue
		}
		json.Unmarshal([]byte(memoryIDsJSON), &e.MemoryIDs)
		json.Unmarshal([]byte(conceptsJSON), &e.Concepts)
		e.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		e.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAt)
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// ExpandEntitiesWithEdges returns neighbor entity IDs with their connecting edge info.
// Unlike ExpandEntities which returns just IDs, this preserves edge type and strength
// for structure-aware graph boosting.
func (s *SQLiteStore) ExpandEntitiesWithEdges(namespace string, entityIDs []string, maxNeighbors int) ([]ExpandedEntityEdge, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}

	placeholders := strings.Repeat("?,", len(entityIDs))
	placeholders = placeholders[:len(placeholders)-1]

	args := make([]interface{}, 0, len(entityIDs)*3+1)
	for _, id := range entityIDs {
		args = append(args, id)
	}
	for _, id := range entityIDs {
		args = append(args, id)
	}
	for _, id := range entityIDs {
		args = append(args, id)
	}
	args = append(args, maxNeighbors)

	query := fmt.Sprintf(`
		SELECT neighbor_id, relation, strength FROM (
			SELECT target_id AS neighbor_id, relation, strength FROM entity_edges WHERE source_id IN (%s)
			UNION ALL
			SELECT source_id AS neighbor_id, relation, strength FROM entity_edges WHERE target_id IN (%s)
		) WHERE neighbor_id NOT IN (%s)
		ORDER BY strength DESC
		LIMIT ?`, placeholders, placeholders, placeholders)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("expand entities with edges: %w", err)
	}
	defer rows.Close()

	var results []ExpandedEntityEdge
	for rows.Next() {
		var e ExpandedEntityEdge
		if err := rows.Scan(&e.NeighborID, &e.Relation, &e.Strength); err != nil {
			continue
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

// --- Helpers ---

func generateID() string {
	// Use crypto/rand for UUIDs but for simplicity use a timestamp + random suffix.
	// In production this would use google/uuid.
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().UnixMicro()%10000)
}
