package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goblincore/geoffreyengram/dualmem"
	"github.com/goblincore/geoffreyengram/dualmem/harness"
)

const lifecycleSecret = "lifecycle-secret-sentinel"

type lifecycleMemory struct {
	contextText string
	err         error
}

func (m lifecycleMemory) AssembleContext(context.Context, string, string, int) (*dualmem.ContextBlock, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &dualmem.ContextBlock{Text: m.contextText}, nil
}

func (m lifecycleMemory) FileContext(context.Context, string, string, int) ([]dualmem.DetailMemory, error) {
	return nil, nil
}

func lifecycleRuntime(memory harness.Memory) *harness.Runtime {
	opts := harness.DefaultResolveOptions()
	opts.GitCommonDir = func(context.Context, string) (string, error) {
		return "/repo/.git", nil
	}
	return &harness.Runtime{Memory: memory, ResolveOptions: opts}
}

func decodeLifecycleResponse(t *testing.T, raw string) harness.Response {
	t.Helper()
	var response harness.Response
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatalf("response is not JSON: %v; raw=%q", err, raw)
	}
	return response
}

func TestRunEventEmitsNeutralResponseForNormalizedFutureHarnessEvent(t *testing.T) {
	input := `{"schema_version":"1.0","kind":"prompt","harness":"pi","cwd":"/repo","session_id":"pi-1","prompt":"Map the auth flow"}`
	var out, errOut bytes.Buffer

	status := runEvent(context.Background(), lifecycleRuntime(lifecycleMemory{contextText: "project context"}), strings.NewReader(input), &out, &errOut)

	if status != 0 {
		t.Fatalf("runEvent() status = %d, want 0; stderr=%q", status, errOut.String())
	}
	response := decodeLifecycleResponse(t, out.String())
	if response.Action != harness.ActionInjectContext || response.Context != "project context" {
		t.Fatalf("response = %#v", response)
	}
	if strings.Contains(out.String()+errOut.String(), lifecycleSecret) {
		t.Fatal("runner exposed lifecycle input in output")
	}
}

func TestRunHookEncodesClaudeAndCodexResponsesForTheirNativeEvents(t *testing.T) {
	registry := harness.BuiltinAdapters()
	fixtures := []struct {
		name    string
		adapter string
		file    string
	}{
		{name: "Claude", adapter: "claude", file: "claude-session-start.json"},
		{name: "Codex", adapter: "codex", file: "codex-session-start.json"},
	}

	for _, tt := range fixtures {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "dualmem", "harness", "testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer

			status := runHook(context.Background(), lifecycleRuntime(lifecycleMemory{contextText: "native context"}), registry, tt.adapter, bytes.NewReader(raw), &out, &errOut)

			if status != 0 {
				t.Fatalf("runHook() status = %d, want 0; stderr=%q", status, errOut.String())
			}
			var response struct {
				HookSpecificOutput struct {
					HookEventName     string `json:"hookEventName"`
					AdditionalContext string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(out.Bytes(), &response); err != nil {
				t.Fatalf("native response is not JSON: %v; raw=%q", err, out.String())
			}
			if response.HookSpecificOutput.HookEventName != "SessionStart" || response.HookSpecificOutput.AdditionalContext != "native context\n\nnative context" {
				t.Fatalf("native response = %s", out.String())
			}
		})
	}
}

func TestLifecycleOutputsBoundEscapeHeavyContextAsValidJSON(t *testing.T) {
	const maxPayloadBytes = 24 * 1024
	escapeHeavyContext := strings.Repeat("\\\"\n\t", 16*1024)
	runtime := lifecycleRuntime(lifecycleMemory{contextText: escapeHeavyContext})
	registry := harness.BuiltinAdapters()

	tests := []struct {
		name string
		run  func(*bytes.Buffer, *bytes.Buffer) int
	}{
		{
			name: "neutral event",
			run: func(out, errOut *bytes.Buffer) int {
				input := `{"schema_version":"1.0","kind":"prompt","harness":"future","cwd":"/repo","prompt":"inspect encoding"}`
				return runEvent(context.Background(), runtime, strings.NewReader(input), out, errOut)
			},
		},
		{
			name: "Claude native hook",
			run: func(out, errOut *bytes.Buffer) int {
				input := `{"session_id":"claude-1","cwd":"/repo","hook_event_name":"UserPromptSubmit","prompt":"inspect encoding"}`
				return runHook(context.Background(), runtime, registry, "claude", strings.NewReader(input), out, errOut)
			},
		},
		{
			name: "Codex native hook",
			run: func(out, errOut *bytes.Buffer) int {
				input := `{"session_id":"codex-1","cwd":"/repo","hook_event_name":"UserPromptSubmit","prompt":"inspect encoding"}`
				return runHook(context.Background(), runtime, registry, "codex", strings.NewReader(input), out, errOut)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if status := tt.run(&out, &errOut); status != 0 {
				t.Fatalf("runner status = %d, want 0; stderr=%q", status, errOut.String())
			}
			if out.Len() > maxPayloadBytes {
				t.Fatalf("encoded payload = %d bytes, want <= %d", out.Len(), maxPayloadBytes)
			}
			if !json.Valid(out.Bytes()) {
				t.Fatalf("encoded payload is invalid JSON: %q", out.String())
			}

			var encoded map[string]any
			if err := json.Unmarshal(out.Bytes(), &encoded); err != nil {
				t.Fatal(err)
			}
			if len(encoded) == 0 {
				t.Fatal("bounded payload dropped all native/neutral response fields")
			}
		})
	}
}

func TestRunHookRejectsUnknownAdapter(t *testing.T) {
	var out, errOut bytes.Buffer

	status := runHook(context.Background(), lifecycleRuntime(lifecycleMemory{}), harness.BuiltinAdapters(), "future", strings.NewReader(`{}`), &out, &errOut)

	if status != 2 {
		t.Fatalf("runHook() status = %d, want 2", status)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "unknown adapter") {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestRunHookFailsOpenWithRedactedDiagnosticsForMalformedInput(t *testing.T) {
	input := `{"hook_event_name":"SessionStart","cwd":"/repo","secret":"` + lifecycleSecret + `"`
	var out, errOut bytes.Buffer

	status := runHook(context.Background(), lifecycleRuntime(lifecycleMemory{}), harness.BuiltinAdapters(), "claude", strings.NewReader(input), &out, &errOut)

	if status != 0 {
		t.Fatalf("runHook() status = %d, want 0", status)
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("native fallback is not JSON: %q", out.String())
	}
	if strings.Contains(out.String()+errOut.String(), lifecycleSecret) {
		t.Fatalf("runner exposed secret: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "lifecycle") {
		t.Fatalf("stderr = %q, want redacted lifecycle diagnostic", errOut.String())
	}
}

func TestRunEventFailsOpenWithRedactedRuntimeDiagnostic(t *testing.T) {
	input := `{"schema_version":"1.0","kind":"prompt","harness":"future","cwd":"/repo","prompt":"` + lifecycleSecret + `"}`
	var out, errOut bytes.Buffer

	status := runEvent(context.Background(), lifecycleRuntime(lifecycleMemory{err: errors.New(lifecycleSecret)}), strings.NewReader(input), &out, &errOut)

	if status != 0 {
		t.Fatalf("runEvent() status = %d, want 0", status)
	}
	response := decodeLifecycleResponse(t, out.String())
	if response.Action != harness.ActionNone || len(response.Diagnostics) != 1 {
		t.Fatalf("response = %#v", response)
	}
	if strings.Contains(out.String()+errOut.String(), lifecycleSecret) {
		t.Fatalf("runner exposed secret: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestResolveNamespacePreservesExplicitAndConfiguredNamespaces(t *testing.T) {
	tests := []struct {
		name string
		flag string
		cfg  CLIConfig
		want string
	}{
		{
			name: "explicit namespace wins",
			flag: "team:explicit",
			cfg:  CLIConfig{DefaultNamespace: "team:configured"},
			want: "team:explicit",
		},
		{
			name: "configured namespace is retained verbatim",
			cfg:  CLIConfig{DefaultNamespace: "team:configured"},
			want: "team:configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveNamespace(tt.flag, tt.cfg); got != tt.want {
				t.Fatalf("resolveNamespace() = %q, want %q", got, tt.want)
			}
		})
	}
}
