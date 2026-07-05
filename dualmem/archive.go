package dualmem

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// v1ArchiveFormat is the on-disk JSON format marker. Bump if the shape changes.
const v1ArchiveFormat = "dualmem-v1-archive-1"

// ArchiveV1Options configures an ArchiveV1 run.
type ArchiveV1Options struct {
	// OutDir overrides the destination directory. When empty, defaults to
	// ~/.dualmem-v1-archive.
	OutDir string
	// Force allows overwriting an existing non-empty archive directory.
	Force bool
}

// ArchiveV1Result summarizes a completed archive run.
type ArchiveV1Result struct {
	OutDir      string                 // absolute path written to
	Namespaces  []string               // namespaces emitted (sorted)
	PerNSCounts map[string]map[string]int // namespace -> table -> row count
	GlobalCounts map[string]int        // global table -> row count (tables with no namespace column)
	TotalRows   int                    // sum of all rows written across all files
}

// tableSchema describes one table discovered in the store.
type tableSchema struct {
	name string
	// columns in definition order: name -> declared type (uppercased)
	colOrder  []string
	colTypes  map[string]string
	nsColumn  string // "namespace", "user_id", or "" for global tables
}

// ArchiveV1 dumps the entire v1 SQLite store to one JSON file per namespace
// under OutDir (default ~/.dualmem-v1-archive). It is the Phase 0 safety net:
// nothing destructive in the v2 migration may run until this dump exists.
//
// The source database is opened read-only; it is never modified. Each emitted
// file is self-contained and carries the full schema version, archive
// timestamp, source DB path, the namespace, and a `tables` map of every table
// to its rows. Namespaced tables are filtered to that namespace; global tables
// (those without a namespace column, e.g. dualmem_config) are included in full
// in every namespace file so each file is independently restorable. BLOB
// columns are base64-encoded.
func ArchiveV1(dbPath string, opts ArchiveV1Options) (*ArchiveV1Result, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("archive: resolve db path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("archive: db not found at %s: %w", abs, err)
	}

	outDir := opts.OutDir
	if outDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("archive: home dir: %w", err)
		}
		outDir = filepath.Join(home, ".dualmem-v1-archive")
	}
	outDir, err = filepath.Abs(outDir)
	if err != nil {
		return nil, fmt.Errorf("archive: resolve out dir: %w", err)
	}
	if err := prepareOutDir(outDir, opts.Force); err != nil {
		return nil, err
	}

	// Open read-only so the source store can never be mutated by the archive.
	// modernc.org/sqlite honors the SQLite URI form with mode=ro when the DSN
	// begins with "file:".
	dsn := "file:" + abs + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("archive: open db (ro): %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	schemaVersion, err := readSchemaVersion(db)
	if err != nil {
		return nil, fmt.Errorf("archive: read schema version: %w", err)
	}

	tables, err := discoverTables(db)
	if err != nil {
		return nil, fmt.Errorf("archive: discover tables: %w", err)
	}

	namespaces, err := discoverNamespaces(db, tables)
	if err != nil {
		return nil, fmt.Errorf("archive: discover namespaces: %w", err)
	}

	// Pre-dump global tables once; their (small) contents are reused in every
	// namespace file so each file is independently restorable.
	globalTables := filterGlobal(tables)
	globalDump := make(map[string][]map[string]any, len(globalTables))
	globalCounts := make(map[string]int, len(globalTables))
	for _, t := range globalTables {
		rows, err := dumpTable(db, t, "")
		if err != nil {
			return nil, fmt.Errorf("archive: dump global table %s: %w", t.name, err)
		}
		globalDump[t.name] = rows
		globalCounts[t.name] = len(rows)
	}

	archivedAt := time.Now().UTC().Format(time.RFC3339)
	res := &ArchiveV1Result{
		OutDir:       outDir,
		PerNSCounts:  make(map[string]map[string]int),
		GlobalCounts: globalCounts,
	}

	namespacedTables := filterNamespaced(tables)
	for _, ns := range namespaces {
		tablesOut := make(map[string][]map[string]any, len(tables))
		counts := make(map[string]int, len(tables))

		// Namespaced tables: this namespace's slice only.
		for _, t := range namespacedTables {
			rows, err := dumpTable(db, t, ns)
			if err != nil {
				return nil, fmt.Errorf("archive: dump %s for ns %q: %w", t.name, ns, err)
			}
			tablesOut[t.name] = rows
			counts[t.name] = len(rows)
			res.TotalRows += len(rows)
		}
		// Global tables: included verbatim in every namespace file.
		for name, rows := range globalDump {
			tablesOut[name] = rows
			if _, ok := counts[name]; !ok {
				counts[name] = len(rows)
			}
		}

		doc := archiveDoc{
			Format:        v1ArchiveFormat,
			SchemaVersion: schemaVersion,
			ArchivedAt:    archivedAt,
			SourceDB:      abs,
			Namespace:     ns,
			Tables:        tablesOut,
		}
		fname := sanitizeNamespace(ns) + ".json"
		fpath := filepath.Join(outDir, fname)
		if err := writeJSON(fpath, doc); err != nil {
			return nil, fmt.Errorf("archive: write %s: %w", fpath, err)
		}

		res.Namespaces = append(res.Namespaces, ns)
		res.PerNSCounts[ns] = counts
	}

	sort.Strings(res.Namespaces)
	return res, nil
}

// archiveDoc is the on-disk JSON shape for one namespace file.
type archiveDoc struct {
	Format        string                 `json:"format"`
	SchemaVersion int                    `json:"schema_version"`
	ArchivedAt    string                 `json:"archived_at"`
	SourceDB      string                 `json:"source_db"`
	Namespace     string                 `json:"namespace"`
	Tables        map[string][]map[string]any `json:"tables"`
}

// prepareOutDir ensures outDir exists and is safe to write. It refuses to
// overwrite a non-empty existing directory unless force is set.
func prepareOutDir(outDir string, force bool) error {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(outDir, 0755)
		}
		return fmt.Errorf("stat out dir: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}
	if !force {
		return fmt.Errorf("archive destination %s already exists and is non-empty (use --force to overwrite)", outDir)
	}
	// Wipe the existing archive so a forced re-run starts clean.
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(outDir, e.Name())); err != nil {
			return fmt.Errorf("clear out dir: %w", err)
		}
	}
	return nil
}

func readSchemaVersion(db *sql.DB) (int, error) {
	var v int
	err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM dualmem_schema_version`).Scan(&v)
	if err != nil {
		// Table may be absent on a totally fresh DB; treat as version 0.
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

// discoverTables enumerates user tables and their columns from sqlite_master +
// PRAGMA table_info. System tables (sqlite_*) are skipped.
func discoverTables(db *sql.DB) ([]tableSchema, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	tables := make([]tableSchema, 0, len(names))
	for _, n := range names {
		ts, err := loadTableSchema(db, n)
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", n, err)
		}
		tables = append(tables, ts)
	}
	return tables, nil
}

func loadTableSchema(db *sql.DB, table string) (tableSchema, error) {
	// PRAGMA table_info is a trusted, parameter-less identifier; quote the name
	// defensively using double quotes (escaped) so odd table names still parse.
	q := fmt.Sprintf(`PRAGMA table_info("%s")`, strings.ReplaceAll(table, `"`, `""`))
	rows, err := db.Query(q)
	if err != nil {
		return tableSchema{}, err
	}
	defer rows.Close()

	ts := tableSchema{
		name:      table,
		colTypes:  make(map[string]string),
		nsColumn:  "",
	}
	hasNS := false
	hasUserID := false
	for rows.Next() {
		var (
			cid     int
			name    string
			decl    string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &decl, &notnull, &dflt, &pk); err != nil {
			return tableSchema{}, err
		}
		ts.colOrder = append(ts.colOrder, name)
		ts.colTypes[name] = strings.ToUpper(decl)
		switch name {
		case "namespace":
			hasNS = true
		case "user_id":
			hasUserID = true
		}
	}
	if err := rows.Err(); err != nil {
		return tableSchema{}, err
	}
	switch {
	case hasNS:
		ts.nsColumn = "namespace"
	case hasUserID:
		ts.nsColumn = "user_id"
	}
	return ts, nil
}

// discoverNamespaces returns the sorted union of distinct namespace values
// across all namespaced tables.
func discoverNamespaces(db *sql.DB, tables []tableSchema) ([]string, error) {
	set := make(map[string]struct{})
	for _, t := range tables {
		if t.nsColumn == "" {
			continue
		}
		q := fmt.Sprintf(`SELECT DISTINCT "%s" FROM "%s"`,
			strings.ReplaceAll(t.nsColumn, `"`, `""`),
			strings.ReplaceAll(t.name, `"`, `""`))
		rows, err := db.Query(q)
		if err != nil {
			return nil, fmt.Errorf("distinct ns from %s: %w", t.name, err)
		}
		for rows.Next() {
			var v sql.NullString
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			if v.Valid && v.String != "" {
				set[v.String] = struct{}{}
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(set))
	for ns := range set {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out, nil
}

func filterGlobal(tables []tableSchema) []tableSchema {
	var out []tableSchema
	for _, t := range tables {
		if t.nsColumn == "" {
			out = append(out, t)
		}
	}
	return out
}

func filterNamespaced(tables []tableSchema) []tableSchema {
	var out []tableSchema
	for _, t := range tables {
		if t.nsColumn != "" {
			out = append(out, t)
		}
	}
	return out
}

// dumpTable reads every row of a table. When ns is non-empty and the table has
// a namespace column, rows are filtered to that namespace; otherwise all rows
// are returned. BLOB columns are base64-encoded.
func dumpTable(db *sql.DB, t tableSchema, ns string) ([]map[string]any, error) {
	var query string
	var args []any
	quotedCols := make([]string, len(t.colOrder))
	for i, c := range t.colOrder {
		quotedCols[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
	}
	quotedName := `"` + strings.ReplaceAll(t.name, `"`, `""`) + `"`
	if ns != "" && t.nsColumn != "" {
		quotedNS := `"` + strings.ReplaceAll(t.nsColumn, `"`, `""`) + `"`
		query = fmt.Sprintf(`SELECT %s FROM %s WHERE %s = ?`, strings.Join(quotedCols, ", "), quotedName, quotedNS)
		args = []any{ns}
	} else {
		query = fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(quotedCols, ", "), quotedName)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]map[string]any, 0)
	for rows.Next() {
		vals := make([]any, len(t.colOrder))
		ptrs := make([]any, len(t.colOrder))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(t.colOrder))
		for i, col := range t.colOrder {
			row[col] = normalizeValue(vals[i], t.colTypes[col])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// normalizeValue coerces a scanned cell into a JSON-friendly value. BLOB
// columns are base64-encoded. Raw []byte from non-BLOB columns (some drivers
// return text as []byte) are converted to string. nil stays nil.
func normalizeValue(v any, declType string) any {
	if v == nil {
		return nil
	}
	if isBlobType(declType) {
		if b, ok := v.([]byte); ok {
			return base64.StdEncoding.EncodeToString(b)
		}
		return v
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

func isBlobType(decl string) bool {
	return strings.Contains(strings.ToUpper(decl), "BLOB")
}

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// sanitizeNamespace turns a namespace like "claude:geoffreyengram" into a
// filesystem-safe filename stem ("claude_geoffreyengram"). Path separators,
// colons, and any non-printable run collapse to a single underscore; the
// result is never empty.
func sanitizeNamespace(ns string) string {
	s := unsafeFilename.ReplaceAllString(ns, "_")
	s = strings.Trim(s, "._-")
	if s == "" {
		s = "unnamed"
	}
	return s
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0644)
}
