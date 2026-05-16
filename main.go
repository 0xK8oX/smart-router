package main

import (
	"log"
	"net/http"
	"os"

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

	server := api.NewServer(r, ht, database, adminKey)

	muxRouter := mux.NewRouter()
	server.RegisterRoutes(muxRouter)

	addr := host + ":" + port
	log.Printf("Smart Router listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, muxRouter))
}
