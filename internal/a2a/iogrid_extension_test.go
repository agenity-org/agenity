// internal/a2a/iogrid_extension_test.go — pins #318 (#225 row E1)
// x-iogrid extension wire shape.
package a2a

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIOgridExtension_DefaultsShape(t *testing.T) {
	t.Parallel()
	ext := DefaultIOgridExtension()
	if ext.Version != "0.9.3" {
		t.Errorf("Version = %q, want 0.9.3", ext.Version)
	}
	if len(ext.SupportedRecipeVersions) != 1 || ext.SupportedRecipeVersions[0] != "1" {
		t.Errorf("SupportedRecipeVersions = %v, want [1]", ext.SupportedRecipeVersions)
	}
}

func TestIOgridExtension_AgentCardSerializesWithXIOgridKey(t *testing.T) {
	t.Parallel()
	ext := DefaultIOgridExtension()
	ext.Endpoint = "https://chepherd.example.com/iogrid"
	card := &AgentCard{
		ProtocolVersion: "1.0",
		Name:            "test",
		URL:             "http://test/",
		Version:         "0.9.3",
		XIOgrid:         ext,
	}
	body, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(body), `"x-iogrid"`) {
		t.Errorf("marshalled card missing x-iogrid key: %s", body)
	}
	if !strings.Contains(string(body), `"endpoint":"https://chepherd.example.com/iogrid"`) {
		t.Errorf("marshalled card missing endpoint: %s", body)
	}
}

func TestIOgridExtension_OmittedWhenNil(t *testing.T) {
	t.Parallel()
	card := &AgentCard{
		ProtocolVersion: "1.0",
		Name:            "test",
		URL:             "http://test/",
		Version:         "0.9.3",
	}
	body, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(body), "x-iogrid") {
		t.Errorf("nil extension still serialized: %s", body)
	}
}
