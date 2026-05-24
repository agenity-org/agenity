// Monotonic per-direction sequence counter.
// Reconnect-resume support per PROTOCOL.md §5.

import Foundation

public final class SequenceCounter: @unchecked Sendable {
    private var value: UInt64 = 0
    private let lock = NSLock()

    public init() {}

    public func next() -> UInt64 {
        lock.lock()
        defer { lock.unlock() }
        value += 1
        return value
    }

    public func current() -> UInt64 {
        lock.lock()
        defer { lock.unlock() }
        return value
    }

    /// Resume from a known-good seq after a reconnect.
    public func setTo(_ v: UInt64) {
        lock.lock()
        defer { lock.unlock() }
        value = v
    }
}
