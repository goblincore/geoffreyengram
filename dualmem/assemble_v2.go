package dualmem

// Assembly v2 — the pinned context block.
//
// This file implements the v2 session-start block described in
// docs/superpowers/plans/2026-07-04-dualmem-v2.md ("Assembly v2"). It is the
// production default (Engine.Assemble with AssembleOptions.Legacy=false).
//
// Hard invariants:
//   - No LLM calls. Everything is SQLite reads + git-diff (already computed
//     for incremental codemap refresh).
//   - No filesystem walk. The codemap line reads the STORED map only
//     (store.GetCodeMap); it never calls getOrGenerateCodeMap, which would
//     scan inline on a true first run. This keeps codemap_swr_test.go green
//     (AssembleContext must never trigger a rescan) and means an empty store
//     degrades to a codemap-absent line rather than a scan.
//   - Hard token cap (DefaultPinnedBudget). Items are emitted in priority
//     order and dropped once the budget is exhausted.
//
// Pinned block order (highest signal first):
//  1. Latest handoff/checkpoint (what was in flight).
//  2. Query-relevant memories — ONLY when the caller passes a specific query
//     (not a session-start sentinel). This is the query-aware retrieval that
//     makes `context "<question>"` surface memories about that question, drawn
//     from detail_memories (DualSearch) and repo facts. Session-start assembly
//     skips it and stays embedding-free.
//  3. User-global preference facts (namespace "", kind "preference").
//  4. Top-k facts whose files intersect paths changed since the last session
//     (git diff via ChangedFilesSince).
//  5. One-line codemap status: "[Codebase Map — N commits behind, ...]" or a
//     freshness note. Reuses the v1 header phrasing so the SWR regression
//     test (which asserts on "[Codebase Map" and "commits behind") stays
//     meaningful.
//
// Item 2 is the only part that embeds the query (one retrieval embedding, the
// same cost as `dualmem recall`); items 1/3/4/5 remain SQLite + git-diff only.
//
// Every emitted fact records its ID on ContextBlock.ServedFactIDs so task 7
// instrumentation can log served facts and later credit file-touch hits.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// assemblePinnedV2 builds the v2 pinned context block.
func (e *Engine) assemblePinnedV2(ctx context.Context, userID, query string, tokenBudget int, opts AssembleOptions) (*ContextBlock, error) {
	budget := opts.PinnedBudget
	if budget <= 0 {
		budget = DefaultPinnedBudget
	}
	// When the caller supplies a specific query (not a session-start sentinel),
	// the block also carries query-relevant memories, and the caller's token
	// budget governs how much of that we render. Session-start assembly keeps
	// the tiny, query-blind, embedding-free fast path.
	specific := isSpecificQuery(query)
	if specific && tokenBudget > budget {
		budget = tokenBudget
	}
	changedCap := opts.ChangedFactCap
	if changedCap <= 0 {
		changedCap = DefaultChangedFactCap
	}

	var (
		parts         []string
		sources       []SourceRef
		servedFactIDs []string
		tokensUsed    int
	)
	emittedFacts := make(map[string]bool) // dedup facts across sections

	emit := func(text string, src SourceRef, factID string) bool {
		toks := estimateTokens(text)
		// Hard cap: never exceed the pinned budget. A single oversize item is
		// still emitted if it is the first one (budget not yet consumed) so the
		// block never degrades to totally empty when one valuable item exists;
		// subsequent items are dropped once the budget is full.
		if tokensUsed > 0 && tokensUsed+toks > budget {
			return false
		}
		parts = append(parts, text)
		sources = append(sources, src)
		tokensUsed += toks
		if factID != "" {
			servedFactIDs = append(servedFactIDs, factID)
			emittedFacts[factID] = true
		}
		return true
	}

	// Item 1 — latest handoff/checkpoint.
	var handoffTask string
	if text, src := e.latestHandoffV2(ctx, userID); text != "" {
		emit(text, src, "")
		handoffTask = src.ID
	}

	// Item 2 — query-relevant memories. Only for specific queries: this is the
	// query-aware retrieval that makes `context "<question>"` surface memories
	// about that question rather than just the newest handoff. Reads
	// detail_memories (via DualSearch) and repo facts; empty/session-start
	// queries skip it entirely (no embedding call).
	if specific {
		for _, it := range e.queryRelevantV2(ctx, userID, query, handoffTask, emittedFacts) {
			if !emit(it.text, it.src, it.factID) {
				break
			}
		}
	}

	// Item 3 — user-global preference facts.
	prefs := e.loadPreferenceFactsV2()
	for _, f := range prefs {
		if emittedFacts[f.ID] {
			continue
		}
		entry := formatFactEntry(f, false) // preferences are file-agnostic
		if !emit(entry, SourceRef{Type: "fact", ID: f.ID}, f.ID) {
			break
		}
	}

	// Item 4 — facts touching files changed since the last session.
	changedFacts := e.changedFileFactsV2(userID, changedCap)
	for _, f := range changedFacts {
		if emittedFacts[f.ID] {
			continue
		}
		entry := formatFactEntry(f, true) // show the touched files
		if !emit(entry, SourceRef{Type: "fact", ID: f.ID}, f.ID) {
			break
		}
	}

	// Item 5 — one-line codemap status. Always last; never scans.
	if text, src := e.codemapStatusV2(userID); text != "" {
		emit(text, src, "")
	}

	// Record a session marker for the next session's diff (best-effort,
	// identical to v1's side effect). Only meaningful inside a git repo.
	if e.cfg.RootDir != "" {
		e.recordSessionMarker(userID)
	}

	// Record served facts for instrumentation (task 7). Every fact emitted
	// above is logged via served_facts so the distill-time file-touch
	// correlator can credit hits. Best-effort and only when a session is named.
	if opts.SessionID != "" {
		e.RecordServed(opts.SessionID, servedFactIDs, FactSurfacePinned)
	}

	block := &ContextBlock{
		Text:          strings.Join(parts, "\n\n"),
		TokenCount:    tokensUsed,
		Sources:       sources,
		ServedFactIDs: servedFactIDs,
	}
	return block, nil
}

// latestHandoffV2 returns the most recent checkpoint rendered for context, or
// "" if there is none. Checkpoints carry no expiry field today; the plan adds a
// TTL in a later task, so for now "unexpired" == "exists". The newest by
// CreatedAt wins.
func (e *Engine) latestHandoffV2(ctx context.Context, userID string) (string, SourceRef) {
	details, err := e.store.GetDetailsByType(userID, "checkpoint")
	if err != nil || len(details) == 0 {
		return "", SourceRef{}
	}
	var (
		best     *detailWithVector
		bestTime time.Time
	)
	for i := range details {
		d := &details[i]
		if best == nil || d.CreatedAt.After(bestTime) {
			best = d
			bestTime = d.CreatedAt
		}
	}
	if best == nil {
		return "", SourceRef{}
	}
	var cp Checkpoint
	if err := json.Unmarshal([]byte(best.Text), &cp); err != nil {
		return "", SourceRef{}
	}
	return cp.FormatForContext(), SourceRef{Type: "checkpoint", ID: cp.Task}
}

// sessionStartSentinels are the generic queries the CLI/agents use to fetch a
// session-start block. They carry no retrieval intent, so the query-aware
// section is skipped for them (keeping the fast, embedding-free path).
var sessionStartSentinels = map[string]bool{
	"":                true,
	"session":         true,
	"session start":   true,
	"session context": true,
	"context":         true,
}

// isSpecificQuery reports whether query expresses a real information need
// (as opposed to a session-start sentinel), which gates the query-aware
// memories section in the pinned block.
func isSpecificQuery(query string) bool {
	return !sessionStartSentinels[strings.ToLower(strings.TrimSpace(query))]
}

// pinnedRelevanceFloor is the minimum blended similarity a detail memory must
// clear to enter the query-relevant section. Prevents a near-empty store from
// padding the block with unrelated memories when nothing actually matches.
const pinnedRelevanceFloor = 0.30

// pinnedItem is a renderable block entry: its text, provenance ref, and (for
// facts) the fact ID used for served-fact instrumentation and dedup.
type pinnedItem struct {
	text   string
	src    SourceRef
	factID string
}

// queryRelevantV2 returns memories relevant to a specific query, ordered
// facts-first (curated) then detail memories (raw), for the pinned block's
// query-aware section. It embeds the query once via SearchFacts/DualSearch.
//
//   - skipCheckpointTask drops the checkpoint already shown as the handoff.
//   - alreadyEmitted drops facts already surfaced (e.g. global preferences).
//   - Detail memories below pinnedRelevanceFloor are dropped as noise.
//
// The returned items are budget-unaware; the caller emits them in order and
// stops when the block budget is exhausted.
func (e *Engine) queryRelevantV2(ctx context.Context, userID, query, skipCheckpointTask string, alreadyEmitted map[string]bool) []pinnedItem {
	var items []pinnedItem

	// Facts first — curated, self-contained, provenance-carrying.
	if facts, err := e.SearchFacts(ctx, query, userID, "", 5); err == nil {
		for _, f := range facts {
			if alreadyEmitted[f.ID] {
				continue
			}
			items = append(items, pinnedItem{
				text:   formatFactEntry(f, len(f.Files) > 0),
				src:    SourceRef{Type: "fact", ID: f.ID},
				factID: f.ID,
			})
		}
	}

	// Detail memories — the bulk of accumulated (pre-v2) memory lives here:
	// checkpoints, warnings, investigations. Query-ranked by DualSearch.
	for _, dm := range e.Search(query, userID, 8, nil) {
		if dm.Similarity < pinnedRelevanceFloor {
			continue
		}
		if dm.Type == "checkpoint" {
			var cp Checkpoint
			if err := json.Unmarshal([]byte(dm.Text), &cp); err == nil {
				if cp.Task == skipCheckpointTask {
					continue // already rendered as the handoff
				}
				items = append(items, pinnedItem{
					text: cp.FormatForContext(),
					src:  SourceRef{Type: "checkpoint", ID: cp.Task},
				})
				continue
			}
		}
		entry := formatTypeLabel(dm.Type, dm.Sector, dm.ImportanceScore) + "\n" + dm.Text
		if len(dm.Files) > 0 {
			entry += "\n  Files: " + strings.Join(dm.Files, ", ")
		}
		items = append(items, pinnedItem{
			text: entry,
			src:  SourceRef{Type: "detail", ID: dm.ID},
		})
	}
	return items
}

// loadPreferenceFactsV2 returns user-global preference facts (namespace "",
// kind "preference"), newest first.
func (e *Engine) loadPreferenceFactsV2() []*Fact {
	facts, err := e.store.ListFacts("", FactKindPreference, false)
	if err != nil {
		return nil
	}
	sort.SliceStable(facts, func(i, j int) bool {
		return facts[i].CreatedAt.After(facts[j].CreatedAt)
	})
	return facts
}

// changedFileFactsV2 returns up to cap repo-scoped facts (namespace == userID)
// whose Files intersect the paths changed since the last session marker, newest
// first. When no marker exists (first session) or the diff can't be computed,
// no changed-file facts are emitted — the pinned block still carries the
// handoff, preferences, and codemap status.
func (e *Engine) changedFileFactsV2(userID string, cap int) []*Fact {
	if e.cfg.RootDir == "" || cap <= 0 {
		return nil
	}
	marker, err := e.store.GetLatestSessionMarker(userID)
	if err != nil || marker == nil || marker.Commit == "" {
		return nil
	}
	changed, ok := ChangedFilesSince(e.cfg.RootDir, marker.Commit)
	if !ok || len(changed) == 0 {
		return nil
	}
	changedSet := make(map[string]bool, len(changed))
	for _, c := range changed {
		changedSet[normalizePath(c)] = true
	}

	facts, err := e.store.ListFacts(userID, "", false)
	if err != nil {
		return nil
	}

	type scored struct {
		f       *Fact
		matches int
	}
	var picks []scored
	for _, f := range facts {
		if len(f.Files) == 0 {
			continue
		}
		m := 0
		for _, p := range f.Files {
			if changedSet[normalizePath(p)] {
				m++
			}
		}
		if m > 0 {
			picks = append(picks, scored{f: f, matches: m})
		}
	}
	// More file matches = more central to the in-flight change; tiebreak by
	// recency.
	sort.SliceStable(picks, func(i, j int) bool {
		if picks[i].matches != picks[j].matches {
			return picks[i].matches > picks[j].matches
		}
		return picks[i].f.CreatedAt.After(picks[j].f.CreatedAt)
	})
	if len(picks) > cap {
		picks = picks[:cap]
	}
	out := make([]*Fact, len(picks))
	for i, p := range picks {
		out[i] = p.f
	}
	return out
}

// codemapStatusV2 renders the one-line codemap status from the STORED map only.
// It must never trigger a scan: a missing map yields no line rather than an
// inline build. The phrasing reuses the v1 header ("[Codebase Map — N commits
// behind, ...]") so the SWR regression test, which asserts on those
// substrings, keeps exercising the no-rescan contract through this path.
func (e *Engine) codemapStatusV2(userID string) (string, SourceRef) {
	stored, err := e.store.GetCodeMap(userID)
	if err != nil || stored == nil {
		// No stored map: emit nothing. First-run scan is the codemap worker's
		// job, not the pinned block's.
		return "", SourceRef{}
	}
	_, currentCommit := GetGitState(e.cfg.RootDir)
	commitsBehind := 0
	stale := false
	if currentCommit != "" && stored.GitCommit != currentCommit {
		stale = true
		commitsBehind = countCommitsBetween(e.cfg.RootDir, stored.GitCommit, currentCommit)
	}

	var line string
	switch {
	case stale && commitsBehind > 0:
		line = fmt.Sprintf("[Codebase Map — %d commits behind, run `dualmem index` to refresh]", commitsBehind)
	case stale:
		line = "[Codebase Map — stale, run `dualmem index` to refresh]"
	default:
		short := stored.GitCommit
		if len(short) > 7 {
			short = short[:7]
		}
		line = fmt.Sprintf("[Codebase Map — fresh at %s]", short)
	}
	return line, SourceRef{Type: "codemap", ID: userID}
}

// formatFactEntry renders a single fact for the pinned block. Compact form:
//
//	[preference] Always use tabs for Go. (id: abc123)
//	[decision] Route auth through middleware. Files: auth.go, mw.go (id: def456)
//
// includeFiles controls whether the Files line is shown (preferences usually
// have none, so it's elided there).
func formatFactEntry(f *Fact, includeFiles bool) string {
	label := "[" + f.Kind + "]"
	if f.Source != "" && f.Source != FactSourceVerified {
		label = fmt.Sprintf("[%s/%s]", f.Kind, f.Source)
	}
	entry := fmt.Sprintf("%s %s (id: %s)", label, strings.TrimSpace(f.Text), shortFactID(f.ID))
	if includeFiles && len(f.Files) > 0 {
		entry += "\n  Files: " + strings.Join(f.Files, ", ")
	}
	return entry
}

// shortFactID returns the leading 8 chars of a fact ID for inline display.
func shortFactID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// normalizePath canonicalizes a repo-relative path for set comparison:
// slash-separated, trimmed, no leading "./". Fact Files are stored
// slash-separated and repo-relative (same convention as codemap/diff), so this
// is mostly defensive.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = filepath.ToSlash(p)
	p = strings.TrimPrefix(p, "./")
	return p
}
