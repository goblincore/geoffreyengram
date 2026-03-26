package dualmem

import (
	"hash/fnv"
	"math"
	"math/rand/v2"
	"strings"
	"unicode"
)

// HDCDimensions is the dimensionality of HDC vectors.
const HDCDimensions = 2048

// HDC encoding weights for module components.
const (
	hdcWeightPath    = 0.25
	hdcWeightSymbols = 0.40
	hdcWeightLang    = 0.15
	hdcWeightContent = 0.20 // identifiers (1.0) + imports (0.8)
)

// HDCEncoder generates hyperdimensional vectors from structural code signals.
// Vectors are deterministic — same input always produces the same vector.
type HDCEncoder struct {
	dim int
}

// NewHDCEncoder creates an encoder with the default HDCDimensions.
func NewHDCEncoder() *HDCEncoder {
	return &HDCEncoder{dim: HDCDimensions}
}

// BasisVector returns a deterministic random vector for a token string.
// Uses FNV-64a hash as PRNG seed, fills with +1/-1 values.
func (e *HDCEncoder) BasisVector(token string) []float32 {
	h := fnv.New64a()
	h.Write([]byte(token))
	seed := h.Sum64()

	rng := rand.New(rand.NewPCG(seed, seed^0xDEADBEEF))
	vec := make([]float32, e.dim)
	for i := range vec {
		if rng.Float64() < 0.5 {
			vec[i] = 1.0
		} else {
			vec[i] = -1.0
		}
	}
	return vec
}

// Rotate circularly shifts a vector by n positions (for positional encoding).
func (e *HDCEncoder) Rotate(vec []float32, n int) []float32 {
	if len(vec) == 0 {
		return vec
	}
	n = n % len(vec)
	if n < 0 {
		n += len(vec)
	}
	if n == 0 {
		return vec
	}
	result := make([]float32, len(vec))
	copy(result, vec[len(vec)-n:])
	copy(result[n:], vec[:len(vec)-n])
	return result
}

// Bundle sums multiple vectors element-wise.
func (e *HDCEncoder) Bundle(vecs ...[]float32) []float32 {
	if len(vecs) == 0 {
		return make([]float32, e.dim)
	}
	result := make([]float32, e.dim)
	for _, v := range vecs {
		for i := range result {
			if i < len(v) {
				result[i] += v[i]
			}
		}
	}
	return result
}

// Scale multiplies every element of a vector by a scalar.
func (e *HDCEncoder) Scale(vec []float32, s float32) []float32 {
	result := make([]float32, len(vec))
	for i, v := range vec {
		result[i] = v * s
	}
	return result
}

// Normalize L2-normalizes a vector. Returns a zero vector if norm is zero.
func (e *HDCEncoder) Normalize(vec []float32) []float32 {
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return vec
	}
	result := make([]float32, len(vec))
	for i, v := range vec {
		result[i] = float32(float64(v) / norm)
	}
	return result
}

// EncodeModule produces an HDC vector for a ModuleMap.
// Combines path (0.25), symbols (0.40), language (0.15), and content (0.20).
// Content layer: identifiers at full weight + imports at 0.8× weight.
func (e *HDCEncoder) EncodeModule(m ModuleMap) []float32 {
	// Path component — position-encoded
	pathTokens := hdcTokenize(m.Path)
	var pathVecs [][]float32
	for i, tok := range pathTokens {
		pathVecs = append(pathVecs, e.Rotate(e.BasisVector(tok), i))
	}
	pathVec := e.Bundle(pathVecs...)

	// Symbols component — types + entry points
	var symbolVecs [][]float32
	for _, kt := range m.KeyTypes {
		for _, tok := range hdcTokenizeSymbol(kt) {
			symbolVecs = append(symbolVecs, e.BasisVector(tok))
		}
	}
	for _, ep := range m.EntryPoints {
		for _, tok := range hdcTokenizeSymbol(ep) {
			symbolVecs = append(symbolVecs, e.BasisVector(tok))
		}
	}
	symbolVec := e.Bundle(symbolVecs...)

	// Language component
	langVec := e.BasisVector(strings.ToLower(m.Language))

	// Content component — identifiers (1.0) + imports (0.8)
	var contentVecs [][]float32
	for _, ident := range m.Identifiers {
		for _, tok := range hdcTokenize(ident) {
			contentVecs = append(contentVecs, e.BasisVector(tok))
		}
	}
	for _, imp := range m.Imports {
		for _, tok := range hdcTokenize(imp) {
			contentVecs = append(contentVecs, e.Scale(e.BasisVector(tok), 0.8))
		}
	}
	contentVec := e.Bundle(contentVecs...)

	// Weighted combination
	result := make([]float32, e.dim)
	for i := range result {
		result[i] = hdcWeightPath*pathVec[i] + hdcWeightSymbols*symbolVec[i] +
			hdcWeightLang*langVec[i] + hdcWeightContent*contentVec[i]
	}
	return e.Normalize(result)
}

// EncodeQuery produces an HDC vector for a query string.
// All tokens are bundled with equal weight, no positional encoding.
func (e *HDCEncoder) EncodeQuery(query string) []float32 {
	tokens := hdcTokenize(query)
	if len(tokens) == 0 {
		return make([]float32, e.dim)
	}
	var vecs [][]float32
	for _, tok := range tokens {
		vecs = append(vecs, e.BasisVector(tok))
	}
	return e.Normalize(e.Bundle(vecs...))
}

// hdcTokenize splits a string into lowercase tokens by common code separators,
// whitespace, and camelCase boundaries. Filters stop words.
func hdcTokenize(s string) []string {
	// Replace common separators with spaces
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == '.' || r == '_' || r == '-' || r == '(' || r == ')' {
			return ' '
		}
		return r
	}, s)

	var tokens []string
	for _, word := range strings.Fields(s) {
		for _, part := range splitCamelCase(word) {
			part = strings.ToLower(part)
			if len(part) > 0 && !hdcStopWords[part] {
				tokens = append(tokens, part)
			}
		}
	}
	return tokens
}

// hdcTokenizeSymbol tokenizes a symbol entry like "struct Engine" or "main()",
// stripping kind prefixes.
func hdcTokenizeSymbol(s string) []string {
	// Strip trailing () from entry points
	s = strings.TrimSuffix(s, "()")
	return hdcTokenize(s)
}

// splitCamelCase splits camelCase/PascalCase with acronym handling.
// "HTMLParser" → ["HTML", "Parser"], "getURL" → ["get", "URL"]
func splitCamelCase(s string) []string {
	var parts []string
	runes := []rune(s)
	start := 0
	for i := 1; i < len(runes); i++ {
		prev := runes[i-1]
		cur := runes[i]
		split := false
		switch {
		case unicode.IsLower(prev) && unicode.IsUpper(cur):
			// "getURL" → split before U
			split = true
		case unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(runes) && unicode.IsLower(runes[i+1]):
			// "HTMLParser" → split before P (uppercase followed by uppercase+lowercase)
			split = true
		case unicode.IsLetter(prev) && unicode.IsDigit(cur):
			split = true
		case unicode.IsDigit(prev) && unicode.IsLetter(cur):
			split = true
		}
		if split {
			parts = append(parts, string(runes[start:i]))
			start = i
		}
	}
	if start < len(runes) {
		parts = append(parts, string(runes[start:]))
	}
	return parts
}

// hdcStopWords are kind prefixes and common noise stripped from symbol tokens.
var hdcStopWords = map[string]bool{
	"struct": true, "interface": true, "type": true, "func": true,
	"class": true, "const": true, "enum": true, "var": true,
	"export": true, "default": true, "async": true,
	"the": true, "a": true, "an": true, "is": true, "in": true,
	"of": true, "to": true, "for": true, "and": true, "or": true,
}

