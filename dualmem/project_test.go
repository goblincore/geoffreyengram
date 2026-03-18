package dualmem

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestProjectorDimensions(t *testing.T) {
	p := NewProjector(42, 768)

	if len(p.R128) != 128 {
		t.Errorf("R128 has %d rows, want 128", len(p.R128))
	}
	if len(p.R128[0]) != 768 {
		t.Errorf("R128 row has %d cols, want 768", len(p.R128[0]))
	}
	if len(p.R64) != 64 {
		t.Errorf("R64 has %d rows, want 64", len(p.R64))
	}
	if len(p.R64[0]) != 768 {
		t.Errorf("R64 row has %d cols, want 768", len(p.R64[0]))
	}
}

func TestProjectTo128(t *testing.T) {
	p := NewProjector(42, 768)
	vec := randomVec(768)

	projected := p.ProjectTo128(vec)
	if len(projected) != 128 {
		t.Fatalf("ProjectTo128 returned %d dims, want 128", len(projected))
	}

	// Verify non-zero output
	allZero := true
	for _, v := range projected {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("ProjectTo128 produced all-zero output")
	}
}

func TestProjectTo64(t *testing.T) {
	p := NewProjector(42, 768)
	vec := randomVec(768)

	projected := p.ProjectTo64(vec)
	if len(projected) != 64 {
		t.Fatalf("ProjectTo64 returned %d dims, want 64", len(projected))
	}
}

func TestProjectionDistancePreservation(t *testing.T) {
	// JL lemma: random projection should approximately preserve pairwise distances.
	// For 1000 random vectors projected 768→128, distortion should be bounded.
	p := NewProjector(42, 768)
	src := rand.New(rand.NewPCG(123, 456))

	const n = 100
	vecs := make([][]float32, n)
	for i := range vecs {
		vecs[i] = make([]float32, 768)
		for j := range vecs[i] {
			vecs[i][j] = float32(src.NormFloat64())
		}
	}

	// Project all
	projected := make([][]float32, n)
	for i, v := range vecs {
		projected[i] = p.ProjectTo128(v)
	}

	// Check pairwise cosine similarity preservation
	var maxDistortion float64
	pairs := 0
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			origSim := CosineSimilarity(vecs[i], vecs[j])
			projSim := CosineSimilarity(projected[i], projected[j])
			distortion := math.Abs(origSim - projSim)
			if distortion > maxDistortion {
				maxDistortion = distortion
			}
			pairs++
		}
	}

	// With 768→128 projection, max distortion should be reasonable
	// JL bound gives ~O(1/sqrt(128)) ≈ 0.088 expected, but with variance
	if maxDistortion > 0.5 {
		t.Errorf("max cosine distortion = %v, expected < 0.5 for %d pairs", maxDistortion, pairs)
	}
	t.Logf("JL validation: %d pairs, max cosine distortion: %.4f", pairs, maxDistortion)
}

func TestDeterministicProjection(t *testing.T) {
	// Same seed should produce identical matrices
	p1 := NewProjector(42, 768)
	p2 := NewProjector(42, 768)

	vec := randomVec(768)
	out1 := p1.ProjectTo128(vec)
	out2 := p2.ProjectTo128(vec)

	for i := range out1 {
		if out1[i] != out2[i] {
			t.Fatalf("deterministic projection mismatch at index %d: %v != %v", i, out1[i], out2[i])
		}
	}
}

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	if sim := CosineSimilarity(a, b); math.Abs(sim-1.0) > 1e-6 {
		t.Errorf("identical vectors: sim = %v, want 1.0", sim)
	}

	c := []float32{0, 1, 0}
	if sim := CosineSimilarity(a, c); math.Abs(sim) > 1e-6 {
		t.Errorf("orthogonal vectors: sim = %v, want 0.0", sim)
	}

	d := []float32{-1, 0, 0}
	if sim := CosineSimilarity(a, d); math.Abs(sim+1.0) > 1e-6 {
		t.Errorf("opposite vectors: sim = %v, want -1.0", sim)
	}

	// Empty/mismatched
	if sim := CosineSimilarity(nil, nil); sim != 0 {
		t.Errorf("nil vectors: sim = %v, want 0", sim)
	}
	if sim := CosineSimilarity(a, []float32{1, 2}); sim != 0 {
		t.Errorf("mismatched lengths: sim = %v, want 0", sim)
	}
}

func randomVec(dim int) []float32 {
	src := rand.New(rand.NewPCG(999, 111))
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(src.NormFloat64())
	}
	return v
}
