package harness

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type testAdapter struct {
	name string
}

func (a testAdapter) Name() string                         { return a.name }
func (testAdapter) Decode([]byte) ([]Event, error)         { return nil, nil }
func (testAdapter) Encode(Event, Response) ([]byte, error) { return nil, nil }

func TestBuiltinAdapters(t *testing.T) {
	registry := BuiltinAdapters()
	for _, name := range []string{"claude", "codex"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("missing adapter %q", name)
		}
	}
	if _, ok := registry.Get("pi"); ok {
		t.Fatal("pi unexpectedly has a native Go adapter")
	}
}

func TestAdapterRegistryRejectsBlankAndDuplicateNames(t *testing.T) {
	if _, err := NewRegistry(testAdapter{name: "   "}); err == nil {
		t.Fatal("NewRegistry accepted a blank adapter name")
	}
	if _, err := NewRegistry(testAdapter{name: "claude"}, testAdapter{name: " claude "}); err == nil {
		t.Fatal("NewRegistry accepted duplicate adapter names")
	}
}

func TestAdapterFixtures(t *testing.T) {
	registry := BuiltinAdapters()
	tests := []struct {
		name    string
		adapter string
		fixture string
		want    Event
	}{
		{
			name:    "Claude session start",
			adapter: "claude",
			fixture: "claude-session-start.json",
			want: Event{
				SchemaVersion: "1.0",
				Kind:          EventSessionStart,
				Harness:       "claude",
				CWD:           "/repo",
				SessionID:     "claude-session-001",
			},
		},
		{
			name:    "Claude user prompt",
			adapter: "claude",
			fixture: "claude-user-prompt.json",
			want: Event{
				SchemaVersion: "1.0",
				Kind:          EventPrompt,
				Harness:       "claude",
				CWD:           "/repo",
				SessionID:     "claude-session-001",
				Prompt:        "Map the authentication flow.",
			},
		},
		{
			name:    "Claude structured read",
			adapter: "claude",
			fixture: "claude-read.json",
			want: Event{
				SchemaVersion: "1.0",
				Kind:          EventFileRead,
				Harness:       "claude",
				CWD:           "/repo/pkg",
				SessionID:     "claude-session-001",
				Files:         []string{"/repo/internal/read.go"},
				Tool:          ToolRef{Name: "Read", Phase: "pre"},
			},
		},
		{
			name:    "Codex session start",
			adapter: "codex",
			fixture: "codex-session-start.json",
			want: Event{
				SchemaVersion: "1.0",
				Kind:          EventSessionStart,
				Harness:       "codex",
				CWD:           "/repo",
				SessionID:     "codex-session-001",
			},
		},
		{
			name:    "Codex user prompt",
			adapter: "codex",
			fixture: "codex-user-prompt.json",
			want: Event{
				SchemaVersion: "1.0",
				Kind:          EventPrompt,
				Harness:       "codex",
				CWD:           "/repo",
				SessionID:     "codex-session-001",
				Prompt:        "Add native payload adapters.",
			},
		},
		{
			name:    "Codex apply patch",
			adapter: "codex",
			fixture: "codex-apply-patch.json",
			want: Event{
				SchemaVersion: "1.0",
				Kind:          EventFileWrite,
				Harness:       "codex",
				CWD:           "/repo",
				SessionID:     "codex-session-001",
				Files: []string{
					"/repo/internal/adapter.go",
					"/repo/internal/new.go",
					"/repo/internal/old.go",
				},
				Tool: ToolRef{Name: "apply_patch", Phase: "post"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, ok := registry.Get(tt.adapter)
			if !ok {
				t.Fatalf("missing adapter %q", tt.adapter)
			}
			events, err := adapter.Decode(readAdapterFixture(t, tt.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 {
				t.Fatalf("Decode() returned %d events, want 1: %#v", len(events), events)
			}
			if !reflect.DeepEqual(events[0], tt.want) {
				t.Fatalf("Decode() = %#v, want %#v", events[0], tt.want)
			}
		})
	}
}

func TestAdapterUnknownToolsAreSuccessfulNoOps(t *testing.T) {
	registry := BuiltinAdapters()
	tests := []struct {
		name    string
		adapter string
		raw     string
	}{
		{
			name:    "Claude unknown tool",
			adapter: "claude",
			raw:     `{"session_id":"s1","cwd":"/repo","hook_event_name":"PreToolUse","tool_name":"FutureTool","tool_input":42}`,
		},
		{
			name:    "Codex unknown tool",
			adapter: "codex",
			raw:     `{"session_id":"s1","cwd":"/repo","hook_event_name":"PostToolUse","tool_name":"future_tool","tool_input":42}`,
		},
		{
			name:    "Codex Bash remains opaque",
			adapter: "codex",
			raw:     `{"session_id":"s1","cwd":"/repo","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"apply_patch *** Update File: should-not-appear.go; $(command); ` + "`command`" + `"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, _ := registry.Get(tt.adapter)
			events, err := adapter.Decode([]byte(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 0 {
				t.Fatalf("Decode() = %#v, want no events", events)
			}
		})
	}
}

func TestAdapterClaudeEditAndWriteBecomeFileWrites(t *testing.T) {
	adapter, _ := BuiltinAdapters().Get("claude")
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "Edit",
			raw:  `{"session_id":"s1","cwd":"/repo/pkg","hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":"../internal/write.go"}}`,
		},
		{
			name: "Write",
			raw:  `{"session_id":"s1","cwd":"/repo/pkg","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"../internal/write.go"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := adapter.Decode([]byte(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 {
				t.Fatalf("Decode() = %#v, want one event", events)
			}
			got := events[0]
			if got.Kind != EventFileWrite || !reflect.DeepEqual(got.Files, []string{"/repo/internal/write.go"}) {
				t.Fatalf("Decode() = %#v", got)
			}
			if got.Tool != (ToolRef{Name: tt.name, Phase: "post"}) {
				t.Fatalf("tool = %#v", got.Tool)
			}
		})
	}
}

func TestAdapterRejectsMalformedJSON(t *testing.T) {
	registry := BuiltinAdapters()
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			adapter, _ := registry.Get(name)
			if _, err := adapter.Decode([]byte(`{"cwd":`)); err == nil {
				t.Fatal("Decode() accepted malformed JSON")
			}
		})
	}
}

func TestAdapterEncodesMatchingNativeHookEvent(t *testing.T) {
	registry := BuiltinAdapters()
	events := []struct {
		name  string
		event Event
		want  string
	}{
		{name: "session start", event: Event{Kind: EventSessionStart}, want: "SessionStart"},
		{name: "prompt", event: Event{Kind: EventPrompt}, want: "UserPromptSubmit"},
		{name: "pre tool", event: Event{Kind: EventFileRead, Tool: ToolRef{Phase: "pre"}}, want: "PreToolUse"},
		{name: "post tool", event: Event{Kind: EventFileWrite, Tool: ToolRef{Phase: "post"}}, want: "PostToolUse"},
	}

	for _, adapterName := range []string{"claude", "codex"} {
		adapter, _ := registry.Get(adapterName)
		for _, tt := range events {
			t.Run(adapterName+" "+tt.name, func(t *testing.T) {
				raw, err := adapter.Encode(tt.event, Response{
					SchemaVersion: "1.0",
					Action:        ActionInjectContext,
					Context:       "bounded context",
				})
				if err != nil {
					t.Fatal(err)
				}
				var got struct {
					HookSpecificOutput struct {
						HookEventName     string `json:"hookEventName"`
						AdditionalContext string `json:"additionalContext"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatalf("invalid native response JSON: %v", err)
				}
				if got.HookSpecificOutput.HookEventName != tt.want {
					t.Fatalf("hookEventName = %q, want %q", got.HookSpecificOutput.HookEventName, tt.want)
				}
				if got.HookSpecificOutput.AdditionalContext != "bounded context" {
					t.Fatalf("additionalContext = %q", got.HookSpecificOutput.AdditionalContext)
				}
			})
		}
	}
}

func TestAdapterEncodesNoContextForNonInjectionResponse(t *testing.T) {
	registry := BuiltinAdapters()
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			adapter, _ := registry.Get(name)
			raw, err := adapter.Encode(Event{}, Response{Action: ActionRecorded, Context: "must not leak"})
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("Encode() = %s, want empty object", raw)
			}
		})
	}
}

func TestExtractPatchPathsOnlyReadsEnvelopeHeaders(t *testing.T) {
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: ./real.go",
		"+$(command)",
		"+`command`",
		"+*** Delete File: body-only.go",
		" *** Add File: leading-space.go",
		"*** Move File: ignored.go",
		"*** Add File: ./new.go",
		"*** Delete File: old.go",
		"*** End Patch",
	}, "\n")
	want := []string{"./real.go", "./new.go", "old.go"}
	if got := ExtractPatchPaths(patch); !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractPatchPaths() = %#v, want %#v", got, want)
	}
}

func TestPiFixturesUseNormalizedEventProtocolDirectly(t *testing.T) {
	tests := []struct {
		fixture string
		kind    EventKind
		files   []string
	}{
		{fixture: "pi-session-start.json", kind: EventSessionStart},
		{fixture: "pi-read.json", kind: EventFileRead, files: []string{"/repo/internal/read.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			event, err := DecodeEvent(bytes.NewReader(readAdapterFixture(t, tt.fixture)), 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			if event.Harness != "pi" || event.Kind != tt.kind || !reflect.DeepEqual(event.Files, tt.files) {
				t.Fatalf("DecodeEvent() = %#v", event)
			}
		})
	}
}

func readAdapterFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
