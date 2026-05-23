package rc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// LocalCommandHandler implements CommandHandler against the local filesystem
// + tmux. Used by the chepherd daemon to satisfy peer commands.
//
// StateDir mirrors daemon.DefaultStateDir() (~/.local/state/chepherd-shadow/
// sessions during shadow mode; ~/.local/state/chepherd/sessions in
// production). Sessions are addressed by claude UUID (matching how the
// daemon's discovery + state code identifies them).
type LocalCommandHandler struct {
	StateDir string
	// TmuxNameByUUID maps claude UUID → tmux session name. Filled by the
	// daemon's discovery loop on each tick; the listener reads it here.
	TmuxNameByUUID func(uuid string) (string, bool)
}

// Pause writes the .paused sentinel file the daemon's tick loop respects.
func (h *LocalCommandHandler) Pause(uuid string) error {
	if uuid == "" {
		return errors.New("uuid required")
	}
	path := filepath.Join(h.StateDir, uuid+".paused")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("paused via chepherd-rc\n"), 0o600)
}

// Unpause removes the .paused sentinel.
func (h *LocalCommandHandler) Unpause(uuid string) error {
	path := filepath.Join(h.StateDir, uuid+".paused")
	err := os.Remove(path)
	if err != nil && os.IsNotExist(err) {
		return nil // already unpaused
	}
	return err
}

// Refresh removes the next_tick_at field by setting it to "now" — the
// daemon's next pass will tick this session immediately.
func (h *LocalCommandHandler) Refresh(uuid string) error {
	// Cheap signal: just touch the state file's mtime; the daemon's tick
	// loop uses next_tick_at, so the real way is to rewrite that field.
	// We use a sentinel sidecar `.refresh-now` that the tick loop can
	// honour — keeps this handler from owning the JSON schema.
	path := filepath.Join(h.StateDir, uuid+".refresh-now")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("refresh requested via chepherd-rc\n"), 0o600)
}

// Inject pastes the given message into the session's tmux pane.
func (h *LocalCommandHandler) Inject(uuid, message string) error {
	if uuid == "" {
		return errors.New("uuid required")
	}
	if h.TmuxNameByUUID == nil {
		return errors.New("TmuxNameByUUID lookup not wired")
	}
	tmuxName, ok := h.TmuxNameByUUID(uuid)
	if !ok {
		return fmt.Errorf("unknown session uuid %s", uuid)
	}
	// tmux load-buffer "-" reads the buffer from stdin; tmux paste-buffer
	// pastes it into the named pane; send-keys Enter submits.
	c1 := exec.Command("tmux", "load-buffer", "-")
	c1.Stdin = nil
	stdin, err := c1.StdinPipe()
	if err != nil {
		return err
	}
	if err := c1.Start(); err != nil {
		return err
	}
	if _, err := stdin.Write([]byte(message)); err != nil {
		return err
	}
	_ = stdin.Close()
	if err := c1.Wait(); err != nil {
		return fmt.Errorf("tmux load-buffer: %w", err)
	}
	if err := exec.Command("tmux", "paste-buffer", "-t", tmuxName).Run(); err != nil {
		return fmt.Errorf("tmux paste-buffer: %w", err)
	}
	if err := exec.Command("tmux", "send-keys", "-t", tmuxName, "Enter").Run(); err != nil {
		return fmt.Errorf("tmux send-keys: %w", err)
	}
	return nil
}
