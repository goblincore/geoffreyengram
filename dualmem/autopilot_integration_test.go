package dualmem

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAutopilot_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	// Create realistic project files
	writeTestFile(t, filepath.Join(tmpDir, "go.mod"), "module testproject\ngo 1.21\n")
	writeTestFile(t, filepath.Join(tmpDir, "main.go"), `package main
import "fmt"
func main() {
	srv := NewServer()
	srv.Start()
	fmt.Println("running")
}
`)
	writeTestFile(t, filepath.Join(tmpDir, "server.go"), `package main
import "net/http"
type Server struct { mux *http.ServeMux }
func NewServer() *Server {
	s := &Server{mux: http.NewServeMux()}
	s.mux.HandleFunc("/health", s.healthHandler)
	s.mux.HandleFunc("/api/users", s.usersHandler)
	return s
}
func (s *Server) Start() { http.ListenAndServe(":8080", s.mux) }
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }
func (s *Server) usersHandler(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[]")) }
`)
	writeTestFile(t, filepath.Join(tmpDir, "auth.go"), `package main
import "crypto/sha256"
func hashPassword(pw string) []byte { h := sha256.Sum256([]byte(pw)); return h[:] }
func validateToken(token string) bool { return len(token) > 0 }
`)

	// Init git
	runGitCmd(t, tmpDir, "init")
	runGitCmd(t, tmpDir, "add", ".")
	runGitCmd(t, tmpDir, "commit", "-m", "initial")

	// Create engine with RootDir
	dbPath := filepath.Join(t.TempDir(), "test.db")
	gen := &mockTextGen{response: "This module provides HTTP server with health and user endpoints."}
	engine, err := New(Config{
		SQLitePath:         dbPath,
		EmbeddingProvider:  &mockEmbedder{dim: 768},
		Classifier:         &mockClassifier{},
		EntityExtractor:    &mockExtractor{},
		SynthesisGenerator: gen,
		ExplorerGenerator:  gen,
		MaxDetailPerUser:   100,
		ImportanceTheta:    0.65,
		RootDir:            tmpDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer engine.Close()

	ctx := context.Background()
	result, err := engine.Autopilot(ctx, "testns", AutopilotOpts{Budget: 50000})
	if err != nil {
		t.Fatalf("Autopilot: %v", err)
	}

	if len(result.Targets) == 0 {
		t.Fatal("expected at least 1 target")
	}
	if result.Explored == 0 {
		t.Error("expected at least 1 exploration")
	}
	if result.MemoriesAdded == 0 {
		t.Error("expected at least 1 memory created")
	}
	t.Logf("Targets: %d, Explored: %d, Memories: %d, Tokens: %d",
		len(result.Targets), result.Explored, result.MemoriesAdded, result.TokensUsed)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
