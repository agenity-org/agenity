// APNs dispatcher — Apple Push Notification service v2 (HTTP/2 + JWT).
//
// Two auth modes supported (env-driven):
//   1) Certificate-based (.p12 / .pem)  — legacy but still common
//   2) Token-based (.p8 + team_id + key_id) — modern, rotates cheaply
//
// When neither set of env vars is complete, this file returns nil
// from newAPNsFromEnv and the push package never instantiates the
// backend (callers see ErrBackendDisabled).

package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"
)

type apnsDispatcher struct {
	client *apns2.Client
}

// Kind returns "apns".
func (a *apnsDispatcher) Kind() string { return "apns" }

// Send marshals the Notification into an APNs alert envelope.
func (a *apnsDispatcher) Send(ctx context.Context, tok Token, n Notification) error {
	if tok.BundleID == "" {
		return errors.New("apns: bundle id required")
	}
	p := payload.NewPayload().
		AlertTitle(n.Title).
		AlertBody(n.Body).
		Custom("session_uuid", n.SessionUUID)
	if n.Sound != "" {
		p.Sound(n.Sound)
	}
	notif := &apns2.Notification{
		DeviceToken: tok.Value,
		Topic:       tok.BundleID,
		Payload:     p,
		CollapseID:  n.CollapseKey,
	}
	if n.TTL > 0 {
		notif.Expiration = time.Now().Add(n.TTL)
	}
	switch n.Priority {
	case PriorityHigh:
		notif.Priority = apns2.PriorityHigh
	default:
		notif.Priority = apns2.PriorityLow
	}
	// Context propagation: the apns2 client doesn't take a context, but
	// the underlying http2 transport honours deadline derivable here.
	done := make(chan error, 1)
	go func() {
		res, err := a.client.Push(notif)
		if err != nil {
			done <- err
			return
		}
		if !res.Sent() {
			done <- fmt.Errorf("apns: rejected status=%d reason=%s", res.StatusCode, res.Reason)
			return
		}
		done <- nil
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// newAPNsFromEnv returns nil when no auth path is fully configured.
func newAPNsFromEnv(_ context.Context) Dispatcher {
	production, _ := strconv.ParseBool(envDefault("CHEPHERD_RELAY_APNS_PRODUCTION", "false"))

	// Token-based first (preferred).
	teamID := os.Getenv("CHEPHERD_RELAY_APNS_TEAM_ID")
	keyID := os.Getenv("CHEPHERD_RELAY_APNS_KEY_ID")
	keyPath := os.Getenv("CHEPHERD_RELAY_APNS_KEY_PATH")
	if teamID != "" && keyID != "" && keyPath != "" {
		raw, err := os.ReadFile(keyPath)
		if err != nil {
			log.Printf("push apns: read key %s: %v", keyPath, err)
			return nil
		}
		block, _ := pem.Decode(raw)
		if block == nil {
			log.Println("push apns: invalid pem key file")
			return nil
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			log.Printf("push apns: parse key: %v", err)
			return nil
		}
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			log.Println("push apns: key is not an ecdsa private key")
			return nil
		}
		t := &token.Token{
			AuthKey: ec,
			KeyID:   keyID,
			TeamID:  teamID,
		}
		c := apns2.NewTokenClient(t)
		if production {
			c = c.Production()
		} else {
			c = c.Development()
		}
		log.Printf("push apns: token-based ready (production=%v)", production)
		return &apnsDispatcher{client: c}
	}

	// Certificate-based fallback.
	certPath := os.Getenv("CHEPHERD_RELAY_APNS_CERT_PATH")
	if certPath != "" {
		raw, err := os.ReadFile(certPath)
		if err != nil {
			log.Printf("push apns: read cert %s: %v", certPath, err)
			return nil
		}
		cert, err := tls.X509KeyPair(raw, raw)
		if err != nil {
			log.Printf("push apns: parse cert: %v", err)
			return nil
		}
		c := apns2.NewClient(cert)
		if production {
			c = c.Production()
		} else {
			c = c.Development()
		}
		log.Printf("push apns: cert-based ready (production=%v)", production)
		return &apnsDispatcher{client: c}
	}

	return nil
}
