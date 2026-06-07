// Package graphify wraps the graphify CLI (graphify.net / safishamsi/graphify)
// so the chepherd daemon can build and serve a per-repo codebase knowledge
// graph — letting agents query structure instead of grepping raw files
// (the context-budget win behind #725).
//
// Decided design (#725): daemon-central single source of truth, default
// code-only. Code extraction is local tree-sitter — no LLM, no tokens, no
// network — via `graphify update <repo> --no-cluster` (clustering's
// community-naming is the only LLM step, so --no-cluster guarantees a free,
// deterministic, sovereign build). The graph lands at
// <repo>/graphify-out/graph.json (graphify's fixed layout) and is served to
// agents over graphify's own Streamable-HTTP MCP server
// (`python -m graphify.serve --transport http`). Docs/media extraction
// (paid, LLM-backed) is a separate opt-in handled by callers, not here.
//
// The pinned graphify version lives in the Containerfile, not in Go.
package graphify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// DefaultBin is the graphify console script (pip package "graphifyy").
const DefaultBin = "graphify"

// graphify's fixed output layout under a repo working tree.
const (
	OutDir    = "graphify-out"
	GraphFile = "graph.json"
)

// Client invokes the graphify CLI. The zero value is usable and uses
// DefaultBin; tests inject a stub via Bin.
type Client struct {
	Bin string // graphify binary path/name; "" → DefaultBin
}

// New returns a Client using the graphify binary on PATH.
func New() *Client { return &Client{} }

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return DefaultBin
}

// Available reports whether the graphify CLI is resolvable. The daemon image
// bundles it (Containerfile); dev hosts may not, so callers gate optional
// behavior — and the real-graphify test — on this.
func (c *Client) Available() bool {
	_, err := exec.LookPath(c.bin())
	return err == nil
}

// GraphPath is the graph.json location for a repo working tree.
func (c *Client) GraphPath(repoPath string) string {
	return filepath.Join(repoPath, OutDir, GraphFile)
}

// BuildCodeOnly (re)builds the repo's code graph with NO LLM/token cost:
// `graphify update <repo> --no-cluster`. This is the default-on path. It is
// idempotent — graphify re-extracts changed code files and updates the graph
// in place, so the same call serves both first-build and incremental refresh.
// Returns an error (with captured CLI output) if the command fails or if the
// graph file is not present afterward.
func (c *Client) BuildCodeOnly(ctx context.Context, repoPath string) error {
	if repoPath == "" {
		return fmt.Errorf("graphify: repoPath required")
	}
	cmd := exec.CommandContext(ctx, c.bin(), "update", repoPath, "--no-cluster")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("graphify update %q: %w (output: %s)", repoPath, err, truncate(out, 600))
	}
	if _, statErr := os.Stat(c.GraphPath(repoPath)); statErr != nil {
		return fmt.Errorf("graphify update %q: graph not produced at %s: %v (output: %s)",
			repoPath, c.GraphPath(repoPath), statErr, truncate(out, 600))
	}
	return nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
