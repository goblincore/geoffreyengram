package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

//go:embed static
var staticFiles embed.FS

// APITask is the JSON representation of a plan task.
type APITask struct {
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Project      string   `json:"project"`
	Model        string   `json:"model"`
	Priority     int      `json:"priority"`
	MaxRuntime   string   `json:"max_runtime"`
	Branch       string   `json:"branch"`
	Created      string   `json:"created"`
	StartedAt    string   `json:"started_at,omitempty"`
	FinishedAt   string   `json:"finished_at,omitempty"`
	ExitCode     int      `json:"exit_code,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"`
	AllowedTools string   `json:"allowed_tools"`
	CreatedBy    string   `json:"created_by,omitempty"`
}

// Server holds the Engine and HTTP mux.
type Server struct {
	eng *Engine
	mux *http.ServeMux
}

// NewServer creates a Server, registers all routes, and returns it.
func NewServer(eng *Engine) *Server {
	s := &Server{
		eng: eng,
		mux: http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

// ServeHTTP implements http.Handler by delegating to the mux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/tasks", s.handleListTasks)
	s.mux.HandleFunc("GET /api/tasks/{name}", s.handleGetTask)
	s.mux.HandleFunc("POST /api/tasks", s.handleCreateTask)
	s.mux.HandleFunc("PUT /api/tasks/{name}", s.handleUpdateTask)
	s.mux.HandleFunc("DELETE /api/tasks/{name}", s.handleDeleteTask)
	s.mux.HandleFunc("POST /api/tasks/{name}/cancel", s.handleCancelTask)
	s.mux.HandleFunc("POST /api/tasks/{name}/retry", s.handleRetryTask)
	s.mux.HandleFunc("GET /api/tasks/{name}/report", s.handleGetReport)
	s.mux.HandleFunc("GET /api/tasks/{name}/plan", s.handleGetPlan)
	s.mux.HandleFunc("GET /api/tasks/{name}/logs", s.handleSSELogs)
	s.mux.HandleFunc("GET /api/config", s.handleGetConfig)
	s.mux.HandleFunc("POST /api/generate-plan", s.handleGeneratePlan)

	// Static files
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("embed static: " + err.Error())
	}
	s.mux.Handle("GET /", http.FileServer(http.FS(sub)))
}

// planToAPI converts a Plan to an APITask.
func planToAPI(p *Plan) APITask {
	return APITask{
		Name:         p.Name,
		Title:        p.Title,
		Status:       p.Status,
		Project:      p.Project,
		Model:        p.Model,
		Priority:     p.Priority,
		MaxRuntime:   p.MaxRuntime,
		Branch:       p.Branch,
		Created:      p.Created,
		StartedAt:    p.StartedAt,
		FinishedAt:   p.FinishedAt,
		ExitCode:     p.ExitCode,
		DependsOn:    p.DependsOn,
		AllowedTools: p.AllowedTools,
		CreatedBy:    p.CreatedBy,
	}
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleListTasks returns all tasks as a JSON array.
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	plans, err := s.eng.ListPlans()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tasks := make([]APITask, 0, len(plans))
	for _, p := range plans {
		tasks = append(tasks, planToAPI(p))
	}
	writeJSON(w, http.StatusOK, tasks)
}

// handleGetTask returns a single task by name.
func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.eng.Config.PlansDir, name+".md")
	plan, err := parsePlanFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "task not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, planToAPI(plan))
}

// createTaskRequest is the JSON body for POST /api/tasks.
type createTaskRequest struct {
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Project      string   `json:"project"`
	Model        string   `json:"model"`
	APIKeyEnv    string   `json:"api_key_env"`
	BaseURL      string   `json:"base_url"`
	Priority     int      `json:"priority"`
	MaxRuntime   string   `json:"max_runtime"`
	AllowedTools string   `json:"allowed_tools"`
	DependsOn    []string `json:"depends_on"`
	Body         string   `json:"body"`
	CreatedBy    string   `json:"created_by"`
	Branch       string   `json:"branch"`
}

// handleCreateTask creates a new task from a JSON body.
func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	// Apply defaults
	if req.Priority == 0 {
		req.Priority = 3
	}
	if req.MaxRuntime == "" {
		req.MaxRuntime = "25m"
	}
	if req.AllowedTools == "" {
		req.AllowedTools = "Edit,Write,Bash,Read,Glob,Grep"
	}

	plan := &Plan{
		Name:         req.Name,
		Path:         filepath.Join(s.eng.Config.PlansDir, req.Name+".md"),
		Title:        req.Title,
		Status:       "pending",
		Project:      req.Project,
		Model:        req.Model,
		APIKeyEnv:    req.APIKeyEnv,
		BaseURL:      req.BaseURL,
		Branch:       req.Branch,
		Priority:     req.Priority,
		MaxRuntime:   req.MaxRuntime,
		AllowedTools: req.AllowedTools,
		DependsOn:    req.DependsOn,
		Body:         req.Body,
		CreatedBy:    req.CreatedBy,
		Created:      time.Now().UTC().Format(time.RFC3339),
	}

	if err := writePlanFile(plan); err != nil {
		writeError(w, http.StatusInternalServerError, "write plan: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, planToAPI(plan))
}

// updateTaskRequest is the JSON body for PUT /api/tasks/{name}.
type updateTaskRequest struct {
	Title        *string  `json:"title"`
	Status       *string  `json:"status"`
	Project      *string  `json:"project"`
	Model        *string  `json:"model"`
	Priority     *int     `json:"priority"`
	MaxRuntime   *string  `json:"max_runtime"`
	Branch       *string  `json:"branch"`
	AllowedTools *string  `json:"allowed_tools"`
	DependsOn    []string `json:"depends_on"`
	Body         *string  `json:"body"`
}

// handleUpdateTask updates fields on an existing task.
func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.eng.Config.PlansDir, name+".md")

	plan, err := parsePlanFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "task not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	var req updateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Title != nil {
		plan.Title = *req.Title
	}
	if req.Status != nil {
		plan.Status = *req.Status
	}
	if req.Project != nil {
		plan.Project = *req.Project
	}
	if req.Model != nil {
		plan.Model = *req.Model
	}
	if req.Priority != nil {
		plan.Priority = *req.Priority
	}
	if req.MaxRuntime != nil {
		plan.MaxRuntime = *req.MaxRuntime
	}
	if req.Branch != nil {
		plan.Branch = *req.Branch
	}
	if req.AllowedTools != nil {
		plan.AllowedTools = *req.AllowedTools
	}
	if req.DependsOn != nil {
		plan.DependsOn = req.DependsOn
	}
	if req.Body != nil {
		plan.Body = *req.Body
	}

	if err := writePlanFile(plan); err != nil {
		writeError(w, http.StatusInternalServerError, "write plan: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, planToAPI(plan))
}

// handleDeleteTask removes a task's plan file.
func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.eng.Config.PlansDir, name+".md")

	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "task not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleCancelTask cancels the running task (only if it's the named task).
func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	s.eng.mu.Lock()
	running := s.eng.running
	s.eng.mu.Unlock()

	if running == nil || running.Plan.Name != name {
		writeError(w, http.StatusConflict, "task is not currently running")
		return
	}

	s.eng.CancelRunning()
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// handleRetryTask resets a failed/done task back to pending.
func (s *Server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.eng.Config.PlansDir, name+".md")

	plan, err := parsePlanFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "task not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	plan.Status = "pending"
	plan.StartedAt = ""
	plan.FinishedAt = ""
	plan.ExitCode = 0

	if err := writePlanFile(plan); err != nil {
		writeError(w, http.StatusInternalServerError, "write plan: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, planToAPI(plan))
}

// handleGetReport returns the report file content as plain text.
func (s *Server) handleGetReport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.eng.Config.PlansDir, name+".md")

	plan, err := parsePlanFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "task not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if plan.Report == "" {
		writeError(w, http.StatusNotFound, "no report available")
		return
	}

	data, err := os.ReadFile(plan.Report)
	if err != nil {
		writeError(w, http.StatusNotFound, "report file not found")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleGetPlan returns the plan body as plain text.
func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.eng.Config.PlansDir, name+".md")

	plan, err := parsePlanFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "task not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, plan.Body)
}

// handleSSELogs streams task logs via Server-Sent Events.
func (s *Server) handleSSELogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Check for live log buffer
	s.eng.mu.Lock()
	lb, hasBuffer := s.eng.logs[name]
	s.eng.mu.Unlock()

	if hasBuffer {
		// Live streaming from LogBuffer
		ch, unsub := lb.Subscribe()
		defer unsub()

		for {
			select {
			case <-r.Context().Done():
				return
			case line, open := <-ch:
				if !open {
					fmt.Fprintf(w, "event: done\n\n")
					flusher.Flush()
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", line)
				flusher.Flush()
			}
		}
	}

	// No live buffer — try reading the report file
	path := filepath.Join(s.eng.Config.PlansDir, name+".md")
	plan, err := parsePlanFile(path)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: task not found\n\n")
		flusher.Flush()
		return
	}

	if plan.Report != "" {
		f, err := os.Open(plan.Report)
		if err == nil {
			defer f.Close()
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				fmt.Fprintf(w, "data: %s\n\n", scanner.Text())
				flusher.Flush()
			}
		}
	}

	fmt.Fprintf(w, "event: done\n\n")
	flusher.Flush()
}

// configResponse is the JSON shape for GET /api/config.
type configResponse struct {
	DefaultModel  string   `json:"default_model"`
	PlanningModel string   `json:"planning_model"`
	ListenAddr    string   `json:"listen_addr"`
	Projects      []string `json:"projects"`
	Models        []string `json:"models"`
}

// handleGetConfig returns dispatch configuration info.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	plans, _ := s.eng.ListPlans()

	// Collect unique project paths
	seen := make(map[string]bool)
	var projects []string
	for _, p := range plans {
		if p.Project != "" && !seen[p.Project] {
			seen[p.Project] = true
			projects = append(projects, p.Project)
		}
	}
	if projects == nil {
		projects = []string{}
	}

	resp := configResponse{
		DefaultModel:  s.eng.Config.DefaultModel,
		PlanningModel: s.eng.Config.PlanningModel,
		ListenAddr:    s.eng.Config.ListenAddr,
		Projects:      projects,
		Models:        []string{"glm-5.1", "sonnet-4.6", "claude-opus-4-6"},
	}

	writeJSON(w, http.StatusOK, resp)
}

// generatePlanRequest is the JSON body for POST /api/generate-plan.
type generatePlanRequest struct {
	Description string `json:"description"`
	Project     string `json:"project"`
}

// handleGeneratePlan calls the planning model to expand a description into a
// full plan body and returns it as JSON.
func (s *Server) handleGeneratePlan(w http.ResponseWriter, r *http.Request) {
	var req generatePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Description == "" {
		writeError(w, http.StatusBadRequest, "description is required")
		return
	}
	if req.Project == "" {
		writeError(w, http.StatusBadRequest, "project is required")
		return
	}

	body, err := s.eng.GeneratePlan(r.Context(), req.Description, req.Project)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generate plan: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"body": body})
}
