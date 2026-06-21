// internal/runtime/d4_resume.go — #350 D4 auto-resume hook.
//
// Pragmatic scope:
//   - Runtime.ResumableSessions(ctx) exposes the SessionRepository's
//     resumable-set list as a SpawnSpec template list
//   - cmd/run.go (or an operator-runtime caller) iterates the result
//     + calls Runtime.Spawn(spec) for each, with --resume <uuid>
//     appended to AgentArgs
//
// Why not auto-Spawn inside NewWithStore: Spawn needs an initialized
// ContainerRuntime + session.Manager pipeline that NewWithStore returns
// to its caller — calling Spawn from inside the constructor before the
// caller wires the runtime would deadlock the spawner. cmd/run.go is
// the natural orchestration point.
//
// Refs #350 D4.
package runtime

import (
	"context"

	"github.com/agenity-org/agenity/internal/persistence"
)

// ResumableSessions queries the SessionRepository for sessions with a
// non-empty claude_session_uuid + not-exited state. Returns []SpawnSpec
// pre-populated with --resume <uuid> in AgentArgs so the caller can
// invoke Runtime.Spawn(spec) for each.
//
// Returns nil + nil when no persistence Store is wired (back-compat
// for v0.9.1 file-on-disk callers).
//
// Refs #350 D4.
func (r *Runtime) ResumableSessions(ctx context.Context) ([]SpawnSpec, error) {
	if r.sessionsRepo == nil {
		return nil, nil
	}
	resumable, err := r.sessionsRepo.ResumableSessions(ctx)
	if err != nil {
		return nil, err
	}
	return toSpawnSpecs(resumable), nil
}

func toSpawnSpecs(sessions []persistence.ResumableSession) []SpawnSpec {
	out := make([]SpawnSpec, 0, len(sessions))
	for _, s := range sessions {
		spec := SpawnSpec{
			Name:      s.Name,
			AgentSlug: s.AgentSlug,
			Team:      s.Team,
			Cwd:       s.Cwd,
			Role:      RoleWorker,
		}
		if spec.AgentSlug == "" {
			spec.AgentSlug = "claude-code"
		}
		spec.AgentArgs = append(spec.AgentArgs, "--resume", s.ClaudeSessionUUID)
		out = append(out, spec)
	}
	return out
}
