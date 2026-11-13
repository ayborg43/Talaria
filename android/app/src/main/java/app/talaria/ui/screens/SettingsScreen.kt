package app.talaria.ui.screens

import app.talaria.ui.theme.Ti

import androidx.activity.compose.BackHandler
import android.content.Intent
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
import app.talaria.data.Conn
import app.talaria.service.Alerts
import androidx.compose.runtime.DisposableEffect
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
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
        "models" -> ModelsScreen(repo, onBack = { sub = null }, modifier = modifier)
        "phone" -> PhoneAccessScreen(activity, repo, onBack = { sub = null }, modifier = modifier)
        else -> SettingsHome(activity, repo, modifier, onOpen = { sub = it }, onAskNotifications = onAskNotifications)
    }
}

@Composable
private fun SettingsHome(activity: AppCompatActivity, repo: Repo, modifier: Modifier, onOpen: (String) -> Unit, onAskNotifications: () -> Unit) {
    val base by repo.base.collectAsState()
    val servers by repo.servers.collectAsState()
    val conn by repo.conn.collectAsState()
    var removing by remember { mutableStateOf<String?>(null) }
    val control = rememberPolled(15_000, "control$base") { repo.api.getJson<ControlDto>("/api/control") }
    val voiceInfo = rememberPolled(0, "voiceinfo$base") { repo.api.getJson<VoiceDto>("/api/voice") }
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
                SectionTitle(if (servers.size > 1) "Servers" else "Server")
                Panel(Modifier.fillMaxWidth()) {
                    Column {
                        servers.forEach { origin ->
                            val live = origin == base
                            Row(
                                Modifier.fillMaxWidth().heightIn(min = 64.dp).clickable(enabled = !live) {
                                    if (repo.switchTo(origin)) { repo.toast("Now on ${Servers.display(origin)}"); ConnectionService.sync(activity) }
                                }.padding(horizontal = 16.dp, vertical = 10.dp),
                                verticalAlignment = Alignment.CenterVertically,
                            ) {
                                Column(Modifier.weight(1f)) {
                                    Text(Servers.display(origin), style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                    Text(
                                        if (live) listOfNotNull(c?.model?.takeIf { it.isNotBlank() }, c?.version?.takeIf { it.isNotBlank() }).joinToString(" · ").ifBlank { "Connected" } else "Tap to switch to it",
                                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    )
                                }
                                if (live) { if (servers.size > 1) Pill("In use", Tone.Brass) } else TextButton(onClick = { removing = origin }) { Text("Remove") }
                            }
                            HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                        }
                        LinkRow("Add another server", "Sign in to a second Talaria and switch between them. Only the one in use sends alerts live; the others are checked every quarter hour.", onClick = { repo.beginAdd() })
                        Column(Modifier.padding(start = 16.dp, end = 16.dp, bottom = 16.dp)) {
                            SecondaryButton(onClick = { confirmOut = true }, shape = MaterialTheme.shapes.medium) { Text(if (servers.size > 1) "Sign out of ${Servers.display(base)}" else "Sign out of this phone") }
                        }
                    }
                }

                SectionTitle("You")
                Panel(Modifier.fillMaxWidth()) {
                    Column {
                        LinkRow("Profile and memory", "Your name, the tone you like, and what Talaria remembers.", onClick = { onOpen("memory") })
                    }
                }

                SectionTitle("Your phone")
                Panel(Modifier.fillMaxWidth()) {
                    Column {
                        val phoneOps by (activity.application as app.talaria.TalariaApp).phone.ops.collectAsState()
                        LinkRow(
                            "What Talaria may read", "Location, contacts, calendar and notifications, only when you ask and only what you switch on.",
                            trailing = { Pill(if (phoneOps.isEmpty()) "Off" else "${phoneOps.size} on", if (phoneOps.isEmpty()) Tone.Neutral else Tone.Brass) },
                            onClick = { onOpen("phone") },
                        )
                    }
                }

                SectionTitle("The agent")
                Panel(Modifier.fillMaxWidth()) {
                    Column {
                        LinkRow("AI models", "Add keys for the services you use, say what each model is for, and choose what to try when one stops working.", onClick = { onOpen("models") })
                        HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
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
                        SpeechWhereRow(activity, repo)
                        HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                        LinkRow(
                            "Speech service on your server", if (voiceInfo.value?.available == true) "On: ${voiceInfo.value?.model}" else "Not set up. Only needed if you choose your server above.",
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
                        if (notifications) AlertRows(activity, repo, conn)
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
    removing?.let { origin ->
        AlertDialog(
            onDismissRequest = { removing = null }, title = { Text("Remove ${Servers.display(origin)}?") },
            text = { Text("This phone will sign out of it and stop checking it. Your conversations stay on that server.") },
            confirmButton = { TextButton(onClick = { removing = null; scope.launch { repo.forgetServer(origin) } }) { Text("Remove", color = MaterialTheme.extra.bad) } },
            dismissButton = { TextButton(onClick = { removing = null }) { Text("Keep it") } },
        )
    }
    if (confirmOut) AlertDialog(
        onDismissRequest = { confirmOut = false }, title = { Text("Sign out?") },
        text = { Text(if (servers.size > 1) "This phone will forget its session on ${Servers.display(base)} and move to another server. Your conversations stay on the server." else "This phone will forget the session. Your conversations stay on the server.") },
        confirmButton = { TextButton(onClick = { confirmOut = false; scope.launch { repo.signOut(); ConnectionService.sync(activity) } }) { Text("Sign out", color = MaterialTheme.extra.bad) } },
        dismissButton = { TextButton(onClick = { confirmOut = false }) { Text("Stay signed in") } },
    )
}

/**
 * Who turns your voice into text. The phone's own recognizer is the default and needs nothing set up; with "keep it on
 * this phone" on (also the default) it only ever uses a recognizer that runs on the phone, so your voice is not sent anywhere.
 */
@Composable
private fun SpeechWhereRow(activity: AppCompatActivity, repo: Repo) {
    var engine by remember { mutableStateOf(repo.store.speechEngine) }
    var local by remember { mutableStateOf(repo.store.speechLocalOnly) }
    var resumed by remember { mutableStateOf(0) }
    DisposableEffect(activity) {
        val o = LifecycleEventObserver { _, e -> if (e == Lifecycle.Event.ON_RESUME) resumed++ }
        activity.lifecycle.addObserver(o)
        onDispose { activity.lifecycle.removeObserver(o) }
    }
    val has = remember(resumed) { app.talaria.voice.PhoneSpeech.exists(activity) }
    val support = app.talaria.voice.Engines.support(has.first, has.second, local)
    Column(Modifier.fillMaxWidth().padding(16.dp)) {
        Text("Who recognises your speech", style = MaterialTheme.typography.titleSmall)
        Gap(8.dp)
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            FilterChip(selected = engine == "phone", onClick = { engine = "phone"; repo.store.speechEngine = "phone" }, label = { Text("This phone") })
            FilterChip(selected = engine == "server", onClick = { engine = "server"; repo.store.speechEngine = "server" }, label = { Text("My server") })
        }
        Gap(6.dp)
        Text(
            when {
                engine == "server" -> "Your voice is sent to the speech service set up on your server. You can talk over a reply to interrupt it."
                support == app.talaria.voice.Support.OnDevice -> "Ready. Your voice is recognised on this phone and goes nowhere. Replies are not interrupted by speaking: tap to stop."
                support == app.talaria.voice.Support.Online -> "Uses Android's online recognizer, so your voice may be sent to the company behind the phone's speech service."
                else -> app.talaria.voice.Engines.why(has.first, has.second, local)
            },
            style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }
    if (engine == "phone") {
        HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
        ToggleRow("Keep speech on this phone", "Only use a recogniser that runs on the phone. Turn off to let Android use its online one when there is no offline pack.", local) { local = it; repo.store.speechLocalOnly = it }
    }
}

/** What keeps alerts arriving: whether the live link is up, whether Android may pause it, quiet hours, and the system sounds. */
@Composable
private fun AlertRows(activity: AppCompatActivity, repo: Repo, conn: Conn) {
    val divider = @Composable { HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f)) }
    var resumed by remember { mutableStateOf(0) }
    DisposableEffect(activity) {
        val o = LifecycleEventObserver { _, e -> if (e == Lifecycle.Event.ON_RESUME) resumed++ }
        activity.lifecycle.addObserver(o)
        onDispose { activity.lifecycle.removeObserver(o) }
    }
    val pm = activity.getSystemService(android.os.PowerManager::class.java)
    val allowed = remember(resumed) { pm.isIgnoringBatteryOptimizations(activity.packageName) }
    var quiet by remember { mutableStateOf(repo.store.quietHours) }
    var from by remember { mutableStateOf(repo.store.quietFrom) }
    var to by remember { mutableStateOf(repo.store.quietTo) }
    var picking by remember { mutableStateOf<String?>(null) }

    divider()
    Row(Modifier.fillMaxWidth().heightIn(min = 64.dp).padding(horizontal = 16.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text("Alert link", style = MaterialTheme.typography.titleSmall)
            Text("The live connection to your server. If Android stops it, it is started again and checked every quarter hour.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        when (conn) {
            is Conn.Online -> Pill("Live", Tone.Ok)
            is Conn.Connecting -> Pill("Connecting", Tone.Neutral)
            is Conn.Offline -> Pill("Offline", Tone.Bad)
            else -> Pill("Off", Tone.Neutral)
        }
    }
    divider()
    LinkRow(
        "Run in the background",
        if (allowed) "Allowed. Android will not pause alerts to save battery." else "Android may pause alerts to save battery. Allow Talaria to keep running so they arrive on time.",
        trailing = { Pill(if (allowed) "Allowed" else "Limited", if (allowed) Tone.Ok else Tone.Brass) },
        onClick = {
            val intent = if (allowed) Intent(android.provider.Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)
            else Intent(android.provider.Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, android.net.Uri.parse("package:${activity.packageName}"))
            try { activity.startActivity(intent) } catch (_: Exception) { repo.toast("Open this phone's battery settings and allow Talaria to run in the background.") }
        },
    )
    divider()
    ToggleRow("Quiet hours", if (quiet) "Replies arrive without a sound from ${Alerts.clock(from)} to ${Alerts.clock(to)}. Questions that need your answer still sound." else "Replies arrive silently during hours you choose. Questions that need your answer still sound.", quiet) { quiet = it; repo.store.quietHours = it }
    if (quiet) Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, bottom = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
        Text("Between", style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f))
        SecondaryButton(onClick = { picking = "from" }) { Text(Alerts.clock(from)) }
        Text("and", style = MaterialTheme.typography.bodyMedium)
        SecondaryButton(onClick = { picking = "to" }) { Text(Alerts.clock(to)) }
    }
    divider()
    LinkRow("Sounds and vibration", "Choose how approvals and replies sound.", onClick = {
        try { activity.startActivity(Intent(android.provider.Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(android.provider.Settings.EXTRA_APP_PACKAGE, activity.packageName)) } catch (_: Exception) {}
    })
    picking?.let { which ->
        val at = if (which == "from") from else to
        val state = rememberTimePickerState(at / 60, at % 60, is24Hour = true)
        AlertDialog(
            onDismissRequest = { picking = null }, title = { Text(if (which == "from") "Quiet from" else "Quiet until") },
            text = { TimePicker(state) },
            confirmButton = { TextButton(onClick = {
                val m = state.hour * 60 + state.minute
                if (which == "from") { from = m; repo.store.quietFrom = m } else { to = m; repo.store.quietTo = m }
                picking = null
            }) { Text("Save") } },
            dismissButton = { TextButton(onClick = { picking = null }) { Text("Cancel") } },
        )
    }
}

@Composable
internal fun ToggleRow(title: String, body: String, checked: Boolean, enabled: Boolean = true, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().heightIn(min = 64.dp).clickable(enabled = enabled) { onChange(!checked) }.padding(horizontal = 16.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.titleSmall)
            Text(body, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        Switch(checked, onCheckedChange = null, enabled = enabled, modifier = Modifier.padding(start = 12.dp))
    }
}

@Composable
internal fun LinkRow(title: String, body: String, trailing: @Composable (() -> Unit)? = null, onClick: () -> Unit) {
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
