// Command dualmem is a CLI for the DualMem dual-path agent memory system.
//
// Usage:
//
//	dualmem add --ns <namespace> --text "memory content" [--sector semantic] [--salience 0.8]
//	dualmem search --ns <namespace> "query" [--limit 5] [--json]
//	dualmem context --ns <namespace> "query" [--budget 3000] [--json]
//	dualmem profile --ns <namespace> [--json]
//	dualmem status --ns <namespace>
//	dualmem promote --ns <namespace> --id <memory-id>
//	dualmem demote --ns <namespace> --id <memory-id>
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goblincore/geoffreyengram/dualmem"
	"gopkg.in/yaml.v3"
)

// CLIConfig mirrors ~/.config/dualmem/config.yaml
type CLIConfig struct {
	DefaultNamespace string `yaml:"default_namespace"`
	Storage          struct {
		Backend    string `yaml:"backend"`
		SQLitePath string `yaml:"sqlite_path"`
	} `yaml:"storage"`
	Providers struct {
		EmbeddingModel     string `yaml:"embedding_model"`
		EmbeddingAPIKeyEnv string `yaml:"embedding_api_key_env"`
		EmbeddingDimension int    `yaml:"embedding_dimension"`
	} `yaml:"providers"`
	Pipeline struct {
		EpisodeInterval string `yaml:"episode_interval"`
		ArcInterval     string `yaml:"arc_interval"`
		ProfileInterval string `yaml:"profile_interval"`
	} `yaml:"pipeline"`
}

func loadConfig() CLIConfig {
	var cfg CLIConfig

	// Defaults
	cfg.Storage.Backend = "sqlite"
	home, _ := os.UserHomeDir()
	cfg.Storage.SQLitePath = filepath.Join(home, ".local", "share", "dualmem", "memories.db")
	cfg.Providers.EmbeddingAPIKeyEnv = "GEMINI_API_KEY"
	cfg.Providers.EmbeddingDimension = 768
	cfg.Pipeline.EpisodeInterval = "5m"
	cfg.Pipeline.ArcInterval = "24h"
	cfg.Pipeline.ProfileInterval = "168h"

	// Try loading config file
	configDir, _ := os.UserConfigDir()
	configPath := filepath.Join(configDir, "dualmem", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err == nil {
		yaml.Unmarshal(data, &cfg)
	}

	// Also check project-local .dualmem.yaml
	if data, err := os.ReadFile(".dualmem.yaml"); err == nil {
		yaml.Unmarshal(data, &cfg)
	}

	return cfg
}

func resolveNamespace(flagNS string, cfg CLIConfig) string {
	if flagNS != "" {
		return flagNS
	}
	if cfg.DefaultNamespace != "" {
		return cfg.DefaultNamespace
	}

	// Auto-detect from cwd
	cwd, _ := os.Getwd()
	base := filepath.Base(cwd)
	return "claude:" + base
}

func newEngine(cfg CLIConfig) (*dualmem.Engine, error) {
	apiKey := os.Getenv(cfg.Providers.EmbeddingAPIKeyEnv)
	if apiKey == "" {
		// Fallback to GOOGLE_API_KEY
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("no API key: set %s or GOOGLE_API_KEY", cfg.Providers.EmbeddingAPIKeyEnv)
	}

	episodeInterval, _ := time.ParseDuration(cfg.Pipeline.EpisodeInterval)
	arcInterval, _ := time.ParseDuration(cfg.Pipeline.ArcInterval)
	profileInterval, _ := time.ParseDuration(cfg.Pipeline.ProfileInterval)

	return dualmem.New(dualmem.Config{
		SQLitePath:            cfg.Storage.SQLitePath,
		EmbeddingProvider:     dualmem.NewGeminiEmbedder(apiKey, cfg.Providers.EmbeddingDimension),
		Classifier:            dualmem.NewGeminiClassifier(apiKey),
		MaxDetailPerUser:      100,
		ImportanceTheta:       0.65,
		EpisodeBatchInterval:  episodeInterval,
		ArcBuildInterval:      arcInterval,
		ProfileUpdateInterval: profileInterval,
	})
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cfg := loadConfig()
	cmd := os.Args[1]

	switch cmd {
	case "add":
		cmdAdd(cfg)
	case "search":
		cmdSearch(cfg)
	case "context":
		cmdContext(cfg)
	case "profile":
		cmdProfile(cfg)
	case "status":
		cmdStatus(cfg)
	case "promote":
		cmdPromote(cfg)
	case "demote":
		cmdDemote(cfg)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `dualmem — dual-path agent memory CLI

Commands:
  add      Add a memory
  search   Dual-path search
  context  Assemble token-budget-aware context
  profile  Get user/project profile sketch
  status   Memory counts and health
  promote  Pin a memory to Detail Path
  demote   Demote a memory to Sketch Path

Flags (all commands):
  --ns     Namespace (default: auto-detect from cwd or config)
  --json   Output as JSON

Config: ~/.config/dualmem/config.yaml or .dualmem.yaml in project root
API key: GEMINI_API_KEY or GOOGLE_API_KEY environment variable

Examples:
  dualmem add --ns "claude:club-mutant" --text "Auth rewrite is compliance-driven"
  dualmem context --ns "claude:club-mutant" "auth system" --budget 3000
  dualmem search --ns "claude:club-mutant" "user preferences" --limit 5
`)
}

// --- Commands ---

func cmdAdd(cfg CLIConfig) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	text := fs.String("text", "", "Memory text (required)")
	sector := fs.String("sector", "", "Sector hint (semantic, episodic, procedural, emotional, reflective)")
	salience := fs.Float64("salience", 0, "Salience override (0 = default 0.5)")
	session := fs.String("session", "", "Session ID")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	// If --text not provided, use remaining args
	if *text == "" {
		*text = strings.Join(fs.Args(), " ")
	}
	if *text == "" {
		fmt.Fprintln(os.Stderr, "error: --text or positional text required")
		os.Exit(1)
	}

	// CLI adds are intentional saves — default salience to 0.7 (not 0.5).
	// Sector is classified by Gemini Flash Lite unless explicitly overridden.
	sal := *salience
	if sal == 0 {
		sal = 0.7
	}

	namespace := resolveNamespace(*ns, cfg)

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ctx := context.Background()
	err = engine.AddWithOptions(ctx, dualmem.MemoryInput{
		UserMessage: *text,
		SectorHint:  *sector,
		Salience:    sal,
		SessionID:   *session,
	}, namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "ok", "namespace": namespace})
	} else {
		fmt.Printf("Stored in %s\n", namespace)
	}
}

func cmdSearch(cfg CLIConfig) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	limit := fs.Int("limit", 5, "Max results")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	query := strings.Join(fs.Args(), " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "error: query required")
		os.Exit(1)
	}

	namespace := resolveNamespace(*ns, cfg)

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ctx := context.Background()
	result, err := engine.DualSearch(ctx, namespace, query, dualmem.SearchOpts{
		Limit:         *limit,
		IncludeSketch: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(result)
		return
	}

	// Plain text output
	if result.Profile != nil {
		fmt.Printf("[Profile] %s\n", formatProfileOneLine(result.Profile))
	}
	for _, arc := range result.Arcs {
		fmt.Printf("[Arc] %s (similarity: %.2f)\n", arc.SummaryText, arc.Similarity)
	}
	for _, dm := range result.DetailMemories {
		fmt.Printf("[Detail] %s (importance: %.2f, similarity: %.2f, %s)\n",
			truncate(dm.Text, 120), dm.ImportanceScore, dm.Similarity, dm.CreatedAt.Format("2006-01-02"))
	}
	for _, ep := range result.Episodes {
		fmt.Printf("[Episode] %s (similarity: %.2f)\n", truncate(ep.SummaryText, 120), ep.Similarity)
	}

	total := len(result.DetailMemories) + len(result.Episodes) + len(result.Arcs)
	if result.Profile != nil {
		total++
	}
	if total == 0 {
		fmt.Println("(no results)")
	}
}

func cmdContext(cfg CLIConfig) {
	fs := flag.NewFlagSet("context", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	budget := fs.Int("budget", 3000, "Token budget")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	query := strings.Join(fs.Args(), " ")
	if query == "" {
		query = "session context"
	}

	namespace := resolveNamespace(*ns, cfg)

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ctx := context.Background()
	block, err := engine.AssembleContext(ctx, namespace, query, *budget)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(block)
		return
	}

	if block.Text == "" {
		fmt.Println("(no context available)")
		return
	}

	fmt.Println(block.Text)
	fmt.Printf("\n(%d tokens, %d sources)\n", block.TokenCount, len(block.Sources))
}

func cmdProfile(cfg CLIConfig) {
	fs := flag.NewFlagSet("profile", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	namespace := resolveNamespace(*ns, cfg)

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ctx := context.Background()
	profile, err := engine.GetProfile(ctx, namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if profile == nil {
		fmt.Println("(no profile yet)")
		return
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(profile)
		return
	}

	if len(profile.Interests) > 0 {
		fmt.Printf("Interests: %s\n", strings.Join(profile.Interests, ", "))
	}
	if len(profile.PersonalityTraits) > 0 {
		fmt.Printf("Personality: %s\n", strings.Join(profile.PersonalityTraits, ", "))
	}
	if profile.RelationshipHist != "" {
		fmt.Printf("Relationship: %s\n", profile.RelationshipHist)
	}
	if len(profile.KeyPreferences) > 0 {
		fmt.Printf("Preferences: %s\n", strings.Join(profile.KeyPreferences, ", "))
	}
	if profile.CommStyle != "" {
		fmt.Printf("Communication: %s\n", profile.CommStyle)
	}
}

func cmdStatus(cfg CLIConfig) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	fs.Parse(os.Args[2:])

	namespace := resolveNamespace(*ns, cfg)

	// Open store directly for stats
	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ctx := context.Background()

	// Search with empty query to get counts (detail only)
	result, _ := engine.DualSearch(ctx, namespace, "status check", dualmem.SearchOpts{
		Limit:         100,
		IncludeSketch: true,
	})

	detailCount := 0
	episodeCount := 0
	arcCount := 0
	hasProfile := false
	if result != nil {
		detailCount = len(result.DetailMemories)
		episodeCount = len(result.Episodes)
		arcCount = len(result.Arcs)
		hasProfile = result.Profile != nil
	}

	fmt.Printf("Namespace: %s\n", namespace)
	fmt.Printf("DB: %s\n", cfg.Storage.SQLitePath)
	fmt.Printf("Detail memories: %d\n", detailCount)
	fmt.Printf("Episodes: %d\n", episodeCount)
	fmt.Printf("Arcs: %d\n", arcCount)
	fmt.Printf("Profile: %v\n", hasProfile)
}

func cmdPromote(cfg CLIConfig) {
	fs := flag.NewFlagSet("promote", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	id := fs.String("id", "", "Memory ID")
	fs.Parse(os.Args[2:])

	if *id == "" {
		fmt.Fprintln(os.Stderr, "error: --id required")
		os.Exit(1)
	}

	namespace := resolveNamespace(*ns, cfg)
	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	if err := engine.PromoteToDetail(context.Background(), namespace, *id); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Promoted to Detail Path")
}

func cmdDemote(cfg CLIConfig) {
	fs := flag.NewFlagSet("demote", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	id := fs.String("id", "", "Memory ID")
	fs.Parse(os.Args[2:])

	if *id == "" {
		fmt.Fprintln(os.Stderr, "error: --id required")
		os.Exit(1)
	}

	namespace := resolveNamespace(*ns, cfg)
	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	if err := engine.DemoteFromDetail(context.Background(), namespace, *id); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Demoted to Sketch Path")
}

// --- Helpers ---

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}

func formatProfileOneLine(p *dualmem.ProfileSketch) string {
	var parts []string
	if len(p.Interests) > 0 {
		parts = append(parts, strings.Join(p.Interests, ", "))
	}
	if p.CommStyle != "" {
		parts = append(parts, p.CommStyle)
	}
	if len(p.KeyPreferences) > 0 {
		parts = append(parts, strings.Join(p.KeyPreferences, ", "))
	}
	if len(parts) == 0 {
		return "(empty)"
	}
	return strings.Join(parts, " | ")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
