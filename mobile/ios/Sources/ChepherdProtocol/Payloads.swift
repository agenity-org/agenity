// Typed payloads for protocol v1 §4. Mirrors:
//   chepherd-rc-web/src/protocol/payloads.ts
//   chepherd/internal/daemon/rc/envelope/payloads.go
// Any drift between this file and either mirror is a protocol bug.

import Foundation

public struct RegisterPayload: Codable, Sendable {
    public let bastion_id: String
    public let user_id: String
    public let chepherd_version: String
    public let capabilities: [String]
    public let session_count: Int
    public let hostname: String?
    /// Only on RECONNECT — see PROTOCOL.md §5.
    public let last_seen_seq: UInt64?

    public init(
        bastion_id: String,
        user_id: String,
        chepherd_version: String,
        capabilities: [String],
        session_count: Int,
        hostname: String? = nil,
        last_seen_seq: UInt64? = nil
    ) {
        self.bastion_id = bastion_id
        self.user_id = user_id
        self.chepherd_version = chepherd_version
        self.capabilities = capabilities
        self.session_count = session_count
        self.hostname = hostname
        self.last_seen_seq = last_seen_seq
    }
}

public enum TrustBand: String, Codable, Sendable {
    case trusted
    case standard
    case concerned
    case crisis
    case paused
}

public enum Verdict: String, Codable, Sendable {
    case silent
    case praise
    case coach
    case intervene
}

public struct Scorecard: Codable, Sendable, Equatable {
    public let G: Int
    public let V: Int
    public let F: Int
    public let E: Int
    public init(G: Int, V: Int, F: Int, E: Int) {
        self.G = G; self.V = V; self.F = F; self.E = E
    }
}

public struct LiveSignals: Codable, Sendable {
    public let refreshed_at: String
    public let in_progress_count: Int
    public let backlog_count: Int
    public let unclaimed_backlog_count: Int
    public let commits_last_hour_count: Int
    public let git_last_commit_age_min: Int
    public let tracker_mtime_age_min: Int
}

public struct SessionState: Codable, Sendable, Identifiable {
    public var id: String { uuid }
    public let uuid: String
    public let tmux_name: String
    public let repo: String?
    public let trust_band: TrustBand?
    public let last_verdict: Verdict?
    public let last_scorecard: Scorecard?
    public let next_tick_at: String?
    public let live_signals: LiveSignals?
    public let intervention_count: Int?
    public let last_intervention_at: String?
    public let paused: Bool
}

public struct StatePayload: Codable, Sendable {
    public let sessions: [SessionState]
}

public struct LogPayload: Codable, Sendable {
    public enum Level: String, Codable, Sendable {
        case verdict, info, warn, error
    }
    public let session: String
    public let level: Level
    public let text: String
}

public struct VerdictPayload: Codable, Sendable {
    public let session_uuid: String
    public let session: String
    public let verdict: Verdict
    public let principle_ref: String?
    public let scorecard: Scorecard?
    public let scorecard_note: String?
    public let message: String?
    public let cost_usd: Double?
    public let injected: Bool
}

public enum CommandAction: String, Codable, Sendable {
    case pause
    case unpause
    case refresh
    case inject
    case tmux_attach_hint
}

public struct CommandPayload: Codable, Sendable {
    public let session_uuid: String
    public let action: CommandAction
    /// Free-form args. Use JSONValue if you need strict typing here.
    public let args: [String: String]?

    public init(
        session_uuid: String,
        action: CommandAction,
        args: [String: String]? = nil
    ) {
        self.session_uuid = session_uuid
        self.action = action
        self.args = args
    }
}

public struct AckPayload: Codable, Sendable {
    public let in_reply_to: UInt64
    public let ok: Bool
    public let result: String?
    public let error: String?
}

public struct ErrorPayload: Codable, Sendable {
    public enum Code: String, Codable, Sendable {
        case AUTH_REVOKED
        case RATE_LIMIT
        case PROTOCOL_VIOLATION
        case VERSION_MISMATCH
        case RESUME_GAP
        case BASTION_UNREACHABLE
        case UNKNOWN_SESSION
        case UNKNOWN_COMMAND
        case INTERNAL_ERROR
    }
    public let code: Code
    public let in_reply_to: UInt64?
    public let message: String
}

public struct EmptyPayload: Codable, Sendable {
    public init() {}
}

public struct PongPayload: Codable, Sendable {
    public let in_reply_to: UInt64
}
