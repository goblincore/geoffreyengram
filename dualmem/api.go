package dualmem

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Handler returns an http.Handler for the DualMem HTTP API.
//
// Routes:
//
//	POST /v1/memory/add       — add a memory
//	POST /v1/memory/search    — dual-path search
//	POST /v1/memory/context   — AssembleContext (token-budget-aware)
//	GET  /v1/memory/profile/:userID — get user profile sketch
//	POST /v1/memory/promote   — pin a memory to Detail Path
//	POST /v1/memory/demote    — demote a memory to Sketch Path
func Handler(engine *Engine) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/memory/add", handleAdd(engine))
	mux.HandleFunc("POST /v1/memory/search", handleSearch(engine))
	mux.HandleFunc("POST /v1/memory/context", handleContext(engine))
	mux.HandleFunc("GET /v1/memory/profile/", handleProfile(engine))
	mux.HandleFunc("POST /v1/memory/promote", handlePromote(engine))
	mux.HandleFunc("POST /v1/memory/demote", handleDemote(engine))

	return mux
}

// --- Request/Response types ---

type addRequest struct {
	UserID    string      `json:"user_id"`
	Input     MemoryInput `json:"input"`
}

type searchRequest struct {
	UserID string     `json:"user_id"`
	Query  string     `json:"query"`
	Opts   SearchOpts `json:"opts"`
}

type contextRequest struct {
	UserID      string `json:"user_id"`
	Query       string `json:"query"`
	TokenBudget int    `json:"token_budget"`
}

type promoteRequest struct {
	UserID   string `json:"user_id"`
	MemoryID string `json:"memory_id"`
}

type demoteRequest struct {
	UserID   string `json:"user_id"`
	MemoryID string `json:"memory_id"`
}

// --- Handlers ---

func handleAdd(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req addRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.UserID == "" {
			http.Error(w, "user_id is required", http.StatusBadRequest)
			return
		}

		if err := e.AddWithOptions(r.Context(), req.Input, req.UserID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func handleSearch(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req searchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.UserID == "" || req.Query == "" {
			http.Error(w, "user_id and query are required", http.StatusBadRequest)
			return
		}

		result, err := e.DualSearch(r.Context(), req.UserID, req.Query, req.Opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

func handleContext(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req contextRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.UserID == "" || req.Query == "" {
			http.Error(w, "user_id and query are required", http.StatusBadRequest)
			return
		}

		block, err := e.AssembleContext(r.Context(), req.UserID, req.Query, req.TokenBudget)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(block)
	}
}

func handleProfile(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract userID from path: /v1/memory/profile/{userID}
		path := r.URL.Path
		userID := strings.TrimPrefix(path, "/v1/memory/profile/")
		if userID == "" {
			http.Error(w, "user_id is required in path", http.StatusBadRequest)
			return
		}

		profile, err := e.GetProfile(r.Context(), userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if profile == nil {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(profile)
	}
}

func handlePromote(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req promoteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := e.PromoteToDetail(r.Context(), req.UserID, req.MemoryID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func handleDemote(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req demoteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := e.DemoteFromDetail(r.Context(), req.UserID, req.MemoryID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}
