// scripts/a2a-conformance/cmd/categoryB-tunnel-walker/main.go drives
// the v0.9.4 QA Category B cell B.5 walk against a running chepherd-
// hub binary:
//
//  1. Stub "bob runner": dials ws://hub/v1/relay/tunnel with
//     X-Chepherd-Org: bob.example header. Read pump echoes inbound
//     frames back with Direction=to-hub + same body bytes
//     (body-blind invariant probe — bob is the only one who sees
//     the cleartext).
//  2. Caller "alice": POSTs HTTP to /v1/relay/bob.example/jsonrpc
//     with 1 KB random body + X-Chepherd-Org: alice.example header.
//     Asserts response body bytes match what was sent (round-trip).
//  3. Compute SHA-256 in-and-out, assert MATCH for body-blind.
//  4. NEG: POST to /v1/relay/carol.example/jsonrpc (no tunnel) →
//     expect 502.
//  5. NEG: disconnect bob mid-test, alice's next POST → expect 502
//     or 504.
//
// Output: writes evidence files under --out-dir.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type relayFrame struct {
	RequestID string            `json:"requestId"`
	Direction string            `json:"direction"`
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      []byte            `json:"body,omitempty"`
	Status    int               `json:"status,omitempty"`
}

func main() {
	hubURL := flag.String("hub-url", "", "")
	wsURL := flag.String("ws-url", "", "")
	outDir := flag.String("out-dir", "", "")
	flag.Parse()
	if *hubURL == "" || *wsURL == "" || *outDir == "" {
		log.Fatalf("--hub-url, --ws-url, --out-dir required")
	}
	_ = os.MkdirAll(*outDir, 0o755)

	// ─── Spin up the stub "bob runner" WS client ───────────────────
	bobConn, _, err := websocket.DefaultDialer.Dial(*wsURL+"/v1/relay/tunnel",
		http.Header{"X-Chepherd-Org": []string{"bob.example"}})
	if err != nil {
		log.Fatalf("bob WS dial: %v", err)
	}
	defer bobConn.Close()
	fmt.Println("bob: WS connected to hub")

	var writeMu sync.Mutex
	bobDone := make(chan struct{})
	go func() {
		defer close(bobDone)
		for {
			_, payload, err := bobConn.ReadMessage()
			if err != nil {
				fmt.Printf("bob: read err: %v\n", err)
				return
			}
			var frame relayFrame
			if err := json.Unmarshal(payload, &frame); err != nil {
				continue
			}
			if frame.Direction != "to-runner" {
				continue
			}
			fmt.Printf("bob: got to-runner frame reqID=%s method=%s path=%s bodyLen=%d\n",
				frame.RequestID, frame.Method, frame.Path, len(frame.Body))
			// Body-blind probe — echo bytes back verbatim
			respFrame := relayFrame{
				RequestID: frame.RequestID,
				Direction: "to-hub",
				Status:    200,
				Headers:   map[string]string{"Content-Type": "application/octet-stream"},
				Body:      frame.Body,
			}
			respBytes, _ := json.Marshal(respFrame)
			writeMu.Lock()
			err = bobConn.WriteMessage(websocket.TextMessage, respBytes)
			writeMu.Unlock()
			if err != nil {
				fmt.Printf("bob: write err: %v\n", err)
				return
			}
		}
	}()

	// Wait a moment for tunnel to register
	time.Sleep(200 * time.Millisecond)

	// ─── Alice POST → hub → bob → echo back ────────────────────────
	payloadIn := make([]byte, 1024)
	_, _ = rand.Read(payloadIn)
	shaIn := sha256.Sum256(payloadIn)
	_ = os.WriteFile(filepath.Join(*outDir, "B5-payload-in.bin"), payloadIn, 0o644)
	_ = os.WriteFile(filepath.Join(*outDir, "B5-sha-in.hex"), []byte(hex.EncodeToString(shaIn[:])), 0o644)

	req, _ := http.NewRequest("POST", *hubURL+"/v1/relay/bob.example/jsonrpc", bytes.NewReader(payloadIn))
	req.Header.Set("X-Chepherd-Org", "alice.example")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("alice POST: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	shaOut := sha256.Sum256(respBody)
	_ = os.WriteFile(filepath.Join(*outDir, "B5-payload-out.bin"), respBody, 0o644)
	_ = os.WriteFile(filepath.Join(*outDir, "B5-sha-out.hex"), []byte(hex.EncodeToString(shaOut[:])), 0o644)
	_ = os.WriteFile(filepath.Join(*outDir, "B5-success.meta"),
		[]byte(fmt.Sprintf("http=%d in_sha=%s out_sha=%s match=%v in_len=%d out_len=%d\n",
			resp.StatusCode, hex.EncodeToString(shaIn[:]), hex.EncodeToString(shaOut[:]), shaIn == shaOut, len(payloadIn), len(respBody))), 0o644)

	if resp.StatusCode == 200 && shaIn == shaOut {
		fmt.Println("PROBE 1 SUCCESS: round-trip + body-blind SHA MATCH")
	} else {
		fmt.Printf("PROBE 1 FAIL: status=%d sha-match=%v\n", resp.StatusCode, shaIn == shaOut)
	}

	// ─── NEG: alice POSTs to org with no tunnel ────────────────────
	negReq, _ := http.NewRequest("POST", *hubURL+"/v1/relay/carol.example/jsonrpc",
		strings.NewReader(`{"x":"y"}`))
	negReq.Header.Set("X-Chepherd-Org", "alice.example")
	negResp, err := http.DefaultClient.Do(negReq)
	negStatus := 0
	negBody := ""
	if err == nil {
		negStatus = negResp.StatusCode
		b, _ := io.ReadAll(negResp.Body)
		negBody = string(b)
		negResp.Body.Close()
	}
	_ = os.WriteFile(filepath.Join(*outDir, "B5-neg-notunnel.meta"),
		[]byte(fmt.Sprintf("http=%d body=%s\n", negStatus, negBody)), 0o644)
	fmt.Printf("PROBE 2 (no tunnel for carol): http=%d body=%s\n", negStatus, negBody)

	// ─── NEG: disconnect bob, alice's next POST should fail ─────────
	fmt.Println("--- disconnecting bob WS ---")
	bobConn.Close()
	<-bobDone
	time.Sleep(200 * time.Millisecond)

	discReq, _ := http.NewRequest("POST", *hubURL+"/v1/relay/bob.example/jsonrpc",
		bytes.NewReader([]byte("after-disconnect")))
	discReq.Header.Set("X-Chepherd-Org", "alice.example")
	discResp, err := http.DefaultClient.Do(discReq)
	discStatus := 0
	discBody := ""
	if err == nil {
		discStatus = discResp.StatusCode
		b, _ := io.ReadAll(discResp.Body)
		discBody = string(b)
		discResp.Body.Close()
	} else {
		discBody = "err: " + err.Error()
	}
	_ = os.WriteFile(filepath.Join(*outDir, "B5-neg-disconnect.meta"),
		[]byte(fmt.Sprintf("http=%d body=%s\n", discStatus, discBody)), 0o644)
	fmt.Printf("PROBE 3 (after disconnect): http=%d body=%s\n", discStatus, discBody)

	// ─── Healthz tunnel counters ───────────────────────────────────
	hzResp, err := http.Get(*hubURL + "/healthz")
	if err == nil {
		hzBody, _ := io.ReadAll(hzResp.Body)
		_ = hzResp.Body.Close()
		_ = os.WriteFile(filepath.Join(*outDir, "B5-healthz.json"), hzBody, 0o644)
		fmt.Printf("healthz: %s\n", string(hzBody))
	}

	// Use url so 'unused import' lint doesn't fire if I removed it
	_ = url.URL{}
}
