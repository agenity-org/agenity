// cmd/chepherd-hub/p0_495_signaling_walk_test.go is the v0.9.4 §10
// Pattern 2 Phase 5 LIVE WALK gate for #495 Wave F5 — boots a real
// chepherd-hub binary, runs the cross-org offer→answer→ICE exchange
// through it, asserts:
//
//  1. Each frame relays to the correct recipient.
//  2. Body-blind invariant: payload bytes are EXACT round-trip.
//  3. Spoofed fromOrgId → 403.
//  4. --allowed-orgs allowlist honored.
//
// Refs #495 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 5.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV094Walk_F5_CrossOrg_OfferAnswerICEThroughRealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live walk in -short")
	}
	gomodOut, _ := exec.Command("go", "env", "GOMOD").Output()
	repoRoot := filepath.Dir(strings.TrimSpace(string(gomodOut)))
	tmpDir := t.TempDir()
	bin := filepath.Join(tmpDir, "chepherd-hub")
	build := exec.Command("go", "build", "-o", bin, "./cmd/chepherd-hub")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build chepherd-hub: %v\n%s", err, out)
	}
	port := freePort(t)
	cmd := exec.Command(bin,
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--stun-listen", "",
		"--turn-listen", "",
		"--allowed-orgs", "alice.example,bob.example",
	)
	logFile, _ := os.CreateTemp("", "hub-f5-live-*.log")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_, _ = cmd.Process.Wait()
		if t.Failed() && logFile != nil {
			if b, err := os.ReadFile(logFile.Name()); err == nil {
				t.Logf("hub log:\n%s", b)
			}
		}
	})
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			break
		}
		if err == nil {
			resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}

	post := func(path, asOrg, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", baseURL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Chepherd-Org", asOrg)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}
	get := func(path, asOrg string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", baseURL+path, nil)
		req.Header.Set("X-Chepherd-Org", asOrg)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp
	}

	sdpOfferPayload := []byte(`{"sdp":"v=0\r\no=- 7 2 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\na=fingerprint:sha-256 AB:CD:EF\r\n"}`)
	offerBody, _ := json.Marshal(map[string]any{
		"toOrgId":   "bob.example",
		"sessionId": "f5-walk",
		"payload":   json.RawMessage(sdpOfferPayload),
	})
	r := post("/v1/signaling/offer", "alice.example", string(offerBody))
	if r.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		t.Fatalf("POST offer = %d, want 202\n%s", r.StatusCode, b)
	}
	r.Body.Close()

	spoofBody, _ := json.Marshal(map[string]any{
		"fromOrgId": "carol.example",
		"toOrgId":   "bob.example",
		"sessionId": "f5-walk",
		"payload":   json.RawMessage(`{}`),
	})
	rs := post("/v1/signaling/offer", "alice.example", string(spoofBody))
	if rs.StatusCode != http.StatusForbidden {
		t.Errorf("spoofed offer = %d, want 403", rs.StatusCode)
	}
	rs.Body.Close()

	bp := get("/v1/signaling/pending?orgId=bob.example", "bob.example")
	defer bp.Body.Close()
	if bp.StatusCode != http.StatusOK {
		t.Fatalf("bob pending = %d, want 200", bp.StatusCode)
	}
	var pending struct {
		Frames []*SignalingFrame `json:"frames"`
		Count  int               `json:"count"`
	}
	_ = json.NewDecoder(bp.Body).Decode(&pending)
	if pending.Count != 1 {
		t.Fatalf("bob pending count = %d, want 1", pending.Count)
	}
	if pending.Frames[0].Kind != SignalingOffer {
		t.Errorf("kind = %q, want offer", pending.Frames[0].Kind)
	}
	if !bytes.Equal(pending.Frames[0].Payload, sdpOfferPayload) {
		t.Errorf("payload bytes mutated by hub:\n got: %s\nwant: %s",
			pending.Frames[0].Payload, sdpOfferPayload)
	}

	answerBody, _ := json.Marshal(map[string]any{
		"toOrgId":   "alice.example",
		"sessionId": "f5-walk",
		"payload":   json.RawMessage(`{"sdp":"bob-answer"}`),
	})
	r = post("/v1/signaling/answer", "bob.example", string(answerBody))
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("answer = %d, want 202", r.StatusCode)
	}
	r.Body.Close()

	ap := get("/v1/signaling/pending?orgId=alice.example", "alice.example")
	defer ap.Body.Close()
	var aPend struct {
		Frames []*SignalingFrame `json:"frames"`
	}
	_ = json.NewDecoder(ap.Body).Decode(&aPend)
	if len(aPend.Frames) != 1 || aPend.Frames[0].Kind != SignalingAnswer {
		t.Errorf("alice pending = %+v, want 1 answer", aPend.Frames)
	}

	r = post("/v1/signaling/ice", "alice.example", `{"toOrgId":"bob.example","sessionId":"f5-walk","payload":{"candidate":"alice-ice"}}`)
	r.Body.Close()
	r = post("/v1/signaling/ice", "bob.example", `{"toOrgId":"alice.example","sessionId":"f5-walk","payload":{"candidate":"bob-ice"}}`)
	r.Body.Close()

	bp2 := get("/v1/signaling/pending?orgId=bob.example", "bob.example")
	var bPend2 struct {
		Frames []*SignalingFrame `json:"frames"`
	}
	_ = json.NewDecoder(bp2.Body).Decode(&bPend2)
	bp2.Body.Close()
	if len(bPend2.Frames) != 1 || bPend2.Frames[0].Kind != SignalingICE {
		t.Errorf("bob ice poll = %+v, want 1 ice", bPend2.Frames)
	}
	ap2 := get("/v1/signaling/pending?orgId=alice.example", "alice.example")
	var aPend2 struct {
		Frames []*SignalingFrame `json:"frames"`
	}
	_ = json.NewDecoder(ap2.Body).Decode(&aPend2)
	ap2.Body.Close()
	if len(aPend2.Frames) != 1 || aPend2.Frames[0].Kind != SignalingICE {
		t.Errorf("alice ice poll = %+v, want 1 ice", aPend2.Frames)
	}

	rej := post("/v1/signaling/offer", "carol.example",
		`{"toOrgId":"bob.example","sessionId":"x","payload":{}}`)
	if rej.StatusCode != http.StatusForbidden {
		t.Errorf("carol (non-allowlisted) offer = %d, want 403", rej.StatusCode)
	}
	rej.Body.Close()

	t.Logf("F5 live walk: cross-org offer/answer/ICE relayed through real chepherd-hub; payload bytes body-blind; spoof + allowlist rejected")
}
