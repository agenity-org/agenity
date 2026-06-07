package graphify

import (
	"context"
	"fmt"
	"os/exec"
)

// Explain returns graphify's plain-language explanation of a node and its
// neighbors from a repo's graph.json — `graphify explain "<node>" --graph`.
// This is the read path the daemon exposes to agents over MCP (the agent
// can't reach a loopback graphify.serve from its own container, so the
// daemon proxies the query against the daemon-side graph).
func (c *Client) Explain(ctx context.Context, graphPath, node string) (string, error) {
	if graphPath == "" || node == "" {
		return "", fmt.Errorf("graphify explain: graphPath and node required")
	}
	out, err := exec.CommandContext(ctx, c.bin(), "explain", node, "--graph", graphPath).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("graphify explain %q: %w (output: %s)", node, err, truncate(out, 400))
	}
	return string(out), nil
}

// ShortestPath returns graphify's shortest path between two nodes —
// `graphify path "A" "B" --graph`.
func (c *Client) ShortestPath(ctx context.Context, graphPath, from, to string) (string, error) {
	if graphPath == "" || from == "" || to == "" {
		return "", fmt.Errorf("graphify path: graphPath, from, to required")
	}
	out, err := exec.CommandContext(ctx, c.bin(), "path", from, to, "--graph", graphPath).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("graphify path %q→%q: %w (output: %s)", from, to, err, truncate(out, 400))
	}
	return string(out), nil
}
