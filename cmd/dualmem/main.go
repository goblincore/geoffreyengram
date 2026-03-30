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

	embedder := dualmem.NewGeminiEmbedder(apiKey, cfg.Providers.EmbeddingDimension)

	sectors := dualmem.CodingSectors()

	// Use EmbeddingClassifier (zero extra API calls per Add, reuses embedding).
	// Falls back to GeminiClassifier if init fails.
	var classifier dualmem.SectorClassifier
	ec, err := dualmem.NewEmbeddingClassifier(context.Background(), embedder, sectors)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: EmbeddingClassifier init failed, falling back to LLM classifier: %v\n", err)
		classifier = dualmem.NewGeminiClassifier(apiKey, sectors)
	} else {
		classifier = ec
	}

	cwd, _ := os.Getwd()
	return dualmem.New(dualmem.Config{
		SQLitePath:            cfg.Storage.SQLitePath,
		EmbeddingProvider:     embedder,
		Classifier:            classifier,
		Summarizer:            dualmem.NewGeminiSummarizer(apiKey, ""),
		Sectors:               sectors,
		MaxDetailPerUser:      100,
		ImportanceTheta:       0.65,
		EpisodeBatchInterval:  episodeInterval,
		ArcBuildInterval:      arcInterval,
		ProfileUpdateInterval: profileInterval,
		RootDir:               cwd,
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
	case "map":
		cmdMap(cfg)
	case "search-code":
		cmdSearchCode(cfg)
	case "diff":
		cmdDiff(cfg)
	case "profile":
		cmdProfile(cfg)
	case "status":
		cmdStatus(cfg)
	case "promote":
		cmdPromote(cfg)
	case "demote":
		cmdDemote(cfg)
	case "checkpoint":
		cmdCheckpoint(cfg)
	case "gc":
		cmdGC(cfg)
	case "seed":
		cmdSeed(cfg)
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
  add         Add a memory
  search      Dual-path search
  context     Assemble token-budget-aware context (includes code map + diff)
  checkpoint  Save/view structured session checkpoints
  map         Generate/view codebase structure map
  diff        Show changes since last session
  profile     Get user/project profile sketch
  status      Memory counts and health
  promote     Pin a memory to Detail Path
  demote      Demote a memory to Sketch Path
  seed        Auto-generate semantic context memories from code structure
  gc          Garbage collect stale/expired memories

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
	sector := fs.String("sector", "", "Sector hint (default: auto-classify; coding presets: decision, warning, map, continuity)")
	salience := fs.Float64("salience", 0, "Salience override (0 = default 0.5)")
	session := fs.String("session", "", "Session ID")
	memType := fs.String("type", "", "Memory type: decision, warning, continuity (default: general)")
	files := fs.String("files", "", "Comma-separated associated file paths")
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

	var filesList []string
	if *files != "" {
		for _, f := range strings.Split(*files, ",") {
			if trimmed := strings.TrimSpace(f); trimmed != "" {
				filesList = append(filesList, trimmed)
			}
		}
	}

	ctx := context.Background()
	err = engine.AddWithOptions(ctx, dualmem.MemoryInput{
		UserMessage: *text,
		SectorHint:  *sector,
		Salience:    sal,
		SessionID:   *session,
		Type:        *memType,
		Files:       filesList,
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
		typeLabel := ""
		if dm.Type != "" {
			typeLabel = dm.Type + ": "
		}
		fmt.Printf("[Detail] %s%s (importance: %.2f, similarity: %.2f, %s)\n",
			typeLabel, truncate(dm.Text, 120), dm.ImportanceScore, dm.Similarity, dm.CreatedAt.Format("2006-01-02"))
		if len(dm.Files) > 0 {
			fmt.Printf("  Files: %s\n", strings.Join(dm.Files, ", "))
		}
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
	intent := fs.String("intent", "", "Task intent override: debug, continue, feature, explore (default: auto-detect)")
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

	var opts *dualmem.ContextOpts
	if *intent != "" {
		opts = &dualmem.ContextOpts{Intent: dualmem.Intent(*intent)}
	}

	ctx := context.Background()
	block, err := engine.AssembleContextWith(ctx, namespace, query, *budget, opts)
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
	intentLabel := string(block.Intent)
	if intentLabel == "" {
		intentLabel = "default"
	}
	fmt.Printf("\n(%d tokens, %d sources, intent: %s)\n", block.TokenCount, len(block.Sources), intentLabel)
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
	id := fs.String("id", "", "Memory ID (single promote)")
	all := fs.Bool("all", false, "Re-evaluate all sketch_raw entries")
	memType := fs.String("type", "", "Type override for promoted memories (warning, decision, continuity)")
	sal := fs.Float64("salience", 0, "Salience override (0 = default)")
	fs.Parse(os.Args[2:])

	if *id == "" && !*all {
		fmt.Fprintln(os.Stderr, "error: --id or --all required")
		os.Exit(1)
	}

	namespace := resolveNamespace(*ns, cfg)
	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	var opts *dualmem.PromoteOpts
	if *memType != "" || *sal > 0 {
		opts = &dualmem.PromoteOpts{Type: *memType, Salience: *sal}
	}

	if *all {
		count, err := engine.ReEvaluateSketchRaw(context.Background(), namespace, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Promoted %d memories to Detail Path\n", count)
	} else {
		if err := engine.PromoteToDetail(context.Background(), namespace, *id, opts); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Promoted to Detail Path")
	}
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

func cmdGC(cfg CLIConfig) {
	fs := flag.NewFlagSet("gc", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	dryRun := fs.Bool("dry-run", false, "Show what would be done without making changes")
	verbose := fs.Bool("verbose", false, "Show each affected entry")
	fs.Parse(os.Args[2:])

	namespace := resolveNamespace(*ns, cfg)
	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	report, err := engine.GarbageCollect(context.Background(), namespace, dualmem.GCOptions{
		DryRun:  *dryRun,
		Verbose: *verbose,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *dryRun {
		fmt.Println("=== Dry Run (no changes made) ===")
	}

	if *verbose && len(report.Entries) > 0 {
		for _, e := range report.Entries {
			fmt.Printf("  [%s] %s: %s\n", e.Action, e.Reason, e.Text)
		}
		fmt.Println()
	}

	total := report.ExpiredEpisodes + report.ExpiredArcs + report.StaleDetails + report.SupersededContinuity + report.AccessColdDetails
	if total == 0 {
		fmt.Println("Nothing to clean up.")
		return
	}

	fmt.Printf("Expired episodes:     %d deleted\n", report.ExpiredEpisodes)
	fmt.Printf("Expired arcs:         %d deleted\n", report.ExpiredArcs)
	fmt.Printf("Git-stale details:    %d demoted\n", report.StaleDetails)
	fmt.Printf("Superseded continuity: %d demoted\n", report.SupersededContinuity)
	fmt.Printf("Access-cold details:  %d demoted\n", report.AccessColdDetails)
	fmt.Printf("Total: %d entries cleaned\n", total)
}

func cmdCheckpoint(cfg CLIConfig) {
	fs := flag.NewFlagSet("checkpoint", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	task := fs.String("task", "", "Task description (required for save)")
	status := fs.String("status", "in_progress", "Status: in_progress, blocked, paused, completed")
	files := fs.String("files", "", "Comma-separated active files")
	done := fs.String("done", "", "Comma-separated completed steps")
	remaining := fs.String("remaining", "", "Comma-separated remaining steps")
	blocked := fs.String("blocked", "", "What's blocking progress")
	decision := fs.String("decision", "", "Pending decision")
	list := fs.Bool("list", false, "List active checkpoints")
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

	if *list {
		checkpoints, err := engine.GetCheckpoints(ctx, namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			json.NewEncoder(os.Stdout).Encode(checkpoints)
			return
		}
		if len(checkpoints) == 0 {
			fmt.Println("(no checkpoints)")
			return
		}
		for _, cp := range checkpoints {
			fmt.Println(cp.FormatForContext())
			fmt.Println()
		}
		return
	}

	if *task == "" {
		// If no --task, use remaining args
		*task = strings.Join(fs.Args(), " ")
	}
	if *task == "" {
		fmt.Fprintln(os.Stderr, "error: --task or positional task description required (or use --list)")
		os.Exit(1)
	}

	cp := &dualmem.Checkpoint{
		Task:            *task,
		Status:          *status,
		DecisionPending: *decision,
		BlockedOn:       *blocked,
	}
	if *files != "" {
		for _, f := range strings.Split(*files, ",") {
			if trimmed := strings.TrimSpace(f); trimmed != "" {
				cp.FilesActive = append(cp.FilesActive, trimmed)
			}
		}
	}
	if *done != "" {
		for _, s := range strings.Split(*done, ",") {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				cp.CompletedSteps = append(cp.CompletedSteps, trimmed)
			}
		}
	}
	if *remaining != "" {
		for _, s := range strings.Split(*remaining, ",") {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				cp.RemainingSteps = append(cp.RemainingSteps, trimmed)
			}
		}
	}

	if err := engine.SaveCheckpoint(ctx, namespace, cp); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(cp)
	} else {
		fmt.Printf("Checkpoint saved: %s [%s]\n", cp.Task, cp.Status)
	}
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

// --- Map command ---

func cmdMap(cfg CLIConfig) {
	fs := flag.NewFlagSet("map", flag.ExitOnError)
	root := fs.String("root", "", "Project root (default: cwd)")
	ns := fs.String("ns", "", "Namespace")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	rootDir := *root
	if rootDir == "" {
		rootDir, _ = os.Getwd()
	}
	namespace := resolveNamespace(*ns, cfg)

	cm, err := dualmem.ScanCodebase(rootDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	cm.Namespace = namespace

	// Get git commit for the map
	_, commit := dualmem.GetGitState(rootDir)
	cm.GitCommit = commit

	// Store in DB (needs engine for store access)
	engine, engErr := newEngine(cfg)
	if engErr == nil {
		engine.StoreCodeMap(context.Background(), namespace, cm)
		engine.Close()
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(cm)
		return
	}

	// Pretty print
	fmt.Println("=== Codebase Map ===")
	fmt.Println()
	fmt.Println(cm.Zoom1)
	fmt.Println()
	for _, m := range cm.Zoom2 {
		fmt.Printf("  %s — %s (%d files)\n", m.Path, m.Summary, m.FileCount)
		if len(m.KeyTypes) > 0 {
			fmt.Printf("    Types: %s\n", strings.Join(m.KeyTypes, ", "))
		}
		if len(m.EntryPoints) > 0 {
			fmt.Printf("    Entry: %s\n", strings.Join(m.EntryPoints, ", "))
		}
	}
}

// --- Search-code command ---

func cmdSearchCode(cfg CLIConfig) {
	fs := flag.NewFlagSet("search-code", flag.ExitOnError)
	root := fs.String("root", "", "Project root (default: cwd)")
	limit := fs.Int("limit", 10, "Max results")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	query := strings.Join(fs.Args(), " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "usage: dualmem search-code <query>")
		os.Exit(1)
	}

	rootDir := *root
	if rootDir == "" {
		rootDir, _ = os.Getwd()
	}

	cm, err := dualmem.ScanCodebase(rootDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	idx := dualmem.BuildCodeIndex(cm)
	results := dualmem.SearchCodeMap(cm, idx, query, *limit)

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(results)
		return
	}

	for i, r := range results {
		fmt.Printf("  %d. %-30s score=%.4f (hdc=%.2f kw=%.2f)  %s\n",
			i+1, r.Path, r.HybridScore, r.Similarity, r.KeywordScore, r.Summary)
		if len(r.KeyTypes) > 0 {
			fmt.Printf("     Types: %s\n", strings.Join(r.KeyTypes, ", "))
		}
		if len(r.EntryPoints) > 0 {
			fmt.Printf("     Entry: %s\n", strings.Join(r.EntryPoints, ", "))
		}
		if len(r.Imports) > 0 && len(r.Imports) <= 8 {
			fmt.Printf("     Imports: %s\n", strings.Join(r.Imports, ", "))
		}
	}
}

// --- Diff command ---

func cmdDiff(cfg CLIConfig) {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	namespace := resolveNamespace(*ns, cfg)
	rootDir, _ := os.Getwd()

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	marker, err := engine.GetLatestSessionMarker(namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if marker == nil {
		fmt.Println("No previous session recorded. Run 'dualmem context' first.")
		return
	}

	diff, err := dualmem.ComputeStructuralDiff(rootDir, marker)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if diff == nil {
		fmt.Println("No diff available (not in a git repository).")
		return
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(diff)
		return
	}

	fmt.Println(diff.Summary)
}

// --- Seed command ---

func cmdSeed(cfg CLIConfig) {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	root := fs.String("root", "", "Project root (default: cwd)")
	dryRun := fs.Bool("dry-run", false, "Show clusters without writing memories")
	force := fs.Bool("force", false, "Replace existing seed memories")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	namespace := resolveNamespace(*ns, cfg)
	rootDir := *root
	if rootDir == "" {
		rootDir, _ = os.Getwd()
	}

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ctx := context.Background()
	result, err := engine.SeedMemories(ctx, namespace, *force, *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(result)
		return
	}

	if *dryRun {
		fmt.Printf("Dry run: found %d clusters from codemap\n\n", len(result.Clusters))
		for i, c := range result.Clusters {
			fmt.Printf("Cluster %d: %s (%d modules)\n", i+1, c.Name, len(c.Modules))
			for _, m := range c.Modules {
				fmt.Printf("  - %s: %s\n", m.Path, m.Summary)
			}
			fmt.Println()
		}
		return
	}

	if len(result.Warnings) > 0 {
		for _, w := range result.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		fmt.Fprintln(os.Stderr)
	}
	fmt.Printf("Seeded %d memories from %d clusters\n\n", len(result.Memories), len(result.Clusters))
	for i, dm := range result.Memories {
		fmt.Printf("%d. [%s] %s\n", i+1, dm.Sector, truncate(dm.Text, 120))
		if len(dm.Files) > 0 {
			fmt.Printf("   Files: %s\n", strings.Join(dm.Files, ", "))
		}
	}
}

