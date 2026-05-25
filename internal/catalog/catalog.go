// Package catalog parses + applies TeamProfile YAML files. The catalog/
// dir at the repo root ships 5 default templates; users can install
// custom templates to ~/.local/state/chepherd-v06/teams/<name>.yaml.
//
// We use a minimal YAML parser to avoid pulling gopkg.in/yaml.v3 — for
// v0.6 the schema is small enough that a hand-rolled parser is cheaper
// than a dep. JSON-compatible YAML is what the catalog ships (key:value
// + lists), no anchors/aliases/multi-doc/folded scalars.
package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chepherd/chepherd/internal/runtime"
)

// TeamProfile is the parsed form of a catalog YAML.
type TeamProfile struct {
	APIVersion  string
	Kind        string
	Name        string
	Description string
	Topology    runtime.Topology
	Members     []MemberSpec
}

// MemberSpec describes one agent to spawn + join when applying a profile.
type MemberSpec struct {
	Name             string
	Agent            string                  // claude-code | qwen-code | ...
	Role             runtime.MembershipRole
	UseDefaultPrompt bool
	BriefOverride    string
	StatSheet        runtime.AgentStatSheet
}

// Load parses a single TeamProfile YAML from path.
func Load(path string) (*TeamProfile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(string(b))
}

// LoadAll loads every *.yaml in the given dir.
func LoadAll(dir string) ([]*TeamProfile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*TeamProfile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		p, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // skip malformed; don't block the rest
		}
		out = append(out, p)
	}
	return out, nil
}

// Parse parses YAML text into a TeamProfile.
// Minimal parser: top-level scalars, "members:" list, member fields,
// stat_sheet sub-block. No multi-doc, no anchors, no folded scalars.
func Parse(text string) (*TeamProfile, error) {
	p := &TeamProfile{Topology: runtime.TopologyHub}
	lines := strings.Split(text, "\n")

	var inMembers, inMemberStatSheet bool
	var inDescription, inBriefOverride bool
	var descIndent int
	var cur *MemberSpec
	flushCur := func() {
		if cur != nil {
			p.Members = append(p.Members, *cur)
			cur = nil
		}
		inMemberStatSheet = false
	}

	for i, raw := range lines {
		_ = i
		// Calculate leading-space indent for indent-based detection
		indent := 0
		for indent < len(raw) && raw[indent] == ' ' {
			indent++
		}
		line := strings.TrimLeft(raw, " ")
		// Handle multi-line description / brief_override (folded |)
		if inDescription {
			if indent > descIndent || strings.TrimSpace(line) == "" {
				p.Description += strings.TrimLeft(raw, " ") + "\n"
				continue
			}
			p.Description = strings.TrimSpace(p.Description)
			inDescription = false
			// fall through to parse this line
		}
		if inBriefOverride {
			if indent > descIndent || strings.TrimSpace(line) == "" {
				cur.BriefOverride += strings.TrimLeft(raw, " ") + "\n"
				continue
			}
			cur.BriefOverride = strings.TrimSpace(cur.BriefOverride)
			inBriefOverride = false
			// fall through
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Top-level keys
		if indent == 0 {
			flushCur()
			inMembers = false
			k, v := splitKV(line)
			switch k {
			case "apiVersion":
				p.APIVersion = v
			case "kind":
				p.Kind = v
			case "name":
				p.Name = v
			case "description":
				if v == "|" {
					inDescription = true
					descIndent = indent
				} else {
					p.Description = v
				}
			case "topology":
				p.Topology = runtime.Topology(v)
			case "members":
				inMembers = true
			}
			continue
		}
		// Inside members list
		if inMembers {
			if indent == 2 && strings.HasPrefix(line, "- ") {
				flushCur()
				cur = &MemberSpec{}
				inMemberStatSheet = false
				// parse first kv on the "- key: value" line
				k, v := splitKV(strings.TrimPrefix(line, "- "))
				setMemberKV(cur, k, v, &inBriefOverride, &descIndent, indent)
				continue
			}
			if cur == nil {
				continue
			}
			if indent == 4 {
				inMemberStatSheet = false
				k, v := splitKV(line)
				switch k {
				case "stat_sheet":
					inMemberStatSheet = true
				default:
					setMemberKV(cur, k, v, &inBriefOverride, &descIndent, indent)
				}
				continue
			}
			if indent == 6 && inMemberStatSheet {
				k, v := splitKV(line)
				setStatSheetKV(&cur.StatSheet, k, v)
				continue
			}
		}
	}
	flushCur()
	if p.APIVersion == "" || p.Kind != "TeamProfile" {
		return nil, fmt.Errorf("catalog: invalid manifest (apiVersion=%q kind=%q)", p.APIVersion, p.Kind)
	}
	return p, nil
}

func splitKV(line string) (string, string) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return strings.TrimSpace(line), ""
	}
	k := strings.TrimSpace(line[:idx])
	v := strings.TrimSpace(line[idx+1:])
	// strip surrounding quotes
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"') {
		v = v[1 : len(v)-1]
	}
	return k, v
}

func setMemberKV(m *MemberSpec, k, v string, inBriefOverride *bool, descIndent *int, indent int) {
	switch k {
	case "name":
		m.Name = v
	case "agent":
		m.Agent = v
	case "role":
		m.Role = runtime.MembershipRole(v)
	case "use_default_prompt":
		m.UseDefaultPrompt = v == "true"
	case "brief_override":
		if v == "|" {
			*inBriefOverride = true
			*descIndent = indent
		} else {
			m.BriefOverride = v
		}
	}
}

func setStatSheetKV(s *runtime.AgentStatSheet, k, v string) {
	switch k {
	case "model_tier":
		s.ModelTier = v
	case "context_budget":
		fmt.Sscanf(v, "%d", &s.ContextBudget)
	case "discipline_weight":
		fmt.Sscanf(v, "%f", &s.DisciplineWeight)
	case "velocity_expect":
		s.VelocityExpect = v
	case "token_budget_usd":
		fmt.Sscanf(v, "%f", &s.TokenBudgetUSD)
	}
}
