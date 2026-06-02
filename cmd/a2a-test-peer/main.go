// chepherd-a2a-test-peer — an independent A2A-aware agent that joins a
// chepherd team to validate the operator↔agent + agent↔agent delivery
// loop end-to-end.
//
// This is NOT a chepherd-runner. It speaks the A2A v1.0 spec on its own
// HTTP listener (/.well-known/agent-card.json + /jsonrpc with message/send)
// AND polls the chepherd daemon's team transcript to receive messages
// addressed to its @-handle (since chepherd's local Deliverer routes
// via PTY today; this peer is HTTP-addressable so the transcript-poll
// path is the integration seam until a federation deliverer hop exists).
//
// Usage:
//
//	chepherd-a2a-test-peer \
//	    --daemon-url http://127.0.0.1:8083 \
//	    --token "$(cat ~/.local/state/chepherd/auth.printed)" \
//	    --name external-peer \
//	    --team default \
//	    --listen 127.0.0.1:18099
//
// What it does:
//  1. Stands up an A2A v1.0 HTTP server with /.well-known/agent-card.json
//     + /jsonrpc (message/send echoes back with role=agent body=ACK).
//  2. Polls $daemon-url/api/v1/teams/$team/messages every 2s.
//  3. For each new message where recipients contains its @-handle (or
//     "everyone" / its role), logs the message AND posts an ACK back
//     to the team transcript so the operator sees a closed loop.
//  4. Prints every action to stdout for verification.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var (
	flagDaemon   = flag.String("daemon-url", "http://127.0.0.1:8083", "chepherd daemon HTTP base URL")
	flagToken    = flag.String("token", "", "chepherd Bearer token (required)")
	flagName     = flag.String("name", "external-peer", "@-handle this peer registers under")
	flagTeam     = flag.String("team", "default", "team to join (poll transcript + post ACKs)")
	flagListen   = flag.String("listen", "127.0.0.1:18099", "address to bind the A2A HTTP listener on")
	flagPoll     = flag.Duration("poll", 2*time.Second, "transcript poll interval")
	flagAutoAck  = flag.Bool("auto-ack", true, "auto-respond ACK to messages addressed to this peer")
	flagQuiet    = flag.Bool("quiet", false, "suppress per-poll heartbeat logs")
)

// AgentCard mirrors the A2A v1.0 spec shape served at
// /.well-known/agent-card.json.
type AgentCard struct {
	Name                 string   `json:"name"`
	Description          string   `json:"description"`
	URL                  string   `json:"url"`
	ProtocolVersion      string   `json:"protocolVersion"`
	PreferredTransport   string   `json:"preferredTransport"`
	DefaultInputModes    []string `json:"defaultInputModes"`
	DefaultOutputModes   []string `json:"defaultOutputModes"`
	Provider             struct {
		Organization string `json:"organization"`
	} `json:"provider"`
	Capabilities struct {
		Streaming         bool `json:"streaming"`
		PushNotifications bool `json:"pushNotifications"`
	} `json:"capabilities"`
}

func main() {
	flag.Parse()
	if *flagToken == "" {
		fmt.Fprintln(os.Stderr, "--token required (use $(cat ~/.local/state/chepherd/auth.printed))")
		os.Exit(2)
	}

	logf := func(format string, args ...any) {
		log.Printf("[a2a-test-peer:"+*flagName+"] "+format, args...)
	}

	// ─── A2A HTTP server side ─────────────────────────────────────────
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", func(w http.ResponseWriter, r *http.Request) {
		card := AgentCard{
			Name:               *flagName,
			Description:        "Independent A2A v1.0 test peer — joins a chepherd team to validate mixed-agent delivery.",
			URL:                "http://" + *flagListen + "/jsonrpc",
			ProtocolVersion:    "1.0",
			PreferredTransport: "JSONRPC",
			DefaultInputModes:  []string{"text/plain"},
			DefaultOutputModes: []string{"text/plain"},
		}
		card.Provider.Organization = "chepherd-test"
		card.Capabilities.Streaming = false
		card.Capabilities.PushNotifications = false
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(card)
	})
	mux.HandleFunc("/jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		logf("A2A inbound: method=%s body=%s", req.Method, truncate(string(body), 200))
		// Generic A2A wire ACK
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + safeID(req.ID) + `,"result":{"task":{"id":"echo","status":{"state":"COMPLETED"}}}}`))
	})

	srv := &http.Server{Addr: *flagListen, Handler: mux}
	go func() {
		logf("A2A listener up on http://%s (agent-card at /.well-known/agent-card.json)", *flagListen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	// ─── Daemon-transcript poll loop ─────────────────────────────────
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	seen := map[string]struct{}{}
	ticker := time.NewTicker(*flagPoll)
	defer ticker.Stop()

	prime, err := fetchTranscript()
	if err != nil {
		logf("initial fetch err (will keep trying): %v", err)
	} else {
		// Mark every pre-existing message as seen so we don't ACK history.
		for _, m := range prime {
			seen[m.ID] = struct{}{}
		}
		logf("primed %d historical messages (will skip)", len(prime))
	}

	logf("polling %s/api/v1/teams/%s/messages every %s — addressed-to=@%s",
		*flagDaemon, *flagTeam, *flagPoll, *flagName)

	for {
		select {
		case <-stop:
			logf("shutdown signal, exiting")
			_ = srv.Close()
			return
		case <-ticker.C:
			msgs, err := fetchTranscript()
			if err != nil {
				if !*flagQuiet {
					logf("poll err: %v", err)
				}
				continue
			}
			for _, m := range msgs {
				if _, dup := seen[m.ID]; dup {
					continue
				}
				seen[m.ID] = struct{}{}
				// Only react if addressed to us. Avoid ACK loops by
				// skipping our own messages.
				if m.Author == *flagName {
					continue
				}
				if !addressedTo(m, *flagName) {
					continue
				}
				logf("RECEIVED from @%s: %q (id=%s, recipients=%v)",
					m.Author, m.Body, m.ID[:8], m.Recipients)
				if *flagAutoAck {
					ack := fmt.Sprintf("ACK from @%s — received your message (%s)",
						*flagName, time.Now().UTC().Format(time.RFC3339))
					if err := postAck(*flagName, ack); err != nil {
						logf("ack err: %v", err)
					} else {
						logf("ACK posted to team transcript")
					}
				}
			}
		}
	}
}

// transcriptMessage matches the chepherd transcript row shape.
type transcriptMessage struct {
	ID         string   `json:"id"`
	Author     string   `json:"author"`
	Body       string   `json:"body"`
	Recipients []string `json:"recipients"`
	CreatedAt  string   `json:"created_at"`
}

func fetchTranscript() ([]transcriptMessage, error) {
	req, _ := http.NewRequest("GET", *flagDaemon+"/api/v1/teams/"+*flagTeam+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+*flagToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 100))
	}
	var env struct {
		Messages []transcriptMessage `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, err
	}
	return env.Messages, nil
}

func postAck(author, body string) error {
	payload, _ := json.Marshal(map[string]any{"author": author, "body": body})
	req, _ := http.NewRequest("POST",
		*flagDaemon+"/api/v1/teams/"+*flagTeam+"/messages",
		bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+*flagToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 100))
	}
	return nil
}

func addressedTo(m transcriptMessage, name string) bool {
	for _, r := range m.Recipients {
		if r == name || r == "everyone" {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func safeID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	return string(raw)
}
