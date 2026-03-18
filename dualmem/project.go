package dualmem

import (
	"math"
	"math/rand/v2"
)

// Projector holds fixed random projection matrices for dimensionality reduction.
// Based on the Johnson-Lindenstrauss lemma: sparse random projection
// with entries in {-1, +1} scaled by 1/sqrt(d_out).
type Projector struct {
	Seed  int64
	R128  [][]float32 // 128 x inputDim for arc embeddings
	R64   [][]float32 // 64 x inputDim for profile embeddings
	InDim int
}

// NewProjector creates projection matrices from a seed.
// inputDim is the full embedding dimension (e.g. 768).
func NewProjector(seed int64, inputDim int) *Projector {
	p := &Projector{
		Seed:  seed,
		InDim: inputDim,
		R128:  makeProjectionMatrix(seed, 128, inputDim),
		R64:   makeProjectionMatrix(seed+1, 64, inputDim),
	}
	return p
}

// ProjectTo128 projects a full-dimensional embedding to 128d.
func (p *Projector) ProjectTo128(vec []float32) []float32 {
	return projectVector(p.R128, vec)
}

// ProjectTo64 projects a full-dimensional embedding to 64d.
func (p *Projector) ProjectTo64(vec []float32) []float32 {
	return projectVector(p.R64, vec)
}

// makeProjectionMatrix generates a sparse random projection matrix.
// Each entry is +1/sqrt(d_out) or -1/sqrt(d_out) with equal probability.
func makeProjectionMatrix(seed int64, outDim, inDim int) [][]float32 {
	src := rand.New(rand.NewPCG(uint64(seed), uint64(seed>>1|0x1)))
	scale := float32(1.0 / math.Sqrt(float64(outDim)))

	matrix := make([][]float32, outDim)
	for i := range matrix {
		row := make([]float32, inDim)
		for j := range row {
			if src.IntN(2) == 0 {
				row[j] = scale
			} else {
				row[j] = -scale
			}
		}
		matrix[i] = row
	}
	return matrix
}

// projectVector computes R @ vec (matrix-vector product).
func projectVector(matrix [][]float32, vec []float32) []float32 {
	out := make([]float32, len(matrix))
	for i, row := range matrix {
		var sum float32
		for j, v := range row {
			if j < len(vec) {
				sum += v * vec[j]
			}
		}
		out[i] = sum
	}
	return out
}

// CosineSimilarity computes cosine similarity between two float32 vectors.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
