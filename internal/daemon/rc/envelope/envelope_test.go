package envelope

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRoundtrip(t *testing.T) {
	var seq atomic.Uint64
	payload := RegisterPayload{
		BastionID:       "emrah-bastion-01",
		UserID:          "alice@example.com",
		ChepherdVersion: "0.2.0",
		Capabilities:    []string{"pause", "inject"},
		SessionCount:    7,
	}

	env, err := New(TypeRegister, payload, &seq)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if env.Seq != 1 {
		t.Errorf("seq want 1 got %d", env.Seq)
	}
	if env.Type != TypeRegister {
		t.Errorf("type want register got %q", env.Type)
	}

	frame, err := env.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := ValidateFrame(frame); err != nil {
		t.Fatalf("ValidateFrame: %v", err)
	}

	decoded, err := Decode(frame)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.Type != env.Type || decoded.Seq != env.Seq || decoded.Ts != env.Ts {
		t.Errorf("envelope header mismatch:\n  got %+v\n want %+v", decoded, env)
	}

	var back RegisterPayload
	if err := decoded.DecodePayload(&back); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if back.BastionID != payload.BastionID || back.SessionCount != payload.SessionCount {
		t.Errorf("payload roundtrip mismatch: %+v vs %+v", back, payload)
	}
}

func TestSeqMonotonic(t *testing.T) {
	var seq atomic.Uint64
	envs := make([]*Envelope, 100)
	var wg sync.WaitGroup
	for i := range envs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			env, err := New(TypePing, PingPayload{}, &seq)
			if err != nil {
				t.Errorf("New: %v", err)
				return
			}
			envs[i] = env
		}(i)
	}
	wg.Wait()
	seen := map[uint64]bool{}
	for _, e := range envs {
		if seen[e.Seq] {
			t.Errorf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
		if e.Seq == 0 || e.Seq > 100 {
			t.Errorf("seq out of range: %d", e.Seq)
		}
	}
}

func TestSizeLimit(t *testing.T) {
	// 257 KiB frame should be rejected
	big := strings.Repeat("x", 257*1024)
	env := &Envelope{
		Type:    TypeLog,
		Ts:      "2026-05-23T21:00:00Z",
		Seq:     1,
		Payload: json.RawMessage(`"` + big + `"`),
	}
	frame, _ := env.Marshal()
	if err := ValidateFrame(frame); err == nil {
		t.Errorf("expected ValidateFrame to reject 257KB frame")
	}
}

func TestDecodeRejectsMissingType(t *testing.T) {
	frame := []byte(`{"ts":"2026-05-23T21:00:00Z","seq":1,"payload":{}}`)
	if _, err := Decode(frame); err == nil {
		t.Errorf("expected Decode to reject frame without type")
	}
}

func TestVerdictPayloadShape(t *testing.T) {
	var seq atomic.Uint64
	p := VerdictPayload{
		SessionUUID: "5c468708-...",
		Session:     "openova-27",
		Verdict:     "intervene",
		PrincipleRef: "P9, P14",
		Scorecard:   map[string]int{"G": 3, "V": 1, "F": 1, "E": 0},
		Injected:    true,
		CostUSD:     0.1127,
	}
	env, err := New(TypeVerdict, p, &seq)
	if err != nil {
		t.Fatal(err)
	}
	b, err := env.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"verdict":"intervene"`) {
		t.Errorf("verdict not in JSON: %s", b)
	}
	if !strings.Contains(string(b), `"in-flight":`) && !strings.Contains(string(b), `"injected":true`) {
		t.Errorf("injected flag not preserved: %s", b)
	}
}

func TestSequenceCounter(t *testing.T) {
	var c SequenceCounter
	if c.Next() != 1 || c.Next() != 2 || c.Next() != 3 {
		t.Errorf("counter not monotonic")
	}
	if c.Current() != 3 {
		t.Errorf("current want 3 got %d", c.Current())
	}
	c.SetTo(100)
	if c.Next() != 101 {
		t.Errorf("after SetTo(100), Next want 101 got %d", c.Next())
	}
}
