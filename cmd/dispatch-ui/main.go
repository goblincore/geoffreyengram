package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	home, _ := os.UserHomeDir()
	dispatchDir := filepath.Join(home, ".claude", "dispatch")
	confPath := filepath.Join(dispatchDir, "dispatch.conf")
	envPath := filepath.Join(dispatchDir, ".env")

	cfg, err := loadDispatchConfig(confPath, envPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if err := os.MkdirAll(cfg.PlansDir, 0755); err != nil {
		log.Fatalf("create plans dir: %v", err)
	}
	if err := os.MkdirAll(cfg.ReportsDir, 0755); err != nil {
		log.Fatalf("create reports dir: %v", err)
	}

	eng := NewEngine(cfg)
	srv := NewServer(eng)

	go pollQueue(eng)

	fmt.Printf("Dispatch UI running at http://%s\n", cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, srv))
}

// pollQueue continuously checks for pending plans whose dependencies are met
// and executes up to MaxConcurrent tasks in parallel.
func pollQueue(eng *Engine) {
	for {
		time.Sleep(5 * time.Second)

		// Check available slots
		eng.mu.Lock()
		slots := eng.Config.MaxConcurrent - len(eng.running)
		// Snapshot running names to avoid launching duplicates
		runningNames := make(map[string]bool, len(eng.running))
		for name := range eng.running {
			runningNames[name] = true
		}
		eng.mu.Unlock()

		if slots <= 0 {
			continue
		}

		plans, err := eng.ListPlans()
		if err != nil {
			log.Printf("pollQueue: list plans: %v", err)
			continue
		}

		// Find eligible pending plans (deps met, not already running)
		var eligible []*Plan
		for _, p := range plans {
			if p.Status == "pending" && !runningNames[p.Name] && eng.dependenciesMet(p) {
				eligible = append(eligible, p)
				if len(eligible) >= slots {
					break
				}
			}
		}

		// Launch all eligible plans
		for _, plan := range eligible {
			go func(p *Plan) {
				if err := eng.ExecutePlan(context.Background(), p); err != nil {
					log.Printf("execute plan %s: %v", p.Name, err)
				}
			}(plan)
		}
	}
}
