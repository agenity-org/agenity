//go:build !race

package main

import "time"

// walkTimeout is the identity for normal (non-race) runs — the live-walk
// deadlines are comfortable at full IO speed. Only the -race build scales
// them up (see walk_timeout_race_test.go, #723 family).
func walkTimeout(base time.Duration) time.Duration { return base }
