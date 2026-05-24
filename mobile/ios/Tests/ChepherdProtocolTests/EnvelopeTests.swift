import Testing
import Foundation
@testable import ChepherdProtocol

@Suite("Envelope")
struct EnvelopeTests {
    @Test func sequenceCounterIsMonotonic() {
        let c = SequenceCounter()
        #expect(c.next() == 1)
        #expect(c.next() == 2)
        #expect(c.current() == 2)
    }

    @Test func sequenceCounterSetToSupportsResume() {
        let c = SequenceCounter()
        c.setTo(42)
        #expect(c.next() == 43)
    }

    @Test func registerEnvelopeRoundTrips() throws {
        let c = SequenceCounter()
        let payload = RegisterPayload(
            bastion_id: "test-bastion",
            user_id: "alice@example.com",
            chepherd_version: "0.2.0-rc1",
            capabilities: ["pause", "inject"],
            session_count: 5
        )
        let env = mkEnvelope(type: .register, payload: payload, counter: c)
        #expect(env.seq == 1)
        let wire = try encodeFrame(env)
        let back = try decodeFrame(wire, of: RegisterPayload.self)
        #expect(back.type == .register)
        #expect(back.payload?.bastion_id == "test-bastion")
        #expect(back.payload?.session_count == 5)
    }

    @Test func validateRejectsEmptyFrame() {
        #expect(throws: EnvelopeError.empty) {
            try validateFrame(Data())
        }
    }

    @Test func validateRejectsOversizedFrame() {
        let big = Data(repeating: 0x41, count: FRAME_SIZE_LIMIT + 1)
        #expect(throws: (any Error).self) {
            try validateFrame(big)
        }
    }
}
