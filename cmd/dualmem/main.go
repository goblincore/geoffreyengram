// Command dualmem is a CLI for the DualMem dual-path agent memory system.
//
// Usage:
//
//	dualmem add --ns <namespace> --text "memory content" [--sector semantic] [--salience 0.8]
//	dualmem search --ns <namespace> "query" [--limit 5] [--json]
//	dualmem context --ns <namespace> "query" [--budget 3000] [--no-graph] [--json]
//	dualmem profile --ns <namespace> [--json]
//	dualmem status --ns <namespace>
//	dualmem promote --ns <namespace> --id <memory-id>
//	dualmem demote --ns <namespace> --id <memory-id>
package main

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
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
		SynthesisProvider  string `yaml:"synthesis_provider"`   // "anthropic" or "" (use main summarizer)
		SynthesisModel     string `yaml:"synthesis_model"`      // e.g. "glm-5.1"
		SynthesisAPIKeyEnv string `yaml:"synthesis_api_key_env"` // e.g. "ZAI_API_KEY"
		SynthesisBaseURL   string `yaml:"synthesis_base_url"`   // e.g. "https://api.z.ai/api/anthropic"
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

	// Try loading config file (check XDG + macOS paths)
	configDir, _ := os.UserConfigDir()
	configPath := filepath.Join(configDir, "dualmem", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		// Fallback to ~/.config (XDG default, common on macOS dev setups)
		home, _ := os.UserHomeDir()
		configPath = filepath.Join(home, ".config", "dualmem", "config.yaml")
		data, err = os.ReadFile(configPath)
	}
	if err == nil {
		yaml.Unmarshal(data, &cfg)
	}

	// Also check project-local .dualmem.yaml
	if data, err := os.ReadFile(".dualmem.yaml"); err == nil {
		yaml.Unmarshal(data, &cfg)
	}

	// Expand ~ in SQLite path (YAML doesn't expand shell vars)
	if strings.HasPrefix(cfg.Storage.SQLitePath, "~/") {
		home, _ := os.UserHomeDir()
		cfg.Storage.SQLitePath = filepath.Join(home, cfg.Storage.SQLitePath[2:])
	}

	return cfg
}

// filterFlags returns only flag arguments (--flag value) from args, for flag.Parse.
func filterFlags(args []string) []string {
	var flags []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			// If this flag takes a value (not --flag=value form), include next arg
			if !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	return flags
}

// positionalArgs returns only non-flag arguments from args.
func positionalArgs(args []string) []string {
	var pos []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			// Skip flag and its value
			if !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		} else {
			pos = append(pos, args[i])
		}
	}
	return pos
}

func resolveNamespace(flagNS string, cfg CLIConfig) string {
	if flagNS != "" {
		return flagNS
	}
	if cfg.DefaultNamespace != "" {
		return cfg.DefaultNamespace
	}

	// Auto-detect from git root, falling back to cwd basename.
	// This prevents namespace fragmentation when running from
	// subdirectories, worktrees, or the root directory.
	cwd, _ := os.Getwd()

	// Try git toplevel first (handles subdirectories and worktrees)
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		gitRoot := strings.TrimSpace(string(out))
		if gitRoot != "" && gitRoot != "/" {
			return "claude:" + filepath.Base(gitRoot)
		}
	}

	base := filepath.Base(cwd)
	if base == "/" || base == "." {
		return "claude:default"
	}
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

	// Optional: stronger model for Consult/Synthesize
	var synthesisGen dualmem.TextGenerator
	if cfg.Providers.SynthesisProvider == "anthropic" {
		synthKey := os.Getenv(cfg.Providers.SynthesisAPIKeyEnv)
		if synthKey != "" {
			synthesisGen = dualmem.NewAnthropicSummarizer(
				synthKey,
				cfg.Providers.SynthesisBaseURL,
				cfg.Providers.SynthesisModel,
			)
		}
	}

	cwd, _ := os.Getwd()
	return dualmem.New(dualmem.Config{
		SQLitePath:            cfg.Storage.SQLitePath,
		EmbeddingProvider:     embedder,
		Classifier:            classifier,
		Summarizer:            dualmem.NewGeminiSummarizer(apiKey, ""),
		SynthesisGenerator:    synthesisGen,
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
	case "synthesize":
		cmdSynthesize(cfg)
	case "docs":
		cmdDocs(cfg)
	case "distill":
		cmdDistill(cfg)
	case "cochange":
		cmdCoChange(cfg)
	case "entities":
		cmdEntities(cfg)
	case "file-context":
		cmdFileContext(cfg)
	case "file-index":
		cmdFileIndex(cfg)
	case "rate":
		cmdRate(cfg)
	case "train":
		cmdTrain(cfg)
	case "show":
		cmdShow(cfg)
	case "stats":
		cmdStats(cfg)
	case "health":
		cmdHealth(cfg)
	case "explore":
		cmdExplore(cfg)
	case "consult":
		cmdConsult(cfg)
	case "index":
		cmdIndex(cfg)
	case "unfold":
		cmdUnfold(cfg)
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
  synthesize  Cluster memories into knowledge docs (concept-oriented summaries)
  distill     Extract memories from a session transcript
  cochange    Query the file co-change graph (which files change together)
  entities    Query the entity graph (stats, search, show, top)
  docs        List/show/delete/export knowledge docs
  file-context  Get memories associated with a specific file (warnings, decisions, maps)
  file-index    Generate file index for Read hook fast-path filtering
  rate        Submit context quality ratings (item-level or session-level)
  train       Train the re-ranker model from accumulated ratings
  stats       Show context quality statistics and re-ranker status
  gc          Garbage collect stale/expired memories
  health      Inspect database and report health findings
  explore     Read ranked code files and produce grounded briefing
  consult     Structured intelligence report (lazy-synthesis)
  index       Pre-warm codebase index with progress output
  unfold      Extract a single symbol (function/type) from a file

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
	memType := fs.String("type", "", "Memory type: decision, warning, continuity, trace, architecture, investigation, requirement, test-strategy (default: general)")
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

	if len(filesList) > 0 {
		regenerateFileIndex(cfg, namespace)
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
		QueryText:     query,
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
	intent := fs.String("intent", "", "Task intent override: debug, continue, feature, explore, plan (default: auto-detect)")
	noGraph := fs.Bool("no-graph", false, "Disable entity graph boosting")
	jsonOut := fs.Bool("json", false, "JSON output")
	indexMode := fs.Bool("index", false, "Progressive disclosure: output compact index instead of full context")
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
	engine.OnScanProgress = func(p dualmem.ScanProgress) {
		switch p.Phase {
		case "walking":
			fmt.Fprintf(os.Stderr, "\rIndexing... %d dirs", p.DirsFound)
		case "parsing":
			fmt.Fprintf(os.Stderr, "\rIndexing... %d/%d dirs", p.DirsDone, p.DirsFound)
		}
	}
	defer fmt.Fprintln(os.Stderr)

	var opts *dualmem.ContextOpts
	if *intent != "" || *noGraph {
		opts = &dualmem.ContextOpts{
			Intent:            dualmem.Intent(*intent),
			DisableGraphBoost: *noGraph,
		}
	}

	ctx := context.Background()

	if *indexMode {
		idx, err := engine.AssembleContextIndex(ctx, namespace, query, *budget, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		go regenerateFileIndex(cfg, namespace)

		if *jsonOut {
			json.NewEncoder(os.Stdout).Encode(idx)
			return
		}

		if len(idx.Items) == 0 {
			fmt.Println("[No cross-session memories found. Run /codebase-onboard for initial context.]")
			return
		}

		intentLabel := string(idx.Intent)
		if intentLabel == "" {
			intentLabel = "default"
		}
		fmt.Printf("[Context Index] %d items available (~%d tokens total), intent: %s\n\n", len(idx.Items), idx.TotalTokens, intentLabel)

		// Render compact table
		fmt.Printf("  %-6s | %-14s | %-52s | %s\n", "Type", "ID", "Title", "Tokens")
		fmt.Printf("  %s-|-%s-|-%s-|-%s\n",
			strings.Repeat("-", 6), strings.Repeat("-", 14), strings.Repeat("-", 52), strings.Repeat("-", 6))
		for _, item := range idx.Items {
			icon := item.TypeIcon
			if icon == "" {
				icon = " "
			}
			typeLabel := item.Type
			if len(typeLabel) > 4 {
				typeLabel = typeLabel[:4]
			}
			label := fmt.Sprintf("%s %-4s", icon, typeLabel)
			id := item.ID
			if len(id) > 14 {
				id = id[:14]
			}
			title := item.Title
			if len([]rune(title)) > 52 {
				title = string([]rune(title)[:51]) + "…"
			}
			fmt.Printf("  %-8s| %-14s | %-52s | ~%d\n", label, id, title, item.TokenCount)
		}

		if idx.AlsoAvailable != "" {
			fmt.Printf("\n  Also: %s\n", idx.AlsoAvailable)
		}

		// Show fetch hint with first few IDs and snapshot reference
		if len(idx.Items) > 0 {
			var sampleIDs []string
			for i, item := range idx.Items {
				if i >= 3 {
					break
				}
				sampleIDs = append(sampleIDs, item.ID)
			}
			snapFlag := ""
			if idx.SnapshotID != "" {
				snapFlag = " --snapshot " + idx.SnapshotID
			}
			fmt.Printf("\n  Fetch: ~/go/bin/dualmem show%s %s\n", snapFlag, strings.Join(sampleIDs, " "))
		}
		return
	}

	block, err := engine.AssembleContextWith(ctx, namespace, query, *budget, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	go regenerateFileIndex(cfg, namespace)

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
	snapInfo := ""
	if block.SnapshotID != "" {
		snapInfo = fmt.Sprintf(", snapshot: %s", block.SnapshotID)
	}
	fmt.Printf("\n(%d tokens, %d sources, intent: %s%s)\n", block.TokenCount, len(block.Sources), intentLabel, snapInfo)
}

func cmdShow(cfg CLIConfig) {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	snapshotID := fs.String("snapshot", "", "Snapshot ID from context --index (enables implicit relevance tracking)")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	ids := fs.Args()
	if len(ids) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: dualmem show [--snapshot <snap_id>] <id1> [id2] [id3] ...")
		fmt.Fprintln(os.Stderr, "  IDs: mem_* (detail), chk_* (checkpoint), kdoc_* (knowledge), ep_* (episode)")
		fmt.Fprintln(os.Stderr, "  --snapshot: links fetch to an index snapshot for implicit relevance tracking")
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
	text, err := engine.ShowItems(ctx, namespace, ids, *snapshotID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(map[string]string{"text": text, "token_count": fmt.Sprintf("%d", len(text)/4)})
		return
	}

	fmt.Println(text)
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
	memType := fs.String("type", "", "Type override for promoted memories (warning, decision, continuity, trace, architecture, investigation, requirement, test-strategy)")
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
	stale := fs.Bool("stale", false, "Check for stale memories using symbol/age analysis")
	fs.Parse(os.Args[2:])

	namespace := resolveNamespace(*ns, cfg)
	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	// Staleness check mode
	if *stale {
		rootDir, _ := os.Getwd()
		var store *dualmem.SQLiteStore
		if s, ok := engine.GetStore().(*dualmem.SQLiteStore); ok {
			store = s
		}

		// Build current tags for symbol checking
		scanner := &dualmem.RepoScanner{RootDir: rootDir, Namespace: namespace, Store: store}
		scanResult, scanErr := scanner.ScanAndRank("", nil)

		staleCount := 0
		totalCount := 0

		// Get all detail memories via search with empty query
		ctx := context.Background()
		results, _ := engine.DualSearch(ctx, namespace, "", dualmem.SearchOpts{})
		if results != nil {
			for _, mem := range results.DetailMemories {
				totalCount++
				// Age-based check (30 day threshold)
				ageResult := dualmem.CheckAgeStaleness(mem.CreatedAt, 30, false)
				// Symbol-level check if we have tags
				var symResult dualmem.StalenessResult
				if scanErr == nil && len(mem.Files) > 0 {
					for _, f := range mem.Files {
						symResult = dualmem.CheckSymbolStaleness(f, mem.Text, scanResult.AllTags)
						if symResult.IsStale {
							break
						}
					}
				}

				if ageResult.IsStale || symResult.IsStale {
					staleCount++
					reason := ageResult.Reason
					if symResult.IsStale {
						reason = symResult.Reason
					}
					fmt.Printf("STALE: %s\n", mem.ID[:8])
					fmt.Printf("  reason: %s\n", reason)
					if len(mem.Files) > 0 {
						fmt.Printf("  files: %v\n", mem.Files)
					}
					fmt.Printf("  text: %.80s...\n\n", mem.Text)
				}
			}
		}
		fmt.Fprintf(os.Stderr, "%d/%d memories are stale\n", staleCount, totalCount)
		return
	}

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

	total := report.ExpiredEpisodes + report.ExpiredArcs + report.StaleDetails + report.SupersededMemories + report.AccessColdDetails
	if total == 0 {
		fmt.Println("Nothing to clean up.")
		return
	}

	fmt.Printf("Expired episodes:     %d deleted\n", report.ExpiredEpisodes)
	fmt.Printf("Expired arcs:         %d deleted\n", report.ExpiredArcs)
	fmt.Printf("Git-stale details:    %d demoted\n", report.StaleDetails)
	fmt.Printf("Superseded memories:   %d demoted\n", report.SupersededMemories)
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

// --- Health command ---

func cmdHealth(cfg CLIConfig) {
	fs := flag.NewFlagSet("health", flag.ExitOnError)
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

	hc, err := engine.Health(context.Background(), namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(hc)
		return
	}

	fmt.Println("=== DualMem Health Report ===")
	fmt.Printf("Namespace: %s\n\n", namespace)

	fmt.Println("Summary:")
	fmt.Printf("  Detail memories:  %d\n", hc.Summary.TotalDetailMemories)
	fmt.Printf("  Sketch raw:       %d\n", hc.Summary.TotalSketchRaw)
	fmt.Printf("  Episodes:         %d\n", hc.Summary.TotalEpisodes)
	fmt.Printf("  Arcs:             %d\n", hc.Summary.TotalArcs)
	fmt.Printf("  Knowledge docs:   %d\n", hc.Summary.TotalKnowledgeDocs)
	fmt.Printf("  Entity nodes:     %d\n", hc.Summary.TotalEntityNodes)
	fmt.Printf("  Entity edges:     %d\n", hc.Summary.TotalEntityEdges)
	if !hc.Summary.OldestMemory.IsZero() {
		fmt.Printf("  Oldest memory:    %s\n", hc.Summary.OldestMemory.Format("2006-01-02"))
		fmt.Printf("  Newest memory:    %s\n", hc.Summary.NewestMemory.Format("2006-01-02"))
	}
	fmt.Printf("  Database size:    %s\n", formatDBSize(hc.Summary.DatabaseSize))

	fmt.Println("\nChecks:")
	for _, check := range hc.Checks {
		icon := "✓"
		switch check.Status {
		case "warning":
			icon = "⚠"
		case "error":
			icon = "✗"
		}
		fmt.Printf("  %s %-30s %s\n", icon, check.Name, check.Message)
	}

	switch hc.Status {
	case "healthy":
		fmt.Println("\nStatus: HEALTHY")
	case "warnings":
		warnCount := 0
		for _, c := range hc.Checks {
			if c.Status == "warning" {
				warnCount++
			}
		}
		fmt.Printf("\nStatus: WARNINGS (%d warning(s))\n", warnCount)
	case "issues":
		errCount := 0
		warnCount := 0
		for _, c := range hc.Checks {
			if c.Status == "error" {
				errCount++
			} else if c.Status == "warning" {
				warnCount++
			}
		}
		fmt.Printf("\nStatus: ISSUES (%d error(s), %d warning(s))\n", errCount, warnCount)
	}
}

func formatDBSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// --- Map command ---

func cmdMap(cfg CLIConfig) {
	fs := flag.NewFlagSet("map", flag.ExitOnError)
	root := fs.String("root", "", "Project root (default: cwd)")
	ns := fs.String("ns", "", "Namespace")
	jsonOut := fs.Bool("json", false, "JSON output")
	refresh := fs.Bool("refresh", false, "Force full re-scan, clear tag cache")
	gitGraph := fs.Bool("git-graph", false, "Dump git co-change graph (diagnostic)")
	depth := fs.Int("depth", 6, "Git co-change depth in months (0 = full history)")
	legacy := fs.Bool("legacy", false, "Use legacy HDC-based codemap")
	fs.Parse(os.Args[2:])

	rootDir := *root
	if rootDir == "" {
		rootDir, _ = os.Getwd()
	}
	namespace := resolveNamespace(*ns, cfg)

	// Git co-change diagnostic mode
	if *gitGraph {
		edges, err := dualmem.BuildGitCoChangeGraph(rootDir, *depth, 50)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Git co-change edges: %d\n\n", len(edges))
		for _, edge := range edges {
			fmt.Printf("  %s ↔ %s (count=%d, weight=%.2f)\n", edge.FileA, edge.FileB, edge.CoCount, edge.Weight)
			for _, r := range edge.Reasons {
				fmt.Printf("    reason: %s\n", r)
			}
		}
		return
	}

	// Legacy mode: use old HDC-based codemap
	if *legacy {
		result, err := dualmem.ScanCodebase(rootDir, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		cm := result.CodeMap
		cm.Namespace = namespace
		_, commit := dualmem.GetGitState(rootDir)
		cm.GitCommit = commit
		engine, engErr := newEngine(cfg)
		if engErr == nil {
			engine.StoreCodeMap(context.Background(), namespace, cm, result.Edges)
			engine.Close()
		}
		if *jsonOut {
			json.NewEncoder(os.Stdout).Encode(cm)
			return
		}
		fmt.Println("=== Codebase Map (legacy) ===")
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
		return
	}

	// New graph-based codemap
	var store *dualmem.SQLiteStore
	engine, engErr := newEngine(cfg)
	if engErr == nil {
		defer engine.Close()
		if s, ok := engine.GetStore().(*dualmem.SQLiteStore); ok {
			store = s
		}
	}

	if *refresh && store != nil {
		store.ClearTagCache(namespace)
	}

	scanner := &dualmem.RepoScanner{
		RootDir:   rootDir,
		Namespace: namespace,
		Store:     store,
	}

	result, err := scanner.ScanAndRank("", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result.Files)
		return
	}

	output := dualmem.RenderRankedFiles(result.Files, 2000)
	fmt.Print(output)
	fmt.Fprintf(os.Stderr, "\n%d files, %d tags\n", result.FileCount, result.TagCount)
}

// --- Search-code command ---

func cmdSearchCode(cfg CLIConfig) {
	fs := flag.NewFlagSet("search-code", flag.ExitOnError)
	root := fs.String("root", "", "Project root (default: cwd)")
	limit := fs.Int("limit", 10, "Max results")
	jsonOut := fs.Bool("json", false, "JSON output")
	moduleOnly := fs.Bool("module-only", false, "Module-level results (skip file ranking)")
	graphMode := fs.Bool("graph", false, "Use graph-based PageRank search (experimental)")
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

	ns := "claude:" + filepath.Base(rootDir)

	// Experimental: graph-based search (PageRank + tag extraction)
	if *graphMode {
		var store *dualmem.SQLiteStore
		engine, engErr := newEngine(cfg)
		if engErr == nil {
			defer engine.Close()
			if s, ok := engine.GetStore().(*dualmem.SQLiteStore); ok {
				store = s
			}
		}
		scanner := &dualmem.RepoScanner{
			RootDir:   rootDir,
			Namespace: ns,
			Store:     store,
		}
		result, err := scanner.ScanAndRank(query, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		files := result.Files
		if len(files) > *limit {
			files = files[:*limit]
		}
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(files)
			return
		}
		for i, f := range files {
			fmt.Printf("%2d. %-40s (%.4f)\n", i+1, f.Path, f.Score)
			if f.Desc != "" {
				fmt.Printf("    %s\n", f.Desc)
			}
		}
		return
	}

	// Default: HDC+BM25 hybrid search (module-level, descriptive summaries)
	engine, engErr := dualmem.NewForCodeSearch(dualmem.Config{
		RootDir:   rootDir,
		SQLitePath: cfg.Storage.SQLitePath,
	})
	if engErr != nil {
		// Fallback: no cache, scan directly
		fmt.Fprintf(os.Stderr, "warning: could not open store for cache: %v\n", engErr)
		result, scanErr := dualmem.ScanCodebase(rootDir, nil)
		if scanErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", scanErr)
			os.Exit(1)
		}
		cm := result.CodeMap
		idx := dualmem.BuildCodeIndex(cm)
		printSearchResults(cm, idx, query, *limit, *jsonOut, *moduleOnly)
		return
	}
	defer engine.Close()

	ctx := context.Background()
	cm, idx := engine.GetCodeMap(ctx, ns)
	if cm == nil {
		fmt.Fprintln(os.Stderr, "error: could not generate code map")
		os.Exit(1)
	}

	// Build search opts with co-change data if available
	var opts dualmem.CodeSearchOpts

	// Get top results first (without co-change) to seed co-change lookup
	seedResults := dualmem.SearchCodeMap(cm, idx, query, 3)
	if len(seedResults) > 0 {
		var seedPaths []string
		for _, r := range seedResults {
			seedPaths = append(seedPaths, r.Path)
			// Also include file paths from FileInfo
			for _, fi := range r.Files {
				seedPaths = append(seedPaths, fi.RelPath)
			}
		}
		neighbors := engine.GetCoChangeForPaths(ns, seedPaths, 0.1)
		if len(neighbors) > 0 {
			opts.CoChangeNeighbors = neighbors
		}
	}

	printSearchResults(cm, idx, query, *limit, *jsonOut, *moduleOnly, opts)
}

func printSearchResults(cm *dualmem.CodeMap, idx *dualmem.CodeIndex, query string, limit int, jsonOut bool, moduleOnly bool, opts ...dualmem.CodeSearchOpts) {
	if !moduleOnly {
		fileResults := dualmem.SearchCodeMapFiles(cm, idx, query, limit, opts...)
		docResults := dualmem.SearchCodeMapDocs(cm, idx, query, 3)
		if jsonOut {
			json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"files": fileResults,
				"docs":  docResults,
			})
			return
		}
		for i, r := range fileResults {
			fmt.Printf("  %d. %-35s score=%.4f (module: %s %.2f, file: %.2f)\n",
				i+1, r.FilePath, r.CombinedScore, r.ModulePath, r.ModuleScore, r.FileScore)
		}
		if len(docResults) > 0 {
			fmt.Println("\n  Related docs:")
			for _, r := range docResults {
				fmt.Printf("    - %-35s score=%.4f  %s\n", r.Path, r.HybridScore, r.Summary)
			}
		}
		return
	}

	results := dualmem.SearchCodeMap(cm, idx, query, limit, opts...)
	docResults := dualmem.SearchCodeMapDocs(cm, idx, query, 3)
	if jsonOut {
		json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"modules": results,
			"docs":    docResults,
		})
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
	if len(docResults) > 0 {
		fmt.Println("\n  Related docs:")
		for _, r := range docResults {
			fmt.Printf("    - %-35s score=%.4f  %s\n", r.Path, r.HybridScore, r.Summary)
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

func cmdSynthesize(cfg CLIConfig) {
	fs := flag.NewFlagSet("synthesize", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	all := fs.Bool("all", false, "Synthesize across all namespaces")
	force := fs.Bool("force", false, "Re-synthesize all docs regardless of staleness")
	topic := fs.String("topic", "", "Only synthesize this specific topic")
	dryRun := fs.Bool("dry-run", false, "Show what would be done without writing")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ctx := context.Background()

	if *all {
		opts := &dualmem.SynthesizeOpts{Force: *force, DryRun: *dryRun}
		results, err := engine.SynthesizeAll(ctx, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			json.NewEncoder(os.Stdout).Encode(results)
			return
		}
		for ns, result := range results {
			created := len(result.Created)
			updated := len(result.Updated)
			if created == 0 && updated == 0 && result.Skipped == 0 {
				continue
			}
			fmt.Printf("[%s] created: %d, updated: %d, skipped: %d, orphaned: %d\n",
				ns, created, updated, result.Skipped, result.Orphaned)
			for _, w := range result.Warnings {
				fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
			}
		}
		return
	}

	namespace := resolveNamespace(*ns, cfg)

	result, err := engine.Synthesize(ctx, namespace, &dualmem.SynthesizeOpts{
		Force:  *force,
		Topic:  *topic,
		DryRun: *dryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(result)
		return
	}

	if *dryRun {
		fmt.Printf("Dry run: %d docs would be created, %d orphaned memories\n\n", len(result.Created), result.Orphaned)
		for _, doc := range result.Created {
			fmt.Printf("  Topic: %s (%d source memories)\n", doc.Topic, len(doc.SourceIDs))
			if len(doc.Files) > 0 {
				fmt.Printf("  Files: %s\n", strings.Join(doc.Files, ", "))
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

	fmt.Printf("Created: %d, Updated: %d, Skipped: %d, Orphaned: %d\n",
		len(result.Created), len(result.Updated), result.Skipped, result.Orphaned)

	for _, doc := range result.Created {
		fmt.Printf("  + %s (%d tokens, %d sources)\n", doc.Topic, doc.TokenCount, len(doc.SourceIDs))
	}
	for _, doc := range result.Updated {
		fmt.Printf("  ~ %s (%d tokens, %d sources)\n", doc.Topic, doc.TokenCount, len(doc.SourceIDs))
	}
}

func cmdDocs(cfg CLIConfig) {
	fs := flag.NewFlagSet("docs", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	jsonOut := fs.Bool("json", false, "JSON output")
	// Parse all args so --ns can appear anywhere (before or after subcommand)
	fs.Parse(filterFlags(os.Args[2:]))

	namespace := resolveNamespace(*ns, cfg)
	args := positionalArgs(os.Args[2:])

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ctx := context.Background()

	// Subcommands: show <topic>, delete <topic>, export [--dir path]
	subcmd := ""
	if len(args) > 0 {
		subcmd = args[0]
	}

	switch subcmd {
	case "show":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: dualmem docs show <topic>\n")
			os.Exit(1)
		}
		doc, err := engine.ListKnowledgeDocs(ctx, namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		topic := args[1]
		for _, d := range doc {
			if d.Topic == topic {
				if *jsonOut {
					json.NewEncoder(os.Stdout).Encode(d)
				} else {
					fmt.Printf("[Knowledge: %s]\n\n%s\n", d.Topic, d.Content)
					if len(d.Files) > 0 {
						fmt.Printf("\nFiles: %s\n", strings.Join(d.Files, ", "))
					}
					fmt.Printf("Sources: %d memories | Tokens: %d | Updated: %s\n",
						len(d.SourceIDs), d.TokenCount, d.UpdatedAt.Format("2006-01-02 15:04"))
				}
				return
			}
		}
		fmt.Fprintf(os.Stderr, "no knowledge doc with topic %q\n", topic)
		os.Exit(1)

	case "delete":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "usage: dualmem docs delete <topic>\n")
			os.Exit(1)
		}
		if err := engine.DeleteKnowledgeDoc(ctx, namespace, args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Deleted knowledge doc %q (source memories preserved)\n", args[1])

	case "export":
		dir := ".claude/knowledge"
		if len(args) > 1 {
			dir = args[1]
		}
		docs, err := engine.ListKnowledgeDocs(ctx, namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if len(docs) == 0 {
			fmt.Println("No knowledge docs to export")
			return
		}
		os.MkdirAll(dir, 0755)
		for _, d := range docs {
			path := filepath.Join(dir, d.Topic+".md")
			content := fmt.Sprintf("# %s\n\n%s\n", d.Topic, d.Content)
			if len(d.Files) > 0 {
				content += fmt.Sprintf("\n## Files\n%s\n", strings.Join(d.Files, ", "))
			}
			os.WriteFile(path, []byte(content), 0644)
			fmt.Printf("  %s\n", path)
		}
		fmt.Printf("\nExported %d docs to %s/\n", len(docs), dir)

	default:
		// List all docs
		docs, err := engine.ListKnowledgeDocs(ctx, namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		if *jsonOut {
			json.NewEncoder(os.Stdout).Encode(docs)
			return
		}

		if len(docs) == 0 {
			fmt.Println("No knowledge docs. Run 'dualmem synthesize' to create them.")
			return
		}

		fmt.Printf("Knowledge docs (%d):\n\n", len(docs))
		for _, d := range docs {
			fmt.Printf("  %-30s %4d tokens  %2d sources  updated %s\n",
				d.Topic, d.TokenCount, len(d.SourceIDs), d.UpdatedAt.Format("2006-01-02"))
		}
	}
}

func cmdDistill(cfg CLIConfig) {
	distillFlags := flag.NewFlagSet("distill", flag.ExitOnError)
	autoFlag := distillFlags.Bool("auto", false, "Auto-detect CC session, skip if already distilled")
	fileFlag := distillFlags.String("file", "", "Explicit transcript file path")
	stdinFlag := distillFlags.Bool("stdin", false, "Read transcript from stdin")
	dryRunFlag := distillFlags.Bool("dry-run", false, "Preview without writing")
	jsonFlag := distillFlags.Bool("json", false, "JSON output")
	nsFlag := distillFlags.String("ns", "", "Namespace override")
	distillFlags.Parse(os.Args[2:])

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	ns := resolveNamespace(*nsFlag, cfg)
	opts := dualmem.DistillOpts{
		File:      *fileFlag,
		Stdin:     *stdinFlag,
		Auto:      *autoFlag,
		DryRun:    *dryRunFlag,
		Namespace: *nsFlag,
	}
	if opts.Namespace == "" {
		opts.Namespace = ns
	}

	ctx := context.Background()
	result, err := engine.Distill(ctx, opts, ns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if result.Written > 0 {
		regenerateFileIndex(cfg, ns)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result)
	} else {
		if result.DryRun {
			fmt.Println("[DRY RUN]")
		}
		fmt.Printf("Session: %s\n", result.SessionID)
		fmt.Printf("Summary: %s\n", result.Summary)
		fmt.Printf("Facts extracted: %d, written: %d, skipped (duplicates): %d\n", len(result.Facts), result.Written, result.Skipped)
		if len(result.Triples) > 0 {
			fmt.Printf("Entity triples: %d\n", len(result.Triples))
		}
		if result.DryRun && len(result.Facts) > 0 {
			fmt.Println("\nExtracted facts:")
			for i, f := range result.Facts {
				fmt.Printf("  %d. [%s] %s (salience: %.1f)\n", i+1, f.Type, f.Text, f.Salience)
				if len(f.Files) > 0 {
					fmt.Printf("     Files: %s\n", strings.Join(f.Files, ", "))
				}
			}
		}
	}
}

func cmdEntities(cfg CLIConfig) {
	entitiesFlags := flag.NewFlagSet("entities", flag.ExitOnError)
	nsFlag := entitiesFlags.String("ns", "", "Namespace override")
	limitFlag := entitiesFlags.Int("limit", 20, "Max results")
	jsonFlag := entitiesFlags.Bool("json", false, "JSON output")

	// Parse subcommand before flags
	subArgs := os.Args[2:]
	subCmd := ""
	if len(subArgs) > 0 && subArgs[0] != "-" && !strings.HasPrefix(subArgs[0], "--") {
		subCmd = subArgs[0]
		subArgs = subArgs[1:]
	}
	entitiesFlags.Parse(subArgs)

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	store := engine.GetStore()
	ns := resolveNamespace(*nsFlag, cfg)

	switch subCmd {
	case "", "stats":
		nodes, edges, links, _ := store.GetEntityStats(ns)
		if *jsonFlag {
			json.NewEncoder(os.Stdout).Encode(map[string]int{
				"nodes": nodes, "edges": edges, "links": links,
			})
		} else {
			fmt.Printf("Entity graph (%s):\n  Nodes: %d\n  Edges: %d\n  Memory links: %d\n", ns, nodes, edges, links)
		}

	case "search":
		query := entitiesFlags.Arg(0)
		if query == "" {
			fmt.Fprintln(os.Stderr, "Usage: dualmem entities search <query>")
			os.Exit(1)
		}
		entities, err := store.GetEntitiesByName(ns, query, *limitFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if *jsonFlag {
			json.NewEncoder(os.Stdout).Encode(entities)
		} else {
			fmt.Printf("Entities matching %q:\n", query)
			for _, e := range entities {
				fmt.Printf("  %s [%s] (mentions: %d)\n", e.Name, e.Type, e.MentionCount)
			}
			if len(entities) == 0 {
				fmt.Println("  (none found)")
			}
		}

	case "show":
		name := entitiesFlags.Arg(0)
		if name == "" {
			fmt.Fprintln(os.Stderr, "Usage: dualmem entities show <entity-name>")
			os.Exit(1)
		}
		entities, err := store.GetEntitiesByName(ns, name, 1)
		if err != nil || len(entities) == 0 {
			fmt.Fprintf(os.Stderr, "Entity %q not found\n", name)
			os.Exit(1)
		}
		ent := entities[0]
		edges, _ := store.GetEntityEdges(ent.ID)
		if *jsonFlag {
			json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"entity": ent, "edges": edges,
			})
		} else {
			fmt.Printf("%s [%s] (mentions: %d)\n", ent.Name, ent.Type, ent.MentionCount)
			if len(edges) > 0 {
				fmt.Println("Relationships:")
				for _, edge := range edges {
					if edge.SourceID == ent.ID {
						targets, _ := store.GetEntitiesByName(ns, "", 1000)
						for _, t := range targets {
							if t.ID == edge.TargetID {
								fmt.Printf("  → %s %s (strength: %.2f)\n", edge.Relation, t.Name, edge.Strength)
							}
						}
					} else {
						sources, _ := store.GetEntitiesByName(ns, "", 1000)
						for _, s := range sources {
							if s.ID == edge.SourceID {
								fmt.Printf("  ← %s %s (strength: %.2f)\n", edge.Relation, s.Name, edge.Strength)
							}
						}
					}
				}
			}
		}

	case "top":
		entities, err := store.GetTopEntities(ns, *limitFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if *jsonFlag {
			json.NewEncoder(os.Stdout).Encode(entities)
		} else {
			fmt.Printf("Top entities (%s):\n", ns)
			for i, e := range entities {
				fmt.Printf("  %d. %s [%s] (mentions: %d)\n", i+1, e.Name, e.Type, e.MentionCount)
			}
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown entities subcommand: %s\nUsage: dualmem entities [stats|search|show|top]\n", subCmd)
		os.Exit(1)
	}
}

func cmdCoChange(cfg CLIConfig) {
	fs := flag.NewFlagSet("cochange", flag.ExitOnError)
	nsFlag := fs.String("ns", "", "Namespace override")
	limitFlag := fs.Int("limit", 10, "Max results")
	minStrength := fs.Float64("min-strength", 0.5, "Minimum co-change strength")
	jsonFlag := fs.Bool("json", false, "JSON output")
	decayFlag := fs.Bool("decay", false, "Run co-change decay before querying")

	// Parse subcommand/file path before flags
	subArgs := os.Args[2:]
	filePath := ""
	if len(subArgs) > 0 && subArgs[0] != "-" && !strings.HasPrefix(subArgs[0], "--") {
		filePath = subArgs[0]
		subArgs = subArgs[1:]
	}
	fs.Parse(subArgs)

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	store := engine.GetStore()
	ns := resolveNamespace(*nsFlag, cfg)

	if *decayFlag {
		if err := store.DecayCoChange(ns, 90); err != nil {
			fmt.Fprintf(os.Stderr, "Decay error: %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "Co-change decay applied (90-day half-life)")
		}
	}

	if filePath == "" {
		// No file specified — show stats
		allEdges, err := store.GetCoChangePaths(ns, []string{}, 1000)
		if err != nil {
			// No edges or empty — just show count
			fmt.Printf("Co-change graph (%s): 0 edges\n", ns)
			fmt.Println("\nUsage: dualmem cochange <file> [--min-strength 0.5] [--limit 10]")
			return
		}
		fmt.Printf("Co-change graph (%s): %d edges\n", ns, len(allEdges))
		fmt.Println("\nUsage: dualmem cochange <file> [--min-strength 0.5] [--limit 10]")
		return
	}

	edges, err := store.GetCoChangeNeighbors(ns, filePath, *minStrength, *limitFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if *jsonFlag {
		json.NewEncoder(os.Stdout).Encode(edges)
		return
	}

	if len(edges) == 0 {
		fmt.Printf("No co-change neighbors for %q (min strength: %.1f)\n", filePath, *minStrength)
		return
	}

	fmt.Printf("Files that co-change with %s:\n", filePath)
	for _, e := range edges {
		neighbor := e.TargetPath
		if neighbor == filePath {
			neighbor = e.SourcePath
		}
		conceptStr := ""
		if len(e.Concepts) > 0 {
			conceptStr = fmt.Sprintf(" [%s]", strings.Join(e.Concepts, ", "))
		}
		fmt.Printf("  %-40s strength: %.1f  (co-changes: %d)%s\n",
			neighbor, e.Strength, e.CoCount, conceptStr)
	}
}

// regenerateFileIndex updates the file index in /tmp after memory changes.
// Best-effort: errors are silently ignored since this is an optimization.
func regenerateFileIndex(cfg CLIConfig, namespace string) {
	engine, err := newEngine(cfg)
	if err != nil {
		return
	}
	defer engine.Close()

	ctx := context.Background()
	files, err := engine.FileIndex(ctx, namespace)
	if err != nil {
		return
	}

	nsHash := fmt.Sprintf("%x", md5.Sum([]byte(namespace)))[:8]
	indexPath := filepath.Join(os.TempDir(), fmt.Sprintf("dualmem-file-index-%s.json", nsHash))

	indexData := map[string]interface{}{
		"files":      files,
		"namespace":  namespace,
		"updated_at": time.Now().Format(time.RFC3339),
	}

	data, _ := json.Marshal(indexData)
	os.WriteFile(indexPath, data, 0644)
}

func cmdFileContext(cfg CLIConfig) {
	fs := flag.NewFlagSet("file-context", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	jsonOut := fs.Bool("json", false, "JSON output")
	gate := fs.Bool("gate", false, "File-read gate mode: structured output for PreToolUse hook")
	limit := fs.Int("limit", 10, "Max results")
	fs.Parse(os.Args[2:])

	filename := strings.Join(fs.Args(), " ")
	if filename == "" {
		fmt.Fprintln(os.Stderr, "error: filename required")
		fmt.Fprintln(os.Stderr, "usage: dualmem file-context <filename> [--ns <namespace>] [--json] [--gate]")
		os.Exit(1)
	}

	namespace := resolveNamespace(*ns, cfg)
	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	// Gate mode: check file size first
	if *gate {
		info, err := os.Stat(filename)
		if err != nil || info.Size() < 1500 {
			return // Silent exit — file too small or not found
		}
	}

	queryLimit := *limit
	if !*gate && queryLimit <= 0 {
		queryLimit = 5
	}
	if *gate && queryLimit <= 0 {
		queryLimit = 10
	}

	ctx := context.Background()
	results, err := engine.FileContext(ctx, namespace, filename, queryLimit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		return // Silent exit — no memories for this file
	}

	if *gate {
		// Gate mode: structured format for PreToolUse hook
		info, _ := os.Stat(filename)
		tokEst := int(info.Size() / 4)

		// Cap at 10 annotations
		cap_ := len(results)
		if cap_ > 10 {
			cap_ = 10
		}
		results = results[:cap_]

		relPath := filename
		if abs, err := filepath.Abs(filename); err == nil {
			relPath = filepath.Base(abs)
		}

		fmt.Printf("[File Memory] %s (%d cached observations, file ~%dtok)\n", relPath, len(results), tokEst)
		fmt.Println("Prior context for this file — the full read will follow, but these may already answer your question:")
		fmt.Println()

		typeIcons := map[string]string{
			"warning":    "⚠",
			"decision":   "★",
			"continuity": "↻",
			"knowledge":  "📖",
			"checkpoint":  "📋",
			"map":        "🗺",
			"trace":      "🔍",
			"seed":       "🌱",
		}

		for _, r := range results {
			icon := typeIcons[r.Type]
			if icon == "" {
				icon = "●"
			}
			dateStr := r.CreatedAt.Format("2006-01-02")
			fmt.Printf("%s [%s] %s (%s)\n", icon, r.Type, r.Text, dateStr)
		}
		return
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(results)
		return
	}

	for _, r := range results {
		fmt.Printf("[%s] %s (%.2f, %s)\n", r.Type, r.Text, r.Salience, r.CreatedAt.Format("2006-01-02"))
	}
}

func cmdFileIndex(cfg CLIConfig) {
	fs := flag.NewFlagSet("file-index", flag.ExitOnError)
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
	files, err := engine.FileIndex(ctx, namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	nsHash := fmt.Sprintf("%x", md5.Sum([]byte(namespace)))[:8]
	indexPath := filepath.Join(os.TempDir(), fmt.Sprintf("dualmem-file-index-%s.json", nsHash))

	indexData := map[string]interface{}{
		"files":      files,
		"namespace":  namespace,
		"updated_at": time.Now().Format(time.RFC3339),
	}

	data, _ := json.Marshal(indexData)
	os.WriteFile(indexPath, data, 0644)

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(indexData)
	} else {
		fmt.Printf("File index: %s (%d files)\n", indexPath, len(files))
	}
}

// --- Rate command ---

func cmdRate(cfg CLIConfig) {
	fs := flag.NewFlagSet("rate", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	snapshot := fs.String("snapshot", "", "Snapshot ID to rate items for")
	phase := fs.String("phase", "late", "Rating phase: early or late")
	ratingsJSON := fs.String("ratings", "", `Item ratings as JSON: {"mem_id": 0-2, ...}`)
	// Session-level rating
	session := fs.Bool("session", false, "Submit a session-level rating instead")
	score := fs.Int("score", 0, "Session score (1-5)")
	explanation := fs.String("explanation", "", "Session rating explanation")
	jsonOut := fs.Bool("json", false, "JSON output")
	fs.Parse(os.Args[2:])

	namespace := resolveNamespace(*ns, cfg)
	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	if *session {
		if *score < 1 || *score > 5 {
			fmt.Fprintln(os.Stderr, "session score must be 1-5")
			os.Exit(1)
		}
		sr := &dualmem.SessionRating{
			ID:          fmt.Sprintf("sr_%d", time.Now().UnixNano()),
			Namespace:   namespace,
			SessionID:   fmt.Sprintf("sess_%d", time.Now().UnixNano()),
			Score:       *score,
			Explanation: *explanation,
		}
		if err := engine.SubmitSessionRating(sr); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "ok", "session_rating_id": sr.ID})
		} else {
			fmt.Printf("Session rating saved (score=%d)\n", *score)
		}
		return
	}

	// Item-level rating
	if *snapshot == "" || *ratingsJSON == "" {
		fmt.Fprintln(os.Stderr, "usage: dualmem rate --snapshot <id> --ratings '{\"mem_id\": 2, ...}'")
		fmt.Fprintln(os.Stderr, "       dualmem rate --session --score 4 --explanation 'helpful'")
		os.Exit(1)
	}

	var ratings map[string]int
	if err := json.Unmarshal([]byte(*ratingsJSON), &ratings); err != nil {
		fmt.Fprintf(os.Stderr, "invalid ratings JSON: %v\n", err)
		os.Exit(1)
	}

	if err := engine.SubmitRatings(namespace, *snapshot, *phase, ratings); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "ok", "items_rated": len(ratings), "phase": *phase})
	} else {
		fmt.Printf("Rated %d items (phase=%s, snapshot=%s)\n", len(ratings), *phase, *snapshot)
	}
}

// --- Train command ---

func cmdTrain(cfg CLIConfig) {
	fs := flag.NewFlagSet("train", flag.ExitOnError)
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

	rows, err := engine.GetTrainingRows(namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading training data: %v\n", err)
		os.Exit(1)
	}

	weights, err := dualmem.TrainReranker(rows)
	if err != nil {
		fmt.Fprintf(os.Stderr, "training failed: %v\n", err)
		os.Exit(1)
	}

	// Store weights in config
	weightsJSON, _ := json.Marshal(weights)
	if err := engine.SetConfigValue("reranker_weights", string(weightsJSON)); err != nil {
		fmt.Fprintf(os.Stderr, "error saving weights: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(weights)
	} else {
		fmt.Printf("Re-ranker trained on %d samples\n", weights.SampleCount)
		fmt.Printf("Coefficients:\n")
		featureNames := []string{"cosine_sim", "importance", "salience", "type_is_warning",
			"type_is_decision", "type_is_continuity", "file_overlap", "age_days", "text_length", "sector_match"}
		for i, name := range featureNames {
			if weights.Coefficients[i] != 0 {
				fmt.Printf("  %-20s %+.3f\n", name, weights.Coefficients[i])
			}
		}
		fmt.Printf("  %-20s %+.3f\n", "bias", weights.Bias)
	}
}

// --- Consult command ---

func cmdIndex(cfg CLIConfig) {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	force := fs.Bool("force", false, "Re-index even if cache is fresh")
	fs.Parse(filterFlags(os.Args[2:]))

	namespace := resolveNamespace(*ns, cfg)
	rootDir, _ := os.Getwd()

	// Check cache unless --force
	if !*force {
		engine, err := newEngine(cfg)
		if err == nil {
			_, currentCommit := dualmem.GetGitState(rootDir)
			stored, _ := engine.GetStore().GetCodeMap(namespace)
			engine.Close()
			if stored != nil && stored.GitCommit == currentCommit && currentCommit != "" {
				commitStr := currentCommit
				if len(commitStr) > 7 {
					commitStr = commitStr[:7]
				}
				fmt.Fprintf(os.Stderr, "Index is current (commit %s). Use --force to re-index.\n", commitStr)
				return
			}
		}
	}

	start := time.Now()
	var lastPhase string

	result, err := dualmem.ScanCodebase(rootDir, func(p dualmem.ScanProgress) {
		if p.Phase != lastPhase {
			if lastPhase != "" {
				fmt.Fprintln(os.Stderr)
			}
			lastPhase = p.Phase
		}
		switch p.Phase {
		case "walking":
			fmt.Fprintf(os.Stderr, "\rWalking... %d dirs", p.DirsFound)
		case "parsing":
			fmt.Fprintf(os.Stderr, "\rParsing... %d/%d dirs (%d files)", p.DirsDone, p.DirsFound, p.FileCount)
		}
	})
	fmt.Fprintln(os.Stderr) // finish the progress line
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Count files by language
	var goCount, tsCount, pyCount, rsCount int
	for _, mod := range result.CodeMap.Zoom2 {
		switch mod.Language {
		case "go", "Go":
			goCount += mod.FileCount
		case "typescript", "TypeScript":
			tsCount += mod.FileCount
		case "python", "Python":
			pyCount += mod.FileCount
		case "rust", "Rust":
			rsCount += mod.FileCount
		}
	}

	_, currentCommit := dualmem.GetGitState(rootDir)
	result.CodeMap.Namespace = namespace
	result.CodeMap.GitCommit = currentCommit

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating engine: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	engine.GetStore().UpsertCodeMap(namespace, rootDir, result.CodeMap.Zoom1, result.CodeMap.MarshalZoom2(), currentCommit)
	if len(result.Edges) > 0 {
		for i := range result.Edges {
			result.Edges[i].Namespace = namespace
		}
		engine.GetStore().InsertStructuralEdges(namespace, result.Edges)
	}

	commitStr := currentCommit
	if len(commitStr) > 7 {
		commitStr = commitStr[:7]
	}

	fmt.Fprintf(os.Stderr, "  Go: %d, TypeScript: %d, Python: %d, Rust: %d\n", goCount, tsCount, pyCount, rsCount)
	fmt.Fprintf(os.Stderr, "  Structural edges: %d\n", len(result.Edges))
	fmt.Fprintf(os.Stderr, "  Modules: %d\n", len(result.CodeMap.Zoom2))
	fmt.Fprintf(os.Stderr, "Cached at commit %s (took %s)\n", commitStr, time.Since(start).Round(time.Millisecond))
}

func cmdUnfold(cfg CLIConfig) {
	fs := flag.NewFlagSet("unfold", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace (unused, for compatibility)")
	jsonOut := fs.Bool("json", false, "JSON output")
	maxLines := fs.Int("max-lines", 200, "Maximum lines to extract")
	fs.Parse(os.Args[2:])

	args := fs.Args()
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: dualmem unfold <file-path> <symbol-name> [--max-lines N] [--json]")
		fmt.Fprintln(os.Stderr, "  Extracts a single function/type/struct from a file with line numbers.")
		fmt.Fprintln(os.Stderr, "  Supports Go, TypeScript/JavaScript, Python, Rust.")
		os.Exit(1)
	}

	filePath := args[0]
	symbolName := args[1]

	// Resolve to absolute path if relative
	if !filepath.IsAbs(filePath) {
		absPath, err := filepath.Abs(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error resolving path: %v\n", err)
			os.Exit(1)
		}
		filePath = absPath
	}

	// Suppress unused variable warning
	_ = *ns

	result, err := dualmem.ExtractSymbol(filePath, symbolName, *maxLines)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(result)
		return
	}

	// Print with line numbers
	lines := strings.Split(result.Content, "\n")
	for i, line := range lines {
		fmt.Printf("%6d\t%s\n", result.StartLine+i, line)
	}
	fmt.Fprintf(os.Stderr, "\n--- %s:%d-%d (%d lines, ~%d tokens) ---\n",
		result.FilePath, result.StartLine, result.EndLine,
		result.EndLine-result.StartLine+1, result.TokenCount)
}

func cmdConsult(cfg CLIConfig) {
	fs := flag.NewFlagSet("consult", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	budget := fs.Int("budget", 2000, "Token budget")
	compare := fs.Bool("compare", false, "Run both Flash Lite and synthesis model, show side-by-side")
	fresh := fs.Bool("fresh", false, "Force re-synthesis with code evidence (bypass cache)")
	// Use filterFlags to handle flags mixed with positional args
	// (Go's flag.Parse stops at first non-flag argument)
	fs.Parse(filterFlags(os.Args[2:]))

	// Extract query from non-flag positional args
	var queryParts []string
	for _, arg := range os.Args[2:] {
		if !strings.HasPrefix(arg, "-") {
			// Skip flag values (already consumed by filterFlags)
			queryParts = append(queryParts, arg)
		}
	}
	query := strings.Join(queryParts, " ")
	if query == "" {
		fmt.Fprintf(os.Stderr, "usage: dualmem consult \"query\" [--budget N] [--ns namespace] [--compare] [--fresh]\n")
		os.Exit(1)
	}

	namespace := resolveNamespace(*ns, cfg)

	if *compare {
		cmdConsultCompare(cfg, namespace, query, *budget)
		return
	}

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()
	engine.OnScanProgress = func(p dualmem.ScanProgress) {
		switch p.Phase {
		case "walking":
			fmt.Fprintf(os.Stderr, "\rIndexing... %d dirs", p.DirsFound)
		case "parsing":
			fmt.Fprintf(os.Stderr, "\rIndexing... %d/%d dirs", p.DirsDone, p.DirsFound)
		}
	}
	defer fmt.Fprintln(os.Stderr)

	ctx := context.Background()

	// --fresh: delete cached knowledge docs to force re-synthesis with code evidence
	if *fresh {
		queryEmb, embErr := engine.Embedder().Embed(ctx, query, "RETRIEVAL_QUERY")
		if embErr == nil {
			kdocs, _ := engine.GetKnowledgeDocs(ctx, namespace, query, queryEmb, 3)
			for _, kd := range kdocs {
				engine.DeleteKnowledgeDoc(ctx, namespace, kd.Topic)
			}
		}
	}

	report, err := engine.Consult(ctx, namespace, query, *budget)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(report.FormatText())
}

func cmdExplore(cfg CLIConfig) {
	fs := flag.NewFlagSet("explore", flag.ExitOnError)
	ns := fs.String("ns", "", "Namespace")
	budget := fs.Int("budget", 4000, "Token budget for snippets")
	fs.Parse(filterFlags(os.Args[2:]))

	// Extract query from non-flag positional args
	var queryParts []string
	for _, arg := range os.Args[2:] {
		if !strings.HasPrefix(arg, "-") {
			queryParts = append(queryParts, arg)
		}
	}
	query := strings.Join(queryParts, " ")
	if query == "" {
		fmt.Fprintf(os.Stderr, "usage: dualmem explore \"query\" [--budget N] [--ns namespace]\n")
		os.Exit(1)
	}

	namespace := resolveNamespace(*ns, cfg)

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()
	engine.OnScanProgress = func(p dualmem.ScanProgress) {
		switch p.Phase {
		case "walking":
			fmt.Fprintf(os.Stderr, "\rIndexing... %d dirs", p.DirsFound)
		case "parsing":
			fmt.Fprintf(os.Stderr, "\rIndexing... %d/%d dirs", p.DirsDone, p.DirsFound)
		}
	}
	defer fmt.Fprintln(os.Stderr)

	ctx := context.Background()
	result, err := engine.Explore(ctx, namespace, query, *budget)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(result.FormatSnippetsFirst())
}

// cmdConsultCompare runs the same synthesis prompt through both Flash Lite and
// the configured synthesis model, printing results side-by-side.
// Uses ConsultCompare which gathers evidence once and calls both generators.
func cmdConsultCompare(cfg CLIConfig, namespace, query string, budget int) {
	synthKey := os.Getenv(cfg.Providers.SynthesisAPIKeyEnv)
	if synthKey == "" {
		fmt.Fprintf(os.Stderr, "error: --compare requires synthesis provider config (set providers.synthesis_* in config.yaml)\n")
		os.Exit(1)
	}

	engine, err := newEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	apiKey := os.Getenv(cfg.Providers.EmbeddingAPIKeyEnv)
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}

	flashGen := dualmem.NewGeminiSummarizer(apiKey, "")
	synthModel := cfg.Providers.SynthesisModel
	if synthModel == "" {
		synthModel = "glm-5.1"
	}
	synthGen := dualmem.NewAnthropicSummarizer(synthKey, cfg.Providers.SynthesisBaseURL, synthModel)

	ctx := context.Background()

	// Gather evidence once via normal Consult (may cache hit — that's fine)
	fmt.Fprintf(os.Stderr, "Gathering structural evidence...\n")
	report, err := engine.Consult(ctx, namespace, query, budget)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Build the synthesis prompt (same one Consult uses internally)
	prompt := fmt.Sprintf(
		"Given the following structural evidence about %q in this codebase, "+
			"write a concise explanation (200-400 words) of how this subsystem works.\n\n"+
			"Include: key files and their roles, data/control flow between components, "+
			"important design decisions or constraints.\n\n"+
			"Do NOT include code snippets. Focus on conceptual understanding that would "+
			"help a developer work on this subsystem correctly.", query)
	if report.Evidence != "" {
		prompt += "\n\n[Structural Evidence]\n" + report.Evidence
	}
	var fileList []string
	for _, f := range report.RelevantFiles {
		entry := f.Path
		if f.Desc != "" {
			entry += " — " + f.Desc
		}
		fileList = append(fileList, entry)
	}
	if len(fileList) > 0 {
		prompt += "\n\n[Relevant Files]\n" + strings.Join(fileList, "\n")
	}

	// Run both generators against the same prompt
	fmt.Fprintf(os.Stderr, "Running Flash Lite...\n")
	start := time.Now()
	flashResult, flashErr := flashGen.GenerateText(ctx, prompt, 500)
	flashDur := time.Since(start)

	fmt.Fprintf(os.Stderr, "Running %s...\n", synthModel)
	start = time.Now()
	synthResult, synthErr := synthGen.GenerateText(ctx, prompt, 500)
	synthDur := time.Since(start)

	// Print comparison
	sep := strings.Repeat("═", 60)
	fmt.Printf("\n%s\n", sep)
	fmt.Printf("QUERY: %s\n", query)
	fmt.Printf("%s\n\n", sep)

	fmt.Printf("── Flash Lite (%.1fs) ──\n\n", flashDur.Seconds())
	if flashErr != nil {
		fmt.Printf("ERROR: %v\n", flashErr)
	} else {
		fmt.Println(flashResult)
	}

	fmt.Printf("\n── %s (%.1fs) ──\n\n", synthModel, synthDur.Seconds())
	if synthErr != nil {
		fmt.Printf("ERROR: %v\n", synthErr)
	} else {
		fmt.Println(synthResult)
	}

	fmt.Printf("\n%s\n", sep)
	flashTokens := len(strings.Fields(flashResult)) * 4 / 3
	synthTokens := len(strings.Fields(synthResult)) * 4 / 3
	fmt.Printf("Flash Lite: ~%d tokens, %.1fs | %s: ~%d tokens, %.1fs\n",
		flashTokens, flashDur.Seconds(), synthModel, synthTokens, synthDur.Seconds())
}

// --- Stats command ---

func cmdStats(cfg CLIConfig) {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
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

	stats, err := engine.GetRatingStats(namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(stats)
		return
	}

	fmt.Printf("Context Quality Stats (%s)\n", namespace)
	fmt.Println(strings.Repeat("─", 45))
	fmt.Printf("  Total ratings:     %d items across %d sessions\n", stats.TotalRatings, stats.TotalSessions)
	fmt.Printf("  Avg precision:     %.2f (items rated 1-2 / total)\n", stats.AvgPrecision)
	if stats.AvgSessionScore > 0 {
		fmt.Printf("  Avg session score: %.1f / 5.0\n", stats.AvgSessionScore)
	}

	if len(stats.ByMemType) > 0 {
		fmt.Println()
		fmt.Println("  By memory type:")
		for memType, ts := range stats.ByMemType {
			fmt.Printf("    %-14s avg=%.2f  (%.0f%% relevant, n=%d)\n", memType, ts.AvgRating, ts.RelevantPct, ts.Count)
		}
	}

	// Re-ranker status
	weightsJSON, _ := engine.GetConfigValue("reranker_weights")
	if weightsJSON != "" {
		var weights dualmem.RerankerWeights
		if json.Unmarshal([]byte(weightsJSON), &weights) == nil {
			fmt.Printf("\n  Re-ranker: trained (%d samples, %s)\n", weights.SampleCount, weights.TrainedAt.Format("2006-01-02"))
			if weights.IsStale(60) {
				fmt.Println("  ⚠ Weights are stale (>60 days) — run `dualmem train` to refresh")
			}
		}
	} else {
		fmt.Printf("\n  Re-ranker: not trained (need 50+ ratings)\n")
	}

	if len(stats.RecentPrecision) > 0 {
		fmt.Print("\n  Trend (recent sessions): ")
		for i, p := range stats.RecentPrecision {
			if i > 0 {
				fmt.Print(" → ")
			}
			fmt.Printf("%.2f", p)
		}
		fmt.Println()
	}
}

