// cmd/iogrid/p0_502_recipes_test.go pins the v0.9.4 §4 + §11
// task-recipe layer (#502 Wave H4). Asserts:
//
//   - Recipe CRUD round-trip via REST: POST /api/v1/recipes →
//     GET /api/v1/recipes → GET /api/v1/recipes/{name} → DELETE.
//   - Template expansion: prompt_template renders with default_params
//     + per-call params; missing required_params → 400.
//   - Recipe execution: POST /v1/runners/recipe/{name} expands the
//     prompt + spawns the same H2 runner flow + returns runner-id
//     + a recipe-named GET state path that eventually flips to
//     completed; result envelope contains the rendered prompt as
//     the input message.
//   - Virtual Agent Card: GET /a2a/recipe/{name}/.well-known/agent-
//     card.json returns the §12.1-shape card with x-iogrid-recipe
//     extension carrying required_credentials / required_params.
//   - Auth: same bearer gate as H2 for the CRUD + execution paths;
//     virtual Agent Card is PUBLIC.
//   - Negative-space: bad template at save → 400; invalid name → 400;
//     unknown recipe → 404; missing required param at execution → 400.
//
// Refs #502 V0.9.2-ARCHITECTURE.md §4 §11 #461.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func sampleRecipe() TaskRecipe {
	return TaskRecipe{
		Name:        "echo-prompt",
		Version:     "1.0.0",
		AgentSlug:   "claude-code",
		Description: "Echo the supplied subject back. Used for live-walk smoke tests.",
		PromptTemplate: "Please respond with exactly the word \"{{.subject}}\" and nothing else.",
		RequiredParams: []string{"subject"},
		DefaultParams: map[string]string{
			"locale": "en",
		},
	}
}

func TestWaveH4_RecipeCRUD_RoundTrip(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)

	// POST — create.
	body, _ := json.Marshal(sampleRecipe())
	resp := fix.post(t, "/api/v1/recipes", body)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("POST status = %d, want 201\n%s", resp.StatusCode, b)
	}
	var created TaskRecipe
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.Name != "echo-prompt" || created.CreatedAt.IsZero() {
		t.Fatalf("created shape wrong: %+v", created)
	}

	// GET list.
	listResp := fix.get(t, "/api/v1/recipes")
	var list struct {
		Recipes []TaskRecipe `json:"recipes"`
	}
	_ = json.NewDecoder(listResp.Body).Decode(&list)
	listResp.Body.Close()
	if len(list.Recipes) != 1 || list.Recipes[0].Name != "echo-prompt" {
		t.Errorf("list = %+v, want one echo-prompt", list)
	}

	// GET single.
	getResp := fix.get(t, "/api/v1/recipes/echo-prompt")
	var got TaskRecipe
	_ = json.NewDecoder(getResp.Body).Decode(&got)
	getResp.Body.Close()
	if got.PromptTemplate != created.PromptTemplate {
		t.Errorf("PromptTemplate not roundtripped: %q", got.PromptTemplate)
	}

	// DELETE.
	delResp := fix.delete(t, "/api/v1/recipes/echo-prompt")
	if delResp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204", delResp.StatusCode)
	}
	delResp.Body.Close()

	// Subsequent GET → 404.
	missResp := fix.get(t, "/api/v1/recipes/echo-prompt")
	if missResp.StatusCode != http.StatusNotFound {
		t.Errorf("post-delete GET status = %d, want 404", missResp.StatusCode)
	}
	missResp.Body.Close()
}

func TestWaveH4_RecipeSave_RejectsInvalidName(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	bad := sampleRecipe()
	bad.Name = "Has Spaces & Symbols!"
	body, _ := json.Marshal(bad)
	resp := fix.post(t, "/api/v1/recipes", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on bad name", resp.StatusCode)
	}
}

func TestWaveH4_RecipeSave_RejectsBadTemplate(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	bad := sampleRecipe()
	bad.PromptTemplate = "{{.unterminated"
	body, _ := json.Marshal(bad)
	resp := fix.post(t, "/api/v1/recipes", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on bad template", resp.StatusCode)
	}
}

func TestWaveH4_ExpandPrompt_MissingRequiredParam(t *testing.T) {
	t.Parallel()
	r := sampleRecipe()
	r.CreatedAt = time.Now()
	r.UpdatedAt = time.Now()
	_, err := r.ExpandPrompt(map[string]string{"locale": "es"})
	if err == nil {
		t.Fatal("expected missing required param error")
	}
	if !strings.Contains(err.Error(), "subject") {
		t.Errorf("error didn't name missing param: %v", err)
	}
}

func TestWaveH4_ExpandPrompt_DefaultsApplied(t *testing.T) {
	t.Parallel()
	r := sampleRecipe()
	r.PromptTemplate = "{{.subject}} in {{.locale}}"
	out, err := r.ExpandPrompt(map[string]string{"subject": "hello"})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if out != "hello in en" {
		t.Errorf("expanded = %q, want 'hello in en'", out)
	}
}

func TestWaveH4_RecipeExecution_SpawnsWithRenderedPrompt(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	body, _ := json.Marshal(sampleRecipe())
	resp := fix.post(t, "/api/v1/recipes", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST recipe status = %d", resp.StatusCode)
	}

	execBody := []byte(`{"params":{"subject":"ack"}}`)
	execResp := fix.post(t, "/v1/runners/recipe/echo-prompt", execBody)
	if execResp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(execResp.Body)
		execResp.Body.Close()
		t.Fatalf("exec status = %d, want 202\n%s", execResp.StatusCode, b)
	}
	var execOut struct {
		ID     string `json:"id"`
		Recipe string `json:"recipe"`
		Prompt string `json:"prompt"`
	}
	_ = json.NewDecoder(execResp.Body).Decode(&execOut)
	execResp.Body.Close()
	if execOut.Recipe != "echo-prompt" {
		t.Errorf("recipe = %q, want echo-prompt", execOut.Recipe)
	}
	if !strings.Contains(execOut.Prompt, "ack") {
		t.Errorf("expanded prompt missing 'ack': %q", execOut.Prompt)
	}

	// Poll state.
	deadline := time.Now().Add(10 * time.Second)
	var finalState string
	for time.Now().Before(deadline) {
		r := fix.get(t, "/v1/runners/"+execOut.ID)
		var s struct{ State string }
		_ = json.NewDecoder(r.Body).Decode(&s)
		r.Body.Close()
		if s.State != "running" && s.State != "" {
			finalState = s.State
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if finalState != "completed" {
		t.Fatalf("final state = %q, want completed", finalState)
	}

	// Fetch result + assert the expanded prompt is in history[0]'s
	// text Part.
	result := fix.get(t, "/v1/runners/"+execOut.ID+"/result")
	defer result.Body.Close()
	var task map[string]any
	_ = json.NewDecoder(result.Body).Decode(&task)
	history, _ := task["history"].([]any)
	if len(history) < 1 {
		t.Fatalf("history empty: %v", task)
	}
	userMsg, _ := history[0].(map[string]any)
	parts, _ := userMsg["parts"].([]any)
	first, _ := parts[0].(map[string]any)
	userText, _ := first["text"].(string)
	if !strings.Contains(userText, "ack") {
		t.Errorf("user message in result history doesn't contain expanded 'ack' prompt: %q", userText)
	}
}

func TestWaveH4_RecipeExecution_MissingParamReturns400(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	body, _ := json.Marshal(sampleRecipe())
	resp := fix.post(t, "/api/v1/recipes", body)
	resp.Body.Close()
	// Execute WITHOUT supplying subject.
	execResp := fix.post(t, "/v1/runners/recipe/echo-prompt", []byte(`{"params":{}}`))
	if execResp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(execResp.Body)
		execResp.Body.Close()
		t.Errorf("status = %d, want 400 on missing required param\n%s", execResp.StatusCode, b)
	}
}

func TestWaveH4_RecipeExecution_UnknownRecipeReturns404(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	resp := fix.post(t, "/v1/runners/recipe/does-not-exist", []byte(`{}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestWaveH4_VirtualAgentCard_PublicAndShape(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	body, _ := json.Marshal(sampleRecipe())
	resp := fix.post(t, "/api/v1/recipes", body)
	resp.Body.Close()

	// Public — NO Authorization header.
	req, _ := http.NewRequest("GET",
		fix.iogridURL+"/a2a/recipe/echo-prompt/.well-known/agent-card.json", nil)
	cardResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET card: %v", err)
	}
	defer cardResp.Body.Close()
	if cardResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (PUBLIC discovery)", cardResp.StatusCode)
	}
	var card map[string]any
	_ = json.NewDecoder(cardResp.Body).Decode(&card)
	if card["name"] != "echo-prompt" {
		t.Errorf("card.name = %v, want echo-prompt", card["name"])
	}
	if card["protocolVersion"] != "1.0" {
		t.Errorf("protocolVersion = %v, want 1.0", card["protocolVersion"])
	}
	ext, _ := card["x-iogrid-recipe"].(map[string]any)
	if ext == nil {
		t.Fatalf("x-iogrid-recipe extension missing: %v", card)
	}
	if ext["agent_slug"] != "claude-code" {
		t.Errorf("extension.agent_slug = %v, want claude-code", ext["agent_slug"])
	}
	reqParams, _ := ext["required_params"].([]any)
	if len(reqParams) != 1 || reqParams[0] != "subject" {
		t.Errorf("required_params = %v, want [subject]", reqParams)
	}
}

func TestWaveH4_RecipeRoutes_RequireAuth(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	// CRUD requires auth — anonymous POST → 401.
	r, _ := http.NewRequest("GET", fix.iogridURL+"/api/v1/recipes", nil)
	resp, _ := http.DefaultClient.Do(r)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anon GET /api/v1/recipes = %d, want 401", resp.StatusCode)
	}
	// Execution requires auth.
	pr, _ := http.NewRequest("POST",
		fix.iogridURL+"/v1/runners/recipe/x",
		bytes.NewReader([]byte(`{}`)))
	pResp, _ := http.DefaultClient.Do(pr)
	pResp.Body.Close()
	if pResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anon POST /v1/runners/recipe/x = %d, want 401", pResp.StatusCode)
	}
}

var _ = fmt.Sprint // silence unused import on future trims
