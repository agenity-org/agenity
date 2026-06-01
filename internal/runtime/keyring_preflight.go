package runtime

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// KeyringPreflightResult holds the parsed state of the kernel keyring
// quota for the current UID (read from /proc/key-users).
type KeyringPreflightResult struct {
	UID      int
	Used     int
	MaxKeys  int
	Exceeded bool // Used >= MaxKeys
	Warning  bool // Used >= 80% of MaxKeys
	Raw      string
}

// KeyringPreflight reads /proc/key-users for the current UID and returns
// quota state. Returns nil on non-Linux platforms or when /proc/key-users
// is unavailable (bare CI images, macOS dev boxes).
//
// Caller should emit a loud warning when Warning=true and an inbox failure
// when Exceeded=true. See #592 — kernel keyring exhaustion silently
// prevents OCI containers from starting (setns/newuidmap fail with EDQUOT).
func KeyringPreflight() *KeyringPreflightResult {
	data, err := os.ReadFile("/proc/key-users")
	if err != nil {
		return nil
	}
	uid := os.Getuid()
	return parseKeyUsers(string(data), uid)
}

// parseKeyUsers parses /proc/key-users content for a specific UID.
// Format (Linux kernel):
//
//	<uid>: <usage> <nkeys>/<maxkeys> <nbytes>/<maxbytes>
//	    0:      4 2/200 36/20000
//	 1000:    137 137/200 14432/20000
func parseKeyUsers(content string, uid int) *KeyringPreflightResult {
	prefix := fmt.Sprintf("%5d:", uid)
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		// line: "  UID: usage nkeys/maxkeys nbytes/maxbytes"
		fields := strings.Fields(line)
		// fields[0]="UID:", fields[1]=usage, fields[2]="nkeys/maxkeys", fields[3]="nbytes/maxbytes"
		if len(fields) < 3 {
			continue
		}
		parts := strings.SplitN(fields[2], "/", 2)
		if len(parts) != 2 {
			continue
		}
		used, errU := strconv.Atoi(parts[0])
		maxKeys, errM := strconv.Atoi(parts[1])
		if errU != nil || errM != nil || maxKeys <= 0 {
			continue
		}
		return &KeyringPreflightResult{
			UID:      uid,
			Used:     used,
			MaxKeys:  maxKeys,
			Exceeded: used >= maxKeys,
			Warning:  used*100/maxKeys >= 80,
			Raw:      line,
		}
	}
	return nil
}
