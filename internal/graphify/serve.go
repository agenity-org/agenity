package graphify

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
)

// DefaultPython is the interpreter used to launch graphify's MCP server
// module (graphify.serve). The daemon image installs python3 (Containerfile).
const DefaultPython = "python3"

// ServeConfig configures a graphify MCP-HTTP server for ONE repo's graph.
// The daemon runs one of these per repo and mints a per-repo APIKey, so an
// agent can only reach the graph for the repo it's assigned (#725 scoping).
type ServeConfig struct {
	GraphPath string // path to the repo's graph.json (required)
	APIKey    string // bearer key clients must present (required — scopes access)
	Host      string // bind host; "" → 127.0.0.1 (loopback only)
	Port      int    // bind port (required; use FreePort to pick one)
	Python    string // interpreter; "" → DefaultPython
}

func (cfg ServeConfig) python() string {
	if cfg.Python != "" {
		return cfg.Python
	}
	return DefaultPython
}

func (cfg ServeConfig) host() string {
	if cfg.Host != "" {
		return cfg.Host
	}
	return "127.0.0.1"
}

// serveArgs builds the `python -m graphify.serve` argv for cfg, validated
// against graphify 0.8.35's serve interface:
//
//	python -m graphify.serve --transport http --host H --port P
//	    --api-key K --stateless <graph_path>
//
// Pure + deterministic so the wiring is unit-testable without a live server.
func serveArgs(cfg ServeConfig) ([]string, error) {
	if cfg.GraphPath == "" {
		return nil, fmt.Errorf("graphify serve: GraphPath required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("graphify serve: APIKey required (scopes repo access)")
	}
	if cfg.Port <= 0 {
		return nil, fmt.Errorf("graphify serve: Port required (see FreePort)")
	}
	return []string{
		"-m", "graphify.serve",
		"--transport", "http",
		"--host", cfg.host(),
		"--port", strconv.Itoa(cfg.Port),
		"--api-key", cfg.APIKey,
		"--stateless",
		cfg.GraphPath,
	}, nil
}

// Server is a running graphify.serve process for one repo's graph.
type Server struct {
	cmd *exec.Cmd
	url string
}

// URL is the base URL the MCP-HTTP server listens on (http://host:port).
func (s *Server) URL() string { return s.url }

// Serve starts a graphify MCP-HTTP server for cfg and returns a handle.
// The process is bound to ctx; cancelling ctx (or calling Stop) terminates it.
func (c *Client) Serve(ctx context.Context, cfg ServeConfig) (*Server, error) {
	args, err := serveArgs(cfg)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, cfg.python(), args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("graphify serve start: %w", err)
	}
	return &Server{
		cmd: cmd,
		url: fmt.Sprintf("http://%s:%d", cfg.host(), cfg.Port),
	}, nil
}

// Stop terminates the server process. Safe to call on a nil/partial Server.
func (s *Server) Stop() error {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	_ = s.cmd.Process.Kill()
	_ = s.cmd.Wait()
	return nil
}

// FreePort asks the OS for an available TCP port on host (loopback default).
// The daemon picks one per repo before calling Serve.
func FreePort(host string) (int, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
