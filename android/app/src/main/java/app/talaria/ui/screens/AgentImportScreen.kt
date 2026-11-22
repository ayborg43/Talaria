package app.talaria.ui.screens

import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.clickable
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
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import app.talaria.data.AgentPreviewDto
import app.talaria.data.AppJson
import app.talaria.data.Repo
import app.talaria.ui.AgentFile
import app.talaria.ui.components.Button
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.OutlinedTextField
import app.talaria.ui.components.Panel
import app.talaria.ui.components.Pill
import app.talaria.ui.components.SecondaryButton
import app.talaria.ui.components.Switch
import app.talaria.ui.components.Tone
import app.talaria.ui.theme.Mono
import app.talaria.ui.theme.Ti
import app.talaria.ui.theme.extra
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * Add an agent someone shared: pick its file or paste its text, read exactly what it will be told to do, and add it.
 * An imported agent comes without a model of its own and starts as "looks only" until the switch is turned off here.
 */
@Composable
fun AgentImportScreen(repo: Repo, initialText: String, onBack: () -> Unit, onAdded: () -> Unit, modifier: Modifier = Modifier) {
    BackHandler(onBack = onBack)
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var text by remember { mutableStateOf(initialText) }
    var preview by remember { mutableStateOf<AgentPreviewDto?>(null) }
    var looksOnly by remember { mutableStateOf(true) } // on until the person turns it off
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    var added by remember { mutableStateOf<String?>(null) }

    fun review() {
        busy = true; error = null; preview = null
        scope.launch {
            try {
                preview = AppJson.decodeFromString(repo.api.postJson("/api/agent-file", buildJsonObject { put("text", text) }))
                looksOnly = true
            } catch (e: Exception) { error = e.message ?: "That could not be read." }
            busy = false
        }
    }

    val pick = rememberLauncherForActivityResult(ActivityResultContracts.GetContent()) { uri ->
        if (uri == null) return@rememberLauncherForActivityResult
        scope.launch {
            try {
                val read = withContext(Dispatchers.IO) { ctx.contentResolver.openInputStream(uri)?.use { AgentFile.readLimited(it) } }
                    ?: throw java.io.IOException("That file could not be read.")
                if (read.size > AgentFile.READ_LIMIT) throw java.io.IOException("That file is too big to be an agent file.")
                text = String(read, Charsets.UTF_8); review()
            } catch (e: Exception) { error = e.message ?: "That file could not be read." }
        }
    }

    // A file shared to Talaria arrives with its text: show what is in it at once.
    LaunchedEffect(Unit) { if (initialText.isNotBlank()) review() }

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("Import an agent", style = MaterialTheme.typography.titleLarge) },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 4.dp)) {
            val p = preview
            if (added != null) {
                Panel(Modifier.fillMaxWidth(), tint = MaterialTheme.extra.okTint, edge = MaterialTheme.extra.okLine) {
                    Text("${added} was added. Find it under Your agents.", Modifier.padding(16.dp), style = MaterialTheme.typography.bodyLarge, color = MaterialTheme.extra.ok)
                }
                Gap(16.dp)
                Button(onClick = onAdded, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp)) { Text("Done") }
            } else if (p == null) {
                Text("An agent file is what someone sent you from Share on their agent: its name and the instructions it follows. Pick the file, or paste what is in it.", style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Gap(16.dp)
                SecondaryButton(onClick = { pick.launch("*/*") }, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp)) { Icon(Ti.File, null, Modifier.size(18.dp)); Text("Choose a file", Modifier.padding(start = 8.dp)) }
                Gap(12.dp)
                OutlinedTextField(
                    text, { text = it; error = null }, label = { Text("Or paste it here") }, minLines = 6, maxLines = 12,
                    modifier = Modifier.fillMaxWidth(), shape = MaterialTheme.shapes.medium,
                )
                error?.let { Gap(12.dp); ErrorNote(it) }
                Gap(16.dp)
                Button(onClick = { review() }, enabled = text.isNotBlank() && !busy, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp)) { Text(if (busy) "Reading…" else "Read it") }
            } else {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    Box(Modifier.size(48.dp), contentAlignment = Alignment.Center) { Text(p.agent.icon.ifBlank { p.agent.name.take(1).uppercase() }, style = MaterialTheme.typography.headlineSmall) }
                    Column(Modifier.weight(1f)) {
                        Text(p.agent.name, style = MaterialTheme.typography.titleLarge)
                        Text("Read what it will be told to do before you add it.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
                Gap(12.dp)
                // The whole of the instructions, in a box that scrolls on its own, so nothing is hidden behind "more".
                Panel(Modifier.fillMaxWidth()) {
                    Box(Modifier.heightIn(max = 320.dp).verticalScroll(rememberScrollState()).padding(14.dp)) {
                        SelectionContainer { Text(p.agent.instructions, style = MaterialTheme.typography.bodyMedium, fontFamily = FontFamily.Default) }
                    }
                }
                Gap(16.dp)
                if (p.exists) {
                    Panel(Modifier.fillMaxWidth(), tint = MaterialTheme.extra.brassSoft, edge = MaterialTheme.extra.brassLine) {
                        Text("You already have an agent called ${p.agent.name}. Remove or rename that one first, or change the name in the file.", Modifier.padding(14.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.extra.brassText)
                    }
                    Gap(16.dp)
                    SecondaryButton(onClick = { preview = null; error = null }, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp)) { Text("Back") }
                } else {
                    Row(Modifier.fillMaxWidth().heightIn(min = 56.dp).clickable { looksOnly = !looksOnly }, verticalAlignment = Alignment.CenterVertically) {
                        Column(Modifier.weight(1f)) {
                            Text("Looks only", style = MaterialTheme.typography.titleSmall)
                            Text("It can read and search, but anything that needs your approval is refused. Turn this off only if you trust what it says above.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                        Switch(looksOnly, null, modifier = Modifier.padding(start = 12.dp))
                    }
                    Gap(8.dp)
                    Pill("No model of its own", Tone.Info)
                    Text("It comes without a model: it uses the same one as your chat until you choose another in its settings.", Modifier.padding(top = 6.dp), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    error?.let { Gap(12.dp); ErrorNote(it) }
                    Gap(20.dp)
                    Button(
                        onClick = {
                            busy = true; error = null
                            scope.launch {
                                try {
                                    // read_only is always sent: the answer is what the switch says, not the server's default.
                                    repo.api.postJson("/api/agent-file", buildJsonObject { put("text", text); put("import", true); put("read_only", looksOnly) })
                                    added = p.agent.name
                                } catch (e: Exception) { error = e.message ?: "That could not be added." }
                                busy = false
                            }
                        },
                        enabled = !busy, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp),
                    ) { Text(if (busy) "Adding…" else "Add ${p.agent.name}") }
                    Gap(8.dp)
                    SecondaryButton(onClick = { preview = null; error = null }, shape = MaterialTheme.shapes.large, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp)) { Text("Back") }
                }
            }
            Gap(32.dp)
        }
    }
}
