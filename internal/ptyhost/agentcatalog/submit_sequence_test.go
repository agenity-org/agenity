package agentcatalog

import (
	"bytes"
	"testing"
)

func TestEffectiveSubmitSequence_DefaultsToCR(t *testing.T) {
	t.Parallel()
	a := Agent{Slug: "test", Binary: "/bin/sh"}
	got := a.EffectiveSubmitSequence()
	if !bytes.Equal(got, []byte{0x0d}) {
		t.Errorf("default = %v, want [0x0d]", got)
	}
}

func TestEffectiveSubmitSequence_HonorsOverride(t *testing.T) {
	t.Parallel()
	a := Agent{
		Slug:           "windows-mode",
		Binary:         "/bin/sh",
		SubmitSequence: []byte{0x0a, 0x0d}, // LF+CR — hypothetical multi-line mode
	}
	got := a.EffectiveSubmitSequence()
	if !bytes.Equal(got, []byte{0x0a, 0x0d}) {
		t.Errorf("override = %v, want [0x0a, 0x0d]", got)
	}
}

func TestBuiltin_AllFlavorsHaveSubmitSequence(t *testing.T) {
	t.Parallel()
	// Every builtin chepherd worker flavor should have a defined submit
	// sequence (default CR is fine; explicit override also OK).
	// 7 flavors: aider, claude-code, cursor-agent, little-coder,
	// opencode, qwen-code, sovereign-shell.
	for _, a := range Builtin {
		got := a.EffectiveSubmitSequence()
		if len(got) == 0 {
			t.Errorf("%s: EffectiveSubmitSequence is empty", a.Slug)
		}
	}
}
