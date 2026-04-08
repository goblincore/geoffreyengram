package dualmem

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const maxBlockLines = 60

// countLines returns the number of lines in a string.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// readFileLines reads a file and returns its lines.
func readFileLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// extractBlock finds the code block enclosing the named identifier.
// Returns (startLine, endLine) as 0-based indices into lines.
// Returns (-1, -1) if identifier not found.
func extractBlock(lines []string, identifier, lang string) (int, int) {
	defLine := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if containsDefinition(trimmed, identifier, lang) {
			defLine = i
			break
		}
	}
	if defLine == -1 {
		return -1, -1
	}

	switch lang {
	case "python":
		return extractPythonBlock(lines, defLine)
	default:
		return extractBraceBlock(lines, defLine)
	}
}

// containsDefinition checks if a line contains a definition of the identifier.
func containsDefinition(line, identifier, lang string) bool {
	switch lang {
	case "go":
		return strings.Contains(line, "func "+identifier) ||
			(strings.Contains(line, "func (") && strings.Contains(line, ") "+identifier)) ||
			strings.Contains(line, "type "+identifier)
	case "python":
		return strings.HasPrefix(line, "def "+identifier) ||
			strings.HasPrefix(line, "class "+identifier) ||
			strings.HasPrefix(line, "async def "+identifier)
	case "typescript", "javascript", "tsx", "jsx":
		return strings.Contains(line, "function "+identifier) ||
			strings.Contains(line, "const "+identifier) ||
			strings.Contains(line, "let "+identifier) ||
			strings.Contains(line, "class "+identifier) ||
			strings.Contains(line, "export function "+identifier) ||
			strings.Contains(line, "export const "+identifier) ||
			strings.Contains(line, "export class "+identifier)
	case "rust":
		return strings.Contains(line, "fn "+identifier) ||
			strings.Contains(line, "struct "+identifier) ||
			strings.Contains(line, "enum "+identifier) ||
			strings.Contains(line, "impl "+identifier)
	default:
		return strings.Contains(line, identifier)
	}
}

// extractBraceBlockWithLimit finds the end of a brace-delimited block with a configurable line limit.
func extractBraceBlockWithLimit(lines []string, startLine, limit int) (int, int) {
	depth := 0
	opened := false
	for i := startLine; i < len(lines); i++ {
		for _, ch := range lines[i] {
			if ch == '{' {
				depth++
				opened = true
			} else if ch == '}' {
				depth--
			}
		}
		if opened && depth <= 0 {
			end := i
			if end-startLine+1 > limit {
				end = startLine + limit - 1
			}
			return startLine, end
		}
	}
	end := startLine + limit - 1
	if end >= len(lines) {
		end = len(lines) - 1
	}
	return startLine, end
}

// extractBraceBlock finds the end of a brace-delimited block.
func extractBraceBlock(lines []string, startLine int) (int, int) {
	return extractBraceBlockWithLimit(lines, startLine, maxBlockLines)
}

// extractPythonBlockWithLimit finds the end of an indentation-delimited block with a configurable line limit.
func extractPythonBlockWithLimit(lines []string, startLine, limit int) (int, int) {
	if startLine >= len(lines) {
		return startLine, startLine
	}
	baseIndent := indentLevel(lines[startLine])
	end := startLine

	for i := startLine + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if indentLevel(line) <= baseIndent {
			break
		}
		end = i
	}

	if end-startLine+1 > limit {
		end = startLine + limit - 1
	}
	return startLine, end
}

// extractPythonBlock finds the end of an indentation-delimited block.
func extractPythonBlock(lines []string, startLine int) (int, int) {
	return extractPythonBlockWithLimit(lines, startLine, maxBlockLines)
}

// indentLevel returns the number of leading spaces (tabs count as 4).
func indentLevel(s string) int {
	n := 0
	for _, ch := range s {
		if ch == ' ' {
			n++
		} else if ch == '\t' {
			n += 4
		} else {
			break
		}
	}
	return n
}

// SymbolExtraction holds the result of extracting a symbol from a file.
type SymbolExtraction struct {
	FilePath   string `json:"file"`
	Symbol     string `json:"symbol"`
	StartLine  int    `json:"start_line"` // 1-indexed
	EndLine    int    `json:"end_line"`   // 1-indexed
	Content    string `json:"content"`
	TokenCount int    `json:"tokens"`
}

// ExtractSymbol reads a file and extracts the code block for the named symbol.
// Returns a SymbolExtraction with the extracted content and metadata.
func ExtractSymbol(filePath, symbol string, maxLines int) (*SymbolExtraction, error) {
	lines, err := readFileLines(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}

	lang := detectLangFromPath(filePath)
	if lang == "" {
		lang = "go" // default fallback
	}

	// Try extractBlock with custom limit
	start, end := extractBlockWithLimit(lines, symbol, lang, maxLines)
	if start >= 0 {
		content := strings.Join(lines[start:end+1], "\n")
		return &SymbolExtraction{
			FilePath:   filePath,
			Symbol:     symbol,
			StartLine:  start + 1,
			EndLine:    end + 1,
			Content:    content,
			TokenCount: estimateTokens(content),
		}, nil
	}

	// Fallback: case-insensitive search
	symbolLower := strings.ToLower(symbol)
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), symbolLower) {
			// Found a mention; extract surrounding block
			blkStart := i
			var blkEnd int
			if lang == "python" {
				blkStart, blkEnd = extractPythonBlockWithLimit(lines, i, maxLines)
			} else {
				blkStart, blkEnd = extractBraceBlockWithLimit(lines, i, maxLines)
			}
			if blkStart >= 0 {
				content := strings.Join(lines[blkStart:blkEnd+1], "\n")
				return &SymbolExtraction{
					FilePath:   filePath,
					Symbol:     symbol,
					StartLine:  blkStart + 1,
					EndLine:    blkEnd + 1,
					Content:    content,
					TokenCount: estimateTokens(content),
				}, nil
			}
			break
		}
	}

	return nil, fmt.Errorf("symbol %q not found in %s", symbol, filePath)
}

// extractBlockWithLimit is like extractBlock but accepts a configurable line limit.
func extractBlockWithLimit(lines []string, identifier, lang string, limit int) (int, int) {
	defLine := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if containsDefinition(trimmed, identifier, lang) {
			defLine = i
			break
		}
	}
	if defLine == -1 {
		return -1, -1
	}

	switch lang {
	case "python":
		return extractPythonBlockWithLimit(lines, defLine, limit)
	default:
		return extractBraceBlockWithLimit(lines, defLine, limit)
	}
}

// matchIdentifiers scores identifiers against query tokens and returns
// those with overlap > 0, sorted by score descending.
func matchIdentifiers(identifiers []string, query string) []string {
	queryTokens := make(map[string]bool)
	for _, tok := range hdcTokenize(query) {
		queryTokens[tok] = true
	}

	type scored struct {
		name  string
		score int
	}
	var hits []scored

	for _, id := range identifiers {
		idTokens := hdcTokenizeSymbol(id)
		overlap := 0
		for _, tok := range idTokens {
			if queryTokens[tok] {
				overlap++
			}
		}
		if overlap > 0 {
			hits = append(hits, scored{id, overlap})
		}
	}

	for i := 0; i < len(hits); i++ {
		for j := i + 1; j < len(hits); j++ {
			if hits[j].score > hits[i].score {
				hits[i], hits[j] = hits[j], hits[i]
			}
		}
	}

	result := make([]string, len(hits))
	for i, h := range hits {
		result[i] = h.name
	}
	return result
}
