package dualmem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// migrate-v2: one-time curation of v1 detail memories + knowledge docs into
// durable facts. It is the Phase 2 step of the v2 rewrite (see
// docs/superpowers/plans/2026-07-04-dualmem-v2.md, "Migration").
//
// The v1 store is never modified: nothing is deleted, and migration only ever
// ADDS facts with source=migrated. The v1 archive produced by `dualmem
// archive-v1` is a hard precondition — migration refuses to run until that
// safety net exists, so a botched curation can always be rolled back to the
// full v1 dump.
//
// Pipeline:
//  1. precondition: archive file for this namespace must exist
//  2. load v1 detail memories + knowledge docs from the live store
//  3. LLM curation pass: classify each item into a fact kind or "skip"
//     (narrative/episodic), rewrite kept items as 1-3 self-contained sentences
//  4. dedupe within the migrated set via embedding similarity; near-duplicates
//     collapse via supersede (newest wins)
//  5. dry-run (default) emits the markdown mirror preview + counts;
//     --commit inserts the facts
//
// The migration is resourced against the same TextGenerator plumbing distill
// uses (e.cfg.Summarizer.(TextGenerator)). A narrow Curator interface wraps it
// so tests can inject a mock without standing up an LLM.

// MigrateV2Options configures a MigrateV2 run.
type MigrateV2Options struct {
	// Namespace to migrate (e.g. "claude:geoffreyengram"). Required.
	Namespace string

	// ArchiveDir overrides where the v1 archive is looked up. When empty,
	// defaults to ~/.dualmem-v1-archive.
	ArchiveDir string

	// DedupeThreshold is the cosine similarity above which two migrated facts
	// are treated as near-duplicates and collapsed via supersede (newest wins).
	// Default 0.90.
	DedupeThreshold float64

	// BatchSize is the number of v1 items sent per LLM curation call. Default 15.
	BatchSize int

	// Commit inserts facts when true; otherwise the run is a dry run that only
	// reports what would happen.
	Commit bool

	// Curator overrides the LLM curation pass. When nil, the engine's
	// configured TextGenerator (Summarizer) is used. Mainly for tests.
	Curator FactCurator
}

// MigrateV2Result summarizes a migration run (dry-run or committed).
type MigrateV2Result struct {
	Namespace     string           // namespace migrated
	DryRun        bool             // mirrored MigrateV2Options.Commit (!commit)
	Archive       string           // absolute archive file path that satisfied the precondition
	Sources       int              // v1 items considered (detail memories + knowledge docs)
	KeptByKind    map[string]int   // kept items grouped by fact kind
	Skipped       int              // items the LLM classified as "skip" (narrative/episodic)
	DedupeSkipped int              // near-duplicate items collapsed (not inserted)
	Inserted      int              // facts actually written (--commit only)
	Markdown      string           // markdown mirror preview of the migrated set (always populated)
	Errors        []MigrateV2Error // per-item curation failures (item skipped, never fatal)
}

// MigrateV2Error records one failed curation item so the caller can report it
// without aborting the whole migration.
type MigrateV2Error struct {
	SourceID string // v1 item id
	Reason   string // short description (e.g. "malformed LLM JSON")
}

// Summary returns a one-line human-readable summary.
func (r *MigrateV2Result) Summary() string {
	mode := "dry-run"
	if !r.DryRun {
		mode = "committed"
	}
	var kinds []string
	for k := range r.KeptByKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s=%d", k, r.KeptByKind[k]))
	}
	return fmt.Sprintf("%s: sources=%d kept(%s) skipped=%d deduped=%d inserted=%d",
		mode, r.Sources, strings.Join(parts, " "), r.Skipped, r.DedupeSkipped, r.Inserted)
}

// FactCurator classifies and rewrites a batch of v1 source items into fact
// candidates. The migration calls it per batch; implementations map each input
// to either a kept candidate (kind + self-contained text) or a "skip" verdict
// (narrative/episodic content not worth keeping).
type FactCurator interface {
	Curate(ctx context.Context, batch []v1SourceItem) ([]curatedFact, error)
}

// v1SourceItem is one migratable unit: either a v1 detail memory or a knowledge
// doc. The LLM only sees Text, Type, and Files; Namespace/SourceKind/SourceID
// are carried for provenance on insert.
type v1SourceItem struct {
	SourceID   string    // v1 detail memory ID or knowledge doc ID
	SourceKind string    // "detail" or "knowledge"
	Namespace  string    // namespace the inserted fact will be scoped to
	Text       string    // original text/content
	Type       string    // v1 memory type (decision/warning/...) or "" for knowledge docs
	Files      []string  // associated file paths
	CreatedAt  time.Time // preserved on the inserted fact
}

// curatedFact is the LLM's verdict for one source item: either skipped, or a
// rewritten fact with a v2 kind.
type curatedFact struct {
	SourceID string // echoes v1SourceItem.SourceID
	Skipped  bool   // true = narrative/episodic, don't migrate
	Kind     string // one of FactKind* when kept
	Text     string // 1-3 self-contained sentences when kept
}

// ArchiveMissingError is returned by MigrateV2 when the v1 archive precondition
// is not satisfied. It carries the command the caller should run first.
type ArchiveMissingError struct {
	Namespace  string
	ArchiveDir string
	FilePath   string // the expected archive file path
}

func (e *ArchiveMissingError) Error() string {
	return fmt.Sprintf("migrate-v2: no v1 archive found for namespace %q at %s — run `dualmem archive-v1` first",
		e.Namespace, e.FilePath)
}

// defaultDedupeThreshold matches distill.go's near-duplicate floor.
const defaultDedupeThreshold = 0.90

// defaultMigrateBatchSize keeps LLM prompts reasonable: ~15 items per call.
const defaultMigrateBatchSize = 15

// MigrateV2 curates v1 detail memories + knowledge docs into durable facts.
// See the file doc comment for the full pipeline.
func (e *Engine) MigrateV2(ctx context.Context, opts MigrateV2Options) (*MigrateV2Result, error) {
	if opts.Namespace == "" {
		return nil, fmt.Errorf("migrate-v2: namespace is required")
	}
	if opts.DedupeThreshold <= 0 {
		opts.DedupeThreshold = defaultDedupeThreshold
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultMigrateBatchSize
	}

	// 1. Precondition: archive must exist.
	archivePath, archiveDir, err := resolveArchivePath(opts.Namespace, opts.ArchiveDir)
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(archivePath); statErr != nil {
		return nil, &ArchiveMissingError{
			Namespace:  opts.Namespace,
			ArchiveDir: archiveDir,
			FilePath:   archivePath,
		}
	}

	// 2. Load v1 sources from the live store (the archive is the safety net,
	//    not the input — we read live rows so timestamps/files are exact).
	items, err := e.loadV1Sources(opts.Namespace)
	if err != nil {
		return nil, fmt.Errorf("migrate-v2: load sources: %w", err)
	}

	// 3. Curation pass.
	curator := opts.Curator
	if curator == nil {
		c, err := e.defaultCurator()
		if err != nil {
			return nil, err
		}
		curator = c
	}

	keptByKind := make(map[string]int)
	skipped := 0
	var candidates []migrateCandidate
	var merrs []MigrateV2Error

	for start := 0; start < len(items); start += opts.BatchSize {
		end := start + opts.BatchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[start:end]
		verdicts, err := curator.Curate(ctx, batch)
		if err != nil {
			// Whole batch failed: record per-item errors but keep going. The
			// migration never aborts on a single bad LLM response.
			for _, it := range batch {
				merrs = append(merrs, MigrateV2Error{SourceID: it.SourceID, Reason: fmt.Sprintf("batch curation: %v", err)})
				skipped++
			}
			continue
		}
		// verdicts may be shorter/longer than batch if the LLM was sloppy; match
		// by SourceID where possible, otherwise treat unmatched items as skipped.
		byID := make(map[string]*curatedFact, len(verdicts))
		for i := range verdicts {
			byID[verdicts[i].SourceID] = &verdicts[i]
		}
		for _, it := range batch {
			v, ok := byID[it.SourceID]
			if !ok {
				merrs = append(merrs, MigrateV2Error{SourceID: it.SourceID, Reason: "no verdict returned for item"})
				skipped++
				continue
			}
			if v.Skipped || !ValidFactKinds[v.Kind] || strings.TrimSpace(v.Text) == "" {
				skipped++
				continue
			}
			keptByKind[v.Kind]++
			candidates = append(candidates, migrateCandidate{
				source: it,
				kind:   v.Kind,
				text:   strings.TrimSpace(v.Text),
			})
		}
	}

	// 4. Dedupe within the migrated set: sort oldest-first so newest wins on a
	//    near-duplicate clash, then collapse via supersede.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].source.CreatedAt.Before(candidates[j].source.CreatedAt)
	})
	kept, deduped := e.dedupeMigrated(ctx, candidates, opts.DedupeThreshold)

	// 5. Insert (commit) or just preview (dry-run).
	inserted := 0
	if opts.Commit {
		for _, c := range kept {
			f, err := e.AddMigratedFact(ctx, c)
			if err != nil {
				merrs = append(merrs, MigrateV2Error{SourceID: c.source.SourceID, Reason: fmt.Sprintf("insert: %v", err)})
				continue
			}
			_ = f
			inserted++
		}
	}

	// Build the markdown mirror preview of the migrated set (regardless of
	// commit) so the user can review what was / would be kept.
	migratedFacts := candidatesToPreviewFacts(kept, opts.Namespace)
	markdown := RenderFactsMarkdown(migratedFacts)

	return &MigrateV2Result{
		Namespace:     opts.Namespace,
		DryRun:        !opts.Commit,
		Archive:       archivePath,
		Sources:       len(items),
		KeptByKind:    keptByKind,
		Skipped:       skipped,
		DedupeSkipped: deduped,
		Inserted:      inserted,
		Markdown:      markdown,
		Errors:        merrs,
	}, nil
}

// migrateCandidate pairs a v1 source with its curated v2 kind + rewritten text.
type migrateCandidate struct {
	source v1SourceItem
	kind   string
	text   string
}

// AddMigratedFact inserts one curated candidate as a fact with source=migrated,
// preserving the original v1 created_at and file associations. It bypasses the
// public AddFact default-timestamp path so the original creation time survives.
func (e *Engine) AddMigratedFact(ctx context.Context, c migrateCandidate) (*Fact, error) {
	f := Fact{
		Namespace: c.source.Namespace,
		Kind:      c.kind,
		Text:      c.text,
		Files:     append([]string(nil), c.source.Files...),
		Source:    FactSourceMigrated,
		CreatedAt: c.source.CreatedAt,
	}
	return e.AddFact(ctx, f)
}

// dedupeMigrated collapses near-duplicate candidates within the migrated set.
// candidates must already be sorted oldest-first. Returns the kept set (newest
// of each near-duplicate cluster wins) and the count collapsed. Embeddings are
// computed once per candidate.
func (e *Engine) dedupeMigrated(ctx context.Context, candidates []migrateCandidate, threshold float64) ([]migrateCandidate, int) {
	if len(candidates) <= 1 {
		return candidates, 0
	}
	embs := make([][]float32, len(candidates))
	for i, c := range candidates {
		emb, err := e.embedder.Embed(ctx, c.text, "RETRIEVAL_DOCUMENT")
		if err != nil {
			// Can't embed → can't dedupe → keep it.
			embs[i] = nil
			continue
		}
		embs[i] = emb
	}

	superseded := make(map[int]bool, len(candidates))
	deduped := 0
	// Walk newest-first so the newest of a cluster survives and older dupes are
	// marked superseded.
	for i := len(candidates) - 1; i >= 0; i-- {
		if superseded[i] || embs[i] == nil {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if superseded[j] || embs[j] == nil {
				continue
			}
			if CosineSimilarity(embs[i], embs[j]) >= threshold {
				superseded[j] = true
				deduped++
			}
		}
	}

	kept := make([]migrateCandidate, 0, len(candidates)-deduped)
	for i, c := range candidates {
		if !superseded[i] {
			kept = append(kept, c)
		}
	}
	return kept, deduped
}

// candidatesToPreviewFacts renders the kept candidates as Fact values purely for
// the markdown mirror preview. They are not persisted in dry-run mode.
func candidatesToPreviewFacts(kept []migrateCandidate, namespace string) []*Fact {
	out := make([]*Fact, 0, len(kept))
	for _, c := range kept {
		out = append(out, &Fact{
			Namespace: namespace,
			Kind:      c.kind,
			Text:      c.text,
			Files:     append([]string(nil), c.source.Files...),
			Source:    FactSourceMigrated,
			CreatedAt: c.source.CreatedAt,
		})
	}
	return out
}

// loadV1Sources reads detail memories + knowledge docs for the namespace from
// the live store. Detail memory file associations and timestamps are preserved.
func (e *Engine) loadV1Sources(namespace string) ([]v1SourceItem, error) {
	var items []v1SourceItem

	// Detail memories (repo-scoped under namespace).
	e.mu.RLock()
	details, err := e.store.GetDetailMemories(namespace)
	e.mu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("load detail memories: %w", err)
	}
	for _, d := range details {
		text := strings.TrimSpace(d.Text)
		if text == "" {
			continue
		}
		items = append(items, v1SourceItem{
			SourceID:   d.ID,
			SourceKind: "detail",
			Namespace:  namespace,
			Text:       text,
			Type:       d.Type,
			Files:      append([]string(nil), d.Files...),
			CreatedAt:  d.CreatedAt,
		})
	}

	// Knowledge docs (already namespace-scoped).
	e.mu.RLock()
	docs, err := e.store.GetKnowledgeDocs(namespace)
	e.mu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("load knowledge docs: %w", err)
	}
	for _, doc := range docs {
		text := strings.TrimSpace(doc.Content)
		if text == "" {
			continue
		}
		items = append(items, v1SourceItem{
			SourceID:   doc.ID,
			SourceKind: "knowledge",
			Namespace:  namespace,
			Text:       text,
			Type:       "",
			Files:      append([]string(nil), doc.Files...),
			CreatedAt:  doc.CreatedAt,
		})
	}

	return items, nil
}

// resolveArchivePath returns the absolute path to the namespace's v1 archive
// file and the directory it lives in.
func resolveArchivePath(namespace, archiveDir string) (filePath, dir string, err error) {
	if archiveDir == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", "", fmt.Errorf("migrate-v2: resolve home dir: %w", herr)
		}
		archiveDir = filepath.Join(home, ".dualmem-v1-archive")
	}
	archiveDir, err = filepath.Abs(archiveDir)
	if err != nil {
		return "", "", fmt.Errorf("migrate-v2: resolve archive dir: %w", err)
	}
	fname := sanitizeNamespace(namespace) + ".json"
	return filepath.Join(archiveDir, fname), archiveDir, nil
}

// --- default curator (LLM-backed) ---

// defaultCurator builds the LLM-backed curator from the engine's configured
// TextGenerator, using the same plumbing as distill.go.
func (e *Engine) defaultCurator() (FactCurator, error) {
	gen, ok := e.cfg.Summarizer.(TextGenerator)
	if !ok || gen == nil {
		return nil, fmt.Errorf("migrate-v2: summarizer does not implement TextGenerator (needed for curation)")
	}
	return &llmCurator{gen: gen}, nil
}

// llmCurator implements FactCurator by batching items into one LLM call.
type llmCurator struct {
	gen TextGenerator
}

func (c *llmCurator) Curate(ctx context.Context, batch []v1SourceItem) ([]curatedFact, error) {
	prompt := formatCuratePrompt(batch)
	resp, err := c.gen.GenerateText(ctx, prompt, 2048)
	if err != nil {
		return nil, err
	}
	return parseCurateResponse(resp, batch)
}

// curateResponse is the expected JSON shape from the curation LLM.
type curateResponse struct {
	Items []struct {
		SourceID string `json:"source_id"`
		Verdict  string `json:"verdict"` // "keep" | "skip"
		Kind     string `json:"kind"`    // one of FactKind* when keep
		Text     string `json:"text"`    // 1-3 self-contained sentences when keep
	} `json:"items"`
}

// formatCuratePrompt builds the per-batch curation prompt. Each input item gets
// a stable index the LLM echoes back via source_id.
func formatCuratePrompt(batch []v1SourceItem) string {
	var b strings.Builder
	b.WriteString("You are a memory curation system. For each SOURCE item below, decide whether it is a durable fact worth keeping for a future coding agent, or narrative/episodic content that should be skipped.\n\n")
	b.WriteString("Fact kinds (pick exactly one when keeping):\n")
	b.WriteString("- decision: a choice made between alternatives, an approach selected or rejected\n")
	b.WriteString("- deadend: a path tried that did NOT work — highest value, least recorded\n")
	b.WriteString("- gotcha: a fragile/intentional/subtle behavior; something that must not be changed without care\n")
	b.WriteString("- preference: a user taste or workflow choice (editor, language, convention)\n")
	b.WriteString("- reference: where things are / how to run / API pointer (self-contained)\n\n")
	b.WriteString("Skip verdicts: pure session narrative, greetings, in-progress status, trivial exchanges, or anything not self-contained without the original session context.\n\n")
	b.WriteString("When keeping, REWRITE the item as 1-3 self-contained sentences (a future agent reading only this fact must understand it). Drop ephemeral references.\n\n")
	b.WriteString("Respond with ONLY valid JSON (no markdown fences, no prose):\n")
	b.WriteString(`{"items":[{"source_id":"<id>","verdict":"keep|skip","kind":"decision|deadend|gotcha|preference|reference","text":"1-3 self-contained sentences"}]}` + "\n\n")
	b.WriteString("SOURCE ITEMS:\n")
	for _, it := range batch {
		fmt.Fprintf(&b, "---\nsource_id: %s\n", it.SourceID)
		if it.Type != "" {
			fmt.Fprintf(&b, "v1_type: %s\n", it.Type)
		}
		if len(it.Files) > 0 {
			fmt.Fprintf(&b, "files: %s\n", strings.Join(it.Files, ", "))
		}
		fmt.Fprintf(&b, "text: %s\n", it.Text)
	}
	return b.String()
}

// parseCurateResponse decodes the LLM's JSON, tolerating markdown fences and
// trailing prose. Malformed output yields a parse error (the caller treats a
// whole-batch error as "skip every item", never a crash).
func parseCurateResponse(resp string, batch []v1SourceItem) ([]curatedFact, error) {
	cleaned := stripCodeFences(strings.TrimSpace(resp))
	var out curateResponse
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return nil, fmt.Errorf("parse curation response: %w (response: %.200s)", err, resp)
	}
	known := make(map[string]bool, len(batch))
	for _, it := range batch {
		known[it.SourceID] = true
	}
	verdicts := make([]curatedFact, 0, len(out.Items))
	for _, item := range out.Items {
		// Ignore verdicts for source_ids we never sent (LLM hallucination).
		if item.SourceID == "" || !known[item.SourceID] {
			continue
		}
		verdict := strings.ToLower(strings.TrimSpace(item.Verdict))
		if verdict == "skip" {
			verdicts = append(verdicts, curatedFact{SourceID: item.SourceID, Skipped: true})
			continue
		}
		// "keep" with an unknown kind is downgraded to skip rather than dropped,
		// so the item is still counted.
		kind := strings.ToLower(strings.TrimSpace(item.Kind))
		if !ValidFactKinds[kind] || strings.TrimSpace(item.Text) == "" {
			verdicts = append(verdicts, curatedFact{SourceID: item.SourceID, Skipped: true})
			continue
		}
		verdicts = append(verdicts, curatedFact{
			SourceID: item.SourceID,
			Kind:     kind,
			Text:     strings.TrimSpace(item.Text),
		})
	}
	return verdicts, nil
}

// stripCodeFences removes a leading ```json / ``` fence and trailing ``` if the
// LLM wrapped its JSON despite being asked not to.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "```json"):
		s = strings.TrimPrefix(s, "```json")
	case strings.HasPrefix(s, "```"):
		s = strings.TrimPrefix(s, "```")
	}
	s = strings.TrimSuffix(s, "```")
	// If the model emitted prose before/after the JSON object, try to isolate it.
	if i := strings.IndexByte(s, '{'); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndexByte(s, '}'); j >= 0 && j < len(s)-1 {
		s = s[:j+1]
	}
	return strings.TrimSpace(s)
}
