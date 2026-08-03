package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/goblincore/geoffreyengram/dualmem"
	"github.com/goblincore/geoffreyengram/dualmem/harness"
)

const lifecycleInputLimit int64 = 64 << 10

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
		writeNeutralResponse(out, lifecycleFailureResponse())
		return 0
	}
	if runtime == nil {
		writeLifecycleDiagnostic(errOut, "runtime")
		writeNeutralResponse(out, lifecycleFailureResponse())
		return 0
	}

	response := runtime.Handle(ctx, event)
	if len(response.Diagnostics) > 0 {
		writeLifecycleDiagnostic(errOut, "processing")
	}
	writeNeutralResponse(out, response)
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

func writeNeutralResponse(out io.Writer, response harness.Response) {
	_ = harness.EncodeResponse(out, response)
}

func writeNativeResponse(adapter harness.Adapter, event harness.Event, response harness.Response, out, errOut io.Writer) {
	encoded, err := adapter.Encode(event, response)
	if err != nil {
		writeLifecycleDiagnostic(errOut, "output")
		encoded = []byte("{}")
	}
	_, _ = out.Write(append(encoded, '\n'))
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
