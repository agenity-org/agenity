// WSTransport — URLSessionWebSocketTask-based relayed transport.
// Same shape as chepherd-rc-web/src/protocol/transport.ts WSTransport.

import Foundation
import ChepherdProtocol

public final class WSTransport: Transport, @unchecked Sendable {
    public let kind: TransportKind = .ws
    public private(set) var state: TransportState = .idle

    private let url: URL
    private let authToken: String
    private let bastionID: String

    private let session: URLSession
    private var task: URLSessionWebSocketTask?

    private let frameContinuation: AsyncStream<Data>.Continuation
    private let frameStream: AsyncStream<Data>
    private let stateContinuation: AsyncStream<TransportState>.Continuation
    private let stateStream: AsyncStream<TransportState>

    public init(url: URL, authToken: String, bastionID: String) {
        self.url = url
        self.authToken = authToken
        self.bastionID = bastionID
        self.session = URLSession(configuration: .ephemeral)
        var fc: AsyncStream<Data>.Continuation!
        self.frameStream = AsyncStream { c in fc = c }
        self.frameContinuation = fc

        var sc: AsyncStream<TransportState>.Continuation!
        self.stateStream = AsyncStream { c in sc = c }
        self.stateContinuation = sc
    }

    public func frames() -> AsyncStream<Data> { frameStream }
    public func states() -> AsyncStream<TransportState> { stateStream }

    public func connect() async throws {
        guard state == .idle || state == .closed else {
            throw TransportError.alreadyClosed
        }
        setState(.connecting)
        let proto = "chepherd-rc-v1.\(bastionID).\(authToken)"
        var req = URLRequest(url: url)
        req.setValue(proto, forHTTPHeaderField: "Sec-WebSocket-Protocol")
        let t = session.webSocketTask(with: req)
        self.task = t
        t.resume()
        setState(.open)
        Task.detached { [weak self] in
            await self?.receiveLoop()
        }
    }

    public func send<P: Codable & Sendable>(_ env: Envelope<P>) async throws {
        guard state == .open, let t = task else {
            throw TransportError.notConnected
        }
        let data = try encodeFrame(env)
        try validateFrame(data)
        let s = String(data: data, encoding: .utf8) ?? ""
        try await t.send(.string(s))
    }

    public func close(reason: String?) async {
        guard state != .closed, state != .idle else { return }
        setState(.closing, reason: reason)
        task?.cancel(with: .normalClosure, reason: reason?.data(using: .utf8))
        task = nil
        setState(.closed, reason: reason)
    }

    private func receiveLoop() async {
        guard let t = task else { return }
        while state == .open {
            do {
                let msg = try await t.receive()
                switch msg {
                case .string(let s):
                    frameContinuation.yield(Data(s.utf8))
                case .data(let d):
                    frameContinuation.yield(d)
                @unknown default: break
                }
            } catch {
                setState(.closed, reason: "receive error: \(error)")
                return
            }
        }
    }

    private func setState(_ next: TransportState, reason: String? = nil) {
        guard next != state else { return }
        state = next
        stateContinuation.yield(next)
        if next == .closed {
            stateContinuation.finish()
            frameContinuation.finish()
        }
    }
}
