package engram

import "testing"

func TestHeuristicClassifyEpisodic(t *testing.T) {
	c := NewHeuristicClassifier("")
	sector := c.Classify("I remember when they visited last time and came back later")
	if sector != SectorEpisodic {
		t.Errorf("expected episodic, got %s", sector)
	}
}

func TestHeuristicClassifySemantic(t *testing.T) {
	c := NewHeuristicClassifier("")
	sector := c.Classify("Alex likes jazz and prefers vinyl records, usually listens to old albums")
	if sector != SectorSemantic {
		t.Errorf("expected semantic, got %s", sector)
	}
}

func TestHeuristicClassifyEmotional(t *testing.T) {
	c := NewHeuristicClassifier("")
	sector := c.Classify("They seemed happy and excited, really grateful for the warm welcome")
	if sector != SectorEmotional {
		t.Errorf("expected emotional, got %s", sector)
	}
}

func TestHeuristicClassifyProcedural(t *testing.T) {
	c := NewHeuristicClassifier("")
	sector := c.Classify("They know how to do it using a specific technique and method")
	if sector != SectorProcedural {
		t.Errorf("expected procedural, got %s", sector)
	}
}

func TestHeuristicClassifyReflective(t *testing.T) {
	c := NewHeuristicClassifier("")
	sector := c.Classify("I notice that they tend to often consistently do this every time")
	if sector != SectorReflective {
		t.Errorf("expected reflective, got %s", sector)
	}
}

func TestHeuristicClassifyAmbiguousDefaultsSemantic(t *testing.T) {
	c := NewHeuristicClassifier("")
	sector := c.Classify("hello world")
	if sector != SectorSemantic {
		t.Errorf("ambiguous content should default to semantic, got %s", sector)
	}
}

func TestHeuristicClassifyNoGeminiFallbackWithoutKey(t *testing.T) {
	c := NewHeuristicClassifier("")
	// Should not panic or error without API key, just use heuristic
	sector := c.Classify("something completely ambiguous xyz")
	if sector != SectorSemantic {
		t.Errorf("without API key, ambiguous should default to semantic, got %s", sector)
	}
}
