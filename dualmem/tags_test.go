package dualmem

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectLanguage verifies extension → language key mapping.
func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"foo.go", "go"},
		{"bar.ts", "javascript"},
		{"baz.tsx", "javascript"},
		{"qux.js", "javascript"},
		{"qux.jsx", "javascript"},
		{"qux.mjs", "javascript"},
		{"qux.cjs", "javascript"},
		{"mod.py", "python"},
		{"mod.pyi", "python"},
		{"lib.rs", "rust"},
		{"README.md", ""},
		{"data.json", ""},
		{"noext", ""},
	}

	for _, tc := range cases {
		got := DetectLanguage(tc.path)
		if got != tc.want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestExtractTagsUnknownLanguage verifies that an unknown language returns nil, nil.
func TestExtractTagsUnknownLanguage(t *testing.T) {
	tags, err := ExtractTags("some/file.txt", "cobol")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Errorf("expected nil tags, got %v", tags)
	}
}

// TestExtractTagsGo runs ExtractTags on hdc.go and checks for expected definitions.
func TestExtractTagsGo(t *testing.T) {
	// hdc.go contains: CodeIndex struct def, HDCEncoder struct def, various funcs
	hdcPath := filepath.Join(".", "hdc.go")
	if _, err := os.Stat(hdcPath); os.IsNotExist(err) {
		t.Skip("hdc.go not found, skipping")
	}

	tags, err := ExtractTags(hdcPath, "go")
	if err != nil {
		t.Fatalf("ExtractTags error: %v", err)
	}
	if len(tags) == 0 {
		t.Fatal("expected tags, got none")
	}

	// Check for CodeIndex type definition
	foundCodeIndex := false
	for _, tag := range tags {
		if tag.Name == "CodeIndex" && tag.Kind == "def" {
			foundCodeIndex = true
			break
		}
	}
	if !foundCodeIndex {
		t.Errorf("expected CodeIndex def tag; got tags: %v", tags[:minInt(10, len(tags))])
	}

	// Check for some reference tags
	foundRef := false
	for _, tag := range tags {
		if tag.Kind == "ref" {
			foundRef = true
			break
		}
	}
	if !foundRef {
		t.Errorf("expected at least one reference tag, got none")
	}

	// All tags should have non-empty Name, Kind, File
	for i, tag := range tags {
		if tag.Name == "" {
			t.Errorf("tag[%d] has empty Name", i)
		}
		if tag.Kind != "def" && tag.Kind != "ref" {
			t.Errorf("tag[%d] has unexpected Kind %q", i, tag.Kind)
		}
		if tag.File == "" {
			t.Errorf("tag[%d] has empty File", i)
		}
		if tag.Line <= 0 {
			t.Errorf("tag[%d] has non-positive Line %d", i, tag.Line)
		}
	}
}

// TestExtractTagsJS tests JavaScript tag extraction on a temp file with known symbols.
func TestExtractTagsJS(t *testing.T) {
	src := `
function greet(name) {
  return "Hello, " + name;
}

class UserService {
  getUser(id) {
    return this.users[id];
  }
}

const helper = (x) => x * 2;
`
	tmpFile, err := os.CreateTemp(t.TempDir(), "*.js")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer tmpFile.Close()

	if _, err := tmpFile.WriteString(src); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	tmpFile.Close()

	tags, err := ExtractTags(tmpFile.Name(), "javascript")
	if err != nil {
		t.Fatalf("ExtractTags error: %v", err)
	}
	if len(tags) == 0 {
		t.Fatal("expected tags, got none")
	}

	// Index tags by name
	byName := make(map[string][]Tag)
	for _, tag := range tags {
		byName[tag.Name] = append(byName[tag.Name], tag)
	}

	wantDefs := []string{"greet", "UserService", "helper"}
	for _, name := range wantDefs {
		found := false
		for _, tag := range byName[name] {
			if tag.Kind == "def" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected def tag for %q; all tags: %v", name, tags)
		}
	}

	// getUser should be a method def (SubKind="method")
	foundGetUser := false
	for _, tag := range byName["getUser"] {
		if tag.Kind == "def" {
			foundGetUser = true
			break
		}
	}
	if !foundGetUser {
		t.Errorf("expected def tag for getUser; all tags: %v", tags)
	}
}

// minInt is a helper for test output truncation.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
