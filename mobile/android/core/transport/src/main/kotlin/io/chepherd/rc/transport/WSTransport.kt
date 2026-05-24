// OkHttp WebSocket-based relayed transport. Same shape as the TS +
// Swift mirrors.

package io.chepherd.rc.transport

import io.chepherd.rc.protocol.Envelope
import io.chepherd.rc.protocol.validateFrame
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException
import kotlin.coroutines.suspendCoroutine

class WSTransport(
    private val url: String,
    private val authToken: String,
    private val bastionId: String,
    private val client: OkHttpClient = defaultClient,
) : Transport {

    override val kind: TransportKind = TransportKind.WS

    private val _state = MutableStateFlow(TransportState.Idle)
    override val state: TransportState get() = _state.value

    private val _frames = MutableSharedFlow<ByteArray>(
        extraBufferCapacity = 256,
        onBufferOverflow = BufferOverflow.SUSPEND,
    )

    @Volatile private var socket: WebSocket? = null

    override fun frames(): Flow<ByteArray> = _frames.asSharedFlow()
    override fun states(): Flow<TransportState> = _state.asStateFlow()

    override suspend fun connect() {
        check(state == TransportState.Idle || state == TransportState.Closed) {
            "ws: already $state"
        }
        _state.value = TransportState.Connecting

        val req = Request.Builder()
            .url(url)
            .header("Sec-WebSocket-Protocol", "chepherd-rc-v1.$bastionId.$authToken")
            .build()

        suspendCoroutine<Unit> { cont ->
            socket = client.newWebSocket(req, object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) {
                    _state.value = TransportState.Open
                    cont.resume(Unit)
                }
                override fun onMessage(webSocket: WebSocket, text: String) {
                    runCatching { _frames.tryEmit(text.toByteArray(Charsets.UTF_8)) }
                }
                override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                    runCatching { _frames.tryEmit(bytes.toByteArray()) }
                }
                override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                    _state.value = TransportState.Closing
                    webSocket.close(1000, null)
                }
                override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                    _state.value = TransportState.Closed
                }
                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                    _state.value = TransportState.Closed
                    if (state == TransportState.Connecting) {
                        cont.resumeWithException(
                            TransportError.ConnectionFailed(t.message ?: t::class.simpleName ?: "unknown")
                        )
                    }
                }
            })
        }
    }

    override suspend fun <P> send(env: Envelope<P>, encode: (Envelope<P>) -> ByteArray) {
        val s = socket ?: throw TransportError.NotConnected
        check(state == TransportState.Open) { "ws: not open" }
        val bytes = encode(env)
        validateFrame(bytes)
        if (!s.send(String(bytes, Charsets.UTF_8))) {
            throw TransportError.ConnectionFailed("send queue closed")
        }
    }

    override suspend fun close(reason: String?) {
        if (state == TransportState.Closed || state == TransportState.Idle) return
        _state.value = TransportState.Closing
        socket?.close(1000, reason ?: "client close")
        socket = null
        _state.value = TransportState.Closed
    }

    companion object {
        val defaultClient: OkHttpClient = OkHttpClient.Builder().build()
    }
}
