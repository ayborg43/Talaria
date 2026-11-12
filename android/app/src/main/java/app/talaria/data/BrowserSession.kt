package app.talaria.data

import android.graphics.BitmapFactory
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString

enum class LiveState { Connecting, Live, Taken, Closed, Failed }

@Serializable
private data class LiveMsg(
    val t: String = "", val w: Int = 0, val h: Int = 0, val msg: String = "", val kind: String = "", val url: String = "",
    val signin: List<SignInField> = emptyList(),
)

/**
 * One live view of one browser tab: pictures arrive as the page paints, and taps, scrolls and keys go back in order over
 * the same WebSocket, so the page answers as fast as the network allows.
 */
class BrowserSession(private val repo: Repo, val tabId: String) {
    private val _frame = MutableStateFlow<ImageBitmap?>(null)
    val frame: StateFlow<ImageBitmap?> = _frame.asStateFlow()
    private val _state = MutableStateFlow(LiveState.Connecting)
    val state: StateFlow<LiveState> = _state.asStateFlow()
    private val _error = MutableStateFlow("")
    val error: StateFlow<String> = _error.asStateFlow()
    /** What has focus in the page: "" none, otherwise text, password, and so on. */
    private val _focus = MutableStateFlow("")
    val focus: StateFlow<String> = _focus.asStateFlow()
    private val _url = MutableStateFlow("")
    val url: StateFlow<String> = _url.asStateFlow()
    private val _signin = MutableStateFlow<List<SignInField>>(emptyList())
    val signin: StateFlow<List<SignInField>> = _signin.asStateFlow()
    /** The page's own size, which pointer positions are given in. */
    @Volatile var pageW = 1280; @Volatile var pageH = 800
    private var socket: WebSocket? = null
    @Volatile private var closed = false

    fun connect() {
        closed = false
        _state.value = LiveState.Connecting; _error.value = ""
        socket = repo.api.openSocket("/api/browser/live?id=" + java.net.URLEncoder.encode(tabId, "UTF-8"), object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) { _state.value = LiveState.Live }
            override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                val b = bytes.toByteArray()
                BitmapFactory.decodeByteArray(b, 0, b.size)?.let { _frame.value = it.asImageBitmap() }
            }
            override fun onMessage(webSocket: WebSocket, text: String) {
                val m = runCatching { AppJson.decodeFromString<LiveMsg>(text) }.getOrNull() ?: return
                when (m.t) {
                    "ready" -> { if (m.w > 0) pageW = m.w; if (m.h > 0) pageH = m.h; _state.value = LiveState.Live }
                    "focus" -> { _focus.value = m.kind; _url.value = m.url; _signin.value = m.signin }
                    "taken" -> _state.value = LiveState.Taken
                    "error" -> { _error.value = m.msg; _state.value = LiveState.Failed }
                }
            }
            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) { webSocket.close(1000, null) }
            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) { if (_state.value == LiveState.Live || _state.value == LiveState.Connecting) _state.value = LiveState.Closed }
            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                if (closed) return
                _error.value = when (response?.code) {
                    404 -> "That tab is no longer open."
                    403 -> "The server did not accept this connection."
                    429 -> "Too many live views are open."
                    else -> "The live view lost its connection."
                }
                _state.value = LiveState.Failed
            }
        })
    }

    fun close() { closed = true; socket?.close(1000, null); socket = null }

    private fun send(build: kotlinx.serialization.json.JsonObjectBuilder.() -> Unit) { socket?.send(buildJsonObject(build).toString()) }

    /** x and y are positions in the page, not on the screen. */
    fun mouse(kind: String, x: Float, y: Float, clicks: Int = 1) = send { put("t", "mouse"); put("k", kind); put("x", x); put("y", y); put("b", "left"); put("n", clicks) }
    fun tap(x: Float, y: Float, clicks: Int = 1) { mouse("down", x, y, clicks); mouse("up", x, y, clicks) }
    fun wheel(x: Float, y: Float, dx: Float, dy: Float) = send { put("t", "wheel"); put("x", x); put("y", y); put("dx", dx); put("dy", dy) }
    fun key(name: String, modifiers: Int = 0) = send { put("t", "key"); put("key", name); put("mod", modifiers) }
    fun text(s: String) = send { put("t", "text"); put("text", s) }
}
