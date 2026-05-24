// SessionStore — Kotlin mirror of chepherd-rc-ios SessionStore + the
// web SessionStore. ViewModel + Flow-based reactive state.

package io.chepherd.rc.viewmodel

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import io.chepherd.rc.protocol.ChepherdJson
import io.chepherd.rc.protocol.CommandAction
import io.chepherd.rc.protocol.CommandPayload
import io.chepherd.rc.protocol.Envelope
import io.chepherd.rc.protocol.EnvelopeType
import io.chepherd.rc.protocol.LogPayload
import io.chepherd.rc.protocol.SequenceCounter
import io.chepherd.rc.protocol.SessionState
import io.chepherd.rc.protocol.StatePayload
import io.chepherd.rc.protocol.VerdictPayload
import io.chepherd.rc.protocol.encodeFrame
import io.chepherd.rc.protocol.mkEnvelope
import io.chepherd.rc.transport.Transport
import io.chepherd.rc.transport.TransportKind
import io.chepherd.rc.transport.TransportState
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive

class SessionStore(private val transport: Transport) : ViewModel() {

    private val _sessions = MutableStateFlow<List<SessionState>>(emptyList())
    val sessions: StateFlow<List<SessionState>> = _sessions.asStateFlow()

    private val _state = MutableStateFlow(TransportState.Idle)
    val state: StateFlow<TransportState> = _state.asStateFlow()

    val kind: TransportKind = transport.kind

    private val _verdicts = MutableSharedFlow<VerdictPayload>(replay = 0, extraBufferCapacity = 64)
    val verdicts: SharedFlow<VerdictPayload> = _verdicts.asSharedFlow()

    /** Ring-buffered accumulator of recent verdicts (cap = verdictRingSize)
     *  — used by the UI for the HistoryStrip filter. */
    private val _verdictHistory = MutableStateFlow<List<VerdictPayload>>(emptyList())
    val verdictHistory: StateFlow<List<VerdictPayload>> = _verdictHistory.asStateFlow()

    private val _logs = MutableStateFlow<List<LogPayload>>(emptyList())
    val logs: StateFlow<List<LogPayload>> = _logs.asStateFlow()

    private val _error = MutableStateFlow<String?>(null)
    val error: StateFlow<String?> = _error.asStateFlow()

    private val counter = SequenceCounter()
    private val logRingSize = 500
    private val verdictRingSize = 100

    fun connect() {
        viewModelScope.launch {
            transport.states().collect { _state.value = it }
        }
        viewModelScope.launch {
            transport.frames().collect { bytes -> handleFrame(bytes) }
        }
        viewModelScope.launch {
            try {
                transport.connect()
            } catch (t: Throwable) {
                _error.value = t.message ?: t::class.simpleName
            }
        }
    }

    fun sendCommand(sessionUUID: String, action: CommandAction, args: Map<String, String>? = null) {
        viewModelScope.launch {
            val payload = CommandPayload(session_uuid = sessionUUID, action = action, args = args)
            val env = mkEnvelope(EnvelopeType.command, payload, counter)
            try {
                transport.send(env) { encodeFrame(it) }
            } catch (t: Throwable) {
                _error.value = t.message ?: t::class.simpleName
            }
        }
    }

    override fun onCleared() {
        super.onCleared()
        viewModelScope.launch { transport.close("ViewModel.onCleared") }
    }

    private fun handleFrame(bytes: ByteArray) {
        val type = sniffType(bytes) ?: return
        when (type) {
            EnvelopeType.state -> {
                val env: Envelope<StatePayload> = try {
                    ChepherdJson.decodeFromString(
                        kotlinx.serialization.serializer(),
                        bytes.toString(Charsets.UTF_8),
                    )
                } catch (_: Throwable) { return }
                env.payload?.sessions?.let { _sessions.value = it }
            }
            EnvelopeType.log -> {
                val env: Envelope<LogPayload> = try {
                    ChepherdJson.decodeFromString(
                        kotlinx.serialization.serializer(),
                        bytes.toString(Charsets.UTF_8),
                    )
                } catch (_: Throwable) { return }
                env.payload?.let { p ->
                    val next = (_logs.value + p).takeLast(logRingSize)
                    _logs.value = next
                }
            }
            EnvelopeType.verdict -> {
                val env: Envelope<VerdictPayload> = try {
                    ChepherdJson.decodeFromString(
                        kotlinx.serialization.serializer(),
                        bytes.toString(Charsets.UTF_8),
                    )
                } catch (_: Throwable) { return }
                env.payload?.let { p ->
                    _verdicts.tryEmit(p)
                    val next = (_verdictHistory.value + p).takeLast(verdictRingSize)
                    _verdictHistory.value = next
                }
            }
            else -> {} // ack / ping / pong / error handled elsewhere
        }
    }

    private fun sniffType(bytes: ByteArray): EnvelopeType? {
        return try {
            val el: JsonElement = ChepherdJson.parseToJsonElement(bytes.toString(Charsets.UTF_8))
            val t = el.jsonObject["type"]?.jsonPrimitive?.content ?: return null
            EnvelopeType.valueOf(t)
        } catch (_: Throwable) {
            null
        }
    }
}
