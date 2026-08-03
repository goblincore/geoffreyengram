package integrate

import "testing"

const (
	testBegin = "<!-- BEGIN DUALMEM -->"
	testEnd   = "<!-- END DUALMEM -->"
)

func TestManagedBlockReplacePreservesUnrelatedTextAndIsIdempotent(t *testing.T) {
	input := "before\n" + testBegin + "\nold\n" + testEnd + "\nafter\n"
	want := "before\n" + testBegin + "\nnew\n" + testEnd + "\nafter\n"
	got, err := ReplaceManagedBlock(input, testBegin, testEnd, "new")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("replace = %q, want %q", got, want)
	}
	again, err := ReplaceManagedBlock(got, testBegin, testEnd, "new")
	if err != nil {
		t.Fatal(err)
	}
	if again != got {
		t.Fatalf("second replace changed content: %q", again)
	}
}

func TestManagedBlockAppendAndRemove(t *testing.T) {
	added, err := ReplaceManagedBlock("unrelated", testBegin, testEnd, "managed\nline")
	if err != nil {
		t.Fatal(err)
	}
	wantAdded := "unrelated\n" + testBegin + "\nmanaged\nline\n" + testEnd + "\n"
	if added != wantAdded {
		t.Fatalf("append = %q, want %q", added, wantAdded)
	}
	removed, err := RemoveManagedBlock(added, testBegin, testEnd)
	if err != nil {
		t.Fatal(err)
	}
	if removed != "unrelated\n" {
		t.Fatalf("remove = %q, want unrelated text preserved", removed)
	}

	only, err := RemoveManagedBlock(testBegin+"\nmanaged\n"+testEnd+"\n", testBegin, testEnd)
	if err != nil {
		t.Fatal(err)
	}
	if only != "" {
		t.Fatalf("removing sole managed block left %q", only)
	}
}

func TestManagedBlockRejectsMalformedDuplicateAndOverlappingMarkers(t *testing.T) {
	tests := map[string]string{
		"begin only":       testBegin + "\nbody\n",
		"end only":         "body\n" + testEnd + "\n",
		"duplicate begin":  testBegin + "\n" + testBegin + "\n" + testEnd + "\n",
		"duplicate end":    testBegin + "\n" + testEnd + "\n" + testEnd + "\n",
		"end before begin": testEnd + "\nbody\n" + testBegin + "\n",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ReplaceManagedBlock(input, testBegin, testEnd, "new"); err == nil {
				t.Fatal("ReplaceManagedBlock accepted malformed markers")
			}
			if _, err := RemoveManagedBlock(input, testBegin, testEnd); err == nil {
				t.Fatal("RemoveManagedBlock accepted malformed markers")
			}
		})
	}
}

func TestManagedBlockMarkersMustMatchExactLines(t *testing.T) {
	input := "prefix " + testBegin + " suffix\ntext " + testEnd + " suffix\n"
	got, err := ReplaceManagedBlock(input, testBegin, testEnd, "managed")
	if err != nil {
		t.Fatal(err)
	}
	want := input + testBegin + "\nmanaged\n" + testEnd + "\n"
	if got != want {
		t.Fatalf("non-exact marker text was treated as managed: %q", got)
	}
}
