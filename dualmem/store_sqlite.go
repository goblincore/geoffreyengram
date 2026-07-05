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

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("dualmem: open db: %w", err)
	}
	db.SetMaxOpenConns(1)

	// Set pragmas separately — modernc.org/sqlite doesn't support DSN query params.
	// Use DELETE journal mode for reliable persistence; WAL with this driver
	// leaves writes in shared memory that may not checkpoint on Close().
	db.Exec("PRAGMA journal_mode=DELETE")
	db.Exec("PRAGMA busy_timeout=5000")
	db.Exec("PRAGMA synchronous=FULL")

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

	if version < 9 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS context_snapshots (
				id              TEXT PRIMARY KEY,
				namespace       TEXT NOT NULL,
				query           TEXT NOT NULL,
				query_embedding BLOB,
				source_ids_json TEXT NOT NULL DEFAULT '[]',
				tokens_used     INTEGER DEFAULT 0,
				created_at      TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_snapshots_ns ON context_snapshots(namespace, created_at);

			CREATE TABLE IF NOT EXISTS context_ratings (
				id          TEXT PRIMARY KEY,
				namespace   TEXT NOT NULL,
				snapshot_id TEXT NOT NULL,
				memory_id   TEXT NOT NULL,
				memory_type TEXT NOT NULL,
				phase       TEXT NOT NULL DEFAULT 'late',
				rating      INTEGER NOT NULL,
				cosine_sim  REAL,
				importance  REAL,
				salience    REAL,
				sector      TEXT,
				mem_type    TEXT,
				file_overlap INTEGER,
				age_days    REAL,
				text_length INTEGER,
				created_at  TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_ratings_ns ON context_ratings(namespace);
			CREATE INDEX IF NOT EXISTS idx_ratings_memory ON context_ratings(memory_id);
			CREATE INDEX IF NOT EXISTS idx_ratings_snapshot ON context_ratings(snapshot_id);
			CREATE INDEX IF NOT EXISTS idx_ratings_phase ON context_ratings(phase);

			CREATE TABLE IF NOT EXISTS session_ratings (
				id          TEXT PRIMARY KEY,
				namespace   TEXT NOT NULL,
				session_id  TEXT NOT NULL,
				score       INTEGER NOT NULL,
				explanation TEXT,
				query_used  TEXT,
				tokens_used INTEGER,
				created_at  TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_session_ratings_ns ON session_ratings(namespace, created_at);
		`); err != nil {
			return fmt.Errorf("migrate v9 (context ratings): %w", err)
		}
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (9)`)
	}

	if version < 10 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS structural_edges (
				source_path        TEXT NOT NULL,
				target_path        TEXT NOT NULL,
				edge_type          TEXT NOT NULL,
				weight             REAL NOT NULL DEFAULT 1.0,
				line_number        INTEGER NOT NULL DEFAULT 0,
				enclosing_function TEXT NOT NULL DEFAULT '',
				callee_name        TEXT NOT NULL DEFAULT '',
				namespace          TEXT NOT NULL,
				created_at         TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_structural_source ON structural_edges(namespace, source_path);
			CREATE INDEX IF NOT EXISTS idx_structural_target ON structural_edges(namespace, target_path);
			CREATE INDEX IF NOT EXISTS idx_structural_type ON structural_edges(namespace, edge_type);
		`); err != nil {
			return fmt.Errorf("migrate v10 (structural edges): %w", err)
		}
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (10)`)
	}

	if version < 11 {
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS codemap_tags (
				namespace  TEXT NOT NULL,
				file_path  TEXT NOT NULL,
				file_mtime INTEGER NOT NULL,
				language   TEXT NOT NULL DEFAULT '',
				tags_json  TEXT NOT NULL,
				scanned_at INTEGER NOT NULL,
				PRIMARY KEY (namespace, file_path)
			);
			CREATE INDEX IF NOT EXISTS idx_tags_ns ON codemap_tags(namespace);
		`); err != nil {
			return fmt.Errorf("migrate v11 (codemap tag cache): %w", err)
		}
		// Add source column to file_cochange to distinguish git-derived vs memory-derived edges
		// Safe ALTER TABLE — ignore error if column already exists
		s.db.Exec(`ALTER TABLE file_cochange ADD COLUMN source TEXT NOT NULL DEFAULT 'memory'`)
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (11)`)
	}

	if version < 12 {
		// dualmem v2 durable facts. Pure addition: does not touch any v1 table.
		// See docs/superpowers/plans/2026-07-04-dualmem-v2.md (P2).
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS facts (
				id             TEXT PRIMARY KEY,
				namespace      TEXT NOT NULL,
				kind           TEXT NOT NULL,
				text           TEXT NOT NULL,
				files_json     TEXT NOT NULL DEFAULT '[]',
				source         TEXT NOT NULL,
				git_commit     TEXT NOT NULL DEFAULT '',
				session_id     TEXT NOT NULL DEFAULT '',
				created_at     INTEGER NOT NULL,
				superseded_by TEXT REFERENCES facts(id),
				embedding      BLOB,
				hits           INTEGER NOT NULL DEFAULT 0,
				last_hit_at    INTEGER
			);
			CREATE INDEX IF NOT EXISTS idx_facts_ns_kind ON facts(namespace, kind);
			CREATE INDEX IF NOT EXISTS idx_facts_ns ON facts(namespace);
			CREATE INDEX IF NOT EXISTS idx_facts_kind ON facts(kind);
			CREATE INDEX IF NOT EXISTS idx_facts_superseded ON facts(superseded_by);
		`); err != nil {
			return fmt.Errorf("migrate v12 (facts): %w", err)
		}
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (12)`)
	}

	if version < 13 {
		// dualmem v2 served-facts log (task 7 instrumentation). Pure addition:
		// records every durable fact surfaced to a session (pinned block or a
		// pull tool) and tracks the file-touch hit signal. Idempotent per
		// (session_id, fact_id) so re-serving the same fact in one session is a
		// no-op and a hit is credited at most once per session.
		// See docs/superpowers/plans/2026-07-04-dualmem-v2.md ("Instrumentation from day one").
		if _, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS served_facts (
				session_id   TEXT NOT NULL,
				fact_id      TEXT NOT NULL REFERENCES facts(id),
				surface      TEXT NOT NULL,
				served_at    INTEGER NOT NULL,
				hit_credited INTEGER NOT NULL DEFAULT 0,
				hit_at       INTEGER,
				PRIMARY KEY (session_id, fact_id)
			);
			CREATE INDEX IF NOT EXISTS idx_served_facts_fact ON served_facts(fact_id);
			CREATE INDEX IF NOT EXISTS idx_served_facts_credited ON served_facts(hit_credited);
		`); err != nil {
			return fmt.Errorf("migrate v13 (served_facts): %w", err)
		}
		s.db.Exec(`INSERT INTO dualmem_schema_version (version) VALUES (13)`)
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

// GetMemoryCountByFiles returns the count of detail memories with any of the given file paths.
func (s *SQLiteStore) GetMemoryCountByFiles(userID string, files []string) (int, error) {
	if len(files) == 0 {
		return 0, nil
	}
	conditions := make([]string, len(files))
	args := make([]interface{}, 0, len(files)+1)
	args = append(args, userID)
	for i, f := range files {
		conditions[i] = "files_json LIKE ?"
		args = append(args, "%"+f+"%")
	}
	query := fmt.Sprintf(`
		SELECT COUNT(*) FROM detail_memories
		WHERE user_id = ? AND (%s)
	`, strings.Join(conditions, " OR "))
	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	return count, err
}

// GetAutopilotMemoryCountByFiles returns the count of autopilot-generated investigation
// memories (those with text starting with "[autopilot]") associated with any of the given files.
func (s *SQLiteStore) GetAutopilotMemoryCountByFiles(userID string, files []string) (int, error) {
	if len(files) == 0 {
		return 0, nil
	}
	conditions := make([]string, len(files))
	args := make([]interface{}, 0, len(files)+2)
	args = append(args, userID, "[autopilot]%")
	for i, f := range files {
		conditions[i] = "files_json LIKE ?"
		args = append(args, "%"+f+"%")
	}
	query := fmt.Sprintf(`
		SELECT COUNT(*) FROM detail_memories
		WHERE user_id = ? AND text LIKE ? AND (%s)
	`, strings.Join(conditions, " OR "))
	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	return count, err
}

// GetAutopilotMemoryCountByArea returns the count of autopilot-generated memories
// for a specific area (those with text starting with "[autopilot:areaKey]").
func (s *SQLiteStore) GetAutopilotMemoryCountByArea(userID, areaKey string) (int, error) {
	prefix := "[autopilot:" + areaKey + "]%"
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM detail_memories
		WHERE user_id = ? AND text LIKE ?
	`, userID, prefix).Scan(&count)
	return count, err
}

// GetWorkflowMemoryCount returns the count of workflow memories for a specific workflow ID.
func (s *SQLiteStore) GetWorkflowMemoryCount(userID, workflowID string) (int, error) {
	prefix := "[workflow:" + workflowID + "]%"
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM detail_memories
		WHERE user_id = ? AND text LIKE ?
	`, userID, prefix).Scan(&count)
	return count, err
}

// GetWorkflowHintsForFiles returns workflow hints for autopilot memories
// whose files_json contains any of the given filenames. Max 3 results per file.
func (s *SQLiteStore) GetWorkflowHintsForFiles(userID string, filenames []string) ([]WorkflowHint, error) {
	if len(filenames) == 0 {
		return nil, nil
	}

	seen := make(map[string]bool)
	var hints []WorkflowHint

	for _, fn := range filenames {
		basename := filepath.Base(fn)
		escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(basename)
		pattern := "%" + escaped + "%"

		rows, err := s.db.Query(`
			SELECT text FROM detail_memories
			WHERE user_id = ? AND type = 'autopilot' AND text LIKE '[workflow:%'
			  AND files_json LIKE ? ESCAPE '\'
			ORDER BY salience DESC LIMIT 3
		`, userID, pattern)
		if err != nil {
			continue
		}

		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				continue
			}
			wh := parseWorkflowHint(text)
			if wh == nil || seen[wh.WorkflowID] {
				continue
			}
			seen[wh.WorkflowID] = true
			wh.MatchedFile = basename
			hints = append(hints, *wh)
		}
		rows.Close()

		if len(hints) >= 3 {
			break
		}
	}

	return hints, nil
}

// GetWorkflowHintsForTickets returns workflow hints for autopilot memories
// whose text contains any of the given ticket prefixes. Max 3 results, deduplicated.
func (s *SQLiteStore) GetWorkflowHintsForTickets(userID string, tickets []string) ([]WorkflowHint, error) {
	if len(tickets) == 0 {
		return nil, nil
	}

	conditions := make([]string, len(tickets))
	args := make([]interface{}, 0, len(tickets)+1)
	args = append(args, userID)
	for i, ticket := range tickets {
		conditions[i] = "text LIKE ?"
		args = append(args, "%"+ticket+"%")
	}

	query := fmt.Sprintf(`
		SELECT DISTINCT text FROM detail_memories
		WHERE user_id = ? AND type = 'autopilot' AND text LIKE '[workflow:%%'
		  AND (%s)
		ORDER BY salience DESC LIMIT 3
	`, strings.Join(conditions, " OR "))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[string]bool)
	var hints []WorkflowHint
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			continue
		}
		wh := parseWorkflowHint(text)
		if wh == nil || seen[wh.WorkflowID] {
			continue
		}
		seen[wh.WorkflowID] = true
		hints = append(hints, *wh)
	}

	return hints, nil
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

func (s *SQLiteStore) GetDetailByID(id string) (*detailWithVector, error) {
	row := s.db.QueryRow(`
		SELECT id, user_id, text, embedding, importance_score, sector, entities_json, session_id, salience, created_at, last_accessed_at, access_count, type, files_json
		FROM detail_memories WHERE id = ?`, id)

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

// --- Structural graph ---

func (s *SQLiteStore) InsertStructuralEdges(namespace string, edges []StructuralEdge) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("insert structural edges: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Full rebuild: clear existing edges for this namespace
	if _, err := tx.Exec(`DELETE FROM structural_edges WHERE namespace = ?`, namespace); err != nil {
		return fmt.Errorf("insert structural edges: clear: %w", err)
	}

	stmt, err := tx.Prepare(
		`INSERT INTO structural_edges (source_path, target_path, edge_type, weight, line_number, enclosing_function, callee_name, namespace)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("insert structural edges: prepare: %w", err)
	}
	defer stmt.Close()

	for _, e := range edges {
		if _, err := stmt.Exec(e.SourcePath, e.TargetPath, e.EdgeType, e.Weight, e.LineNumber, e.EnclosingFunction, e.CalleeName, namespace); err != nil {
			return fmt.Errorf("insert structural edge %s->%s: %w", e.SourcePath, e.TargetPath, err)
		}
	}

	return tx.Commit()
}

// likeEscaper escapes SQLite LIKE wildcards so directory names containing
// underscores (common) or percent signs don't match unrelated paths.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// ReplaceStructuralEdgesForDirs deletes edges whose source file lives directly
// in one of relDirs (non-recursive) and inserts the freshly extracted
// replacements. Used by incremental rescans; InsertStructuralEdges remains the
// full-rebuild path.
func (s *SQLiteStore) ReplaceStructuralEdgesForDirs(namespace string, relDirs []string, edges []StructuralEdge) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("replace structural edges: begin tx: %w", err)
	}
	defer tx.Rollback()

	for _, dir := range relDirs {
		if dir == "." || dir == "" {
			// Root-level sources have no path separator.
			if _, err := tx.Exec(
				`DELETE FROM structural_edges WHERE namespace = ? AND source_path NOT LIKE '%/%'`,
				namespace,
			); err != nil {
				return fmt.Errorf("replace structural edges: clear root: %w", err)
			}
			continue
		}
		// Direct children only: dir/x matches, dir/sub/x does not.
		esc := likeEscaper.Replace(dir)
		if _, err := tx.Exec(
			`DELETE FROM structural_edges WHERE namespace = ? AND source_path LIKE ? ESCAPE '\' AND source_path NOT LIKE ? ESCAPE '\'`,
			namespace, esc+"/%", esc+"/%/%",
		); err != nil {
			return fmt.Errorf("replace structural edges: clear %s: %w", dir, err)
		}
	}

	stmt, err := tx.Prepare(
		`INSERT INTO structural_edges (source_path, target_path, edge_type, weight, line_number, enclosing_function, callee_name, namespace)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("replace structural edges: prepare: %w", err)
	}
	defer stmt.Close()

	for _, e := range edges {
		if _, err := stmt.Exec(e.SourcePath, e.TargetPath, e.EdgeType, e.Weight, e.LineNumber, e.EnclosingFunction, e.CalleeName, namespace); err != nil {
			return fmt.Errorf("replace structural edge %s->%s: %w", e.SourcePath, e.TargetPath, err)
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetStructuralNeighbors(namespace, path string, edgeTypes []string, limit int) ([]StructuralEdge, error) {
	if limit <= 0 {
		limit = 50
	}

	var rows *sql.Rows
	var err error

	if len(edgeTypes) > 0 {
		// Build IN clause placeholders
		placeholders := strings.Repeat("?,", len(edgeTypes))
		placeholders = placeholders[:len(placeholders)-1]

		args := make([]interface{}, 0, len(edgeTypes)+4)
		args = append(args, namespace, path, path)
		for _, et := range edgeTypes {
			args = append(args, et)
		}
		args = append(args, limit)

		query := fmt.Sprintf(
			`SELECT source_path, target_path, edge_type, weight, line_number, enclosing_function, callee_name, namespace
			 FROM structural_edges
			 WHERE namespace = ? AND (source_path = ? OR target_path = ?) AND edge_type IN (%s)
			 ORDER BY weight DESC
			 LIMIT ?`,
			placeholders,
		)
		rows, err = s.db.Query(query, args...)
	} else {
		rows, err = s.db.Query(
			`SELECT source_path, target_path, edge_type, weight, line_number, enclosing_function, callee_name, namespace
			 FROM structural_edges
			 WHERE namespace = ? AND (source_path = ? OR target_path = ?)
			 ORDER BY weight DESC
			 LIMIT ?`,
			namespace, path, path, limit,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("get structural neighbors: %w", err)
	}
	defer rows.Close()
	return scanStructuralEdges(rows)
}

func (s *SQLiteStore) GetStructuralEdgesForPath(namespace, path string, limit int) ([]StructuralEdge, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT source_path, target_path, edge_type, weight, line_number, enclosing_function, callee_name, namespace
		 FROM structural_edges
		 WHERE namespace = ? AND source_path = ?
		 ORDER BY weight DESC
		 LIMIT ?`,
		namespace, path, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get structural edges for path: %w", err)
	}
	defer rows.Close()
	return scanStructuralEdges(rows)
}

func scanStructuralEdges(rows *sql.Rows) ([]StructuralEdge, error) {
	var edges []StructuralEdge
	for rows.Next() {
		var e StructuralEdge
		if err := rows.Scan(&e.SourcePath, &e.TargetPath, &e.EdgeType, &e.Weight,
			&e.LineNumber, &e.EnclosingFunction, &e.CalleeName, &e.Namespace); err != nil {
			continue
		}
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// --- Context snapshots & ratings ---

func (s *SQLiteStore) SearchSnapshotsForMemory(namespace, memoryID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM context_snapshots
		 WHERE namespace = ? AND source_ids_json LIKE ?`,
		namespace, "%"+memoryID+"%",
	).Scan(&count)
	return count > 0, err
}

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

// --- Health check helpers ---

func (s *SQLiteStore) GetEmbeddingModelCounts(userID string) (map[string]int, error) {
	rows, err := s.db.Query(
		`SELECT embedding_model, COUNT(*) FROM detail_memories WHERE user_id = ? GROUP BY embedding_model`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var model string
		var count int
		if err := rows.Scan(&model, &count); err != nil {
			return nil, err
		}
		result[model] = count
	}
	return result, rows.Err()
}

func (s *SQLiteStore) GetStaleSketchRawCount(userID string, olderThan time.Time) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sketch_raw WHERE user_id = ? AND processed = 0 AND created_at < ?`,
		userID, olderThan.Format("2006-01-02 15:04:05"),
	).Scan(&count)
	return count, err
}

func (s *SQLiteStore) GetMemoryTimeRange(userID string) (oldest, newest time.Time, err error) {
	var oldestStr, newestStr sql.NullString
	err = s.db.QueryRow(
		`SELECT MIN(created_at), MAX(created_at) FROM detail_memories WHERE user_id = ?`,
		userID,
	).Scan(&oldestStr, &newestStr)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if oldestStr.Valid {
		oldest = parseTime(oldestStr.String)
	}
	if newestStr.Valid {
		newest = parseTime(newestStr.String)
	}
	return oldest, newest, nil
}

func (s *SQLiteStore) GetIsolatedEntityNodeCount(namespace string) (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM entity_nodes n
		WHERE n.namespace = ?
		AND NOT EXISTS (
			SELECT 1 FROM entity_edges e
			WHERE e.namespace = n.namespace
			AND (e.source_id = n.id OR e.target_id = n.id)
		)`,
		namespace,
	).Scan(&count)
	return count, err
}

func (s *SQLiteStore) GetSketchRawCount(userID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sketch_raw WHERE user_id = ?`, userID).Scan(&count)
	return count, err
}

// --- Facts (v2 durable facts) ---

// scanFact decodes one facts row into a *Fact, including its embedding BLOB.
func scanFact(row interface{ Scan(dest ...any) error }) (*Fact, error) {
	var (
		f            Fact
		filesJSON    string
		embBlob      []byte
		createdAt    int64
		supersededBy sql.NullString
		lastHitAt    sql.NullInt64
	)
	if err := row.Scan(
		&f.ID, &f.Namespace, &f.Kind, &f.Text, &filesJSON,
		&f.Source, &f.GitCommit, &f.SessionID, &createdAt,
		&supersededBy, &embBlob, &f.Hits, &lastHitAt,
	); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(filesJSON), &f.Files)
	if f.Files == nil {
		f.Files = []string{}
	}
	f.CreatedAt = time.Unix(0, createdAt).UTC()
	if supersededBy.Valid {
		f.SupersededBy = supersededBy.String
	}
	if lastHitAt.Valid {
		f.LastHitAt = time.Unix(0, lastHitAt.Int64).UTC()
	}
	if len(embBlob) > 0 {
		f.Vector = decodeVector(embBlob)
	}
	return &f, nil
}

func (s *SQLiteStore) InsertFact(f *Fact, embedding []float32) error {
	filesJSON, err := json.Marshal(f.Files)
	if err != nil {
		filesJSON = []byte("[]")
	}
	_, err = s.db.Exec(`
		INSERT INTO facts (id, namespace, kind, text, files_json, source, git_commit, session_id, created_at, superseded_by, embedding, hits, last_hit_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, 0, NULL)`,
		f.ID, f.Namespace, f.Kind, f.Text, string(filesJSON),
		f.Source, f.GitCommit, f.SessionID, f.CreatedAt.UnixNano(),
		encodeVector(embedding),
	)
	return err
}

func (s *SQLiteStore) GetFact(id string) (*Fact, error) {
	row := s.db.QueryRow(`
		SELECT id, namespace, kind, text, files_json, source, git_commit, session_id, created_at, superseded_by, embedding, hits, last_hit_at
		FROM facts WHERE id = ?`, id)
	f, err := scanFact(row)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// buildFactsQuery constructs the SELECT + WHERE clause for fact queries.
// namespaces is a non-empty list; kind is optional ("" = any). When
// includeSuperseded is false, only facts with NULL superseded_by are returned.
func buildFactsQuery(namespaces []string, kind string, includeSuperseded bool) (string, []any) {
	var (
		placeholders []string
		args         []any
	)
	for _, ns := range namespaces {
		placeholders = append(placeholders, "?")
		args = append(args, ns)
	}
	where := fmt.Sprintf("namespace IN (%s)", strings.Join(placeholders, ","))
	if kind != "" {
		where += " AND kind = ?"
		args = append(args, kind)
	}
	if !includeSuperseded {
		where += " AND superseded_by IS NULL"
	}
	return `
		SELECT id, namespace, kind, text, files_json, source, git_commit, session_id, created_at, superseded_by, embedding, hits, last_hit_at
		FROM facts WHERE ` + where + " ORDER BY created_at DESC", args
}

func (s *SQLiteStore) ListFacts(namespace, kind string, includeSuperseded bool) ([]*Fact, error) {
	q, args := buildFactsQuery([]string{namespace}, kind, includeSuperseded)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetFactsByNamespaces(namespaces []string, kind string, includeSuperseded bool) ([]*Fact, error) {
	if len(namespaces) == 0 {
		return nil, nil
	}
	// Dedup namespaces while preserving order.
	seen := make(map[string]bool, len(namespaces))
	deduped := namespaces[:0]
	for _, ns := range namespaces {
		if !seen[ns] {
			seen[ns] = true
			deduped = append(deduped, ns)
		}
	}
	q, args := buildFactsQuery(deduped, kind, includeSuperseded)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SupersedeFact marks oldID as superseded by newID in a single transaction.
// This keeps any existing revision chain walkable: nodes that pointed to oldID
// still reach oldID, and oldID now reaches newID.
func (s *SQLiteStore) SupersedeFact(oldID, newID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("supersede fact: begin tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE facts SET superseded_by = ? WHERE id = ? AND superseded_by IS NULL`, newID, oldID)
	if err != nil {
		return fmt.Errorf("supersede fact: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either oldID doesn't exist or it's already superseded. Distinguish for callers.
		var existing string
		err := tx.QueryRow(`SELECT COALESCE(superseded_by, '') FROM facts WHERE id = ?`, oldID).Scan(&existing)
		if err != nil {
			return fmt.Errorf("supersede fact: %q not found", oldID)
		}
		if existing != "" {
			return fmt.Errorf("supersede fact: %q already superseded by %q", oldID, existing)
		}
	}
	return tx.Commit()
}

// IncrementFactHits bumps the passive usefulness counter for a served fact.
func (s *SQLiteStore) IncrementFactHits(id string) error {
	_, err := s.db.Exec(`UPDATE facts SET hits = hits + 1, last_hit_at = ? WHERE id = ?`, time.Now().UnixNano(), id)
	return err
}

// ListAllFacts returns facts across every namespace, optionally including
// superseded entries. Used by the markdown export, which must render the whole
// live store regardless of namespace.
func (s *SQLiteStore) ListAllFacts(includeSuperseded bool) ([]*Fact, error) {
	where := ""
	if !includeSuperseded {
		where = " WHERE superseded_by IS NULL"
	}
	q := `
		SELECT id, namespace, kind, text, files_json, source, git_commit, session_id, created_at, superseded_by, embedding, hits, last_hit_at
		FROM facts` + where + " ORDER BY kind, namespace, created_at DESC"
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// RetireFact marks id as superseded by itself. A self-reference is the sentinel
// for "retired with no successor": it is excluded from live queries
// (superseded_by IS NULL) while remaining auditable. There is no hard delete.
// Idempotent on already-retired facts. Returns an error if id does not exist.
func (s *SQLiteStore) RetireFact(id string) error {
	res, err := s.db.Exec(`UPDATE facts SET superseded_by = ? WHERE id = ? AND superseded_by IS NULL`, id, id)
	if err != nil {
		return fmt.Errorf("retire fact: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either missing or already superseded. Distinguish for callers.
		var existing string
		err := s.db.QueryRow(`SELECT COALESCE(superseded_by, '') FROM facts WHERE id = ?`, id).Scan(&existing)
		if err != nil {
			return fmt.Errorf("retire fact: %q not found", id)
		}
		if existing != "" {
			// Already superseded (including already-retired). Treat as success.
			return nil
		}
	}
	return nil
}

// GetFactsByNamespacesKinds is like GetFactsByNamespaces but matches any of
// the given kinds (logical OR). An empty kinds slice means "any kind" (no kind
// filter), matching the single-kind "" convention. Used by precedent
// (decision|deadend) and other multi-kind pull tools.
func (s *SQLiteStore) GetFactsByNamespacesKinds(namespaces, kinds []string, includeSuperseded bool) ([]*Fact, error) {
	if len(namespaces) == 0 {
		return nil, nil
	}
	deduped := dedupStrings(namespaces)
	q, args := buildFactsQueryKinds(deduped, kinds, includeSuperseded)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFactsByFile returns non-superseded facts in the given namespaces whose
// files_json array cites path or its basename. path is matched literally and as
// a basename so callers can pass either a repo-relative path ("dualmem/facts.go")
// or a bare filename ("facts.go"). limit <= 0 means no limit.
func (s *SQLiteStore) GetFactsByFile(namespaces []string, path, basename string, limit int) ([]*Fact, error) {
	if len(namespaces) == 0 || (path == "" && basename == "") {
		return nil, nil
	}
	deduped := dedupStrings(namespaces)

	nsPh := make([]string, len(deduped))
	args := make([]any, 0, len(deduped)+3)
	for i, ns := range deduped {
		nsPh[i] = "?"
		args = append(args, ns)
	}
	where := fmt.Sprintf("namespace IN (%s)", strings.Join(nsPh, ","))
	where += " AND superseded_by IS NULL"
	where += " AND EXISTS (SELECT 1 FROM json_each(files_json) WHERE json_each.value IN (?, ?))"
	args = append(args, path, basename)

	q := `SELECT id, namespace, kind, text, files_json, source, git_commit, session_id, created_at, superseded_by, embedding, hits, last_hit_at
		FROM facts WHERE ` + where + " ORDER BY created_at DESC"
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// buildFactsQueryKinds is the multi-kind generalization of buildFactsQuery. An
// empty kinds slice applies no kind filter (any kind).
func buildFactsQueryKinds(namespaces, kinds []string, includeSuperseded bool) (string, []any) {
	nsPh := make([]string, len(namespaces))
	args := make([]any, 0, len(namespaces)+len(kinds)+1)
	for i, ns := range namespaces {
		nsPh[i] = "?"
		args = append(args, ns)
	}
	where := fmt.Sprintf("namespace IN (%s)", strings.Join(nsPh, ","))
	if len(kinds) > 0 {
		kindPh := make([]string, len(kinds))
		for i, k := range kinds {
			kindPh[i] = "?"
			args = append(args, k)
		}
		where += fmt.Sprintf(" AND kind IN (%s)", strings.Join(kindPh, ","))
	}
	if !includeSuperseded {
		where += " AND superseded_by IS NULL"
	}
	return `SELECT id, namespace, kind, text, files_json, source, git_commit, session_id, created_at, superseded_by, embedding, hits, last_hit_at
		FROM facts WHERE ` + where + " ORDER BY created_at DESC", args
}

// dedupStrings returns ns with duplicates removed, preserving order.
func dedupStrings(ns []string) []string {
	seen := make(map[string]bool, len(ns))
	out := ns[:0]
	for _, s := range ns {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// --- Served facts (v2 instrumentation) ---
//
// served_facts logs every durable fact surfaced to a session (pinned block or
// a pull tool) so the file-touch hit signal can later credit useful facts. The
// (session_id, fact_id) primary key makes recording idempotent: re-serving the
// same fact in one session is a no-op, and a hit is credited at most once per
// session (guarded by hit_credited).

// InsertServedFact records that factID was surfaced to sessionID via surface.
// Idempotent on (session_id, fact_id): a repeated serve in the same session
// leaves the original row (and its hit state) untouched. Empty factID is a
// silent no-op (callers may pass partial slices).
func (s *SQLiteStore) InsertServedFact(sessionID, factID, surface string) error {
	if sessionID == "" || factID == "" {
		return nil
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO served_facts (session_id, fact_id, surface, served_at, hit_credited, hit_at)
		VALUES (?, ?, ?, ?, 0, NULL)`,
		sessionID, factID, surface, time.Now().UnixNano())
	return err
}

// GetServedFactsForSession returns every served_facts row for a session, in
// serve order. Used by the file-touch correlator to decide which facts to
// credit. Rows carry the fact's Files slice so the caller can intersect
// against touched paths without a second lookup.
func (s *SQLiteStore) GetServedFactsForSession(sessionID string) ([]ServedFact, error) {
	if sessionID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT sf.fact_id, sf.surface, sf.served_at, sf.hit_credited, sf.hit_at,
		       f.files_json
		FROM served_facts sf
		JOIN facts f ON f.id = sf.fact_id
		WHERE sf.session_id = ?
		ORDER BY sf.served_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServedFact
	for rows.Next() {
		var (
			sf        ServedFact
			filesJSON string
			servedAt  int64
			hitAt     sql.NullInt64
		)
		if err := rows.Scan(&sf.FactID, &sf.Surface, &servedAt, &sf.HitCredited, &hitAt, &filesJSON); err != nil {
			return nil, err
		}
		sf.SessionID = sessionID
		sf.ServedAt = time.Unix(0, servedAt).UTC()
		if hitAt.Valid {
			sf.HitAt = time.Unix(0, hitAt.Int64).UTC()
		}
		_ = json.Unmarshal([]byte(filesJSON), &sf.Files)
		if sf.Files == nil {
			sf.Files = []string{}
		}
		out = append(out, sf)
	}
	return out, rows.Err()
}

// MarkServedFactHit credits one served fact: bumps the fact's hits counter and
// last_hit_at, and marks the served_facts row hit_credited=1 with hit_at set.
// It is a no-op if the row is already credited, so calling it twice for the
// same (session, fact) only counts once. Returns whether a credit happened.
func (s *SQLiteStore) MarkServedFactHit(sessionID, factID string) (bool, error) {
	if sessionID == "" || factID == "" {
		return false, nil
	}
	now := time.Now().UnixNano()
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("mark served hit: begin tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE served_facts SET hit_credited = 1, hit_at = ?
		WHERE session_id = ? AND fact_id = ? AND hit_credited = 0`,
		now, sessionID, factID)
	if err != nil {
		return false, fmt.Errorf("mark served hit: update: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Already credited (or no such row). Nothing to credit.
		return false, tx.Commit()
	}
	if _, err := tx.Exec(`UPDATE facts SET hits = hits + 1, last_hit_at = ? WHERE id = ?`,
		now, factID); err != nil {
		return false, fmt.Errorf("mark served hit: bump fact: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("mark served hit: commit: %w", err)
	}
	return true, nil
}

// FactStatsRow is one row of the per-kind aggregation computed by
// GetFactStatsCounts: how many facts exist, how many were ever served, and how
// many earned at least one hit. ServedCount and HitCount count distinct facts.
type FactStatsRow struct {
	Kind        string
	FactsCount  int
	ServedCount int
	HitCount    int
}

// GetFactStatsCounts aggregates per-kind fact/served/hit counts across the
// given namespaces. Superseded facts are excluded. A fact counts as "served"
// if it has any served_facts row, and as "hit" if it has hits > 0. Empty
// namespaces selects all.
func (s *SQLiteStore) GetFactStatsCounts(namespaces []string) ([]FactStatsRow, error) {
	q := `SELECT f.kind,
			COUNT(*) AS facts,
			SUM(CASE WHEN sf.fact_id IS NOT NULL THEN 1 ELSE 0 END) AS served,
			SUM(CASE WHEN f.hits > 0 THEN 1 ELSE 0 END) AS hits
		FROM facts f
		LEFT JOIN (SELECT DISTINCT fact_id FROM served_facts) sf ON sf.fact_id = f.id
		WHERE f.superseded_by IS NULL`
	var args []any
	if len(namespaces) > 0 {
		ph := make([]string, len(namespaces))
		for i, ns := range namespaces {
			ph[i] = "?"
			args = append(args, ns)
		}
		q += fmt.Sprintf(" AND f.namespace IN (%s)", strings.Join(ph, ","))
	}
	q += ` GROUP BY f.kind ORDER BY f.kind`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FactStatsRow
	for rows.Next() {
		var r FactStatsRow
		if err := rows.Scan(&r.Kind, &r.FactsCount, &r.ServedCount, &r.HitCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetDeadFacts returns non-superserved fact IDs that were served at least
// minServes times (across all sessions) but have zero hits — candidates for
// pruning or rewriting. Returns at most limit IDs (limit <= 0 = no cap).
func (s *SQLiteStore) GetDeadFacts(namespaces []string, minServes, limit int) ([]DeadFact, error) {
	if minServes < 1 {
		minServes = 1
	}
	q := `SELECT f.id, f.kind, f.text, COUNT(sf.fact_id) AS serves
		FROM facts f
		JOIN served_facts sf ON sf.fact_id = f.id
		WHERE f.superseded_by IS NULL AND f.hits = 0
		GROUP BY f.id, f.kind, f.text
		HAVING serves >= ?`
	args := []any{minServes}
	if len(namespaces) > 0 {
		ph := make([]string, len(namespaces))
		for i, ns := range namespaces {
			ph[i] = "?"
			args = append(args, ns)
		}
		q += fmt.Sprintf(" AND f.namespace IN (%s)", strings.Join(ph, ","))
	}
	q += ` ORDER BY serves DESC, f.id`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeadFact
	for rows.Next() {
		var d DeadFact
		if err := rows.Scan(&d.ID, &d.Kind, &d.Text, &d.Serves); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetStaleFactCandidates returns non-superserved facts whose git_commit is
// reachable from HEAD only through more than maxCommitsBehind commits, i.e.
// the codebase has moved well past where the fact was written. A fact with an
// empty git_commit or one that can't be resolved (rebased/GC'd, shallow clone)
// is treated as non-stale (best-effort: we can't prove staleness, so we don't
// flag it). Commit-distance is computed by the caller (engine) via git; this
// method just loads the (id, kind, text, git_commit) rows for the engine to
// score. Returns at most limit rows (limit <= 0 = no cap).
func (s *SQLiteStore) GetStaleFactCandidates(namespaces []string, limit int) ([]StaleFact, error) {
	q := `SELECT id, kind, text, git_commit FROM facts
		WHERE superseded_by IS NULL AND git_commit != ''`
	var args []any
	if len(namespaces) > 0 {
		ph := make([]string, len(namespaces))
		for i, ns := range namespaces {
			ph[i] = "?"
			args = append(args, ns)
		}
		q += fmt.Sprintf(" AND namespace IN (%s)", strings.Join(ph, ","))
	}
	q += ` ORDER BY created_at DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StaleFact
	for rows.Next() {
		var sf StaleFact
		if err := rows.Scan(&sf.ID, &sf.Kind, &sf.Text, &sf.GitCommit); err != nil {
			return nil, err
		}
		out = append(out, sf)
	}
	return out, rows.Err()
}

// --- Helpers ---

func generateID() string {
	// Use crypto/rand for UUIDs but for simplicity use a timestamp + random suffix.
	// In production this would use google/uuid.
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().UnixMicro()%10000)
}
