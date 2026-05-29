// Package runtime — Team: a logical group of agents with its own canon
// + topology. v0.6 first-class object. Agents and Teams are joined via
// Membership records (see membership.go).
package runtime

import "time"

// Topology describes how agents in a team are allowed to talk to each other.
type Topology string

const (
	// TopologyHub — one shepherd watches workers; workers don't @-each-other
	// directly (all routing through the shepherd's coaching path). Default
	// for catalog templates.
	TopologyHub Topology = "hub"
	// TopologyMesh — no shepherd required; agents @-each-other freely. Use
	// when the operator wants peer collaboration without supervision.
	TopologyMesh Topology = "mesh"
	// TopologyCustom — arbitrary graph defined by explicit grants + roles.
	TopologyCustom Topology = "custom"
)

// Team groups agents under a shared canon. Topology constrains routing.
type Team struct {
	Name      string    `json:"name"`
	CanonPath string    `json:"canon_path,omitempty"` // path to team's CLAUDE.md (re-read per shepherd tick)
	Topology  Topology  `json:"topology"`
	CreatedAt time.Time `json:"created_at"`
}

// DefaultTeam returns a stock team with hub topology and a derived canon
// path under the chepherd state dir.
func DefaultTeam(name, stateDir string) Team {
	return Team{
		Name:      name,
		CanonPath: stateDir + "/teams/" + name + "/CLAUDE.md",
		Topology:  TopologyHub,
		CreatedAt: time.Now().UTC(),
	}
}
