// SessionStore — Swift mirror of chepherd-rc-web/src/lib/session-store.ts.
// Subscribes to a Transport's frame stream, maintains the live session
// list, log ring buffer, and verdict event stream. Drives the UI.

#if canImport(SwiftUI)
import Foundation
import Observation
import ChepherdProtocol
import ChepherdTransport

@MainActor
@Observable
public final class SessionStore {
    public var sessions: [SessionState] = []
    public var transportState: TransportState = .idle
    public var transportKind: TransportKind?
    public var logs: [LogEntry] = []
    public var verdicts: [VerdictEntry] = []
    public var errorMessage: String?

    private let transport: Transport
    private let counter = SequenceCounter()
    private var receiveTask: Task<Void, Never>?
    private var stateTask: Task<Void, Never>?

    private let logRingSize = 500
    private let verdictRingSize = 100

    public init(transport: Transport) {
        self.transport = transport
        self.transportKind = transport.kind
    }

    public func connect() async {
        receiveTask = Task { [weak self] in
            guard let self else { return }
            for await frame in transport.frames() {
                await self.handleFrame(frame)
            }
        }
        stateTask = Task { [weak self] in
            guard let self else { return }
            for await s in transport.states() {
                await MainActor.run { self.transportState = s }
            }
        }
        do {
            try await transport.connect()
        } catch {
            errorMessage = String(describing: error)
        }
    }

    public func disconnect() async {
        receiveTask?.cancel()
        stateTask?.cancel()
        await transport.close(reason: "user disconnect")
    }

    public func sendCommand(sessionUUID: String, action: CommandAction, args: [String: String]? = nil) async {
        let payload = CommandPayload(session_uuid: sessionUUID, action: action, args: args)
        let env = mkEnvelope(type: .command, payload: payload, counter: counter)
        do {
            try await transport.send(env)
        } catch {
            errorMessage = String(describing: error)
        }
    }

    public struct LogEntry: Identifiable, Sendable {
        public let id = UUID()
        public let session: String
        public let level: LogPayload.Level
        public let text: String
        public let at: Date
    }

    public struct VerdictEntry: Identifiable, Sendable {
        public let id = UUID()
        public let payload: VerdictPayload
        public let at: Date
    }

    private func handleFrame(_ frame: Data) async {
        // Type-discriminator pre-decode — sniff just the `type` field so we
        // can route to the right concrete payload type.
        struct Sniff: Codable { let type: EnvelopeType }
        let sniff: Sniff
        do { sniff = try JSONDecoder().decode(Sniff.self, from: frame) } catch { return }

        switch sniff.type {
        case .state:
            if let env = try? decodeFrame(frame, of: StatePayload.self),
               let p = env.payload {
                await MainActor.run { self.sessions = p.sessions }
            }
        case .log:
            if let env = try? decodeFrame(frame, of: LogPayload.self),
               let p = env.payload {
                await MainActor.run {
                    let entry = LogEntry(session: p.session, level: p.level, text: p.text, at: Date())
                    if self.logs.count >= self.logRingSize {
                        self.logs.removeFirst(self.logs.count - self.logRingSize + 1)
                    }
                    self.logs.append(entry)
                }
            }
        case .verdict:
            if let env = try? decodeFrame(frame, of: VerdictPayload.self),
               let p = env.payload {
                await MainActor.run {
                    if self.verdicts.count >= self.verdictRingSize {
                        self.verdicts.removeFirst(self.verdicts.count - self.verdictRingSize + 1)
                    }
                    self.verdicts.append(VerdictEntry(payload: p, at: Date()))
                }
            }
        default:
            break
        }
    }
}
#endif
