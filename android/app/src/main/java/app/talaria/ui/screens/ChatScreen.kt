package app.talaria.ui.screens

import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.scaleIn
import androidx.compose.animation.scaleOut
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.slideOutVertically
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextField
import androidx.compose.material3.TextFieldDefaults
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.data.Conn
import app.talaria.data.Message
import app.talaria.data.Repo
import app.talaria.ui.components.Button
import app.talaria.ui.components.Gap
import app.talaria.ui.components.LiveDot
import app.talaria.ui.components.Markdown
import app.talaria.ui.components.Panel
import app.talaria.ui.components.Pill
import app.talaria.ui.components.RoundIconButton
import app.talaria.ui.components.SecondaryButton
import app.talaria.ui.components.TalariaMark
import app.talaria.ui.components.TextButton
import app.talaria.ui.components.Tone
import app.talaria.ui.components.VoicePanel
import app.talaria.ui.components.plainText
import app.talaria.ui.components.rememberHaptics
import app.talaria.ui.humanTool
import app.talaria.ui.parseTime
import app.talaria.ui.seconds
import app.talaria.ui.theme.Mono
import app.talaria.ui.theme.Ti
import app.talaria.ui.theme.extra
import app.talaria.voice.Phase
import app.talaria.voice.VoiceController
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import java.time.LocalDate
import java.time.LocalTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

private val starters = listOf(
    "What is on my plate today?" to Ti.Calendar,
    "Summarise my unread mail" to Ti.File,
    "Research something for me in the background" to Ti.Globe,
    "Remind me about something tomorrow morning" to Ti.History,
)

/** One line of the conversation as it is drawn: a message, or the day it began. */
private sealed interface Row {
    data class Day(val label: String, val key: String) : Row
    data class Msg(val m: Message, val startsReply: Boolean, val endsReply: Boolean, val latest: Boolean = false) : Row
}

private fun dayLabel(at: String): Pair<LocalDate, String>? {
    val t = parseTime(at) ?: return null
    val d = t.atZone(ZoneId.systemDefault()).toLocalDate()
    val today = LocalDate.now()
    val label = when (d) {
        today -> "Today"
        today.minusDays(1) -> "Yesterday"
        else -> DateTimeFormatter.ofPattern("EEEE d MMMM", Locale.getDefault()).format(d)
    }
    return d to label
}

private fun rowsOf(messages: List<Message>): List<Row> {
    val out = ArrayList<Row>(messages.size + 4)
    var lastDay: LocalDate? = null
    messages.forEachIndexed { i, m ->
        dayLabel(m.at)?.let { (d, label) -> if (d != lastDay) { out += Row.Day(label, "day-$d"); lastDay = d } }
        val prev = messages.getOrNull(i - 1)?.role
        val next = messages.getOrNull(i + 1)?.role
        out += Row.Msg(m, startsReply = m.role == "assistant" && prev != "assistant", endsReply = m.role == "assistant" && next != "assistant", latest = i == messages.lastIndex)
    }
    return out
}

@OptIn(ExperimentalMaterial3Api::class, ExperimentalFoundationApi::class)
@Composable
fun ChatScreen(
    repo: Repo,
    voice: VoiceController,
    draft: String,
    onDraft: (String) -> Unit,
    onOpenChats: () -> Unit,
    onOpenBrowser: () -> Unit,
    onAttach: () -> Unit,
    externalError: String?,
    onExternalErrorShown: () -> Unit,
    focusSignal: Int = 0,
    approvalSignal: Int = 0,
    modifier: Modifier = Modifier,
) {
    val uploading by repo.uploading.collectAsState()
    val messages by repo.messages.collectAsState()
    val running by repo.running.collectAsState()
    val typing by repo.typing.collectAsState()
    val conn by repo.conn.collectAsState()
    val paused by repo.paused.collectAsState()
    val model by repo.model.collectAsState()
    val profile by repo.profile.collectAsState()
    val channel by repo.channel.collectAsState()
    val suggestion by repo.suggestion.collectAsState()
    val workers by repo.workers.collectAsState()
    val scope = rememberCoroutineScope()
    val haptics = rememberHaptics()
    var sendError by remember { mutableStateOf<String?>(null) }
    LaunchedEffect(externalError) { if (externalError != null) { sendError = externalError; onExternalErrorShown() } }
    val list = rememberLazyListState()
    val rows = remember(messages) { rowsOf(messages) }
    var searching by rememberSaveable { mutableStateOf(false) }
    var query by rememberSaveable { mutableStateOf("") }
    // While searching, only the messages that hold every word typed are listed.
    val shown = remember(rows, searching, query) {
        if (searching && query.isNotBlank()) rows.filterIsInstance<Row.Msg>().filter { r -> app.talaria.ui.searchIn(listOf(r.m), { it.text + " " + it.tool + " " + it.result }, query).isNotEmpty() } else rows
    }

    val title = channel?.title?.ifBlank { null } ?: "Talaria" // the profile name is yours, not the assistant's
    val doing = workers.firstOrNull { it.kind == "main" }?.doing.orEmpty()

    fun send(text: String) {
        val t = text.trim(); if (t.isEmpty()) return
        sendError = null
        haptics.confirm()
        scope.launch { try { repo.send(t); onDraft("") } catch (e: Exception) { sendError = e.message } }
    }

    // Keep the newest message in view while you are at the end of the conversation; leave the list alone if you have
    // scrolled up to read. A tall reply is shown from its top, a short one at the bottom of the screen.
    var follow by remember { mutableStateOf(true) }
    var programmatic by remember { mutableStateOf(false) }
    val last = messages.lastOrNull()
    LaunchedEffect(list) {
        snapshotFlow { list.isScrollInProgress }.collect { moving -> if (!moving && !programmatic) follow = !list.canScrollForward }
    }
    LaunchedEffect(rows.size, last?.state, last?.text?.length, typing, running, searching) {
        val target = rows.size - 1 + if (running || typing) 1 else 0
        if (searching || target < 0 || !(follow || last?.role == "user")) return@LaunchedEffect
        snapshotFlow { list.layoutInfo.totalItemsCount }.first { it > target }
        programmatic = true
        try { list.animateScrollToItem(target); follow = true } finally { programmatic = false }
    }

    // "Show me what needs my answer": go to the first approval still waiting.
    LaunchedEffect(approvalSignal) {
        if (approvalSignal == 0) return@LaunchedEffect
        val at = rows.indexOfFirst { it is Row.Msg && it.m.role == "approval" && it.m.state == "pending" }
        if (at >= 0) { follow = false; list.animateScrollToItem(at) } else repo.toast("Nothing is waiting for your answer.")
    }
    Column(modifier.fillMaxSize()) {
        if (searching) TopAppBar(
            title = {
                val focus = remember { androidx.compose.ui.focus.FocusRequester() }
                LaunchedEffect(Unit) { focus.requestFocus() }
                androidx.compose.material3.TextField(
                    value = query, onValueChange = { query = it }, singleLine = true, modifier = Modifier.fillMaxWidth().focusRequester(focus),
                    placeholder = { Text("Search this conversation") },
                    colors = TextFieldDefaults.colors(focusedContainerColor = Color.Transparent, unfocusedContainerColor = Color.Transparent, focusedIndicatorColor = Color.Transparent, unfocusedIndicatorColor = Color.Transparent, cursorColor = MaterialTheme.extra.brassText),
                )
            },
            navigationIcon = { IconButton(onClick = { searching = false; query = "" }) { Icon(Ti.Back, "Close search") } },
            actions = {
                if (query.isNotBlank()) { Text("${shown.size}", style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant); IconButton(onClick = { query = "" }) { Icon(Ti.Close, "Clear") } }
            },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background, scrolledContainerColor = MaterialTheme.colorScheme.background),
        ) else TopAppBar(
            title = {
                Column {
                    Text(title, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        if (running) LiveDot()
                        Text(
                            when {
                                paused -> "Paused"
                                running && doing.isNotBlank() -> humanTool(doing)
                                running -> "Working…"
                                channel?.agent?.isNotBlank() == true -> channel?.agent.orEmpty() + if (channel?.read_only == "1") " · looks only" else ""
                                model.isNotBlank() -> model
                                else -> "Ready"
                            },
                            style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
                            maxLines = 1, overflow = TextOverflow.Ellipsis,
                        )
                    }
                }
            },
            actions = {
                IconButton(onClick = { searching = true }, enabled = messages.isNotEmpty()) { Icon(Ti.Search, "Search this conversation") }
                IconButton(onClick = onOpenChats) { Icon(Ti.History, "Conversations") }
                IconButton(onClick = { scope.launch { runCatching { repo.newChat() } } }) { Icon(Ti.Compose, "New chat") }
            },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background, scrolledContainerColor = MaterialTheme.colorScheme.background),
        )

        AnimatedVisibility(conn !is Conn.Online && conn !is Conn.SignedOut, enter = fadeIn() + slideInVertically { -it / 2 }, exit = fadeOut()) {
            Banner(Ti.WifiOff, (conn as? Conn.Offline)?.reason ?: "Connecting…", MaterialTheme.extra.brassSoft, MaterialTheme.extra.brassText)
        }
        AnimatedVisibility(paused, enter = fadeIn() + slideInVertically { -it / 2 }, exit = fadeOut()) {
            Banner(Ti.Pause, "Paused. It will not start anything on its own until you resume it in Settings.", MaterialTheme.colorScheme.surfaceContainerHigh, MaterialTheme.colorScheme.onSurfaceVariant)
        }

        Box(Modifier.weight(1f).fillMaxWidth()) {
            if (messages.isEmpty()) {
                Starters(profile.name, onPick = { send(it) }, modifier = Modifier.fillMaxSize())
            } else {
                LazyColumn(
                    state = list, modifier = Modifier.fillMaxSize(),
                    contentPadding = PaddingValues(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 20.dp),
                    verticalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    itemsIndexed(shown, key = { _, r -> when (r) { is Row.Day -> r.key; is Row.Msg -> r.m.id } }) { _, r ->
                        Box(Modifier.fillMaxWidth().animateItem(fadeInSpec = tween(260), placementSpec = androidx.compose.animation.core.spring(0.8f, 380f)), contentAlignment = Alignment.TopCenter) {
                            Box(Modifier.widthIn(max = 720.dp).fillMaxWidth()) {
                                when (r) {
                                    is Row.Day -> Text(r.label, Modifier.fillMaxWidth().padding(vertical = 10.dp), style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
                                    is Row.Msg -> MessageItem(r.m, repo, r.endsReply, r.latest, onOpenBrowser, onError = { sendError = it })
                                }
                            }
                        }
                    }
                    if ((running || typing) && !searching) item(key = "typing") { TypingRow() }
                    if (searching && query.isNotBlank() && shown.isEmpty()) item(key = "none") { Text("No message here has all of those words.", Modifier.fillMaxWidth().padding(24.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center) }
                }
            }
        }

        AnimatedVisibility(suggestion != null, enter = fadeIn() + slideInVertically { it / 2 }, exit = fadeOut() + slideOutVertically { it / 2 }) {
            suggestion?.let { s ->
                Box(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp), contentAlignment = Alignment.Center) {
                    Panel(Modifier.widthIn(max = 720.dp).fillMaxWidth(), tint = MaterialTheme.extra.brassSoft, edge = MaterialTheme.extra.brassLine.copy(alpha = 0.6f)) {
                        Column(Modifier.padding(16.dp)) {
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Icon(Ti.Sparkles, null, Modifier.size(18.dp), tint = MaterialTheme.extra.brassText)
                                Text(s.title, style = MaterialTheme.typography.titleSmall, color = MaterialTheme.extra.brassText, modifier = Modifier.weight(1f))
                            }
                            if (s.body.isNotBlank()) { Gap(4.dp); Text(s.body, style = MaterialTheme.typography.bodySmall, maxLines = 3, overflow = TextOverflow.Ellipsis) }
                            Gap(10.dp)
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                if (s.startable) Button(onClick = { scope.launch { runCatching { repo.api.postJson("/api/suggestions", buildJsonObject { put("start", s.id) }); repo.reload() } } }) { Text("Start") }
                                TextButton(onClick = { scope.launch { runCatching { repo.api.postJson("/api/suggestions", buildJsonObject { put("hide", s.id) }); repo.reload() } } }) { Text("Hide") }
                            }
                        }
                    }
                }
            }
        }

        sendError?.let { e ->
            Row(Modifier.fillMaxWidth().padding(start = 20.dp, end = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                Icon(Ti.Alert, null, Modifier.size(16.dp), tint = MaterialTheme.extra.bad)
                Text(e, Modifier.weight(1f).padding(start = 8.dp), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.extra.bad)
                IconButton(onClick = { sendError = null }, Modifier.size(40.dp)) { Icon(Ti.Close, "Dismiss", Modifier.size(16.dp)) }
            }
        }

        val vphase by voice.phase.collectAsState()
        val vnotice by voice.notice.collectAsState()
        if (vphase != Phase.Off) VoicePanel(voice)
        else {
            vnotice?.let { n ->
                Row(Modifier.fillMaxWidth().padding(start = 20.dp, end = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(n.text, Modifier.weight(1f), style = MaterialTheme.typography.bodySmall, color = if (n.isError) MaterialTheme.extra.bad else MaterialTheme.colorScheme.onSurfaceVariant)
                    IconButton(onClick = voice::clearNotice, Modifier.size(40.dp)) { Icon(Ti.Close, "Dismiss", Modifier.size(16.dp)) }
                }
            }
            Composer(draft, onDraft, onSend = { send(draft) }, voice = voice, enabled = conn is Conn.Online, onAttach = onAttach, uploading = uploading, focusSignal = focusSignal)
        }
    }
}

@Composable
private fun Banner(icon: androidx.compose.ui.graphics.vector.ImageVector, text: String, container: Color, content: Color) {
    Surface(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp), shape = MaterialTheme.shapes.medium, color = container) {
        Row(Modifier.padding(horizontal = 14.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            Icon(icon, null, Modifier.size(18.dp), tint = content)
            Text(text, style = MaterialTheme.typography.bodySmall, color = content, maxLines = 3)
        }
    }
}

private fun greeting(): String = when (LocalTime.now().hour) { in 5..11 -> "Good morning"; in 12..17 -> "Good afternoon"; else -> "Good evening" }

@Composable
private fun Starters(name: String, onPick: (String) -> Unit, modifier: Modifier = Modifier) {
    Column(modifier.padding(horizontal = 20.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
        TalariaMark(52.dp)
        Gap(18.dp)
        Text(if (name.isNotBlank()) "${greeting()}, $name" else greeting(), style = MaterialTheme.typography.headlineMedium, textAlign = TextAlign.Center)
        Gap(4.dp)
        Text("What can I do for you?", style = MaterialTheme.typography.bodyLarge, color = MaterialTheme.colorScheme.onSurfaceVariant)
        Gap(28.dp)
        Column(Modifier.widthIn(max = 520.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            starters.forEach { (text, icon) ->
                Surface(
                    onClick = { onPick(text) }, shape = MaterialTheme.shapes.large, color = MaterialTheme.colorScheme.surfaceContainerLow,
                    border = BorderStroke(0.5.dp, MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.55f)), modifier = Modifier.fillMaxWidth(),
                ) {
                    Row(Modifier.heightIn(min = 56.dp).padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
                        Icon(icon, null, Modifier.size(20.dp), tint = MaterialTheme.extra.brassText)
                        Text(text, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f))
                    }
                }
            }
        }
    }
}

@Composable
private fun TypingRow() {
    val t = rememberInfiniteTransition(label = "typing")
    Row(Modifier.padding(start = 4.dp, top = 4.dp, bottom = 4.dp).semantics { contentDescription = "Talaria is working" }, horizontalArrangement = Arrangement.spacedBy(5.dp), verticalAlignment = Alignment.CenterVertically) {
        for (i in 0..2) {
            val y by t.animateFloat(0f, -5f, infiniteRepeatable(tween(420, delayMillis = i * 120), RepeatMode.Reverse), label = "d$i")
            Box(Modifier.size(7.dp).offsetY(y).clip(CircleShape).background(MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.7f)))
        }
    }
}

private fun Modifier.offsetY(dp: Float): Modifier = this.graphicsLayer { translationY = dp.dp.toPx() }

@Composable
private fun MessageItem(m: Message, repo: Repo, endsReply: Boolean, latest: Boolean, onOpenBrowser: () -> Unit, onError: (String) -> Unit) {
    when (m.role) {
        "user" -> if (UPLOADED.matches(m.text)) Attachment(m.text, repo, onError) else Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.CenterEnd) {
            Surface(Modifier.widthIn(max = 560.dp), color = MaterialTheme.extra.bubble, shape = RoundedCornerShape(24.dp, 24.dp, 8.dp, 24.dp)) {
                SelectionContainer { Text(m.text, Modifier.padding(horizontal = 16.dp, vertical = 11.dp), style = MaterialTheme.typography.bodyLarge) }
            }
        }
        "assistant" -> Column(Modifier.fillMaxWidth().padding(end = 8.dp)) {
            SelectionContainer { Markdown(m.text) }
            if (endsReply) ReplyRow(m, repo, latest, onError)
        }
        "tool" -> if (m.url.isNotBlank()) Handoff(m, onOpenBrowser) else {
            val art = remember(m.artifact) { app.talaria.ui.Artifact.of(m.artifact) }
            val scope = rememberCoroutineScope()
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                ToolRow(m)
                if (art != null) ArtifactCard(art, send = { text -> scope.launch { runCatching { repo.send(text) }.onFailure { onError(it.message ?: "That could not be sent.") } } }, openBrowser = onOpenBrowser)
            }
        }
        "approval" -> Approval(m, repo, onError)
        else -> Text(m.text, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

private val UPLOADED = Regex("""^Uploaded (.+) — /api/files/([0-9a-f]+)$""")

/** A file you sent: shown as what it is, not as the line the server wrote. Tap to open it. */
@Composable
private fun Attachment(text: String, repo: Repo, onError: (String) -> Unit) {
    val (name, id) = UPLOADED.matchEntire(text)!!.destructured
    val openFile = app.talaria.ui.LocalOpenFile.current
    val mime = when (name.substringAfterLast('.', "").lowercase()) { "pdf" -> "application/pdf"; "png" -> "image/png"; "jpg", "jpeg" -> "image/jpeg"; "gif" -> "image/gif"; "webp" -> "image/webp"; else -> "" }
    Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.CenterEnd) {
        Surface(
            onClick = { openFile(app.talaria.data.FileMeta(id = id, name = name, mime = mime)) },
            shape = RoundedCornerShape(20.dp), color = MaterialTheme.extra.bubble, modifier = Modifier.widthIn(max = 300.dp),
        ) {
            Row(Modifier.padding(horizontal = 14.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                Icon(Ti.File, null, Modifier.size(22.dp), tint = MaterialTheme.extra.brassText)
                Column(Modifier.weight(1f, fill = false)) {
                    Text(name, style = MaterialTheme.typography.labelLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text("Sent · tap to open", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        }
    }
}

/** Under a reply: copy, thumbs, which model wrote it and why, and (on the latest) a way to ask again on a stronger one. */
@Composable
private fun ReplyRow(m: Message, repo: Repo, latest: Boolean, onError: (String) -> Unit) {
    val scope = rememberCoroutineScope()
    val haptics = rememberHaptics()
    val x = MaterialTheme.extra
    var asking by remember(m.id) { mutableStateOf(false) }
    Column {
        Row(verticalAlignment = Alignment.CenterVertically) {
            CopyRow(m.text)
            if (m.profile.isNotBlank()) {
                for ((v, icon, label) in listOf(Triple("up", Ti.ThumbUp, "Good answer"), Triple("down", Ti.ThumbDown, "Poor answer"))) {
                    val on = m.vote == v
                    IconButton(onClick = { haptics.tick(); scope.launch { runCatching { repo.vote(m.id, if (on) "" else v) }.onFailure { onError(it.message ?: "That could not be saved.") } } }, Modifier.size(40.dp).semantics { contentDescription = label + if (on) ", chosen" else "" }) {
                        Icon(icon, null, Modifier.size(18.dp), tint = if (on) x.brassText else MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
                if (latest) {
                    app.talaria.ui.components.TextButton(enabled = !asking, onClick = {
                        asking = true
                        scope.launch {
                            runCatching { repo.retry(m.id) }.onSuccess { repo.toast("Trying again on $it…") }.onFailure { onError(it.message ?: "That could not be tried again."); asking = false }
                        }
                    }) { Icon(Ti.Refresh, null, Modifier.size(16.dp)); Text("  Try a stronger model") }
                }
            }
        }
        if (m.profile.isNotBlank()) {
            val name = if (m.model.isNotBlank() && m.model != m.profile) "${m.model} · ${m.profile}" else m.profile
            Text("Answered by $name", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
            if (m.why.isNotBlank()) Text(m.why, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.85f))
        }
    }
}

@Composable
private fun CopyRow(text: String) {
    val clip = LocalClipboardManager.current
    val haptics = rememberHaptics()
    var copied by remember { mutableStateOf(false) }
    LaunchedEffect(copied) { if (copied) { delay(1600); copied = false } }
    Row(Modifier.offset(x = (-10).dp)) {
        IconButton(onClick = { clip.setText(AnnotatedString(text)); haptics.tick(); copied = true }, Modifier.size(40.dp)) {
            AnimatedContent(copied, transitionSpec = { (fadeIn(tween(120)) + scaleIn(initialScale = 0.7f)) togetherWith fadeOut(tween(90)) }, label = "copy") { done ->
                Icon(if (done) Ti.Check else Ti.Copy, if (done) "Copied" else "Copy reply", Modifier.size(18.dp), tint = if (done) MaterialTheme.extra.ok else MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }
}

@Composable
private fun ToolRow(m: Message) {
    var open by rememberSaveable(m.id) { mutableStateOf(false) }
    val x = MaterialTheme.extra
    val canOpen = m.result.isNotBlank()
    Column(Modifier.fillMaxWidth().animateContentSize()) {
        Surface(
            shape = RoundedCornerShape(16.dp), color = MaterialTheme.colorScheme.surfaceContainerHigh.copy(alpha = 0.7f),
            modifier = Modifier.clip(RoundedCornerShape(16.dp)).then(if (canOpen) Modifier.clickable { open = !open } else Modifier),
        ) {
            Column {
                Row(Modifier.heightIn(min = 38.dp).padding(horizontal = 12.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    when (m.state) {
                        "running" -> LiveDot(x.info)
                        "error" -> Icon(Ti.Alert, "failed", Modifier.size(15.dp), tint = x.bad)
                        else -> Icon(Ti.Check, "done", Modifier.size(15.dp), tint = x.ok)
                    }
                    Text(humanTool(m.tool), style = MaterialTheme.typography.labelLarge, modifier = Modifier.weight(1f, fill = false), maxLines = 1, overflow = TextOverflow.Ellipsis)
                    if (m.ms > 0 && m.state != "running") Text(seconds(m.ms), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    if (canOpen) Icon(Ti.ChevronDown, if (open) "Hide result" else "Show result", Modifier.size(16.dp).rotate(if (open) 180f else 0f), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                if (open) SelectionContainer {
                    Text(m.result.take(2400), Modifier.padding(start = 12.dp, end = 12.dp, bottom = 12.dp), style = MaterialTheme.typography.bodySmall.copy(fontFamily = Mono, fontWeight = FontWeight.Normal))
                }
            }
        }
    }
}

@Composable
private fun Handoff(m: Message, onOpen: () -> Unit) {
    Panel(Modifier.fillMaxWidth(), tint = MaterialTheme.extra.infoTint.copy(alpha = 0.6f), edge = Color.Transparent) {
        Column(Modifier.padding(16.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Icon(Ti.Hand, null, Modifier.size(18.dp), tint = MaterialTheme.extra.infoInk)
                Text("It needs you in the browser", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.extra.infoInk)
            }
            if (m.text.isNotBlank()) { Gap(6.dp); Text(m.text, style = MaterialTheme.typography.bodyMedium) }
            Gap(12.dp)
            Button(onClick = onOpen) { Icon(Ti.External, null, Modifier.size(18.dp)); Spacer(Modifier.size(8.dp)); Text("Open the browser") }
        }
    }
}

@Composable
private fun Approval(m: Message, repo: Repo, onError: (String) -> Unit) {
    val scope = rememberCoroutineScope()
    val haptics = rememberHaptics()
    var sending by remember { mutableStateOf(false) }
    val pending = m.state == "pending"
    val x = MaterialTheme.extra
    fun decide(choice: String) {
        sending = true
        if (choice == "deny") haptics.reject() else haptics.confirm()
        scope.launch {
            try { repo.decide(m.token, choice) } catch (e: Exception) { onError(e.message ?: "That could not be sent.") }
            sending = false
        }
    }
    Panel(
        Modifier.fillMaxWidth().animateContentSize(),
        tint = if (pending) x.brassSoft else MaterialTheme.colorScheme.surfaceContainerLow,
        edge = if (pending) x.brassLine.copy(alpha = 0.7f) else MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.55f),
    ) {
        Column(Modifier.padding(16.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Icon(Ti.Shield, null, Modifier.size(18.dp), tint = if (pending) x.brassText else MaterialTheme.colorScheme.onSurfaceVariant)
                Text(if (pending) "Needs your answer" else "You were asked", style = MaterialTheme.typography.titleSmall, color = if (pending) x.brassText else MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f))
                if (!pending) when (m.state) {
                    "allowed" -> Pill("Allowed", Tone.Ok); "always" -> Pill("Always allowed", Tone.Ok)
                    "denied" -> Pill("Denied", Tone.Bad); else -> Pill("Expired")
                }
            }
            Gap(8.dp)
            SelectionContainer { Markdown(m.text.ifBlank { humanTool(m.tool) }) }
            if (pending) {
                Gap(14.dp)
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                    Button(onClick = { decide("allow") }, enabled = !sending) { Text("Allow") }
                    if (m.always) SecondaryButton(onClick = { decide("always") }, enabled = !sending) { Text("Always") }
                    SecondaryButton(
                        onClick = { decide("deny") }, enabled = !sending,
                        colors = androidx.compose.material3.ButtonDefaults.filledTonalButtonColors(containerColor = x.badTint, contentColor = x.bad),
                    ) { Text("Deny") }
                }
            }
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun Composer(draft: String, onDraft: (String) -> Unit, onSend: () -> Unit, voice: VoiceController, enabled: Boolean, onAttach: () -> Unit, uploading: Boolean, focusSignal: Int) {
    val focus = remember { androidx.compose.ui.focus.FocusRequester() }
    LaunchedEffect(focusSignal) { if (focusSignal > 0) runCatching { focus.requestFocus() } }
    val canSend = draft.isNotBlank() && enabled
    val haptics = rememberHaptics()
    Box(Modifier.fillMaxWidth().padding(start = 12.dp, end = 12.dp, top = 6.dp, bottom = 10.dp), contentAlignment = Alignment.Center) {
        Surface(
            Modifier.widthIn(max = 720.dp).fillMaxWidth().shadow(10.dp, RoundedCornerShape(30.dp), ambientColor = Color(0x33000000), spotColor = Color(0x33000000)),
            shape = RoundedCornerShape(30.dp), color = MaterialTheme.colorScheme.surfaceContainerLowest,
            border = BorderStroke(0.5.dp, MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.7f)),
        ) {
            Row(Modifier.padding(start = 6.dp, end = 8.dp, top = 6.dp, bottom = 6.dp), verticalAlignment = Alignment.Bottom) {
                IconButton(onClick = onAttach, enabled = enabled && !uploading, modifier = Modifier.size(44.dp)) {
                    if (uploading) androidx.compose.material3.CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp, color = MaterialTheme.extra.brassText)
                    else Icon(Ti.Plus, "Attach a photo or PDF", tint = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                TextField(
                    value = draft, onValueChange = onDraft, modifier = Modifier.weight(1f).focusRequester(focus),
                    placeholder = { Text("Message Talaria", color = MaterialTheme.colorScheme.onSurfaceVariant) },
                    minLines = 1, maxLines = 6,
                    keyboardOptions = KeyboardOptions(capitalization = androidx.compose.ui.text.input.KeyboardCapitalization.Sentences),
                    colors = TextFieldDefaults.colors(
                        focusedContainerColor = Color.Transparent, unfocusedContainerColor = Color.Transparent,
                        focusedIndicatorColor = Color.Transparent, unfocusedIndicatorColor = Color.Transparent, disabledIndicatorColor = Color.Transparent,
                        cursorColor = MaterialTheme.extra.brassText,
                    ),
                    textStyle = MaterialTheme.typography.bodyLarge,
                )
                AnimatedContent(
                    canSend || draft.isNotBlank(), label = "action",
                    transitionSpec = { (fadeIn(tween(140)) + scaleIn(initialScale = 0.6f)) togetherWith (fadeOut(tween(90)) + scaleOut(targetScale = 0.6f)) },
                ) { hasText ->
                    if (hasText) RoundIconButton(Ti.Send, "Send", onClick = onSend, enabled = canSend, container = MaterialTheme.colorScheme.primary, content = MaterialTheme.colorScheme.onPrimary)
                    else Box(
                        Modifier.size(44.dp).clip(CircleShape).background(MaterialTheme.colorScheme.surfaceContainerHigh)
                            .combinedClickable(
                                onClick = { haptics.tick(); voice.toggleConversation() },
                                onLongClick = { haptics.confirm(); voice.startDictation { onDraft((draft + " " + it).trim()) } },
                                onClickLabel = "Talk with Talaria", onLongClickLabel = "Dictate a message",
                            ).semantics { contentDescription = "Talk. Press and hold to dictate." },
                        contentAlignment = Alignment.Center,
                    ) { Icon(Ti.Wave, null, Modifier.size(22.dp), tint = MaterialTheme.colorScheme.onSurface) }
                }
            }
        }
    }
}
