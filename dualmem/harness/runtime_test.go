package harness

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goblincore/geoffreyengram/dualmem"
)

type memoryCall struct {
	namespace string
	query     string
	budget    int
}

type fakeRuntimeMemory struct {
	assemble func(context.Context, string, string, int) (*dualmem.ContextBlock, error)
	file     func(context.Context, string, string, int) ([]dualmem.DetailMemory, error)
	calls    []memoryCall
	files    []memoryCall
}

func (m *fakeRuntimeMemory) AssembleContext(ctx context.Context, namespace, query string, budget int) (*dualmem.ContextBlock, error) {
	m.calls = append(m.calls, memoryCall{namespace: namespace, query: query, budget: budget})
	if m.assemble != nil {
		return m.assemble(ctx, namespace, query, budget)
	}
	return &dualmem.ContextBlock{}, nil
}

func (m *fakeRuntimeMemory) FileContext(ctx context.Context, namespace, filename string, limit int) ([]dualmem.DetailMemory, error) {
	m.files = append(m.files, memoryCall{namespace: namespace, query: filename, budget: limit})
	if m.file != nil {
		return m.file(ctx, namespace, filename, limit)
	}
	return nil, nil
}

type fakeActivitySink struct {
	records []Activity
	err     error
}

func (s *fakeActivitySink) Record(_ context.Context, activity Activity) error {
	s.records = append(s.records, activity)
	return s.err
}

func testRuntime(memory Memory, activity ActivitySink) *Runtime {
	opts := DefaultResolveOptions()
	opts.GitCommonDir = func(context.Context, string) (string, error) {
		return "/projects/repo/.git", nil
	}
	return &Runtime{Memory: memory, Activity: activity, ResolveOptions: opts}
}

func testRuntimeEvent(kind EventKind) Event {
	return Event{
		SchemaVersion: "1.0",
		Kind:          kind,
		Harness:       "future-harness",
		CWD:           "/projects/repo",
		SessionID:     "session-1",
	}
}

func TestRuntimeSessionStartLoadsProjectAndInfrastructureContext(t *testing.T) {
	memory := &fakeRuntimeMemory{assemble: func(_ context.Context, namespace, _ string, _ int) (*dualmem.ContextBlock, error) {
		return &dualmem.ContextBlock{Text: namespace + " context"}, nil
	}}
	runtime := testRuntime(memory, nil)

	response := runtime.Handle(context.Background(), testRuntimeEvent(EventSessionStart))

	if response.Action != ActionInjectContext || response.Context != "claude:repo context\n\nclaude:infra context" {
		t.Fatalf("Handle() = %#v", response)
	}
	want := []memoryCall{
		{namespace: "claude:repo", query: "session context", budget: 3000},
		{namespace: "claude:infra", query: "session context", budget: 1500},
	}
	if !reflect.DeepEqual(memory.calls, want) {
		t.Fatalf("AssembleContext calls = %#v, want %#v", memory.calls, want)
	}
}

func TestRuntimeResolveOptionsUseSharedLegacyPrefixDefault(t *testing.T) {
	memory := &fakeRuntimeMemory{}
	runtime := &Runtime{
		Memory: memory,
		ResolveOptions: ResolveOptions{GitCommonDir: func(context.Context, string) (string, error) {
			return "/projects/repo/.git", nil
		}},
	}
	event := testRuntimeEvent(EventPrompt)
	event.Prompt = "Inspect the runtime defaults"

	response := runtime.Handle(context.Background(), event)

	if response.Action != ActionNone || len(response.Diagnostics) != 0 {
		t.Fatalf("Handle() = %#v", response)
	}
	want := []memoryCall{{namespace: "claude:repo", query: event.Prompt, budget: 1500}}
	if !reflect.DeepEqual(memory.calls, want) {
		t.Fatalf("AssembleContext calls = %#v, want %#v", memory.calls, want)
	}
}

func TestRuntimePromptUsesTaskAwareQueryAndDefaultBudget(t *testing.T) {
	memory := &fakeRuntimeMemory{assemble: func(_ context.Context, _, query string, _ int) (*dualmem.ContextBlock, error) {
		return &dualmem.ContextBlock{Text: "memory for " + query}, nil
	}}
	runtime := testRuntime(memory, nil)
	event := testRuntimeEvent(EventPrompt)
	event.Prompt = "Map the authentication flow"

	response := runtime.Handle(context.Background(), event)

	if response.Action != ActionInjectContext || response.Context != "memory for Map the authentication flow" {
		t.Fatalf("Handle() = %#v", response)
	}
	want := []memoryCall{{namespace: "claude:repo", query: event.Prompt, budget: 1500}}
	if !reflect.DeepEqual(memory.calls, want) {
		t.Fatalf("AssembleContext calls = %#v, want %#v", memory.calls, want)
	}
}

func TestRuntimeSuppressesTrivialAndDuplicatePromptsPerSession(t *testing.T) {
	memory := &fakeRuntimeMemory{assemble: func(context.Context, string, string, int) (*dualmem.ContextBlock, error) {
		return &dualmem.ContextBlock{Text: "context"}, nil
	}}
	runtime := testRuntime(memory, nil)

	trivial := testRuntimeEvent(EventPrompt)
	trivial.Prompt = "  Thanks!  "
	if response := runtime.Handle(context.Background(), trivial); response.Action != ActionNone {
		t.Fatalf("trivial prompt response = %#v", response)
	}

	prompt := testRuntimeEvent(EventPrompt)
	prompt.Prompt = "Investigate the cache invalidation path"
	if response := runtime.Handle(context.Background(), prompt); response.Action != ActionInjectContext {
		t.Fatalf("first prompt response = %#v", response)
	}
	if response := runtime.Handle(context.Background(), prompt); response.Action != ActionNone {
		t.Fatalf("duplicate prompt response = %#v", response)
	}
	prompt.SessionID = "session-2"
	if response := runtime.Handle(context.Background(), prompt); response.Action != ActionInjectContext {
		t.Fatalf("new-session prompt response = %#v", response)
	}
	if len(memory.calls) != 2 {
		t.Fatalf("AssembleContext called %d times, want 2", len(memory.calls))
	}
}

func TestRuntimeFileReadFormatsFileMemoriesAndRecordsMetadata(t *testing.T) {
	created := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	memory := &fakeRuntimeMemory{file: func(_ context.Context, _, _ string, _ int) ([]dualmem.DetailMemory, error) {
		return []dualmem.DetailMemory{
			{Type: "warning", Text: "Keep the ordering stable", CreatedAt: created},
			{Type: "decision", Text: "Use the shared resolver", CreatedAt: created.Add(24 * time.Hour)},
		}, nil
	}}
	activity := &fakeActivitySink{}
	runtime := testRuntime(memory, activity)
	event := testRuntimeEvent(EventFileRead)
	event.Files = []string{"/projects/repo/runtime.go"}

	response := runtime.Handle(context.Background(), event)

	wantContext := "[File Memory] runtime.go (2 cached observations)\n" +
		"Prior context for this file:\n\n" +
		"⚠ [warning] Keep the ordering stable (2026-07-31)\n" +
		"★ [decision] Use the shared resolver (2026-08-01)"
	if response.Action != ActionInjectContext || response.Context != wantContext {
		t.Fatalf("Handle() = %#v, want context %q", response, wantContext)
	}
	if !reflect.DeepEqual(memory.files, []memoryCall{{namespace: "claude:repo", query: event.Files[0], budget: 5}}) {
		t.Fatalf("FileContext calls = %#v", memory.files)
	}
	if len(activity.records) != 1 || activity.records[0].Kind != EventFileRead || !reflect.DeepEqual(activity.records[0].Files, event.Files) {
		t.Fatalf("activity records = %#v", activity.records)
	}
	if activity.records[0].Timestamp.IsZero() {
		t.Fatal("activity timestamp is zero")
	}
}

func TestRuntimeRecordsFileWritesAndSessionEndArtifacts(t *testing.T) {
	activity := &fakeActivitySink{}
	runtime := testRuntime(&fakeRuntimeMemory{}, activity)

	write := testRuntimeEvent(EventFileWrite)
	write.Files = []string{"relative.go", "/projects/repo/absolute.go", "relative.go"}
	if response := runtime.Handle(context.Background(), write); response.Action != ActionRecorded {
		t.Fatalf("file-write response = %#v", response)
	}

	end := testRuntimeEvent(EventSessionEnd)
	end.ArtifactRef = "/cache/session-transcript.jsonl"
	if response := runtime.Handle(context.Background(), end); response.Action != ActionRecorded {
		t.Fatalf("session-end response = %#v", response)
	}

	want := []Activity{
		{Harness: "future-harness", Namespace: "claude:repo", SessionID: "session-1", Kind: EventFileWrite, Files: []string{"/projects/repo/relative.go", "/projects/repo/absolute.go"}},
		{Harness: "future-harness", Namespace: "claude:repo", SessionID: "session-1", Kind: EventSessionEnd, Artifact: end.ArtifactRef},
	}
	if len(activity.records) != len(want) {
		t.Fatalf("activity records = %#v", activity.records)
	}
	for i := range want {
		got := activity.records[i]
		got.Timestamp = time.Time{}
		if !reflect.DeepEqual(got, want[i]) {
			t.Fatalf("activity[%d] = %#v, want %#v", i, got, want[i])
		}
	}
}

func TestRuntimeUnknownEventIsNoOp(t *testing.T) {
	opts := DefaultResolveOptions()
	opts.GitCommonDir = func(context.Context, string) (string, error) {
		t.Fatal("ResolveProject attempted for unknown event")
		return "", nil
	}
	runtime := &Runtime{Memory: &fakeRuntimeMemory{}, Activity: &fakeActivitySink{}, ResolveOptions: opts}
	event := testRuntimeEvent(EventKind("future_event"))

	response := runtime.Handle(context.Background(), event)
	if response.Action != ActionNone || response.SchemaVersion != "1.0" || response.Context != "" || len(response.Diagnostics) != 0 {
		t.Fatalf("Handle() = %#v", response)
	}
}

func TestRuntimeBoundsModelVisibleContext(t *testing.T) {
	memory := &fakeRuntimeMemory{assemble: func(context.Context, string, string, int) (*dualmem.ContextBlock, error) {
		return &dualmem.ContextBlock{Text: strings.Repeat("x", 30<<10)}, nil
	}}
	runtime := testRuntime(memory, nil)

	response := runtime.Handle(context.Background(), testRuntimeEvent(EventSessionStart))
	if response.Action != ActionInjectContext {
		t.Fatalf("Handle() = %#v", response)
	}
	if got, want := len(response.Context), 24<<10; got != want {
		t.Fatalf("context length = %d, want %d", got, want)
	}
}

func TestRuntimeOperationalFailuresAreFailOpenAndRedacted(t *testing.T) {
	const secret = "SECRET-provider-details"
	tests := []struct {
		name    string
		runtime *Runtime
		event   Event
		code    string
	}{
		{
			name: "project resolution",
			runtime: func() *Runtime {
				opts := DefaultResolveOptions()
				opts.GitCommonDir = func(context.Context, string) (string, error) { return "", errors.New(secret) }
				return &Runtime{Memory: &fakeRuntimeMemory{}, ResolveOptions: opts}
			}(),
			event: testRuntimeEvent(EventPrompt),
			code:  "project_resolution_failed",
		},
		{
			name: "memory",
			runtime: testRuntime(&fakeRuntimeMemory{assemble: func(context.Context, string, string, int) (*dualmem.ContextBlock, error) {
				return nil, errors.New(secret)
			}}, nil),
			event: testRuntimeEvent(EventSessionStart),
			code:  "memory_unavailable",
		},
		{
			name:    "activity",
			runtime: testRuntime(&fakeRuntimeMemory{}, &fakeActivitySink{err: errors.New(secret)}),
			event: func() Event {
				event := testRuntimeEvent(EventFileWrite)
				event.Files = []string{"/projects/repo/runtime.go"}
				return event
			}(),
			code: "activity_unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.event.Kind == EventPrompt {
				tt.event.Prompt = "substantive prompt"
			}
			response := tt.runtime.Handle(context.Background(), tt.event)
			if response.Action != ActionNone || response.Context != "" || len(response.Diagnostics) != 1 {
				t.Fatalf("Handle() = %#v", response)
			}
			if response.Diagnostics[0].Code != tt.code {
				t.Fatalf("diagnostic = %#v, want code %q", response.Diagnostics[0], tt.code)
			}
			if strings.Contains(response.Diagnostics[0].Message, secret) {
				t.Fatalf("diagnostic leaked error details: %#v", response.Diagnostics[0])
			}
		})
	}
}
