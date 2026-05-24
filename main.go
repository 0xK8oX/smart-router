package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"
	"encoding/base64"

	"smart-router/internal/alerts"
	"smart-router/internal/api"
	"smart-router/internal/auth"
	"smart-router/internal/db"
	"smart-router/internal/health"
	"smart-router/internal/router"
	"smart-router/internal/types"
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

	// Load provider API key encryption key if available
	if encKeyB64 := os.Getenv("KEY_ENCRYPTION_KEY"); encKeyB64 != "" {
		encKey, err := base64.StdEncoding.DecodeString(encKeyB64)
		if err != nil {
			log.Fatalf("decode KEY_ENCRYPTION_KEY: %v", err)
		}
		if len(encKey) != 32 {
			log.Fatalf("KEY_ENCRYPTION_KEY must decode to 32 bytes (got %d)", len(encKey))
		}
		database.WithEncryptionKey(encKey)
		log.Printf("Provider API key encryption enabled")

		// Migrate any existing plaintext plans to encrypted
		plans, err := database.ListPlans()
		if err != nil {
			log.Printf("warning: failed to list plans for encryption migration: %v", err)
		} else {
			for slug, plan := range plans {
				if err := database.SavePlan(slug, plan); err != nil {
					log.Printf("warning: failed to encrypt plan %s: %v", slug, err)
				}
			}
			log.Printf("Encrypted %d existing plan(s)", len(plans))
		}
	}

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
	r.InvalidateAllPlanCache()

	rateLimiter := auth.NewRateLimiter()
	authHandler := api.NewAuth(database, rateLimiter)
	server := api.NewServer(r, ht, database, authHandler, adminKey)

	// Bootstrap a default API key if none exist
	if count, err := database.CountAPIKeys(); err != nil {
		log.Printf("warning: failed to count api keys: %v", err)
	} else if count == 0 {
		log.Printf("WARNING: Database has 0 API keys. If this is unexpected, restore from data/backups/")
		defaultKey := types.APIKey{
			Key:       auth.GenerateAPIKey(),
			Name:      "default",
			Plans:     []string{},
			Models:    []string{},
			CreatedAt: time.Now().Unix(),
		}
		if err := database.CreateAPIKey(defaultKey); err != nil {
			log.Printf("warning: failed to create default api key: %v", err)
		} else {
			log.Printf("=================================================")
			log.Printf("DEFAULT API KEY CREATED: %s", defaultKey.Key)
			log.Printf("Use this key with Authorization: Bearer %s", defaultKey.Key)
			log.Printf("=================================================")
		}
	}

	// Start Telegram bot for commands
	alerts.StartBot(database, ht)

	muxRouter := mux.NewRouter()
	// Auth middleware MUST be applied BEFORE route registration
	// so that gorilla/mux captures it on every route.
	muxRouter.Use(authHandler.Middleware)
	server.RegisterRoutes(muxRouter)
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
