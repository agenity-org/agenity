// Package envelope is the protocol v1 message envelope per docs/PROTOCOL.md §3:
//
//	{
//	  "type":    "register" | "state" | "log" | "verdict" | "command" | "ack" | "ping" | "pong" | "error",
//	  "ts":      "2026-05-23T21:30:14.123Z",
//	  "seq":     12345,
//	  "payload": { ... }
//	}
//
// All Transport-layer frames are exactly one Envelope serialised as JSON.
// This package is transport-agnostic — same struct types travel over WebRTC
// or WebSocket unchanged.
package envelope

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

// Type names the message-type discriminator.
type Type string

const (
	TypeRegister Type = "register"
	TypeState    Type = "state"
	TypeLog      Type = "log"
	TypeVerdict  Type = "verdict"
	TypeCommand  Type = "command"
	TypeAck      Type = "ack"
	TypePing     Type = "ping"
	TypePong     Type = "pong"
	TypeError    Type = "error"
)

// Envelope is the wire shape every frame takes. Per protocol v1 §3.
type Envelope struct {
	Type    Type            `json:"type"`
	Ts      string          `json:"ts"`             // RFC3339Nano UTC, sender clock
	Seq     uint64          `json:"seq"`            // monotonic per-direction, per-connection
	Payload json.RawMessage `json:"payload,omitempty"`
}

// New constructs an Envelope with sender clock + auto-incremented seq.
// counter MUST be the same atomic.Uint64 across all sends on one direction.
func New(t Type, payload any, counter *atomic.Uint64) (*Envelope, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: marshal payload: %w", err)
	}
	return &Envelope{
		Type:    t,
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Seq:     counter.Add(1),
		Payload: b,
	}, nil
}

// Marshal returns the JSON bytes for sending.
func (e *Envelope) Marshal() ([]byte, error) {
	return json.Marshal(e)
}

// Decode parses one frame from the wire.
func Decode(frame []byte) (*Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(frame, &e); err != nil {
		return nil, fmt.Errorf("envelope: decode: %w", err)
	}
	if e.Type == "" {
		return nil, fmt.Errorf("envelope: missing type")
	}
	return &e, nil
}

// DecodePayload unmarshals the Envelope's Payload into a typed struct.
func (e *Envelope) DecodePayload(dst any) error {
	if len(e.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(e.Payload, dst)
}

// FrameSizeLimit per protocol v1 §3 — frames > 256 KiB MUST be rejected.
// Larger payloads must chunk at a higher layer.
const FrameSizeLimit = 256 * 1024

// ValidateFrame applies the size guard before decode.
func ValidateFrame(frame []byte) error {
	if len(frame) > FrameSizeLimit {
		return fmt.Errorf("envelope: frame too large (%d > %d)", len(frame), FrameSizeLimit)
	}
	if len(frame) == 0 {
		return fmt.Errorf("envelope: empty frame")
	}
	return nil
}

// SequenceCounter is the monotonic seq generator each direction maintains.
// Wrap-around (uint64 overflow) is impossible in practice: at 1000 frames/sec
// continuously, wrap takes ~584 million years.
type SequenceCounter struct {
	v atomic.Uint64
}

// Next returns the next sequence number.
func (s *SequenceCounter) Next() uint64 { return s.v.Add(1) }

// Current reports the last-issued sequence number (for resume coordination).
func (s *SequenceCounter) Current() uint64 { return s.v.Load() }

// SetTo updates the counter — used during reconnect-resume when the peer
// reports its last_seen_seq and we need to advance past it.
func (s *SequenceCounter) SetTo(v uint64) { s.v.Store(v) }
