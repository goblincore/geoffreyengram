package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/goblincore/geoffreyengram/dualmem"
	"github.com/goblincore/geoffreyengram/dualmem/harness"
)

const (
	lifecycleInputLimit  int64 = 64 << 10
	lifecycleOutputLimit       = 24 << 10
)

func cmdEvent(cfg CLIConfig) {
	if len(os.Args) != 2 {
		lifecycleArgumentError("event")
		return
	}
	raw, err := readLifecycleInput(os.Stdin)
	if err != nil || !validNormalizedLifecycleInput(raw) {
		runEvent(context.Background(), nil, bytes.NewReader(raw), os.Stdout, os.Stderr)
		return
	}
	engine, err := newEngine(cfg)
	if err != nil {
		runEvent(context.Background(), nil, bytes.NewReader(raw), os.Stdout, os.Stderr)
		return
	}
	runEvent(context.Background(), newHarnessRuntime(engine), bytes.NewReader(raw), os.Stdout, os.Stderr)
}

func cmdHook(cfg CLIConfig) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	adapterName := fs.String("adapter", "", "native hook adapter")
	if err := fs.Parse(os.Args[2:]); err != nil || fs.NArg() != 0 || strings.TrimSpace(*adapterName) == "" {
		lifecycleArgumentError("hook")
		return
	}

	registry := harness.BuiltinAdapters()
	adapter, ok := registry.Get(*adapterName)
	if !ok {
		fmt.Fprintln(os.Stderr, "dualmem hook: unknown adapter")
		os.Exit(2)
	}
	raw, err := readLifecycleInput(os.Stdin)
	if err != nil || !validNativeLifecycleInput(adapter, raw) {
		runHook(context.Background(), nil, registry, *adapterName, bytes.NewReader(raw), os.Stdout, os.Stderr)
		return
	}
	engine, err := newEngine(cfg)
	if err != nil {
		runHook(context.Background(), nil, registry, *adapterName, bytes.NewReader(raw), os.Stdout, os.Stderr)
		return
	}
	runHook(context.Background(), newHarnessRuntime(engine), registry, *adapterName, bytes.NewReader(raw), os.Stdout, os.Stderr)
}

func lifecycleArgumentError(command string) {
	fmt.Fprintf(os.Stderr, "dualmem %s: invalid arguments\n", command)
	os.Exit(2)
}

// runEvent handles one normalized lifecycle event. Lifecycle input and memory
// failures deliberately fail open so they cannot block the calling harness.
func runEvent(ctx context.Context, runtime *harness.Runtime, in io.Reader, out, errOut io.Writer) int {
	event, err := harness.DecodeEvent(in, lifecycleInputLimit)
	if err != nil {
		writeLifecycleDiagnostic(errOut, "input")
		writeNeutralResponse(out, errOut, lifecycleFailureResponse())
		return 0
	}
	if runtime == nil {
		writeLifecycleDiagnostic(errOut, "runtime")
		writeNeutralResponse(out, errOut, lifecycleFailureResponse())
		return 0
	}

	response := runtime.Handle(ctx, event)
	if len(response.Diagnostics) > 0 {
		writeLifecycleDiagnostic(errOut, "processing")
	}
	writeNeutralResponse(out, errOut, response)
	return 0
}

// runHook decodes a native hook payload, runs the matching normalized event,
// and returns the adapter's native response shape.
func runHook(ctx context.Context, runtime *harness.Runtime, registry harness.Registry, adapterName string, in io.Reader, out, errOut io.Writer) int {
	adapter, ok := registry.Get(adapterName)
	if !ok {
		fmt.Fprintln(errOut, "dualmem hook: unknown adapter")
		return 2
	}

	raw, err := readLifecycleInput(in)
	if err != nil {
		writeLifecycleDiagnostic(errOut, "input")
		writeNativeResponse(adapter, harness.Event{}, lifecycleFailureResponse(), out, errOut)
		return 0
	}
	events, err := adapter.Decode(raw)
	if err != nil {
		writeLifecycleDiagnostic(errOut, "input")
		writeNativeResponse(adapter, harness.Event{}, lifecycleFailureResponse(), out, errOut)
		return 0
	}
	if runtime == nil {
		writeLifecycleDiagnostic(errOut, "runtime")
		writeNativeResponse(adapter, harness.Event{}, lifecycleFailureResponse(), out, errOut)
		return 0
	}

	event := harness.Event{}
	response := harness.Response{SchemaVersion: "1.0", Action: harness.ActionNone}
	for _, decoded := range events {
		event = decoded
		response = runtime.Handle(ctx, event)
		if len(response.Diagnostics) > 0 {
			writeLifecycleDiagnostic(errOut, "processing")
		}
	}
	writeNativeResponse(adapter, event, response, out, errOut)
	return 0
}

func readLifecycleInput(in io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(in, lifecycleInputLimit+1))
	if err != nil {
		return raw, err
	}
	if int64(len(raw)) > lifecycleInputLimit {
		return raw, fmt.Errorf("lifecycle input exceeds limit")
	}
	return raw, nil
}

func writeNeutralResponse(out, errOut io.Writer, response harness.Response) {
	payload, err := boundedEncodedResponse(response, func(candidate harness.Response) ([]byte, error) {
		return json.Marshal(candidate)
	})
	if err != nil {
		writeLifecycleDiagnostic(errOut, "output")
		payload = []byte("{}\n")
	}
	_, _ = out.Write(payload)
}

func writeNativeResponse(adapter harness.Adapter, event harness.Event, response harness.Response, out, errOut io.Writer) {
	payload, err := boundedEncodedResponse(response, func(candidate harness.Response) ([]byte, error) {
		return adapter.Encode(event, candidate)
	})
	if err != nil {
		writeLifecycleDiagnostic(errOut, "output")
		payload = []byte("{}\n")
	}
	_, _ = out.Write(payload)
}

func boundedEncodedResponse(response harness.Response, encode func(harness.Response) ([]byte, error)) ([]byte, error) {
	encoded, err := encode(response)
	if err != nil {
		return nil, err
	}
	if len(encoded)+1 <= lifecycleOutputLimit {
		return withTrailingNewline(encoded), nil
	}

	contextRunes := []rune(response.Context)
	low, high := 0, len(contextRunes)
	var best []byte
	for low <= high {
		mid := low + (high-low)/2
		candidate := response
		candidate.Context = string(contextRunes[:mid])
		encoded, err = encode(candidate)
		if err != nil {
			return nil, err
		}
		if len(encoded)+1 <= lifecycleOutputLimit {
			best = append(best[:0], encoded...)
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if best == nil {
		return nil, fmt.Errorf("encoded lifecycle response exceeds limit")
	}
	return withTrailingNewline(best), nil
}

func withTrailingNewline(encoded []byte) []byte {
	payload := make([]byte, len(encoded)+1)
	copy(payload, encoded)
	payload[len(encoded)] = '\n'
	return payload
}

func lifecycleFailureResponse() harness.Response {
	return harness.Response{
		SchemaVersion: "1.0",
		Action:        harness.ActionNone,
		Diagnostics: []harness.Diagnostic{{
			Code:    "lifecycle_unavailable",
			Message: "lifecycle memory unavailable",
		}},
	}
}

func writeLifecycleDiagnostic(errOut io.Writer, stage string) {
	if strings.TrimSpace(stage) == "" {
		stage = "processing"
	}
	fmt.Fprintf(errOut, "dualmem lifecycle: %s unavailable\n", stage)
}

func newHarnessRuntime(engine *dualmem.Engine) *harness.Runtime {
	return &harness.Runtime{
		Memory:         engine,
		Activity:       &harness.JSONLActivitySink{},
		ResolveOptions: harness.DefaultResolveOptions(),
	}
}

func validNormalizedLifecycleInput(raw []byte) bool {
	_, err := harness.DecodeEvent(bytes.NewReader(raw), lifecycleInputLimit)
	return err == nil
}

func validNativeLifecycleInput(adapter harness.Adapter, raw []byte) bool {
	_, err := adapter.Decode(raw)
	return err == nil
}
