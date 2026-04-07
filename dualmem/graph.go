package dualmem

import "sort"

// FileGraph is a directed weighted graph at file granularity.
// Nodes are file paths; edges represent cross-file symbol references.
// Edge direction: source file → target file (source references a symbol defined in target).
type FileGraph struct {
	Nodes map[string]bool            // Set of all file paths
	Edges map[string]map[string]int  // source → target → reference count
}

// FileRank is a file with its PageRank score and definition tags.
type FileRank struct {
	Path        string `json:"path"`
	Score       float64 `json:"score"`
	Definitions []Tag   `json:"definitions,omitempty"`
}

// BuildFileGraph constructs a file-level dependency graph from extracted tags.
// For each reference tag, an edge is created from the referencing file to the file
// where the symbol is defined. Edge weights accumulate by reference count.
func BuildFileGraph(tags []Tag) *FileGraph {
	g := &FileGraph{
		Nodes: make(map[string]bool),
		Edges: make(map[string]map[string]int),
	}

	// Build definition index: symbol name → file where defined
	defs := make(map[string]string) // name → file
	for _, t := range tags {
		if t.Kind == "def" {
			defs[t.Name] = t.File
		}
		g.Nodes[t.File] = true
	}

	// Create edges from references
	for _, t := range tags {
		if t.Kind != "ref" {
			continue
		}
		defFile, ok := defs[t.Name]
		if !ok {
			continue // reference to unknown symbol, skip
		}
		if t.File == defFile {
			continue // skip self-references within same file
		}
		if g.Edges[t.File] == nil {
			g.Edges[t.File] = make(map[string]int)
		}
		g.Edges[t.File][defFile]++
	}

	return g
}

// PageRank computes personalized PageRank scores for all nodes in the graph.
// personalization maps file paths to bias weights (will be normalized to sum to 1).
// If personalization is nil or empty, uniform teleportation is used (standard PageRank).
// damping is the probability of following an edge (typically 0.85).
// iterations controls convergence (typically 20-50 is sufficient).
func (g *FileGraph) PageRank(personalization map[string]float64, damping float64, iterations int) map[string]float64 {
	n := len(g.Nodes)
	if n == 0 {
		return make(map[string]float64)
	}

	// Initialize scores uniformly
	scores := make(map[string]float64, n)
	for node := range g.Nodes {
		scores[node] = 1.0 / float64(n)
	}

	// Build personalization vector (normalized)
	teleport := make(map[string]float64, n)
	if len(personalization) > 0 {
		sum := 0.0
		for _, v := range personalization {
			sum += v
		}
		if sum > 0 {
			for node := range g.Nodes {
				if v, ok := personalization[node]; ok {
					teleport[node] = v / sum
				} else {
					teleport[node] = 0
				}
			}
		} else {
			// personalization sums to zero, fall back to uniform
			for node := range g.Nodes {
				teleport[node] = 1.0 / float64(n)
			}
		}
	} else {
		for node := range g.Nodes {
			teleport[node] = 1.0 / float64(n)
		}
	}

	// Precompute out-degree and edge targets for each node
	type outInfo struct {
		total  int
		targets map[string]int
	}
	outDegrees := make(map[string]outInfo, n)
	for node := range g.Nodes {
		info := outInfo{targets: g.Edges[node]}
		for _, count := range g.Edges[node] {
			info.total += count
		}
		outDegrees[node] = info
	}

	// Identify dangling nodes (no outgoing edges)
	var dangling []string
	for node := range g.Nodes {
		if outDegrees[node].total == 0 {
			dangling = append(dangling, node)
		}
	}

	oneMinusD := 1.0 - damping

	for i := 0; i < iterations; i++ {
		// Compute dangling node contribution
		danglingSum := 0.0
		for _, d := range dangling {
			danglingSum += scores[d]
		}
		danglingPerNode := danglingSum / float64(n)

		newScores := make(map[string]float64, n)

		// For each node: newScore = (1-d)*teleport + d*(danglingPerNode + sum of incoming)
		for node := range g.Nodes {
			newScores[node] = oneMinusD*teleport[node] + damping*danglingPerNode
		}

		// Add contributions from edges
		for src, targets := range g.Edges {
			srcScore := scores[src]
			srcTotal := outDegrees[src].total
			if srcTotal == 0 {
				continue
			}
			for tgt, count := range targets {
				newScores[tgt] += damping * srcScore * float64(count) / float64(srcTotal)
			}
		}

		scores = newScores
	}

	return scores
}

// RankFiles runs personalized PageRank and returns files sorted by score (descending).
// Each FileRank includes its definition tags from the provided tag set.
//
// When personalization targets specific files, the damping factor is lowered
// to give query-relevant files more weight over structurally central ones.
// Without personalization, standard damping (0.85) is used so the most
// connected/imported files rank highest.
func (g *FileGraph) RankFiles(tags []Tag, personalization map[string]float64) []FileRank {
	// Adaptive damping: lower damping = more personalization influence.
	// Standard PageRank uses 0.85 (85% structure, 15% teleport).
	// With personalization, we lower damping so query-relevant files
	// can overcome structurally central but irrelevant nodes.
	// The damping scales with how focused the personalization is:
	// few personalized files → very low damping (query dominates)
	// many personalized files → moderate damping (blend with structure)
	damping := 0.85
	if len(personalization) > 0 {
		ratio := float64(len(personalization)) / float64(len(g.Nodes))
		switch {
		case ratio < 0.05: // <5% of files match query → strong personalization
			damping = 0.30
		case ratio < 0.20: // 5-20% match → moderate
			damping = 0.50
		default: // >20% match → mild (query is too generic)
			damping = 0.65
		}
	}
	scores := g.PageRank(personalization, damping, 30)

	// Collect definition tags per file
	defs := make(map[string][]Tag)
	for _, t := range tags {
		if t.Kind == "def" {
			defs[t.File] = append(defs[t.File], t)
		}
	}

	// Build ranked list
	result := make([]FileRank, 0, len(g.Nodes))
	for path := range g.Nodes {
		score := scores[path]
		result = append(result, FileRank{
			Path:        path,
			Score:       score,
			Definitions: defs[path],
		})
	}

	// Sort by score descending, then by path for stability
	sort.Slice(result, func(i, j int) bool {
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		return result[i].Path < result[j].Path
	})

	return result
}
