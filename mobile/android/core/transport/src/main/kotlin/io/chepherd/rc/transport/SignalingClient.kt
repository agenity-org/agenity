// HTTPSignaling — Kotlin SignalingClient impl calling chepherd-relay's
// /v1/signaling/{offer,candidate,candidates} endpoints. Mirrors the
// Swift HTTPSignaling and the TS HTTPSignaling.

package io.chepherd.rc.transport

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.channels.awaitClose
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.callbackFlow
import kotlinx.coroutines.flow.flowOn
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject

data class HTTPSignalingConfig(
    val baseUrl: String,
    val authToken: String,
    val httpClient: OkHttpClient = OkHttpClient(),
)

class HTTPSignaling(private val cfg: HTTPSignalingConfig) : SignalingClient {

    private val jsonMt = "application/json".toMediaType()

    override suspend fun exchangeOffer(bastionId: String, sdp: String): String = withContext(Dispatchers.IO) {
        val body = JSONObject().apply {
            put("bastion_id", bastionId)
            put("offer", JSONObject().apply {
                put("type", "offer")
                put("sdp", sdp)
            })
        }
        val req = Request.Builder()
            .url("${cfg.baseUrl.trimEnd('/')}/v1/signaling/offer")
            .header("Authorization", "Bearer ${cfg.authToken}")
            .post(body.toString().toRequestBody(jsonMt))
            .build()
        cfg.httpClient.newCall(req).execute().use { resp ->
            val text = resp.body?.string() ?: ""
            check(resp.isSuccessful) { "signaling/offer rejected ${resp.code}: $text" }
            val parsed = JSONObject(text)
            parsed.getJSONObject("answer").getString("sdp")
        }
    }

    override suspend fun postCandidate(
        bastionId: String,
        candidate: String,
        sdpMid: String?,
        sdpMLineIndex: Int?,
    ) = withContext(Dispatchers.IO) {
        val body = JSONObject().apply {
            put("bastion_id", bastionId)
            put("candidate", JSONObject().apply {
                put("candidate", candidate)
                if (sdpMid != null) put("sdpMid", sdpMid)
                if (sdpMLineIndex != null) put("sdpMLineIndex", sdpMLineIndex)
            })
        }
        val req = Request.Builder()
            .url("${cfg.baseUrl.trimEnd('/')}/v1/signaling/candidate")
            .header("Authorization", "Bearer ${cfg.authToken}")
            .post(body.toString().toRequestBody(jsonMt))
            .build()
        cfg.httpClient.newCall(req).execute().use { resp ->
            val text = resp.body?.string() ?: ""
            check(resp.isSuccessful) { "signaling/candidate rejected ${resp.code}: $text" }
        }
    }

    override fun recvCandidates(bastionId: String): Flow<RemoteCandidate> = callbackFlow {
        // Inline-launch via launch{}, scoped to the callbackFlow's coroutine
        // scope (the receiver of callbackFlow IS a CoroutineScope). This
        // avoids the DelicateCoroutinesApi opt-in that GlobalScope would
        // require under Kotlin 2.0 + ensures cancellation propagates from
        // the consumer to this loop automatically.
        val job = launch(Dispatchers.IO) {
            try {
                while (!isClosedForSend) {
                    try {
                        val cands = pollOnce(bastionId)
                        for (c in cands) trySend(c)
                    } catch (t: Throwable) {
                        delay(1000)
                    }
                }
            } catch (_: Throwable) {
                // exit gracefully
            }
        }
        awaitClose { job.cancel() }
    }.flowOn(Dispatchers.IO)

    private fun pollOnce(bastionId: String): List<RemoteCandidate> {
        val req = Request.Builder()
            .url("${cfg.baseUrl.trimEnd('/')}/v1/signaling/candidates?bastion_id=${java.net.URLEncoder.encode(bastionId, "UTF-8")}")
            .header("Authorization", "Bearer ${cfg.authToken}")
            .get()
            .build()
        return cfg.httpClient.newCall(req).execute().use { resp ->
            if (resp.code == 204) return@use emptyList()
            if (!resp.isSuccessful) return@use emptyList()
            val text = resp.body?.string() ?: return@use emptyList()
            val parsed = JSONObject(text)
            val arr: JSONArray = parsed.optJSONArray("candidates") ?: return@use emptyList()
            buildList {
                for (i in 0 until arr.length()) {
                    val c = arr.getJSONObject(i)
                    add(
                        RemoteCandidate(
                            sdp = c.optString("candidate", ""),
                            sdpMid = if (c.isNull("sdpMid")) null else c.optString("sdpMid"),
                            sdpMLineIndex = if (c.isNull("sdpMLineIndex")) null else c.optInt("sdpMLineIndex"),
                        )
                    )
                }
            }
        }
    }
}
