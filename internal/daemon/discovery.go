// Package daemon implements the chepherd supervisor — Go port of the
// proven Python supervisor.py. Same logic, same prompt, same state file
// shape so existing Python state files can be read by the Go daemon
// during the dual-daemon dry-run period.
package daemon

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Session is one watched Claude Code session — tmux pane + claude PID + JSONL.
type Session struct {
	TmuxName  string
	Repo      string // path-suffix matched from tmux name (e.g. "openova")
	ClaudePID int
	CWD       string
	UUID      string
	JSONLPath string
	GHRepo    string // derived from `git remote get-url origin`; "" if not GitHub
}

// TmuxNameRE matches the canonical session-name pattern: <repo>-<idx>.
// Must match Python supervisor's regex exactly for dual-daemon compatibility.
var TmuxNameRE = regexp.MustCompile(`^(?P<repo>[a-z][a-z0-9_-]+)-(?P<idx>\d+)$`)

// DiscoverSessions joins:
//   1. tmux ls (named sessions matching TmuxNameRE)
//   2. ps -eo pid,args (top-level claude REPL processes)
//   3. /proc/<pid>/cwd
// Returns deduped (by claude UUID) session list.
func DiscoverSessions() ([]*Session, error) {
	tmuxNames, err := listTmuxSessions()
	if err != nil {
		return nil, fmt.Errorf("tmux ls: %w", err)
	}
	procs, err := listClaudeProcesses()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}

	var sessions []*Session
	for _, tname := range tmuxNames {
		m := TmuxNameRE.FindStringSubmatch(tname)
		if m == nil {
			continue
		}
		repo := m[1]
		var proc *claudeProcess
		for _, p := range procs {
			if filepath.Base(p.cwd) == repo {
				proc = p
				break
			}
		}
		if proc == nil {
			continue
		}
		uuid := extractResumeUUID(proc.args)
		jsonlPath := findJSONL(repo, uuid)
		if jsonlPath == "" {
			continue
		}
		if uuid == "" {
			// Fallback: filename stem of the JSONL.
			uuid = strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl")
		}
		ghRepo := ghRemoteFromCWD(proc.cwd)
		sessions = append(sessions, &Session{
			TmuxName:  tname,
			Repo:      repo,
			ClaudePID: proc.pid,
			CWD:       proc.cwd,
			UUID:      uuid,
			JSONLPath: jsonlPath,
			GHRepo:    ghRepo,
		})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].TmuxName < sessions[j].TmuxName
	})
	// Dedupe by UUID — tmux session groups can map multiple tmux names
	// to the same claude pane/JSONL. Keep alphabetically-first name.
	seen := map[string]bool{}
	deduped := sessions[:0]
	for _, s := range sessions {
		if seen[s.UUID] {
			continue
		}
		seen[s.UUID] = true
		deduped = append(deduped, s)
	}
	return deduped, nil
}

type claudeProcess struct {
	pid  int
	cwd  string
	args string
}

func listTmuxSessions() ([]string, error) {
	out, err := exec.Command("tmux", "ls").Output()
	if err != nil {
		// tmux not running = no sessions; not an error.
		return nil, nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

func listClaudeProcesses() ([]*claudeProcess, error) {
	out, err := exec.Command("ps", "-eo", "pid,args", "--no-headers").Output()
	if err != nil {
		return nil, err
	}
	var procs []*claudeProcess
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		pidStr := strings.TrimSpace(parts[0])
		args := strings.TrimSpace(parts[1])
		if !strings.Contains(args, "claude") {
			continue
		}
		if !strings.Contains(args, "--resume") && !strings.Contains(args, "--continue") {
			continue
		}
		var pid int
		fmt.Sscanf(pidStr, "%d", &pid)
		if pid == 0 {
			continue
		}
		cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
		if err != nil {
			continue
		}
		procs = append(procs, &claudeProcess{pid: pid, cwd: cwd, args: args})
	}
	return procs, nil
}

var resumeRE = regexp.MustCompile(`--resume\s+([0-9a-f-]{36})`)

func extractResumeUUID(args string) string {
	m := resumeRE.FindStringSubmatch(args)
	if m == nil {
		return ""
	}
	return m[1]
}

// findJSONL returns the JSONL path for the given repo + uuid.
// If uuid is empty, picks the most-recently-modified JSONL in the project dir.
func findJSONL(repo, uuid string) string {
	home, _ := os.UserHomeDir()
	projDir := filepath.Join(home, ".claude", "projects",
		"-home-openova-repos-"+repo)
	if uuid != "" {
		p := filepath.Join(projDir, uuid+".jsonl")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Fallback: newest *.jsonl
	var newest string
	var newestMtime int64
	_ = filepath.WalkDir(projDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Ext(p) != ".jsonl" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		mt := info.ModTime().Unix()
		if mt > newestMtime {
			newestMtime = mt
			newest = p
		}
		return nil
	})
	return newest
}

var ghRemoteRE = regexp.MustCompile(`github\.com[:/]([^/]+)/([^/.]+)(?:\.git)?$`)

// ghRemoteFromCWD returns "org/repo" derived from `git remote get-url origin`,
// or "" if the cwd isn't a git repo or the remote isn't on GitHub.
func ghRemoteFromCWD(cwd string) string {
	out, err := exec.Command("git", "-C", cwd, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(string(out))
	m := ghRemoteRE.FindStringSubmatch(url)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}
