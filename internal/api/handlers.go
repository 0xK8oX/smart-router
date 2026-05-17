package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"gopkg.in/yaml.v3"
	"smart-router/internal/db"
	"smart-router/internal/health"
	"smart-router/internal/router"
	"smart-router/internal/types"
)

type Server struct {
	router   *router.Router
	health   *health.HealthTracker
	db       *db.DB
	adminKey string
}

func NewServer(r *router.Router, h *health.HealthTracker, d *db.DB, adminKey string) *Server {
	return &Server{
		router:   r,
		health:   h,
		db:       d,
		adminKey: adminKey,
	}
}

func (s *Server) RegisterRoutes(r *mux.Router) {
	r.HandleFunc("/v1/chat/completions", s.handleChatCompletions).Methods(http.MethodPost, http.MethodOptions)
	r.HandleFunc("/v1/plans", s.handleListPlans).Methods(http.MethodGet, http.MethodOptions)
	r.HandleFunc("/v1/plans/{slug}", s.handleGetPlan).Methods(http.MethodGet, http.MethodOptions)
	r.HandleFunc("/v1/plans/{slug}", s.handleUpdatePlan).Methods(http.MethodPut, http.MethodOptions)
	r.HandleFunc("/v1/plans/{slug}", s.handleDeletePlan).Methods(http.MethodDelete, http.MethodOptions)
	r.HandleFunc("/v1/health", s.handleHealth).Methods(http.MethodGet, http.MethodOptions)
	r.HandleFunc("/v1/health/activity", s.handleHealthActivity).Methods(http.MethodGet, http.MethodOptions)
	r.HandleFunc("/v1/stats", s.handleStats).Methods(http.MethodGet, http.MethodOptions)
	r.HandleFunc("/v1/stats/aggregated", s.handleStatsAggregated).Methods(http.MethodGet, http.MethodOptions)

	// CORS preflight
	r.Methods(http.MethodOptions).HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// CORS middleware
	r.Use(corsMiddleware)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Plan, X-Admin-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

func maskPlan(plan types.PlanConfig) types.PlanConfig {
	masked := types.PlanConfig{
		Providers: make([]types.ProviderConfig, len(plan.Providers)),
	}
	for i, p := range plan.Providers {
		p.APIKey = maskKey(p.APIKey)
		masked.Providers[i] = p
	}
	return masked
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("json encode error: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	body := make(map[string]interface{})
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	planSlug := r.Header.Get("X-Plan")
	if planSlug == "" {
		planSlug = "default"
	}

	// Check for auto- prefix in model field
	if model, ok := body["model"].(string); ok && strings.HasPrefix(model, "auto-") {
		planSlug = strings.TrimPrefix(model, "auto-")
		body["model"] = "auto"
	}

	isStreaming := false
	if stream, ok := body["stream"].(bool); ok {
		isStreaming = stream
	}

	resp, _, err := s.router.Route(planSlug, body, isStreaming)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("copy response body error: %v", err)
	}
}

func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	plans, err := s.db.ListPlans()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Mask keys
	for slug, plan := range plans {
		plans[slug] = maskPlan(plan)
	}

	writeJSON(w, http.StatusOK, plans)
}

func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	slug := mux.Vars(r)["slug"]
	plan, err := s.db.GetPlan(slug)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	if r.Header.Get("X-Admin-Key") != s.adminKey {
		masked := maskPlan(*plan)
		writeJSON(w, http.StatusOK, masked)
		return
	}

	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleUpdatePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Header.Get("X-Admin-Key") != s.adminKey {
		writeError(w, http.StatusForbidden, "invalid admin key")
		return
	}

	slug := mux.Vars(r)["slug"]
	var plan types.PlanConfig
	if err := json.NewDecoder(r.Body).Decode(&plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if err := s.db.SavePlan(slug, plan); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeletePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Header.Get("X-Admin-Key") != s.adminKey {
		writeError(w, http.StatusForbidden, "invalid admin key")
		return
	}

	slug := mux.Vars(r)["slug"]
	if err := s.db.DeletePlan(slug); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	planSlug := r.URL.Query().Get("plan")

	var providerNames []string
	if planSlug != "" {
		plan, err := s.db.GetPlan(planSlug)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		for _, p := range plan.Providers {
			providerNames = append(providerNames, p.Name)
		}
	}

	result := make(map[string]types.ProviderHealth)

	if len(providerNames) > 0 {
		for _, name := range providerNames {
			h, err := s.health.GetHealth(name)
			if err != nil {
				continue
			}
			result[name] = h
		}
	} else {
		// Return all providers' health
		// Since BadgerDB doesn't support listing keys easily without iteration,
		// we list all plans and collect their providers
		plans, err := s.db.ListPlans()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		seen := make(map[string]bool)
		for _, plan := range plans {
			for _, p := range plan.Providers {
				if seen[p.Name] {
					continue
				}
				seen[p.Name] = true
				h, err := s.health.GetHealth(p.Name)
				if err != nil {
					continue
				}
				result[p.Name] = h
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleHealthActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	plans, err := s.db.ListPlans()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	seen := make(map[string]bool)
	var activities []struct {
		Name           string `json:"name"`
		LastActivityAt int64  `json:"last_activity_at"`
		Status         string `json:"status"`
	}

	for _, plan := range plans {
		for _, p := range plan.Providers {
			if seen[p.Name] {
				continue
			}
			seen[p.Name] = true
			h, err := s.health.GetHealth(p.Name)
			if err != nil {
				continue
			}
			activities = append(activities, struct {
				Name           string `json:"name"`
				LastActivityAt int64  `json:"last_activity_at"`
				Status         string `json:"status"`
			}{
				Name:           p.Name,
				LastActivityAt: h.LastActivityAt,
				Status:         h.Status,
			})
		}
	}

	// Sort by last activity descending
	for i := 0; i < len(activities)-1; i++ {
		for j := i + 1; j < len(activities); j++ {
			if activities[j].LastActivityAt > activities[i].LastActivityAt {
				activities[i], activities[j] = activities[j], activities[i]
			}
		}
	}

	writeJSON(w, http.StatusOK, activities)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	plan := r.URL.Query().Get("plan")
	provider := r.URL.Query().Get("provider")
	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}

	stats, err := s.db.GetStats(plan, provider, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleStatsAggregated(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "provider"
	}

	stats, err := s.db.GetStats("", "", 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	aggregated := make(map[string]map[string]int64)
	for _, s := range stats {
		var key string
		switch groupBy {
		case "provider":
			key = s.Provider
		case "plan":
			key = s.Plan
		case "model":
			key = s.Model
		default:
			key = s.Provider
		}

		if _, ok := aggregated[key]; !ok {
			aggregated[key] = map[string]int64{
				"total":   0,
				"success": 0,
				"failure": 0,
			}
		}
		aggregated[key]["total"]++
		if s.Status == "success" {
			aggregated[key]["success"]++
		} else {
			aggregated[key]["failure"]++
		}
	}

	writeJSON(w, http.StatusOK, aggregated)
}


// SeedPlansFromFile loads plans from a YAML config file and saves them to the DB.
func SeedPlansFromFile(database *db.DB, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var cfg struct {
		Plans map[string]types.PlanConfig `yaml:"plans"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return err
	}

	for slug, plan := range cfg.Plans {
		if err := database.SavePlan(slug, plan); err != nil {
			return err
		}
	}
	return nil
}

// ensureDefaultPlan creates a default plan if none exists.
func ensureDefaultPlan(database *db.DB) error {
	_, err := database.GetPlan("default")
	if err == nil {
		return nil
	}
	// Create a minimal default plan
	defaultPlan := types.PlanConfig{
		Providers: []types.ProviderConfig{},
	}
	return database.SavePlan("default", defaultPlan)
}
