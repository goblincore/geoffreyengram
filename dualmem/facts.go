package dualmem

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Facts are the durable, provenance-stamped primitives of dualmem v2 — the
// primary write target that replaces detail memories. This file holds the
// Engine-level API; persistence lives in store_sqlite.go and the Store interface
// (see docs/superpowers/plans/2026-07-04-dualmem-v2.md, P2).
//
// Lifecycle is supersede-only: a stale/wrong fact is marked superseded_by a
// newer fact rather than decayed or deleted. There is no hard delete.

// AddFact validates, embeds, and persists a new fact. Kind and source must be
// from the accepted sets; text must be non-empty. GitCommit is stamped from the
// repo HEAD (cfg.RootDir) when available. ID and CreatedAt are filled in if the
// caller leaves them zero. The stored fact is returned with its embedding.
func (e *Engine) AddFact(ctx context.Context, f Fact) (*Fact, error) {
	if err := validateFactInput(&f); err != nil {
		return nil, err
	}

	// Defaults for caller-managed identity fields.
	if f.ID == "" {
		f.ID = generateID()
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC()
	}
	if f.Files == nil {
		f.Files = []string{}
	}

	// Stamp provenance from the repo HEAD when available.
	if f.GitCommit == "" {
		f.GitCommit = gitCurrentCommit(e.cfg.RootDir)
	}

	embedding, err := e.embedder.Embed(ctx, f.Text, "RETRIEVAL_DOCUMENT")
	if err != nil {
		return nil, fmt.Errorf("dualmem: embed fact: %w", err)
	}
	f.Vector = embedding

	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.store.InsertFact(&f, embedding); err != nil {
		return nil, fmt.Errorf("dualmem: insert fact: %w", err)
	}
	return &f, nil
}

// GetFact loads a fact by ID, regardless of supersede state.
func (e *Engine) GetFact(id string) (*Fact, error) {
	if id == "" {
		return nil, fmt.Errorf("dualmem: empty fact id")
	}
	f, err := e.store.GetFact(id)
	if err != nil {
		return nil, fmt.Errorf("dualmem: get fact %q: %w", id, err)
	}
	return f, nil
}

// ListFacts returns facts for a namespace, optionally filtered by kind.
// Superseded facts are excluded by default; pass includeSuperseded=true to walk
// the full history. Namespace "" selects user-global facts (preferences).
func (e *Engine) ListFacts(namespace, kind string, includeSuperseded bool) ([]*Fact, error) {
	if kind != "" && !ValidFactKinds[kind] {
		return nil, fmt.Errorf("dualmem: invalid fact kind %q", kind)
	}
	return e.store.ListFacts(namespace, kind, includeSuperseded)
}

// SupersedeFact inserts newFact and atomically marks oldID as superseded by it.
// The new fact inherits oldID's namespace when its own is empty. Any chain that
// already pointed at oldID stays walkable: A→oldID→newFact.
//
// Calling this on an ID that is itself already superseded returns an error —
// supersede the current head of the chain instead.
func (e *Engine) SupersedeFact(ctx context.Context, oldID string, newFact Fact) (*Fact, error) {
	if oldID == "" {
		return nil, fmt.Errorf("dualmem: empty oldID")
	}
	if err := validateFactInput(&newFact); err != nil {
		return nil, err
	}

	e.mu.Lock()
	old, err := e.store.GetFact(oldID)
	e.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("dualmem: supersede: load %q: %w", oldID, err)
	}
	if old.SupersededBy != "" {
		return nil, fmt.Errorf("dualmem: %q is already superseded by %q; supersede the head instead", oldID, old.SupersededBy)
	}

	// Keep the chain in one namespace by default.
	if newFact.Namespace == "" {
		newFact.Namespace = old.Namespace
	}
	if newFact.Files == nil {
		newFact.Files = []string{}
	}
	if newFact.ID == "" {
		newFact.ID = generateID()
	}
	if newFact.CreatedAt.IsZero() {
		newFact.CreatedAt = time.Now().UTC()
	}
	if newFact.GitCommit == "" {
		newFact.GitCommit = gitCurrentCommit(e.cfg.RootDir)
	}

	embedding, err := e.embedder.Embed(ctx, newFact.Text, "RETRIEVAL_DOCUMENT")
	if err != nil {
		return nil, fmt.Errorf("dualmem: embed superseding fact: %w", err)
	}
	newFact.Vector = embedding

	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.store.InsertFact(&newFact, embedding); err != nil {
		return nil, fmt.Errorf("dualmem: insert superseding fact: %w", err)
	}
	if err := e.store.SupersedeFact(oldID, newFact.ID); err != nil {
		return nil, fmt.Errorf("dualmem: mark %q superseded: %w", oldID, err)
	}
	return &newFact, nil
}

// SearchFacts recalls facts by embedding similarity blended with file-path
// match. Searching a repo namespace always blends in user-global (namespace "")
// facts too, so a repo query can surface cross-repo preferences. Superseded
// facts are never returned. Each returned fact has Similarity and FileMatch set.
func (e *Engine) SearchFacts(ctx context.Context, query, namespace, kind string, limit int) ([]*Fact, error) {
	var kinds []string
	if kind != "" {
		kinds = []string{kind}
	}
	return e.SearchFactsMulti(ctx, query, namespace, kinds, limit)
}

// SearchFactsMulti is the multi-kind variant of SearchFacts. kinds is an OR
// filter: an empty slice means "any kind". It embeds the query once and scores
// candidates from the namespace plus the user-global ("") namespace. Use this
// for sugar tools like precedent (decision|deadend) so the query is embedded
// exactly once.
func (e *Engine) SearchFactsMulti(ctx context.Context, query, namespace string, kinds []string, limit int) ([]*Fact, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("dualmem: empty search query")
	}
	for _, k := range kinds {
		if !ValidFactKinds[k] {
			return nil, fmt.Errorf("dualmem: invalid fact kind %q", k)
		}
	}
	if limit <= 0 {
		limit = 10
	}

	qEmb, err := e.embedder.Embed(ctx, query, "RETRIEVAL_QUERY")
	if err != nil {
		return nil, fmt.Errorf("dualmem: embed search query: %w", err)
	}
	return e.searchFactsMultiWithEmbedding(ctx, query, namespace, kinds, limit, qEmb)
}

// searchFactsMultiWithEmbedding ranks facts with a caller-supplied query
// embedding. Callers must provide a validated, non-empty query and limit.
func (e *Engine) searchFactsMultiWithEmbedding(ctx context.Context, query, namespace string, kinds []string, limit int, queryEmbedding []float32) ([]*Fact, error) {
	// Always blend user-global ("") into the searched namespaces, deduped.
	namespaces := []string{namespace, ""}
	if namespace == "" {
		namespaces = []string{""}
	}

	e.mu.RLock()
	candidates, err := e.store.GetFactsByNamespacesKinds(namespaces, kinds, false)
	e.mu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("dualmem: load facts for search: %w", err)
	}

	return rankFacts(candidates, queryEmbedding, query, limit), nil
}

// rankFacts scores candidates by embedding similarity blended with file-path
// match, then sorts (blend score, then LastHitAt, then CreatedAt) and trims to
// limit. Pure function shared by SearchFacts/SearchFactsMulti.
func rankFacts(candidates []*Fact, qEmb []float32, query string, limit int) []*Fact {
	for _, f := range candidates {
		f.Similarity = CosineSimilarity(qEmb, f.Vector)
		f.FileMatch = factFileMatchScore(query, f.Files)
	}

	sort.Slice(candidates, func(i, j int) bool {
		si, sj := factBlendScore(candidates[i]), factBlendScore(candidates[j])
		if si != sj {
			return si > sj
		}
		// Tiebreak: more recently touched first, then created_at.
		if !candidates[i].LastHitAt.Equal(candidates[j].LastHitAt) {
			return candidates[i].LastHitAt.After(candidates[j].LastHitAt)
		}
		return candidates[i].CreatedAt.After(candidates[j].CreatedAt)
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

// factBlendScore combines embedding similarity with file-path match.
// Cosine similarity is the primary signal (weight 0.75); a file whose path
// appears in the query lifts the score up to the remaining 0.25. Both inputs
// are in [0,1] so the blended score stays in [0,1].
func factBlendScore(f *Fact) float64 {
	return 0.75*f.Similarity + 0.25*f.FileMatch
}

// factFileMatchScore returns the fraction of a fact's files whose (lowercased)
// path appears as a substring of the query. A query that names a file strongly
// signals intent for facts citing that file.
func factFileMatchScore(query string, files []string) float64 {
	if len(files) == 0 {
		return 0
	}
	q := strings.ToLower(query)
	matches := 0
	for _, f := range files {
		if f == "" {
			continue
		}
		if strings.Contains(q, strings.ToLower(f)) {
			matches++
		}
	}
	return float64(matches) / float64(len(files))
}

// validateFactInput enforces the kind/source/text contract for AddFact and
// SupersedeFact. It does not touch identity (ID/CreatedAt) or provenance
// (GitCommit), which are stamped by the caller path.
func validateFactInput(f *Fact) error {
	if !ValidFactKinds[f.Kind] {
		return fmt.Errorf("dualmem: invalid fact kind %q (want one of decision|deadend|gotcha|preference|reference)", f.Kind)
	}
	if !ValidFactSources[f.Source] {
		return fmt.Errorf("dualmem: invalid fact source %q (want one of verified|inferred|doc|migrated)", f.Source)
	}
	if strings.TrimSpace(f.Text) == "" {
		return fmt.Errorf("dualmem: fact text must be non-empty")
	}
	return nil
}

// FactsForFile returns non-superseded facts in the namespace (plus user-global
// "") whose files cite the given path. The path is matched literally and as a
// basename, so callers may pass either a repo-relative path
// ("dualmem/facts.go") or a bare filename ("facts.go"). Used by the reworked
// file_context pull tool. No embedding is needed — this is a path lookup.
func (e *Engine) FactsForFile(namespace, path string, limit int) ([]*Fact, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("dualmem: empty file path")
	}
	if limit <= 0 {
		limit = 10
	}

	namespaces := []string{namespace, ""}
	if namespace == "" {
		namespaces = []string{""}
	}
	basename := filepath.Base(path)

	e.mu.RLock()
	defer e.mu.RUnlock()
	facts, err := e.store.GetFactsByFile(namespaces, path, basename, limit)
	if err != nil {
		return nil, fmt.Errorf("dualmem: facts for file: %w", err)
	}
	return facts, nil
}

// RecordServed logs that the given facts were surfaced to a session via a pull
// tool or the pinned block. surface names the call site (see FactSurface*
// constants, e.g. "recall", "precedent", "file_context", "pinned").
//
// This is the file-touch instrumentation seam: every surfaced fact is logged
// to served_facts so that a later distill pass can credit a "hit" when the
// session reads/edits a file the fact cites. Empty IDs are dropped; an empty
// sessionID or empty slice is a silent no-op. Recording is idempotent per
// (session_id, fact_id) — re-serving the same fact in one session is a no-op.
// Best-effort: a store error is swallowed rather than failing the tool call.
func (e *Engine) RecordServed(sessionID string, factIDs []string, surface string) {
	if sessionID == "" || len(factIDs) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, id := range factIDs {
		if id == "" {
			continue
		}
		_ = e.store.InsertServedFact(sessionID, id, surface)
	}
}

// RecordFileTouches credits the file-touch hit signal for a session. For each
// fact the session was served, if any of its cited files intersects the
// touched set, the fact's hits counter is bumped and the served_facts row is
// marked credited — once per (session, fact). Idempotent: a second call with
// the same session is a no-op for already-credited pairs. Returns the number
// of newly-credited hits.
//
// This is the distill-time half of the instrumentation loop: it correlates
// what was served against what the session actually touched (read/edited) and
// rewards facts that proved relevant. Best-effort: store errors stop the loop
// early but whatever was already credited stays.
func (e *Engine) RecordFileTouches(sessionID string, touched []string) (int, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(touched) == 0 {
		return 0, nil
	}

	touchedSet := make(map[string]bool, len(touched))
	for _, p := range touched {
		touchedSet[normalizePath(p)] = true
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	served, err := e.store.GetServedFactsForSession(sessionID)
	if err != nil {
		return 0, fmt.Errorf("dualmem: load served facts for session %q: %w", sessionID, err)
	}

	credits := 0
	for _, sf := range served {
		if sf.HitCredited != 0 {
			continue
		}
		if !factFilesIntersect(sf.Files, touchedSet) {
			continue
		}
		credited, err := e.store.MarkServedFactHit(sessionID, sf.FactID)
		if err != nil {
			return credits, fmt.Errorf("dualmem: credit hit for fact %q: %w", sf.FactID, err)
		}
		if credited {
			credits++
		}
	}
	return credits, nil
}

// factFilesIntersect reports whether any of the fact's cited paths is in the
// touched set. Both sides are normalized via normalizePath so callers may pass
// repo-relative paths, basenames, or slash-variant forms interchangeably. A
// fact with no files never intersects (preferences are file-agnostic — they
// are credited only if the caller explicitly touches a path they cite).
func factFilesIntersect(files []string, touched map[string]bool) bool {
	for _, f := range files {
		if f == "" {
			continue
		}
		if touched[normalizePath(f)] {
			return true
		}
	}
	return false
}
