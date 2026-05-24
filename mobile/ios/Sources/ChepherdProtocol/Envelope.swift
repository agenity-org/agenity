// chepherd-rc protocol v1 envelope — Swift mirror of envelope.ts.
//
// Source-of-truth spec: chepherd/docs/PROTOCOL.md
// Wire-compatible with the Go envelope at
// chepherd/internal/daemon/rc/envelope and the TypeScript envelope at
// chepherd-rc-web/src/protocol/envelope.ts. All three encode the SAME
// JSON object per frame.

import Foundation

/// Protocol v1 message type discriminator (PROTOCOL.md §4).
public enum EnvelopeType: String, Codable, Sendable {
    case register
    case state
    case log
    case verdict
    case command
    case ack
    case ping
    case pong
    case error
}

/// Wire envelope — exactly one of these per frame on every transport.
public struct Envelope<P: Codable & Sendable>: Codable, Sendable {
    /// PROTOCOL.md §3 type discriminator.
    public let type: EnvelopeType
    /// RFC3339Nano UTC timestamp on the sender's clock.
    public let ts: String
    /// Monotonic per-direction, per-connection. uint64 in JSON.
    public let seq: UInt64
    /// Type-specific payload (see protocol §4).
    public let payload: P?

    public init(type: EnvelopeType, ts: String, seq: UInt64, payload: P?) {
        self.type = type
        self.ts = ts
        self.seq = seq
        self.payload = payload
    }
}

/// Frame size limit from PROTOCOL.md §3.
public let FRAME_SIZE_LIMIT: Int = 256 * 1024

public enum EnvelopeError: Error, Equatable {
    case empty
    case tooLarge(bytes: Int)
    case missingType
    case decodeFailed(String)
}

/// Validate the frame size + non-emptiness.
public func validateFrame(_ frame: Data) throws {
    if frame.isEmpty { throw EnvelopeError.empty }
    if frame.count > FRAME_SIZE_LIMIT {
        throw EnvelopeError.tooLarge(bytes: frame.count)
    }
}

/// Decode one wire frame into a typed envelope.
public func decodeFrame<P: Codable & Sendable>(_ frame: Data, of: P.Type) throws -> Envelope<P> {
    try validateFrame(frame)
    let decoder = JSONDecoder()
    do {
        let env = try decoder.decode(Envelope<P>.self, from: frame)
        return env
    } catch {
        throw EnvelopeError.decodeFailed(String(describing: error))
    }
}

/// Encode one envelope to its wire form.
public func encodeFrame<P: Codable & Sendable>(_ env: Envelope<P>) throws -> Data {
    let encoder = JSONEncoder()
    encoder.outputFormatting = []
    return try encoder.encode(env)
}

/// Build an envelope with the sender clock + auto-incrementing seq.
public func mkEnvelope<P: Codable & Sendable>(
    type: EnvelopeType,
    payload: P,
    counter: SequenceCounter
) -> Envelope<P> {
    Envelope(
        type: type,
        ts: ISO8601DateFormatter.chepherdInstance.string(from: Date()),
        seq: counter.next(),
        payload: payload
    )
}

extension ISO8601DateFormatter {
    /// UTC RFC3339Nano formatter — matches the Go envelope encoder.
    ///
    /// Apple's ISO8601DateFormatter is thread-safe in practice (and
    /// documented as such), but the standard library hasn't annotated
    /// it Sendable yet under Swift 6 strict concurrency. The
    /// `nonisolated(unsafe)` opt-out is the canonical workaround for
    /// this exact case — see SE-0414 "Region based isolation" review.
    nonisolated(unsafe) static let chepherdInstance: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        f.timeZone = TimeZone(identifier: "UTC")
        return f
    }()
}
