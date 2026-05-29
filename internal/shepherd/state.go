package shepherd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// LoadState reads ~/.local/state/chepherd/sessions/<uuid>.json (or the
// chepherd-shadow subdir during dual-daemon period). Returns empty map
// if the file doesn't exist.
func LoadState(dir, uuid string) (map[string]any, error) {
	p := filepath.Join(dir, uuid+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return map[string]any{}, nil
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", p, err)
	}
	return s, nil
}

// SaveState writes the state map atomically (write-rename).
func SaveState(dir, uuid string, state map[string]any) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, uuid+".json.tmp")
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, uuid+".json"))
}

// DefaultStateDir is where the Go daemon writes session state during
// shadow mode (separate from Python supervisor's ~/.local/state/workflow).
func DefaultStateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "chepherd-shadow", "sessions")
}
