package dualmem

import (
	"bufio"
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

// extractBraceBlock finds the end of a brace-delimited block.
func extractBraceBlock(lines []string, startLine int) (int, int) {
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
			if end-startLine+1 > maxBlockLines {
				end = startLine + maxBlockLines - 1
			}
			return startLine, end
		}
	}
	end := startLine + maxBlockLines - 1
	if end >= len(lines) {
		end = len(lines) - 1
	}
	return startLine, end
}

// extractPythonBlock finds the end of an indentation-delimited block.
func extractPythonBlock(lines []string, startLine int) (int, int) {
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

	if end-startLine+1 > maxBlockLines {
		end = startLine + maxBlockLines - 1
	}
	return startLine, end
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
