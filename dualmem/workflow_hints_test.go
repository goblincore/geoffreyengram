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

	insertWorkflowMemory(t, store, "user1", "guardian-approval",
		"[workflow:guardian-approval] Guardian approval gate for managed children. Involves LC-1731 ticket. Data flows from credential issuance through guardian verification.",
		[]string{"auth.go", "middleware.go", "routes/inbox.ts"},
	)

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

	insertWorkflowMemory(t, store, "user1", "issue-credentials",
		"[workflow:issue-credentials] Credential issuance flow for LC-1635 and LC-1729. Handles boost creation and delivery.",
		[]string{"routes/inbox.ts", "services/credential.ts"},
	)
	insertWorkflowMemory(t, store, "user1", "guardian-approval",
		"[workflow:guardian-approval] Guardian approval gate for LC-1731. Parent PIN verification before finalize.",
		[]string{"routes/inbox.ts", "middleware/guardian.ts"},
	)

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

	hints, err = store.GetWorkflowHintsForTickets("user1", []string{"LC-1635", "LC-1729"})
	if err != nil {
		t.Fatalf("GetWorkflowHintsForTickets multi: %v", err)
	}
	if len(hints) != 1 {
		t.Fatalf("expected 1 deduplicated hint, got %d", len(hints))
	}

	hints, err = store.GetWorkflowHintsForTickets("user1", []string{"PROJ-999"})
	if err != nil {
		t.Fatalf("GetWorkflowHintsForTickets empty: %v", err)
	}
	if len(hints) != 0 {
		t.Fatalf("expected 0 hints, got %d", len(hints))
	}
}
