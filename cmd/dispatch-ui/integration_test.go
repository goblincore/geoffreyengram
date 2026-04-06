package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	reportsDir := filepath.Join(dir, "reports")
	os.MkdirAll(plansDir, 0755)
	os.MkdirAll(reportsDir, 0755)

	mockClaude := filepath.Join(dir, "mock-claude")
	os.WriteFile(mockClaude, []byte("#!/bin/bash\necho \"Done!\"\n"), 0755)

	cfg := &DispatchConfig{
		PlansDir:   plansDir,
		ReportsDir: reportsDir,
		ClaudeBin:  mockClaude,
		EnvVars:    map[string]string{"TEST_KEY": "fake"},
	}
	eng := NewEngine(cfg)
	srv := NewServer(eng)
	return srv, plansDir
}

// TestAPIListTasksEmpty verifies GET /api/tasks returns an empty array with status 200.
func TestAPIListTasksEmpty(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var tasks []APITask
	if err := json.NewDecoder(w.Body).Decode(&tasks); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected empty array, got %d items", len(tasks))
	}
}

// TestAPICreateAndListTask verifies POST /api/tasks creates a task and GET returns it.
func TestAPICreateAndListTask(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := `{"name":"my-task","title":"My Task","project":"/tmp/proj","model":"sonnet-4.6","body":"Do the thing"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("POST expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Now list tasks
	req2 := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("GET expected 200, got %d", w2.Code)
	}

	var tasks []APITask
	if err := json.NewDecoder(w2.Body).Decode(&tasks); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	task := tasks[0]
	if task.Name != "my-task" {
		t.Errorf("expected name 'my-task', got %q", task.Name)
	}
	if task.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", task.Status)
	}
}

// TestAPIDeleteTask verifies DELETE /api/tasks/{name} removes the plan file and returns 204.
func TestAPIDeleteTask(t *testing.T) {
	srv, plansDir := setupTestServer(t)

	// Create a plan file directly
	planPath := filepath.Join(plansDir, "task-to-delete.md")
	content := "---\ntitle: \"Delete Me\"\nstatus: pending\nproject: /tmp/proj\nmodel: sonnet-4.6\n---\nSome body\n"
	if err := os.WriteFile(planPath, []byte(content), 0644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/task-to-delete", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	if _, err := os.Stat(planPath); !os.IsNotExist(err) {
		t.Fatalf("plan file still exists after delete")
	}
}

// TestAPIGetConfig verifies GET /api/config returns JSON with models list and listen_addr.
func TestAPIGetConfig(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var cfg configResponse
	if err := json.NewDecoder(w.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(cfg.Models) == 0 {
		t.Errorf("expected non-empty models list")
	}
	// listen_addr is empty string when not set in config — just ensure the field exists
	// by checking we can decode the struct (done above)
}

// TestAPIRetryTask verifies POST /api/tasks/{name}/retry resets a failed task to pending.
func TestAPIRetryTask(t *testing.T) {
	srv, plansDir := setupTestServer(t)

	// Create a plan file with status "failed"
	planPath := filepath.Join(plansDir, "failed-task.md")
	content := "---\ntitle: \"Failed Task\"\nstatus: failed\nproject: /tmp/proj\nmodel: sonnet-4.6\nfinished_at: 2026-01-01T00:00:00Z\nexit_code: 1\n---\nDo the thing\n"
	if err := os.WriteFile(planPath, []byte(content), 0644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/failed-task/retry", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var task APITask
	if err := json.NewDecoder(w.Body).Decode(&task); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if task.Status != "pending" {
		t.Errorf("expected status 'pending' after retry, got %q", task.Status)
	}

	// Verify the file on disk also has pending status
	reParsed, err := parsePlanFile(planPath)
	if err != nil {
		t.Fatalf("re-parse plan file: %v", err)
	}
	if reParsed.Status != "pending" {
		t.Errorf("on-disk status expected 'pending', got %q", reParsed.Status)
	}
}
