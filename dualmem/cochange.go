package dualmem

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// buildCoChangeEdges creates co-change edges between all pairs of files
// mentioned in a memory. Called from AddWithOptions when a memory has 2+ files.
func (e *Engine) buildCoChangeEdges(namespace, memoryID string, files []string) {
	if len(files) < 2 {
		return
	}

	// Normalize file paths: strip leading ./ and use forward slashes
	normalized := make([]string, 0, len(files))
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		f = filepath.ToSlash(f)
		f = strings.TrimPrefix(f, "./")
		normalized = append(normalized, f)
	}

	// Generate all pairs (N choose 2) and upsert co-change edges
	for i := 0; i < len(normalized); i++ {
		for j := i + 1; j < len(normalized); j++ {
			_ = e.store.UpsertCoChange(namespace, normalized[i], normalized[j], memoryID)
		}
	}
}

// GetCoChangeNeighbors returns files that co-change with the given file path.
// Returns a map of neighbor path → strength for use in codemap boosting.
func (e *Engine) GetCoChangeNeighbors(namespace, path string, minStrength float64) map[string]float64 {
	edges, err := e.store.GetCoChangeNeighbors(namespace, path, minStrength, 20)
	if err != nil {
		return nil
	}

	result := make(map[string]float64, len(edges))
	for _, edge := range edges {
		neighbor := edge.TargetPath
		if neighbor == path {
			neighbor = edge.SourcePath
		}
		result[neighbor] = edge.Strength
	}
	return result
}

// GetCoChangeForPaths returns a combined co-change neighbor map for multiple seed paths.
// Used in context assembly to expand file hints via co-change.
func (e *Engine) GetCoChangeForPaths(namespace string, paths []string, minStrength float64) map[string]float64 {
	if len(paths) == 0 {
		return nil
	}

	// Normalize paths
	normalized := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = filepath.ToSlash(p)
		p = strings.TrimPrefix(p, "./")
		normalized = append(normalized, p)
	}

	edges, err := e.store.GetCoChangePaths(namespace, normalized, 50)
	if err != nil {
		return nil
	}

	// Build seed set for exclusion
	seedSet := make(map[string]bool, len(normalized))
	for _, p := range normalized {
		seedSet[p] = true
	}

	// Collect neighbor strengths, keeping max strength per neighbor
	result := make(map[string]float64)
	for _, edge := range edges {
		if edge.Strength < minStrength {
			continue
		}
		for _, path := range []string{edge.SourcePath, edge.TargetPath} {
			if !seedSet[path] {
				if edge.Strength > result[path] {
					result[path] = edge.Strength
				}
			}
		}
	}
	return result
}

// EnrichCoChangeWithEntities walks the entity graph to find concept labels
// that explain why files co-change. For each co-change edge, it finds entities
// shared between both files' memories and adds them as concept labels.
func (e *Engine) EnrichCoChangeWithEntities(namespace string) (int, error) {
	edges, err := e.store.GetCoChangeAll(namespace, 500)
	if err != nil {
		return 0, err
	}

	enriched := 0
	for _, edge := range edges {
		existingConcepts := make(map[string]bool)
		for _, c := range edge.Concepts {
			existingConcepts[c] = true
		}

		// Find entities shared between memories of both files
		srcMems, err := e.store.GetDetailsByFiles(namespace, filepath.Base(edge.SourcePath), nil, 20)
		if err != nil {
			continue
		}
		tgtMems, err := e.store.GetDetailsByFiles(namespace, filepath.Base(edge.TargetPath), nil, 20)
		if err != nil {
			continue
		}

		srcEntities := make(map[string]bool)
		for _, m := range srcMems {
			for _, ent := range m.Entities {
				if ent.Text != "" {
					srcEntities[ent.Text] = true
				}
			}
		}

		conceptSet := make(map[string]bool)
		for c := range existingConcepts {
			conceptSet[c] = true
		}
		for _, m := range tgtMems {
			for _, ent := range m.Entities {
				if ent.Text != "" && srcEntities[ent.Text] && !conceptSet[ent.Text] && ent.Type != "file" {
					conceptSet[ent.Text] = true
				}
			}
		}

		newConcepts := make([]string, 0, len(conceptSet))
		for c := range conceptSet {
			newConcepts = append(newConcepts, c)
		}

		if len(newConcepts) > len(edge.Concepts) {
			conceptsJSON, _ := json.Marshal(newConcepts)
			if err := e.store.UpdateCoChangeConcepts(namespace, edge.SourcePath, edge.TargetPath, string(conceptsJSON)); err != nil {
				continue
			}
			enriched++
		}
	}

	return enriched, nil
}
