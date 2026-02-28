package engram

import (
	"regexp"
	"strings"
)

// KnownEntity is a domain-specific entity to match during extraction.
// For example, a music NPC would add known artists here.
type KnownEntity struct {
	Text string
	Type string // e.g. "music_artist", "game_item", "place"
}

// DefaultEntityExtractor pulls entities from memory content using heuristics.
// Detects bracket names, quoted strings, capitalized phrases, and caller-provided known entities.
// Implements EntityExtractor.
type DefaultEntityExtractor struct {
	KnownEntities []KnownEntity
}

// Extract returns entities found in the content.
func (e *DefaultEntityExtractor) Extract(content string) []Entity {
	var entities []Entity
	seen := make(map[string]bool)

	add := func(text, entityType string) {
		text = strings.TrimSpace(text)
		lower := strings.ToLower(text)
		if text == "" || len(text) < 2 || len(text) > 60 || seen[lower] {
			return
		}
		seen[lower] = true
		entities = append(entities, Entity{Text: text, Type: entityType})
	}

	// 1. Player names in brackets: [PlayerName]: message
	bracketRe := regexp.MustCompile(`\[([A-Za-z0-9_]+)\]`)
	for _, match := range bracketRe.FindAllStringSubmatch(content, -1) {
		add(match[1], "person")
	}

	// 2. Quoted strings (potential song names, topics, etc.)
	quoteRe := regexp.MustCompile(`"([^"]{2,40})"`)
	for _, match := range quoteRe.FindAllStringSubmatch(content, -1) {
		add(match[1], "topic")
	}

	// 3. Known entities (domain-specific, provided by caller)
	if len(e.KnownEntities) > 0 {
		lower := strings.ToLower(content)
		for _, known := range e.KnownEntities {
			if strings.Contains(lower, strings.ToLower(known.Text)) {
				add(known.Text, known.Type)
			}
		}
	}

	// 4. Capitalized multi-word phrases (potential proper nouns, not at sentence start)
	properRe := regexp.MustCompile(`(?:^|[.!?]\s+|\s)([A-Z][a-z]+(?:\s+[A-Z][a-z]+)+)`)
	for _, match := range properRe.FindAllStringSubmatch(content, 5) {
		text := strings.TrimSpace(match[1])
		if !isCommonPhrase(text) {
			add(text, "topic")
		}
	}

	return entities
}

// isCommonPhrase filters out false-positive proper nouns.
func isCommonPhrase(s string) bool {
	common := []string{
		"The", "This", "That", "What", "When", "Where", "How", "Why",
		"I Am", "You Are", "We Are", "They Are",
	}
	lower := strings.ToLower(s)
	for _, c := range common {
		if strings.ToLower(c) == lower {
			return true
		}
	}
	return false
}

// --- Waypoint graph expansion ---

// ExpandViaWaypoints performs one-hop graph expansion from seed memories.
// Returns additional memories linked through shared waypoints (entities).
func ExpandViaWaypoints(store *Store, seedMemories []memoryWithVector, userID string) map[int64]float64 {
	linkWeights := make(map[int64]float64)

	// Collect seed memory IDs
	seedIDs := make(map[int64]bool)
	for _, m := range seedMemories {
		seedIDs[m.ID] = true
	}

	// For each seed memory, get its waypoints, then get other memories sharing those waypoints
	for _, m := range seedMemories {
		waypointIDs, err := store.GetAssociatedWaypointIDs(m.ID)
		if err != nil {
			continue
		}

		for _, wpID := range waypointIDs {
			linked, err := store.GetMemoriesByWaypoint(wpID, userID, seedIDs)
			if err != nil {
				continue
			}
			for _, lm := range linked {
				// Propagate link weight: 0.8 multiplier per hop
				if w := 0.8; w > linkWeights[lm.ID] {
					linkWeights[lm.ID] = w
				}
			}
		}
	}

	return linkWeights
}
