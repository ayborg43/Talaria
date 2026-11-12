package app.talaria.ui.screens

import app.talaria.ui.theme.Ti

import androidx.activity.compose.BackHandler
import androidx.appcompat.app.AppCompatActivity
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import app.talaria.ui.components.Button
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import app.talaria.ui.components.SecondaryButton
import app.talaria.ui.components.OutlinedTextField
import app.talaria.ui.components.Switch
import androidx.compose.material3.Text
import app.talaria.ui.components.TextButton
import androidx.compose.material3.TimePicker
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.rememberTimePickerState
import androidx.compose.runtime.LaunchedEffect
import app.talaria.data.AppJson
import app.talaria.data.BriefDto
import app.talaria.ui.dayAndClock
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.BuildConfig
import app.talaria.data.ControlDto
import app.talaria.data.RulesDto
import app.talaria.data.Repo
import app.talaria.data.Servers
import app.talaria.data.VoiceDto
import app.talaria.service.ConnectionService
import app.talaria.ui.Lock
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.Panel
import app.talaria.ui.components.Pill
import app.talaria.ui.components.SectionTitle
import app.talaria.ui.components.Tone
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.extra
import app.talaria.voice.VoiceController
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

@Composable
fun SettingsScreen(activity: AppCompatActivity, repo: Repo, voice: VoiceController, modifier: Modifier, onAskNotifications: () -> Unit) {
    var sub by remember { mutableStateOf<String?>(null) }
    BackHandler(enabled = sub != null) { sub = null }
    when (sub) {
        "rules" -> RulesScreen(repo, onBack = { sub = null }, modifier = modifier)
        "voice" -> VoiceSetupScreen(repo, onBack = { sub = null }, modifier = modifier)
        "memory" -> MemoryScreen(repo, onBack = { sub = null }, modifier = modifier)
        else -> SettingsHome(activity, repo, modifier, onOpen = { sub = it }, onAskNotifications = onAskNotifications)
    }
}

@Composable
private fun SettingsHome(activity: AppCompatActivity, repo: Repo, modifier: Modifier, onOpen: (String) -> Unit, onAskNotifications: () -> Unit) {
    val control = rememberPolled(15_000, "control") { repo.api.getJson<ControlDto>("/api/control") }
    val voiceInfo = rememberPolled(0, "voiceinfo") { repo.api.getJson<VoiceDto>("/api/voice") }
    val paused by repo.paused.collectAsState()
    val theme by repo.theme.collectAsState()
    val scope = rememberCoroutineScope()
    var error by remember { mutableStateOf<String?>(null) }
    var confirmOut by remember { mutableStateOf(false) }
    var notifications by remember { mutableStateOf(repo.store.notifications) }
    var lock by remember { mutableStateOf(repo.store.lockWithBiometrics) }
    var speak by remember { mutableStateOf(repo.store.speakReplies) }
    val c = control.value

    Column(modifier.fillMaxSize()) {
        TopAppBar(title = { Text("Settings", style = MaterialTheme.typography.headlineSmall) }, colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background))
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp)) {
            item {
                error?.let { ErrorNote(it); Gap(8.dp) }
                SectionTitle("Server")
                Panel(Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(16.dp)) {
                        Text(Servers.display(repo.store.baseUrl), style = MaterialTheme.typography.titleMedium)
                        Gap(4.dp)
                        Text(listOfNotNull(c?.model?.takeIf { it.isNotBlank() }, c?.version?.takeIf { it.isNotBlank() }).joinToString(" · ").ifBlank { "Connected" }, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        Gap(12.dp)
                        SecondaryButton(onClick = { confirmOut = true }, shape = MaterialTheme.shapes.medium) { Text("Sign out of this phone") }
                    }
                }

                SectionTitle("You")
                Panel(Modifier.fillMaxWidth()) {
                    Column {
                        LinkRow("Profile and memory", "Your name, the tone you like, and what Talaria remembers.", onClick = { onOpen("memory") })
                    }
                }

                SectionTitle("The agent")
                Panel(Modifier.fillMaxWidth()) {
                    Column {
                        BriefRow(repo)
                        HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                        ToggleRow("Pause everything", "Stops goals, watches and scheduled jobs from starting anything until you resume.", paused) { v -> scope.launch { try { repo.setPaused(v); error = null } catch (e: Exception) { error = e.message } } }
                        HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                        LinkRow("What it asks first", "Choose what it may do on its own, and what it must ask about.", onClick = { onOpen("rules") })
                    }
                }

                SectionTitle("Voice")
                Panel(Modifier.fillMaxWidth()) {
                    Column {
                        LinkRow(
                            "Speech service", if (voiceInfo.value?.available == true) "On: ${voiceInfo.value?.model}" else "Not set up. Needed to talk to Talaria.",
                            trailing = { Pill(if (voiceInfo.value?.available == true) "On" else "Off", if (voiceInfo.value?.available == true) Tone.Ok else Tone.Neutral) }, onClick = { onOpen("voice") },
                        )
                        HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                        ToggleRow("Read replies aloud", "In a voice conversation, replies are spoken with this phone's own voice.", speak) { speak = it; repo.store.speakReplies = it }
                    }
                }

                SectionTitle("This phone")
                Panel(Modifier.fillMaxWidth()) {
                    Column {
                        ToggleRow("Notifications", "Approvals and finished work, while the app is closed. Keeps a small connection notice showing.", notifications) { v ->
                            notifications = v; repo.store.notifications = v
                            if (v) onAskNotifications() else ConnectionService.sync(activity)
                            ConnectionService.sync(activity)
                        }
                        HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                        val canLock = remember { Lock.available(activity) }
                        ToggleRow("Lock the app", if (canLock) "Ask for your fingerprint, face or PIN when you open it. Approvals from notifications then need unlocking first." else "Set a screen lock on this phone first.", lock, enabled = canLock) { v ->
                            if (v) Lock.prompt(activity, "Turn on the app lock", "Confirm it is you", { lock = true; repo.store.lockWithBiometrics = true }, { error = it })
                            else { lock = false; repo.store.lockWithBiometrics = false }
                        }
                        HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                        Column(Modifier.padding(16.dp)) {
                            Text("Appearance", style = MaterialTheme.typography.titleSmall)
                            Gap(8.dp)
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                listOf("system" to "Match phone", "light" to "Light", "dark" to "Dark").forEach { (k, label) ->
                                    FilterChip(selected = theme == k, onClick = { repo.setTheme(k) }, label = { Text(label) })
                                }
                            }
                        }
                    }
                }

                SectionTitle("About")
                Panel(Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(16.dp)) {
                        Text("Talaria for Android ${BuildConfig.VERSION_NAME}", style = MaterialTheme.typography.titleSmall)
                        Text("A client for your own Talaria server. Your chats, memory and files stay on that server.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
                Gap(32.dp)
            }
        }
    }
    if (confirmOut) AlertDialog(
        onDismissRequest = { confirmOut = false }, title = { Text("Sign out?") },
        text = { Text("This phone will forget the session. Your conversations stay on the server.") },
        confirmButton = { TextButton(onClick = { confirmOut = false; scope.launch { repo.signOut(); ConnectionService.sync(activity) } }) { Text("Sign out", color = MaterialTheme.extra.bad) } },
        dismissButton = { TextButton(onClick = { confirmOut = false }) { Text("Stay signed in") } },
    )
}

@Composable
private fun ToggleRow(title: String, body: String, checked: Boolean, enabled: Boolean = true, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().heightIn(min = 64.dp).clickable(enabled = enabled) { onChange(!checked) }.padding(horizontal = 16.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.titleSmall)
            Text(body, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        Switch(checked, onCheckedChange = null, enabled = enabled, modifier = Modifier.padding(start = 12.dp))
    }
}

@Composable
private fun LinkRow(title: String, body: String, trailing: @Composable (() -> Unit)? = null, onClick: () -> Unit) {
    Row(Modifier.fillMaxWidth().heightIn(min = 64.dp).clickable(onClick = onClick).padding(horizontal = 16.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.titleSmall)
            Text(body, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        trailing?.invoke()
        Icon(Ti.ChevronRight, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

// ---- what it asks first ----

private val modeWords = mapOf("default" to "Usual", "allow" to "Do it", "ask" to "Ask me", "handoff" to "Hand it to me", "deny" to "Never")

@Composable
private fun RulesScreen(repo: Repo, onBack: () -> Unit, modifier: Modifier) {
    val rules = rememberPolled(0, "rules") { repo.api.getJson<RulesDto>("/api/rules") }
    val scope = rememberCoroutineScope()
    var error by remember { mutableStateOf<String?>(null) }
    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("What it asks first", style = MaterialTheme.typography.titleLarge) },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp)) {
            item {
                Text("For each kind of action: do it, ask you first, hand it to you to do yourself, or never do it. “Usual” follows the server's own default.", style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Gap(12.dp)
                error?.let { ErrorNote(it); Gap(8.dp) }
                rules.error?.let { ErrorNote(it) }
                val r = rules.value
                if (r != null) Panel(Modifier.fillMaxWidth()) {
                    Column {
                        r.categories.forEachIndexed { i, cat ->
                            if (i > 0) HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                            var open by remember { mutableStateOf(false) }
                            Row(Modifier.fillMaxWidth().heightIn(min = 64.dp).padding(start = 16.dp, end = 8.dp, top = 10.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically) {
                                Column(Modifier.weight(1f)) {
                                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                                        Text(cat.title, style = MaterialTheme.typography.titleSmall)
                                        if (cat.risky) Pill("Careful", Tone.Bad)
                                    }
                                    Text(cat.detail, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                }
                                Box {
                                    SecondaryButton(onClick = { open = true }, shape = MaterialTheme.shapes.medium) { Text(modeWords[cat.mode] ?: cat.mode) }
                                    DropdownMenu(open, { open = false }) {
                                        r.modes.forEach { m ->
                                            DropdownMenuItem(text = { Text(modeWords[m] ?: m) }, onClick = {
                                                open = false
                                                scope.launch { try { repo.api.postJson("/api/rules", buildJsonObject { put("category", cat.id); put("mode", m) }); rules.refresh(); error = null } catch (e: Exception) { error = e.message } }
                                            })
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
                Gap(24.dp)
            }
        }
    }
}

// ---- speech service ----

@Composable
private fun VoiceSetupScreen(repo: Repo, onBack: () -> Unit, modifier: Modifier) {
    val raw = rememberPolled(0, "voiceraw") { repo.api.get("/api/voice") }
    val scope = rememberCoroutineScope()
    var base by remember { mutableStateOf("") }
    var model by remember { mutableStateOf("") }
    var key by remember { mutableStateOf("") }
    var provider by remember { mutableStateOf("openai") }
    var loaded by remember { mutableStateOf(false) }
    var note by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    val obj = remember(raw.value) { runCatching { app.talaria.data.AppJson.parseToJsonElement(raw.value ?: "{}") as JsonObject }.getOrNull() }
    val presets = remember(obj) { (obj?.get("presets") as? JsonArray).orEmpty().mapNotNull { it as? JsonObject } }
    fun s(o: JsonObject?, k: String) = (o?.get(k) as? JsonPrimitive)?.content.orEmpty()
    if (obj != null && obj.containsKey("provider") && !loaded) { loaded = true; base = s(obj, "base_url"); model = s(obj, "model"); provider = s(obj, "provider").ifBlank { "openai" } }
    val available = (obj?.get("available") as? JsonPrimitive)?.content == "true"

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("Speech service", style = MaterialTheme.typography.titleLarge) },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 4.dp)) {
            Text("Your voice is turned into text by a service you choose. It is sent there with your key; Talaria stores neither. A server on this network can be used without one.", style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Gap(12.dp)
            if (available) Pill("On: ${s(obj, "model")}", Tone.Ok) else Pill("Not set up")
            Gap(16.dp)
            if (presets.isNotEmpty()) {
                Text("Start from", style = MaterialTheme.typography.labelLarge); Gap(8.dp)
                androidx.compose.foundation.layout.FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    presets.forEach { p -> FilterChip(selected = false, onClick = { base = s(p, "base_url"); model = s(p, "model"); provider = s(p, "provider").ifBlank { "openai" } }, label = { Text(s(p, "label")) }) }
                }
                Gap(16.dp)
            }
            OutlinedTextField(base, { base = it }, label = { Text("Service address") }, singleLine = true, modifier = Modifier.fillMaxWidth(), shape = MaterialTheme.shapes.medium)
            Gap(12.dp)
            OutlinedTextField(model, { model = it }, label = { Text("Model") }, singleLine = true, modifier = Modifier.fillMaxWidth(), shape = MaterialTheme.shapes.medium)
            Gap(12.dp)
            OutlinedTextField(key, { key = it }, label = { Text("API key (optional)") }, supportingText = { Text("Sent to your server and kept there. Leave empty to keep what is saved.") }, singleLine = true, visualTransformation = PasswordVisualTransformation(), modifier = Modifier.fillMaxWidth(), shape = MaterialTheme.shapes.medium)
            error?.let { Gap(12.dp); ErrorNote(it) }
            note?.let { Gap(12.dp); Text(it, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.extra.ok) }
            Gap(20.dp)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(
                    onClick = {
                        busy = true; error = null; note = null
                        scope.launch {
                            try {
                                repo.api.postJson("/api/voice", buildJsonObject { put("provider", provider); put("base_url", base.trim()); put("model", model.trim()); if (key.isNotBlank()) put("api_key", key) })
                                key = ""; note = "Saved."; raw.refresh()
                            } catch (e: Exception) { error = e.message }
                            busy = false
                        }
                    }, enabled = base.isNotBlank() && model.isNotBlank() && !busy, shape = MaterialTheme.shapes.large, modifier = Modifier.heightIn(min = 48.dp),
                ) { Text("Save") }
                SecondaryButton(
                    onClick = { busy = true; error = null; note = null; scope.launch { try { note = messageOf(repo.api.post("/api/voice/test")) ?: "The service answered."; } catch (e: Exception) { error = e.message }; busy = false } },
                    enabled = available && !busy, shape = MaterialTheme.shapes.large, modifier = Modifier.heightIn(min = 48.dp),
                ) { Text("Test it") }
                if (available) TextButton(onClick = { scope.launch { try { repo.api.postJson("/api/voice", buildJsonObject { put("disconnect", true) }); raw.refresh(); loaded = false } catch (e: Exception) { error = e.message } } }) { Text("Disconnect") }
            }
            Gap(32.dp)
        }
    }
}


// ---- the morning brief ----

@Composable
private fun BriefRow(repo: Repo) {
    val brief = rememberPolled(0, "brief") { repo.api.getJson<BriefDto>("/api/brief") }
    var data by remember { mutableStateOf<BriefDto?>(null) }
    LaunchedEffect(brief.value) { brief.value?.let { data = it } }
    val scope = rememberCoroutineScope()
    var picking by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val b = data ?: return
    if (!b.available) return
    fun save(enabled: Boolean, at: String) = scope.launch {
        try { data = AppJson.decodeFromString(repo.api.postJson("/api/brief", buildJsonObject { put("enabled", enabled); put("at", at) })); error = null } catch (e: Exception) { error = e.message }
    }
    val at = b.at.ifBlank { "07:30" }
    Column {
        ToggleRow("Morning brief", if (b.enabled) "Arrives at $at${if (b.next.isNotBlank()) ". Next: ${dayAndClock(b.next)}" else ""}." else "A short summary of your day, sent to you each morning.", b.enabled) { save(it, at) }
        if (b.enabled) Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, bottom = 12.dp), verticalAlignment = Alignment.CenterVertically) {
            Text("Time", style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f))
            SecondaryButton(onClick = { picking = true }) { Icon(Ti.Sun, null, Modifier.size(18.dp)); Text(at, Modifier.padding(start = 8.dp)) }
        }
        error?.let { Text(it, Modifier.padding(horizontal = 16.dp, vertical = 4.dp), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.extra.bad) }
    }
    if (picking) {
        val state = rememberTimePickerState(at.substringBefore(':').toIntOrNull() ?: 7, at.substringAfter(':').toIntOrNull() ?: 30, is24Hour = true)
        AlertDialog(
            onDismissRequest = { picking = false }, title = { Text("When should it arrive?") },
            text = { TimePicker(state) },
            confirmButton = { TextButton(onClick = { picking = false; save(true, "%02d:%02d".format(state.hour, state.minute)) }) { Text("Save") } },
            dismissButton = { TextButton(onClick = { picking = false }) { Text("Cancel") } },
        )
    }
}
