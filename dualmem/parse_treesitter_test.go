package dualmem

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTreeSitter_TypeScript(t *testing.T) {
	ensureLangs()
	tmpDir := t.TempDir()

	writeFile(t, tmpDir, "service.ts", `export class UserService {
  constructor() {}
}
export interface Config {
  dbPath: string;
}
export function createApp(): void {}
export const VERSION = "1.0";
function helperFn() {}
import { Router } from 'express';
`)

	tsFile := filepath.Join(tmpDir, "service.ts")
	mod := parseTSModule("service", []string{tsFile})
	if mod == nil {
		t.Fatal("expected non-nil module")
	}

	// Language
	if mod.Language != "typescript" {
		t.Errorf("Language = %q, want typescript", mod.Language)
	}

	// KeyTypes: exported class/interface with "class " prefix
	typeNames := strings.Join(mod.KeyTypes, " ")
	if !strings.Contains(typeNames, "class UserService") {
		t.Errorf("expected 'class UserService' in KeyTypes, got %v", mod.KeyTypes)
	}
	if !strings.Contains(typeNames, "type Config") {
		t.Errorf("expected 'type Config' in KeyTypes, got %v", mod.KeyTypes)
	}

	// EntryPoints: exported function names suffixed with "()"
	entryNames := strings.Join(mod.EntryPoints, " ")
	if !strings.Contains(entryNames, "createApp()") {
		t.Errorf("expected 'createApp()' in EntryPoints, got %v", mod.EntryPoints)
	}

	// Imports: extracted from import statement source
	importNames := strings.Join(mod.Imports, " ")
	if !strings.Contains(importNames, "express") {
		t.Errorf("expected 'express' in Imports, got %v", mod.Imports)
	}

	// Identifiers: non-exported functions
	identNames := strings.Join(mod.Identifiers, " ")
	if !strings.Contains(identNames, "helperFn") {
		t.Errorf("expected 'helperFn' in Identifiers, got %v", mod.Identifiers)
	}

	t.Logf("TS module: types=%v entries=%v imports=%v idents=%v",
		mod.KeyTypes, mod.EntryPoints, mod.Imports, mod.Identifiers)
}

func TestParseTreeSitter_Python(t *testing.T) {
	ensureLangs()
	tmpDir := t.TempDir()

	writeFile(t, tmpDir, "models.py", `class MyModel:
    pass
def public_function():
    pass
def _private_helper():
    pass
from os import path
import json
`)

	pyFile := filepath.Join(tmpDir, "models.py")
	mod := parsePythonModule("models", []string{pyFile})
	if mod == nil {
		t.Fatal("expected non-nil module")
	}

	// Language
	if mod.Language != "python" {
		t.Errorf("Language = %q, want python", mod.Language)
	}

	// KeyTypes: class names with "class " prefix
	typeNames := strings.Join(mod.KeyTypes, " ")
	if !strings.Contains(typeNames, "class MyModel") {
		t.Errorf("expected 'class MyModel' in KeyTypes, got %v", mod.KeyTypes)
	}

	// EntryPoints: public functions (no underscore prefix)
	entryNames := strings.Join(mod.EntryPoints, " ")
	if !strings.Contains(entryNames, "public_function()") {
		t.Errorf("expected 'public_function()' in EntryPoints, got %v", mod.EntryPoints)
	}
	if strings.Contains(entryNames, "_private_helper()") {
		t.Errorf("should not have '_private_helper()' in EntryPoints, got %v", mod.EntryPoints)
	}

	// Identifiers: private functions (underscore prefix)
	identNames := strings.Join(mod.Identifiers, " ")
	if !strings.Contains(identNames, "_private_helper") {
		t.Errorf("expected '_private_helper' in Identifiers, got %v", mod.Identifiers)
	}

	// Imports: from-import and plain import
	importNames := strings.Join(mod.Imports, " ")
	if !strings.Contains(importNames, "os") {
		t.Errorf("expected 'os' in Imports, got %v", mod.Imports)
	}
	if !strings.Contains(importNames, "json") {
		t.Errorf("expected 'json' in Imports, got %v", mod.Imports)
	}

	t.Logf("Python module: types=%v entries=%v imports=%v idents=%v",
		mod.KeyTypes, mod.EntryPoints, mod.Imports, mod.Identifiers)
}

func TestParseTreeSitter_Rust(t *testing.T) {
	ensureLangs()
	tmpDir := t.TempDir()

	writeFile(t, tmpDir, "lib.rs", `pub struct Config {
    pub name: String,
}
pub enum Status {
    Active,
    Inactive,
}
pub trait Handler {
    fn handle(&self);
}
pub fn init() {}
fn private_helper() {}
use std::collections::HashMap;
`)

	rsFile := filepath.Join(tmpDir, "lib.rs")
	mod := parseRustModule("lib", []string{rsFile})
	if mod == nil {
		t.Fatal("expected non-nil module")
	}

	// Language
	if mod.Language != "rust" {
		t.Errorf("Language = %q, want rust", mod.Language)
	}

	// KeyTypes with correct prefixes
	typeNames := strings.Join(mod.KeyTypes, " ")
	if !strings.Contains(typeNames, "struct Config") {
		t.Errorf("expected 'struct Config' in KeyTypes, got %v", mod.KeyTypes)
	}
	if !strings.Contains(typeNames, "enum Status") {
		t.Errorf("expected 'enum Status' in KeyTypes, got %v", mod.KeyTypes)
	}
	if !strings.Contains(typeNames, "trait Handler") {
		t.Errorf("expected 'trait Handler' in KeyTypes, got %v", mod.KeyTypes)
	}

	// EntryPoints: only pub functions
	entryNames := strings.Join(mod.EntryPoints, " ")
	if !strings.Contains(entryNames, "init()") {
		t.Errorf("expected 'init()' in EntryPoints, got %v", mod.EntryPoints)
	}
	if strings.Contains(entryNames, "private_helper()") {
		t.Errorf("should not have 'private_helper()' in EntryPoints, got %v", mod.EntryPoints)
	}

	// Identifiers: private (non-pub) functions
	identNames := strings.Join(mod.Identifiers, " ")
	if !strings.Contains(identNames, "private_helper") {
		t.Errorf("expected 'private_helper' in Identifiers, got %v", mod.Identifiers)
	}

	// Imports from use declarations
	importNames := strings.Join(mod.Imports, " ")
	if !strings.Contains(importNames, "HashMap") {
		t.Errorf("expected import containing 'HashMap', got %v", mod.Imports)
	}

	t.Logf("Rust module: types=%v entries=%v imports=%v idents=%v",
		mod.KeyTypes, mod.EntryPoints, mod.Imports, mod.Identifiers)
}

func TestParseTreeSitter_ClassifySignificance(t *testing.T) {
	ensureLangs()
	tmpDir := t.TempDir()
	cfg := &tsLangConfig

	cases := []struct {
		name    string
		content string
		wantSig bool
	}{
		{
			name: "five_exports_is_significant",
			content: `export function useCreate() { return null }
export function useDelete() { return null }
export function useUpdate() { return null }
export function useSearch() { return null }
export function useList() { return null }
`,
			wantSig: true,
		},
		{
			name: "two_exports_not_significant",
			content: `export function a() {}
export function b() {}
`,
			wantSig: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fileName := strings.ReplaceAll(tc.name, "_", "-") + ".ts"
			writeFile(t, tmpDir, fileName, tc.content)
			sig := classifyFileSignificance(filepath.Join(tmpDir, fileName), cfg)
			if sig.isSignificant != tc.wantSig {
				t.Errorf("isSignificant = %v, want %v (exports=%d, lines=%d)",
					sig.isSignificant, tc.wantSig, sig.exportCount, sig.lineCount)
			}
		})
	}

	// Large file (>=300 lines) is significant regardless of export count
	t.Run("large_file_is_significant", func(t *testing.T) {
		var big strings.Builder
		big.WriteString("export function onlyExport() {}\n")
		for i := 0; i < 310; i++ {
			big.WriteString("// padding line\n")
		}
		writeFile(t, tmpDir, "big-file.ts", big.String())
		sig := classifyFileSignificance(filepath.Join(tmpDir, "big-file.ts"), cfg)
		if !sig.isSignificant {
			t.Errorf("large file should be significant (exports=%d, lines=%d)",
				sig.exportCount, sig.lineCount)
		}
	})
}

func TestParseTreeSitter_ModuleSplit_SingleFile(t *testing.T) {
	ensureLangs()
	tmpDir := t.TempDir()

	writeFile(t, tmpDir, "index.ts", `export function hello() {}`)

	tsFile := filepath.Join(tmpDir, "index.ts")
	modules := parseTSModuleSplit("src", []string{tsFile})

	if len(modules) != 1 {
		t.Fatalf("expected 1 module for single file, got %d", len(modules))
	}
	if modules[0].FileCount != 1 {
		t.Errorf("FileCount = %d, want 1", modules[0].FileCount)
	}
}

func TestParseTreeSitter_ModuleSplit_SignificantFileSeparates(t *testing.T) {
	ensureLangs()
	tmpDir := t.TempDir()

	// Significant file: 5 exports (>= threshold)
	writeFile(t, tmpDir, "big.ts", `export function a() {}
export function b() {}
export function c() {}
export function d() {}
export function e() {}
`)

	// Small file: below significance threshold
	writeFile(t, tmpDir, "small.ts", `export function x() {}`)

	bigFile := filepath.Join(tmpDir, "big.ts")
	smallFile := filepath.Join(tmpDir, "small.ts")

	modules := parseTSModuleSplit("src", []string{bigFile, smallFile})

	// Should produce 2 modules: big.ts as its own, small.ts in rest group
	if len(modules) != 2 {
		t.Fatalf("expected 2 modules, got %d", len(modules))
	}

	for _, m := range modules {
		t.Logf("Module: path=%s summary=%s fileCount=%d", m.Path, m.Summary, m.FileCount)
	}

	// One module should be the significant file ("TypeScript file" in summary)
	// One module should be the rest group ("TypeScript module" in summary)
	foundSig := false
	foundRest := false
	for _, m := range modules {
		if m.FileCount == 1 && strings.Contains(m.Summary, "TypeScript file") {
			foundSig = true
		}
		if strings.Contains(m.Summary, "TypeScript module") {
			foundRest = true
		}
	}
	if !foundSig {
		t.Error("expected one module for the significant file with 'TypeScript file' summary")
	}
	if !foundRest {
		t.Error("expected one module for the rest with 'TypeScript module' summary")
	}
}
