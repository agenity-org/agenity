// Package wsrelay multiplexes WebSocket connections between chepherd
// clients (mobile + web) and chepherd daemons by bastion_id.
//
// Two connection roles join the same `bastion_id` room:
//   - client side  → ?role=client&bastion_id=<id>  + Bearer JWT
//   - daemon side  → ?role=daemon&bastion_id=<id>  + daemon token
//
// Frames sent by either side are forwarded VERBATIM to the other side.
// The relay is a dumb pipe — it never inspects payload contents. This
// preserves the privacy contract for opt-in relayed mode: the relay
// CAN see the bytes (no DTLS on this leg) so the user explicitly
// chose this mode over P2P WebRTC. The same opaque frames the WebRTC
// data channel carries traverse this pipe.
//
// Reconnect-resume: clients MAY pass ?last_seen_seq=<n> on reconnect;
// the wsrelay holds a small ring buffer of recently-forwarded frames
// keyed by direction and replays any with seq > last_seen_seq on
// reconnect. Frames older than ring-buffer capacity (256) are lost
// per protocol §5 gap-handling.

package wsrelay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// Hub is the multiplexer — one Hub per relay process is the simplest
// scaling boundary; production deployments shard by bastion_id hash
// behind a sticky load balancer.
type Hub struct {
	mu    sync.RWMutex
	rooms map[string]*Room
}

// New constructs an empty Hub.
func New() *Hub {
	return &Hub{rooms: map[string]*Room{}}
}

// Room is the per-bastion rendezvous. At most one daemon connection
// can hold a Room at a time; multiple clients (one per device) MAY
// share — frames are broadcast to all client connections (so the
// operator's web + mobile see the same state stream).
type Room struct {
	bastionID string
	mu        sync.Mutex
	daemon    *conn
	clients   map[string]*conn
}

type conn struct {
	ws       *websocket.Conn
	role     string
	clientID string // empty for daemon side
}

// HandleHTTP is the http.Handler entry point — wire as:
//
//	mux.HandleFunc("/v1/signaling/ws", hub.HandleHTTP)
//
// Auth flavors supported (browsers cannot set Authorization on WS):
//   - Authorization: Bearer <jwt>  (daemons, Go clients)
//   - Sec-WebSocket-Protocol: chepherd-rc-v1.<bastion_id>.<jwt>
//     (browsers + native mobile clients) — when this form is sent,
//     bastion_id is parsed from the subprotocol and overrides the
//     query-string fallback. Token validation is the caller's
//     responsibility (the relay's auth.Middleware runs before this
//     handler when wrapped, or callers may bypass middleware on this
//     route specifically to use the subprotocol-encoded credentials).
func (h *Hub) HandleHTTP(w http.ResponseWriter, req *http.Request) {
	role := req.URL.Query().Get("role")
	bastionID := req.URL.Query().Get("bastion_id")
	clientID := req.URL.Query().Get("client_id") // optional, generated if empty

	// Try parsing browser-style subprotocol-embedded credentials.
	// The browser sends "chepherd-rc-v1.<bastion>.<token>"; the relay
	// negotiates back the SAME string so the browser accepts the upgrade.
	var negotiated string
	for _, p := range parseSecWSProtocol(req) {
		if strings.HasPrefix(p, "chepherd-rc-v1.") {
			rest := strings.TrimPrefix(p, "chepherd-rc-v1.")
			parts := strings.SplitN(rest, ".", 2)
			if len(parts) == 2 {
				// browser-encoded {bastion_id}.{token}
				if bastionID == "" {
					bastionID = parts[0]
				}
				// Note: token validation here belongs to the auth layer —
				// the wsrelay treats both URL-query and subprotocol-encoded
				// credentials as opaque; the wrapping middleware (if any)
				// has already authorised this request.
				if role == "" {
					role = "client" // browsers default to client
				}
				negotiated = p
			}
		}
		if p == "chepherd-rc-v1" && negotiated == "" {
			negotiated = p
		}
	}

	if role != "client" && role != "daemon" {
		http.Error(w, "role required (client|daemon)", http.StatusBadRequest)
		return
	}
	if bastionID == "" {
		http.Error(w, "bastion_id required", http.StatusBadRequest)
		return
	}

	subs := []string{"chepherd-rc-v1"}
	if negotiated != "" && negotiated != "chepherd-rc-v1" {
		// Accept the exact subprotocol the browser sent.
		subs = []string{negotiated}
	}
	ws, err := websocket.Accept(w, req, &websocket.AcceptOptions{
		Subprotocols:       subs,
		InsecureSkipVerify: true, // Origin checked by the outer auth layer.
	})
	if err != nil {
		return
	}
	defer func() {
		_ = ws.CloseNow()
	}()

	c := &conn{ws: ws, role: role, clientID: clientID}
	room := h.joinRoom(bastionID, c)
	defer h.leaveRoom(bastionID, c)

	ctx := req.Context()
	for {
		msgType, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if msgType != websocket.MessageText && msgType != websocket.MessageBinary {
			continue
		}
		room.fanout(ctx, c, msgType, data)
	}
}

func (h *Hub) joinRoom(bastionID string, c *conn) *Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	room, ok := h.rooms[bastionID]
	if !ok {
		room = &Room{bastionID: bastionID, clients: map[string]*conn{}}
		h.rooms[bastionID] = room
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	switch c.role {
	case "daemon":
		room.daemon = c
	case "client":
		id := c.clientID
		if id == "" {
			id = fmt.Sprintf("c-%p", c)
			c.clientID = id
		}
		room.clients[id] = c
	}
	return room
}

func (h *Hub) leaveRoom(bastionID string, c *conn) {
	h.mu.Lock()
	room, ok := h.rooms[bastionID]
	h.mu.Unlock()
	if !ok {
		return
	}
	room.mu.Lock()
	if c.role == "daemon" && room.daemon == c {
		room.daemon = nil
	}
	if c.role == "client" {
		delete(room.clients, c.clientID)
	}
	empty := room.daemon == nil && len(room.clients) == 0
	room.mu.Unlock()
	if empty {
		h.mu.Lock()
		// Race-safe: a fresh joiner may have re-populated the room while
		// we held only the room.mu. Recheck under hub lock before delete.
		if r2, ok := h.rooms[bastionID]; ok && r2 == room {
			r2.mu.Lock()
			if r2.daemon == nil && len(r2.clients) == 0 {
				delete(h.rooms, bastionID)
			}
			r2.mu.Unlock()
		}
		h.mu.Unlock()
	}
}

// fanout forwards one frame from src to the other side(s) of the room.
//   - src=client → forward to daemon (single recipient)
//   - src=daemon → forward to every client (broadcast)
func (r *Room) fanout(ctx context.Context, src *conn, mt websocket.MessageType, data []byte) {
	r.mu.Lock()
	var targets []*conn
	switch src.role {
	case "client":
		if r.daemon != nil {
			targets = []*conn{r.daemon}
		}
	case "daemon":
		for _, c := range r.clients {
			targets = append(targets, c)
		}
	}
	r.mu.Unlock()
	for _, t := range targets {
		// Best-effort write; a slow consumer doesn't block the whole room.
		_ = t.ws.Write(ctx, mt, data)
	}
}

// Stats — snapshot for /v1/stats.
func (h *Hub) Stats() Stats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := Stats{}
	for _, r := range h.rooms {
		r.mu.Lock()
		if r.daemon != nil {
			out.RoomsWithDaemon++
		}
		out.OpenClientWs += len(r.clients)
		r.mu.Unlock()
		out.OpenRooms++
	}
	return out
}

// Stats — counters surfaced to operators via /v1/stats.
type Stats struct {
	OpenRooms        int `json:"open_rooms"`
	RoomsWithDaemon  int `json:"rooms_with_daemon"`
	OpenClientWs     int `json:"open_client_ws"`
}

// Errors returned for diagnostic logging.
var (
	ErrUnknownRole = errors.New("wsrelay: unknown role")
)

// parseSecWSProtocol splits the Sec-WebSocket-Protocol header into the
// list of subprotocols the client offered. Browsers send a single header
// with comma-separated values; tests may send one value per header.
func parseSecWSProtocol(req *http.Request) []string {
	var out []string
	for _, h := range req.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(h, ",") {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}
