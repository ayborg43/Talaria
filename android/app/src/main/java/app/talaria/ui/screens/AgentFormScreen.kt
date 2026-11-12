package app.talaria.ui.screens

import app.talaria.ui.theme.Ti

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import app.talaria.ui.components.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import app.talaria.ui.components.OutlinedTextField
import app.talaria.ui.components.Switch
import androidx.compose.material3.Text
import app.talaria.ui.components.TextButton
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
import androidx.compose.ui.draw.clip
import androidx.compose.ui.unit.dp
import app.talaria.data.PersonaDto
import app.talaria.data.PersonasDto
import app.talaria.data.Repo
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.extra
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/** Make or change a named agent: what it is for, how it behaves, and whether it may only look. */
@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
fun AgentFormScreen(repo: Repo, agent: PersonaDto?, onDone: () -> Unit, modifier: Modifier = Modifier) {
    val meta = rememberPolled(0, "persona-meta") { repo.api.getJson<PersonasDto>("/api/personas") }
    var name by remember { mutableStateOf(agent?.name.orEmpty()) }
    var icon by remember { mutableStateOf(agent?.icon.orEmpty()) }
    var color by remember { mutableStateOf(agent?.color?.ifBlank { "brass" } ?: "brass") }
    var instructions by remember { mutableStateOf(agent?.instructions.orEmpty()) }
    var readOnly by remember { mutableStateOf(agent?.read_only ?: false) }
    var profile by remember { mutableStateOf(agent?.profile.orEmpty()) }
    var error by remember { mutableStateOf<String?>(null) }
    var confirmRemove by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text(if (agent == null) "New agent" else agent.name, style = MaterialTheme.typography.titleLarge) },
            navigationIcon = { IconButton(onClick = onDone) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 8.dp)) {
            Text("A named agent is a persona: its own instructions, kept as its own conversation. It shares your memory, goals and rules.", style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Gap(16.dp)
            OutlinedTextField(name, { name = it.take(30) }, label = { Text("Name") }, singleLine = true, modifier = Modifier.fillMaxWidth(), shape = MaterialTheme.shapes.medium)
            Gap(12.dp)
            OutlinedTextField(icon, { icon = it.take(8) }, label = { Text("Icon (optional)") }, supportingText = { Text("One emoji or a couple of letters") }, singleLine = true, modifier = Modifier.fillMaxWidth(), shape = MaterialTheme.shapes.medium)
            Gap(12.dp)
            OutlinedTextField(
                instructions, { instructions = it.take(1500) }, label = { Text("What it is for and how it should behave") },
                supportingText = { Text("${instructions.length} / 1500") }, minLines = 5, maxLines = 10, modifier = Modifier.fillMaxWidth(), shape = MaterialTheme.shapes.medium,
            )
            Gap(12.dp)
            Text("Colour", style = MaterialTheme.typography.labelLarge)
            Gap(8.dp)
            FlowRow(horizontalArrangement = Arrangement.spacedBy(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                val colors = meta.value?.colors?.ifEmpty { null } ?: listOf("brass", "info", "ok", "coral", "plum", "slate")
                colors.forEach { c ->
                    val col = agentColor(c, MaterialTheme.extra, MaterialTheme.colorScheme)
                    Box(
                        Modifier.size(48.dp).clip(CircleShape).background(col).clickable { color = c }.then(if (c == color) Modifier.padding(0.dp) else Modifier),
                        contentAlignment = Alignment.Center,
                    ) { if (c == color) Icon(Ti.Check, "$c, chosen", tint = androidx.compose.ui.graphics.Color.Black.copy(alpha = 0.75f)) }
                }
            }
            val profiles = meta.value?.profiles.orEmpty()
            if (profiles.size > 1) {
                Gap(16.dp)
                Text("Model", style = MaterialTheme.typography.labelLarge)
                Gap(8.dp)
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    FilterChip(selected = profile.isBlank(), onClick = { profile = "" }, label = { Text("Same as the chat") })
                    profiles.forEach { p -> FilterChip(selected = profile == p, onClick = { profile = p }, label = { Text(p) }) }
                }
            }
            Gap(16.dp)
            Row(Modifier.fillMaxWidth().heightIn(min = 56.dp).clip(MaterialTheme.shapes.medium).clickable { readOnly = !readOnly }, verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text("Looks only", style = MaterialTheme.typography.titleSmall)
                    Text("It can read and search, but anything that needs your approval is refused, even if one of your rules would allow it.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                Switch(readOnly, null, modifier = Modifier.padding(start = 12.dp))
            }
            error?.let { Gap(12.dp); ErrorNote(it) }
            Gap(24.dp)
            Button(
                onClick = {
                    busy = true; error = null
                    scope.launch {
                        try {
                            repo.api.postJson("/api/personas", buildJsonObject {
                                put("action", "save"); agent?.let { put("id", it.id) }
                                put("name", name.trim()); put("icon", icon.trim()); put("color", color)
                                put("instructions", instructions.trim()); put("profile", profile); put("read_only", readOnly)
                            })
                            onDone()
                        } catch (e: Exception) { error = e.message }
                        busy = false
                    }
                },
                enabled = name.isNotBlank() && instructions.isNotBlank() && !busy, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp),
            ) { Text("Save agent") }
            if (agent != null) {
                Gap(8.dp)
                TextButton(onClick = { confirmRemove = true }, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp)) { Text("Remove this agent", color = MaterialTheme.extra.bad) }
            }
            Gap(32.dp)
        }
    }
    if (confirmRemove && agent != null) AlertDialog(
        onDismissRequest = { confirmRemove = false }, title = { Text("Remove ${agent.name}?") },
        text = { Text("Its conversations stay in your list, but you will no longer be able to start new ones with it.") },
        confirmButton = { TextButton(onClick = { confirmRemove = false; scope.launch { try { repo.api.postJson("/api/personas", buildJsonObject { put("action", "remove"); put("id", agent.id) }); onDone() } catch (e: Exception) { error = e.message } } }) { Text("Remove", color = MaterialTheme.extra.bad) } },
        dismissButton = { TextButton(onClick = { confirmRemove = false }) { Text("Keep it") } },
    )
}
