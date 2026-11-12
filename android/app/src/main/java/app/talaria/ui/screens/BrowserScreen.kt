package app.talaria.ui.screens

import app.talaria.ui.theme.Ti

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.calculateCentroid
import androidx.compose.foundation.gestures.calculatePan
import androidx.compose.foundation.gestures.calculateZoom
import androidx.compose.foundation.gestures.detectDragGestures
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AssistChip
import app.talaria.ui.components.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.FilledTonalIconButton
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import app.talaria.ui.components.SecondaryButton
import app.talaria.ui.components.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import app.talaria.ui.components.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.graphics.TransformOrigin
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.autofill.ContentType
import androidx.compose.ui.semantics.contentType
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import app.talaria.data.BrowserSession
import app.talaria.data.BrowserTabs
import app.talaria.data.LiveState
import app.talaria.data.Repo
import app.talaria.data.SignInField
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.LiveDot
import app.talaria.ui.components.Panel
import app.talaria.ui.components.Pill
import app.talaria.ui.components.Tone
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.extra
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/** The agent's browser, live. Tap, scroll and type to do anything by hand; the agent carries on afterwards. */
@Composable
fun BrowserScreen(repo: Repo, onBack: () -> Unit) {
    BackHandler(onBack = onBack)
    val tabs = rememberPolled(3000, "browser-tabs") { repo.api.getJson<BrowserTabs>("/api/browser") }
    var chosen by remember { mutableStateOf<String?>(null) }
    val list = tabs.value?.tabs.orEmpty()
    val active = list.firstOrNull { it.id == chosen } ?: list.firstOrNull()

    Column(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background).statusBarsPadding().navigationBarsPadding().imePadding()) {
        TopAppBar(
            title = { Text(active?.url?.let { hostOf(it) } ?: "Browser", style = MaterialTheme.typography.titleLarge, maxLines = 1, overflow = TextOverflow.Ellipsis) },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
            windowInsets = androidx.compose.foundation.layout.WindowInsets(0, 0, 0, 0),
        )
        RecordBar(repo)
        if (list.size > 1) Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp).verticalScroll(rememberScrollState(), enabled = false), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            list.forEach { t -> FilterChip(selected = t.id == active?.id, onClick = { chosen = t.id }, label = { Text(hostOf(t.url), maxLines = 1) }) }
        }
        when {
            active == null -> Box(Modifier.weight(1f).fillMaxWidth()) {
                if (tabs.loading) Unit else EmptyState(
                    "No page is open", tabs.error ?: "Ask Talaria in Chat to open a page. When it does, it appears here and you can use it by hand.",
                )
            }
            else -> LiveView(repo, active.id, Modifier.weight(1f))
        }
    }
}

private fun hostOf(url: String): String = runCatching { java.net.URI(url).host ?: url }.getOrDefault(url).removePrefix("www.").ifBlank { "Page" }

// ---- teaching a routine ----

@Composable
private fun RecordBar(repo: Repo) {
    val rec = rememberPolled(2500, "recording") { repo.api.get("/api/routines") }
    val scope = rememberCoroutineScope()
    val obj = remember(rec.value) { runCatching { app.talaria.data.AppJson.parseToJsonElement(rec.value ?: "{}") as JsonObject }.getOrNull() }
    if (obj == null || (obj["available"] as? JsonPrimitive)?.content != "true") return
    val recording = (obj["recording"] as? JsonPrimitive)?.content == "true"
    val count = (obj["count"] as? JsonPrimitive)?.content?.toIntOrNull() ?: 0
    var naming by remember { mutableStateOf(false) }
    var name by remember { mutableStateOf("") }
    var error by remember { mutableStateOf<String?>(null) }
    fun act(body: JsonObject, then: () -> Unit = {}) = scope.launch { try { repo.api.postJson("/api/routines", body); rec.refresh(); error = null; then() } catch (e: Exception) { error = e.message } }

    Surface(Modifier.fillMaxWidth(), color = if (recording) MaterialTheme.extra.badTint else MaterialTheme.colorScheme.surfaceContainer) {
        Column(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                if (recording) { LiveDot(MaterialTheme.extra.bad); Text("Recording · $count ${if (count == 1) "step" else "steps"}", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.extra.bad, modifier = Modifier.weight(1f)) }
                else Text("Teach it a routine: record what you do here.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f))
                if (recording) {
                    SecondaryButton(onClick = { act(buildJsonObject { put("action", "discard") }) }, shape = MaterialTheme.shapes.medium) { Text("Discard") }
                    Button(onClick = { naming = true }, enabled = count > 0, shape = MaterialTheme.shapes.medium) { Icon(Ti.Stop, null, Modifier.size(18.dp)); Text("Save", Modifier.padding(start = 4.dp)) }
                } else SecondaryButton(onClick = { act(buildJsonObject { put("action", "start") }) }, shape = MaterialTheme.shapes.medium) { Icon(Ti.Record, null, Modifier.size(14.dp), tint = MaterialTheme.extra.bad); Text("Record", Modifier.padding(start = 6.dp)) }
            }
            if (naming) {
                Gap(8.dp)
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedTextField(name, { name = it.take(60) }, label = { Text("Name this routine") }, singleLine = true, modifier = Modifier.weight(1f), shape = MaterialTheme.shapes.medium)
                    Button(onClick = { act(buildJsonObject { put("action", "save"); put("name", name.trim()) }) { naming = false; name = "" } }, enabled = name.isNotBlank(), shape = MaterialTheme.shapes.medium) { Text("Save") }
                }
            }
            error?.let { Gap(4.dp); Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.extra.bad) }
        }
    }
}

// ---- the live page ----

@Composable
private fun LiveView(repo: Repo, tabId: String, modifier: Modifier) {
    val session = remember(tabId) { BrowserSession(repo, tabId) }
    DisposableEffect(session) { session.connect(); onDispose { session.close() } }
    val frame by session.frame.collectAsState()
    val state by session.state.collectAsState()
    val focus by session.focus.collectAsState()
    val signin by session.signin.collectAsState()
    val error by session.error.collectAsState()
    var typing by remember { mutableStateOf(false) }
    var showBoxes by remember { mutableStateOf(true) }
    var scale by remember { mutableFloatStateOf(1f) }
    var offset by remember { mutableStateOf(Offset.Zero) }
    var box by remember { mutableStateOf(IntSize.Zero) }

    // The picture is as wide as the screen; zooming scales it about the top-left corner and keeps it inside the frame.
    fun imageHeight(): Float = frame?.let { box.width.toFloat() * it.height / it.width } ?: 0f
    fun clamp(o: Offset, sc: Float): Offset {
        val minX = -(sc - 1f) * box.width
        val minY = minOf(0f, box.height - sc * imageHeight())
        return Offset(o.x.coerceIn(minX, 0f), o.y.coerceIn(minY, 0f))
    }
    fun zoomBy(factor: Float, centroid: Offset) {
        val ns = (scale * factor).coerceIn(1f, 4f)
        offset = clamp(centroid - (centroid - offset) * (ns / scale), ns)
        scale = ns
    }
    /** A position on screen as a position in the page. */
    fun toPage(pos: Offset): Pair<Float, Float> {
        val img = (pos - offset) / scale
        val ih = imageHeight().takeIf { it > 0 } ?: box.height.toFloat()
        return (img.x / box.width * session.pageW).coerceIn(0f, session.pageW.toFloat()) to (img.y / ih * session.pageH).coerceIn(0f, session.pageH.toFloat())
    }

    Column(modifier.fillMaxWidth()) {
        Box(
            Modifier.weight(1f).fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerHigh).clipToBounds().onSizeChanged { box = it }
                .pointerInput(session) {
                    detectTapGestures(
                        onTap = { o -> val (x, y) = toPage(o); session.tap(x, y) },
                        onDoubleTap = { o -> val (x, y) = toPage(o); session.tap(x, y, 2) },
                    )
                }
                .pointerInput(session) {
                    detectDragGestures { change, drag ->
                        change.consume()
                        val (x, y) = toPage(change.position)
                        val k = session.pageW.toFloat() / (size.width * scale)
                        // Dragging up scrolls the page down, as a finger does on any phone. Zoomed in, sideways moves the picture instead.
                        if (scale > 1f) { offset = clamp(Offset(offset.x + drag.x, offset.y), scale); session.wheel(x, y, 0f, -drag.y * k) }
                        else session.wheel(x, y, -drag.x * k, -drag.y * k)
                    }
                }
                // Two fingers zoom and move the picture. This is last so it sees the touches first and the single-finger gestures above stand down.
                .pointerInput(session) {
                    awaitEachGesture {
                        awaitFirstDown(requireUnconsumed = false)
                        do {
                            val ev = awaitPointerEvent()
                            if (ev.changes.count { it.pressed } >= 2) {
                                val z = ev.calculateZoom(); val pan = ev.calculatePan(); val c = ev.calculateCentroid()
                                val ns = (scale * z).coerceIn(1f, 4f)
                                offset = clamp(c - (c - offset) * (ns / scale) + pan, ns)
                                scale = ns
                                ev.changes.forEach { it.consume() }
                            }
                        } while (ev.changes.any { it.pressed })
                    }
                },
        ) {
            val bmp = frame
            if (bmp != null) {
                Image(
                    bmp, "The agent's browser page", contentScale = ContentScale.FillWidth,
                    modifier = Modifier.fillMaxWidth().aspectRatio(bmp.width.toFloat() / bmp.height).align(Alignment.TopStart).graphicsLayer {
                        transformOrigin = TransformOrigin(0f, 0f); scaleX = scale; scaleY = scale; translationX = offset.x; translationY = offset.y
                    },
                )
            }
            when {
                bmp == null && state == LiveState.Connecting -> Text("Opening the page…", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.align(Alignment.Center))
                state == LiveState.Taken -> Overlay("This page is open on another screen.", "Take it back here") { session.connect() }
                state == LiveState.Failed || state == LiveState.Closed -> Overlay(error.ifBlank { "The live view closed." }, "Reconnect") { session.connect() }
            }
            if (state == LiveState.Live) Pill("Live", Tone.Ok, Modifier.align(Alignment.TopEnd).padding(8.dp))
            if (bmp != null) Column(Modifier.align(Alignment.BottomEnd).padding(10.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                ZoomButton(Ti.Plus, "Zoom in") { zoomBy(1.6f, Offset.Zero) }
                ZoomButton(Ti.Minus, "Zoom out") { zoomBy(1f / 1.6f, Offset.Zero) }
            }
        }

        val boxes = signin
        val secretFocus = focus == "password"
        when {
            boxes.isNotEmpty() && (focus.isEmpty() || secretFocus || showBoxes && !typing && focus.isEmpty()) -> SignInPanel(repo, tabId, boxes) { typing = true; showBoxes = false }
            focus.isNotEmpty() || typing -> TypeBar(session, focus, hasBoxes = boxes.isNotEmpty(), onBoxes = { showBoxes = true; typing = false }) { typing = false }
            else -> Surface(color = MaterialTheme.colorScheme.surfaceContainer, modifier = Modifier.fillMaxWidth()) {
                Row(Modifier.padding(horizontal = 12.dp, vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text("Tap to click, drag to scroll, pinch to zoom.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f))
                    IconButton(onClick = { typing = true }) { Icon(Ti.Keyboard, "Open the keyboard") }
                }
            }
        }
    }
}

@Composable
private fun ZoomButton(icon: androidx.compose.ui.graphics.vector.ImageVector, label: String, onClick: () -> Unit) {
    FilledTonalIconButton(onClick = onClick, modifier = Modifier.size(48.dp), colors = IconButtonDefaults.filledTonalIconButtonColors(containerColor = MaterialTheme.colorScheme.inverseSurface.copy(alpha = 0.72f), contentColor = MaterialTheme.colorScheme.inverseOnSurface)) { Icon(icon, label) }
}

@Composable
private fun BoxScope.Overlay(text: String, action: String, onClick: () -> Unit) {
    Surface(Modifier.align(Alignment.Center).padding(24.dp), shape = MaterialTheme.shapes.large, color = MaterialTheme.colorScheme.surfaceContainerHigh) {
        Column(Modifier.padding(20.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            Text(text, style = MaterialTheme.typography.bodyMedium)
            Gap(12.dp)
            Button(onClick = onClick, shape = MaterialTheme.shapes.medium) { Icon(Ti.Refresh, null, Modifier.size(18.dp)); Text(action, Modifier.padding(start = 6.dp)) }
        }
    }
}

private typealias BoxScope = androidx.compose.foundation.layout.BoxScope

/** What you type goes straight into the page, a character at a time; keys the keyboard lacks are one tap away. */
@Composable
private fun TypeBar(session: BrowserSession, focus: String, hasBoxes: Boolean, onBoxes: () -> Unit, onClose: () -> Unit) {
    var text by remember(focus) { mutableStateOf("") }
    val secret = focus == "password"
    Surface(color = MaterialTheme.colorScheme.surfaceContainer, modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(horizontal = 12.dp, vertical = 8.dp)) {
            OutlinedTextField(
                value = text,
                onValueChange = { new ->
                    // Send only what changed: added letters as text, removed ones as Backspace.
                    when {
                        new.length > text.length && new.startsWith(text) -> session.text(new.substring(text.length))
                        new.length < text.length && text.startsWith(new) -> repeat(text.length - new.length) { session.key("Backspace") }
                        else -> { repeat(text.length) { session.key("Backspace") }; if (new.isNotEmpty()) session.text(new) }
                    }
                    text = new
                },
                placeholder = { Text(if (secret) "Type the password here. It goes straight into the page." else "Type here. It goes straight into the page.") },
                singleLine = true, modifier = Modifier.fillMaxWidth().semantics { contentDescription = "Type into the page" }, shape = MaterialTheme.shapes.medium,
                visualTransformation = if (secret) PasswordVisualTransformation() else VisualTransformation.None,
                keyboardOptions = KeyboardOptions(keyboardType = if (secret) KeyboardType.Password else KeyboardType.Text, imeAction = ImeAction.Go),
                keyboardActions = KeyboardActions(onGo = { session.key("Enter"); text = "" }),
            )
            Gap(6.dp)
            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(0.dp)) {
                AssistChip(onClick = { session.key("Enter"); text = "" }, label = { Text("Enter") }, leadingIcon = { Icon(Ti.Return, null, Modifier.size(18.dp)) })
                AssistChip(onClick = { session.key("Tab"); text = "" }, label = { Text("Tab") })
                AssistChip(onClick = { session.key("Backspace"); text = text.dropLast(1) }, label = { Text("Delete") }, leadingIcon = { Icon(Ti.Backspace, null, Modifier.size(18.dp)) })
                AssistChip(onClick = { session.key("Escape") }, label = { Text("Esc") })
                if (hasBoxes) TextButton(onClick = onBoxes) { Text("Sign-in boxes") }
                TextButton(onClick = onClose) { Text("Done") }
            }
        }
    }
}

/** The page asks for specific things (an email, then a password, or a code): the boxes follow the page. */
@Composable
private fun SignInPanel(repo: Repo, tabId: String, fields: List<SignInField>, onTypeDirectly: () -> Unit) {
    val values = remember(fields.map { it.kind + it.label }) { mutableStateListOf<String>().also { l -> fields.forEach { _ -> l.add("") } } }
    val scope = rememberCoroutineScope()
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val title = when {
        fields.size > 1 -> "This page asks for your details"
        fields[0].kind == "password" -> if (fields[0].new) "This page asks you to choose a password" else "This page asks for your password"
        fields[0].kind == "code" -> "This page asks for a code"
        else -> "This page asks for your username or email"
    }
    Surface(color = MaterialTheme.colorScheme.surfaceContainer, modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(horizontal = 12.dp, vertical = 10.dp).verticalScroll(rememberScrollState())) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Icon(Ti.Lock, null, Modifier.size(18.dp), tint = MaterialTheme.extra.brassText)
                Text(title, style = MaterialTheme.typography.titleSmall)
            }
            Text("What you type here goes straight into the page. The agent never sees it and it is not kept.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Gap(8.dp)
            fields.forEachIndexed { i, f ->
                OutlinedTextField(
                    values[i], { values[i] = it }, label = { Text(f.label.ifBlank { f.kind }) }, singleLine = true,
                    modifier = Modifier.fillMaxWidth().padding(bottom = 8.dp).semantics {
                        // Tells the phone's password manager what each box is for, so it can offer what it has saved.
                        contentType = when (f.kind) {
                            "password" -> if (f.new) ContentType.NewPassword else ContentType.Password
                            "code" -> ContentType.SmsOtpCode
                            else -> ContentType.Username + ContentType.EmailAddress
                        }
                    }, shape = MaterialTheme.shapes.medium,
                    visualTransformation = if (f.kind == "password") PasswordVisualTransformation() else VisualTransformation.None,
                    keyboardOptions = KeyboardOptions(
                        keyboardType = when (f.kind) { "password" -> KeyboardType.Password; "code" -> KeyboardType.Number; else -> KeyboardType.Email },
                        imeAction = if (i == fields.lastIndex) ImeAction.Done else ImeAction.Next,
                    ),
                )
            }
            error?.let { ErrorNote(it); Gap(8.dp) }
            Button(
                onClick = {
                    busy = true; error = null
                    scope.launch {
                        try {
                            repo.api.postJson("/api/browser/act", buildJsonObject {
                                put("id", tabId); put("kind", "login")
                                put("fields", buildJsonArray { fields.forEachIndexed { i, f -> if (values[i].isNotEmpty()) add(buildJsonObject { put("index", i); put("kind", f.kind); put("value", values[i]) }) } })
                            })
                            for (i in values.indices) values[i] = ""
                        } catch (e: Exception) { error = e.message }
                        busy = false
                    }
                },
                enabled = values.any { it.isNotEmpty() } && !busy, shape = MaterialTheme.shapes.medium, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp),
            ) { Text(if (fields.size == 1) "Fill ${fields[0].label.lowercase().ifBlank { "it in" }}" else "Fill these in") }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("It fills the boxes; you press the page's own button to go on.", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f))
                TextButton(onClick = onTypeDirectly) { Text("Type directly") }
            }
        }
    }
}
