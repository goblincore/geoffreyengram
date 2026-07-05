package dualmem

// Fact stats scorecard — the v2 "are my facts useful?" view.
//
// This file implements Engine.FactStats, the read side of the task-7
// instrumentation loop. It aggregates per-kind counts (facts, served, hit),
// surfaces dead facts (served repeatedly, never hit) and staleness candidates
// (git_commit far behind HEAD). The dead-facts signal is the practical
// replacement for the deleted rate_context feedback loop: instead of asking the
// agent to rate context, we observe that a fact keeps getting served and never
// correlates with a file touch.

import (
	"fmt"
	"sort"
)

// FactStats computes the v2 scorecard over the store. See FactStatsOpts for
// tunables; zero-value fields use the plan defaults (dead min-serves 5, stale
// max-commits-behind 50). The scorecard is repo-scoped to opts.Namespaces
// (empty = all facts).
//
// Staleness is best-effort: when RootDir is not a git repo, or a fact's
// git_commit can't be resolved (rebased/GC'd, shallow clone), that fact is
// treated as non-stale. We can't prove staleness, so we don't flag it.
func (e *Engine) FactStats(opts FactStatsOpts) (*FactScorecard, error) {
	if opts.DeadMinServes <= 0 {
		opts.DeadMinServes = 5
	}
	if opts.StaleMaxCommitsBehind <= 0 {
		opts.StaleMaxCommitsBehind = 50
	}
	if opts.DeadLimit <= 0 {
		opts.DeadLimit = 20
	}
	if opts.StaleLimit <= 0 {
		opts.StaleLimit = 20
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	rows, err := e.store.GetFactStatsCounts(opts.Namespaces)
	if err != nil {
		return nil, fmt.Errorf("dualmem: fact stats counts: %w", err)
	}
	scorecard := &FactScorecard{Opts: opts}
	for _, r := range rows {
		ks := FactKindStats{Kind: r.Kind, FactsCount: r.FactsCount, ServedCount: r.ServedCount, HitCount: r.HitCount}
		scorecard.ByKind = append(scorecard.ByKind, ks)
		scorecard.Overall.FactsCount += ks.FactsCount
		scorecard.Overall.ServedCount += ks.ServedCount
		scorecard.Overall.HitCount += ks.HitCount
	}
	// Overall.Kind stays "" to mark it as the roll-up row.
	scorecard.Overall.Kind = ""

	dead, err := e.store.GetDeadFacts(opts.Namespaces, opts.DeadMinServes, opts.DeadLimit)
	if err != nil {
		return nil, fmt.Errorf("dualmem: dead facts: %w", err)
	}
	scorecard.Dead = dead

	stale, err := e.computeStaleFacts(opts)
	if err != nil {
		return nil, fmt.Errorf("dualmem: stale facts: %w", err)
	}
	scorecard.Stale = stale

	return scorecard, nil
}

// computeStaleFacts loads git_commit-bearing facts and keeps those whose commit
// is more than opts.StaleMaxCommitsBehind behind HEAD. Commit distance is via
// git (countCommitsBetween); unresolvable commits are non-stale.
func (e *Engine) computeStaleFacts(opts FactStatsOpts) ([]StaleFact, error) {
	candidates, err := e.store.GetStaleFactCandidates(opts.Namespaces, 0)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	headCommit := gitCurrentCommit(e.cfg.RootDir)
	if headCommit == "" {
		return nil, nil // not a git repo — can't score staleness
	}

	type scored struct {
		f      StaleFact
		behind int
	}
	var picks []scored
	for _, c := range candidates {
		if c.GitCommit == "" || c.GitCommit == headCommit {
			continue
		}
		behind := countCommitsBetween(e.cfg.RootDir, c.GitCommit, headCommit)
		if behind > opts.StaleMaxCommitsBehind {
			picks = append(picks, scored{f: c, behind: behind})
		}
	}
	// Most-behind first; tiebreak by id for stable output.
	sort.SliceStable(picks, func(i, j int) bool {
		if picks[i].behind != picks[j].behind {
			return picks[i].behind > picks[j].behind
		}
		return picks[i].f.ID < picks[j].f.ID
	})
	limit := opts.StaleLimit
	if limit <= 0 || limit > len(picks) {
		limit = len(picks)
	}
	out := make([]StaleFact, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, picks[i].f)
	}
	return out, nil
}
