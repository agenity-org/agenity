//go:build !race

package webrtcrtc

import "time"

// walkTimeout is the identity for normal (non-race) test runs — the
// LIVE-WALK deadlines are already comfortable when pion's real DTLS/ICE
// IO runs at full speed. Only the -race build scales them up (see
// walk_timeout_race_test.go, #723).
func walkTimeout(base time.Duration) time.Duration { return base }
