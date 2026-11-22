package app.talaria.ui.screens

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TimePicker
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.rememberTimePickerState
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
import app.talaria.data.DigestDto
import app.talaria.data.DigestSchedule
import app.talaria.data.Repo
import app.talaria.ui.Digest
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.Markdown
import app.talaria.ui.components.Panel
import app.talaria.ui.components.SecondaryButton
import app.talaria.ui.components.SectionTitle
import app.talaria.ui.components.SkeletonCard
import app.talaria.ui.components.TextButton
import app.talaria.ui.dayAndClock
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.Ti
import app.talaria.ui.theme.extra
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * What Talaria did on its own while you were not looking (the server writes it without asking a model), and when it
 * should arrive in the chat by itself. The scheduled one comes as an ordinary reply, so it uses the reply notification.
 */
@Composable
fun DigestScreen(repo: Repo, onBack: () -> Unit, modifier: Modifier = Modifier) {
    BackHandler(onBack = onBack)
    var span by remember { mutableStateOf(Digest.Span.Day) }
    val base by repo.base.collectAsState()
    val digest = rememberPolled(0, "digest${span.hours}$base") { repo.api.getJson<DigestDto>("/api/digest?hours=${span.hours}") }
    var schedule by remember { mutableStateOf<DigestSchedule?>(null) }
    LaunchedEffect(digest.value) { digest.value?.let { schedule = it.schedule } }
    val scope = rememberCoroutineScope()
    var error by remember { mutableStateOf<String?>(null) }
    var picking by remember { mutableStateOf(false) }
    val d = digest.value
    val s = schedule

    fun save(enabled: Boolean, at: String, weekly: Boolean) = scope.launch {
        try {
            val out = AppJson.decodeFromString<DigestDto>(repo.api.postJson("/api/digest", buildJsonObject { put("enabled", enabled); put("at", at); put("weekly", weekly) }))
            schedule = out.schedule; error = null
        } catch (e: Exception) { error = e.message ?: "That could not be saved." }
    }

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("While you were away", style = MaterialTheme.typography.titleLarge) },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp)) {
            item {
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Digest.Span.entries.forEach { sp -> FilterChip(selected = span == sp, onClick = { span = sp }, label = { Text(sp.label) }) }
                }
                Gap(12.dp)
            }
            digest.error?.let { e -> item { ErrorNote(e); Gap(8.dp) } }
            when {
                d == null -> if (digest.error == null) item { SkeletonCard(lines = 4) }
                d.text == null -> item { EmptyState("Not available here", "This server cannot write a summary of what happened while you were away.") }
                else -> item { Panel(Modifier.fillMaxWidth()) { Markdown(Digest.body(d.text), Modifier.padding(16.dp)) } }
            }

            if (s != null && s.available) {
                item { SectionTitle("Send it to me") }
                item {
                    Panel(Modifier.fillMaxWidth()) {
                        Column {
                            ToggleRow("Send a summary on a schedule", Digest.scheduleLine(s, dayAndClock(s.next)), s.enabled) { save(it, s.at.ifBlank { "08:00" }, s.weekly) }
                            if (s.enabled) {
                                HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                                Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 12.dp), verticalAlignment = Alignment.CenterVertically) {
                                    Text("Time", style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f))
                                    SecondaryButton(onClick = { picking = true }) { Icon(Ti.Sun, null, Modifier.size(18.dp)); Text(s.at.ifBlank { "08:00" }, Modifier.padding(start = 8.dp)) }
                                }
                                Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, bottom = 14.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                    FilterChip(selected = !s.weekly, onClick = { save(true, s.at.ifBlank { "08:00" }, false) }, label = { Text("Every day") })
                                    FilterChip(selected = s.weekly, onClick = { save(true, s.at.ifBlank { "08:00" }, true) }, label = { Text("Every Monday") })
                                }
                            }
                        }
                    }
                }
                item {
                    Gap(8.dp)
                    Text(
                        "It arrives in your chat like any reply, and you get the usual notification for it when the app is closed. Nothing is sent when nothing happened.",
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 4.dp),
                    )
                }
            } else if (s != null) {
                item { SectionTitle("Send it to me") }
                item { Panel(Modifier.fillMaxWidth()) { Text("The schedule is not set up on this server.", Modifier.padding(16.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } }
            }
            error?.let { e -> item { Gap(8.dp); ErrorNote(e) } }
            item { Gap(32.dp) }
        }
    }

    if (picking && s != null) {
        val at = s.at.ifBlank { "08:00" }
        val state = rememberTimePickerState(Digest.hourOf(at), Digest.minuteOf(at), is24Hour = true)
        AlertDialog(
            onDismissRequest = { picking = false }, title = { Text("When should it arrive?") },
            text = { TimePicker(state) },
            confirmButton = { TextButton(onClick = { picking = false; Digest.clock(state.hour, state.minute)?.let { save(true, it, s.weekly) } }) { Text("Save") } },
            dismissButton = { TextButton(onClick = { picking = false }) { Text("Cancel") } },
        )
    }
}
