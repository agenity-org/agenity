package graphify

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestServeArgs_Valid(t *testing.T) {
	args, err := serveArgs(ServeConfig{
		GraphPath: "/repo/graphify-out/graph.json",
		APIKey:    "secret-key",
		Host:      "127.0.0.1",
		Port:      54321,
	})
	if err != nil {
		t.Fatalf("serveArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-m graphify.serve",
		"--transport http",
		"--host 127.0.0.1",
		"--port 54321",
		"--api-key secret-key",
		"--stateless",
		"/repo/graphify-out/graph.json",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("serveArgs missing %q; got: %s", want, joined)
		}
	}
	// graph path must be the trailing positional arg.
	if args[len(args)-1] != "/repo/graphify-out/graph.json" {
		t.Errorf("graph path not last arg; got %q", args[len(args)-1])
	}
}

func TestServeArgs_DefaultsHost(t *testing.T) {
	args, err := serveArgs(ServeConfig{GraphPath: "g.json", APIKey: "k", Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "--host 127.0.0.1") {
		t.Errorf("expected loopback default host; got %v", args)
	}
}

func TestServeArgs_Validation(t *testing.T) {
	cases := map[string]ServeConfig{
		"no graph":  {APIKey: "k", Port: 1},
		"no apikey": {GraphPath: "g", Port: 1},
		"no port":   {GraphPath: "g", APIKey: "k"},
	}
	for name, cfg := range cases {
		if _, err := serveArgs(cfg); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
}

func TestFreePort(t *testing.T) {
	p, err := FreePort("127.0.0.1")
	if err != nil {
		t.Fatalf("FreePort: %v", err)
	}
	if p <= 0 || p > 65535 {
		t.Errorf("FreePort returned out-of-range port %d", p)
	}
}

func TestServe_StartStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub interpreter uses a POSIX shell script")
	}
	// Stub "python" that ignores args and blocks, so we can verify the
	// manager starts a process and Stop() reaps it — without a real server.
	dir := t.TempDir()
	stub := filepath.Join(dir, "python3")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	port, err := FreePort("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	c := New()
	srv, err := c.Serve(context.Background(), ServeConfig{
		GraphPath: filepath.Join(dir, "graph.json"),
		APIKey:    "k",
		Port:      port,
		Python:    stub,
	})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if srv.URL() == "" || !strings.Contains(srv.URL(), "127.0.0.1") {
		t.Errorf("unexpected URL %q", srv.URL())
	}
	if srv.cmd == nil || srv.cmd.Process == nil {
		t.Fatal("server process not started")
	}
	if err := srv.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
	// Stop is idempotent / nil-safe.
	if err := (*Server)(nil).Stop(); err != nil {
		t.Errorf("nil Stop: %v", err)
	}
}
