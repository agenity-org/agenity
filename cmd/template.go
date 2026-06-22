// Package cmd — `chepherd template ...` subcommands: list installed
// templates, install from the catalog dir, apply (spawn agents +
// memberships), save-as (snapshot current state to a YAML file).
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/agenity-org/agenity/internal/catalog"
	"github.com/agenity-org/agenity/internal/runtime"
)

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage TeamProfile templates (catalog YAMLs)",
}

var templateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available templates",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Look in repo's catalog/ first (dev path), then ~/.local/state/chepherd/catalog/.
		dirs := []string{
			"./catalog",
			filepath.Join(os.Getenv("HOME"), ".local/state/chepherd/catalog"),
		}
		seen := map[string]bool{}
		for _, d := range dirs {
			ps, _ := catalog.LoadAll(d)
			for _, p := range ps {
				if seen[p.Name] {
					continue
				}
				seen[p.Name] = true
				fmt.Printf("  %s — %d members, topology=%s\n", p.Name, len(p.Members), p.Topology)
				fmt.Printf("    %s\n", oneline(p.Description))
			}
		}
		return nil
	},
}

func oneline(s string) string {
	if len(s) > 100 {
		return s[:100] + "…"
	}
	first := s
	for i, c := range s {
		if c == '\n' && i > 0 {
			first = s[:i]
			break
		}
	}
	return first
}

func init() {
	templateCmd.AddCommand(templateListCmd)
	rootCmd.AddCommand(templateCmd)
}

// ApplyTemplate is the runtime-side helper called by the dashboard when
// the operator picks a template in the spawn modal. Spawns each member
// agent + auto-joins to the named team.
func ApplyTemplate(rt *runtime.Runtime, p *catalog.TeamProfile, teamName, cwd string) error {
	if teamName == "" {
		teamName = p.Name
	}
	if _, _ = rt.CreateTeam(teamName, "", p.Topology); false {
	}
	for _, m := range p.Members {
		// Spawn the agent (no team — we attach via membership separately
		// to keep v0.5 SessionInfo.Team alignment optional).
		_, _, err := rt.Spawn(runtime.SpawnSpec{
			Name:      m.Name,
			AgentSlug: m.Agent,
			Team:      teamName,
			Role:      mapRole(m.Role),
			Cwd:       cwd,
		})
		if err != nil {
			return fmt.Errorf("apply template %s: spawn %s: %w", p.Name, m.Name, err)
		}
		if _, err := rt.JoinTeam(m.Name, teamName, m.Role, m.BriefOverride); err != nil {
			return fmt.Errorf("apply template %s: join %s: %w", p.Name, m.Name, err)
		}
	}
	rt.RecordEvent(runtime.Event{
		Kind: "template_applied", Actor: "runtime",
		Body: fmt.Sprintf("template %q applied as team %q with %d members", p.Name, teamName, len(p.Members)),
		Meta: map[string]any{"template": p.Name, "team": teamName, "members": len(p.Members)},
	})
	return nil
}

func mapRole(r runtime.MembershipRole) runtime.Role {
	if r == runtime.RoleMemberShepherd {
		return runtime.RoleShepherd
	}
	return runtime.RoleWorker
}
