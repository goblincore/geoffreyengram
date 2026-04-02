# Proactive Memory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add file-scoped memory recall via Read hook, enhanced wrap-up via distill, and effort-based saving guidance.

**Architecture:** Three coordinated features: (1) new `FileContext()` engine method + `FileIndex()` for fast file-scoped memory lookup, surfaced by a Claude Code Read hook; (2) persist session summary in `Distill()` and update wrap-up skill to use distill; (3) effort marker guidance in CLAUDE.md and skills.

**Tech Stack:** Go (engine + CLI), SQLite (queries), Bash (hook script), Markdown (skills)

---

### Task 1: Store — Add `GetDetailsByFiles` query

New SQLite query method that finds detail memories matching a filename, filtered by type.

**Files:**
- Modify: `dualmem/types.go:529-540` (Store interface)
- Modify: `dualmem/store_sqlite.go` (new method)
- Test: `dualmem/store_sqlite_test.go`

- [ ] **Step 1: Write the failing test**

In `dualmem/store_sqlite_test.go`, add:

```go
func TestGetDetailsByFiles(t *testing.T) {
	store := newTestSQLiteStore(t)

	// Insert memories with different types and files
	store.InsertDetail(&DetailMemory{
		ID:              "warn-1",
		Text:            "Don't touch: rateLimiter cleanup() skips nil check intentionally",
		ImportanceScore: 0.9,
		Sector:          "warning",
		Type:            "warning",
		Salience:        0.9,
		Files:           []string{"rate_limiter.go"},
	}, make([]float32, 768), "ns1")

	store.InsertDetail(&DetailMemory{
		ID:              "dec-1",
		Text:            "Chose SQLite over Postgres for zero-setup",
		ImportanceScore: 0.85,
		Sector:          "decision",
		Type:            "decision",
		Salience:        0.85,
		Files:           []string{"store_sqlite.go", "config.go"},
	}, make([]float32, 768), "ns1")

	store.InsertDetail(&DetailMemory{
		ID:              "gen-1",
		Text:            "General note about rate limiter",
		ImportanceScore: 0.5,
		Sector:          "semantic",
		Type:            "general",
		Salience:        0.5,
		Files:           []string{"rate_limiter.go"},
	}, make([]float32, 768), "ns1")

	store.InsertDetail(&DetailMemory{
		ID:              "map-1",
		Text:            "Auth flow: auth.go -> middleware.go -> jwt.go",
		ImportanceScore: 0.7,
		Sector:          "procedural",
		Type:            "map",
		Salience:        0.7,
		Files:           []string{"auth.go", "middleware.go", "jwt.go"},
	}, make([]float32, 768), "ns1")

	// Search for "rate_limiter.go" — should find warning but not general
	results, err := store.GetDetailsByFiles("ns1", "rate_limiter.go", []string{"warning", "decision", "map"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for rate_limiter.go, got %d", len(results))
	}
	if results[0].ID != "warn-1" {
		t.Errorf("expected warn-1, got %s", results[0].ID)
	}

	// Search for "store_sqlite.go" — should find decision
	results, err = store.GetDetailsByFiles("ns1", "store_sqlite.go", []string{"warning", "decision", "map"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for store_sqlite.go, got %d", len(results))
	}
	if results[0].ID != "dec-1" {
		t.Errorf("expected dec-1, got %s", results[0].ID)
	}

	// Search for "auth.go" — should find map
	results, err = store.GetDetailsByFiles("ns1", "auth.go", []string{"warning", "decision", "map"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for auth.go, got %d", len(results))
	}

	// Search for nonexistent file — should return empty
	results, err = store.GetDetailsByFiles("ns1", "nonexistent.go", []string{"warning", "decision", "map"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for nonexistent.go, got %d", len(results))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestGetDetailsByFiles -v`
Expected: compilation error — `GetDetailsByFiles` not defined

- [ ] **Step 3: Add `GetDetailsByFiles` to the Store interface**

In `dualmem/types.go`, after the `GetDetailsByType` line (~540), add:

```go
	GetDetailsByFiles(userID, filename string, types []string, limit int) ([]detailWithVector, error)
```

- [ ] **Step 4: Implement `GetDetailsByFiles` on SQLiteStore**

In `dualmem/store_sqlite.go`, add after `GetDetailsByType`:

```go
func (s *SQLiteStore) GetDetailsByFiles(userID, filename string, types []string, limit int) ([]detailWithVector, error) {
	if len(types) == 0 {
		return nil, nil
	}
	// Build type placeholders
	placeholders := make([]string, len(types))
	args := make([]interface{}, 0, len(types)+3)
	args = append(args, userID)
	for i, t := range types {
		placeholders[i] = "?"
		args = append(args, t)
	}
	// Use JSON array search — files_json contains entries like "rate_limiter.go"
	// Match basename anywhere in the JSON array
	pattern := "%" + filename + "%"
	args = append(args, pattern, limit)

	query := fmt.Sprintf(`
		SELECT id, user_id, text, embedding, importance_score, sector, entities_json, session_id, salience, created_at, last_accessed_at, access_count, type, files_json
		FROM detail_memories
		WHERE user_id = ? AND type IN (%s) AND files_json LIKE ?
		ORDER BY salience DESC
		LIMIT ?`, strings.Join(placeholders, ","))

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
```

Note: Requires `"fmt"` and `"strings"` imports in `store_sqlite.go` — check if already present.

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestGetDetailsByFiles -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
git add dualmem/types.go dualmem/store_sqlite.go dualmem/store_sqlite_test.go
git commit -m "feat: add GetDetailsByFiles store query for file-scoped memory lookup"
```

---

### Task 2: Store — Add `GetFilesWithMemories` query

Returns the set of basenames that have associated warning/decision/map memories. Used to generate the file index.

**Files:**
- Modify: `dualmem/types.go:529` (Store interface)
- Modify: `dualmem/store_sqlite.go`
- Test: `dualmem/store_sqlite_test.go`

- [ ] **Step 1: Write the failing test**

In `dualmem/store_sqlite_test.go`, add:

```go
func TestGetFilesWithMemories(t *testing.T) {
	store := newTestSQLiteStore(t)

	store.InsertDetail(&DetailMemory{
		ID: "w1", Text: "warning about foo", ImportanceScore: 0.9,
		Sector: "warning", Type: "warning", Salience: 0.9,
		Files: []string{"foo.go", "bar.go"},
	}, make([]float32, 768), "ns1")

	store.InsertDetail(&DetailMemory{
		ID: "g1", Text: "general note", ImportanceScore: 0.5,
		Sector: "semantic", Type: "general", Salience: 0.5,
		Files: []string{"baz.go"},
	}, make([]float32, 768), "ns1")

	store.InsertDetail(&DetailMemory{
		ID: "d1", Text: "decision about qux", ImportanceScore: 0.8,
		Sector: "decision", Type: "decision", Salience: 0.8,
		Files: []string{"qux.go", "foo.go"},
	}, make([]float32, 768), "ns1")

	files, err := store.GetFilesWithMemories("ns1", []string{"warning", "decision", "map"})
	if err != nil {
		t.Fatal(err)
	}

	// Should contain foo.go, bar.go, qux.go but NOT baz.go (general type)
	fileSet := make(map[string]bool)
	for _, f := range files {
		fileSet[f] = true
	}
	if !fileSet["foo.go"] {
		t.Error("expected foo.go in results")
	}
	if !fileSet["bar.go"] {
		t.Error("expected bar.go in results")
	}
	if !fileSet["qux.go"] {
		t.Error("expected qux.go in results")
	}
	if fileSet["baz.go"] {
		t.Error("baz.go (general type) should not be in results")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestGetFilesWithMemories -v`
Expected: compilation error — `GetFilesWithMemories` not defined

- [ ] **Step 3: Add to Store interface**

In `dualmem/types.go`, after the `GetDetailsByFiles` line added in Task 1:

```go
	GetFilesWithMemories(userID string, types []string) ([]string, error)
```

- [ ] **Step 4: Implement on SQLiteStore**

In `dualmem/store_sqlite.go`, add after `GetDetailsByFiles`:

```go
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

	fileSet := make(map[string]bool)
	for rows.Next() {
		var filesJSON string
		if err := rows.Scan(&filesJSON); err != nil {
			return nil, err
		}
		for _, f := range decodeStringSlice(filesJSON) {
			// Store basenames for index matching
			base := filepath.Base(f)
			fileSet[base] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]string, 0, len(fileSet))
	for f := range fileSet {
		result = append(result, f)
	}
	sort.Strings(result)
	return result, nil
}
```

Note: Requires `"path/filepath"` and `"sort"` imports in `store_sqlite.go` — check if already present.

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestGetFilesWithMemories -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
git add dualmem/types.go dualmem/store_sqlite.go dualmem/store_sqlite_test.go
git commit -m "feat: add GetFilesWithMemories store query for file index generation"
```

---

### Task 3: Engine — Add `FileContext()` and `FileIndex()` methods

**Files:**
- Modify: `dualmem/dualmem.go` (new methods)
- Test: `dualmem/dualmem_test.go`

- [ ] **Step 1: Write the failing test for FileContext**

In `dualmem/dualmem_test.go`, add:

```go
func TestFileContext(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add a warning associated with rate_limiter.go
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Don't touch: rateLimiter cleanup() skips nil check intentionally for hot path",
		Type:        "warning",
		Salience:    0.9,
		Files:       []string{"rate_limiter.go"},
	}, "ns1")

	// Add a decision associated with store_sqlite.go
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Chose SQLite over Postgres for zero-setup CLI",
		Type:        "decision",
		Salience:    0.85,
		Files:       []string{"store_sqlite.go", "config.go"},
	}, "ns1")

	// Add a general memory (should not appear in file context)
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "General note about the project",
		Type:        "",
		Salience:    0.5,
		Files:       []string{"rate_limiter.go"},
	}, "ns1")

	// Query for rate_limiter.go
	results, err := engine.FileContext(ctx, "ns1", "rate_limiter.go", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for rate_limiter.go, got %d", len(results))
	}
	if results[0].Type != "warning" {
		t.Errorf("expected warning, got %s", results[0].Type)
	}

	// Query for nonexistent file
	results, err = engine.FileContext(ctx, "ns1", "nonexistent.go", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestFileContext -v`
Expected: compilation error — `FileContext` not defined

- [ ] **Step 3: Implement `FileContext`**

In `dualmem/dualmem.go`, add after the `DualSearch` method:

```go
// FileContext returns detail memories associated with a specific file.
// Only returns high-signal types (warning, decision, map). No embedding needed.
func (e *Engine) FileContext(ctx context.Context, userID string, filename string, limit int) ([]DetailMemory, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	basename := filepath.Base(filename)
	if limit <= 0 {
		limit = 5
	}

	highSignalTypes := []string{"warning", "decision", "map"}
	details, err := e.store.GetDetailsByFiles(userID, basename, highSignalTypes, limit)
	if err != nil {
		return nil, fmt.Errorf("dualmem: file context: %w", err)
	}

	// Convert to public DetailMemory (strip vectors)
	result := make([]DetailMemory, len(details))
	for i, d := range details {
		result[i] = DetailMemory{
			ID:              d.ID,
			Text:            d.Text,
			ImportanceScore: d.ImportanceScore,
			Sector:          d.Sector,
			Entities:        d.Entities,
			Type:            d.Type,
			Files:           d.Files,
			Salience:        d.Salience,
			SessionID:       d.SessionID,
			CreatedAt:       d.CreatedAt,
		}
	}
	return result, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestFileContext -v`
Expected: PASS

- [ ] **Step 5: Write the failing test for FileIndex**

In `dualmem/dualmem_test.go`, add:

```go
func TestFileIndex(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add memories with files
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Warning about foo", Type: "warning", Salience: 0.9,
		Files: []string{"foo.go"},
	}, "ns1")
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Decision about bar", Type: "decision", Salience: 0.8,
		Files: []string{"bar.go", "baz.go"},
	}, "ns1")
	// General (should not appear in index)
	engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "General note", Salience: 0.5,
		Files: []string{"qux.go"},
	}, "ns1")

	files, err := engine.FileIndex(ctx, "ns1")
	if err != nil {
		t.Fatal(err)
	}

	fileSet := make(map[string]bool)
	for _, f := range files {
		fileSet[f] = true
	}

	if !fileSet["foo.go"] {
		t.Error("expected foo.go in index")
	}
	if !fileSet["bar.go"] {
		t.Error("expected bar.go in index")
	}
	if !fileSet["baz.go"] {
		t.Error("expected baz.go in index")
	}
	if fileSet["qux.go"] {
		t.Error("qux.go (general) should not be in index")
	}
}
```

- [ ] **Step 6: Implement `FileIndex`**

In `dualmem/dualmem.go`, add after `FileContext`:

```go
// FileIndex returns all basenames that have associated high-signal memories.
// Used to generate the file index for the Read hook fast-path.
func (e *Engine) FileIndex(ctx context.Context, userID string) ([]string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	highSignalTypes := []string{"warning", "decision", "map"}
	files, err := e.store.GetFilesWithMemories(userID, highSignalTypes)
	if err != nil {
		return nil, fmt.Errorf("dualmem: file index: %w", err)
	}

	// Also include files from knowledge docs
	docs, err := e.store.GetKnowledgeDocs(userID)
	if err == nil {
		fileSet := make(map[string]bool)
		for _, f := range files {
			fileSet[f] = true
		}
		for _, doc := range docs {
			for _, f := range doc.Files {
				base := filepath.Base(f)
				if !fileSet[base] {
					fileSet[base] = true
					files = append(files, base)
				}
			}
		}
	}

	return files, nil
}
```

- [ ] **Step 7: Run both tests**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run "TestFileContext|TestFileIndex" -v`
Expected: both PASS

- [ ] **Step 8: Commit**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
git add dualmem/dualmem.go dualmem/dualmem_test.go
git commit -m "feat: add FileContext and FileIndex engine methods"
```

---

### Task 4: CLI — Add `file-context` and `file-index` subcommands

**Files:**
- Modify: `cmd/dualmem/main.go`

- [ ] **Step 1: Add `file-context` command**

In `cmd/dualmem/main.go`, add to the `switch cmd` block (after `"distill":` case):

```go
	case "file-context":
		cmdFileContext(cfg)
	case "file-index":
		cmdFileIndex(cfg)
```

Add to `printUsage()`:

```go
  file-context  Get memories associated with a specific file (warnings, decisions, maps)
  file-index    Generate file index for Read hook fast-path filtering
```

Then add the command functions:

```go
func cmdFileContext(cfg CLIConfig) {
	fs := flag.NewFlagSet("file-context", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	jsonOut := fs.Bool("json", false, "JSON output")
	limit := fs.Int("limit", 5, "Max results")
	fs.Parse(os.Args[2:])

	filename := strings.Join(fs.Args(), " ")
	if filename == "" {
		fmt.Fprintln(os.Stderr, "error: filename required")
		fmt.Fprintln(os.Stderr, "usage: dualmem file-context <filename> [--ns <namespace>] [--json]")
		os.Exit(1)
	}

	namespace := resolveNamespace(*ns, cfg)

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ctx := context.Background()
	results, err := engine.FileContext(ctx, namespace, filename, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		// Silent exit — no memories for this file
		return
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(results)
		return
	}

	for _, r := range results {
		fmt.Printf("[%s] %s (%.2f, %s)\n", r.Type, r.Text, r.Salience, r.CreatedAt.Format("2006-01-02"))
	}
}

func cmdFileIndex(cfg CLIConfig) {
	fs := flag.NewFlagSet("file-index", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	namespace := resolveNamespace(*ns, cfg)

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ctx := context.Background()
	files, err := engine.FileIndex(ctx, namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Write to /tmp/dualmem-file-index-<hash>.json
	nsHash := fmt.Sprintf("%x", md5Hash(namespace))[:8]
	indexPath := filepath.Join(os.TempDir(), fmt.Sprintf("dualmem-file-index-%s.json", nsHash))

	indexData := map[string]interface{}{
		"files":      files,
		"namespace":  namespace,
		"updated_at": time.Now().Format(time.RFC3339),
	}

	data, _ := json.Marshal(indexData)
	os.WriteFile(indexPath, data, 0644)

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(indexData)
	} else {
		fmt.Printf("File index: %s (%d files)\n", indexPath, len(files))
	}
}
```

- [ ] **Step 2: Add the `md5Hash` helper**

In `cmd/dualmem/main.go`, add near the top-level helpers:

```go
import "crypto/md5"

func md5Hash(s string) [16]byte {
	return md5.Sum([]byte(s))
}
```

Make sure `"crypto/md5"` is in the imports block.

- [ ] **Step 3: Run full test suite to verify nothing broke**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dualmem/`
Expected: clean build

- [ ] **Step 4: Manual smoke test**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
~/go/bin/dualmem file-context dualmem.go
~/go/bin/dualmem file-index
cat /tmp/dualmem-file-index-*.json | jq .
```

Expected: `file-context` should return any existing memories for dualmem.go (possibly empty). `file-index` should create the JSON file.

- [ ] **Step 5: Commit**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
git add cmd/dualmem/main.go
git commit -m "feat: add file-context and file-index CLI subcommands"
```

---

### Task 5: CLI — Auto-regenerate file index after `add --files` and `distill`

Trigger file index regeneration as a side effect of commands that write file-associated memories.

**Files:**
- Modify: `cmd/dualmem/main.go`

- [ ] **Step 1: Extract file index generation into a shared helper**

In `cmd/dualmem/main.go`, add a helper used by multiple commands:

```go
// regenerateFileIndex updates the file index in /tmp after memory changes.
// Best-effort: errors are silently ignored since this is an optimization.
func regenerateFileIndex(cfg CLIConfig, namespace string) {
	engine, err := newEngine(cfg)
	if err != nil {
		return
	}
	defer engine.Close()

	ctx := context.Background()
	files, err := engine.FileIndex(ctx, namespace)
	if err != nil {
		return
	}

	nsHash := fmt.Sprintf("%x", md5Hash(namespace))[:8]
	indexPath := filepath.Join(os.TempDir(), fmt.Sprintf("dualmem-file-index-%s.json", nsHash))

	indexData := map[string]interface{}{
		"files":      files,
		"namespace":  namespace,
		"updated_at": time.Now().Format(time.RFC3339),
	}

	data, _ := json.Marshal(indexData)
	os.WriteFile(indexPath, data, 0644)
}
```

- [ ] **Step 2: Call from `cmdAdd` when `--files` is provided**

In `cmdAdd`, after the successful `AddWithOptions` call and before the output, add:

```go
	if len(filesList) > 0 {
		regenerateFileIndex(cfg, namespace)
	}
```

- [ ] **Step 3: Call from `cmdDistill` after successful distillation**

In `cmdDistill`, after the successful `engine.Distill` call and before the output, add:

```go
	if result.Written > 0 {
		regenerateFileIndex(cfg, ns)
	}
```

- [ ] **Step 4: Call from `cmdContext` to refresh on session start**

In `cmdContext`, after the successful `AssembleContextWith` call, add (best-effort, non-blocking):

```go
	// Side effect: refresh file index for Read hook
	go regenerateFileIndex(cfg, namespace)
```

Note: Use goroutine so it doesn't slow down context output. The `regenerateFileIndex` creates its own engine instance so no data races.

- [ ] **Step 5: Refactor `cmdFileIndex` to reuse helper**

Update `cmdFileIndex` to call `regenerateFileIndex` and then read/print the result, avoiding code duplication. The function should still support `--json` flag for direct output.

- [ ] **Step 6: Build and verify**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dualmem/`
Expected: clean build

- [ ] **Step 7: Commit**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
git add cmd/dualmem/main.go
git commit -m "feat: auto-regenerate file index after add --files, distill, and context"
```

---

### Task 6: Read hook script and settings.json configuration

**Files:**
- Create: `~/.claude/hooks/dualmem-file-recall.sh`
- Modify: `~/.claude/settings.json`

- [ ] **Step 1: Create the hook script**

Write `~/.claude/hooks/dualmem-file-recall.sh`:

```bash
#!/bin/bash
# dualmem Read hook: inject file-scoped memories before file reads.
# Checks a pre-built file index for fast-path filtering, then queries
# dualmem for warnings/decisions/maps associated with the file.

INPUT=$(cat)
FILE_PATH=$(echo "$INPUT" | jq -r '.tool_input.file_path // empty')
[ -z "$FILE_PATH" ] && exit 0

BASENAME=$(basename "$FILE_PATH")

# Skip non-code files (images, binaries, etc.)
case "$BASENAME" in
  *.png|*.jpg|*.jpeg|*.gif|*.svg|*.ico|*.woff|*.woff2|*.ttf|*.eot|*.pdf|*.lock) exit 0 ;;
esac

# Resolve namespace from cwd
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
if [ -n "$CWD" ]; then
  NS_BASE=$(basename "$CWD")
  NS_HASH=$(echo -n "claude:${NS_BASE}" | md5 | cut -c1-8)
else
  NS_HASH="default"
fi

INDEX="/tmp/dualmem-file-index-${NS_HASH}.json"

# Fast path: check file index (skip if file has no memories)
if [ -f "$INDEX" ]; then
  if ! jq -e --arg f "$BASENAME" '.files | if . == null then false else index($f) end' "$INDEX" >/dev/null 2>&1; then
    exit 0
  fi
fi

# File is in index (or no index exists) — query for memories
RESULT=$(~/go/bin/dualmem file-context "$BASENAME" 2>/dev/null || true)
[ -z "$RESULT" ] && exit 0

echo "DUALMEM FILE CONTEXT for $BASENAME:"
echo "$RESULT"
```

- [ ] **Step 2: Make it executable**

```bash
chmod +x ~/.claude/hooks/dualmem-file-recall.sh
```

- [ ] **Step 3: Add Read hook to settings.json**

In `~/.claude/settings.json`, add to the existing `hooks.PreToolUse` array:

```json
{
  "matcher": "Read",
  "hooks": [
    {
      "type": "command",
      "command": "~/.claude/hooks/dualmem-file-recall.sh",
      "timeout": 5
    }
  ]
}
```

- [ ] **Step 4: Manual test**

First, add a test memory:
```bash
~/go/bin/dualmem add --type warning --text "Test: don't change the nil check in cleanup()" --files "rate_limiter.go" --salience 0.9
~/go/bin/dualmem file-index
```

Then verify the hook would work by simulating its input:
```bash
echo '{"tool_input":{"file_path":"/Users/donny/Projects/test/rate_limiter.go"},"cwd":"/Users/donny/Projects/2026/geoffreyengram"}' | ~/.claude/hooks/dualmem-file-recall.sh
```

Expected output:
```
DUALMEM FILE CONTEXT for rate_limiter.go:
[warning] Test: don't change the nil check in cleanup() (0.90, 2026-04-02)
```

- [ ] **Step 5: Commit**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
git add ~/.claude/hooks/dualmem-file-recall.sh
git commit -m "feat: add dualmem Read hook for file-scoped memory recall"
```

---

### Task 7: Persist session summary in `Distill()`

**Files:**
- Modify: `dualmem/distill.go`
- Test: `dualmem/distill_test.go`

- [ ] **Step 1: Write the failing test**

In `dualmem/distill_test.go`, add:

```go
func TestDistillPersistsSessionSummary(t *testing.T) {
	// Test that collectAllFiles extracts unique file paths from facts
	facts := []DistilledFact{
		{Text: "fact 1", Files: []string{"auth.go", "middleware.go"}},
		{Text: "fact 2", Files: []string{"middleware.go", "jwt.go"}},
		{Text: "fact 3", Files: nil},
	}
	files := collectAllFiles(facts)

	fileSet := make(map[string]bool)
	for _, f := range files {
		fileSet[f] = true
	}

	if len(fileSet) != 3 {
		t.Fatalf("expected 3 unique files, got %d: %v", len(fileSet), files)
	}
	if !fileSet["auth.go"] || !fileSet["middleware.go"] || !fileSet["jwt.go"] {
		t.Errorf("missing expected files: %v", files)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestDistillPersistsSessionSummary -v`
Expected: compilation error — `collectAllFiles` not defined

- [ ] **Step 3: Implement `collectAllFiles` and session summary persistence**

In `dualmem/distill.go`, add the helper function:

```go
// collectAllFiles returns the union of all file paths from distilled facts.
func collectAllFiles(facts []DistilledFact) []string {
	seen := make(map[string]bool)
	var result []string
	for _, f := range facts {
		for _, path := range f.Files {
			if !seen[path] {
				seen[path] = true
				result = append(result, path)
			}
		}
	}
	return result
}
```

Then in the `Distill()` method, after the fact-writing loop (after `result.Skipped = skipped`, around line 168) and before the "Mark as distilled" step, add:

```go
	// Step 4b: Persist session summary as a continuity memory
	if extraction.SessionSummary != "" && !opts.DryRun {
		summaryFiles := collectAllFiles(extraction.Facts)
		summaryErr := e.AddWithOptions(ctx, MemoryInput{
			UserMessage: extraction.SessionSummary,
			Type:        "continuity",
			Salience:    0.75,
			Files:       summaryFiles,
			SessionID:   sessionID,
		}, userID)
		if summaryErr == nil {
			written++
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestDistillPersistsSessionSummary -v`
Expected: PASS

- [ ] **Step 5: Run full distill tests**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run Test.*istill -v`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
git add dualmem/distill.go dualmem/distill_test.go
git commit -m "feat: persist session summary as continuity memory during distill"
```

---

### Task 8: Update wrap-up skills

**Files:**
- Modify: `~/.claude/commands/wrap-up.md`
- Modify: `/Users/donny/Projects/2026/geoffreyengram/.claude/commands/wrap-up.md`

- [ ] **Step 1: Update the global wrap-up skill**

Replace the content of `~/.claude/commands/wrap-up.md` with:

```markdown
---
description: End-of-session wrap-up — extracts and saves session learnings via distill
---

Before ending this session, extract learnings from the full session transcript.

**Step 1: Run session distillation**

```
~/go/bin/dualmem distill --auto
```

This reads the current session transcript and:
- Extracts decisions, warnings, continuity notes, and file maps
- Saves them as individual memories (deduplicates against existing)
- Persists a session summary as a continuity memory
- Auto-triggers knowledge doc synthesis if ≥3 facts were written

**Step 2: Check the output**

Review what was extracted. If distill missed an important decision or warning from this session, save it manually:

```
~/go/bin/dualmem add --type decision --text "<decision and rationale>" --files "<relevant files>" --salience 0.85
```

**Step 3: If distill fails**

If distill errors (e.g., no Gemini API key, empty session), fall back to a manual continuity note:

```
~/go/bin/dualmem add --type continuity --text "<summary: what was done, what remains, any gotchas>" --files "<key files>" --salience 0.8
```

Keep the manual summary concise (2-4 sentences). The --files flag is critical — it tells the next session exactly where to start.
```

- [ ] **Step 2: Update the project-level wrap-up skill**

Copy the same content to `/Users/donny/Projects/2026/geoffreyengram/.claude/commands/wrap-up.md`.

- [ ] **Step 3: Commit**

```bash
cd /Users/donny/Projects/2026/geoffreyengram
git add .claude/commands/wrap-up.md
git commit -m "feat: update wrap-up skill to use distill --auto"
```

---

### Task 9: Add effort-based saving guidance to CLAUDE.md

**Files:**
- Modify: `~/.claude/CLAUDE.md`

- [ ] **Step 1: Add effort-based saving section**

In `~/.claude/CLAUDE.md`, in the DualMem section (after the "What to save" subsection or at the end of the dualmem documentation), add:

```markdown
### Effort-based saving (proactive)

When you spend significant effort (>3 tool calls) discovering something non-obvious — a root cause, a file relationship, a constraint — save it immediately with `dualmem add`. Don't wait for wrap-up.

**The heuristic:** "Would a fresh Claude starting this task be surprised by what I just learned?" If yes, save it.

Examples of effort-derived learnings worth saving:
- **Root cause found after debugging:** `dualmem add --type warning --text "<root cause and why it was non-obvious>" --files "<affected files>" --salience 0.85`
- **File flow discovered after navigating:** `dualmem add --text "<A calls B which triggers C>" --files "<files in the flow>" --sector procedural`
- **Dead end identified:** `dualmem add --text "<thing> is NOT in <location> — it's in <actual location>" --sector semantic`

The wrap-up distill step catches what you miss, but saving in the moment preserves more detail.
```

- [ ] **Step 2: Verify the edit reads correctly**

Read back the section to confirm it integrates with the existing CLAUDE.md structure.

- [ ] **Step 3: Commit**

Note: `~/.claude/CLAUDE.md` is outside the repo, so this is a local-only change — no git commit needed.

---

### Task 10: Run full test suite and verify

**Files:** none (verification only)

- [ ] **Step 1: Run the full dualmem test suite**

```bash
cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -v -count=1
```

Expected: all tests pass, including new tests from Tasks 1-3 and 7.

- [ ] **Step 2: Build the binary**

```bash
cd /Users/donny/Projects/2026/geoffreyengram && go build -o ~/go/bin/dualmem ./cmd/dualmem/
```

Expected: clean build

- [ ] **Step 3: Smoke test the full flow**

```bash
# 1. Add a test warning with file association
~/go/bin/dualmem add --type warning --text "Test proactive memory: nil check in cleanup is intentional" --files "rate_limiter.go" --salience 0.9

# 2. Generate file index
~/go/bin/dualmem file-index

# 3. Check file index was created
cat /tmp/dualmem-file-index-*.json | jq .

# 4. Query file context
~/go/bin/dualmem file-context rate_limiter.go

# 5. Test hook simulation
echo '{"tool_input":{"file_path":"/Users/donny/Projects/test/rate_limiter.go"},"cwd":"/Users/donny/Projects/2026/geoffreyengram"}' | ~/.claude/hooks/dualmem-file-recall.sh
```

Expected: Steps 3-5 should show the test warning memory.

- [ ] **Step 4: Clean up test memory**

```bash
# Optional: verify with search, then let the memory stay as a real test
~/go/bin/dualmem search "nil check cleanup" --limit 1
```
