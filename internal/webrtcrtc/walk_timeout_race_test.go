//go:build race

package webrtcrtc

import "time"

// walkTimeout scales live-handshake deadlines when the race detector is
// active. Real pion DTLS/ICE IO runs 2–10× slower under -race (detector
// overhead + contended CI), so the fixed wall-clock deadlines in the
// LIVE-WALK tests must account for that or they flake (#723). This is NOT
// blind widening: the base deadline is unchanged for normal runs; only
// the race job — where the slowdown is real and known — gets the extra
// room. Keeps the valuable race coverage of the concurrent
// OnOpen/OnMessage/recvBuffer callbacks (cf. #717) instead of skipping
// the walk under race.
func walkTimeout(base time.Duration) time.Duration { return base * 5 }
