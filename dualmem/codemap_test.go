package dualmem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanCodebase_GoProject(t *testing.T) {
	// Create a temp Go project
	tmpDir := t.TempDir()

	// Root package
	writeFile(t, tmpDir, "main.go", `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`)

	// Sub-package
	subDir := filepath.Join(tmpDir, "engine")
	os.MkdirAll(subDir, 0755)
	writeFile(t, subDir, "engine.go", `package engine

// Engine is the core type.
type Engine struct {
	Name string
}

// Config holds settings.
type Config struct {
	DBPath string
}

// Init creates a new engine.
func Init(cfg Config) (*Engine, error) {
	return &Engine{}, nil
}

// Search finds things.
func (e *Engine) Search(query string) []string {
	return nil
}
`)

	cm, err := ScanCodebase(tmpDir)
	if err != nil {
		t.Fatalf("ScanCodebase: %v", err)
	}

	// Should have zoom-1
	if cm.Zoom1 == "" {
		t.Fatal("expected non-empty zoom-1")
	}
	t.Logf("Zoom-1: %s", cm.Zoom1)

	// Should have at least 2 modules (root + engine/)
	if len(cm.Zoom2) < 2 {
		t.Fatalf("expected at least 2 modules, got %d", len(cm.Zoom2))
	}

	// Find the engine module
	var engineMod *ModuleMap
	for i := range cm.Zoom2 {
		if strings.Contains(cm.Zoom2[i].Path, "engine") {
			engineMod = &cm.Zoom2[i]
			break
		}
	}
	if engineMod == nil {
		t.Fatal("expected engine/ module in zoom-2")
	}

	if engineMod.Language != "go" {
		t.Errorf("language = %q, want go", engineMod.Language)
	}

	// Should have found exported types
	foundEngine := false
	foundConfig := false
	for _, kt := range engineMod.KeyTypes {
		if strings.Contains(kt, "Engine") {
			foundEngine = true
		}
		if strings.Contains(kt, "Config") {
			foundConfig = true
		}
	}
	if !foundEngine {
		t.Errorf("expected Engine in KeyTypes, got %v", engineMod.KeyTypes)
	}
	if !foundConfig {
		t.Errorf("expected Config in KeyTypes, got %v", engineMod.KeyTypes)
	}

	// Should have found Init as entry point
	foundInit := false
	for _, ep := range engineMod.EntryPoints {
		if strings.Contains(ep, "Init") {
			foundInit = true
		}
	}
	if !foundInit {
		t.Errorf("expected Init() in EntryPoints, got %v", engineMod.EntryPoints)
	}
}

func TestScanCodebase_TypeScriptModule(t *testing.T) {
	tmpDir := t.TempDir()

	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)

	writeFile(t, srcDir, "index.ts", `
export function createApp(): App {
  return new App()
}

export default class App {
  run() {}
}

export interface Config {
  port: number
}

export type UserID = string

export const VERSION = "1.0.0"
`)

	cm, err := ScanCodebase(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	var srcMod *ModuleMap
	for i := range cm.Zoom2 {
		if strings.Contains(cm.Zoom2[i].Path, "src") {
			srcMod = &cm.Zoom2[i]
			break
		}
	}
	if srcMod == nil {
		t.Fatal("expected src/ module")
	}

	if srcMod.Language != "typescript" {
		t.Errorf("language = %q, want typescript", srcMod.Language)
	}

	// Should have found class, interface, type
	typeNames := strings.Join(srcMod.KeyTypes, " ")
	if !strings.Contains(typeNames, "App") {
		t.Errorf("expected App in types, got %v", srcMod.KeyTypes)
	}
	if !strings.Contains(typeNames, "Config") {
		t.Errorf("expected Config in types, got %v", srcMod.KeyTypes)
	}
}

func TestRenderAtBudget(t *testing.T) {
	cm := &CodeMap{
		Zoom1: "Go project. packages: engine, store. binaries: cmd/cli.",
		Zoom2: []ModuleMap{
			{Path: "engine/", Language: "go", Summary: "Go package engine", KeyTypes: []string{"struct Engine", "interface Provider"}, FileCount: 10},
			{Path: "store/", Language: "go", Summary: "Go package store", KeyTypes: []string{"struct SQLiteStore"}, FileCount: 5},
			{Path: "cmd/cli/", Language: "go", Summary: "Go binary (package main)", EntryPoints: []string{"main()"}, FileCount: 1},
		},
	}

	// Tiny budget: zoom-1 only
	out := cm.RenderAtBudget(30)
	if !strings.Contains(out, "Go project") {
		t.Error("expected zoom-1 in tiny budget")
	}
	if strings.Contains(out, "engine/") {
		t.Error("should not include zoom-2 at 30 token budget")
	}

	// Large budget: zoom-1 + zoom-2
	out = cm.RenderAtBudget(500)
	if !strings.Contains(out, "engine/") {
		t.Error("expected engine/ in large budget")
	}
	if !strings.Contains(out, "struct Engine") {
		t.Error("expected types in large budget")
	}
}

func TestSynthesizeZoom1(t *testing.T) {
	modules := []ModuleMap{
		{Path: "./", Language: "go", Summary: "Go package engram", FileCount: 10},
		{Path: "dualmem/", Language: "go", Summary: "Go package dualmem", FileCount: 8},
		{Path: "cmd/dualmem/", Language: "go", Summary: "Go binary (package main)", FileCount: 1},
	}

	zoom1 := synthesizeZoom1(modules, "/tmp/test")
	if !strings.Contains(zoom1, "Go") {
		t.Errorf("expected Go in zoom1, got %q", zoom1)
	}
	if !strings.Contains(zoom1, "packages") || !strings.Contains(zoom1, "binaries") {
		t.Errorf("expected packages and binaries in zoom1, got %q", zoom1)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
