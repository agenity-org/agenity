// HTTPSignaling — Swift implementation of SignalingClient that calls
// chepherd-relay's /v1/signaling/{offer,candidate,candidates} REST
// endpoints. Mirrors:
//   chepherd-rc-web/src/protocol/signaling.ts
//   chepherd/internal/daemon/rc/signaling/client.go

import Foundation

public struct HTTPSignalingConfig: Sendable {
    public let baseURL: URL
    public let authToken: String
    public let session: URLSession

    public init(baseURL: URL, authToken: String, session: URLSession = .shared) {
        self.baseURL = baseURL
        self.authToken = authToken
        self.session = session
    }
}

public enum SignalingError: Error, Sendable {
    case offerFailed(status: Int, body: String)
    case candidateFailed(status: Int, body: String)
    case decodeFailed(String)
}

public final class HTTPSignaling: SignalingClient, @unchecked Sendable {
    private let cfg: HTTPSignalingConfig

    public init(cfg: HTTPSignalingConfig) {
        self.cfg = cfg
    }

    public func exchangeOffer(bastionID: String, sdp: String) async throws -> String {
        let url = cfg.baseURL.appendingPathComponent("v1/signaling/offer")
        let body = OfferBody(bastion_id: bastionID, offer: SDPField(type: "offer", sdp: sdp))
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("Bearer \(cfg.authToken)", forHTTPHeaderField: "Authorization")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(body)

        let (data, resp) = try await cfg.session.data(for: req)
        let status = (resp as? HTTPURLResponse)?.statusCode ?? -1
        guard status == 200 else {
            throw SignalingError.offerFailed(status: status, body: String(data: data, encoding: .utf8) ?? "")
        }
        do {
            let parsed = try JSONDecoder().decode(OfferResp.self, from: data)
            return parsed.answer.sdp
        } catch {
            throw SignalingError.decodeFailed(String(describing: error))
        }
    }

    public func postCandidate(bastionID: String, candidate: String, sdpMid: String?, sdpMLineIndex: Int32?) async throws {
        let url = cfg.baseURL.appendingPathComponent("v1/signaling/candidate")
        let body = CandidateBody(
            bastion_id: bastionID,
            candidate: CandidateField(
                candidate: candidate,
                sdpMid: sdpMid,
                sdpMLineIndex: sdpMLineIndex
            )
        )
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("Bearer \(cfg.authToken)", forHTTPHeaderField: "Authorization")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(body)

        let (data, resp) = try await cfg.session.data(for: req)
        let status = (resp as? HTTPURLResponse)?.statusCode ?? -1
        guard status == 200 || status == 204 else {
            throw SignalingError.candidateFailed(status: status, body: String(data: data, encoding: .utf8) ?? "")
        }
    }

    public func recvCandidates(bastionID: String) -> AsyncStream<RemoteCandidate> {
        AsyncStream { continuation in
            let task = Task {
                while !Task.isCancelled {
                    do {
                        let cands = try await self.pollOnce(bastionID: bastionID)
                        for c in cands {
                            continuation.yield(c)
                        }
                    } catch is CancellationError {
                        break
                    } catch {
                        // Transient network blip — small back-off, keep going.
                        try? await Task.sleep(nanoseconds: 1_000_000_000)
                    }
                }
                continuation.finish()
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    private func pollOnce(bastionID: String) async throws -> [RemoteCandidate] {
        var components = URLComponents(
            url: cfg.baseURL.appendingPathComponent("v1/signaling/candidates"),
            resolvingAgainstBaseURL: false
        )!
        components.queryItems = [URLQueryItem(name: "bastion_id", value: bastionID)]
        var req = URLRequest(url: components.url!)
        req.setValue("Bearer \(cfg.authToken)", forHTTPHeaderField: "Authorization")
        let (data, resp) = try await cfg.session.data(for: req)
        let status = (resp as? HTTPURLResponse)?.statusCode ?? -1
        if status == 204 { return [] }
        guard status == 200 else {
            return []
        }
        let parsed = try JSONDecoder().decode(CandidatesResp.self, from: data)
        return parsed.candidates.map {
            RemoteCandidate(sdp: $0.candidate, sdpMid: $0.sdpMid, sdpMLineIndex: $0.sdpMLineIndex)
        }
    }

    // Wire-shape structs — match the relay + web/Go implementations.

    private struct SDPField: Codable {
        let type: String
        let sdp: String
    }

    private struct OfferBody: Codable {
        let bastion_id: String
        let offer: SDPField
    }

    private struct OfferResp: Codable {
        let answer: SDPField
        let client_id: String?
    }

    private struct CandidateField: Codable {
        let candidate: String
        let sdpMid: String?
        let sdpMLineIndex: Int32?
    }

    private struct CandidateBody: Codable {
        let bastion_id: String
        let candidate: CandidateField
    }

    private struct CandidatesResp: Codable {
        let candidates: [CandidateField]
        let cursor: String?
    }
}
