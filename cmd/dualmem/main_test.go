package main

import (
	"flag"
	"reflect"
	"testing"
)

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
