package graphify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGraphPath(t *testing.T) {
	c := New()
	got := c.GraphPath("/repo")
	want := filepath.Join("/repo", "graphify-out", "graph.json")
	if got != want {
		t.Fatalf("GraphPath = %q, want %q", got, want)
	}
}

// writeStub drops an executable shell script named "graphify" in its own dir
// and returns its path. body is the script after the shebang.
func writeStub(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub graphify uses a POSIX shell script")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "graphify")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildCodeOnly_InvokesUpdateNoClusterAndDetectsGraph(t *testing.T) {
	repo := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	// $1=update $2=<repo> $3=--no-cluster ; record args + emit a graph.json.
	stub := writeStub(t,
		"printf '%s\\n' \"$@\" > "+argsFile+"\n"+
			"mkdir -p \"$2/"+OutDir+"\"\n"+
			"printf '{\"nodes\":[],\"links\":[]}' > \"$2/"+OutDir+"/"+GraphFile+"\"\n")

	c := &Client{Bin: stub}
	if err := c.BuildCodeOnly(context.Background(), repo); err != nil {
		t.Fatalf("BuildCodeOnly: %v", err)
	}
	got, _ := os.ReadFile(argsFile)
	for _, want := range []string{"update", repo, "--no-cluster"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("graphify args missing %q; got:\n%s", want, got)
		}
	}
	if _, err := os.Stat(c.GraphPath(repo)); err != nil {
		t.Errorf("graph.json not detected after build: %v", err)
	}
}

func TestBuildCodeOnly_CLIFailureReturnsError(t *testing.T) {
	repo := t.TempDir()
	stub := writeStub(t, "echo boom >&2\nexit 1\n")
	c := &Client{Bin: stub}
	if err := c.BuildCodeOnly(context.Background(), repo); err == nil {
		t.Fatal("expected error when graphify exits non-zero, got nil")
	}
}

func TestBuildCodeOnly_GraphNotProducedReturnsError(t *testing.T) {
	repo := t.TempDir()
	// exits 0 but writes nothing — wrapper must still fail.
	stub := writeStub(t, "exit 0\n")
	c := &Client{Bin: stub}
	if err := c.BuildCodeOnly(context.Background(), repo); err == nil {
		t.Fatal("expected error when no graph.json produced, got nil")
	}
}

func TestBuildCodeOnly_EmptyRepoPath(t *testing.T) {
	if err := New().BuildCodeOnly(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty repoPath")
	}
}

// Real-byte coverage: when graphify is actually installed (dev hosts + the
// daemon image), drive it for real and assert a code-only build yields a
// non-empty node set. Skips in environments without graphify (e.g. the bare
// CI image) — the stub tests above cover the wrapper contract there.
func TestBuildCodeOnly_RealGraphify(t *testing.T) {
	c := New()
	if !c.Available() {
		t.Skip("real graphify not on PATH")
	}
	repo := t.TempDir()
	src := "package main\nfunc add(a, b int) int { return a + b }\nfunc main() { println(add(1, 2)) }\n"
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.BuildCodeOnly(context.Background(), repo); err != nil {
		t.Fatalf("real graphify build: %v", err)
	}
	data, err := os.ReadFile(c.GraphPath(repo))
	if err != nil {
		t.Fatalf("read graph.json: %v", err)
	}
	var g struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatalf("parse graph.json: %v", err)
	}
	if len(g.Nodes) == 0 {
		t.Errorf("real graphify produced 0 nodes for a non-empty repo")
	}
}
