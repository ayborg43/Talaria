package app.talaria.data

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import okhttp3.Call
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.io.IOException
import java.util.concurrent.TimeUnit

/** The server said no, with words the person can read. */
class ApiException(val code: Int, message: String) : IOException(message)

/** The session ended: the cookie is no longer good and the person has to sign in again. */
class AuthLost : IOException("Your session ended. Sign in again.")

/**
 * Talks to the gateway the way the web app does: a session cookie, and an `X-Talaria` header plus an Origin that
 * matches the server's address on anything that changes something (that is the server's check that a request came
 * from the app and not from another site).
 */
class Api(private val store: Credentials) {
    private val http = OkHttpClient.Builder()
        .connectTimeout(10, TimeUnit.SECONDS)
        .readTimeout(40, TimeUnit.SECONDS)
        .writeTimeout(60, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    /** For the event stream, which stays open: no read timeout, and a ping so a dead link is noticed. */
    private val longLived = http.newBuilder().readTimeout(0, TimeUnit.MILLISECONDS).pingInterval(25, TimeUnit.SECONDS).build()

    var onAuthLost: () -> Unit = {}

    val base: String get() = store.baseUrl

    private fun builder(path: String, origin: String = store.baseUrl, cookie: String = store.cookie): Request.Builder =
        Request.Builder().url(origin + path)
            .header("X-Talaria", "1")
            .header("Origin", origin)
            .header("User-Agent", "Talaria-Android/0.1")
            .apply { if (cookie.isNotEmpty()) header("Cookie", cookie) }

    private fun Response.check(): Response {
        if (code == 401) { close(); onAuthLost(); throw AuthLost() }
        if (!isSuccessful) {
            val raw = try { body?.string().orEmpty() } catch (_: IOException) { "" }
            close()
            val msg = runCatching { AppJson.decodeFromString<ErrorBody>(raw).error }.getOrNull().orEmpty()
            throw ApiException(code, msg.ifBlank { "The server answered ${code}." })
        }
        return this
    }

    private suspend fun run(req: Request): String = withContext(Dispatchers.IO) {
        try {
            http.newCall(req).execute().check().use { it.body?.string().orEmpty() }
        } catch (e: ApiException) { throw e } catch (e: AuthLost) { throw e }
        catch (e: IOException) { throw IOException(friendly(e), e) }
    }

    private fun friendly(e: IOException): String = when {
        e is java.net.UnknownHostException -> "That address could not be found. Check it and your connection."
        e is java.net.ConnectException || e.message?.contains("Failed to connect") == true -> "Could not reach the server. Is it running, and is this phone on a network that can see it?"
        e is java.net.SocketTimeoutException -> "The server took too long to answer."
        e is javax.net.ssl.SSLException -> "The secure connection failed (${e.message?.take(80)})."
        else -> e.message ?: "The connection failed."
    }

    suspend fun get(path: String): String = run(builder(path).get().build())

    suspend inline fun <reified T> getJson(path: String): T = AppJson.decodeFromString(get(path))

    suspend fun post(path: String, json: String = "{}"): String =
        run(builder(path).post(json.toRequestBody("application/json".toMediaType())).build())

    suspend fun postJson(path: String, body: JsonObject): String = post(path, body.toString())

    suspend inline fun <reified T> postBody(path: String, value: T): String = post(path, AppJson.encodeToString(value))

    suspend fun postBytes(path: String, bytes: ByteArray, mime: String): String =
        run(builder(path).post(bytes.toRequestBody(mime.toMediaType())).build())

    suspend fun getBytes(path: String): ByteArray = withContext(Dispatchers.IO) {
        try { http.newCall(builder(path).get().build()).execute().check().use { it.body?.bytes() ?: ByteArray(0) } }
        catch (e: ApiException) { throw e } catch (e: AuthLost) { throw e }
        catch (e: IOException) { throw IOException(friendly(e), e) }
    }

    /** Sends a file (a photo or a PDF) to the server's kept files; the server notes it in the conversation. */
    suspend fun upload(name: String, mime: String, bytes: ByteArray): String = run(
        builder("/api/files").post(
            okhttp3.MultipartBody.Builder().setType(okhttp3.MultipartBody.FORM)
                .addFormDataPart("file", name, bytes.toRequestBody(mime.toMediaType())).build(),
        ).build(),
    )

    /** Signs in and returns "name=value" for the session cookie the server set. */
    suspend fun login(origin: String, password: String): String = withContext(Dispatchers.IO) {
        val body = AppJson.encodeToString(mapOf("password" to password)).toRequestBody("application/json".toMediaType())
        val req = builder("/api/login", origin, "").post(body).build()
        try {
            http.newCall(req).execute().use { r ->
                if (r.code == 401) throw ApiException(401, "That password is not right.")
                if (r.code == 429) throw ApiException(429, runCatching { AppJson.decodeFromString<ErrorBody>(r.body?.string().orEmpty()).error }.getOrNull().orEmpty().ifBlank { "Too many tries. Wait a moment." })
                if (r.code == 403) throw ApiException(403, "The server refused this app. If it sits behind a proxy, its public address must match the one you entered.")
                if (!r.isSuccessful) throw ApiException(r.code, "The server answered ${r.code}.")
                val c = r.headers("Set-Cookie").firstOrNull()?.substringBefore(';')
                if (c.isNullOrBlank() || !c.contains('=')) throw ApiException(500, "The server did not start a session.")
                c
            }
        } catch (e: ApiException) { throw e }
        catch (e: IOException) { throw IOException(friendly(e), e) }
    }

    /** Whether a Talaria is at this address, before the password is asked for (the login page is public). */
    suspend fun probe(origin: String): Boolean = withContext(Dispatchers.IO) {
        try {
            http.newCall(Request.Builder().url("$origin/manifest.webmanifest").header("User-Agent", "Talaria-Android/0.1").build()).execute()
                .use { it.isSuccessful && (it.body?.string().orEmpty().contains("Talaria")) }
        } catch (e: IOException) { throw IOException(friendly(e), e) }
    }

    /** The open event stream: one call per connection; cancel it to close. */
    fun streamCall(path: String): Call = longLived.newCall(builder(path).header("Accept", "text/event-stream").get().build())

    fun checkStream(r: Response): Response = r.check()

    fun openSocket(path: String, listener: WebSocketListener): WebSocket {
        val url = (store.baseUrl + path).replaceFirst("http", "ws")
        val req = Request.Builder().url(url)
            .header("Origin", store.baseUrl).header("Cookie", store.cookie).header("X-Talaria", "1")
            .header("User-Agent", "Talaria-Android/0.1").build()
        return longLived.newWebSocket(req, listener)
    }

    fun fileUrl(id: String) = "${store.baseUrl}/api/files/$id"
}
