package app.talaria.ui.screens

import app.talaria.ui.theme.Ti

import androidx.compose.foundation.clickable
import androidx.compose.ui.draw.clip
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.selection.SelectionContainer
import app.talaria.ui.components.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import app.talaria.ui.components.SecondaryButton
import app.talaria.ui.components.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.data.Repo
import app.talaria.data.RunDetail
import app.talaria.ui.ago
import app.talaria.ui.clock
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.LiveDot
import app.talaria.ui.components.Markdown
import app.talaria.ui.components.Panel
import app.talaria.ui.components.Pill
import app.talaria.ui.components.SectionTitle
import app.talaria.ui.components.Tone
import app.talaria.ui.humanTool
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.Mono
import app.talaria.ui.theme.extra
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/** What one run did, step by step, with the means to steer it while it works or carry on from it once it is done. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun RunDetailScreen(repo: Repo, id: String, onBack: () -> Unit, modifier: Modifier = Modifier) {
    val run = rememberPolled(3000, id) { repo.api.getJson<RunDetail>("/api/runs/$id") }
    val scope = rememberCoroutineScope()
    var text by remember { mutableStateOf("") }
    var note by remember { mutableStateOf<String?>(null) }
    var failure by remember { mutableStateOf<String?>(null) }
    var sending by remember { mutableStateOf(false) }
    val r = run.value
    val running = r?.status == "running"

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text(r?.name?.ifBlank { null } ?: "Activity", style = MaterialTheme.typography.titleLarge, maxLines = 1, overflow = TextOverflow.Ellipsis) },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            actions = {
                if (running) SecondaryButton(
                    onClick = { scope.launch { try { repo.api.post("/api/runs/${r!!.id}/stop"); run.refresh() } catch (e: Exception) { failure = e.message } } },
                    shape = MaterialTheme.shapes.medium, modifier = Modifier.padding(end = 8.dp),
                    colors = ButtonDefaults.outlinedButtonColors(contentColor = MaterialTheme.extra.bad),
                ) { Icon(Ti.Stop, null, Modifier.size(18.dp)); Text("Stop", Modifier.padding(start = 4.dp)) }
            },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        if (r == null) {
            if (run.error != null) Box(Modifier.weight(1f).padding(16.dp)) { ErrorNote(run.error!!) } else Box(Modifier.weight(1f))
        } else LazyColumn(Modifier.weight(1f).fillMaxWidth(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp)) {
            item {
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                    when (r.status) { "running" -> { LiveDot(); Pill("Working", Tone.Ok) }; "done" -> Pill("Done"); "failed" -> Pill("Failed", Tone.Bad); else -> Pill("Stopped") }
                    if (r.model.isNotBlank()) Pill(r.model, Tone.Info)
                    Text(if (r.ended.isNotBlank()) "Ended ${ago(r.ended)}" else "Started ${ago(r.started)}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
            if (r.prompt.isNotBlank()) {
                item { SectionTitle("It was asked") }
                item { Panel(Modifier.fillMaxWidth()) { SelectionContainer { Text(r.prompt, Modifier.padding(14.dp), style = MaterialTheme.typography.bodyMedium) } } }
            }
            item { SectionTitle("Steps") { Text("${r.steps.size + r.skipped}", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } }
            if (r.steps.isEmpty()) item { EmptyState("No steps yet", if (running) "It has not used a tool yet." else "It finished without using a tool.") }
            else item {
                Panel(Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(vertical = 6.dp)) {
                        r.steps.forEachIndexed { i, s ->
                            if (r.skipped > 0 && i == 5) Text("… ${r.skipped} steps in between are not shown", Modifier.padding(horizontal = 16.dp, vertical = 6.dp), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 6.dp), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                                Text(clock(s.at), style = MaterialTheme.typography.labelSmall.copy(fontFamily = Mono), color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 2.dp))
                                Column(Modifier.weight(1f)) {
                                    Text(humanTool(s.tool), style = MaterialTheme.typography.labelLarge)
                                    if (s.detail.isNotBlank()) Text(s.detail, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                }
                            }
                        }
                    }
                }
            }
            if (r.files.isNotEmpty()) {
                item { SectionTitle("Files it made") }
                items(r.files, key = { it.id }) { f ->
                    val openFile = app.talaria.ui.LocalOpenFile.current
                    Panel(Modifier.fillMaxWidth().padding(bottom = 6.dp).clip(MaterialTheme.shapes.large).clickable { openFile(app.talaria.data.FileMeta(id = f.id, name = f.name, mime = f.mime)) }) { Row(Modifier.padding(14.dp), verticalAlignment = Alignment.CenterVertically) { Text(f.name, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f)); Text(f.mime.substringAfter('/'), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant) } }
                }
            }
            if (r.result.isNotBlank()) {
                item { SectionTitle("What it reported") }
                item { Panel(Modifier.fillMaxWidth()) { Box(Modifier.padding(14.dp)) { SelectionContainer { Markdown(r.result) } } } }
            }
            if (r.steers.isNotEmpty()) {
                item { SectionTitle("What you told it") }
                items(r.steers) { s ->
                    Panel(Modifier.fillMaxWidth().padding(bottom = 6.dp), tint = MaterialTheme.extra.brassSoft, edge = MaterialTheme.extra.brassLine) {
                        Column(Modifier.padding(12.dp)) { Text(s.text, style = MaterialTheme.typography.bodyMedium); Text(if (s.applied) "Read by the task" else "Waiting for its next step", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.extra.brassText) }
                    }
                }
            }
            failure?.let { item { Gap(8.dp); ErrorNote(it) } }
            note?.let { item { Gap(8.dp); Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.extra.ok) } }
            item { Gap(16.dp) }
        }

        if (r != null) Column(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp)) {
            Row(verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(
                    text, { text = it }, modifier = Modifier.weight(1f), shape = MaterialTheme.shapes.large, maxLines = 4,
                    placeholder = { Text(if (running) "Tell it something while it works" else "Carry on from this", style = MaterialTheme.typography.bodyMedium) },
                )
                Button(
                    onClick = {
                        sending = true
                        scope.launch {
                            try {
                                val raw = repo.api.postJson("/api/runs/${r.id}/steer", buildJsonObject { put("text", text.trim()) })
                                note = if (raw.contains("follow_up")) "Started a follow-up. Its result will come to your chat." else "Sent. It reads this at its next step."
                                failure = null; text = ""; run.refresh()
                            } catch (e: Exception) { failure = e.message }
                            sending = false
                        }
                    },
                    enabled = text.isNotBlank() && !sending, shape = MaterialTheme.shapes.large, modifier = Modifier.heightIn(min = 56.dp),
                ) { Icon(Ti.Send, "Send") }
            }
            Text(
                if (running) "It cannot send, post, pay or delete on its own, whatever you write here." else "This starts a new background task that knows what this one did.",
                style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 4.dp, top = 4.dp),
            )
        }
    }
}
