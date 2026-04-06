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
// and executes the highest-priority one. It skips if a task is already running.
func pollQueue(eng *Engine) {
	for {
		time.Sleep(5 * time.Second)

		// Skip if already running
		eng.mu.Lock()
		isRunning := eng.running != nil
		eng.mu.Unlock()
		if isRunning {
			continue
		}

		plans, err := eng.ListPlans()
		if err != nil {
			log.Printf("pollQueue: list plans: %v", err)
			continue
		}

		// ListPlans returns running first, then pending by descending priority.
		// Find the first pending plan with dependencies met.
		var next *Plan
		for _, p := range plans {
			if p.Status == "pending" && eng.dependenciesMet(p) {
				next = p
				break
			}
		}

		if next == nil {
			continue
		}

		go func(plan *Plan) {
			if err := eng.ExecutePlan(context.Background(), plan); err != nil {
				log.Printf("execute plan %s: %v", plan.Name, err)
			}
		}(next)
	}
}
