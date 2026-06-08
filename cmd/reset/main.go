// reset-health resets provider health state in BadgerDB.
// Usage: go run ./cmd/reset/main.go [provider_name]
// With no args, resets ALL unhealthy providers.
package main

import (
	"fmt"
	"log"
	"os"

	"smart-router/internal/health"
)

func main() {
	healthPath := os.Getenv("SMART_ROUTER_HEALTH_PATH")
	if healthPath == "" {
		healthPath = "./data/health"
	}

	ht, err := health.New(healthPath)
	if err != nil {
		log.Fatalf("open health tracker: %v", err)
	}
	defer ht.Close()

	if len(os.Args) > 1 {
		// Reset specific provider
		name := os.Args[1]
		if err := ht.ResetProvider(name); err != nil {
			log.Fatalf("reset %s: %v", name, err)
		}
		fmt.Printf("✅ %s health reset\n", name)
		return
	}

	// Reset all unhealthy providers
	all, err := ht.List()
	if err != nil {
		log.Fatalf("list health: %v", err)
	}

	var reset []string
	for name, h := range all {
		if h.Status == "unhealthy" {
			if err := ht.ResetProvider(name); err != nil {
				log.Printf("⚠️  failed to reset %s: %v", name, err)
				continue
			}
			reset = append(reset, name)
		}
	}

	if len(reset) == 0 {
		fmt.Println("No unhealthy providers to reset")
		return
	}
	fmt.Printf("Reset %d unhealthy provider(s): %v\n", len(reset), reset)
}
