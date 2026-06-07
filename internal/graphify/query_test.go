package graphify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplain_InvokesCLIWithGraphFlag(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	stub := writeStub(t, "printf '%s\\n' \"$@\" > "+argsFile+"\necho EXPLANATION\n")
	c := &Client{Bin: stub}
	out, err := c.Explain(context.Background(), "/g/graph.json", "add()")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if !strings.Contains(out, "EXPLANATION") {
		t.Errorf("output not returned; got %q", out)
	}
	got, _ := os.ReadFile(argsFile)
	for _, want := range []string{"explain", "add()", "--graph", "/g/graph.json"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("explain args missing %q; got:\n%s", want, got)
		}
	}
}

func TestShortestPath_InvokesCLI(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	stub := writeStub(t, "printf '%s\\n' \"$@\" > "+argsFile+"\necho PATH\n")
	c := &Client{Bin: stub}
	if _, err := c.ShortestPath(context.Background(), "/g/graph.json", "A", "B"); err != nil {
		t.Fatalf("ShortestPath: %v", err)
	}
	got, _ := os.ReadFile(argsFile)
	for _, want := range []string{"path", "A", "B", "--graph", "/g/graph.json"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("path args missing %q; got:\n%s", want, got)
		}
	}
}

func TestQuery_Validation(t *testing.T) {
	c := New()
	if _, err := c.Explain(context.Background(), "", "x"); err == nil {
		t.Error("Explain: expected error for empty graphPath")
	}
	if _, err := c.Explain(context.Background(), "g", ""); err == nil {
		t.Error("Explain: expected error for empty node")
	}
	if _, err := c.ShortestPath(context.Background(), "g", "a", ""); err == nil {
		t.Error("ShortestPath: expected error for empty endpoint")
	}
}

// Real-byte coverage: build a graph with the real CLI, then explain a node
// that the build produced. Skips when graphify is absent.
func TestExplain_RealGraphify(t *testing.T) {
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
		t.Fatalf("build: %v", err)
	}
	out, err := c.Explain(context.Background(), c.GraphPath(repo), "add")
	if err != nil {
		t.Fatalf("real explain: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("real graphify explain returned empty output")
	}
}
