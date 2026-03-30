package dualmem

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// --- Memory clustering ---

// memoryCluster groups related detail memories for synthesis into a knowledge doc.
type memoryCluster struct {
	topic   string           // auto-generated or matched existing topic
	files   []string         // union of all files in the cluster
	members []detailWithVector
}

// clusterMemories groups detail memories by file overlap and semantic similarity.
// Returns clusters with ≥ minSize members. Memories that don't cluster are omitted.
func clusterMemories(memories []detailWithVector, existingDocs []KnowledgeDoc, minSize int) ([]memoryCluster, int) {
	if minSize <= 0 {
		minSize = 3
	}

	// Phase 1: Match memories to existing docs by file overlap or source ID
	docClusters := make(map[string]*memoryCluster) // topic → cluster
	assigned := make(map[string]bool)               // memory ID → assigned

	for _, doc := range existingDocs {
		docClusters[doc.Topic] = &memoryCluster{
			topic: doc.Topic,
			files: doc.Files,
		}
	}

	// Assign memories to existing docs by file overlap
	for i, dm := range memories {
		if assigned[dm.ID] {
			continue
		}
		for topic, cluster := range docClusters {
			if filesOverlap(dm.Files, cluster.files) >= 2 {
				cluster.members = append(cluster.members, memories[i])
				assigned[dm.ID] = true
				// Expand cluster files
				cluster.files = mergeFiles(cluster.files, dm.Files)
				_ = topic
				break
			}
		}
	}

	// Phase 2: Cluster remaining memories by file overlap (≥2 shared files)
	var unassigned []detailWithVector
	for i, dm := range memories {
		if !assigned[dm.ID] {
			unassigned = append(unassigned, memories[i])
		}
	}

	newClusters := clusterByFileOverlap(unassigned, 2)

	// Phase 3: Within each new cluster that's still small, try to absorb nearby
	// unassigned memories by semantic similarity
	for _, nc := range newClusters {
		for _, m := range nc.members {
			assigned[m.ID] = true
		}
	}

	// Remaining truly unassigned — try semantic grouping
	var stillUnassigned []detailWithVector
	for i, dm := range memories {
		if !assigned[dm.ID] {
			stillUnassigned = append(stillUnassigned, memories[i])
		}
	}

	semanticClusters := clusterBySimilarity(stillUnassigned, 0.7)
	for _, sc := range semanticClusters {
		for _, m := range sc.members {
			assigned[m.ID] = true
		}
	}

	// Merge all clusters, apply minSize filter
	allClusters := make([]memoryCluster, 0)
	for _, c := range docClusters {
		if len(c.members) > 0 {
			allClusters = append(allClusters, *c)
		}
	}
	allClusters = append(allClusters, newClusters...)
	allClusters = append(allClusters, semanticClusters...)

	var result []memoryCluster
	orphaned := 0
	for _, c := range allClusters {
		if len(c.members) >= minSize {
			if c.topic == "" {
				c.topic = autoTopicName(c.files, c.members)
			}
			result = append(result, c)
		} else {
			orphaned += len(c.members)
		}
	}

	// Also count truly unassigned as orphaned
	for _, dm := range memories {
		if !assigned[dm.ID] {
			orphaned++
		}
	}

	return result, orphaned
}

// clusterByFileOverlap groups memories sharing ≥ minOverlap files.
// Uses single-linkage: if A overlaps B and B overlaps C, all three are in one cluster.
func clusterByFileOverlap(memories []detailWithVector, minOverlap int) []memoryCluster {
	n := len(memories)
	if n == 0 {
		return nil
	}

	// Union-Find
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if filesOverlap(memories[i].Files, memories[j].Files) >= minOverlap {
				union(i, j)
			}
		}
	}

	groups := make(map[int][]int)
	for i := 0; i < n; i++ {
		root := find(i)
		groups[root] = append(groups[root], i)
	}

	var clusters []memoryCluster
	for _, indices := range groups {
		c := memoryCluster{}
		for _, idx := range indices {
			c.members = append(c.members, memories[idx])
			c.files = mergeFiles(c.files, memories[idx].Files)
		}
		clusters = append(clusters, c)
	}
	return clusters
}

// clusterBySimilarity groups memories by embedding cosine similarity.
// Uses single-linkage with the given threshold.
func clusterBySimilarity(memories []detailWithVector, threshold float64) []memoryCluster {
	n := len(memories)
	if n == 0 {
		return nil
	}

	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if len(memories[i].Vector) > 0 && len(memories[j].Vector) > 0 {
				sim := CosineSimilarity(memories[i].Vector, memories[j].Vector)
				if sim >= threshold {
					union(i, j)
				}
			}
		}
	}

	groups := make(map[int][]int)
	for i := 0; i < n; i++ {
		root := find(i)
		groups[root] = append(groups[root], i)
	}

	var clusters []memoryCluster
	for _, indices := range groups {
		c := memoryCluster{}
		for _, idx := range indices {
			c.members = append(c.members, memories[idx])
			c.files = mergeFiles(c.files, memories[idx].Files)
		}
		clusters = append(clusters, c)
	}
	return clusters
}

// --- Topic naming ---

// autoTopicName generates a short topic identifier from file paths and memory content.
func autoTopicName(files []string, members []detailWithVector) string {
	if len(files) > 0 {
		// Use the most common directory. For flat files (dir="."), use the
		// shortest filename stem to pick a stable, representative name.
		dirCounts := make(map[string]int)
		for _, f := range files {
			dir := filepath.Dir(f)
			if dir != "." {
				dirCounts[dir]++
			}
		}

		// If we have real directories, pick the most common one
		if len(dirCounts) > 0 {
			best := ""
			bestCount := 0
			for dir, count := range dirCounts {
				if count > bestCount || (count == bestCount && dir < best) {
					best = dir
					bestCount = count
				}
			}
			if best != "" {
				return sanitizeTopic(best)
			}
		}

		// All files are flat — use the shortest stem (most likely the "main" file)
		shortest := ""
		for _, f := range files {
			stem := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
			// Skip test files for naming
			if strings.HasSuffix(stem, "_test") || strings.HasSuffix(stem, ".test") {
				continue
			}
			if shortest == "" || len(stem) < len(shortest) || (len(stem) == len(shortest) && stem < shortest) {
				shortest = stem
			}
		}
		if shortest != "" {
			return sanitizeTopic(shortest)
		}
		// All files are test files — use the first one
		stem := strings.TrimSuffix(filepath.Base(files[0]), filepath.Ext(files[0]))
		return sanitizeTopic(stem)
	}

	// Fall back to first few words of the first memory
	if len(members) > 0 {
		words := strings.Fields(members[0].Text)
		if len(words) > 4 {
			words = words[:4]
		}
		return sanitizeTopic(strings.Join(words, "-"))
	}
	return "unnamed"
}

func sanitizeTopic(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, " ", "-")
	// Remove anything that's not alphanumeric, dash, or dot
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			sb.WriteRune(r)
		}
	}
	result := sb.String()
	// Trim leading/trailing dashes
	result = strings.Trim(result, "-")
	if len(result) > 64 {
		result = result[:64]
	}
	if result == "" {
		result = "unnamed"
	}
	return result
}

// --- Synthesis prompt ---

// formatSynthesisPrompt builds the LLM prompt for synthesizing a knowledge doc.
func formatSynthesisPrompt(topic string, files []string, memories []detailWithVector) string {
	var sb strings.Builder
	sb.WriteString("You are writing a knowledge document for a software project. ")
	sb.WriteString("Given these related memories, write a concise document explaining this system or concept.\n\n")

	if topic != "" {
		sb.WriteString(fmt.Sprintf("Topic: %s\n", topic))
	}
	if len(files) > 0 {
		sb.WriteString(fmt.Sprintf("Associated files: %s\n", strings.Join(files, ", ")))
	}

	sb.WriteString("\nMemories:\n")
	for i, m := range memories {
		sb.WriteString(fmt.Sprintf("%d. %s", i+1, m.Text))
		if m.Type != "" {
			sb.WriteString(fmt.Sprintf(" [%s]", m.Type))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`
Guidelines:
- Focus on how things work, architectural decisions, and constraints
- Include any warnings about code that shouldn't be changed
- Write as if explaining to a developer joining the project
- Be concise — aim for 100-300 words
- Use markdown formatting where helpful
- Do NOT include a title — it will be added automatically`)

	return sb.String()
}

// --- Staleness detection ---

// isDocStale checks if a knowledge doc needs re-synthesis because its source
// memories have been updated since the doc was last synthesized.
func isDocStale(doc *KnowledgeDoc, memories []detailWithVector) bool {
	sourceSet := make(map[string]bool)
	for _, id := range doc.SourceIDs {
		sourceSet[id] = true
	}

	for _, dm := range memories {
		if sourceSet[dm.ID] && dm.CreatedAt.After(doc.UpdatedAt) {
			return true
		}
	}
	return false
}

// --- Helpers ---

func filesOverlap(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, f := range a {
		set[f] = true
	}
	count := 0
	for _, f := range b {
		if set[f] {
			count++
		}
	}
	return count
}

func mergeFiles(a, b []string) []string {
	set := make(map[string]bool)
	for _, f := range a {
		set[f] = true
	}
	for _, f := range b {
		set[f] = true
	}
	result := make([]string, 0, len(set))
	for f := range set {
		result = append(result, f)
	}
	sort.Strings(result)
	return result
}

// countNewUncoveredMemories returns how many detail memories aren't covered by any knowledge doc.
func countNewUncoveredMemories(store Store, namespace string) int {
	uncovered, err := store.GetUncoveredMemories(namespace)
	if err != nil {
		return 0
	}
	// Exclude checkpoints and seeds — they have their own rendering
	count := 0
	for _, dm := range uncovered {
		if dm.Type != "checkpoint" && dm.Type != "seed" {
			count++
		}
	}
	return count
}

// synthesizeDoc creates or updates a single knowledge doc from a cluster of memories.
func synthesizeDoc(ctx context.Context, gen TextGenerator, embedder EmbeddingProvider, cluster memoryCluster, namespace string) (*KnowledgeDoc, error) {
	prompt := formatSynthesisPrompt(cluster.topic, cluster.files, cluster.members)
	content, err := gen.GenerateText(ctx, prompt, 500)
	if err != nil {
		return nil, fmt.Errorf("synthesize %q: %w", cluster.topic, err)
	}

	// Embed the synthesized content for later relevance ranking
	embedding, err := embedder.Embed(ctx, content, "RETRIEVAL_DOCUMENT")
	if err != nil {
		return nil, fmt.Errorf("embed doc %q: %w", cluster.topic, err)
	}

	// Collect source IDs
	sourceIDs := make([]string, len(cluster.members))
	for i, m := range cluster.members {
		sourceIDs[i] = m.ID
	}

	now := time.Now()
	doc := &KnowledgeDoc{
		ID:         generateID(),
		Namespace:  namespace,
		Topic:      cluster.topic,
		Content:    strings.TrimSpace(content),
		Files:      cluster.files,
		SourceIDs:  sourceIDs,
		Embedding:  embedding,
		TokenCount: estimateTokens(content),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	return doc, nil
}
