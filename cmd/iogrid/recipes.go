// cmd/iogrid/recipes.go implements the v0.9.4 §4 + §11 + EPIC #461
// task-recipe layer on the iogrid HTTP API (#502 Wave H4). Operators
// declare named, parametrized task templates once; consumers POST by
// recipe name with params instead of inline JSON.
//
// Naming note: the v0.9.3 internal/iogrid package already has a
// `Recipe` type that represents an OCI-image container template
// (signed JWS + agent-slug + resources + network policy). H4's
// TaskRecipe is ORTHOGONAL — a parametrized PROMPT template, not a
// container template. The two could compose later (a TaskRecipe's
// execution could reference an internal/iogrid.Recipe to pick its
// runtime), but for v0.9.4 they're independent surfaces.
//
// Wire shape:
//
//	POST   /api/v1/recipes           { ...TaskRecipe... }
//	                                  → 201 + persisted record
//	GET    /api/v1/recipes           → {"recipes":[...]}
//	GET    /api/v1/recipes/{name}    → TaskRecipe
//	DELETE /api/v1/recipes/{name}    → 204
//	POST   /v1/runners/recipe/{name} { params:{...}, credentials:[...] }
//	                                  → 202 + {"id":"<runner-id>"}
//	GET    /a2a/recipe/{name}/.well-known/agent-card.json
//	                                  → virtual Agent Card describing
//	                                    this recipe-as-agent
//
// Auth: every /api/v1/recipes/* + /v1/runners/recipe/* requires the
// configured bearer token (same gate as H2's POST /v1/runners).
// /a2a/recipe/{name}/.well-known/agent-card.json is PUBLIC (per the
// A2A v1.0 well-known discovery convention).
//
// Refs #502 V0.9.2-ARCHITECTURE.md §4 §11 #461.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"
)

// TaskRecipe is the H4 task-template wire shape. JSON field names
// are spec-frozen; do NOT rename without an ADR.
type TaskRecipe struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version,omitempty"`
	AgentSlug            string            `json:"agent_slug"`
	Description          string            `json:"description,omitempty"`
	PromptTemplate       string            `json:"prompt_template"`
	RequiredCredentials  []string          `json:"required_credentials,omitempty"`
	RequiredParams       []string          `json:"required_params,omitempty"`
	DefaultParams        map[string]string `json:"default_params,omitempty"`
	CreatedAt            time.Time         `json:"created_at,omitempty"`
	UpdatedAt            time.Time         `json:"updated_at,omitempty"`
}

// recipeStore is the in-memory recipe catalog. Sufficient for H4 —
// recipes survive iogrid-process lifetime. Persistence to sqlite /
// filesystem is a follow-up if cross-restart catalog survival
// becomes a requirement.
type recipeStore struct {
	mu      sync.RWMutex
	byName  map[string]*TaskRecipe
}

func newRecipeStore() *recipeStore {
	return &recipeStore{byName: map[string]*TaskRecipe{}}
}

// recipeNameRE is the allowlist for recipe names — path-safe + URL-
// safe + Agent-Card-discoverable. Same character class as DNS labels
// plus dot.
var recipeNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{0,62}$`)

// Save validates + stores the recipe. Returns an error on invalid
// fields; on success the stored record is returned with timestamps
// populated.
func (s *recipeStore) Save(r TaskRecipe) (*TaskRecipe, error) {
	if !recipeNameRE.MatchString(r.Name) {
		return nil, fmt.Errorf("name %q invalid: must match %s", r.Name, recipeNameRE)
	}
	if r.AgentSlug == "" {
		return nil, errors.New("agent_slug required")
	}
	if r.PromptTemplate == "" {
		return nil, errors.New("prompt_template required")
	}
	// Compile the template once at save time so syntax errors are
	// caught before any consumer tries to execute the recipe.
	if _, err := template.New(r.Name).Parse(r.PromptTemplate); err != nil {
		return nil, fmt.Errorf("prompt_template parse: %w", err)
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.byName[r.Name]
	if ok {
		r.CreatedAt = existing.CreatedAt
	} else {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	cp := r
	s.byName[r.Name] = &cp
	out := cp
	return &out, nil
}

func (s *recipeStore) Get(name string) (*TaskRecipe, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.byName[name]
	if !ok {
		return nil, false
	}
	cp := *r
	return &cp, true
}

func (s *recipeStore) List() []*TaskRecipe {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*TaskRecipe, 0, len(s.byName))
	for _, r := range s.byName {
		cp := *r
		out = append(out, &cp)
	}
	return out
}

func (s *recipeStore) Delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byName[name]; !ok {
		return false
	}
	delete(s.byName, name)
	return true
}

// ExpandPrompt renders the recipe's PromptTemplate against the
// supplied params, applying DefaultParams as fallbacks and rejecting
// when a RequiredParams entry is missing.
func (r *TaskRecipe) ExpandPrompt(params map[string]string) (string, error) {
	final := map[string]string{}
	for k, v := range r.DefaultParams {
		final[k] = v
	}
	for k, v := range params {
		final[k] = v
	}
	for _, req := range r.RequiredParams {
		if _, ok := final[req]; !ok {
			return "", fmt.Errorf("missing required param %q", req)
		}
	}
	tmpl, err := template.New(r.Name).Parse(r.PromptTemplate)
	if err != nil {
		return "", fmt.Errorf("template parse: %w", err)
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, final); err != nil {
		return "", fmt.Errorf("template execute: %w", err)
	}
	return sb.String(), nil
}

// virtualAgentCard returns the §12.1-equivalent well-known Agent
// Card representation of this recipe. Stable shape across versions
// since A2A clients consume it for discovery.
func (r *TaskRecipe) virtualAgentCard(selfBaseURL string) map[string]any {
	skills := []map[string]any{
		{
			"id":          r.Name,
			"name":        r.Name,
			"description": r.Description,
			"tags":        []string{"iogrid", "recipe", r.AgentSlug},
		},
	}
	card := map[string]any{
		"protocolVersion":    "1.0",
		"name":               r.Name,
		"description":        r.Description,
		"url":                strings.TrimRight(selfBaseURL, "/") + "/v1/runners/recipe/" + r.Name,
		"version":            valueOr(r.Version, "0.0.0"),
		"capabilities":       map[string]any{"streaming": false, "pushNotifications": false},
		"defaultInputModes":  []string{"text/plain"},
		"defaultOutputModes": []string{"text/plain"},
		"skills":             skills,
		"x-iogrid-recipe": map[string]any{
			"agent_slug":           r.AgentSlug,
			"required_credentials": r.RequiredCredentials,
			"required_params":      r.RequiredParams,
			"default_params":       r.DefaultParams,
		},
	}
	return card
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// ─── HTTP handlers ────────────────────────────────────────────────

func (s *server) handleRecipesRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "read body: "+err.Error())
			return
		}
		var recipe TaskRecipe
		if err := json.Unmarshal(body, &recipe); err != nil {
			writeJSONError(w, http.StatusBadRequest, "decode: "+err.Error())
			return
		}
		stored, err := s.recipes.Save(recipe)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(stored)
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"recipes": s.recipes.List()})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "POST or GET")
	}
}

func (s *server) handleRecipeByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/recipes/")
	if name == "" || strings.Contains(name, "/") {
		writeJSONError(w, http.StatusBadRequest, "recipe name required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		recipe, ok := s.recipes.Get(name)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "recipe not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(recipe)
	case http.MethodDelete:
		if !s.recipes.Delete(name) {
			writeJSONError(w, http.StatusNotFound, "recipe not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "GET or DELETE")
	}
}

// handleRecipeExecution is POST /v1/runners/recipe/{name}. Body:
// {params:{...}, credentials:[...]}. Expands recipe → reuses the
// existing H2 spawn flow.
func (s *server) handleRecipeExecution(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/v1/runners/recipe/")
	if name == "" || strings.Contains(name, "/") {
		writeJSONError(w, http.StatusBadRequest, "recipe name required")
		return
	}
	recipe, ok := s.recipes.Get(name)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "recipe not found: "+name)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var execReq struct {
		Params      map[string]string  `json:"params"`
		Credentials []map[string]any   `json:"credentials"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &execReq); err != nil {
			writeJSONError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
	}
	prompt, err := recipe.ExpandPrompt(execReq.Params)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "expand recipe: "+err.Error())
		return
	}
	// Reuse the H2 spawn primitive. Build a task body the runner
	// already understands ({prompt, credentials}).
	taskEnvelope := map[string]any{"prompt": prompt}
	if len(execReq.Credentials) > 0 {
		taskEnvelope["credentials"] = execReq.Credentials
	}
	taskBody, _ := json.Marshal(taskEnvelope)
	info, err := s.spawnRunner(taskBody)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "spawn: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":          info.ID,
		"recipe":      name,
		"prompt":      prompt, // for caller convenience; not a security leak
	})
}

// handleVirtualAgentCard is GET /a2a/recipe/{name}/.well-known/
// agent-card.json. Public; no auth required (A2A discovery
// convention).
func (s *server) handleVirtualAgentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	// Path shape: /a2a/recipe/{name}/.well-known/agent-card.json
	tail := strings.TrimPrefix(r.URL.Path, "/a2a/recipe/")
	const suffix = "/.well-known/agent-card.json"
	if !strings.HasSuffix(tail, suffix) {
		writeJSONError(w, http.StatusNotFound, "not a recipe agent-card path")
		return
	}
	name := strings.TrimSuffix(tail, suffix)
	if name == "" || strings.Contains(name, "/") {
		writeJSONError(w, http.StatusBadRequest, "recipe name invalid")
		return
	}
	recipe, ok := s.recipes.Get(name)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "recipe not found")
		return
	}
	selfURL := "http://" + r.Host
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		selfURL = proto + "://" + r.Host
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(recipe.virtualAgentCard(selfURL))
}
