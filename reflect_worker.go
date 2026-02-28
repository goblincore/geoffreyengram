package engram

import (
	"context"
	"log"
	"time"
)

// startReflectionWorker runs a background goroutine that periodically
// triggers reflective synthesis for users with recent activity.
func (cm *Engram) startReflectionWorker(interval time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	cm.cancelReflect = cancel

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				cm.runReflectionCycle(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// runReflectionCycle finds users with stored memories and triggers synthesis.
func (cm *Engram) runReflectionCycle(ctx context.Context) {
	userIDs, err := cm.store.GetActiveUserIDs()
	if err != nil {
		log.Printf("[engram] Reflection cycle: get users failed: %v", err)
		return
	}

	for _, userID := range userIDs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		results, err := cm.Reflect(ctx, ReflectOptions{
			UserID:       userID,
			MemoryWindow: 50,
			MinMemories:  5,
		})
		if err != nil {
			log.Printf("[engram] Reflection for %s failed: %v", userID, err)
		} else if len(results) > 0 {
			log.Printf("[engram] Generated %d reflections for %s", len(results), userID)
		}
	}
}
