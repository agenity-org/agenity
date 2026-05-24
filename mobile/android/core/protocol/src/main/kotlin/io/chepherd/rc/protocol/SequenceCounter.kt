// Thread-safe monotonic counter with setTo() for reconnect-resume (§5).

package io.chepherd.rc.protocol

import java.util.concurrent.atomic.AtomicLong

class SequenceCounter {
    private val value = AtomicLong(0)

    fun next(): Long = value.incrementAndGet()
    fun current(): Long = value.get()
    fun setTo(v: Long) { value.set(v) }
}
