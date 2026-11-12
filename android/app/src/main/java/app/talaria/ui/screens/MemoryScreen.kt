package app.talaria.ui.screens

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import app.talaria.data.AppJson
import app.talaria.data.ProfileDto
import app.talaria.data.Repo
import app.talaria.ui.components.Button
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.OutlinedTextField
import app.talaria.ui.components.Panel
import app.talaria.ui.components.SectionTitle
import app.talaria.ui.components.SecondaryButton
import app.talaria.ui.components.SkeletonCard
import app.talaria.ui.components.TextButton
import app.talaria.ui.components.rememberHaptics
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.Ti
import app.talaria.ui.theme.extra
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

private data class NoteEdit(val target: String, val index: Int?, val old: String)

/** What Talaria knows about you: your name and the tone you like, the notes about you, and the notes it keeps for itself. */
@Composable
fun MemoryScreen(repo: Repo, onBack: () -> Unit, modifier: Modifier = Modifier) {
    BackHandler(onBack = onBack)
    val loaded = rememberPolled(0, "profile") { repo.api.getJson<ProfileDto>("/api/profile") }
    var data by remember { mutableStateOf<ProfileDto?>(null) }
    LaunchedEffect(loaded.value) { if (data == null) data = loaded.value }
    val scope = rememberCoroutineScope()
    val haptics = rememberHaptics()
    var error by remember { mutableStateOf<String?>(null) }
    var name by remember { mutableStateOf("") }
    var tone by remember { mutableStateOf("") }
    var avatar by remember { mutableStateOf("") }
    var filled by remember { mutableStateOf(false) }
    var editing by remember { mutableStateOf<NoteEdit?>(null) }
    var removing by remember { mutableStateOf<NoteEdit?>(null) }
    val d = data
    if (d != null && !filled) { filled = true; name = d.profile.name; tone = d.profile.tone; avatar = d.profile.avatar }

    fun post(body: JsonObject, done: String? = null) = scope.launch {
        try { data = AppJson.decodeFromString(repo.api.post("/api/profile", body.toString())); error = null; if (done != null) { haptics.confirm(); repo.toast(done) } } catch (e: Exception) { error = e.message }
    }

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("You and Talaria", style = MaterialTheme.typography.titleLarge) },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp)) {
            error?.let { e -> item { ErrorNote(e); Gap(8.dp) } }
            loaded.error?.let { e -> if (d == null) item { ErrorNote(e) } }
            if (d == null) { if (loaded.error == null) item { SkeletonCard(lines = 3) } }
            else {
                item { SectionTitle("How it should address you") }
                item {
                    Panel(Modifier.fillMaxWidth()) {
                        Column(Modifier.padding(16.dp)) {
                            OutlinedTextField(name, { name = it.take(60) }, label = { Text("Your name") }, singleLine = true, modifier = Modifier.fillMaxWidth())
                            Gap(12.dp)
                            OutlinedTextField(tone, { tone = it.take(200) }, label = { Text("How replies should sound") }, supportingText = { Text("In your own words, for example: short and direct, no small talk. ${tone.length} / 200") }, minLines = 2, maxLines = 4, modifier = Modifier.fillMaxWidth())
                            Gap(12.dp)
                            OutlinedTextField(avatar, { avatar = it.take(4) }, label = { Text("Icon (an emoji or initials)") }, singleLine = true, modifier = Modifier.fillMaxWidth())
                            Gap(16.dp)
                            Button(onClick = { post(buildJsonObject { put("profile", buildJsonObject { put("name", name.trim()); put("tone", tone.trim()); put("avatar", avatar.trim()); put("theme", d.profile.theme) }) }, "Saved") }) { Text("Save") }
                        }
                    }
                }
                noteSection("About you", "Things it should remember about you. It reads these before every reply.", "user", d.user.entries, d.user.used, d.user.limit, { editing = it }, { removing = it })
                noteSection("Its own notes", "What Talaria has decided to remember while working with you. You can correct or remove anything.", "memory", d.memory.entries, d.memory.used, d.memory.limit, { editing = it }, { removing = it })
                item { Gap(24.dp) }
            }
        }
    }

    editing?.let { e ->
        var text by remember(e) { mutableStateOf(androidx.compose.ui.text.input.TextFieldValue(e.old, androidx.compose.ui.text.TextRange(e.old.length))) }
        AlertDialog(
            onDismissRequest = { editing = null }, title = { Text(if (e.index == null) "Add a note" else "Edit the note") },
            text = { androidx.compose.material3.OutlinedTextField(text, { text = it }, minLines = 3, maxLines = 8, modifier = Modifier.fillMaxWidth(), colors = app.talaria.ui.components.fieldColors(), shape = MaterialTheme.shapes.medium) },
            confirmButton = {
                TextButton(enabled = text.text.isNotBlank(), onClick = {
                    editing = null
                    post(buildJsonObject { put("note", buildJsonObject { put("target", e.target); if (e.index != null) { put("index", e.index); put("old", e.old) }; put("text", text.text.trim()) }) }, if (e.index == null) "Note added" else "Note saved")
                }) { Text("Save") }
            },
            dismissButton = { TextButton(onClick = { editing = null }) { Text("Cancel") } },
        )
    }
    removing?.let { e ->
        AlertDialog(
            onDismissRequest = { removing = null }, title = { Text("Forget this?") }, text = { Text(e.old) },
            confirmButton = { TextButton(onClick = { removing = null; post(buildJsonObject { put("note", buildJsonObject { put("target", e.target); put("index", e.index!!); put("old", e.old); put("text", "") }) }, "Forgotten") }) { Text("Forget it", color = MaterialTheme.extra.bad) } },
            dismissButton = { TextButton(onClick = { removing = null }) { Text("Keep it") } },
        )
    }
}

private fun androidx.compose.foundation.lazy.LazyListScope.noteSection(
    title: String, hint: String, target: String, entries: List<String>, used: Int, limit: Int, onEdit: (NoteEdit) -> Unit, onRemove: (NoteEdit) -> Unit,
) {
    item { SectionTitle(title) { if (limit > 0) Text("$used / $limit", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } }
    item { Text(hint, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 4.dp, bottom = 8.dp)) }
    item {
        Panel(Modifier.fillMaxWidth()) {
            Column {
                if (entries.isEmpty()) Text("Nothing here yet.", Modifier.padding(16.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                entries.forEachIndexed { i, text ->
                    if (i > 0) HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                    Row(Modifier.fillMaxWidth().padding(start = 16.dp, top = 6.dp, bottom = 6.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                        Text(text, Modifier.weight(1f).padding(vertical = 6.dp), style = MaterialTheme.typography.bodyMedium)
                        IconButton(onClick = { onEdit(NoteEdit(target, i, text)) }) { Icon(Ti.Edit, "Edit note", Modifier.size(20.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant) }
                        IconButton(onClick = { onRemove(NoteEdit(target, i, text)) }) { Icon(Ti.Trash, "Forget note", Modifier.size(20.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant) }
                    }
                }
            }
        }
    }
    item { Gap(8.dp); SecondaryButton(onClick = { onEdit(NoteEdit(target, null, "")) }) { Icon(Ti.Plus, null, Modifier.size(18.dp)); Text("Add a note", Modifier.padding(start = 8.dp)) } }
}
