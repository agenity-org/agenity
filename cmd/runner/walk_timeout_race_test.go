//go:build race

package main

import "time"

// walkTimeout scales live-walk deadlines when the race detector is active.
// Real DTLS/ICE IO runs 2–10× slower under -race (detector overhead +
// contended CI), so the F2 two-runner DataChannel walk's fixed deadlines
// flake. This is the cmd/runner sibling of the internal/webrtcrtc helper
// (#723 family — F4 there, F2 here). NOT blind widening: identity in normal
// runs (walk_timeout_norace_test.go); only the -race job gets the headroom,
// preserving its concurrency coverage instead of skipping the walk.
func walkTimeout(base time.Duration) time.Duration { return base * 5 }
