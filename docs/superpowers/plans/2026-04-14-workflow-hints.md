# Lightweight Workflow Hints Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Proactively surface one-liner workflow hints when agents read files or work on ticketed tasks, so they know relevant workflow context exists without searching.

**Architecture:** Two independent hint paths — file-context hints (injected into `FileAnnotations()`) and ticket-prefix hints (injected into `AssembleContextWith()`). Both produce compact one-liners pointing to full workflow memories. A new `WorkflowHint` struct and two store query methods provide the data layer.

**Tech Stack:** Go, SQLite, existing dualmem engine

---

### Task 1: Add WorkflowHint Type

**Files:**
- Modify: `dualmem/types.go:276` (near Checkpoint struct)

- [ ] **Step 1: Add WorkflowHint struct to types.go**

Add after the `Checkpoint` struct (after line ~284):

```go
// WorkflowHint is a lightweight pointer to a full workflow memory.
// Produced by store queries, rendered as one-liner hints in file-context and context assembly.
type WorkflowHint struct {
	WorkflowID  string   // e.g. "issue-credentials" — parsed from [workflow:ID] prefix
	Tickets     []string // e.g. ["LC-1635", "LC-1729"] — extracted from memory text
	Summary     string   // first ~150 chars of workflow text after the [workflow:ID] prefix
	MatchedFile string   // which file triggered the match (file-context path only, empty for ticket path)
}
```

- [ ] **Step 2: Add parseWorkflowHint helper to types.go**

This parses a raw `[workflow:ID] summary text...` memory into a `WorkflowHint`. Both store methods will use it.

```go
// parseWorkflowHint extracts a WorkflowHint from a raw autopilot memory text.
// Input format: "[workflow:issue-credentials] Full summary text mentioning LC-1635..."
// Returns nil if text doesn't match the [workflow:*] pattern.
func parseWorkflowHint(text string) *WorkflowHint {
	if !strings.HasPrefix(text, "[workflow:") {
		return nil
	}
	closeBracket := strings.Index(text, "]")
	if closeBracket < 0 {
		return nil
	}
	id := text[len("[workflow:"):closeBracket]
	summary := ""
	if closeBracket+2 < len(text) {
		summary = strings.TrimSpace(text[closeBracket+1:])
		if len(summary) > 150 {
			summary = summary[:147] + "..."
		}
	}

	// Extract ticket prefixes from the full text
	var tickets []string
	// Use a simple regex inline — matches LC-1635, PROJ-42, etc.
	re := regexp.MustCompile(`[A-Z]+-\d+`)
	for _, m := range re.FindAllString(text, -1) {
		tickets = append(tickets, m)
	}
	// Deduplicate
	seen := make(map[string]bool)
	deduped := tickets[:0]
	for _, t := range tickets {
		if !seen[t] {
			seen[t] = true
			deduped = append(deduped, t)
		}
	}

	return &WorkflowHint{
		WorkflowID: id,
		Tickets:    deduped,
		Summary:    summary,
	}
}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./...`
Expected: clean build, no errors

- [ ] **Step 4: Commit**

```bash
git add dualmem/types.go
git commit -m "feat(workflow-hints): add WorkflowHint type and parseWorkflowHint helper"
```

---

### Task 2: Add WorkflowHint Store Methods + Tests

**Files:**
- Modify: `dualmem/store_sqlite.go:596` (after `GetWorkflowMemoryCount`)
- Create: `dualmem/workflow_hints_test.go`

- [ ] **Step 1: Write the failing test for GetWorkflowHintsForFiles**

Create `dualmem/workflow_hints_test.go`:

```go
package dualmem

import (
	"testing"
	"time"
)

func insertWorkflowMemory(t *testing.T, store *SQLiteStore, userID, workflowID, text string, files []string) {
	t.Helper()
	dm := &DetailMemory{
		ID:        "wftest_" + workflowID,
		Text:      text,
		Type:      "autopilot",
		Salience:  0.9,
		Files:     files,
		Sector:    "semantic",
		CreatedAt: time.Now(),
	}
	if err := store.InsertDetail(dm, make([]float32, 768), userID); err != nil {
		t.Fatalf("InsertDetail: %v", err)
	}
}

func TestGetWorkflowHintsForFiles(t *testing.T) {
	store := newTestStore(t)

	// Insert a workflow memory that references auth.go and middleware.go
	insertWorkflowMemory(t, store, "user1", "guardian-approval",
		"[workflow:guardian-approval] Guardian approval gate for managed children. Involves LC-1731 ticket. Data flows from credential issuance through guardian verification.",
		[]string{"auth.go", "middleware.go", "routes/inbox.ts"},
	)

	// Insert a non-workflow autopilot memory (area analysis) — should NOT match
	dm := &DetailMemory{
		ID:        "area_auth",
		Text:      "auth/ area: handles JWT validation and session management",
		Type:      "autopilot",
		Salience:  0.8,
		Files:     []string{"auth.go"},
		Sector:    "semantic",
		CreatedAt: time.Now(),
	}
	store.InsertDetail(dm, make([]float32, 768), "user1")

	// Query for auth.go — should return only the workflow hint
	hints, err := store.GetWorkflowHintsForFiles("user1", []string{"auth.go"})
	if err != nil {
		t.Fatalf("GetWorkflowHintsForFiles: %v", err)
	}
	if len(hints) != 1 {
		t.Fatalf("expected 1 hint, got %d", len(hints))
	}
	if hints[0].WorkflowID != "guardian-approval" {
		t.Errorf("expected workflow ID 'guardian-approval', got %q", hints[0].WorkflowID)
	}
	if hints[0].MatchedFile != "auth.go" {
		t.Errorf("expected matched file 'auth.go', got %q", hints[0].MatchedFile)
	}
	if len(hints[0].Tickets) == 0 || hints[0].Tickets[0] != "LC-1731" {
		t.Errorf("expected ticket LC-1731, got %v", hints[0].Tickets)
	}

	// Query for a file not in any workflow — should return empty
	hints, err = store.GetWorkflowHintsForFiles("user1", []string{"README.md"})
	if err != nil {
		t.Fatalf("GetWorkflowHintsForFiles empty: %v", err)
	}
	if len(hints) != 0 {
		t.Fatalf("expected 0 hints for README.md, got %d", len(hints))
	}
}

func TestGetWorkflowHintsForTickets(t *testing.T) {
	store := newTestStore(t)

	// Insert two workflow memories with different tickets
	insertWorkflowMemory(t, store, "user1", "issue-credentials",
		"[workflow:issue-credentials] Credential issuance flow for LC-1635 and LC-1729. Handles boost creation and delivery.",
		[]string{"routes/inbox.ts", "services/credential.ts"},
	)
	insertWorkflowMemory(t, store, "user1", "guardian-approval",
		"[workflow:guardian-approval] Guardian approval gate for LC-1731. Parent PIN verification before finalize.",
		[]string{"routes/inbox.ts", "middleware/guardian.ts"},
	)

	// Query for LC-1635 — should match issue-credentials only
	hints, err := store.GetWorkflowHintsForTickets("user1", []string{"LC-1635"})
	if err != nil {
		t.Fatalf("GetWorkflowHintsForTickets: %v", err)
	}
	if len(hints) != 1 {
		t.Fatalf("expected 1 hint for LC-1635, got %d", len(hints))
	}
	if hints[0].WorkflowID != "issue-credentials" {
		t.Errorf("expected 'issue-credentials', got %q", hints[0].WorkflowID)
	}

	// Query for LC-1731 — should match guardian-approval only
	hints, err = store.GetWorkflowHintsForTickets("user1", []string{"LC-1731"})
	if err != nil {
		t.Fatalf("GetWorkflowHintsForTickets: %v", err)
	}
	if len(hints) != 1 {
		t.Fatalf("expected 1 hint for LC-1731, got %d", len(hints))
	}
	if hints[0].WorkflowID != "guardian-approval" {
		t.Errorf("expected 'guardian-approval', got %q", hints[0].WorkflowID)
	}

	// Query for multiple tickets — should deduplicate if same workflow
	hints, err = store.GetWorkflowHintsForTickets("user1", []string{"LC-1635", "LC-1729"})
	if err != nil {
		t.Fatalf("GetWorkflowHintsForTickets multi: %v", err)
	}
	if len(hints) != 1 {
		t.Fatalf("expected 1 deduplicated hint, got %d", len(hints))
	}

	// Query for unknown ticket — should return empty
	hints, err = store.GetWorkflowHintsForTickets("user1", []string{"PROJ-999"})
	if err != nil {
		t.Fatalf("GetWorkflowHintsForTickets empty: %v", err)
	}
	if len(hints) != 0 {
		t.Fatalf("expected 0 hints, got %d", len(hints))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestGetWorkflowHints -v`
Expected: FAIL — `GetWorkflowHintsForFiles` and `GetWorkflowHintsForTickets` undefined

- [ ] **Step 3: Implement GetWorkflowHintsForFiles in store_sqlite.go**

Add after `GetWorkflowMemoryCount` (after line 596):

```go
// GetWorkflowHintsForFiles returns workflow hints for autopilot memories
// whose files_json contains any of the given filenames. Max 3 results per file.
func (s *SQLiteStore) GetWorkflowHintsForFiles(userID string, filenames []string) ([]WorkflowHint, error) {
	if len(filenames) == 0 {
		return nil, nil
	}

	seen := make(map[string]bool) // deduplicate by workflow ID
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
```

- [ ] **Step 4: Implement GetWorkflowHintsForTickets in store_sqlite.go**

Add directly after `GetWorkflowHintsForFiles`:

```go
// GetWorkflowHintsForTickets returns workflow hints for autopilot memories
// whose text contains any of the given ticket prefixes. Max 3 results, deduplicated.
func (s *SQLiteStore) GetWorkflowHintsForTickets(userID string, tickets []string) ([]WorkflowHint, error) {
	if len(tickets) == 0 {
		return nil, nil
	}

	// Build OR clause for ticket patterns
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
```

- [ ] **Step 5: Add necessary imports to store_sqlite.go**

Ensure `path/filepath` is in the import block (it may already be there). Check and add if needed.

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestGetWorkflowHints -v`
Expected: PASS — both `TestGetWorkflowHintsForFiles` and `TestGetWorkflowHintsForTickets` pass

- [ ] **Step 7: Commit**

```bash
git add dualmem/store_sqlite.go dualmem/workflow_hints_test.go
git commit -m "feat(workflow-hints): add store methods for file and ticket workflow hint queries"
```

---

### Task 3: Wire File-Context Hints into FileAnnotations + Tests

**Files:**
- Modify: `dualmem/dualmem.go:681-801` (FileAnnotations function)
- Modify: `dualmem/dualmem_test.go` (add test)

- [ ] **Step 1: Write the failing test**

Add to `dualmem/dualmem_test.go` after the existing `TestFileContext` function:

```go
func TestFileAnnotationsIncludesWorkflowHints(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add a workflow memory referencing auth.go
	if err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "[workflow:guardian-approval] Guardian approval gate for managed children. Involves LC-1731 ticket.",
		Type:        "autopilot",
		Salience:    0.9,
		Files:       []string{"auth.go", "middleware.go"},
	}, "user1"); err != nil {
		t.Fatalf("AddWithOptions workflow: %v", err)
	}

	// Add a regular warning for the same file
	if err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "Don't modify auth.go hot path",
		Type:        "warning",
		Salience:    0.9,
		Files:       []string{"auth.go"},
	}, "user1"); err != nil {
		t.Fatalf("AddWithOptions warning: %v", err)
	}

	// FileAnnotations should return both the warning and the workflow hint
	anns := engine.FileAnnotations(ctx, "user1", []string{"auth.go"}, 10)

	hasWarning := false
	hasWorkflow := false
	for _, ann := range anns {
		if ann.Type == "warning" {
			hasWarning = true
		}
		if ann.Type == "workflow" {
			hasWorkflow = true
			if ann.Source != "workflow_hint" {
				t.Errorf("expected source 'workflow_hint', got %q", ann.Source)
			}
			if !strings.Contains(ann.Text, "guardian-approval") {
				t.Errorf("expected workflow hint text to contain 'guardian-approval', got %q", ann.Text)
			}
		}
	}
	if !hasWarning {
		t.Error("expected warning annotation for auth.go")
	}
	if !hasWorkflow {
		t.Error("expected workflow hint annotation for auth.go")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestFileAnnotationsIncludesWorkflowHints -v`
Expected: FAIL — no workflow annotation returned (FileAnnotations doesn't query workflow hints yet)

- [ ] **Step 3: Add workflow hint source to FileAnnotations**

In `dualmem/dualmem.go`, in the `FileAnnotations` function, add a new section after the checkpoint source (after line 789, before the sort at line 792). Insert between the checkpoint block and the sort:

```go
	// 4. Workflow hints — lightweight pointers to autopilot workflow memories
	workflowHints, _ := e.store.GetWorkflowHintsForFiles(namespace, filePaths)
	for _, wh := range workflowHints {
		whID := "wh_" + wh.WorkflowID
		if seenIDs[whID] {
			continue
		}
		seenIDs[whID] = true
		ticketStr := ""
		if len(wh.Tickets) > 0 {
			ticketStr = " (" + strings.Join(wh.Tickets, ", ") + ")"
		}
		hintText := fmt.Sprintf(`"%s"%s — search "workflow:%s" for full detail`, wh.Summary, ticketStr, wh.WorkflowID)
		if len(hintText) > 200 {
			hintText = hintText[:197] + "..."
		}
		annotations = append(annotations, Annotation{
			FilePath: wh.MatchedFile,
			Type:     "workflow",
			Text:     hintText,
			Source:   "workflow_hint",
			Salience: 0.85, // between warning (0.9) and knowledge (0.8)
			MemoryID: whID,
		})
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestFileAnnotationsIncludesWorkflowHints -v`
Expected: PASS

- [ ] **Step 5: Run all existing FileContext tests to check for regressions**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestFileContext -v`
Expected: PASS — existing tests unaffected (they don't have workflow memories)

- [ ] **Step 6: Commit**

```bash
git add dualmem/dualmem.go dualmem/dualmem_test.go
git commit -m "feat(workflow-hints): wire workflow hints into FileAnnotations"
```

---

### Task 4: Wire Ticket-Prefix Hints into AssembleContextWith + Tests

**Files:**
- Modify: `dualmem/dualmem.go:933-942` (after checkpoints, before file context)
- Modify: `dualmem/dualmem_test.go` (add test)

- [ ] **Step 1: Write the failing test**

Add to `dualmem/dualmem_test.go`:

```go
func TestAssembleContextWorkflowHints(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add a workflow memory with ticket LC-1635
	if err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "[workflow:issue-credentials] Credential issuance flow for LC-1635 and LC-1729. Handles boost creation.",
		Type:        "autopilot",
		Salience:    0.9,
		Files:       []string{"routes/inbox.ts"},
	}, "user1"); err != nil {
		t.Fatalf("AddWithOptions workflow: %v", err)
	}

	// Create a checkpoint with ticket prefix in the task field
	if err := engine.SaveCheckpoint(ctx, "user1", Checkpoint{
		Task:        "LC-1635 credential fixes",
		Status:      "in_progress",
		FilesActive: []string{"routes/inbox.ts"},
	}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	// Assemble context — should include [Workflow Hints] section
	result := engine.AssembleContext(ctx, "user1", "fixing the issuance bug", 5000)

	if !strings.Contains(result.Text, "[Workflow Hints]") {
		t.Error("expected [Workflow Hints] section in context output")
		t.Logf("Got: %s", result.Text)
	}
	if !strings.Contains(result.Text, "issue-credentials") {
		t.Error("expected 'issue-credentials' workflow ID in hints")
	}
}

func TestAssembleContextNoWorkflowHintsWithoutTickets(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	// Add a workflow memory
	if err := engine.AddWithOptions(ctx, MemoryInput{
		UserMessage: "[workflow:issue-credentials] Credential issuance flow for LC-1635.",
		Type:        "autopilot",
		Salience:    0.9,
		Files:       []string{"routes/inbox.ts"},
	}, "user1"); err != nil {
		t.Fatalf("AddWithOptions workflow: %v", err)
	}

	// Create a checkpoint WITHOUT ticket prefix
	if err := engine.SaveCheckpoint(ctx, "user1", Checkpoint{
		Task:        "general refactoring",
		Status:      "in_progress",
		FilesActive: []string{"routes/inbox.ts"},
	}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	// Assemble context — should NOT include [Workflow Hints] section
	result := engine.AssembleContext(ctx, "user1", "general exploration", 5000)

	if strings.Contains(result.Text, "[Workflow Hints]") {
		t.Error("expected no [Workflow Hints] section without ticket prefixes")
		t.Logf("Got: %s", result.Text)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestAssembleContextWorkflowHints -v`
Expected: FAIL — no `[Workflow Hints]` section exists yet

- [ ] **Step 3: Add extractTicketsFromCheckpoints helper**

Add to `dualmem/dualmem.go` near `extractFileHints` (around line 1168):

```go
// extractTicketsFromCheckpoints scans checkpoint Task and Text fields for ticket prefixes.
// Returns deduplicated ticket IDs like ["LC-1635", "LC-1729"].
func extractTicketsFromCheckpoints(checkpoints []Checkpoint) []string {
	re := regexp.MustCompile(`[A-Z]+-\d+`)
	seen := make(map[string]bool)
	var tickets []string

	for _, cp := range checkpoints {
		for _, m := range re.FindAllString(cp.Task, -1) {
			if !seen[m] {
				seen[m] = true
				tickets = append(tickets, m)
			}
		}
		// Also scan completed/remaining steps for ticket refs
		for _, step := range cp.CompletedSteps {
			for _, m := range re.FindAllString(step, -1) {
				if !seen[m] {
					seen[m] = true
					tickets = append(tickets, m)
				}
			}
		}
		for _, step := range cp.RemainingSteps {
			for _, m := range re.FindAllString(step, -1) {
				if !seen[m] {
					seen[m] = true
					tickets = append(tickets, m)
				}
			}
		}
	}

	return tickets
}
```

- [ ] **Step 4: Add [Workflow Hints] section to AssembleContextWith**

In `dualmem/dualmem.go`, in `AssembleContextWith`, add after the checkpoint rendering (after line 942) and before the file context section (before line 944):

```go
	// Workflow Hints — ticket-prefix lookup from active checkpoints
	ticketPrefixes := extractTicketsFromCheckpoints(checkpoints)
	if len(ticketPrefixes) > 0 {
		workflowHints, _ := e.store.GetWorkflowHintsForTickets(userID, ticketPrefixes)
		if len(workflowHints) > 0 {
			var hintLines []string
			for _, wh := range workflowHints {
				ticketStr := ""
				if len(wh.Tickets) > 0 {
					ticketStr = " (" + strings.Join(wh.Tickets, ", ") + ")"
				}
				hintLines = append(hintLines, fmt.Sprintf(`📎 "%s"%s — search "workflow:%s"`, wh.Summary, ticketStr, wh.WorkflowID))
			}
			whText := "[Workflow Hints]\n" + strings.Join(hintLines, "\n")
			whTokens := estimateTokens(whText)
			if tokensUsed+whTokens <= tokenBudget {
				parts = append(parts, whText)
				sources = append(sources, SourceRef{Type: "workflow_hint", ID: "tickets"})
				tokensUsed += whTokens
			}
		}
	}
```

- [ ] **Step 5: Add `regexp` import if not already present in dualmem.go**

Check the imports at the top of `dualmem/dualmem.go`. If `regexp` is not already imported, add it.

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -run TestAssembleContextWorkflowHints -v`
Expected: PASS — both tests pass

- [ ] **Step 7: Run full test suite to check for regressions**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./dualmem/ -v -count=1 2>&1 | tail -20`
Expected: All tests pass

- [ ] **Step 8: Commit**

```bash
git add dualmem/dualmem.go dualmem/dualmem_test.go
git commit -m "feat(workflow-hints): wire ticket-prefix hints into context assembly"
```

---

### Task 5: Update file-context CLI Rendering

**Files:**
- Modify: `cmd/dualmem/main.go:2384-2402` (gate mode type icons and rendering)

- [ ] **Step 1: Add workflow icon to typeIcons map**

In `cmd/dualmem/main.go` at line 2384, add the workflow entry to the `typeIcons` map:

```go
		typeIcons := map[string]string{
			"warning":    "⚠",
			"decision":   "★",
			"continuity": "↻",
			"knowledge":  "📖",
			"checkpoint": "📋",
			"map":        "🗺",
			"trace":      "🔍",
			"seed":       "🌱",
			"workflow":   "📎",
		}
```

- [ ] **Step 2: Verify it compiles and build**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build ./cmd/dualmem/`
Expected: clean build

- [ ] **Step 3: Commit**

```bash
git add cmd/dualmem/main.go
git commit -m "feat(workflow-hints): add workflow icon to file-context CLI output"
```

---

### Task 6: Update CLAUDE.md with Workflow Hints Documentation

**Files:**
- Modify: `~/.claude/CLAUDE.md`

- [ ] **Step 1: Add workflow hints section to CLAUDE.md**

Find the "When to search explicitly" section and add after it:

```markdown
### Workflow hints
When you see 📎 Workflow hints in file-context or context output, these are
lightweight pointers to full workflow analyses discovered by autopilot. If
the hint is relevant to your current task, expand it with:
```
dualmem search "workflow:<id>"
```
This gives you the full data flow, trigger, cross-service boundaries, and
error handling for that workflow.
```

- [ ] **Step 2: Commit**

```bash
git add ~/.claude/CLAUDE.md
git commit -m "docs: add workflow hints section to CLAUDE.md"
```

---

### Task 7: Integration Test — End-to-End Verification

**Files:**
- No new files — manual verification against real data

- [ ] **Step 1: Build and install the updated binary**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go build -o ~/go/bin/dualmem ./cmd/dualmem/`
Expected: clean build, binary installed

- [ ] **Step 2: Test file-context hints against LearnCard data**

Run:
```bash
cd ~/Work/LearnCard && source ~/.claude/hooks/dualmem-env.sh
~/go/bin/dualmem file-context apps/learn-card-app/src/components/boost/boostCMS/BoostCMSHeader/BoostCMSHeader.tsx
```
Expected: If this file is referenced in a workflow memory, a `📎 [workflow]` line appears. If no workflow memories reference it, only existing annotations show (no error, no crash).

- [ ] **Step 3: Test file-context with a file that has no workflow associations**

Run:
```bash
~/go/bin/dualmem file-context README.md
```
Expected: No `📎` lines. No errors. Existing behavior unchanged.

- [ ] **Step 4: Test ticket-prefix hints in context assembly**

Run:
```bash
~/go/bin/dualmem checkpoint --task "LC-1635 credential fixes" --status in_progress --files "routes/inbox.ts" --ns "claude:LearnCard"
~/go/bin/dualmem context "fixing the issuance bug" --budget 3000 --ns "claude:LearnCard"
```
Expected: If a `[workflow:*]` memory mentions LC-1635, a `[Workflow Hints]` section appears in the output.

- [ ] **Step 5: Test context assembly without ticket prefixes**

Run:
```bash
~/go/bin/dualmem context "general exploration" --budget 3000 --ns "claude:LearnCard"
```
Expected: No `[Workflow Hints]` section. Existing behavior unchanged.

- [ ] **Step 6: Run full test suite one final time**

Run: `cd /Users/donny/Projects/2026/geoffreyengram && go test ./... 2>&1 | tail -5`
Expected: All packages pass
