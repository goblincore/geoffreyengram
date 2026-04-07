package dualmem

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	ts "github.com/odvcencio/gotreesitter"
)

// countNewlines counts newline characters in data up to position pos.
func countNewlines(data []byte, pos uint32) int {
	return bytes.Count(data[:pos], []byte("\n"))
}

// findEnclosingFunction walks up the AST from a node to find the enclosing function name.
// Returns empty string if no enclosing function is found (module-level call).
func findEnclosingFunction(node *ts.Node, lang *ts.Language, src []byte) string {
	current := node.Parent()
	for current != nil {
		nodeType := current.Type(lang)
		switch nodeType {
		case "function_declaration", "method_definition", "function_definition", "function_item":
			nameNode := current.ChildByFieldName("name", lang)
			if nameNode != nil {
				return string(src[nameNode.StartByte():nameNode.EndByte()])
			}
		case "arrow_function":
			// Arrow functions don't have names; the name is on the variable_declarator parent.
			parent := current.Parent()
			if parent != nil && parent.Type(lang) == "variable_declarator" {
				nameNode := parent.ChildByFieldName("name", lang)
				if nameNode != nil {
					return string(src[nameNode.StartByte():nameNode.EndByte()])
				}
			}
		}
		current = current.Parent()
	}
	return ""
}

// extractGoCallEdges extracts function call edges from Go source files using go/ast.
// relPath is the module-relative directory path (e.g. "dualmem").
// goFiles is a list of absolute paths to .go files in that directory.
func extractGoCallEdges(relPath string, goFiles []string) []StructuralEdge {
	if len(goFiles) == 0 {
		return nil
	}

	fset := token.NewFileSet()
	dir := filepath.Dir(goFiles[0])

	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil
	}

	var edges []StructuralEdge
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				d, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}

				// Build enclosing function name
				enclosing := d.Name.Name
				if d.Recv != nil && len(d.Recv.List) > 0 {
					recvType := d.Recv.List[0].Type
					switch rt := recvType.(type) {
					case *ast.StarExpr:
						if ident, ok := rt.X.(*ast.Ident); ok {
							enclosing = ident.Name + "." + d.Name.Name
						}
					case *ast.Ident:
						enclosing = rt.Name + "." + d.Name.Name
					}
				}

				ast.Inspect(d.Body, func(n ast.Node) bool {
					callExpr, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}

					var callee string
					switch fun := callExpr.Fun.(type) {
					case *ast.Ident:
						callee = fun.Name
					case *ast.SelectorExpr:
						if ident, ok := fun.X.(*ast.Ident); ok {
							callee = ident.Name + "." + fun.Sel.Name
						} else {
							return true
						}
					default:
						return true
					}

					if callee == "" {
						return true
					}

					line := fset.Position(callExpr.Pos()).Line
					edges = append(edges, StructuralEdge{
						SourcePath:        relPath,
						TargetPath:        relPath,
						EdgeType:          "calls",
						Weight:            1.0,
						LineNumber:        line,
						EnclosingFunction: enclosing,
						CalleeName:        callee,
					})
					return true
				})
			}
		}
	}
	return edges
}

// extractTSCallEdges extracts function call edges from TypeScript/JavaScript files using tree-sitter.
func extractTSCallEdges(relPath string, files []string) []StructuralEdge {
	ensureLangs()
	if len(files) == 0 || tsLangConfig.lang == nil {
		return nil
	}

	lang := tsLangConfig.lang
	p := ts.NewParser(lang)

	simpleQuery := `(call_expression function: (identifier) @call)`
	memberQuery := `(call_expression function: (member_expression property: (property_identifier) @call))`

	var edges []StructuralEdge
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		tree, err := p.Parse(data)
		if err != nil {
			continue
		}
		root := tree.RootNode()

		for _, queryStr := range []string{simpleQuery, memberQuery} {
			extractCaptures(root, lang, data, queryStr, func(text string, node *ts.Node) {
				line := 1 + countNewlines(data, node.StartByte())
				enclosing := findEnclosingFunction(node, lang, data)
				edges = append(edges, StructuralEdge{
					SourcePath:        relPath,
					TargetPath:        relPath,
					EdgeType:          "calls",
					Weight:            1.0,
					LineNumber:        line,
					EnclosingFunction: enclosing,
					CalleeName:        text,
				})
			})
		}

		tree.Release()
	}
	return edges
}

// extractPyCallEdges extracts function call edges from Python files using tree-sitter.
func extractPyCallEdges(relPath string, files []string) []StructuralEdge {
	ensureLangs()
	if len(files) == 0 || pyConfig.lang == nil {
		return nil
	}

	lang := pyConfig.lang
	p := ts.NewParser(lang)

	simpleQuery := `(call function: (identifier) @call)`
	attrQuery := `(call function: (attribute attribute: (identifier) @call))`

	var edges []StructuralEdge
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		tree, err := p.Parse(data)
		if err != nil {
			continue
		}
		root := tree.RootNode()

		for _, queryStr := range []string{simpleQuery, attrQuery} {
			extractCaptures(root, lang, data, queryStr, func(text string, node *ts.Node) {
				line := 1 + countNewlines(data, node.StartByte())
				enclosing := findEnclosingFunction(node, lang, data)
				edges = append(edges, StructuralEdge{
					SourcePath:        relPath,
					TargetPath:        relPath,
					EdgeType:          "calls",
					Weight:            1.0,
					LineNumber:        line,
					EnclosingFunction: enclosing,
					CalleeName:        text,
				})
			})
		}

		tree.Release()
	}
	return edges
}

// extractRsCallEdges extracts function call edges from Rust files using tree-sitter.
func extractRsCallEdges(relPath string, files []string) []StructuralEdge {
	ensureLangs()
	if len(files) == 0 || rsConfig.lang == nil {
		return nil
	}

	lang := rsConfig.lang
	p := ts.NewParser(lang)

	simpleQuery := `(call_expression function: (identifier) @call)`
	methodQuery := `(call_expression function: (field_expression field: (field_identifier) @call))`

	var edges []StructuralEdge
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		tree, err := p.Parse(data)
		if err != nil {
			continue
		}
		root := tree.RootNode()

		for _, queryStr := range []string{simpleQuery, methodQuery} {
			extractCaptures(root, lang, data, queryStr, func(text string, node *ts.Node) {
				line := 1 + countNewlines(data, node.StartByte())
				enclosing := findEnclosingFunction(node, lang, data)
				edges = append(edges, StructuralEdge{
					SourcePath:        relPath,
					TargetPath:        relPath,
					EdgeType:          "calls",
					Weight:            1.0,
					LineNumber:        line,
					EnclosingFunction: enclosing,
					CalleeName:        text,
				})
			})
		}

		tree.Release()
	}
	return edges
}

// extractImportEdges extracts import relationship edges from source files.
// For Go files, pass nil for cfg; uses go/ast.
// For tree-sitter languages, pass the appropriate langConfig.
func extractImportEdges(relPath string, files []string, cfg *langConfig) []StructuralEdge {
	if len(files) == 0 {
		return nil
	}

	if cfg != nil {
		// Tree-sitter path
		ensureLangs()
		if cfg.lang == nil {
			return nil
		}

		p := ts.NewParser(cfg.lang)
		var edges []StructuralEdge

		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			tree, err := p.Parse(data)
			if err != nil {
				continue
			}
			root := tree.RootNode()

			if cfg.importQuery != "" {
				extractCaptures(root, cfg.lang, data, cfg.importQuery, func(text string, node *ts.Node) {
					line := 1 + countNewlines(data, node.StartByte())
					edges = append(edges, StructuralEdge{
						SourcePath: relPath,
						TargetPath: text,
						EdgeType:   "imports",
						Weight:     0.7,
						LineNumber: line,
						CalleeName: text,
					})
				})
			}

			tree.Release()
		}
		return edges
	}

	// Go path — use go/ast
	fset := token.NewFileSet()
	dir := filepath.Dir(files[0])

	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil
	}

	var edges []StructuralEdge
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				genDecl, ok := decl.(*ast.GenDecl)
				if !ok || genDecl.Tok != token.IMPORT {
					continue
				}
				for _, spec := range genDecl.Specs {
					imp, ok := spec.(*ast.ImportSpec)
					if !ok {
						continue
					}
					importPath := strings.Trim(imp.Path.Value, `"`)
					line := fset.Position(imp.Pos()).Line
					edges = append(edges, StructuralEdge{
						SourcePath: relPath,
						TargetPath: importPath,
						EdgeType:   "imports",
						Weight:     0.7,
						LineNumber: line,
						CalleeName: importPath,
					})
				}
			}
		}
	}
	return edges
}


