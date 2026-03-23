package dualmem

import (
	"regexp"
	"strings"
)

// ImportanceScorer computes the importance score for routing memories
// to the Detail Path (>= theta) or Sketch Path (< theta).
//
// Formula:
//
//	importance = (sectorBonus * 0.3) + (salience * 0.3) + (specificity * 0.2) + (novelty * 0.2)
type ImportanceScorer struct {
	Theta      float64         // routing threshold (default 0.65)
	DetailBias map[string]bool // sectors that get a 1.0 bonus (biased toward Detail Path)
}

// Score computes the importance of a memory.
// existingMaxSim is the max cosine similarity against existing Detail memories (0 if none).
// memType is the memory type tag ("warning", "decision", "continuity", or "").
func (s *ImportanceScorer) Score(sector string, salience float64, content string, existingMaxSim float64, memType string) float64 {
	sectorBonus := s.sectorBonusScore(sector)
	specificity := specificityScore(content)
	novelty := noveltyScore(existingMaxSim)

	score := sectorBonus*0.3 + salience*0.3 + specificity*0.2 + novelty*0.2

	// Typed memories (warning, decision, continuity) are always important enough
	// for the Detail Path — they represent explicit human intent to persist.
	if memType != "" {
		minScore := s.Theta + 0.05 // ensure they clear the threshold
		if score < minScore {
			score = minScore
		}
	}

	return score
}

// IsDetail returns true if the score meets the importance threshold.
func (s *ImportanceScorer) IsDetail(score float64) bool {
	return score >= s.Theta
}

// sectorBonusScore returns 1.0 for sectors in the DetailBias set,
// 0.0 for everything else.
func (s *ImportanceScorer) sectorBonusScore(sector string) float64 {
	if s.DetailBias[strings.ToLower(sector)] {
		return 1.0
	}
	return 0.0
}

// Heuristic regex patterns for specificity detection.
var (
	// Proper nouns: capitalized words not at sentence start (rough heuristic).
	reProperNoun = regexp.MustCompile(`\b[A-Z][a-z]+(?:\s[A-Z][a-z]+)*\b`)
	// Numbers: integers, decimals, percentages.
	reNumber = regexp.MustCompile(`\b\d+(?:\.\d+)?%?\b`)
	// Dates: various date-like patterns.
	reDate = regexp.MustCompile(`\b(?:\d{1,2}[/-]\d{1,2}[/-]\d{2,4}|\d{4}[/-]\d{2}[/-]\d{2}|(?:January|February|March|April|May|June|July|August|September|October|November|December)\s+\d{1,2})\b`)
	// Quoted strings.
	reQuoted = regexp.MustCompile(`"[^"]{2,}"`)
)

// specificityScore returns 0.0–1.0 based on presence of specific markers in content.
func specificityScore(content string) float64 {
	var score float64
	if len(reProperNoun.FindAllString(content, 5)) > 0 {
		score += 0.3
	}
	if len(reNumber.FindAllString(content, 3)) > 0 {
		score += 0.25
	}
	if reDate.MatchString(content) {
		score += 0.25
	}
	if reQuoted.MatchString(content) {
		score += 0.2
	}
	if score > 1.0 {
		score = 1.0
	}
	return score
}

// noveltyScore returns 1.0 if no existing Detail memory is similar (cosine > 0.85),
// decays linearly to 0.0 at similarity 1.0.
func noveltyScore(existingMaxSim float64) float64 {
	if existingMaxSim <= 0.85 {
		return 1.0
	}
	// Linear decay from 1.0 at 0.85 to 0.0 at 1.0
	return (1.0 - existingMaxSim) / 0.15
}
