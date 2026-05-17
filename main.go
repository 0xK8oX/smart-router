package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"
	"smart-router/internal/api"
	"smart-router/internal/db"
	"smart-router/internal/health"
	"smart-router/internal/router"
)

func main() {
	port := os.Getenv("SMART_ROUTER_PORT")
	if port == "" {
		port = "8790"
	}
	host := os.Getenv("SMART_ROUTER_HOST")
	if host == "" {
		host = "0.0.0.0"
	}
	dbPath := os.Getenv("SMART_ROUTER_DB_PATH")
	if dbPath == "" {
		dbPath = "smart-router.db"
	}
	healthPath := os.Getenv("SMART_ROUTER_HEALTH_PATH")
	if healthPath == "" {
		healthPath = "health.db"
	}
	adminKey := os.Getenv("SMART_ROUTER_ADMIN_KEY")

	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ht, err := health.New(healthPath)
	if err != nil {
		log.Fatalf("open health tracker: %v", err)
	}
	defer ht.Close()

	r := router.New(ht, database)

	// Seed plans from config file on startup
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config/plans.yaml"
	}
	if err := api.SeedPlansFromFile(database, configPath); err != nil {
		log.Printf("warning: failed to seed plans: %v", err)
	}

	server := api.NewServer(r, ht, database, adminKey)

	muxRouter := mux.NewRouter()
	server.RegisterRoutes(muxRouter)

	// Logging middleware
	handler := loggingMiddleware(muxRouter)

	addr := host + ":" + port
	log.Printf("Smart Router listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}
