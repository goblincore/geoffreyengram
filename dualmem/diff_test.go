package dualmem

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestComputeStructuralDiff_NilMarker(t *testing.T) {
	diff, err := ComputeStructuralDiff("/tmp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if diff != nil {
		t.Error("expected nil diff for nil marker")
	}
}

func TestComputeStructuralDiff_SameBranch(t *testing.T) {
	dir := setupGitRepo(t)

	// Get initial commit
	commit1 := gitCurrentCommit(dir)
	branch := gitCurrentBranch(dir)

	// Make a change and commit
	writeFile(t, dir, "new_file.go", "package main\n\nfunc newFunc() {}\n")
	gitAdd(t, dir, "new_file.go")
	gitCommit(t, dir, "add new file")
	commit2 := gitCurrentCommit(dir)

	marker := &SessionMarker{
		ID:        "test-1",
		Namespace: "test",
		Branch:    branch,
		Commit:    commit1,
		Timestamp: time.Now().Add(-1 * time.Hour),
	}

	diff, err := ComputeStructuralDiff(dir, marker)
	if err != nil {
		t.Fatal(err)
	}
	if diff == nil {
		t.Fatal("expected non-nil diff")
	}

	if !diff.SameBranch {
		t.Error("expected same branch")
	}
	if diff.FromCommit != commit1 {
		t.Errorf("from commit = %q, want %q", diff.FromCommit, commit1)
	}
	if diff.ToCommit != commit2 {
		t.Errorf("to commit = %q, want %q", diff.ToCommit, commit2)
	}

	// Should detect the added file
	if len(diff.FilesAdded) != 1 || diff.FilesAdded[0] != "new_file.go" {
		t.Errorf("expected new_file.go in added, got %v", diff.FilesAdded)
	}

	// Summary should mention the file
	if !strings.Contains(diff.Summary, "new_file.go") {
		t.Errorf("summary should mention new_file.go, got %q", diff.Summary)
	}
}

func TestComputeStructuralDiff_NoChanges(t *testing.T) {
	dir := setupGitRepo(t)
	commit := gitCurrentCommit(dir)
	branch := gitCurrentBranch(dir)

	marker := &SessionMarker{
		ID:        "test-1",
		Namespace: "test",
		Branch:    branch,
		Commit:    commit,
		Timestamp: time.Now(),
	}

	diff, err := ComputeStructuralDiff(dir, marker)
	if err != nil {
		t.Fatal(err)
	}
	if diff.TotalChanges() != 0 {
		t.Errorf("expected 0 changes, got %d", diff.TotalChanges())
	}
	if !strings.Contains(diff.Summary, "No changes") {
		t.Errorf("expected 'No changes' summary, got %q", diff.Summary)
	}
}

func TestComputeStructuralDiff_ModifiedFile(t *testing.T) {
	dir := setupGitRepo(t)

	commit1 := gitCurrentCommit(dir)
	branch := gitCurrentBranch(dir)

	// Modify existing file
	writeFile(t, dir, "main.go", "package main\n\nfunc main() { /* modified */ }\n")
	gitAdd(t, dir, "main.go")
	gitCommit(t, dir, "modify main.go")

	marker := &SessionMarker{
		ID:        "test-1",
		Namespace: "test",
		Branch:    branch,
		Commit:    commit1,
		Timestamp: time.Now().Add(-30 * time.Minute),
	}

	diff, err := ComputeStructuralDiff(dir, marker)
	if err != nil {
		t.Fatal(err)
	}

	if len(diff.FilesModified) != 1 || diff.FilesModified[0] != "main.go" {
		t.Errorf("expected main.go modified, got %v", diff.FilesModified)
	}
}

func TestComputeStructuralDiff_DifferentBranch(t *testing.T) {
	dir := setupGitRepo(t)

	commit1 := gitCurrentCommit(dir)

	// Create and switch to new branch
	runGit(t, dir, "checkout", "-b", "feature-branch")
	writeFile(t, dir, "feature.go", "package main\n\nfunc feature() {}\n")
	gitAdd(t, dir, "feature.go")
	gitCommit(t, dir, "add feature")

	marker := &SessionMarker{
		ID:        "test-1",
		Namespace: "test",
		Branch:    "main",
		Commit:    commit1,
		Timestamp: time.Now().Add(-2 * time.Hour),
	}

	diff, err := ComputeStructuralDiff(dir, marker)
	if err != nil {
		t.Fatal(err)
	}
	if diff == nil {
		t.Fatal("expected diff")
	}

	if diff.SameBranch {
		t.Error("expected different branch")
	}
	if diff.FromBranch != "main" {
		t.Errorf("from branch = %q, want main", diff.FromBranch)
	}
	if diff.ToBranch != "feature-branch" {
		t.Errorf("to branch = %q, want feature-branch", diff.ToBranch)
	}
	if !strings.Contains(diff.Summary, "branch changed") {
		t.Errorf("summary should mention branch change, got %q", diff.Summary)
	}
}

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Minute, "30m"},
		{3 * time.Hour, "3h"},
		{48 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		got := formatElapsed(tt.d)
		if got != tt.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestTruncateFileList(t *testing.T) {
	files := []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go"}

	out := truncateFileList(files, 3)
	if len(out) != 4 { // 3 files + "(+4 more)"
		t.Errorf("expected 4 items, got %d: %v", len(out), out)
	}
	if !strings.Contains(out[3], "+4 more") {
		t.Errorf("expected truncation message, got %q", out[3])
	}
}

func TestParseNameStatusOutput(t *testing.T) {
	output := `A	new_file.go
M	existing.go
D	old_file.go
R100	renamed_old.go	renamed_new.go
`
	diff := &StructuralDiff{}
	parseNameStatusOutput(output, diff)

	if len(diff.FilesAdded) != 2 { // new_file.go + renamed_new.go
		t.Errorf("expected 2 added, got %v", diff.FilesAdded)
	}
	if len(diff.FilesModified) != 1 {
		t.Errorf("expected 1 modified, got %v", diff.FilesModified)
	}
	if len(diff.FilesDeleted) != 2 { // old_file.go + renamed_old.go
		t.Errorf("expected 2 deleted, got %v", diff.FilesDeleted)
	}
}

// --- Test helpers ---

func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	runGit(t, dir, "init")
	runGit(t, dir, "checkout", "-b", "main")

	// Configure git for commits
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")

	// Initial commit
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	gitAdd(t, dir, "main.go")
	gitCommit(t, dir, "initial commit")

	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2025-01-01T00:00:00+00:00", "GIT_COMMITTER_DATE=2025-01-01T00:00:00+00:00")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitAdd(t *testing.T, dir, file string) {
	t.Helper()
	runGit(t, dir, "add", file)
}

func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	cmd := exec.Command("git", "commit", "-m", msg)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func gitAddCommit(t *testing.T, dir string) {
	t.Helper()
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if !strings.HasPrefix(rel, ".git") {
			gitAdd(t, dir, rel)
		}
		return nil
	})
	gitCommit(t, dir, "auto commit")
}
