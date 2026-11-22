package app.talaria.ui.screens

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import app.talaria.data.AppJson
import app.talaria.data.Repo
import app.talaria.data.RetentionDto
import app.talaria.ui.Retention
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.Panel
import app.talaria.ui.components.SecondaryButton
import app.talaria.ui.components.SectionTitle
import app.talaria.ui.components.SkeletonCard
import app.talaria.ui.components.Button
import app.talaria.ui.components.TextButton
import app.talaria.ui.dayAndClock
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.Ti
import app.talaria.ui.theme.extra
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * How long old conversations are kept. Nothing is removed unless a number is chosen here: after that many days of
 * nobody touching it a conversation is archived, and one archived this way is deleted for good a month later.
 */
@Composable
fun RetentionScreen(repo: Repo, onBack: () -> Unit, modifier: Modifier = Modifier) {
    BackHandler(onBack = onBack)
    val base by repo.base.collectAsState()
    val fetched = rememberPolled(0, "retention$base") { repo.api.getJson<RetentionDto>("/api/retention") }
    var view by remember { mutableStateOf<RetentionDto?>(null) }
    LaunchedEffect(fetched.value) { fetched.value?.let { view = it } }
    val scope = rememberCoroutineScope()
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    var confirm by remember { mutableStateOf(false) }
    var note by remember { mutableStateOf<String?>(null) }
    val v = view

    /** Sends a change (or a sweep) and takes the server's own view of the result back. */
    fun send(body: kotlinx.serialization.json.JsonObject, done: String? = null) {
        busy = true; error = null; note = null
        scope.launch {
            try { view = AppJson.decodeFromString(repo.api.postJson("/api/retention", body)); note = done }
            catch (e: Exception) { error = e.message ?: "That could not be saved." }
            busy = false
        }
    }

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("Old conversations", style = MaterialTheme.typography.titleLarge) },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp)) {
            fetched.error?.let { e -> if (v == null) item { ErrorNote(e); Gap(8.dp) } }
            error?.let { e -> item { ErrorNote(e); Gap(8.dp) } }
            when {
                v == null -> if (fetched.error == null) item { SkeletonCard(lines = 3) }
                !v.available -> item { EmptyState("Not available here", "This server does not keep conversations in a way it can tidy.") }
                else -> {
                    item {
                        Text(
                            "Nothing is ever removed unless you choose a time here. It is checked a minute after the server starts and every few hours after.",
                            style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                        Gap(12.dp)
                    }
                    item {
                        Panel(Modifier.fillMaxWidth()) {
                            Column {
                                ChoiceRow(
                                    "Archive conversations nobody has touched for", Retention.label(Retention.archiveChoices, v.keep_days),
                                    Retention.offered(Retention.archiveChoices, v.keep_days), v.keep_days, enabled = !busy,
                                ) { send(buildJsonObject { put("keep_days", it) }) }
                                if (v.can_prune_history) {
                                    HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                                    ChoiceRow(
                                        "Forget the searchable history older than", Retention.label(Retention.historyChoices, v.history_keep_days),
                                        Retention.offered(Retention.historyChoices, v.history_keep_days), v.history_keep_days, enabled = !busy,
                                    ) { send(buildJsonObject { put("history_keep_days", it) }) }
                                }
                            }
                        }
                    }
                    item {
                        Gap(8.dp)
                        Text(Retention.archiveNote(v.keep_days, v.grace_days), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 4.dp))
                    }
                    Retention.historyWarning(v.history_keep_days)?.takeIf { v.can_prune_history }?.let { w ->
                        item {
                            Gap(10.dp)
                            Panel(Modifier.fillMaxWidth(), tint = MaterialTheme.extra.brassSoft, edge = MaterialTheme.extra.brassLine) {
                                Text(w, Modifier.padding(14.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.extra.brassText)
                            }
                        }
                    }
                    item { SectionTitle("Now") }
                    item {
                        Panel(Modifier.fillMaxWidth()) {
                            Column(Modifier.padding(16.dp)) {
                                if (v.keep_days > 0) Text(Retention.dueLine(v), style = MaterialTheme.typography.titleSmall)
                                else Text("Nothing is due: no time is set.", style = MaterialTheme.typography.titleSmall)
                                Retention.lastLine(v, dayAndClock(v.last_at))?.let { Gap(4.dp); Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                                Gap(12.dp)
                                SecondaryButton(onClick = { confirm = true }, enabled = Retention.canTidy(v) && !busy, shape = MaterialTheme.shapes.medium) { Text("Tidy now") }
                                note?.let { Gap(8.dp); Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.extra.ok) }
                            }
                        }
                    }
                }
            }
            item { Gap(32.dp) }
        }
    }

    if (confirm && v != null) AlertDialog(
        onDismissRequest = { confirm = false }, title = { Text("Tidy now?") },
        text = { Text(Retention.confirmText(v)) },
        confirmButton = { TextButton(onClick = { confirm = false; send(buildJsonObject { put("sweep", true) }, "Tidied.") }) { Text("Tidy now", color = MaterialTheme.extra.bad) } },
        dismissButton = { TextButton(onClick = { confirm = false }) { Text("Cancel") } },
    )
}

/** A heading with the current choice as a button that opens the list. */
@Composable
private fun ChoiceRow(title: String, current: String, choices: List<Retention.Choice>, days: Int, enabled: Boolean, onPick: (Int) -> Unit) {
    var open by remember { mutableStateOf(false) }
    Row(Modifier.fillMaxWidth().heightIn(min = 64.dp).padding(start = 16.dp, end = 12.dp, top = 10.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        Text(title, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
        Box {
            SecondaryButton(onClick = { open = true }, enabled = enabled, shape = MaterialTheme.shapes.medium) { Text(current) }
            DropdownMenu(open, { open = false }) {
                choices.forEach { c ->
                    DropdownMenuItem(text = { Text(c.label + if (c.days == days) "  ✓" else "") }, onClick = { open = false; if (c.days != days) onPick(c.days) })
                }
            }
        }
    }
}
