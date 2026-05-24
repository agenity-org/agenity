// Transport abstractions — Swift mirror of:
//   chepherd-rc-web/src/protocol/transport.ts
//   chepherd/internal/daemon/rc/transport/

import Foundation
import ChepherdProtocol

public enum TransportState: String, Sendable {
    case idle
    case connecting
    case open
    case closing
    case closed
}

public enum TransportKind: String, Sendable {
    case ws
    case webrtc
}

/// The pluggable transport contract — WS and WebRTC both conform.
public protocol Transport: AnyObject, Sendable {
    var kind: TransportKind { get }
    var state: TransportState { get }
    func connect() async throws
    func send<P: Codable & Sendable>(_ env: Envelope<P>) async throws
    func close(reason: String?) async
    /// Stream of incoming frames as raw Data. Caller decodes per envelope type.
    func frames() -> AsyncStream<Data>
    /// Stream of state transitions.
    func states() -> AsyncStream<TransportState>
}

public protocol SignalingClient: Sendable {
    func exchangeOffer(bastionID: String, sdp: String) async throws -> String
    func postCandidate(bastionID: String, candidate: String, sdpMid: String?, sdpMLineIndex: Int32?) async throws
    func recvCandidates(bastionID: String) -> AsyncStream<RemoteCandidate>
}

public struct RemoteCandidate: Sendable {
    public let sdp: String
    public let sdpMid: String?
    public let sdpMLineIndex: Int32?
    public init(sdp: String, sdpMid: String?, sdpMLineIndex: Int32?) {
        self.sdp = sdp; self.sdpMid = sdpMid; self.sdpMLineIndex = sdpMLineIndex
    }
}

public enum TransportError: Error, Sendable {
    case notConnected
    case frameTooLarge
    case connectionFailed(String)
    case alreadyClosed
}
