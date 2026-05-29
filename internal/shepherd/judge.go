package shepherd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Verdict mirrors the Python supervisor's verdict structure exactly so
// downstream consumers (TUI, status command, persisted state) read both.
type Verdict struct {
	Verdict        string         `json:"verdict"`        // silent | praise | coach | intervene
	Reason         string         `json:"reason"`
	PrincipleRef   string         `json:"principle_ref"`
	Message        string         `json:"message"`
	Scorecard      map[string]int `json:"scorecard"`
	ScorecardNote  string         `json:"scorecard_note"`
	CostUSD        float64        `json:"cost_usd"`
	JudgeDuration  time.Duration  `json:"judge_duration_ms"`
}

// JudgeConfig holds tunables for the judge subprocess invocation.
type JudgeConfig struct {
	// Model for the LLM call (passed to claude --model).
	Model string
	// MaxTokens for the response.
	MaxTokens int
	// SystemPromptPath — path to the judge.md file (~/.claude or repo-local).
	SystemPromptPath string
	// Timeout for the subprocess.
	Timeout time.Duration
}

// DefaultJudgeConfig returns the default tunables — same as Python supervisor.
func DefaultJudgeConfig() JudgeConfig {
	home, _ := os.UserHomeDir()
	// Prefer chepherd's own judge.md, fall back to Python supervisor's.
	candidates := []string{
		filepath.Join(home, ".config", "chepherd", "judge.md"),
		filepath.Join(home, "repos", "workflow", "prompts", "judge.md"),
	}
	var found string
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			found = p
			break
		}
	}
	return JudgeConfig{
		Model:            "claude-sonnet-4-6",
		MaxTokens:        400,
		SystemPromptPath: found,
		Timeout:          120 * time.Second,
	}
}

// CallJudge invokes claude headlessly with the judge.md system prompt and
// returns the parsed verdict. CLAUDE_CODE_OAUTH_TOKEN is stripped from the
// env (the Python supervisor's bug we already learned about — long-lived
// tokens are inference-only and break the SDK).
func CallJudge(cfg JudgeConfig, userPrompt string) (*Verdict, error) {
	if cfg.SystemPromptPath == "" {
		return nil, fmt.Errorf("judge.md path not configured")
	}
	systemPrompt, err := os.ReadFile(cfg.SystemPromptPath)
	if err != nil {
		return nil, fmt.Errorf("read judge.md: %w", err)
	}

	start := time.Now()

	args := []string{
		"--print",
		"--model", cfg.Model,
		"--system-prompt", string(systemPrompt),
		"--output-format", "json",
		"--permission-mode", "bypassPermissions",
		"--disable-slash-commands",
		userPrompt,
	}

	env := os.Environ()
	cleanedEnv := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_CODE_OAUTH_TOKEN=") {
			continue
		}
		cleanedEnv = append(cleanedEnv, kv)
	}

	ctx, cancel := contextWithTimeout(cfg.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Env = cleanedEnv
	out, err := cmd.Output()
	dur := time.Since(start)
	if err != nil {
		return &Verdict{
			Verdict:       "silent",
			Reason:        fmt.Sprintf("subprocess error: %v", err),
			JudgeDuration: dur,
		}, nil
	}

	// claude --print --output-format json returns an envelope; the model's
	// reply text is in .result. Pluck + parse the JSON inside.
	var envelope struct {
		Result      string `json:"result"`
		TotalCost   float64 `json:"total_cost_usd"`
		IsError     bool   `json:"is_error"`
		Subtype     string `json:"subtype"`
		StopReason  string `json:"stop_reason"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return &Verdict{
			Verdict:       "silent",
			Reason:        fmt.Sprintf("envelope parse: %v", err),
			JudgeDuration: dur,
		}, nil
	}
	if envelope.IsError {
		return &Verdict{
			Verdict:       "silent",
			Reason:        fmt.Sprintf("api-error: %s", envelope.Subtype),
			CostUSD:       envelope.TotalCost,
			JudgeDuration: dur,
		}, nil
	}

	text := strings.TrimSpace(envelope.Result)
	// Strip ```json fences.
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	// Extract outer JSON object {…}.
	objRE := regexp.MustCompile(`(?s)\{.*\}`)
	m := objRE.FindString(text)
	if m == "" {
		return &Verdict{
			Verdict:       "silent",
			Reason:        "non-json verdict",
			CostUSD:       envelope.TotalCost,
			JudgeDuration: dur,
		}, nil
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(m), &data); err != nil {
		return &Verdict{
			Verdict:       "silent",
			Reason:        fmt.Sprintf("json-parse: %v", err),
			CostUSD:       envelope.TotalCost,
			JudgeDuration: dur,
		}, nil
	}

	v := &Verdict{
		Verdict:       getString(data, "verdict", "silent"),
		Reason:        truncate(getString(data, "reason", ""), 200),
		PrincipleRef:  getString(data, "principle_ref", ""),
		Message:       getString(data, "message", ""),
		ScorecardNote: truncate(getString(data, "scorecard_note", ""), 200),
		CostUSD:       envelope.TotalCost,
		JudgeDuration: dur,
	}
	if sc, ok := data["scorecard"].(map[string]any); ok {
		v.Scorecard = map[string]int{}
		for _, k := range []string{"G", "V", "F", "E"} {
			if raw, ok := sc[k]; ok {
				v.Scorecard[k] = toInt(raw, 5)
			}
		}
	}
	return v, nil
}

func getString(m map[string]any, k, fallback string) string {
	v, ok := m[k]
	if !ok || v == nil {
		return fallback
	}
	s, ok := v.(string)
	if !ok {
		return fallback
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func toInt(v any, fallback int) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	}
	return fallback
}
