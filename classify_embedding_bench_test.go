//go:build legacy

package engram

import (
	"context"
	"os"
	"testing"
)

// Labeled test corpus for accuracy benchmarking — 50 examples across 4 sectors.
// Reflective is excluded: it's synthesized by Reflect(), not classified from input.
var classifyBenchCorpus = []struct {
	content string
	sector  Sector
}{
	// --- Episodic (13 examples) — events, things that happened ---
	{"Player visited the bar last night and ordered a whiskey", SectorEpisodic},
	{"Remember when you came in during the thunderstorm?", SectorEpisodic},
	{"You stopped by yesterday and brought that friend", SectorEpisodic},
	{"First time I saw you, you looked lost", SectorEpisodic},
	{"That time we stayed up talking until 3am", SectorEpisodic},
	{"You came back after being away for two weeks", SectorEpisodic},
	{"Last Tuesday you tried the new cocktail menu", SectorEpisodic},
	{"We ran into each other at the farmer's market on Saturday", SectorEpisodic},
	{"The power went out while you were here last week", SectorEpisodic},
	{"You showed up right as we were closing", SectorEpisodic},
	{"That night the whole bar sang karaoke together", SectorEpisodic},
	{"Player arrived early today before the lunch rush", SectorEpisodic},
	{"You brought your dog in last month and everyone loved it", SectorEpisodic},

	// --- Semantic (13 examples) — facts, preferences, knowledge ---
	{"Player likes jazz and lo-fi beats", SectorSemantic},
	{"Works as a software engineer in the city", SectorSemantic},
	{"Allergic to peanuts, always asks about ingredients", SectorSemantic},
	{"Prefers window seats and quiet corners", SectorSemantic},
	{"Has a golden retriever named Max", SectorSemantic},
	{"Speaks three languages fluently", SectorSemantic},
	{"Their favorite drink is an old fashioned", SectorSemantic},
	{"Originally from Portland, moved here for work", SectorSemantic},
	{"Vegetarian, doesn't eat any meat or fish", SectorSemantic},
	{"Birthday is in March, turning 30 this year", SectorSemantic},
	{"Plays guitar in a local band on weekends", SectorSemantic},
	{"Has two older sisters who visit sometimes", SectorSemantic},
	{"Doesn't drink coffee, only tea", SectorSemantic},

	// --- Procedural (12 examples) — skills, how-to, processes ---
	{"Knows how to fix the espresso machine when it jams", SectorProcedural},
	{"Taught me the technique for layering cocktails", SectorProcedural},
	{"The process for closing up involves locking both doors", SectorProcedural},
	{"Step one is to preheat the oven to 350 degrees", SectorProcedural},
	{"The method for resetting the register is holding power for 5 seconds", SectorProcedural},
	{"Instructions for the new recipe require soaking overnight", SectorProcedural},
	{"To make the perfect latte, froth the milk to 150 degrees", SectorProcedural},
	{"The trick to their card game is always drawing two cards first", SectorProcedural},
	{"When the dishwasher breaks, you need to clear the filter first", SectorProcedural},
	{"The way to get the best sound is adjusting the bass knob clockwise", SectorProcedural},
	{"For the pasta sauce, let it simmer for at least twenty minutes", SectorProcedural},
	{"To open the cellar, turn the key left then push hard", SectorProcedural},

	// --- Emotional (12 examples) — feelings expressed or observed ---
	{"Player seemed really happy today, kept smiling", SectorEmotional},
	{"Got angry when the music was too loud", SectorEmotional},
	{"Felt nostalgic talking about their hometown", SectorEmotional},
	{"Was nervous about the job interview tomorrow", SectorEmotional},
	{"Appreciated the birthday surprise, looked moved", SectorEmotional},
	{"Seemed down and didn't want to talk much", SectorEmotional},
	{"Got really excited when their favorite song came on", SectorEmotional},
	{"Was frustrated that the kitchen was closed already", SectorEmotional},
	{"Teared up a little when talking about their grandmother", SectorEmotional},
	{"Seemed relieved after finally finishing the big project", SectorEmotional},
	{"Was annoyed at the noise from the construction outside", SectorEmotional},
	{"Lit up with joy when they saw their friend walk in", SectorEmotional},
}

// TestClassifyBenchmark_Heuristic measures heuristic classifier accuracy
// against the labeled corpus. Runs without any API calls.
func TestClassifyBenchmark_Heuristic(t *testing.T) {
	classifier := NewHeuristicClassifier("")

	correct := 0
	for _, tc := range classifyBenchCorpus {
		got := classifier.Classify(tc.content)
		if got == tc.sector {
			correct++
		} else {
			t.Logf("MISS: %q → got %s, want %s", tc.content, got, tc.sector)
		}
	}
	total := len(classifyBenchCorpus)
	pct := float64(correct) / float64(total) * 100
	t.Logf("Heuristic accuracy: %d/%d (%.1f%%)", correct, total, pct)
}

// TestClassifyBenchmark_Embedding measures embedding classifier accuracy
// using the mock embedder. Validates classification logic, not embedding quality.
func TestClassifyBenchmark_Embedding(t *testing.T) {
	embedder := &classifyMockEmbedder{dimension: 8}
	classifier, err := NewEmbeddingClassifier(context.Background(), embedder)
	if err != nil {
		t.Fatal(err)
	}

	correct := 0
	for _, tc := range classifyBenchCorpus {
		got := classifier.Classify(tc.content)
		if got == tc.sector {
			correct++
		}
	}
	total := len(classifyBenchCorpus)
	pct := float64(correct) / float64(total) * 100
	t.Logf("Embedding (mock) accuracy: %d/%d (%.1f%%)", correct, total, pct)
}

// TestClassifyBenchmark_EmbeddingLive runs the benchmark with a real Gemini API.
// Skip unless DUALMEM_LIVE_TESTS=1 and GEMINI_API_KEY are set.
//
//	DUALMEM_LIVE_TESTS=1 GEMINI_API_KEY=... go test -tags legacy -run TestClassifyBenchmark_EmbeddingLive -v -count=1
func TestClassifyBenchmark_EmbeddingLive(t *testing.T) {
	if os.Getenv("DUALMEM_LIVE_TESTS") != "1" {
		t.Skip("set DUALMEM_LIVE_TESTS=1 to run provider-backed tests")
	}

	apiKey := getTestAPIKey(t)

	embedder := NewGeminiEmbedder2(apiKey, 768)
	classifier, err := NewEmbeddingClassifier(context.Background(), embedder)
	if err != nil {
		t.Fatalf("init classifier: %v", err)
	}

	heuristic := NewHeuristicClassifier("")

	embCorrect, heurCorrect := 0, 0
	for _, tc := range classifyBenchCorpus {
		embGot := classifier.Classify(tc.content)
		heurGot := heuristic.Classify(tc.content)

		embOK := embGot == tc.sector
		heurOK := heurGot == tc.sector

		if embOK {
			embCorrect++
		}
		if heurOK {
			heurCorrect++
		}

		marker := "  "
		if embOK != heurOK {
			marker = "△ " // disagreement
		}
		if !embOK && !heurOK {
			marker = "✗ " // both wrong
		}
		t.Logf("%s%-60s emb=%-12s heur=%-12s want=%-12s", marker, tc.content, embGot, heurGot, tc.sector)
	}

	total := len(classifyBenchCorpus)
	t.Logf("")
	t.Logf("=== Results (4 sectors, no reflective) ===")
	t.Logf("Corpus size: %d examples", total)
	t.Logf("Embedding classifier: %d/%d (%.1f%%)", embCorrect, total, float64(embCorrect)/float64(total)*100)
	t.Logf("Heuristic classifier: %d/%d (%.1f%%)", heurCorrect, total, float64(heurCorrect)/float64(total)*100)
	t.Logf("Agreement: %d/%d", countAgreement(classifier, heuristic), total)

	embPct := float64(embCorrect) / float64(total) * 100
	if embPct < 80 {
		t.Logf("⚠ Embedding classifier below 80%% — keep as media-only classifier")
	} else {
		t.Logf("✓ Embedding classifier ≥80%% — candidate to replace heuristic for text too")
	}
}

func countAgreement(emb *EmbeddingClassifier, heur *HeuristicClassifier) int {
	agree := 0
	for _, tc := range classifyBenchCorpus {
		if emb.Classify(tc.content) == heur.Classify(tc.content) {
			agree++
		}
	}
	return agree
}

func getTestAPIKey(t *testing.T) string {
	t.Helper()
	for _, env := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		if key := os.Getenv(env); key != "" {
			return key
		}
	}
	t.Skip("no GEMINI_API_KEY set — skipping live benchmark")
	return ""
}
