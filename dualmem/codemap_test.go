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
	embs := HDCEncodeCodeMap(cm)

	results := SearchCodeMap(cm, embs, "authentication token bcrypt", 2)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Path != "auth/" {
		t.Errorf("expected auth/ first, got %s (sim=%.4f)", results[0].Path, results[0].Similarity)
	}
	if results[0].Similarity <= 0 {
		t.Error("expected positive similarity for top result")
	}
	if results[0].Similarity <= results[1].Similarity {
		t.Error("expected results sorted by descending similarity")
	}

	t.Logf("Results: %s (%.4f), %s (%.4f)", results[0].Path, results[0].Similarity, results[1].Path, results[1].Similarity)

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
	out := cm.RenderAtBudget(30, nil, nil)
	if !strings.Contains(out, "Go project") {
		t.Error("expected zoom-1 in tiny budget")
	}
	if strings.Contains(out, "engine/") {
		t.Error("should not include zoom-2 at 30 token budget")
	}

	// Large budget: zoom-1 + zoom-2
	out = cm.RenderAtBudget(500, nil, nil)
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
		for _, bad := range []string{"images", "icons", "fonts", "sass", ".github", ".changeset", ".superpowers", "random-stuff"} {
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

	// Fake embeddings: engine is similar to query, store is not
	queryEmb := []float32{1.0, 0.0, 0.0}
	moduleEmbs := map[string][]float32{
		"engine/":  {0.9, 0.1, 0.0},  // high similarity
		"store/":   {0.0, 0.0, 1.0},  // low similarity
		"cmd/cli/": {0.1, 0.9, 0.0},  // medium similarity
	}

	out := cm.RenderAtBudget(500, queryEmb, moduleEmbs)

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
	out := cm.RenderAtBudget(500, nil, nil)

	bigIdx := strings.Index(out, "big/")
	smallIdx := strings.Index(out, "small/")
	if bigIdx < 0 || smallIdx < 0 {
		t.Fatalf("expected both modules, got: %s", out)
	}
	if bigIdx > smallIdx {
		t.Errorf("expected big/ before small/ (more files) in fallback sort")
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
