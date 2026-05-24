// chepherd-rc protocol v1 envelope — Kotlin mirror of envelope.ts and
// envelope.swift. Wire-compatible with the Go canon at
// chepherd/internal/daemon/rc/envelope.

package io.chepherd.rc.protocol

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.serializer
import kotlinx.serialization.encodeToString
import java.time.Instant

/** Protocol v1 message type discriminator (PROTOCOL.md §4). */
@Serializable
enum class EnvelopeType {
    register, state, log, verdict, command, ack, ping, pong, error
}

/** Wire envelope — exactly one of these per frame on every transport. */
@Serializable
data class Envelope<P>(
    val type: EnvelopeType,
    /** RFC3339Nano UTC timestamp on the sender's clock. */
    val ts: String,
    /** Monotonic per-direction, per-connection. uint64 in JSON. */
    val seq: Long,
    /** Type-specific payload (see protocol §4). */
    val payload: P? = null,
)

const val FRAME_SIZE_LIMIT: Int = 256 * 1024

sealed class EnvelopeException(msg: String) : RuntimeException(msg) {
    object Empty : EnvelopeException("envelope: empty frame")
    class TooLarge(bytes: Int) : EnvelopeException("envelope: frame too large ($bytes > $FRAME_SIZE_LIMIT)")
    object MissingType : EnvelopeException("envelope: missing type")
    class DecodeFailed(message: String) : EnvelopeException("envelope: decode failed: $message")
}

/** Frame size validator — receivers MUST reject frames > FRAME_SIZE_LIMIT. */
fun validateFrame(bytes: ByteArray) {
    if (bytes.isEmpty()) throw EnvelopeException.Empty
    if (bytes.size > FRAME_SIZE_LIMIT) throw EnvelopeException.TooLarge(bytes.size)
}

/** Shared JSON config — lenient enough to accept additive payload extensions. */
val ChepherdJson: Json = Json {
    ignoreUnknownKeys = true
    encodeDefaults = false
    explicitNulls = false
}

inline fun <reified P> decodeFrame(bytes: ByteArray): Envelope<P> {
    validateFrame(bytes)
    return try {
        ChepherdJson.decodeFromString(serializer(), bytes.toString(Charsets.UTF_8))
    } catch (t: Throwable) {
        throw EnvelopeException.DecodeFailed(t.message ?: t::class.simpleName ?: "unknown")
    }
}

inline fun <reified P> encodeFrame(env: Envelope<P>): ByteArray =
    ChepherdJson.encodeToString(env).toByteArray(Charsets.UTF_8)

/** Build an envelope with the sender clock + auto-incrementing seq. */
inline fun <reified P> mkEnvelope(
    type: EnvelopeType,
    payload: P,
    counter: SequenceCounter,
): Envelope<P> = Envelope(
    type = type,
    ts = Instant.now().toString(),
    seq = counter.next(),
    payload = payload,
)
