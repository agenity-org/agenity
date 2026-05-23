package envelope

// Payload structs for each protocol v1 §4 message type. All clients
// (daemon, web, iOS, Android) share these shapes via the JSON schema
// served at https://chepherd.org/schema/v1.json (the Go structs here
// are the source of truth that schema is generated from).

// RegisterPayload — daemon → peer at connection establishment (§4.register).
type RegisterPayload struct {
	BastionID       string   `json:"bastion_id"`
	UserID          string   `json:"user_id"`
	ChepherdVersion string   `json:"chepherd_version"`
	Capabilities    []string `json:"capabilities"`
	SessionCount    int      `json:"session_count"`
	Hostname        string   `json:"hostname,omitempty"`
	// LastSeenSeq — only on RECONNECT; daemon asks peer to replay events
	// with seq > this. See §5.
	LastSeenSeq uint64 `json:"last_seen_seq,omitempty"`
}

// StatePayload — daemon → peer (§4.state). Carries the full session snapshot.
type StatePayload struct {
	Sessions []SessionState `json:"sessions"`
}

// SessionState mirrors the shape exposed by chepherd status. Field names
// match internal/state/state.go so a single JSON marshal works.
type SessionState struct {
	UUID               string         `json:"uuid"`
	TmuxName           string         `json:"tmux_name"`
	Repo               string         `json:"repo,omitempty"`
	TrustBand          string         `json:"trust_band,omitempty"`
	LastVerdict        string         `json:"last_verdict,omitempty"`
	LastScorecard      map[string]int `json:"last_scorecard,omitempty"`
	NextTickAt         string         `json:"next_tick_at,omitempty"`
	LiveSignals        *LiveSignals   `json:"live_signals,omitempty"`
	InterventionCount  int            `json:"intervention_count,omitempty"`
	LastInterventionAt string         `json:"last_intervention_at,omitempty"`
	Paused             bool           `json:"paused"`
}

// LiveSignals mirrors internal/state.LiveSignals — the cheap signals
// refreshed by chepherd live every 5 sec independent of judge cadence.
type LiveSignals struct {
	RefreshedAt       string  `json:"refreshed_at"`
	InProgressCount   int     `json:"in_progress_count"`
	BacklogCount      int     `json:"backlog_count"`
	UnclaimedCount    int     `json:"unclaimed_backlog_count"`
	CommitCountLast1H int     `json:"commits_last_hour_count"`
	LastCommitAgeMin  float64 `json:"git_last_commit_age_min"`
	TrackerMtimeMin   float64 `json:"tracker_mtime_age_min"`
}

// LogPayload — daemon → peer (§4.log). One log line.
type LogPayload struct {
	Session string `json:"session"`
	Level   string `json:"level"`
	Text    string `json:"text"`
}

// VerdictPayload — daemon → peer (§4.verdict). Emitted after each judge tick.
type VerdictPayload struct {
	SessionUUID   string         `json:"session_uuid"`
	Session       string         `json:"session"`
	Verdict       string         `json:"verdict"`
	PrincipleRef  string         `json:"principle_ref,omitempty"`
	Scorecard     map[string]int `json:"scorecard,omitempty"`
	ScorecardNote string         `json:"scorecard_note,omitempty"`
	Message       string         `json:"message,omitempty"`
	CostUSD       float64        `json:"cost_usd,omitempty"`
	Injected      bool           `json:"injected"`
}

// CommandPayload — peer → daemon (§4.command). Operator-initiated action.
type CommandPayload struct {
	SessionUUID string         `json:"session_uuid"`
	Action      string         `json:"action"`   // pause | unpause | refresh | inject | tmux_attach_hint
	Args        map[string]any `json:"args,omitempty"`
}

// AckPayload — daemon → peer (§4.ack). Response to a CommandPayload.
type AckPayload struct {
	InReplyTo uint64 `json:"in_reply_to"`
	OK        bool   `json:"ok"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

// PingPayload / PongPayload (§4.ping/pong). Pong includes the ping's seq.
type PingPayload struct{}

type PongPayload struct {
	InReplyTo uint64 `json:"in_reply_to"`
}

// ErrorPayload — bidirectional (§4.error).
type ErrorPayload struct {
	Code      string `json:"code"`
	InReplyTo uint64 `json:"in_reply_to,omitempty"`
	Message   string `json:"message"`
}

// Standard error codes per protocol v1 §4.error.
const (
	CodeAuthRevoked        = "AUTH_REVOKED"
	CodeRateLimit          = "RATE_LIMIT"
	CodeProtocolViolation  = "PROTOCOL_VIOLATION"
	CodeVersionMismatch    = "VERSION_MISMATCH"
	CodeResumeGap          = "RESUME_GAP"
	CodeBastionUnreachable = "BASTION_UNREACHABLE"
	CodeUnknownSession     = "UNKNOWN_SESSION"
	CodeUnknownCommand     = "UNKNOWN_COMMAND"
	CodeInternalError      = "INTERNAL_ERROR"
)
