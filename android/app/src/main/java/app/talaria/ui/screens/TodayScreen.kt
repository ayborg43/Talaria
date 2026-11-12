package app.talaria.ui.screens

import app.talaria.ui.theme.Ti

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import app.talaria.ui.components.Button
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import app.talaria.ui.components.TextButton
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
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.data.AgendaItem
import app.talaria.data.FileMeta
import app.talaria.data.MailItem
import app.talaria.data.Repo
import app.talaria.data.TodayDto
import app.talaria.ui.FileOpen
import app.talaria.ui.ago
import app.talaria.ui.clock
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.Panel
import app.talaria.ui.components.Pill
import app.talaria.ui.components.SectionTitle
import app.talaria.ui.components.Tone
import app.talaria.ui.parseTime
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.extra
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

@Composable
fun TodayScreen(repo: Repo, modifier: Modifier, onAsk: (String) -> Unit, onChat: () -> Unit = {}, onAllFiles: () -> Unit = {}) {
    val openFile = app.talaria.ui.LocalOpenFile.current
    val today = rememberPolled(30_000, "today") { repo.api.getJson<TodayDto>("/api/today") }
    val scope = rememberCoroutineScope()
    var refreshing by remember { mutableStateOf(false) }
    val ctx = LocalContext.current
    var fileError by remember { mutableStateOf<String?>(null) }
    val t = today.value
    val date = DateTimeFormatter.ofPattern("EEEE d MMMM", Locale.getDefault()).withZone(ZoneId.systemDefault()).format(java.time.Instant.now())

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Column { Text("Today", style = MaterialTheme.typography.headlineSmall); Text(date, style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        PullToRefreshBox(refreshing, onRefresh = { scope.launch { refreshing = true; today.refresh(); delay(600); refreshing = false } }, modifier = Modifier.fillMaxSize()) {
            LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp)) {
                today.error?.let { e -> item { ErrorNote(e); Gap(8.dp) } }
                fileError?.let { e -> item { ErrorNote(e); Gap(8.dp) } }
                if (t != null && t.pending > 0) item {
                    Panel(Modifier.fillMaxWidth(), tint = MaterialTheme.extra.brassSoft, edge = MaterialTheme.extra.brassLine) {
                        Row(Modifier.padding(start = 16.dp, end = 8.dp, top = 8.dp, bottom = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                            Text("${t.pending} ${if (t.pending == 1) "thing needs" else "things need"} your answer", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.extra.brassText, modifier = Modifier.weight(1f))
                            Button(onClick = onChat, shape = MaterialTheme.shapes.medium) { Text("Open chat") }
                        }
                    }
                }

                item { SectionTitle("Your day") }
                when {
                    t == null -> item { app.talaria.ui.components.SkeletonCard(lines = 3) }
                    !t.agendaOn -> item { Panel(Modifier.fillMaxWidth()) { Text("No calendar is connected. Connect one on your server and your day will show here.", Modifier.padding(16.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } }
                    t.agenda.isEmpty() -> item { Panel(Modifier.fillMaxWidth()) { Text("Nothing on the calendar for the rest of today.", Modifier.padding(16.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } }
                    else -> item { Panel(Modifier.fillMaxWidth()) { Column { t.agenda.forEachIndexed { i, a -> if (i > 0) HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f)); AgendaRow(a) } } } }
                }

                item { SectionTitle("Mail") { if ((t?.mail?.unread ?: 0) > 0) Pill("${t!!.mail!!.unread} unread", Tone.Brass) } }
                val mail = t?.mail
                if (t == null) item { app.talaria.ui.components.SkeletonCard(lines = 2) }
                else if (mail == null) item { Panel(Modifier.fillMaxWidth()) { Text("No mailbox is connected. Connect one on your server and the messages that need you will show here.", Modifier.padding(16.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } }
                else if (mail.items.isEmpty()) item { Panel(Modifier.fillMaxWidth()) { Text("Nothing new in your inbox.", Modifier.padding(16.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } }
                else items(mail.items, key = { it.id }) { m -> MailRow(m) { onAsk("Handle message ${m.id}: read it, work out what it needs from me, and do what you can (draft a reply, add to my calendar). Ask me before anything is sent.") }; Gap(8.dp) }

                val files = t?.files.orEmpty()
                if (files.isNotEmpty()) {
                    item { SectionTitle("Files") { TextButton(onClick = onAllFiles) { Text("See all") } } }
                    item {
                        Panel(Modifier.fillMaxWidth()) {
                            Column {
                                files.forEachIndexed { i, f ->
                                    if (i > 0) HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                                    FileRow(f) { openFile(f) }
                                }
                            }
                        }
                    }
                }
                item { Gap(24.dp) }
            }
        }
    }
}

@Composable
private fun AgendaRow(a: AgendaItem) {
    Row(Modifier.fillMaxWidth().heightIn(min = 60.dp).padding(horizontal = 16.dp, vertical = 10.dp), horizontalArrangement = Arrangement.spacedBy(14.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.width(56.dp)) {
            if (a.allDay) Text("All day", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.extra.brassText)
            else { Text(clock(a.start), style = MaterialTheme.typography.titleSmall); Text(clock(a.end), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
        }
        Column(Modifier.weight(1f)) {
            Text(a.title, style = MaterialTheme.typography.titleSmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
            val sub = listOf(a.where, a.calendar).filter { it.isNotBlank() }.joinToString(" · ")
            if (sub.isNotBlank()) Text(sub, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

@Composable
private fun MailRow(m: MailItem, onHandle: () -> Unit) {
    Panel(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(m.from, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f), maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (m.needs) Pill("Needs you", Tone.Brass)
                Text(ago(m.at), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            Gap(2.dp)
            Text(m.subject, style = MaterialTheme.typography.bodyMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
            if (m.why.isNotBlank()) Text(m.why, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 3, overflow = TextOverflow.Ellipsis)
            if (m.needs) TextButton(onClick = onHandle, contentPadding = PaddingValues(horizontal = 0.dp)) { Text("Have Talaria handle it") }
        }
    }
}

@Composable
private fun FileRow(f: FileMeta, onOpen: () -> Unit) {
    Row(Modifier.fillMaxWidth().clickable(onClick = onOpen).heightIn(min = 56.dp).padding(horizontal = 16.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        Icon(Ti.File, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
        Column(Modifier.weight(1f)) {
            Text(f.name, style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text("${size(f.size)} · ${ago(f.created)}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        Text("Open", style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.tertiary)
    }
}

private fun size(b: Long) = when { b < 1024 -> "$b B"; b < 1 shl 20 -> "${b / 1024} KB"; else -> String.format(Locale.US, "%.1f MB", b / 1048576.0) }
