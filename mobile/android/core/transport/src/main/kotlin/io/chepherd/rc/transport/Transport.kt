// Transport contract — same shape as Swift + TS mirrors.

package io.chepherd.rc.transport

import io.chepherd.rc.protocol.Envelope
import kotlinx.coroutines.flow.Flow

enum class TransportState { Idle, Connecting, Open, Closing, Closed }
enum class TransportKind { WS, WebRTC }

sealed class TransportError(msg: String) : RuntimeException(msg) {
    object NotConnected : TransportError("transport: not connected")
    class FrameTooLarge(bytes: Int) : TransportError("transport: frame too large ($bytes)")
    class ConnectionFailed(reason: String) : TransportError("transport: connection failed ($reason)")
    object AlreadyClosed : TransportError("transport: already closed")
}

interface Transport {
    val kind: TransportKind
    val state: TransportState

    suspend fun connect()
    suspend fun <P> send(env: Envelope<P>, encode: (Envelope<P>) -> ByteArray)
    suspend fun close(reason: String?)

    /** Stream of incoming wire frames as raw bytes. Caller decodes per type. */
    fun frames(): Flow<ByteArray>
    /** Stream of state transitions. */
    fun states(): Flow<TransportState>
}

interface SignalingClient {
    suspend fun exchangeOffer(bastionId: String, sdp: String): String
    suspend fun postCandidate(bastionId: String, candidate: String, sdpMid: String?, sdpMLineIndex: Int?)
    fun recvCandidates(bastionId: String): Flow<RemoteCandidate>
}

data class RemoteCandidate(
    val sdp: String,
    val sdpMid: String?,
    val sdpMLineIndex: Int?,
)
