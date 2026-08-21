package main

import (
	"bytes"
	"flag"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrintContextDiagnostics(t *testing.T) {
	var output bytes.Buffer
	printContextDiagnostics(&output, []string{"provider unavailable", "retry with network permission"})

	const want = "warning: provider unavailable\nwarning: retry with network permission\n"
	if got := output.String(); got != want {
		t.Errorf("printContextDiagnostics() = %q, want %q", got, want)
	}
}

// TestNewEngineLocalDoesNotRequireProviderKey protects local-only commands
// from failing before they can open the SQLite store when no provider is
// configured.
func TestNewEngineLocalDoesNotRequireProviderKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	cfg := defaultCLIConfig(t.TempDir())
	cfg.Storage.SQLitePath = filepath.Join(t.TempDir(), "memories.db")

	engine, err := newEngine(cfg, engineLocal)
	if err != nil {
		t.Fatalf("newEngine local: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
}

// TestContextCapabilities protects the distinction between the fast,
// embedding-free session-start path and every query path that needs semantic
// retrieval.
func TestContextCapabilities(t *testing.T) {
	cases := []struct {
		name          string
		query         string
		legacy, index bool
		want          engineCapabilities
	}{
		{name: "default query", want: engineLocal},
		{name: "session start sentinel", query: "session start", want: engineLocal},
		{name: "session context sentinel", query: "session context", want: engineLocal},
		{name: "specific query", query: "why does auth fail", want: engineSemantic},
		{name: "legacy session query", query: "session context", legacy: true, want: engineSemantic},
		{name: "index session query", query: "session context", index: true, want: engineSemantic},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contextCapabilities(tc.query, tc.legacy, tc.index); got != tc.want {
				t.Errorf("contextCapabilities(%q, legacy=%t, index=%t) = %#v, want %#v", tc.query, tc.legacy, tc.index, got, tc.want)
			}
		})
	}
}

func TestCheckpointCapabilities(t *testing.T) {
	if got := checkpointCapabilities(true); got != engineLocal {
		t.Errorf("checkpointCapabilities(list=true) = %#v, want local-only %#v", got, engineLocal)
	}
	if got := checkpointCapabilities(false); got != engineEmbedWrite {
		t.Errorf("checkpointCapabilities(list=false) = %#v, want embed-write %#v", got, engineEmbedWrite)
	}
}

// TestParseFlagsInterspersed is the regression guard for the flag-ordering
// footgun: Go's stdlib flag.Parse stops at the first positional token, so
// `dualmem recall "query" --ns X` used to silently drop --ns and fall back to
// the cwd namespace. parseFlagsInterspersed must honor flags on either side of
// the positional query.
func TestParseFlagsInterspersed(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantNS  string
		wantLim int
		wantPos []string
	}{
		{
			name:    "flags before positional (already worked)",
			args:    []string{"--ns", "claude:LearnCard", "--limit", "8", "LC-1831", "i18n"},
			wantNS:  "claude:LearnCard",
			wantLim: 8,
			wantPos: []string{"LC-1831", "i18n"},
		},
		{
			name:    "flags after positional (the bug)",
			args:    []string{"LC-1831", "i18n", "--ns", "claude:LearnCard", "--limit", "8"},
			wantNS:  "claude:LearnCard",
			wantLim: 8,
			wantPos: []string{"LC-1831", "i18n"},
		},
		{
			name:    "flags interspersed on both sides",
			args:    []string{"--ns", "claude:LearnCard", "LC-1831", "--limit", "8", "i18n"},
			wantNS:  "claude:LearnCard",
			wantLim: 8,
			wantPos: []string{"LC-1831", "i18n"},
		},
		{
			name:    "no flags at all",
			args:    []string{"just", "a", "query"},
			wantNS:  "",
			wantLim: 5,
			wantPos: []string{"just", "a", "query"},
		},
		{
			name:    "only flags, no positional",
			args:    []string{"--ns", "claude:x", "--limit", "3"},
			wantNS:  "claude:x",
			wantLim: 3,
			wantPos: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("recall", flag.ContinueOnError)
			ns := fs.String("ns", "", "")
			limit := fs.Int("limit", 5, "")

			pos := parseFlagsInterspersed(fs, tc.args)

			if *ns != tc.wantNS {
				t.Errorf("ns = %q, want %q", *ns, tc.wantNS)
			}
			if *limit != tc.wantLim {
				t.Errorf("limit = %d, want %d", *limit, tc.wantLim)
			}
			if !reflect.DeepEqual(pos, tc.wantPos) {
				t.Errorf("positionals = %#v, want %#v", pos, tc.wantPos)
			}
		})
	}
}

func TestResolveMemType(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// An explicit `dualmem add` is an intentional save. Leaving the type
		// empty dropped it below the Detail Path importance floor, so the row
		// was routed to sketch and never became searchable.
		{name: "empty becomes general", in: "", want: "general"},
		{name: "whitespace becomes general", in: "   ", want: "general"},
		{name: "explicit type preserved", in: "warning", want: "warning"},
		{name: "explicit type trimmed", in: " decision ", want: "decision"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMemType(tc.in); got != tc.want {
				t.Errorf("resolveMemType(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeSectorHint(t *testing.T) {
	valid := []string{"decision", "warning", "map", "continuity"}

	t.Run("empty hint passes through with no warning", func(t *testing.T) {
		sector, warning := normalizeSectorHint("", valid)
		if sector != "" || warning != "" {
			t.Errorf("got (%q, %q), want empty sector and no warning", sector, warning)
		}
	})

	t.Run("valid hint is kept", func(t *testing.T) {
		sector, warning := normalizeSectorHint("Warning", valid)
		if sector != "warning" {
			t.Errorf("sector = %q, want %q", sector, "warning")
		}
		if warning != "" {
			t.Errorf("unexpected warning %q", warning)
		}
	})

	// "procedural" and "semantic" are legacy cognitive sectors that this
	// deployment no longer defines. They scored 0.0 on the sector bonus and
	// silently sank the memory into the sketch path.
	for _, legacy := range []string{"procedural", "semantic"} {
		t.Run("legacy sector "+legacy+" is dropped and warned about", func(t *testing.T) {
			sector, warning := normalizeSectorHint(legacy, valid)
			if sector != "" {
				t.Errorf("sector = %q, want %q so the classifier picks a real one", sector, "")
			}
			if warning == "" {
				t.Fatalf("expected a warning naming the valid sectors")
			}
			for _, v := range valid {
				if !strings.Contains(warning, v) {
					t.Errorf("warning %q does not mention valid sector %q", warning, v)
				}
			}
		})
	}

	t.Run("does not mutate the caller's slice", func(t *testing.T) {
		in := []string{"warning", "decision"}
		normalizeSectorHint("bogus", in)
		if !reflect.DeepEqual(in, []string{"warning", "decision"}) {
			t.Errorf("caller slice mutated to %v", in)
		}
	})
}
