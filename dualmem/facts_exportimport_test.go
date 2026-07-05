package dualmem

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestExportImportNoOp verifies that exporting live facts and re-importing the
// unchanged markdown is a no-op (nothing added/edited/retired), and that the
// store is unchanged after a committed dry round-trip.
func TestExportImportNoOp(t *testing.T) {
	engine := newFactsTestEngine(t)
	ctx := context.Background()

	seedFacts(t, engine, ctx)

	md, err := engine.ExportFacts()
	if err != nil {
		t.Fatalf("ExportFacts: %v", err)
	}
	if !strings.Contains(md, "# dualmem facts") {
		t.Fatalf("export missing header:\n%s", md)
	}

	// Dry run: no-op.
	res, err := engine.ImportFacts(ctx, md, false)
	if err != nil {
		t.Fatalf("ImportFacts dry-run: %v", err)
	}
	if len(res.Added) != 0 || len(res.Edited) != 0 || len(res.Retired) != 0 {
		t.Fatalf("dry-run no-op expected, got added=%d edited=%d retired=%d",
			len(res.Added), len(res.Edited), len(res.Retired))
	}
	if len(res.Unchanged) == 0 {
		t.Fatalf("dry-run should report unchanged facts, got %d", len(res.Unchanged))
	}

	// Committed re-import is also a no-op.
	res2, err := engine.ImportFacts(ctx, md, true)
	if err != nil {
		t.Fatalf("ImportFacts commit: %v", err)
	}
	if len(res2.Added) != 0 || len(res2.Edited) != 0 || len(res2.Retired) != 0 {
		t.Fatalf("commit no-op expected, got added=%d edited=%d retired=%d",
			len(res2.Added), len(res2.Edited), len(res2.Retired))
	}

	// Store should be unchanged: re-export is byte-identical.
	md2, err := engine.ExportFacts()
	if err != nil {
		t.Fatalf("re-export: %v", err)
	}
	if md != md2 {
		t.Fatalf("store changed after no-op import:\n--- before ---\n%s\n--- after ---\n%s", md, md2)
	}
}

// TestImportEditRoundTrip edits a bullet's text in the exported markdown and
// verifies the old fact is superseded by a new one carrying the edited text and
// preserved source, while other facts stay live.
func TestImportEditRoundTrip(t *testing.T) {
	engine := newFactsTestEngine(t)
	ctx := context.Background()
	original := seedFacts(t, engine, ctx)
	_ = original

	md, err := engine.ExportFacts()
	if err != nil {
		t.Fatalf("ExportFacts: %v", err)
	}

	// Edit one bullet: locate its line (ID anchor intact) and swap only the text,
	// preserving the provenance suffix and ID comment so import sees it as an edit.
	editedText := "Use SQLite (one file, with a namespace column) for the local store."
	editedMD := swapBulletText(md, "Chose SQLite over Postgres", editedText)
	if !strings.Contains(editedMD, editedText) {
		t.Fatalf("edit did not land in markdown:\n%s", editedMD)
	}

	res, err := engine.ImportFacts(ctx, editedMD, true)
	if err != nil {
		t.Fatalf("ImportFacts commit: %v", err)
	}
	if len(res.Edited) != 1 {
		t.Fatalf("want 1 edited, got %d (added=%d retired=%d)", len(res.Edited), len(res.Added), len(res.Retired))
	}
	if res.Edited[0].Text != editedText {
		t.Fatalf("edited item text = %q, want %q", res.Edited[0].Text, editedText)
	}

	// The old decision fact is now superseded; the live set still has exactly
	// the same count (one decision replaced by another).
	live, err := engine.ListFacts("repo", "", false)
	if err != nil {
		t.Fatalf("ListFacts: %v", err)
	}
	if len(live) != 3 {
		t.Fatalf("live count should be unchanged at 3, got %d", len(live))
	}

	// Find the live decision and confirm it carries the edited text + verified source.
	var liveDecision *Fact
	all, _ := engine.ListFacts("repo", "", false)
	for _, f := range all {
		if f.Kind == FactKindDecision {
			liveDecision = f
		}
	}
	if liveDecision == nil {
		t.Fatal("no live decision after edit")
	}
	if liveDecision.Text != editedText {
		t.Fatalf("live decision text = %q, want %q", liveDecision.Text, editedText)
	}
	if liveDecision.Source != FactSourceVerified {
		t.Fatalf("source should be preserved as verified, got %q", liveDecision.Source)
	}

	// The old decision is superseded by the new one.
	allInc, _ := engine.ListFacts("repo", "", true)
	var oldCount int
	for _, f := range allInc {
		if strings.Contains(f.Text, "Postgres") {
			oldCount++
			if f.SupersededBy == "" {
				t.Fatal("old decision should be superseded")
			}
		}
	}
	if oldCount != 1 {
		t.Fatalf("expected the old Postgres decision to still exist (superseded), got %d", oldCount)
	}
}

// TestImportRetire removes a bullet from the markdown and verifies the
// corresponding fact is retired (superseded-by-self) and disappears from the
// live list.
func TestImportRetire(t *testing.T) {
	engine := newFactsTestEngine(t)
	ctx := context.Background()
	original := seedFacts(t, engine, ctx)

	md, err := engine.ExportFacts()
	if err != nil {
		t.Fatalf("ExportFacts: %v", err)
	}

	// Remove the deadend bullet entirely.
	removed := strings.Replace(md, original["deadend-repo"]+"\n", "", 1)
	if !strings.Contains(removed, "## ") {
		t.Fatal("sanity: removal broke the document")
	}

	res, err := engine.ImportFacts(ctx, removed, true)
	if err != nil {
		t.Fatalf("ImportFacts commit: %v", err)
	}
	if len(res.Retired) != 1 {
		t.Fatalf("want 1 retired, got %d (added=%d edited=%d)", len(res.Retired), len(res.Added), len(res.Edited))
	}

	// The deadend fact is no longer live.
	live, _ := engine.ListFacts("repo", FactKindDeadEnd, false)
	if len(live) != 0 {
		t.Fatalf("deadend should be retired (not live), got %d live", len(live))
	}
	// But it still exists (superseded-by-self), keeping the chain auditable.
	all, _ := engine.ListFacts("repo", FactKindDeadEnd, true)
	if len(all) != 1 {
		t.Fatalf("retired fact should still exist, got %d", len(all))
	}
	if all[0].SupersededBy != all[0].ID {
		t.Fatalf("retired fact should be superseded-by-self, got SupersededBy=%q ID=%q", all[0].SupersededBy, all[0].ID)
	}

	// Other facts are untouched.
	dec, _ := engine.ListFacts("repo", FactKindDecision, false)
	if len(dec) != 1 {
		t.Fatalf("decision should still be live, got %d", len(dec))
	}
}

// TestImportAddNewBullet adds a brand-new bullet (no ID comment) to an existing
// section and verifies it is inserted as a new fact with source=verified and the
// kind from its section heading.
func TestImportAddNewBullet(t *testing.T) {
	engine := newFactsTestEngine(t)
	ctx := context.Background()
	_ = seedFacts(t, engine, ctx)

	md, err := engine.ExportFacts()
	if err != nil {
		t.Fatalf("ExportFacts: %v", err)
	}

	// Append a new bullet under the existing "## gotcha" / "### repo" section.
	// Insert it right after the existing gotcha bullet.
	newBullet := "- Never call store.Close() from a hook; it deadlocks the engine. (verified, 0000000, 2026-07-04)"
	gotchaSection := "### repo\n\n- The config cache"
	if !strings.Contains(md, gotchaSection) {
		t.Fatalf("could not locate gotcha section anchor:\n%s", md)
	}
	added := strings.Replace(md, gotchaSection, "### repo\n\n"+newBullet+"\n- The config cache", 1)

	res, err := engine.ImportFacts(ctx, added, true)
	if err != nil {
		t.Fatalf("ImportFacts commit: %v", err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("want 1 added, got %d (edited=%d retired=%d)", len(res.Added), len(res.Edited), len(res.Retired))
	}
	if res.Added[0].Kind != FactKindGotcha {
		t.Fatalf("added fact kind = %q, want %q", res.Added[0].Kind, FactKindGotcha)
	}
	if res.Added[0].NS != "repo" {
		t.Fatalf("added fact ns = %q, want %q", res.Added[0].NS, "repo")
	}

	// The new fact is live and carries the verified source.
	gotchas, _ := engine.ListFacts("repo", FactKindGotcha, false)
	var found bool
	for _, f := range gotchas {
		if strings.Contains(f.Text, "store.Close()") {
			found = true
			if f.Source != FactSourceVerified {
				t.Fatalf("new bullet source = %q, want verified", f.Source)
			}
		}
	}
	if !found {
		t.Fatalf("new gotcha not live among %d gotchas", len(gotchas))
	}
}

// TestImportDryRunDoesNotWrite verifies that a dry run leaves the store
// untouched even when the markdown would edit, retire, and add.
func TestImportDryRunDoesNotWrite(t *testing.T) {
	engine := newFactsTestEngine(t)
	ctx := context.Background()
	original := seedFacts(t, engine, ctx)

	before, err := engine.ExportFacts()
	if err != nil {
		t.Fatalf("ExportFacts before: %v", err)
	}

	// Drastic edit: remove all bullets and add nothing -> would retire everything.
	empty := "# dualmem facts\n\n## decision\n\n### repo\n\n"
	res, err := engine.ImportFacts(ctx, empty, false)
	if err != nil {
		t.Fatalf("ImportFacts dry-run: %v", err)
	}
	if len(res.Retired) == 0 {
		t.Fatalf("dry-run should detect retirements, got %d", len(res.Retired))
	}

	after, err := engine.ExportFacts()
	if err != nil {
		t.Fatalf("ExportFacts after: %v", err)
	}
	if before != after {
		t.Fatalf("dry-run mutated the store:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	// original map is only used to assert seed happened; reference to avoid unused warning.
	_ = original
}

// TestRenderFactsMarkdownOrdering checks grouping/ordering determinism: kinds
// in canonical order, global namespace first within a kind, oldest-first within
// a namespace.
func TestRenderFactsMarkdownOrdering(t *testing.T) {
	facts := []*Fact{
		{ID: "b", Namespace: "repo", Kind: FactKindDecision, Text: "B decision", Source: FactSourceVerified, CreatedAt: mustParse("2026-07-02")},
		{ID: "a", Namespace: "repo", Kind: FactKindDecision, Text: "A decision", Source: FactSourceVerified, CreatedAt: mustParse("2026-07-01")},
		{ID: "g", Namespace: "", Kind: FactKindPreference, Text: "global pref", Source: FactSourceVerified, CreatedAt: mustParse("2026-07-01")},
		{ID: "d", Namespace: "repo", Kind: FactKindDeadEnd, Text: "dead end", Source: FactSourceVerified, CreatedAt: mustParse("2026-07-01")},
	}
	md := RenderFactsMarkdown(facts)

	// Decision section before deadend before preference (canonical order).
	decIdx := strings.Index(md, "## decision")
	deadIdx := strings.Index(md, "## deadend")
	prefIdx := strings.Index(md, "## preference")
	if decIdx < 0 || deadIdx < 0 || prefIdx < 0 {
		t.Fatalf("missing sections in:\n%s", md)
	}
	if !(decIdx < deadIdx && deadIdx < prefIdx) {
		t.Fatalf("kind order wrong: dec=%d dead=%d pref=%d", decIdx, deadIdx, prefIdx)
	}

	// Within decision, "A decision" (older) before "B decision".
	aIdx := strings.Index(md, "A decision")
	bIdx := strings.Index(md, "B decision")
	if !(aIdx < bIdx) {
		t.Fatalf("oldest-first order wrong within section: a=%d b=%d", aIdx, bIdx)
	}

	// Global namespace rendered with the (global) sentinel heading.
	if !strings.Contains(md, "### (global)") {
		t.Fatalf("global namespace heading missing in:\n%s", md)
	}
}

// TestParseBulletFormats verifies the parser handles ID+provenance, provenance
// only, and bare text bullets.
func TestParseBulletFormats(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantText string
		wantID   string
		wantSrc  string
	}{
		{
			name:     "id and provenance",
			line:     "- Chose SQLite. <!-- fact:abc-123 --> (verified, deadbee, 2026-07-04)",
			wantText: "Chose SQLite.",
			wantID:   "abc-123",
			wantSrc:  "verified",
		},
		{
			name:     "provenance only",
			line:     "- A new note. (inferred, 0000000, 2026-07-04)",
			wantText: "A new note.",
			wantID:   "",
			wantSrc:  "inferred",
		},
		{
			name:     "bare text",
			line:     "- Just a bullet with no markers.",
			wantText: "Just a bullet with no markers.",
			wantID:   "",
			wantSrc:  "",
		},
		{
			name:     "text with parens then provenance",
			line:     "- Use foo (the bar variant). <!-- fact:x --> (doc, 1a2b3c4, 2026-07-04)",
			wantText: "Use foo (the bar variant).",
			wantID:   "x",
			wantSrc:  "doc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Wrap in a section so kind/ns get attached.
			doc := "## decision\n\n### repo\n\n" + tc.line + "\n"
			parsed, err := ParseFactsMarkdown(doc)
			if err != nil {
				t.Fatalf("ParseFactsMarkdown: %v", err)
			}
			if len(parsed) != 1 {
				t.Fatalf("want 1 bullet, got %d", len(parsed))
			}
			p := parsed[0]
			if p.text != tc.wantText {
				t.Errorf("text = %q, want %q", p.text, tc.wantText)
			}
			if p.id != tc.wantID {
				t.Errorf("id = %q, want %q", p.id, tc.wantID)
			}
			if p.source != tc.wantSrc {
				t.Errorf("source = %q, want %q", p.source, tc.wantSrc)
			}
			if p.kind != FactKindDecision {
				t.Errorf("kind = %q, want %q", p.kind, FactKindDecision)
			}
			if p.namespace != "repo" {
				t.Errorf("namespace = %q, want repo", p.namespace)
			}
		})
	}
}

// --- helpers ---

// seedFacts inserts a known set of facts and returns a map of section->bullet
// line, used by tests to locate/modify specific bullets in exported markdown.
func seedFacts(t *testing.T, engine *Engine, ctx context.Context) map[string]string {
	t.Helper()
	out := make(map[string]string)

	dec, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindDecision,
		Source:    FactSourceVerified,
		Text:      "Chose SQLite over Postgres for the CLI to keep zero-setup.",
		Files:     []string{"store_sqlite.go"},
	})
	if err != nil {
		t.Fatalf("AddFact decision: %v", err)
	}
	out["decision-repo"] = FormatFactBullet(dec)

	dead, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindDeadEnd,
		Source:    FactSourceVerified,
		Text:      "Tried storing embeddings as JSON text; too slow on large stores.",
	})
	if err != nil {
		t.Fatalf("AddFact deadend: %v", err)
	}
	out["deadend-repo"] = FormatFactBullet(dead)

	got, err := engine.AddFact(ctx, Fact{
		Namespace: "repo",
		Kind:      FactKindGotcha,
		Source:    FactSourceVerified,
		Text:      "The config cache key includes the provider name; do not reuse across providers.",
	})
	if err != nil {
		t.Fatalf("AddFact gotcha: %v", err)
	}
	out["gotcha-repo"] = FormatFactBullet(got)

	pref, err := engine.AddFact(ctx, Fact{
		Namespace: "",
		Kind:      FactKindPreference,
		Source:    FactSourceVerified,
		Text:      "Prefer concise commit messages with a clear subject line.",
	})
	if err != nil {
		t.Fatalf("AddFact preference: %v", err)
	}
	out["preference-global"] = FormatFactBullet(pref)

	return out
}

// swapBulletText replaces the text portion of a bullet line that contains
// marker, preserving its provenance suffix and ID anchor. It locates the line,
// strips the markers, and re-emits with newText + the original suffix/comment.
func swapBulletText(md, marker, newText string) string {
	lines := strings.Split(md, "\n")
	for i, ln := range lines {
		if !strings.Contains(ln, marker) {
			continue
		}
		pb, ok := parseBulletLine(ln)
		if !ok {
			continue
		}
		// Rebuild: prefix "  - " + newText + optional id comment + optional provenance.
		var b strings.Builder
		// Preserve leading whitespace from the original bullet.
		trimmed := strings.TrimLeft(ln, " ")
		lead := ln[:len(ln)-len(trimmed)]
		b.WriteString(lead)
		b.WriteString("- ")
		b.WriteString(newText)
		if pb.id != "" {
			b.WriteString(" <!-- fact:")
			b.WriteString(pb.id)
			b.WriteString(" -->")
		}
		if pb.source != "" {
			b.WriteString(" (")
			b.WriteString(pb.source)
			b.WriteString(", ")
			b.WriteString(pb.sha)
			b.WriteString(", ")
			b.WriteString(pb.date)
			b.WriteString(")")
		}
		lines[i] = b.String()
		break
	}
	return strings.Join(lines, "\n")
}

func mustParse(date string) time.Time {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic("mustParse: " + err.Error())
	}
	return t
}
