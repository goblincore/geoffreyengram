package dualmem

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Markdown mirror for durable facts (dualmem v2, principle 5: the user must be
// able to read and correct what the system believes). SQLite stays the source of
// truth; this file renders live facts as grouped markdown and round-trips edits
// back into the store via the supersede lifecycle. See
// docs/superpowers/plans/2026-07-04-dualmem-v2.md (P2).
//
// Export groups live facts by kind, then by namespace. Each bullet carries a
// provenance suffix "(source, git short-sha, YYYY-MM-DD)" and a stable ID anchor
// as an HTML comment on the same line:
//
//	- Chose SQLite over Postgres. <!-- fact:1700000000-1234 --> (verified, abc1234, 2026-07-04)
//
// Import reconciles the file against the live store:
//   - matching ID, unchanged text  -> no-op
//   - matching ID, edited text     -> new fact superseding the old (source preserved)
//   - live ID absent from the file -> retire (supersede-by-self, no successor)
//   - bullet without an ID comment  -> new fact (source=verified), kind from its section

// factKindOrder is the canonical section order in the export.
var factKindOrder = []string{
	FactKindDecision,
	FactKindDeadEnd,
	FactKindGotcha,
	FactKindPreference,
	FactKindReference,
}

// globalNamespaceHeading is the rendered heading for the user-global namespace
// (namespace==""). On import it maps back to "".
const globalNamespaceHeading = "(global)"

// ExportFacts renders all live (non-superseded) facts as grouped markdown. The
// output is deterministic for a given store state, so unchanged content
// round-trips as a no-op on import.
func (e *Engine) ExportFacts() (string, error) {
	e.mu.RLock()
	facts, err := e.store.ListAllFacts(false)
	e.mu.RUnlock()
	if err != nil {
		return "", fmt.Errorf("dualmem: export facts: %w", err)
	}
	return RenderFactsMarkdown(facts), nil
}

// RenderFactsMarkdown formats a set of facts into the grouped markdown mirror.
// It is pure (no I/O) so it can be unit-tested directly. facts may include any
// facts; superseded ones should be filtered by the caller.
func RenderFactsMarkdown(facts []*Fact) string {
	// Group by kind -> namespace -> facts.
	type nsGroup = map[string][]*Fact
	byKind := make(map[string]nsGroup)
	for _, f := range facts {
		if byKind[f.Kind] == nil {
			byKind[f.Kind] = make(nsGroup)
		}
		byKind[f.Kind][f.Namespace] = append(byKind[f.Kind][f.Namespace], f)
	}

	var b strings.Builder
	b.WriteString("# dualmem facts\n\n")
	b.WriteString("<!-- Markdown mirror of the SQLite facts store. Edit text, remove bullets, or\n")
	b.WriteString("     add new bullets, then run `dualmem facts import` to apply. The SQLite store\n")
	b.WriteString("     stays the source of truth. -->\n\n")

	for _, kind := range factKindOrder {
		nsMap, ok := byKind[kind]
		if !ok || len(nsMap) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", kind)

		// Deterministic namespace order: global ("") first, then alphabetical.
		namespaces := make([]string, 0, len(nsMap))
		for ns := range nsMap {
			namespaces = append(namespaces, ns)
		}
		sort.Slice(namespaces, func(i, j int) bool {
			// "" (global) sorts first.
			if namespaces[i] == "" {
				return true
			}
			if namespaces[j] == "" {
				return false
			}
			return namespaces[i] < namespaces[j]
		})

		for _, ns := range namespaces {
			heading := ns
			if ns == "" {
				heading = globalNamespaceHeading
			}
			fmt.Fprintf(&b, "### %s\n\n", heading)
			// Stable within a namespace: oldest first (creation order), so the
			// section reads as a timeline.
			group := nsMap[ns]
			sort.SliceStable(group, func(i, j int) bool {
				return group[i].CreatedAt.Before(group[j].CreatedAt)
			})
			for _, f := range group {
				b.WriteString(FormatFactBullet(f))
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// FormatFactBullet renders one fact as a single markdown bullet with the
// provenance suffix and stable ID anchor comment on the same line.
func FormatFactBullet(f *Fact) string {
	return fmt.Sprintf("- %s <!-- fact:%s --> (%s, %s, %s)",
		f.Text, f.ID, f.Source, shortSHA(f.GitCommit), f.CreatedAt.Format("2006-01-02"))
}

// shortSHA returns the first 7 characters of a git commit hash (the standard
// "short" width) or the whole value if shorter / empty.
func shortSHA(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

// ImportFactsResult summarizes a (dry-run or applied) import.
type ImportFactsResult struct {
	Unchanged []ImportFactItem // matching ID, unchanged text
	Edited    []ImportFactItem // matching ID, edited text -> superseded
	Retired   []ImportFactItem // live ID absent from file -> retired
	Added     []ImportFactItem // new bullet (no ID, or unknown ID) -> inserted
}

// ImportFactItem describes one bullet's disposition. ID is the existing fact ID
// for unchanged/edited/retired, or the new ID for added (empty before commit).
type ImportFactItem struct {
	ID   string
	Text string
	NS   string // namespace as parsed from the section heading
	Kind string // kind as parsed from the section heading
}

// Summary returns a one-line human-readable summary string.
func (r *ImportFactsResult) Summary() string {
	return fmt.Sprintf("unchanged=%d edited=%d retired=%d added=%d",
		len(r.Unchanged), len(r.Edited), len(r.Retired), len(r.Added))
}

// ImportFacts parses markdown produced by ExportFacts and reconciles it against
// the live store. When commit is false it is a dry run: no writes occur, but the
// result reflects exactly what would happen. When commit is true it applies the
// changes (supersede edited, retire removed, insert new).
//
// The existing fact's kind and namespace are preserved on edit (editing text is
// not a re-classification); only the source is explicitly preserved and the
// timestamp is refreshed. Brand-new bullets take kind+namespace from their
// section heading and source=verified.
func (e *Engine) ImportFacts(ctx context.Context, markdown string, commit bool) (*ImportFactsResult, error) {
	parsed, err := ParseFactsMarkdown(markdown)
	if err != nil {
		return nil, err
	}

	e.mu.RLock()
	live, err := e.store.ListAllFacts(false)
	e.mu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("dualmem: import facts: load live: %w", err)
	}
	liveByID := make(map[string]*Fact, len(live))
	for _, f := range live {
		liveByID[f.ID] = f
	}

	res := &ImportFactsResult{}
	seen := make(map[string]bool, len(parsed))

	for _, p := range parsed {
		if p.id != "" {
			if old, ok := liveByID[p.id]; ok {
				seen[p.id] = true
				if normalizeFactText(p.text) == normalizeFactText(old.Text) {
					res.Unchanged = append(res.Unchanged, ImportFactItem{ID: old.ID, Text: old.Text, NS: old.Namespace, Kind: old.Kind})
					continue
				}
				// Edited: supersede old with new, preserving source/kind/ns.
				res.Edited = append(res.Edited, ImportFactItem{ID: old.ID, Text: p.text, NS: old.Namespace, Kind: old.Kind})
				if commit {
					if _, err := e.SupersedeFact(ctx, old.ID, Fact{
						Namespace: old.Namespace,
						Kind:      old.Kind,
						Source:    chooseSource(p.source, old.Source),
						Text:      p.text,
						Files:     old.Files,
					}); err != nil {
						return nil, fmt.Errorf("dualmem: import facts: supersede %q: %w", old.ID, err)
					}
				}
				continue
			}
			// ID present but not in live store: treat as a new addition.
		}
		// New bullet (no ID, or unknown ID).
		ns := p.namespace
		kind := p.kind
		if kind == "" {
			kind = FactKindDecision
		}
		item := ImportFactItem{Text: p.text, NS: ns, Kind: kind, ID: p.id}
		res.Added = append(res.Added, item)
		if commit {
			src := p.source
			if src == "" {
				src = FactSourceVerified
			}
			added, err := e.AddFact(ctx, Fact{
				Namespace: ns,
				Kind:      kind,
				Source:    src,
				Text:      p.text,
			})
			if err != nil {
				return nil, fmt.Errorf("dualmem: import facts: add: %w", err)
			}
			item.ID = added.ID
		}
	}

	// Retire live facts whose ID did not appear in the file.
	for _, f := range live {
		if seen[f.ID] {
			continue
		}
		res.Retired = append(res.Retired, ImportFactItem{ID: f.ID, Text: f.Text, NS: f.Namespace, Kind: f.Kind})
		if commit {
			if err := e.RetireFact(f.ID); err != nil {
				return nil, fmt.Errorf("dualmem: import facts: retire %q: %w", f.ID, err)
			}
		}
	}

	return res, nil
}

// RetireFact marks a fact superseded with no successor (self-reference). Used by
// import when a live fact's bullet is removed from the mirror. There is no hard
// delete; the chain stays auditable.
func (e *Engine) RetireFact(id string) error {
	if id == "" {
		return fmt.Errorf("dualmem: empty fact id")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.store.RetireFact(id); err != nil {
		return fmt.Errorf("dualmem: retire fact %q: %w", id, err)
	}
	return nil
}

// chooseSource prefers the parsed source if present (user may have upgraded
// inferred->verified in the mirror), otherwise preserves the old fact's source.
func chooseSource(parsed, old string) string {
	if parsed != "" && ValidFactSources[parsed] {
		return parsed
	}
	return old
}

// normalizeFactText collapses whitespace so trivial formatting changes don't
// read as edits.
func normalizeFactText(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// --- Markdown parsing ---

type parsedBullet struct {
	namespace string
	kind      string
	text      string
	id        string
	source    string // may be empty if the bullet had no provenance suffix
	sha       string
	date      string
}

var (
	// factIDCommentRe matches the stable ID anchor, e.g. "<!-- fact:ID -->".
	factIDCommentRe = regexp.MustCompile(`<!--\s*fact:([A-Za-z0-9_.:-]+)\s*-->`)
	// factProvenanceRe matches a trailing "(source, sha, YYYY-MM-DD)" suffix.
	factProvenanceRe = regexp.MustCompile(`\(([^,()]+),\s*([^,()]+),\s*(\d{4}-\d{2}-\d{2})\)\s*$`)
)

// ParseFactsMarkdown extracts bullets from the mirror format. It tracks the
// current section kind ("## kind") and namespace ("### namespace") and attaches
// them to each bullet. Lines that don't parse as headings or bullets are
// ignored, so free-form prose in the file is preserved-but-skipped.
func ParseFactsMarkdown(markdown string) ([]parsedBullet, error) {
	var (
		out  []parsedBullet
		kind string
		ns   string
	)
	lines := strings.Split(markdown, "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "### "):
			heading := strings.TrimSpace(strings.TrimPrefix(line, "### "))
			ns = heading
			if ns == globalNamespaceHeading {
				ns = ""
			}
		case strings.HasPrefix(line, "## "):
			kind = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			// Reset namespace when changing kind so a stray bullet before the
			// next ### isn't mis-attributed.
			ns = ""
		case strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "):
			pb, ok := parseBulletLine(line)
			if !ok {
				continue
			}
			pb.kind = kind
			pb.namespace = ns
			out = append(out, pb)
		}
	}
	return out, nil
}

// parseBulletLine extracts the text, optional ID anchor, and optional provenance
// from a single "- ..." line. ok is false if no text remains after stripping
// markers.
func parseBulletLine(line string) (parsedBullet, bool) {
	// Strip the leading bullet marker.
	rest := strings.TrimSpace(strings.TrimPrefix(line, "-"))
	rest = strings.TrimSpace(strings.TrimPrefix(rest, "*"))

	var pb parsedBullet

	// Extract & remove provenance first (it's the trailing paren).
	if m := factProvenanceRe.FindStringSubmatchIndex(rest); m != nil {
		pb.source = strings.TrimSpace(rest[m[2]:m[3]])
		pb.sha = strings.TrimSpace(rest[m[4]:m[5]])
		pb.date = strings.TrimSpace(rest[m[6]:m[7]])
		rest = strings.TrimSpace(rest[:m[0]])
	}

	// Extract & remove the ID anchor.
	if m := factIDCommentRe.FindStringSubmatchIndex(rest); m != nil {
		pb.id = rest[m[2]:m[3]]
		rest = strings.TrimSpace(rest[:m[0]] + rest[m[1]:])
	}

	pb.text = strings.TrimSpace(rest)
	if pb.text == "" {
		return pb, false
	}
	return pb, true
}
