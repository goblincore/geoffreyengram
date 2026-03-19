package dualmem

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
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
		INSERT INTO detail_memories (id, user_id, text, embedding, importance_score, sector, entities_json, session_id, parent_id, salience, files_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		dm.ID, userID, dm.Text, encodeVector(embedding), dm.ImportanceScore,
		dm.Sector, encodeEntities(dm.Entities), dm.SessionID, "",
		dm.Salience, encodeStringSlice(dm.Files),
	)
	return err
}

func (s *SQLiteStore) GetDetailMemories(userID string) ([]detailWithVector, error) {
	rows, err := s.db.Query(`
		SELECT id, user_id, text, embedding, importance_score, sector, entities_json, session_id, salience, created_at, last_accessed_at, access_count, files_json
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
			&createdAt, &lastAccessed, &d.AccessCount, &filesJSON); err != nil {
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

func (s *SQLiteStore) GetDetailCount(userID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM detail_memories WHERE user_id = ?`, userID).Scan(&count)
	return count, err
}

func (s *SQLiteStore) GetLowestImportanceDetail(userID string) (*detailWithVector, error) {
	row := s.db.QueryRow(`
		SELECT id, user_id, text, embedding, importance_score, sector, entities_json, session_id, salience, created_at, last_accessed_at, access_count, files_json
		FROM detail_memories WHERE user_id = ? ORDER BY importance_score ASC LIMIT 1`, userID)

	var d detailWithVector
	var vecBlob []byte
	var entitiesJSON, filesJSON, createdAt, lastAccessed string
	if err := row.Scan(&d.ID, &d.UserID, &d.Text, &vecBlob, &d.ImportanceScore,
		&d.Sector, &entitiesJSON, &d.SessionID, &d.Salience,
		&createdAt, &lastAccessed, &d.AccessCount, &filesJSON); err != nil {
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
	rows, err := s.db.Query(`
		SELECT id, user_id, content, sector, session_id, embedding, created_at
		FROM sketch_raw WHERE user_id = ? AND processed = 0
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

// --- Lifecycle ---

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// --- Helpers ---

func generateID() string {
	// Use crypto/rand for UUIDs but for simplicity use a timestamp + random suffix.
	// In production this would use google/uuid.
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().UnixMicro()%10000)
}
