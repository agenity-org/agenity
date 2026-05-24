// Typed payloads for protocol v1 §4. Kotlin mirror of:
//   chepherd-rc-web/src/protocol/payloads.ts
//   chepherd-rc-ios/Sources/ChepherdProtocol/Payloads.swift
//   chepherd/internal/daemon/rc/envelope/payloads.go

package io.chepherd.rc.protocol

import kotlinx.serialization.Serializable

@Serializable
data class RegisterPayload(
    val bastion_id: String,
    val user_id: String,
    val chepherd_version: String,
    val capabilities: List<String>,
    val session_count: Int,
    val hostname: String? = null,
    /** Only on RECONNECT — see PROTOCOL.md §5. */
    val last_seen_seq: Long? = null,
)

@Serializable
enum class TrustBand { trusted, standard, concerned, crisis, paused }

@Serializable
enum class Verdict { silent, praise, coach, intervene }

@Serializable
data class Scorecard(
    val G: Int,
    val V: Int,
    val F: Int,
    val E: Int,
)

@Serializable
data class LiveSignals(
    val refreshed_at: String,
    val in_progress_count: Int,
    val backlog_count: Int,
    val unclaimed_backlog_count: Int,
    val commits_last_hour_count: Int,
    val git_last_commit_age_min: Int,
    val tracker_mtime_age_min: Int,
)

@Serializable
data class SessionState(
    val uuid: String,
    val tmux_name: String,
    val repo: String? = null,
    val trust_band: TrustBand? = null,
    val last_verdict: Verdict? = null,
    val last_scorecard: Scorecard? = null,
    val next_tick_at: String? = null,
    val live_signals: LiveSignals? = null,
    val intervention_count: Int? = null,
    val last_intervention_at: String? = null,
    val paused: Boolean,
)

@Serializable
data class StatePayload(val sessions: List<SessionState>)

@Serializable
data class LogPayload(
    val session: String,
    val level: Level,
    val text: String,
) {
    @Serializable
    enum class Level { verdict, info, warn, error }
}

@Serializable
data class VerdictPayload(
    val session_uuid: String,
    val session: String,
    val verdict: Verdict,
    val principle_ref: String? = null,
    val scorecard: Scorecard? = null,
    val scorecard_note: String? = null,
    val message: String? = null,
    val cost_usd: Double? = null,
    val injected: Boolean,
)

@Serializable
enum class CommandAction { pause, unpause, refresh, inject, tmux_attach_hint }

@Serializable
data class CommandPayload(
    val session_uuid: String,
    val action: CommandAction,
    val args: Map<String, String>? = null,
)

@Serializable
data class AckPayload(
    val in_reply_to: Long,
    val ok: Boolean,
    val result: String? = null,
    val error: String? = null,
)

@Serializable
data class ErrorPayload(
    val code: Code,
    val in_reply_to: Long? = null,
    val message: String,
) {
    @Serializable
    enum class Code {
        AUTH_REVOKED, RATE_LIMIT, PROTOCOL_VIOLATION, VERSION_MISMATCH,
        RESUME_GAP, BASTION_UNREACHABLE, UNKNOWN_SESSION, UNKNOWN_COMMAND,
        INTERNAL_ERROR,
    }
}

@Serializable
data class PongPayload(val in_reply_to: Long)
