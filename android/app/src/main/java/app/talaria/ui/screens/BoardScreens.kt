package app.talaria.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.clickable
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.data.BoardDto
import app.talaria.data.CardDto
import app.talaria.data.Repo
import app.talaria.ui.components.Button
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.Markdown
import app.talaria.ui.components.OutlinedTextField
import app.talaria.ui.components.Panel
import app.talaria.ui.components.Pill
import app.talaria.ui.components.SecondaryButton
import app.talaria.ui.components.SectionTitle
import app.talaria.ui.components.Switch
import app.talaria.ui.components.TextButton
import app.talaria.ui.components.Tone
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.Ti
import app.talaria.ui.theme.extra
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/** Most useful first on a phone: what is working, what waits for a look, then what is lined up. */
private val boardOrder = listOf("running", "review", "ready", "backlog", "done")
private val columnNames = mapOf("backlog" to "Backlog", "ready" to "Ready", "running" to "Running", "review" to "Review", "done" to "Done")
private val columnNotes = mapOf(
    "backlog" to "Ideas and to-dos. Nothing here does anything yet.",
    "ready" to "Starts as a background task when it has an agent and there is room (three at a time).",
    "running" to "Being worked on now.",
    "review" to "Finished. Read what it reported, then mark it done or run it again.",
    "done" to "Looked at and closed.",
)

/** The name a card's agent is shown by. */
internal fun agentLabel(agent: String, main: String) = when {
    agent.isBlank() -> "No agent yet"
    agent == main -> "Talaria"
    else -> agent
}

/**
 * The board: work lined up for the agent, in columns. A card in Ready with an agent starts by itself as a background
 * task, sits in Running while it works, and lands in Review with what it reported.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun BoardTab(repo: Repo, onWatch: (String) -> Unit, onOpenCard: (CardDto) -> Unit, onNewCard: () -> Unit) {
    val board = rememberPolled(4000, "board") { repo.api.getJson<BoardDto>("/api/board") }
    val scope = rememberCoroutineScope()
    var failure by remember { mutableStateOf<String?>(null) }
    var note by remember { mutableStateOf<String?>(null) }
    var refreshing by remember { mutableStateOf(false) }

    fun act(body: JsonObject, done: String? = null) = scope.launch {
        try { repo.api.postJson("/api/board", body); note = done; failure = null; board.refresh() } catch (e: Exception) { failure = e.message }
    }
    fun move(c: CardDto, column: String, done: String? = null) = act(buildJsonObject { put("action", "move"); put("id", c.id); put("column", column) }, done)

    val b = board.value
    PullToRefreshBox(refreshing, onRefresh = { scope.launch { refreshing = true; board.refresh(); kotlinx.coroutines.delay(500); refreshing = false } }, modifier = Modifier.fillMaxSize()) {
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp)) {
            item {
                Gap(8.dp)
                if (b?.available != false) SecondaryButton(onClick = onNewCard, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp)) {
                    Icon(Ti.Plus, null, Modifier.size(18.dp)); Text("New card", Modifier.padding(start = 8.dp))
                }
            }
            note?.let { n -> item { Panel(Modifier.fillMaxWidth().padding(top = 8.dp), tint = MaterialTheme.extra.okTint, edge = MaterialTheme.extra.okLine) { Text(n, Modifier.padding(14.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.extra.ok) } } }
            failure?.let { f -> item { Gap(8.dp); ErrorNote(f) } }
            when {
                b == null && board.loading -> item { Gap(8.dp); app.talaria.ui.components.SkeletonCard(lines = 3) }
                b != null && !b.available -> item { EmptyState("No board here", "This server does not have the board set up.") }
                b != null -> {
                    item {
                        Text(
                            "A card in Ready with an agent starts by itself, as a background task. Like any task it cannot ask you anything, so what needs your approval is refused unless you allow it on the card.",
                            Modifier.padding(top = 10.dp, start = 4.dp, end = 4.dp), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                    for (col in boardOrder.filter { it in b.columns }) {
                        val cards = b.cards.filter { it.column == col }
                        item(key = "h-$col") { SectionTitle(columnNames[col] ?: col) { if (cards.isNotEmpty()) Pill("${cards.size}", if (col == "running") Tone.Ok else Tone.Neutral) } }
                        if (cards.isEmpty()) item(key = "e-$col") {
                            Text(columnNotes[col].orEmpty(), Modifier.padding(horizontal = 4.dp, vertical = 2.dp), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        } else items(cards, key = { it.id }) { c ->
                            CardRow(
                                c, b.main, onOpen = { onOpenCard(c) },
                                onStart = { if (c.agent.isBlank()) onOpenCard(c) else move(c, "ready", "Started: it runs as a background task.") },
                                onBack = { move(c, "backlog") },
                                onStop = { act(buildJsonObject { put("action", "stop"); put("id", c.id) }, "Stopping it.") },
                                onWatch = { onWatch(c.job) },
                                onDone = { move(c, "done") },
                                onAgain = { move(c, "ready", "Started again.") },
                            )
                            Gap(8.dp)
                        }
                    }
                }
            }
            board.error?.let { item { Gap(8.dp); ErrorNote(it) } }
            item { Gap(24.dp) }
        }
    }
}

@Composable
private fun CardRow(
    c: CardDto, main: String, onOpen: () -> Unit, onStart: () -> Unit, onBack: () -> Unit, onStop: () -> Unit, onWatch: () -> Unit, onDone: () -> Unit, onAgain: () -> Unit,
) {
    val running = c.column == "running"
    Panel(
        Modifier.fillMaxWidth().clip(MaterialTheme.shapes.large).clickable(onClick = onOpen),
        tint = if (running) MaterialTheme.extra.brassSoft else MaterialTheme.colorScheme.surfaceContainerLow,
        edge = if (running) MaterialTheme.extra.brassLine else if (c.status == "failed") MaterialTheme.extra.badLine else MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.55f),
    ) {
        Column(Modifier.padding(horizontal = 16.dp, vertical = 12.dp)) {
            Text(c.title, style = MaterialTheme.typography.titleSmall, maxLines = 3, overflow = TextOverflow.Ellipsis)
            Gap(6.dp)
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                Pill(agentLabel(c.agent, main), if (c.agent.isBlank()) Tone.Neutral else Tone.Info)
                if (c.allow_risky) Pill("may act freely", Tone.Brass)
                when (c.status) { "failed" -> Pill("failed", Tone.Bad); "cancelled" -> Pill("stopped"); "lost" -> Pill("lost") }
            }
            val line = when {
                c.column == "ready" && c.agent.isBlank() -> "Waiting for you to pick who does it."
                running -> "Working…"
                (c.column == "review" || c.column == "done") && c.result.isNotBlank() -> c.result.replace(Regex("\\s+"), " ").trim()
                else -> ""
            }
            if (line.isNotBlank()) { Gap(6.dp); Text(line, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 3, overflow = TextOverflow.Ellipsis) }
            Gap(8.dp)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                when (c.column) {
                    "backlog" -> Button(onClick = onStart, shape = MaterialTheme.shapes.medium) { Text("Start") }
                    "ready" -> SecondaryButton(onClick = onBack, shape = MaterialTheme.shapes.medium) { Text("Back to backlog") }
                    "running" -> {
                        SecondaryButton(onClick = onStop, shape = MaterialTheme.shapes.medium) { Text("Stop") }
                        if (c.job.isNotBlank()) TextButton(onClick = onWatch) { Text("Watch") }
                    }
                    "review" -> {
                        Button(onClick = onDone, shape = MaterialTheme.shapes.medium) { Icon(Ti.Check, null, Modifier.size(16.dp)); Text("Done", Modifier.padding(start = 6.dp)) }
                        SecondaryButton(onClick = onAgain, shape = MaterialTheme.shapes.medium) { Text("Run again") }
                    }
                    "done" -> SecondaryButton(onClick = onAgain, shape = MaterialTheme.shapes.medium) { Text("Run again") }
                }
            }
        }
    }
}

/** Make or change a card: what it is, who does it, and whether its task may use tools that normally ask first. */
@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
fun CardFormScreen(repo: Repo, card: CardDto?, onDone: () -> Unit, onWatch: (String) -> Unit, modifier: Modifier = Modifier) {
    val meta = rememberPolled(0, "board-meta") { repo.api.getJson<BoardDto>("/api/board") }
    var title by remember { mutableStateOf(card?.title.orEmpty()) }
    var body by remember { mutableStateOf(card?.body.orEmpty()) }
    var agent by remember { mutableStateOf(card?.agent.orEmpty()) }
    var risky by remember { mutableStateOf(card?.allow_risky ?: false) }
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    var confirmRemove by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val running = card?.column == "running"
    val main = meta.value?.main ?: "main"
    val saved = meta.value?.agents.orEmpty()

    fun go(block: suspend () -> Unit) {
        busy = true; error = null
        scope.launch { try { block(); onDone() } catch (e: Exception) { error = e.message }; busy = false }
    }
    suspend fun post(vararg pairs: Pair<String, Any>) = repo.api.postJson("/api/board", buildJsonObject {
        pairs.forEach { (k, v) -> when (v) { is Boolean -> put(k, v); else -> put(k, v.toString()) } }
    })
    suspend fun saveEdit() { post("action" to "edit", "id" to card!!.id, "title" to title.trim(), "body" to body.trim(), "agent" to agent, "allow_risky" to risky) }

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text(if (card == null) "New card" else card.title, style = MaterialTheme.typography.titleLarge, maxLines = 1, overflow = TextOverflow.Ellipsis) },
            navigationIcon = { IconButton(onClick = onDone) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 8.dp)) {
            if (card != null) Pill((columnNames[card.column] ?: card.column) + if (card.status.isNotBlank()) " · ${card.status}" else "", if (running) Tone.Ok else Tone.Neutral)
            if (card != null) Gap(12.dp)
            Text("The task has no memory of any chat, so say everything it needs: names, places, what a good answer looks like.", style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Gap(16.dp)
            OutlinedTextField(title, { title = it.take(120) }, label = { Text("Title") }, singleLine = true, enabled = !running, modifier = Modifier.fillMaxWidth(), shape = MaterialTheme.shapes.medium)
            Gap(12.dp)
            OutlinedTextField(
                body, { body = it.take(4000) }, label = { Text("Detail") }, supportingText = { Text("${body.length} / 4000") },
                minLines = 5, maxLines = 12, enabled = !running, modifier = Modifier.fillMaxWidth(), shape = MaterialTheme.shapes.medium,
            )
            Gap(16.dp)
            Text("Who does it", style = MaterialTheme.typography.labelLarge)
            Gap(8.dp)
            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                FilterChip(selected = agent.isBlank(), onClick = { if (!running) agent = "" }, label = { Text("Pick later") }, enabled = !running)
                FilterChip(selected = agent == main, onClick = { if (!running) agent = main }, label = { Text("Talaria") }, enabled = !running)
                saved.forEach { n -> FilterChip(selected = agent == n, onClick = { if (!running) agent = n }, label = { Text(n) }, enabled = !running) }
            }
            if (agent.isBlank()) { Gap(6.dp); Text("Pick who should do this before you start it.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
            Gap(16.dp)
            Row(Modifier.fillMaxWidth().heightIn(min = 56.dp).clip(MaterialTheme.shapes.medium).clickable(enabled = !running) { risky = !risky }, verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text("Let it act freely", style = MaterialTheme.typography.titleSmall)
                    Text("Its task may use tools that normally ask you first (send, change, delete). It cannot ask you while it works.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                Switch(risky, null, modifier = Modifier.padding(start = 12.dp))
            }
            if (running) { Gap(12.dp); Text("It is running, so this is read-only. Stop it to change the card.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
            error?.let { Gap(12.dp); ErrorNote(it) }
            Gap(20.dp)

            if (card == null) {
                Button(
                    onClick = { go { post("action" to "add", "title" to title.trim(), "body" to body.trim(), "agent" to agent, "allow_risky" to risky, "column" to "backlog") } },
                    enabled = title.isNotBlank() && !busy, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp),
                ) { Text("Add to backlog") }
                Gap(8.dp)
                SecondaryButton(
                    onClick = { go { post("action" to "add", "title" to title.trim(), "body" to body.trim(), "agent" to agent, "allow_risky" to risky, "column" to "ready") } },
                    enabled = title.isNotBlank() && agent.isNotBlank() && !busy, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp),
                ) { Text("Add and start") }
            } else if (running) {
                Button(onClick = { go { post("action" to "stop", "id" to card.id) } }, enabled = !busy, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp)) { Text("Stop it") }
                if (card.job.isNotBlank()) { Gap(8.dp); SecondaryButton(onClick = { onWatch(card.job) }, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp)) { Text("Watch it work") } }
            } else {
                Button(onClick = { go { saveEdit() } }, enabled = title.isNotBlank() && !busy, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp)) { Text("Save") }
                if (card.column == "backlog" || card.column == "review" || card.column == "done") {
                    Gap(8.dp)
                    SecondaryButton(
                        onClick = { go { saveEdit(); post("action" to "move", "id" to card.id, "column" to "ready") } },
                        enabled = title.isNotBlank() && agent.isNotBlank() && !busy, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp),
                    ) { Text(if (card.column == "backlog") "Save and start" else "Save and run again") }
                }
                if (card.column == "ready") {
                    Gap(8.dp)
                    SecondaryButton(onClick = { go { post("action" to "move", "id" to card.id, "column" to "backlog") } }, enabled = !busy, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp)) { Text("Back to backlog") }
                }
                if (card.job.isNotBlank() && (card.column == "review" || card.column == "done")) {
                    Gap(8.dp)
                    SecondaryButton(onClick = { onWatch(card.job) }, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp)) { Text("See what it did") }
                }
                Gap(8.dp)
                TextButton(onClick = { confirmRemove = true }, modifier = Modifier.fillMaxWidth()) { Text("Remove this card") }
            }

            if (card != null && (card.column == "review" || card.column == "done") && card.result.isNotBlank()) {
                Gap(20.dp)
                SectionTitle(if (card.status == "failed") "What went wrong" else "What it reported")
                Panel(Modifier.fillMaxWidth()) { Column(Modifier.padding(14.dp)) { Markdown(card.result) } }
            }
            Gap(24.dp)
        }
    }
    if (confirmRemove && card != null) AlertDialog(
        onDismissRequest = { confirmRemove = false },
        title = { Text("Remove this card?") },
        text = { Text("“${card.title}” is taken off the board for good. What it already did stays done.") },
        confirmButton = { TextButton(onClick = { confirmRemove = false; go { post("action" to "remove", "id" to card.id) } }) { Text("Remove") } },
        dismissButton = { TextButton(onClick = { confirmRemove = false }) { Text("Keep") } },
    )
}
