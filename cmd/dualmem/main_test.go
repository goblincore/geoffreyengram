package main

import (
	"bytes"
	"flag"
	"path/filepath"
	"reflect"
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
