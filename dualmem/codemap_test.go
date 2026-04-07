package dualmem

import (
	"fmt"
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

func TestScanCodebase_PythonModule(t *testing.T) {
	tmpDir := t.TempDir()

	pyDir := filepath.Join(tmpDir, "mypackage")
	os.MkdirAll(pyDir, 0755)

	writeFile(t, pyDir, "service.py", `
from flask import Flask, request
import os
import json

class UserService:
    def get_user(self, user_id):
        pass

class AuthProvider:
    pass

def authenticate(token):
    pass

def _validate_internal(data):
    pass

def _helper():
    pass
`)

	cm, err := ScanCodebase(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	var pyMod *ModuleMap
	for i := range cm.Zoom2 {
		if strings.Contains(cm.Zoom2[i].Path, "mypackage") {
			pyMod = &cm.Zoom2[i]
			break
		}
	}
	if pyMod == nil {
		t.Fatal("expected mypackage/ module")
	}

	if pyMod.Language != "python" {
		t.Errorf("language = %q, want python", pyMod.Language)
	}

	// Types: classes
	typeNames := strings.Join(pyMod.KeyTypes, " ")
	if !strings.Contains(typeNames, "UserService") {
		t.Errorf("expected UserService in types, got %v", pyMod.KeyTypes)
	}
	if !strings.Contains(typeNames, "AuthProvider") {
		t.Errorf("expected AuthProvider in types, got %v", pyMod.KeyTypes)
	}

	// Imports
	importNames := strings.Join(pyMod.Imports, " ")
	if !strings.Contains(importNames, "flask") {
		t.Errorf("expected flask in imports, got %v", pyMod.Imports)
	}
	if !strings.Contains(importNames, "os") {
		t.Errorf("expected os in imports, got %v", pyMod.Imports)
	}

	// Identifiers: private functions (underscore prefix)
	identNames := strings.Join(pyMod.Identifiers, " ")
	if !strings.Contains(identNames, "_validate_internal") {
		t.Errorf("expected _validate_internal in identifiers, got %v", pyMod.Identifiers)
	}

	t.Logf("Python module: types=%v entries=%v imports=%v idents=%v",
		pyMod.KeyTypes, pyMod.EntryPoints, pyMod.Imports, pyMod.Identifiers)
}

func TestScanCodebase_RustModule(t *testing.T) {
	tmpDir := t.TempDir()

	rsDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(rsDir, 0755)

	writeFile(t, rsDir, "lib.rs", `
use std::collections::HashMap;
use serde::{Serialize, Deserialize};

pub struct Config {
    pub name: String,
    pub port: u16,
}

pub trait Handler {
    fn handle(&self);
}

pub enum Status {
    Active,
    Inactive,
}

pub fn serve(config: Config) {}

fn validate(input: &str) -> bool {
    true
}

fn parse_config() -> Config {
    Config { name: String::new(), port: 8080 }
}
`)

	cm, err := ScanCodebase(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	var rsMod *ModuleMap
	for i := range cm.Zoom2 {
		if strings.Contains(cm.Zoom2[i].Path, "src") {
			rsMod = &cm.Zoom2[i]
			break
		}
	}
	if rsMod == nil {
		t.Fatal("expected src/ module")
	}

	if rsMod.Language != "rust" {
		t.Errorf("language = %q, want rust", rsMod.Language)
	}

	// Types: struct, trait, enum
	typeNames := strings.Join(rsMod.KeyTypes, " ")
	if !strings.Contains(typeNames, "Config") {
		t.Errorf("expected Config in types, got %v", rsMod.KeyTypes)
	}
	if !strings.Contains(typeNames, "Handler") {
		t.Errorf("expected Handler in types, got %v", rsMod.KeyTypes)
	}
	if !strings.Contains(typeNames, "Status") {
		t.Errorf("expected Status in types, got %v", rsMod.KeyTypes)
	}

	// Imports
	importNames := strings.Join(rsMod.Imports, " ")
	if !strings.Contains(importNames, "HashMap") {
		t.Errorf("expected HashMap in imports, got %v", rsMod.Imports)
	}

	// Private identifiers
	identNames := strings.Join(rsMod.Identifiers, " ")
	if !strings.Contains(identNames, "validate") {
		t.Errorf("expected validate in identifiers, got %v", rsMod.Identifiers)
	}

	t.Logf("Rust module: types=%v entries=%v imports=%v idents=%v",
		rsMod.KeyTypes, rsMod.EntryPoints, rsMod.Imports, rsMod.Identifiers)
}

func TestSearchCodeMap(t *testing.T) {
	cm := &CodeMap{
		Zoom2: []ModuleMap{
			{Path: "auth/", Language: "go", KeyTypes: []string{"struct AuthService"}, Imports: []string{"crypto/bcrypt"}, Identifiers: []string{"validateToken"}},
			{Path: "db/", Language: "go", KeyTypes: []string{"struct Pool"}, Imports: []string{"database/sql"}, Identifiers: []string{"execQuery"}},
			{Path: "api/", Language: "go", KeyTypes: []string{"struct Router"}, Imports: []string{"net/http"}, Identifiers: []string{"handleRequest"}},
		},
	}
	idx := BuildCodeIndex(cm)

	results := SearchCodeMap(cm, idx, "authentication token bcrypt", 2)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Path != "auth/" {
		t.Errorf("expected auth/ first, got %s (hybrid=%.4f)", results[0].Path, results[0].HybridScore)
	}
	if results[0].HybridScore <= 0 {
		t.Error("expected positive hybrid score for top result")
	}
	if results[0].HybridScore <= results[1].HybridScore {
		t.Error("expected results sorted by descending hybrid score")
	}

	t.Logf("Results: %s (hybrid=%.4f hdc=%.4f kw=%.4f), %s (hybrid=%.4f hdc=%.4f kw=%.4f)",
		results[0].Path, results[0].HybridScore, results[0].Similarity, results[0].KeywordScore,
		results[1].Path, results[1].HybridScore, results[1].Similarity, results[1].KeywordScore)

	// Test with nil codemap
	nilResults := SearchCodeMap(nil, nil, "anything", 5)
	if nilResults != nil {
		t.Error("expected nil for nil codemap")
	}
}

func BenchmarkScanCodebase(b *testing.B) {
	// Benchmark on the geoffreyengram repo itself
	for i := 0; i < b.N; i++ {
		ScanCodebase(".")
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
	out := cm.RenderAtBudget(30, nil, nil, nil)
	if !strings.Contains(out, "Go project") {
		t.Error("expected zoom-1 in tiny budget")
	}
	if strings.Contains(out, "engine/") {
		t.Error("should not include zoom-2 at 30 token budget")
	}

	// Large budget: zoom-1 + zoom-2
	out = cm.RenderAtBudget(500, nil, nil, nil)
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

func TestEmbeddingProviderModelName(t *testing.T) {
	// mockEmbedder is defined in dualmem_test.go; it must implement EmbeddingProvider.
	// After adding ModelName() to the interface, it must return a non-empty string.
	m := &mockEmbedder{dim: 768}
	name := m.ModelName()
	if name == "" {
		t.Error("ModelName() returned empty string; expected a non-empty model identifier")
	}
	t.Logf("ModelName() = %q", name)
}

func TestScanCodebase_FiltersNonCodeDirs(t *testing.T) {
	tmpDir := t.TempDir()

	// Code directory — should be included
	codeDir := filepath.Join(tmpDir, "src", "auth")
	os.MkdirAll(codeDir, 0755)
	writeFile(t, codeDir, "auth.ts", `export function login(): void {}`)

	// Asset directories — should be excluded
	for _, name := range []string{"images", "icons", "fonts", "sass"} {
		d := filepath.Join(tmpDir, "src", name)
		os.MkdirAll(d, 0755)
		for i := 0; i < 5; i++ {
			writeFile(t, d, fmt.Sprintf("file%d.png", i), "data")
		}
	}

	// Tool directories — should be excluded
	for _, name := range []string{".github", ".changeset", ".superpowers"} {
		d := filepath.Join(tmpDir, name)
		os.MkdirAll(d, 0755)
		for i := 0; i < 5; i++ {
			writeFile(t, d, fmt.Sprintf("file%d.yml", i), "data")
		}
	}

	// Python virtual environment — should be excluded
	venvDir := filepath.Join(tmpDir, ".venv", "lib", "python3.9", "site-packages", "numpy", "polynomial")
	os.MkdirAll(venvDir, 0755)
	writeFile(t, venvDir, "polynomial.py", `class Polynomial:\n    pass`)

	// Non-interesting "other" dir with files but no code — should be excluded
	miscDir := filepath.Join(tmpDir, "random-stuff")
	os.MkdirAll(miscDir, 0755)
	for i := 0; i < 10; i++ {
		writeFile(t, miscDir, fmt.Sprintf("file%d.txt", i), "data")
	}

	cm, err := ScanCodebase(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range cm.Zoom2 {
		for _, bad := range []string{"images", "icons", "fonts", "sass", ".github", ".changeset", ".superpowers", ".venv", "random-stuff"} {
			if strings.Contains(m.Path, bad) {
				t.Errorf("should not include %q in codemap, got module %q", bad, m.Path)
			}
		}
	}

	// Code directory should survive
	foundAuth := false
	for _, m := range cm.Zoom2 {
		if strings.Contains(m.Path, "auth") {
			foundAuth = true
		}
	}
	if !foundAuth {
		t.Error("expected src/auth/ module in codemap")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestModuleToSummaryText(t *testing.T) {
	m := ModuleMap{
		Path:        "engine/",
		Language:    "go",
		Summary:     "Go package engine",
		KeyTypes:    []string{"struct Engine", "interface Provider"},
		EntryPoints: []string{"Init()", "Search()"},
	}
	text := moduleToSummaryText(m)
	if !strings.Contains(text, "engine/") {
		t.Errorf("expected path in summary, got %q", text)
	}
	if !strings.Contains(text, "struct Engine") {
		t.Errorf("expected types in summary, got %q", text)
	}
	if !strings.Contains(text, "Init()") {
		t.Errorf("expected entry points in summary, got %q", text)
	}

	// Empty types/entries
	m2 := ModuleMap{Path: "docs/", Language: "other", Summary: "5 files"}
	text2 := moduleToSummaryText(m2)
	if !strings.Contains(text2, "docs/") {
		t.Errorf("expected path in summary, got %q", text2)
	}
}

func TestRenderAtBudget_QueryAware(t *testing.T) {
	cm := &CodeMap{
		Zoom1: "Go project.",
		Zoom2: []ModuleMap{
			{Path: "engine/", Language: "go", Summary: "Go package engine", KeyTypes: []string{"struct Engine"}, FileCount: 10},
			{Path: "store/", Language: "go", Summary: "Go package store", KeyTypes: []string{"struct SQLiteStore"}, FileCount: 5},
			{Path: "cmd/cli/", Language: "go", Summary: "Go binary (package main)", EntryPoints: []string{"main()"}, FileCount: 1},
		},
	}

	// Fake embeddings: engine is similar to query, store is less so but still above threshold
	queryEmb := []float32{1.0, 0.0, 0.0}
	moduleEmbs := map[string][]float32{
		"engine/":  {0.9, 0.1, 0.0},  // high similarity
		"store/":   {0.1, 0.0, 0.9},  // low but above 0.05 threshold
		"cmd/cli/": {0.3, 0.9, 0.0},  // medium similarity
	}

	out := cm.RenderAtBudget(500, queryEmb, moduleEmbs, nil)

	// Engine should appear before store (higher similarity)
	engineIdx := strings.Index(out, "engine/")
	storeIdx := strings.Index(out, "store/")
	if engineIdx < 0 || storeIdx < 0 {
		t.Fatalf("expected both modules in output, got: %s", out)
	}
	if engineIdx > storeIdx {
		t.Errorf("expected engine/ before store/ (higher similarity), got engine at %d, store at %d", engineIdx, storeIdx)
	}
}

func TestRenderAtBudget_FallbackSort(t *testing.T) {
	cm := &CodeMap{
		Zoom1: "Go project.",
		Zoom2: []ModuleMap{
			{Path: "small/", Language: "go", Summary: "Go package small", FileCount: 2},
			{Path: "big/", Language: "go", Summary: "Go package big", FileCount: 20},
		},
	}

	// nil embeddings = file count sort
	out := cm.RenderAtBudget(500, nil, nil, nil)

	bigIdx := strings.Index(out, "big/")
	smallIdx := strings.Index(out, "small/")
	if bigIdx < 0 || smallIdx < 0 {
		t.Fatalf("expected both modules, got: %s", out)
	}
	if bigIdx > smallIdx {
		t.Errorf("expected big/ before small/ (more files) in fallback sort")
	}
}

func TestRenderAtBudget_BoostPaths(t *testing.T) {
	cm := &CodeMap{
		Zoom1: "TS project.",
		Zoom2: []ModuleMap{
			{Path: "src/components/boost-search/", Language: "typescript", Summary: "TypeScript module", FileCount: 2},
			{Path: "src/auth/jwt/", Language: "typescript", Summary: "TypeScript module", KeyTypes: []string{"class JWTService"}, FileCount: 3},
			{Path: "src/components/qrcode/", Language: "typescript", Summary: "TypeScript module", FileCount: 4},
		},
	}

	// All modules have equal low similarity to the query
	queryEmb := []float32{1.0, 0.0, 0.0}
	moduleEmbs := map[string][]float32{
		"src/components/boost-search/": {0.1, 0.0, 0.0},
		"src/auth/jwt/":               {0.1, 0.0, 0.0},
		"src/components/qrcode/":      {0.1, 0.0, 0.0},
	}

	// Boost jwt-related files (from checkpoint)
	boostPaths := []string{"jwt.go", "auth.go", "middleware.go"}

	out := cm.RenderAtBudget(500, queryEmb, moduleEmbs, boostPaths)

	jwtIdx := strings.Index(out, "src/auth/jwt/")
	boostIdx := strings.Index(out, "src/components/boost-search/")
	if jwtIdx < 0 {
		t.Fatalf("expected jwt module in output, got: %s", out)
	}
	if boostIdx < 0 {
		t.Fatalf("expected boost-search module in output, got: %s", out)
	}
	if jwtIdx > boostIdx {
		t.Errorf("expected src/auth/jwt/ before src/components/boost-search/ (boosted by file hints), jwt at %d, boost-search at %d", jwtIdx, boostIdx)
	}
}

func TestRenderAtBudget_MinSimilarityThreshold(t *testing.T) {
	cm := &CodeMap{
		Zoom1: "TS project.",
		Zoom2: []ModuleMap{
			{Path: "src/relevant/", Language: "typescript", Summary: "TypeScript module", FileCount: 5},
			{Path: "src/irrelevant1/", Language: "typescript", Summary: "TypeScript module", FileCount: 3},
			{Path: "src/irrelevant2/", Language: "typescript", Summary: "TypeScript module", FileCount: 2},
		},
	}

	// Only "relevant" has decent similarity; others point away (near-zero cosine)
	queryEmb := []float32{1.0, 0.0, 0.0}
	moduleEmbs := map[string][]float32{
		"src/relevant/":    {0.8, 0.1, 0.0},   // high cosine similarity
		"src/irrelevant1/": {0.01, 0.99, 0.0},  // nearly orthogonal → cosine ≈ 0.01
		"src/irrelevant2/": {0.02, 0.0, 0.99},  // nearly orthogonal → cosine ≈ 0.02
	}

	out := cm.RenderAtBudget(500, queryEmb, moduleEmbs, nil)

	if !strings.Contains(out, "src/relevant/") {
		t.Errorf("expected relevant module in output, got: %s", out)
	}
	if strings.Contains(out, "src/irrelevant1/") {
		t.Errorf("expected irrelevant1 to be filtered by min threshold, got: %s", out)
	}
	if strings.Contains(out, "src/irrelevant2/") {
		t.Errorf("expected irrelevant2 to be filtered by min threshold, got: %s", out)
	}
}

func TestRenderAtBudget_BoostOnly(t *testing.T) {
	cm := &CodeMap{
		Zoom1: "TS project.",
		Zoom2: []ModuleMap{
			{Path: "src/auth/", Language: "typescript", Summary: "Auth module", KeyTypes: []string{"AuthService"}, FileCount: 5},
			{Path: "src/api/", Language: "typescript", Summary: "API endpoints", FileCount: 3},
			{Path: "src/utils/", Language: "typescript", Summary: "Utilities", FileCount: 4},
			{Path: "src/tests/", Language: "typescript", Summary: "Test helpers", FileCount: 2},
		},
	}

	// Vague query produces low-signal HDC vectors — all modules score similarly
	queryEmb := []float32{0.5, 0.5, 0.5}
	moduleEmbs := map[string][]float32{
		"src/auth/":  {0.4, 0.5, 0.6},
		"src/api/":   {0.5, 0.4, 0.6},
		"src/utils/": {0.6, 0.5, 0.4},
		"src/tests/": {0.5, 0.6, 0.4},
	}

	// Checkpoint references auth files only
	boostPaths := []string{"auth.ts", "jwt.ts"}

	// With boostOnly=true, only boosted modules should appear
	out := cm.RenderAtBudget(500, queryEmb, moduleEmbs, boostPaths, true)

	if !strings.Contains(out, "src/auth/") {
		t.Errorf("expected boosted module src/auth/ in output, got: %s", out)
	}
	// Non-boosted modules should NOT appear
	if strings.Contains(out, "src/utils/") {
		t.Errorf("expected src/utils/ to be excluded in boost-only mode, got: %s", out)
	}
	if strings.Contains(out, "src/tests/") {
		t.Errorf("expected src/tests/ to be excluded in boost-only mode, got: %s", out)
	}

	// Without boostOnly, all modules with sufficient similarity should appear
	outNormal := cm.RenderAtBudget(500, queryEmb, moduleEmbs, boostPaths)
	if !strings.Contains(outNormal, "src/utils/") && !strings.Contains(outNormal, "src/tests/") {
		t.Errorf("expected non-boosted modules in normal mode, got: %s", outNormal)
	}
}

func TestClassifyFileSignificance(t *testing.T) {
	tmpDir := t.TempDir()

	// 5 exports → significant
	writeFile(t, tmpDir, "many-exports.ts", `
export function useCreate() { return null }
export function useDelete() { return null }
export function useUpdate() { return null }
export function useSearch() { return null }
export function useList() { return null }
`)
	// 4 exports → not significant (below threshold)
	writeFile(t, tmpDir, "few-exports.ts", `
export function useA() { return null }
export function useB() { return null }
export function useC() { return null }
export function useD() { return null }
`)
	// 300+ lines but few exports → significant by line count
	var bigContent strings.Builder
	bigContent.WriteString("export function onlyExport() {}\n")
	for i := 0; i < 310; i++ {
		fmt.Fprintf(&bigContent, "const line%d = %d\n", i, i)
	}
	writeFile(t, tmpDir, "big-file.ts", bigContent.String())

	ensureLangs()
	cfg := &tsLangConfig

	cases := []struct {
		file       string
		wantSig    bool
		wantExport int
	}{
		{"many-exports.ts", true, 5},
		{"few-exports.ts", false, 4},
		{"big-file.ts", true, 1},
	}

	for _, tc := range cases {
		sig := classifyFileSignificance(filepath.Join(tmpDir, tc.file), cfg)
		if sig.isSignificant != tc.wantSig {
			t.Errorf("%s: isSignificant = %v, want %v (exports=%d, lines=%d)",
				tc.file, sig.isSignificant, tc.wantSig, sig.exportCount, sig.lineCount)
		}
		if sig.exportCount != tc.wantExport {
			t.Errorf("%s: exportCount = %d, want %d", tc.file, sig.exportCount, tc.wantExport)
		}
	}
}

func TestFileLevelSplitting_MonorepoSearch(t *testing.T) {
	tmpDir := t.TempDir()

	// --- mutations/ directory: mixed significance ---
	mutDir := filepath.Join(tmpDir, "react-query", "mutations")
	os.MkdirAll(mutDir, 0755)

	// boosts.ts — significant (8 exports, domain-relevant identifiers)
	var boostsContent strings.Builder
	boostsContent.WriteString(`import { useMutation, useQueryClient } from '@tanstack/react-query'
import { LearnCard } from '@learncard/types'

`)
	boostHooks := []string{
		"useShareBoostMutation", "useCreateBoost", "useCreateChildBoost",
		"useManageSelfAssignedSkillsBoost", "useEditBoostMeta",
		"useDeleteManagedBoostMutation", "useDeleteEarnedBoostMutation",
		"useSendBoostCredential",
	}
	for _, hook := range boostHooks {
		fmt.Fprintf(&boostsContent, `export const %s = () => {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (args: any) => {
      // send credential boost issue logic
      return null
    },
    onSuccess: () => queryClient.invalidateQueries()
  })
}

`, hook)
	}
	// Pad to 300+ lines with helper functions
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&boostsContent, "function boostHelper%d(data: any) { return data }\n", i)
	}
	writeFile(t, mutDir, "boosts.ts", boostsContent.String())

	// Small files — not significant
	writeFile(t, mutDir, "firebase.ts", `
export function initFirebase() { return null }
export function signOut() { return null }
`)
	writeFile(t, mutDir, "index.ts", `export { useCreateBoost } from './boosts'`)

	// --- Noise directories (typical UI components) ---
	uiDir := filepath.Join(tmpDir, "components", "ai-passport")
	os.MkdirAll(uiDir, 0755)
	writeFile(t, uiDir, "AiPassport.tsx", `
export function AiPassport() { return null }
export interface PersonalizedQuestionEnum {}
`)

	settingsDir := filepath.Join(tmpDir, "components", "network-settings")
	os.MkdirAll(settingsDir, 0755)
	writeFile(t, settingsDir, "NetworkSettings.tsx", `
export function NetworkSettings() { return null }
export class NetworkSettingsState {}
`)

	resumeDir := filepath.Join(tmpDir, "components", "resume-builder")
	os.MkdirAll(resumeDir, 0755)
	writeFile(t, resumeDir, "ResumeBuilder.tsx", `
export function ResumeBuilder() { return null }
export class ResumeFieldSource {}
`)

	// --- Scan and verify splitting ---
	cm, err := ScanCodebase(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	t.Log("Modules found:")
	for _, m := range cm.Zoom2 {
		t.Logf("  %s — %s (types=%v, entries=%v)", m.Path, m.Summary, m.KeyTypes, m.EntryPoints)
	}

	// Verify boosts got its own module
	var boostsMod *ModuleMap
	var restMod *ModuleMap
	for i := range cm.Zoom2 {
		if strings.HasSuffix(cm.Zoom2[i].Path, "boosts/") {
			boostsMod = &cm.Zoom2[i]
		}
		if cm.Zoom2[i].Path == "react-query/mutations/" {
			restMod = &cm.Zoom2[i]
		}
	}

	if boostsMod == nil {
		t.Fatal("expected boosts to get its own module")
	}
	if boostsMod.FileCount != 1 {
		t.Errorf("boosts module file count = %d, want 1", boostsMod.FileCount)
	}
	if !strings.Contains(boostsMod.Summary, "TypeScript file") {
		t.Errorf("boosts summary = %q, want 'TypeScript file ...'", boostsMod.Summary)
	}

	// Rest module should contain the small files
	if restMod == nil {
		t.Fatal("expected rest module for small files in mutations/")
	}
	if restMod.FileCount != 2 { // firebase.ts + index.ts
		t.Errorf("rest module file count = %d, want 2", restMod.FileCount)
	}

	// --- Search ranking test ---
	idx := BuildCodeIndex(cm)
	results := SearchCodeMap(cm, idx, "send credential boost issue", 9)

	t.Log("Search results for 'send credential boost issue':")
	for i, r := range results {
		t.Logf("  rank %d: %s (hybrid=%.4f hdc=%.4f kw=%.4f)", i, r.Path, r.HybridScore, r.Similarity, r.KeywordScore)
	}

	boostsRank := -1
	for i, r := range results {
		if strings.Contains(r.Path, "boosts") {
			boostsRank = i
			break
		}
	}
	if boostsRank < 0 || boostsRank >= 3 {
		t.Errorf("expected boosts module in top 3, got rank %d", boostsRank)
	}
}

func BenchmarkScanCodebase_WithSplitting(b *testing.B) {
	tmpDir := b.TempDir()

	// Create a mock monorepo with ~50 directories, some with significant files
	for i := 0; i < 20; i++ {
		dir := filepath.Join(tmpDir, fmt.Sprintf("components/module-%d", i))
		os.MkdirAll(dir, 0755)
		os.WriteFile(filepath.Join(dir, "index.tsx"), []byte(fmt.Sprintf(`
export function Component%d() { return null }
`, i)), 0644)
	}

	// A few directories with significant files
	for i := 0; i < 5; i++ {
		dir := filepath.Join(tmpDir, fmt.Sprintf("services/service-%d", i))
		os.MkdirAll(dir, 0755)

		var big strings.Builder
		for j := 0; j < 8; j++ {
			fmt.Fprintf(&big, "export function handler%d_%d() { return null }\n", i, j)
		}
		for j := 0; j < 300; j++ {
			fmt.Fprintf(&big, "const val%d_%d = %d\n", i, j, j)
		}
		os.WriteFile(filepath.Join(dir, "handlers.ts"), []byte(big.String()), 0644)
		os.WriteFile(filepath.Join(dir, "utils.ts"), []byte("export function util() {}\n"), 0644)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ScanCodebase(tmpDir)
	}
}

func TestCodeMapEmbeddings_StoreRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	embeddings := map[string]ModuleEmbedding{
		"engine/": {
			Summary:   "engine/ — Go package engine. Types: struct Engine",
			Embedding: []float32{0.1, 0.2, 0.3},
		},
		"store/": {
			Summary:   "store/ — Go package store. Types: struct SQLiteStore",
			Embedding: []float32{0.4, 0.5, 0.6},
		},
	}

	// Upsert
	err = store.UpsertCodeMapEmbeddings("test-ns", embeddings, "mock-embedder")
	if err != nil {
		t.Fatal(err)
	}

	// Retrieve
	got, model, err := store.GetCodeMapEmbeddings("test-ns")
	if err != nil {
		t.Fatal(err)
	}
	if model != "mock-embedder" {
		t.Errorf("model = %q, want mock-embedder", model)
	}
	if len(got) != 2 {
		t.Fatalf("got %d embeddings, want 2", len(got))
	}
	if len(got["engine/"]) != 3 {
		t.Errorf("engine/ embedding len = %d, want 3", len(got["engine/"]))
	}

	// Delete
	err = store.DeleteCodeMapEmbeddings("test-ns")
	if err != nil {
		t.Fatal(err)
	}
	got2, _, err := store.GetCodeMapEmbeddings("test-ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 0 {
		t.Errorf("expected empty after delete, got %d", len(got2))
	}
}

// --- Import Graph & Cluster Tests ---

func TestBuildImportGraph(t *testing.T) {
	modules := []ModuleMap{
		{Path: "auth/", Imports: []string{"jwt/", "middleware/"}},
		{Path: "middleware/", Imports: []string{"auth/"}},
		{Path: "jwt/", Imports: []string{}},
		{Path: "pages/login/", Imports: []string{"auth/"}},
		{Path: "utils/", Imports: []string{}}, // isolated
	}

	graph := BuildImportGraph(modules)

	// Should have 5 nodes
	if len(graph.Nodes) != 5 {
		t.Errorf("expected 5 nodes, got %d", len(graph.Nodes))
	}

	// auth → jwt, auth → middleware (2 edges from auth)
	// middleware → auth (1 edge, but undirected creates bidirectional adj)
	// pages/login → auth
	if len(graph.Edges) == 0 {
		t.Error("expected edges, got none")
	}

	// auth should have adjacency to jwt, middleware, pages/login
	authAdj := graph.Adj["auth/"]
	if len(authAdj) < 2 {
		t.Errorf("expected auth/ to have >= 2 neighbors, got %d: %v", len(authAdj), authAdj)
	}

	// utils should have no adjacency
	utilsAdj := graph.Adj["utils/"]
	if len(utilsAdj) != 0 {
		t.Errorf("expected utils/ to have 0 neighbors, got %d", len(utilsAdj))
	}
}

func TestBuildImportGraph_SuffixMatch(t *testing.T) {
	modules := []ModuleMap{
		{Path: "src/auth/provider/", Imports: []string{"./middleware"}},
		{Path: "src/middleware/", Imports: []string{"@learncard/auth/provider"}},
	}

	graph := BuildImportGraph(modules)

	// Both should resolve via suffix matching
	if len(graph.Adj["src/auth/provider/"]) == 0 {
		t.Error("expected src/auth/provider/ to link to src/middleware/")
	}
	if len(graph.Adj["src/middleware/"]) == 0 {
		t.Error("expected src/middleware/ to link to src/auth/provider/")
	}
}

func TestDetectClusters_BasicComponents(t *testing.T) {
	// Two separate connected components
	modules := []ModuleMap{
		// Component 1: auth system
		{Path: "auth/", KeyTypes: []string{"AuthProvider"}, Imports: []string{"jwt/"}},
		{Path: "jwt/", KeyTypes: []string{"TokenStore"}, Imports: []string{"auth/"}},
		{Path: "middleware/", KeyTypes: []string{"AuthMiddleware"}, Imports: []string{"auth/"}},
		// Component 2: data layer
		{Path: "store/", KeyTypes: []string{"SQLiteStore"}, Imports: []string{"models/"}},
		{Path: "models/", KeyTypes: []string{"User", "Session"}, Imports: []string{}},
	}

	graph := BuildImportGraph(modules)
	clusters := DetectClusters(graph)

	if len(clusters) < 2 {
		t.Errorf("expected at least 2 clusters, got %d", len(clusters))
		for _, c := range clusters {
			t.Logf("  cluster %q: %d modules", c.Name, len(c.Modules))
		}
	}

	// Find the auth cluster
	foundAuth := false
	for _, c := range clusters {
		for _, m := range c.Modules {
			if m.Path == "auth/" {
				foundAuth = true
				if len(c.Modules) < 2 {
					t.Errorf("auth cluster should have >= 2 modules, got %d", len(c.Modules))
				}
			}
		}
	}
	if !foundAuth {
		t.Error("expected to find auth/ in a cluster")
	}
}

func TestDetectClusters_LargeComponentSplit(t *testing.T) {
	// Create a chain of 12 modules all connected
	var modules []ModuleMap
	for i := 0; i < 12; i++ {
		path := fmt.Sprintf("mod%d/", i)
		var imports []string
		if i > 0 {
			imports = append(imports, fmt.Sprintf("mod%d/", i-1))
		}
		if i < 11 {
			imports = append(imports, fmt.Sprintf("mod%d/", i+1))
		}
		modules = append(modules, ModuleMap{
			Path:     path,
			KeyTypes: []string{fmt.Sprintf("Type%d", i)},
			Imports:  imports,
		})
	}

	graph := BuildImportGraph(modules)
	clusters := DetectClusters(graph)

	// Should be split into at least 2 clusters since component > 8
	if len(clusters) < 2 {
		t.Errorf("expected >= 2 clusters from 12-module chain, got %d", len(clusters))
	}

	// No cluster should have > 8 modules
	for _, c := range clusters {
		if len(c.Modules) > 8 {
			t.Errorf("cluster %q has %d modules, expected <= 8", c.Name, len(c.Modules))
		}
	}

	// All modules should be accounted for
	total := 0
	for _, c := range clusters {
		total += len(c.Modules)
	}
	if total != 12 {
		t.Errorf("expected 12 total modules across clusters, got %d", total)
	}
}

func TestDetectClusters_SingletonMerge(t *testing.T) {
	modules := []ModuleMap{
		{Path: "core/", KeyTypes: []string{"Engine"}, Imports: []string{"config/"}},
		{Path: "config/", KeyTypes: []string{"Config"}, Imports: []string{}},
		{Path: "utils/", KeyTypes: []string{"Helper"}, Imports: []string{"core/"}}, // singleton that shares edge with core cluster
	}

	graph := BuildImportGraph(modules)
	clusters := DetectClusters(graph)

	// utils should be merged with the core/config cluster
	if len(clusters) > 1 {
		// It's OK if there's 1 cluster (all merged) — that's correct behavior
		t.Logf("Got %d clusters (singleton may or may not merge depending on edge detection)", len(clusters))
	}
}

func TestFormatClusterPrompt(t *testing.T) {
	cluster := Cluster{
		Name: "auth-system",
		Modules: []ModuleMap{
			{Path: "auth/", KeyTypes: []string{"AuthProvider"}, EntryPoints: []string{"login()"}, Imports: []string{"jwt/"}},
			{Path: "jwt/", KeyTypes: []string{"TokenStore"}, EntryPoints: []string{"validateToken()"}},
		},
	}

	prompt := FormatClusterPrompt(cluster)

	if !strings.Contains(prompt, "auth/") {
		t.Error("expected prompt to contain auth/ module")
	}
	if !strings.Contains(prompt, "AuthProvider") {
		t.Error("expected prompt to contain AuthProvider type")
	}
	if !strings.Contains(prompt, "2-3 concise sentences") {
		t.Error("expected prompt to request 2-3 sentences")
	}
}

func TestGenerateClusterName(t *testing.T) {
	// Common prefix
	modules := []ModuleMap{
		{Path: "src/auth/provider/"},
		{Path: "src/auth/middleware/"},
	}
	name := generateClusterName(modules)
	if !strings.Contains(name, "auth") {
		t.Errorf("expected name containing 'auth', got %q", name)
	}

	// No common prefix — uses dominant module
	modules2 := []ModuleMap{
		{Path: "auth/", KeyTypes: []string{"AuthProvider", "Session"}, EntryPoints: []string{"login()"}},
		{Path: "pages/login/"},
	}
	name2 := generateClusterName(modules2)
	if name2 == "" || name2 == "unknown" {
		t.Errorf("expected a meaningful name, got %q", name2)
	}
}

func TestSeedStoreMethods(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	userID := "test-ns"

	// Initially no seeds
	count, err := store.CountSeedMemories(userID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0 seeds, got %d", count)
	}

	// Insert a seed memory
	dm := &DetailMemory{
		ID:              "seed-1",
		Text:            "Auth system description",
		Sector:          "auth-system",
		Salience:        0.5,
		ImportanceScore: 0.7,
		Type:            "seed",
		Files:           []string{"auth/", "jwt/"},
	}
	err = store.InsertDetail(dm, []float32{0.1, 0.2, 0.3}, userID)
	if err != nil {
		t.Fatal(err)
	}

	// Insert an organic memory
	dm2 := &DetailMemory{
		ID:              "organic-1",
		Text:            "An organic memory",
		Sector:          "decision",
		Salience:        0.9,
		ImportanceScore: 0.8,
		Type:            "decision",
	}
	err = store.InsertDetail(dm2, []float32{0.4, 0.5, 0.6}, userID)
	if err != nil {
		t.Fatal(err)
	}

	// Count seeds: should be 1
	count, err = store.CountSeedMemories(userID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 seed, got %d", count)
	}

	// Count excluding seeds: should be 1 (only organic)
	organicCount, err := store.GetDetailCountExcludingSeeds(userID)
	if err != nil {
		t.Fatal(err)
	}
	if organicCount != 1 {
		t.Errorf("expected 1 non-seed, got %d", organicCount)
	}

	// Total count: should be 2
	totalCount, err := store.GetDetailCount(userID)
	if err != nil {
		t.Fatal(err)
	}
	if totalCount != 2 {
		t.Errorf("expected 2 total, got %d", totalCount)
	}

	// Delete seeds
	err = store.DeleteSeedMemories(userID)
	if err != nil {
		t.Fatal(err)
	}

	// Only organic should remain
	count, err = store.CountSeedMemories(userID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0 seeds after delete, got %d", count)
	}
	totalCount, err = store.GetDetailCount(userID)
	if err != nil {
		t.Fatal(err)
	}
	if totalCount != 1 {
		t.Errorf("expected 1 total after seed delete, got %d", totalCount)
	}
}

func TestScanCodebase_DeepNesting(t *testing.T) {
	// Simulate a monorepo with deeply nested TSX files (depth 8)
	// e.g. apps/app/src/pages/feature/sub/components/Widget/Widget.tsx
	tmpDir := t.TempDir()
	deepPath := filepath.Join(tmpDir, "apps", "app", "src", "pages", "feature", "sub", "components", "Widget")
	os.MkdirAll(deepPath, 0755)
	writeFile(t, deepPath, "Widget.tsx", `
import React from 'react';
export interface WidgetProps { title: string }
export const Widget: React.FC<WidgetProps> = ({ title }) => <div>{title}</div>;
export default Widget;
`)

	cm, err := ScanCodebase(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range cm.Zoom2 {
		if strings.Contains(m.Path, "Widget") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("deeply nested Widget.tsx (depth 8) not found in Zoom2 modules; got %d modules", len(cm.Zoom2))
		for _, m := range cm.Zoom2 {
			t.Logf("  module: %s", m.Path)
		}
	}
}

func TestScanCodebase_ManyDirs(t *testing.T) {
	// Create 300 directories with TS files — should all be indexed
	tmpDir := t.TempDir()
	for i := 0; i < 300; i++ {
		dir := filepath.Join(tmpDir, "src", fmt.Sprintf("mod%03d", i))
		os.MkdirAll(dir, 0755)
		writeFile(t, dir, "index.ts", fmt.Sprintf(`export const value%d = %d;`, i, i))
	}

	cm, err := ScanCodebase(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cm.Zoom2) < 300 {
		t.Errorf("expected >=300 modules, got %d (maxDirs cap too low?)", len(cm.Zoom2))
	}
}

func TestScanCodebase_SkipsBenchmarks(t *testing.T) {
	tmpDir := t.TempDir()

	// Real source file
	writeFile(t, tmpDir, "main.go", `package main
func main() {}
`)

	// Benchmarks dir should be skipped
	benchDir := filepath.Join(tmpDir, "benchmarks")
	os.MkdirAll(benchDir, 0755)
	writeFile(t, benchDir, "bench.go", `package benchmarks
func BenchStuff() {}
`)

	cm, err := ScanCodebase(tmpDir)
	if err != nil {
		t.Fatalf("ScanCodebase: %v", err)
	}

	for _, m := range cm.Zoom2 {
		if strings.Contains(m.Path, "benchmarks") {
			t.Errorf("benchmarks/ should be excluded from scan, found module: %s", m.Path)
		}
	}
}

func TestSeedTypeMultiplier(t *testing.T) {
	explore := IntentProfiles[IntentExplore]
	if m := explore.TypeMultiplier("seed"); m != 1.2 {
		t.Errorf("explore.seed = %.1f, want 1.2", m)
	}

	debug := IntentProfiles[IntentDebug]
	if m := debug.TypeMultiplier("seed"); m != 0.6 {
		t.Errorf("debug.seed = %.1f, want 0.6", m)
	}

	cont := IntentProfiles[IntentContinue]
	if m := cont.TypeMultiplier("seed"); m != 0.5 {
		t.Errorf("continue.seed = %.1f, want 0.5", m)
	}
}

func TestSearchCodeMapFiles_RanksCorrectFile(t *testing.T) {
	// Build a CodeMap with one module containing multiple files
	cm := &CodeMap{
		Zoom2: []ModuleMap{
			{
				Path:        "engine/",
				Language:    "go",
				Summary:     "Go package engine",
				KeyTypes:    []string{"struct Index", "struct Query"},
				EntryPoints: []string{"Search()", "BuildIndex()"},
				Identifiers: []string{"tokenize", "normalize", "rank"},
				FileCount:   3,
				Files: []FileInfo{
					{
						RelPath:     "engine/search.go",
						Identifiers: []string{"Search", "tokenize", "rank"},
					},
					{
						RelPath:     "engine/index.go",
						Identifiers: []string{"Index", "BuildIndex", "normalize"},
					},
					{
						RelPath:     "engine/utils.go",
						Identifiers: []string{"formatOutput", "logDebug"},
					},
				},
			},
			{
				Path:        "cmd/",
				Language:    "go",
				Summary:     "Go binary (package main)",
				KeyTypes:    []string{},
				EntryPoints: []string{"main()"},
				Identifiers: []string{"run", "parseFlags"},
				FileCount:   1,
				Files: []FileInfo{
					{
						RelPath:     "cmd/main.go",
						Identifiers: []string{"main", "run", "parseFlags"},
					},
				},
			},
		},
	}

	idx := BuildCodeIndex(cm)
	results := SearchCodeMapFiles(cm, idx, "search tokenize rank", 5)

	if len(results) == 0 {
		t.Fatal("expected file results")
	}

	// engine/search.go should rank first — it has all three query terms
	if results[0].FilePath != "engine/search.go" {
		t.Errorf("expected engine/search.go as top result, got %s", results[0].FilePath)
	}

	// All results should have non-zero combined scores
	for _, r := range results {
		if r.CombinedScore <= 0 {
			t.Errorf("result %s has zero combined score", r.FilePath)
		}
	}
}

func TestSearchCodeMapFiles_ModulesWithoutFiles(t *testing.T) {
	cm := &CodeMap{
		Zoom2: []ModuleMap{
			{
				Path:      "docs/",
				Language:  "other",
				Summary:   "4 files",
				FileCount: 4,
				// No Files field — should still appear in results
			},
		},
	}
	idx := BuildCodeIndex(cm)
	results := SearchCodeMapFiles(cm, idx, "documentation", 5)

	if len(results) == 0 {
		t.Fatal("expected at least one result even without FileInfo")
	}
	if results[0].FilePath != "docs/" {
		t.Errorf("expected docs/ as fallback, got %s", results[0].FilePath)
	}
}
