package harness

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/goblincore/geoffreyengram/dualmem"
)

const (
	defaultProjectBudget        = 3000
	defaultInfrastructureNS     = "claude:infra"
	defaultInfrastructureBudget = 1500
	defaultPromptBudget         = 1500
	defaultFileContextLimit     = 5
	defaultMaxContextBytes      = 24 << 10
)

type Memory interface {
	AssembleContext(context.Context, string, string, int) (*dualmem.ContextBlock, error)
	FileContext(context.Context, string, string, int) ([]dualmem.DetailMemory, error)
}

type Runtime struct {
	Memory               Memory
	Activity             ActivitySink
	ResolveOptions       ResolveOptions
	ProjectBudget        int
	InfrastructureNS     string
	InfrastructureBudget int
	PromptBudget         int
	MaxContextBytes      int

	promptMu     sync.Mutex
	promptHashes map[string]map[[sha256.Size]byte]struct{}
}

func (r *Runtime) Handle(ctx context.Context, event Event) Response {
	response := Response{SchemaVersion: "1.0", Action: ActionNone}
	if !knownRuntimeEvent(event.Kind) {
		return response
	}

	if event.Kind == EventPrompt && trivialPrompt(event.Prompt) {
		return response
	}

	resolveOptions := r.ResolveOptions
	if resolveOptions.LegacyPrefix == "" {
		resolveOptions.LegacyPrefix = DefaultResolveOptions().LegacyPrefix
	}
	project, err := ResolveProject(ctx, event, resolveOptions)
	if err != nil {
		return diagnosticResponse("project_resolution_failed", "project identity unavailable")
	}

	switch event.Kind {
	case EventSessionStart:
		return r.handleSessionStart(ctx, project.Namespace)
	case EventPrompt:
		return r.handlePrompt(ctx, project.Namespace, event)
	case EventFileRead:
		return r.handleFileRead(ctx, project.Namespace, event)
	case EventFileWrite:
		return r.recordActivity(ctx, project.Namespace, eventWithFiles(event, NormalizePaths(event.CWD, event.Files)))
	case EventSessionEnd:
		return r.recordActivity(ctx, project.Namespace, event)
	default:
		return response
	}
}

func (r *Runtime) handleSessionStart(ctx context.Context, namespace string) Response {
	if r.Memory == nil {
		return diagnosticResponse("memory_unavailable", "memory context unavailable")
	}

	projectBlock, err := r.Memory.AssembleContext(ctx, namespace, "session context", positiveOr(r.ProjectBudget, defaultProjectBudget))
	if err != nil {
		return diagnosticResponse("memory_unavailable", "memory context unavailable")
	}
	parts := blockText(projectBlock)

	infrastructureNS := strings.TrimSpace(r.InfrastructureNS)
	if infrastructureNS == "" {
		infrastructureNS = defaultInfrastructureNS
	}
	if infrastructureNS != namespace {
		infrastructureBlock, err := r.Memory.AssembleContext(ctx, infrastructureNS, "session context", positiveOr(r.InfrastructureBudget, defaultInfrastructureBudget))
		if err != nil {
			return diagnosticResponse("memory_unavailable", "memory context unavailable")
		}
		parts = append(parts, blockText(infrastructureBlock)...)
	}

	return r.contextResponse(strings.Join(parts, "\n\n"))
}

func (r *Runtime) handlePrompt(ctx context.Context, namespace string, event Event) Response {
	if r.Memory == nil {
		return diagnosticResponse("memory_unavailable", "memory context unavailable")
	}
	prompt := strings.TrimSpace(event.Prompt)
	cacheKey := namespace + "\x00" + event.SessionID
	hash := sha256.Sum256([]byte(prompt))
	if !r.claimPrompt(cacheKey, hash) {
		return Response{SchemaVersion: "1.0", Action: ActionNone}
	}

	block, err := r.Memory.AssembleContext(ctx, namespace, prompt, positiveOr(r.PromptBudget, defaultPromptBudget))
	if err != nil {
		r.releasePrompt(cacheKey, hash)
		return diagnosticResponse("memory_unavailable", "memory context unavailable")
	}
	return r.contextResponse(strings.Join(blockText(block), ""))
}

func (r *Runtime) handleFileRead(ctx context.Context, namespace string, event Event) Response {
	if r.Memory == nil {
		return diagnosticResponse("memory_unavailable", "memory context unavailable")
	}
	paths := NormalizePaths(event.CWD, event.Files)
	sections := make([]string, 0, len(paths))
	for _, path := range paths {
		memories, err := r.Memory.FileContext(ctx, namespace, path, defaultFileContextLimit)
		if err != nil {
			return diagnosticResponse("memory_unavailable", "file memory unavailable")
		}
		if len(memories) > 0 {
			sections = append(sections, formatFileMemories(path, memories))
		}
	}

	recorded := r.recordActivity(ctx, namespace, eventWithFiles(event, paths))
	if len(sections) == 0 {
		return recorded
	}
	response := r.contextResponse(strings.Join(sections, "\n\n"))
	response.Diagnostics = append(response.Diagnostics, recorded.Diagnostics...)
	return response
}

func (r *Runtime) recordActivity(ctx context.Context, namespace string, event Event) Response {
	if r.Activity == nil {
		return diagnosticResponse("activity_unavailable", "activity recording unavailable")
	}
	activity := Activity{
		Timestamp: time.Now().UTC(),
		Harness:   event.Harness,
		Namespace: namespace,
		SessionID: event.SessionID,
		Kind:      event.Kind,
		Files:     append([]string(nil), event.Files...),
	}
	if event.Kind == EventSessionEnd {
		activity.Artifact = event.ArtifactRef
	}
	if err := r.Activity.Record(ctx, activity); err != nil {
		return diagnosticResponse("activity_unavailable", "activity recording unavailable")
	}
	return Response{SchemaVersion: "1.0", Action: ActionRecorded}
}

func (r *Runtime) contextResponse(contextText string) Response {
	contextText = strings.TrimSpace(contextText)
	if contextText == "" {
		return Response{SchemaVersion: "1.0", Action: ActionNone}
	}
	return Response{
		SchemaVersion: "1.0",
		Action:        ActionInjectContext,
		Context:       truncateUTF8(contextText, positiveOr(r.MaxContextBytes, defaultMaxContextBytes)),
	}
}

func (r *Runtime) claimPrompt(session string, hash [sha256.Size]byte) bool {
	r.promptMu.Lock()
	defer r.promptMu.Unlock()
	if r.promptHashes == nil {
		r.promptHashes = make(map[string]map[[sha256.Size]byte]struct{})
	}
	hashes := r.promptHashes[session]
	if hashes == nil {
		hashes = make(map[[sha256.Size]byte]struct{})
		r.promptHashes[session] = hashes
	}
	if _, exists := hashes[hash]; exists {
		return false
	}
	hashes[hash] = struct{}{}
	return true
}

func (r *Runtime) releasePrompt(session string, hash [sha256.Size]byte) {
	r.promptMu.Lock()
	defer r.promptMu.Unlock()
	delete(r.promptHashes[session], hash)
	if len(r.promptHashes[session]) == 0 {
		delete(r.promptHashes, session)
	}
}

func knownRuntimeEvent(kind EventKind) bool {
	switch kind {
	case EventSessionStart, EventPrompt, EventFileRead, EventFileWrite, EventSessionEnd:
		return true
	default:
		return false
	}
}

func trivialPrompt(prompt string) bool {
	normalized := strings.ToLower(strings.TrimSpace(prompt))
	normalized = strings.Trim(normalized, " .,!?:;\t\r\n")
	switch normalized {
	case "", "ok", "okay", "thanks", "thank you", "yes", "no", "sure", "got it", "sounds good":
		return true
	default:
		return false
	}
}

func formatFileMemories(path string, memories []dualmem.DetailMemory) string {
	var out strings.Builder
	out.WriteString("[File Memory] ")
	out.WriteString(filepath.Base(path))
	out.WriteString(" (")
	out.WriteString(strconv.Itoa(len(memories)))
	out.WriteString(" cached observations)\nPrior context for this file:\n")
	for _, memory := range memories {
		memoryType := strings.TrimSpace(memory.Type)
		if memoryType == "" {
			memoryType = "memory"
		}
		out.WriteString("\n")
		out.WriteString(memoryIcon(memoryType))
		out.WriteString(" [")
		out.WriteString(memoryType)
		out.WriteString("] ")
		out.WriteString(strings.TrimSpace(memory.Text))
		if !memory.CreatedAt.IsZero() {
			out.WriteString(" (")
			out.WriteString(memory.CreatedAt.Format("2006-01-02"))
			out.WriteString(")")
		}
	}
	return out.String()
}

func memoryIcon(memoryType string) string {
	switch memoryType {
	case "warning":
		return "⚠"
	case "decision":
		return "★"
	case "continuity":
		return "↻"
	case "knowledge":
		return "📖"
	case "checkpoint":
		return "📋"
	case "map":
		return "🗺"
	case "trace":
		return "🔍"
	default:
		return "●"
	}
}

func blockText(block *dualmem.ContextBlock) []string {
	if block == nil || strings.TrimSpace(block.Text) == "" {
		return nil
	}
	return []string{strings.TrimSpace(block.Text)}
}

func eventWithFiles(event Event, files []string) Event {
	event.Files = files
	return event
}

func diagnosticResponse(code, message string) Response {
	return Response{
		SchemaVersion: "1.0",
		Action:        ActionNone,
		Diagnostics:   []Diagnostic{{Code: code, Message: message}},
	}
}

func positiveOr(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
