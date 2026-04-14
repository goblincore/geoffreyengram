package dualmem

import (
	"testing"
	"time"
)

func TestExtractTicketPrefix(t *testing.T) {
	tests := []struct {
		subject string
		want    string
	}{
		{"[LC-1729] guardian credential issuance", "LC-1729"},
		{"[PROJ-42] fix login bug", "PROJ-42"},
		{"no ticket prefix here", ""},
		{"[LC-1729] [LC-1731] multiple prefixes", "LC-1729"}, // returns first
		{"fix: [AB-1] trailing ticket", "AB-1"},
		{"lowercase [lc-123] should not match", ""},
	}
	for _, tt := range tests {
		got := extractTicketPrefix(tt.subject)
		if got != tt.want {
			t.Errorf("extractTicketPrefix(%q) = %q, want %q", tt.subject, got, tt.want)
		}
	}
}

func TestGroupCommitsByTicket(t *testing.T) {
	commits := []GitCommit{
		{Subject: "[LC-1] first", Files: []string{"a.ts"}},
		{Subject: "[LC-1] second", Files: []string{"b.ts"}},
		{Subject: "[LC-2] other", Files: []string{"c.ts"}},
		{Subject: "no ticket", Files: []string{"d.ts"}},
	}

	groups := groupCommitsByTicket(commits)

	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if len(groups["LC-1"]) != 2 {
		t.Errorf("expected 2 commits in LC-1, got %d", len(groups["LC-1"]))
	}
	if len(groups["LC-2"]) != 1 {
		t.Errorf("expected 1 commit in LC-2, got %d", len(groups["LC-2"]))
	}
	if _, ok := groups[""]; ok {
		t.Error("should not have empty-key group")
	}
}

func TestBuildWorkflowClusters(t *testing.T) {
	now := time.Now()
	groups := map[string][]GitCommit{
		"LC-1": {
			{Subject: "[LC-1] auth flow", Files: []string{"auth.ts", "login.tsx"}, Date: now},
			{Subject: "[LC-1] auth tests", Files: []string{"auth.ts", "auth.test.ts"}, Date: now.Add(-24 * time.Hour)},
			{Subject: "[LC-1] auth fix", Files: []string{"auth.ts"}, Date: now.Add(-48 * time.Hour)},
		},
		"LC-2": {
			{Subject: "[LC-2] single commit", Files: []string{"x.ts"}, Date: now},
		},
	}

	clusters := buildWorkflowClusters(groups, 3)

	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster (LC-2 has only 1 commit), got %d", len(clusters))
	}
	c := clusters[0]
	if c.Commits != 3 {
		t.Errorf("expected 3 commits, got %d", c.Commits)
	}
	// Should have 3 unique files: auth.ts, login.tsx, auth.test.ts
	if len(c.Files) != 3 {
		t.Errorf("expected 3 unique files, got %d: %v", len(c.Files), c.Files)
	}
}

func TestMergeOverlappingClusters(t *testing.T) {
	c1 := WorkflowCluster{
		ID:       "LC-1",
		Tickets:  []string{"LC-1"},
		Subjects: []string{"auth flow"},
		Files:    []string{"auth.ts", "login.tsx", "middleware.ts"},
		Commits:  3,
		Score:    0.9,
	}
	c2 := WorkflowCluster{
		ID:       "LC-2",
		Tickets:  []string{"LC-2"},
		Subjects: []string{"auth fix"},
		Files:    []string{"auth.ts", "login.tsx", "session.ts"},
		Commits:  2,
		Score:    0.8,
	}
	c3 := WorkflowCluster{
		ID:       "LC-3",
		Tickets:  []string{"LC-3"},
		Subjects: []string{"database migration"},
		Files:    []string{"db.go", "migrate.go"},
		Commits:  4,
		Score:    0.7,
	}

	// c1 and c2 overlap: 2 shared files / 4 total = 0.5 Jaccard → should merge at threshold 0.5
	merged := mergeOverlappingClusters([]WorkflowCluster{c1, c2, c3}, 0.5, 100)

	if len(merged) != 2 {
		t.Fatalf("expected 2 clusters (c1+c2 merged, c3 separate), got %d", len(merged))
	}

	// Find the merged cluster (should have both tickets).
	var mergedCluster *WorkflowCluster
	for i := range merged {
		if len(merged[i].Tickets) > 1 {
			mergedCluster = &merged[i]
			break
		}
	}
	if mergedCluster == nil {
		t.Fatal("expected a merged cluster with multiple tickets")
	}
	if mergedCluster.Commits != 5 {
		t.Errorf("expected 5 commits in merged cluster, got %d", mergedCluster.Commits)
	}
	if len(mergedCluster.Files) != 4 {
		t.Errorf("expected 4 unique files in merged cluster, got %d", len(mergedCluster.Files))
	}
}

func TestMergeOverlappingClusters_NoOverlap(t *testing.T) {
	c1 := WorkflowCluster{
		ID: "LC-1", Tickets: []string{"LC-1"}, Subjects: []string{"auth"},
		Files: []string{"auth.ts"}, Commits: 3, Score: 0.9,
	}
	c2 := WorkflowCluster{
		ID: "LC-2", Tickets: []string{"LC-2"}, Subjects: []string{"db"},
		Files: []string{"db.go"}, Commits: 3, Score: 0.8,
	}

	merged := mergeOverlappingClusters([]WorkflowCluster{c1, c2}, 0.5, 100)
	if len(merged) != 2 {
		t.Fatalf("expected 2 clusters (no overlap), got %d", len(merged))
	}
}

func TestNameWorkflow(t *testing.T) {
	tests := []struct {
		subjects []string
		contains string // result should contain this substring
	}{
		{[]string{"guardian credential issuance", "guardian credential UI", "guardian fix"}, "guardian"},
		{[]string{"database migration step 1", "database migration step 2"}, "database"},
		{[]string{"completely unique subject"}, "completely"},
	}
	for _, tt := range tests {
		got := nameWorkflow(tt.subjects)
		if got == "" {
			t.Errorf("nameWorkflow(%v) returned empty", tt.subjects)
		}
		if !containsSubstr(got, tt.contains) {
			t.Errorf("nameWorkflow(%v) = %q, expected to contain %q", tt.subjects, got, tt.contains)
		}
	}
}

func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && len(sub) > 0 && containsHelper(s, sub))
}

func TestBuildWorkflowPrompt(t *testing.T) {
	cluster := WorkflowCluster{
		ID:       "guardian-approval",
		Tickets:  []string{"LC-1729"},
		Subjects: []string{"guardian credential issuance", "guardian PIN flow"},
		Files:    []string{"auth.ts", "guardian.ts"},
		Commits:  5,
	}
	prompt := buildWorkflowPrompt(cluster, []string{"auth.ts", "guardian.ts"}, "const x = 1;")

	checks := []string{"WORKFLOW SUMMARY", "TRIGGER", "DATA FLOW", "CROSS-SERVICE", "ERROR HANDLING", "LC-1729", "guardian credential issuance"}
	for _, check := range checks {
		if !containsSubstr(prompt, check) {
			t.Errorf("prompt should contain %q", check)
		}
	}
}
