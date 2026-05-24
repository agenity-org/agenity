// FCM dispatcher — Firebase Cloud Messaging v1 (HTTP).
//
// We use firebase-admin-go which handles service-account JWT minting
// and token refresh transparently. Without a credential file (env var
// unset) this file returns nil and the push package degrades to APNs-
// only.

package push

import (
	"context"
	"fmt"
	"log"
	"os"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
)

type fcmDispatcher struct {
	client *messaging.Client
}

// Kind returns "fcm".
func (f *fcmDispatcher) Kind() string { return "fcm" }

// Send sends to a single device token via FCM v1.
func (f *fcmDispatcher) Send(ctx context.Context, tok Token, n Notification) error {
	msg := &messaging.Message{
		Token: tok.Value,
		Notification: &messaging.Notification{
			Title: n.Title,
			Body:  n.Body,
		},
		Data: map[string]string{
			"session_uuid": n.SessionUUID,
		},
		Android: &messaging.AndroidConfig{
			Priority:    androidPriority(n.Priority),
			CollapseKey: n.CollapseKey,
			TTL:         &n.TTL,
			Notification: &messaging.AndroidNotification{
				Sound: n.Sound,
			},
		},
	}
	if tok.Platform == PlatformWebPush {
		msg.Android = nil
		msg.Webpush = &messaging.WebpushConfig{
			Headers: map[string]string{
				"Urgency": webpushUrgency(n.Priority),
			},
			Notification: &messaging.WebpushNotification{
				Title: n.Title,
				Body:  n.Body,
			},
		}
	}
	id, err := f.client.Send(ctx, msg)
	if err != nil {
		return fmt.Errorf("fcm: send: %w", err)
	}
	log.Printf("push fcm: sent %s to %s", id, redactToken(tok.Value))
	return nil
}

func androidPriority(p Priority) string {
	if p == PriorityHigh {
		return "high"
	}
	return "normal"
}

func webpushUrgency(p Priority) string {
	if p == PriorityHigh {
		return "high"
	}
	return "normal"
}

func redactToken(t string) string {
	if len(t) < 8 {
		return "***"
	}
	return t[:4] + "…" + t[len(t)-4:]
}

func newFCMFromEnv(ctx context.Context) Dispatcher {
	credPath := os.Getenv("CHEPHERD_RELAY_FCM_CRED_PATH")
	if credPath == "" {
		return nil
	}
	app, err := firebase.NewApp(ctx, nil, option.WithCredentialsFile(credPath))
	if err != nil {
		log.Printf("push fcm: firebase init: %v", err)
		return nil
	}
	c, err := app.Messaging(ctx)
	if err != nil {
		log.Printf("push fcm: messaging init: %v", err)
		return nil
	}
	log.Println("push fcm: ready")
	return &fcmDispatcher{client: c}
}
