package engram

import (
	"context"
	"fmt"
	"log"
)

// Reflection represents a synthesized observation generated from a set of memories.
type Reflection struct {
	Content  string   // The observation/thought text
	Salience float64  // How significant this observation is (0-1)
	Entities []Entity // Entities mentioned in the reflection
}

// ReflectionProvider generates reflective observations from a set of memories.
// The characterContext is an optional prompt fragment describing the character's
// personality, perspective, or role — it shapes how reflections are generated.
type ReflectionProvider interface {
	Reflect(ctx context.Context, memories []Memory, characterContext string) ([]Reflection, error)
}

// ReflectOptions controls how reflection is triggered.
type ReflectOptions struct {
	UserID           string
	CharacterContext string   // Character personality/perspective prompt fragment
	MemoryWindow     int      // How many recent memories to consider (default: 50)
	Sectors          []Sector // Which sectors to draw from (default: all)
	MinMemories      int      // Minimum memories needed before reflecting (default: 5)
}

// Reflect triggers reflective synthesis for a user.
// It loads recent memories, passes them to the ReflectionProvider, and stores
// the resulting observations as high-salience reflective memories.
// Returns the newly created reflective memories.
func (cm *Engram) Reflect(ctx context.Context, opts ReflectOptions) ([]Memory, error) {
	if cm.reflector == nil {
		return nil, fmt.Errorf("engram: no ReflectionProvider configured")
	}

	// Apply defaults
	if opts.MemoryWindow <= 0 {
		opts.MemoryWindow = 50
	}
	if opts.MinMemories <= 0 {
		opts.MinMemories = 5
	}

	// 1. Load recent memories
	recentMemories, err := cm.store.GetRecentMemories(opts.UserID, opts.MemoryWindow, opts.Sectors)
	if err != nil {
		return nil, fmt.Errorf("engram: load recent memories: %w", err)
	}
	if len(recentMemories) < opts.MinMemories {
		return nil, nil // not enough memories to reflect on
	}

	// 2. Filter out existing reflections (don't reflect on reflections)
	var inputMemories []Memory
	for _, m := range recentMemories {
		if m.Sector != SectorReflective {
			inputMemories = append(inputMemories, m)
		}
	}
	if len(inputMemories) < opts.MinMemories {
		return nil, nil
	}

	// 3. Call the provider
	reflections, err := cm.reflector.Reflect(ctx, inputMemories, opts.CharacterContext)
	if err != nil {
		return nil, fmt.Errorf("engram: reflection provider: %w", err)
	}
	if len(reflections) == 0 {
		return nil, nil
	}

	// 4. Deduplicate against existing reflective memories
	reflections = cm.deduplicateReflections(ctx, opts.UserID, reflections)
	if len(reflections) == 0 {
		return nil, nil
	}

	// 5. Store each reflection as a new Memory
	var stored []Memory
	for _, ref := range reflections {
		// Clamp salience
		salience := ref.Salience
		if salience <= 0 {
			salience = 0.7
		}
		if salience > 1.0 {
			salience = 1.0
		}

		mem := Memory{
			Content:  ref.Content,
			Sector:   SectorReflective,
			Salience: salience,
			UserID:   opts.UserID,
			Summary:  truncateSummary(ref.Content, 200),
		}

		memID, err := cm.store.InsertMemory(mem)
		if err != nil {
			log.Printf("[engram] Store reflection failed: %v", err)
			continue
		}
		mem.ID = memID

		// Embed the reflection for future similarity search
		if cm.embedder != nil {
			vec, err := cm.embedder.Embed(ctx, ref.Content, "RETRIEVAL_DOCUMENT")
			if err == nil && vec != nil {
				cm.store.InsertVector(memID, SectorReflective, vec)
			}
		}

		// Create waypoint associations for entities in the reflection
		for _, entity := range ref.Entities {
			wpID, err := cm.store.UpsertWaypoint(entity.Text, entity.Type)
			if err == nil {
				cm.store.InsertAssociation(memID, wpID, 0.7) // higher weight for reflective associations
			}
		}

		stored = append(stored, mem)
	}

	if len(stored) > 0 {
		log.Printf("[engram] Generated %d reflections for %s", len(stored), opts.UserID)
	}

	return stored, nil
}

// deduplicateReflections checks if similar reflections already exist for this user.
// Uses embedding similarity to avoid storing near-duplicate observations.
func (cm *Engram) deduplicateReflections(ctx context.Context, userID string, reflections []Reflection) []Reflection {
	if cm.embedder == nil {
		return reflections // can't deduplicate without embeddings
	}

	// Load existing reflective memories with vectors
	existingWithVecs, err := cm.store.GetMemoriesWithVectors(userID)
	if err != nil {
		return reflections
	}

	// Filter to only reflective memories with vectors
	var reflectiveVecs []memoryWithVector
	for _, mwv := range existingWithVecs {
		if mwv.Sector == SectorReflective && mwv.Vector != nil {
			reflectiveVecs = append(reflectiveVecs, mwv)
		}
	}

	if len(reflectiveVecs) == 0 {
		return reflections
	}

	const duplicateThreshold = 0.85

	var unique []Reflection
	for _, ref := range reflections {
		refVec, err := cm.embedder.Embed(ctx, ref.Content, "RETRIEVAL_DOCUMENT")
		if err != nil {
			unique = append(unique, ref) // keep if we can't check
			continue
		}

		isDuplicate := false
		for _, ev := range reflectiveVecs {
			if CosineSimilarity(refVec, ev.Vector) > duplicateThreshold {
				isDuplicate = true
				break
			}
		}

		if !isDuplicate {
			unique = append(unique, ref)
		}
	}

	return unique
}
