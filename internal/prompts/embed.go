// Package prompts embeds chepherd's default system prompts for worker
// agents and shepherd (the meta-supervisor watcher) agents.
//
// These are baked into the binary so chepherd ships with a usable
// out-of-box configuration. Operators can override per-session via
// SpawnSpec.SystemPrompt when spawning.
package prompts

import _ "embed"

//go:embed worker.md
var Worker string

//go:embed shepherd.md
var ScrumMaster string
