package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gorilla/mux"
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

	r := mux.NewRouter()

	addr := host + ":" + port
	log.Printf("Smart Router listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, r))
}
