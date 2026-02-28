package engram

import "testing"

func TestExtractBracketNames(t *testing.T) {
	e := &DefaultEntityExtractor{}
	entities := e.Extract("[PlayerOne]: hello there")
	found := false
	for _, ent := range entities {
		if ent.Text == "PlayerOne" && ent.Type == "person" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected person entity 'PlayerOne', got %v", entities)
	}
}

func TestExtractQuotedStrings(t *testing.T) {
	e := &DefaultEntityExtractor{}
	entities := e.Extract(`she ordered a "Nebula Fizz" at the bar`)
	found := false
	for _, ent := range entities {
		if ent.Text == "Nebula Fizz" && ent.Type == "topic" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected topic entity 'Nebula Fizz', got %v", entities)
	}
}

func TestExtractKnownEntities(t *testing.T) {
	e := &DefaultEntityExtractor{
		KnownEntities: []KnownEntity{
			{Text: "Aphex Twin", Type: "music_artist"},
			{Text: "Boards of Canada", Type: "music_artist"},
		},
	}
	entities := e.Extract("they were listening to aphex twin while coding")
	found := false
	for _, ent := range entities {
		if ent.Text == "Aphex Twin" && ent.Type == "music_artist" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected music_artist 'Aphex Twin', got %v", entities)
	}
}

func TestExtractCapitalizedPhrases(t *testing.T) {
	e := &DefaultEntityExtractor{}
	entities := e.Extract("they went to Harajuku Station last weekend")
	found := false
	for _, ent := range entities {
		if ent.Text == "Harajuku Station" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected topic 'Harajuku Station', got %v", entities)
	}
}

func TestExtractDeduplication(t *testing.T) {
	e := &DefaultEntityExtractor{}
	entities := e.Extract(`[Alex]: hello | [Alex]: goodbye`)
	count := 0
	for _, ent := range entities {
		if ent.Text == "Alex" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 deduplicated entity, got %d", count)
	}
}

func TestExtractFiltersShortStrings(t *testing.T) {
	e := &DefaultEntityExtractor{}
	entities := e.Extract(`"x" is not a real entity`)
	for _, ent := range entities {
		if ent.Text == "x" {
			t.Errorf("single-char strings should be filtered out")
		}
	}
}

func TestExtractNoKnownEntitiesDoesNotPanic(t *testing.T) {
	e := &DefaultEntityExtractor{}
	entities := e.Extract("just a normal sentence with no entities")
	// Should not panic, may return empty
	_ = entities
}

func TestExtractCommonPhrasesFiltered(t *testing.T) {
	e := &DefaultEntityExtractor{}
	entities := e.Extract("I Am sure about this. You Are welcome.")
	for _, ent := range entities {
		if ent.Text == "I Am" || ent.Text == "You Are" {
			t.Errorf("common phrase '%s' should be filtered", ent.Text)
		}
	}
}
