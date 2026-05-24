// Regression test: payload public initializers must remain exposed
// across the SPM module boundary. Without explicit public init,
// the auto-synthesised memberwise init is internal-only and
// cross-module call sites (chepherd-rc-ios app target, SessionStore,
// etc.) fail to compile.
//
// Bug was caught in CI run 26347425005 — public CommandPayload init
// was missing, SessionStore.sendCommand couldn't construct one.

import Testing
import Foundation
@testable import ChepherdProtocol

@Suite("Payload public inits")
struct PayloadInitsTests {

    @Test func registerPayloadInit() {
        let p = RegisterPayload(
            bastion_id: "b1",
            user_id: "u@example.com",
            chepherd_version: "0.2.0-rc3",
            capabilities: [],
            session_count: 0
        )
        #expect(p.bastion_id == "b1")
    }

    @Test func commandPayloadInit() {
        let p = CommandPayload(
            session_uuid: "s1",
            action: .pause,
            args: ["reason": "test"]
        )
        #expect(p.session_uuid == "s1")
        #expect(p.action == .pause)
        #expect(p.args?["reason"] == "test")
    }

    @Test func commandPayloadInitDefaultsArgsToNil() {
        let p = CommandPayload(session_uuid: "s1", action: .refresh)
        #expect(p.args == nil)
    }

    @Test func scorecardInit() {
        let s = Scorecard(G: 8, V: 7, F: 6, E: 5)
        #expect(s.G == 8)
        #expect(s == Scorecard(G: 8, V: 7, F: 6, E: 5))
    }
}
