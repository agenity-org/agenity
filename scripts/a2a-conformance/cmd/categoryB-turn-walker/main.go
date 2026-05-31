// scripts/a2a-conformance/cmd/categoryB-turn-walker/main.go drives
// the v0.9.4 QA Category B cell B.4 walk against a running
// chepherd-hub binary:
//
//  1. POST /v1/turn/credentials → mint REST creds
//  2. pion/turn/v5 Client.Allocate against the real hub UDP listener
//     with minted creds → assert RELAYED-ADDRESS returned
//  3. Tamper password → re-Allocate → assert pion returns auth fail
//  4. Hub /healthz before + after to capture active_allocations
//     counter increment
//
// Output: writes evidence files under --out-dir.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

func main() {
	hubURL := flag.String("hub-url", "", "")
	turnUDP := flag.String("turn-udp", "", "")
	asOrg := flag.String("as-org", "alice.example", "")
	outDir := flag.String("out-dir", "", "")
	flag.Parse()
	if *hubURL == "" || *turnUDP == "" || *outDir == "" {
		log.Fatalf("--hub-url, --turn-udp, --out-dir required")
	}

	// ─── Healthz BEFORE ──────────────────────────────────────────
	mustWriteHealthz(*hubURL, filepath.Join(*outDir, "B4-healthz.before.json"))

	// ─── Mint REST creds via the hub HTTP endpoint ──────────────
	creds := mintCreds(*hubURL, *asOrg, filepath.Join(*outDir, "B4-mint"))

	// ─── pion Allocate with VALID creds ──────────────────────────
	allocVerdict, relayAddr := runAllocate(*turnUDP, creds.Username, creds.Password, filepath.Join(*outDir, "B4-allocate-valid.log"))
	fmt.Printf("VALID allocate: %s relay=%s\n", allocVerdict, relayAddr)

	// ─── Healthz DURING (between Allocate + Close) ───────────────
	// Note: runAllocate above already closed the alloc on exit. Re-do
	// with a held-open allocate to capture the during-state.
	relayAddr2 := runAllocateHeldOpen(*turnUDP, creds.Username, creds.Password, filepath.Join(*outDir, "B4-allocate-heldopen.log"),
		filepath.Join(*outDir, "B4-healthz.during.json"), *hubURL)
	fmt.Printf("HELD-OPEN relay=%s\n", relayAddr2)

	// ─── Healthz AFTER (post-close) ──────────────────────────────
	// Short sleep so OnAllocationDeleted callback decrements counter.
	time.Sleep(500 * time.Millisecond)
	mustWriteHealthz(*hubURL, filepath.Join(*outDir, "B4-healthz.after.json"))

	// ─── pion Allocate with TAMPERED password ────────────────────
	tamperVerdict, _ := runAllocate(*turnUDP, creds.Username, "xx"+creds.Password+"xx",
		filepath.Join(*outDir, "B4-allocate-tampered.log"))
	fmt.Printf("TAMPERED allocate: %s\n", tamperVerdict)
}

type credsResp struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	TTL      int      `json:"ttl"`
	URIs     []string `json:"uris"`
	Realm    string   `json:"realm"`
}

func mintCreds(hubURL, asOrg, outPrefix string) credsResp {
	req, _ := http.NewRequest("POST", hubURL+"/v1/turn/credentials", nil)
	req.Header.Set("X-Chepherd-Org", asOrg)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("mint creds: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_ = os.WriteFile(outPrefix+".resp.json", body, 0o644)
	_ = os.WriteFile(outPrefix+".http", []byte(fmt.Sprintf("http=%d\n", resp.StatusCode)), 0o644)
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("mint creds non-200: %d body=%s", resp.StatusCode, body)
	}
	var c credsResp
	if err := json.Unmarshal(body, &c); err != nil {
		log.Fatalf("decode creds: %v", err)
	}
	return c
}

func runAllocate(turnUDP, user, pass, outLog string) (verdict, relayAddr string) {
	f, _ := os.Create(outLog)
	defer f.Close()

	conn, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		fmt.Fprintf(f, "ListenPacket: %v\n", err)
		return "LISTEN-FAIL", ""
	}
	defer conn.Close()

	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel = logging.LogLevelTrace
	loggerFactory.Writer = f

	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: turnUDP,
		TURNServerAddr: turnUDP,
		Conn:           conn,
		Username:       user,
		Password:       pass,
		Realm:          "chepherd-hub",
		LoggerFactory:  loggerFactory,
	})
	if err != nil {
		fmt.Fprintf(f, "NewClient: %v\n", err)
		return "NEWCLIENT-FAIL", ""
	}
	defer client.Close()

	if err := client.Listen(); err != nil {
		fmt.Fprintf(f, "Listen: %v\n", err)
		return "LISTEN-FAIL", ""
	}

	relayConn, err := client.Allocate()
	if err != nil {
		fmt.Fprintf(f, "Allocate FAILED: %v\n", err)
		s := err.Error()
		if strings.Contains(s, "401") || strings.Contains(s, "Unauthorized") || strings.Contains(s, "Wrong") {
			return "ALLOCATE-AUTHFAIL", ""
		}
		return "ALLOCATE-FAIL", ""
	}
	defer relayConn.Close()
	addr := relayConn.LocalAddr()
	fmt.Fprintf(f, "Allocate OK relay=%s\n", addr)
	return "ALLOCATE-OK", addr.String()
}

func runAllocateHeldOpen(turnUDP, user, pass, outLog, healthzPath, hubURL string) string {
	f, _ := os.Create(outLog)
	defer f.Close()

	conn, _ := net.ListenPacket("udp4", "0.0.0.0:0")
	defer conn.Close()
	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel = logging.LogLevelWarn
	loggerFactory.Writer = f
	client, _ := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: turnUDP,
		TURNServerAddr: turnUDP,
		Conn:           conn,
		Username:       user,
		Password:       pass,
		Realm:          "chepherd-hub",
		LoggerFactory:  loggerFactory,
	})
	defer client.Close()
	_ = client.Listen()
	relayConn, err := client.Allocate()
	if err != nil {
		fmt.Fprintf(f, "Allocate FAILED: %v\n", err)
		return ""
	}
	// Capture healthz while alloc is held open
	mustWriteHealthz(hubURL, healthzPath)
	addr := relayConn.LocalAddr().String()
	_ = relayConn.Close()
	return addr
}

func mustWriteHealthz(hubURL, outPath string) {
	resp, err := http.Get(hubURL + "/healthz")
	if err != nil {
		log.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_ = os.WriteFile(outPath, body, 0o644)
}
