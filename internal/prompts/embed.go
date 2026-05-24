// Package prompts embeds chepherd's default system prompts for Adam
// (the worker default) and Chepherd (the meta-shepherd watcher).
//
// These are baked into the binary so chepherd ships with a usable
// out-of-box configuration. Operators can override per-session via
// SpawnSpec.SystemPrompt when spawning.
package prompts

import _ "embed"

//go:embed adam.md
var Adam string

//go:embed shepherd.md
var Shepherd string
