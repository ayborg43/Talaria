package app.talaria.ui.screens

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
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
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import app.talaria.data.AppJson
import app.talaria.data.Repo
import app.talaria.ui.Polled
import app.talaria.ui.components.Button
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.OutlinedTextField
import app.talaria.ui.components.Panel
import app.talaria.ui.components.Pill
import app.talaria.ui.components.SecondaryButton
import app.talaria.ui.components.SectionTitle
import app.talaria.ui.components.TextButton
import app.talaria.ui.components.Tone
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.Ti
import app.talaria.ui.theme.extra
import kotlinx.coroutines.launch
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

@Serializable
data class ModelProfile(
    val name: String = "", val type: String = "", val model: String = "", val notes: String = "",
    val has_key: Boolean = false, val available: Boolean = false, val status: String = "",
    val default: Boolean = false, val active: Boolean = false, val best: List<String> = emptyList(),
    val failing: String = "", val failing_hard: Boolean = false,
    val base_url: String = "", val api_key_env: String = "",
)

@Serializable
data class ModelPreset(
    val label: String = "", val type: String = "", val base_url: String = "", val model: String = "",
    val key_env: String = "", val hint: String = "", val needs_key: Boolean = false,
)

@Serializable
data class ModelChoicesDto(val models: List<String> = emptyList(), val model: String = "", val listed: Boolean = false)

@Serializable
data class ModelsDto(
    val profiles: List<ModelProfile> = emptyList(), val presets: List<ModelPreset> = emptyList(),
    val fallback_order: List<String> = emptyList(), val strengths: List<String> = emptyList(),
)

@Serializable
data class RouteRow(
    val kind: String = "", val profile: String = "", val Up: Int = 0, val Down: Int = 0, val Retry: Int = 0,
    val Correction: Int = 0, val Continue: Int = 0, val score: Double = 0.0,
)

@Serializable
data class RoutingDto(
    val smart: Boolean = false, val profile: String = "", val learn: Boolean = true,
    val profiles: List<String> = emptyList(), val rows: List<RouteRow> = emptyList(),
)

private val strengthWords = mapOf("code" to "Code", "cheap" to "Cheap", "fast" to "Fast", "long" to "Long documents", "writing" to "Writing")

/**
 * The AI models Talaria can use: add one from a provider's preset with its key, say what each is for (the agent reads
 * that when it picks a model for a task), and set the order of what to try when one stops working.
 */
@Composable
fun ModelsScreen(repo: Repo, onBack: () -> Unit, modifier: Modifier = Modifier) {
    BackHandler(onBack = onBack)
    val polled = rememberPolled(0, "models" + repo.store.baseUrl) { repo.api.getJson<ModelsDto>("/api/settings") }
    val scope = rememberCoroutineScope()
    var error by remember { mutableStateOf<String?>(null) }
    var adding by remember { mutableStateOf(false) }
    var removing by remember { mutableStateOf<ModelProfile?>(null) }
    var offerTest by remember { mutableStateOf<String?>(null) }
    var choosing by remember { mutableStateOf<Pair<ModelProfile, ModelChoicesDto>?>(null) }
    val d = polled.value

    fun post(body: kotlinx.serialization.json.JsonObject, done: String? = null) = scope.launch {
        try {
            val out = repo.api.postJson("/api/settings", body)
            error = null
            // A profile saved without a usable key comes back with a warning in words.
            val warning = runCatching { (AppJson.parseToJsonElement(out) as? kotlinx.serialization.json.JsonObject)?.get("warning")?.let { (it as? JsonPrimitive)?.content } }.getOrNull()
            repo.toast(warning ?: done ?: "Saved")
            polled.refresh()
        } catch (e: Exception) { error = e.message }
    }

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("AI models", style = MaterialTheme.typography.titleLarge) },
            navigationIcon = { IconButton(onClick = if (adding) ({ adding = false }) else onBack) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        if (adding && d != null) {
            AddModel(d, onCancel = { adding = false }, onAdd = { body -> adding = false; offerTest = (body["name"] as? JsonPrimitive)?.content; post(body, "Added. Use Test to check it answers.") })
            return@Column
        }
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp)) {
            item {
                error?.let { ErrorNote(it); Gap(8.dp) }
                polled.error?.let { if (d == null) ErrorNote(it) }
                Text(
                    "Add a key for each service you use. Talaria can then give each task the model that suits it, and move on to the next one when a model stops working.",
                    style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 4.dp, vertical = 8.dp),
                )
                Gap(8.dp)
                if (d != null) {
                    d.profiles.sortedBy { it.name }.forEach { p ->
                        ProfileCard(p, repo, onTest = { post(buildJsonObject { put("test_profile", p.name) }, "It answers.") }, onModels = {
                            scope.launch {
                                try { choosing = p to AppJson.decodeFromString<ModelChoicesDto>(repo.api.postJson("/api/settings", buildJsonObject { put("list_models", p.name) })); error = null }
                                catch (e: Exception) { error = "Could not list the models: " + (e.message ?: "") }
                            }
                        }, onRemove = { removing = p }, onNotes = { n -> post(buildJsonObject { put("name", p.name); put("notes", n) }, "Saved") })
                        Gap(10.dp)
                    }
                    Button(onClick = { adding = true }, modifier = Modifier.fillMaxWidth(), shape = MaterialTheme.shapes.large) { Text("Add a model") }
                    Gap(16.dp)
                    TestQuestionsCard(repo, offerFor = offerTest, onOfferHandled = { offerTest = null })
                    Gap(16.dp)
                    if (d.profiles.size >= 2) RoutingCard(repo)
                    if (d.profiles.size >= 2) Gap(16.dp)
                    if (d.profiles.size >= 2) Fallbacks(d, onSave = { next -> post(buildJsonObject { put("fallback_order", JsonArray(next.map { JsonPrimitive(it) })) }, "Saved. It applies from the next message.") })
                }
                Gap(32.dp)
            }
        }
    }
    choosing?.let { (p, c) ->
        AlertDialog(
            onDismissRequest = { choosing = null }, title = { Text("Models for ${p.name}") },
            text = {
                Column {
                    Text(if (c.models.isEmpty()) "The service listed no models for this key." else if (c.listed) "This key can use these. It uses ${c.model} now." else "This key cannot use ${c.model}. Pick one it can use:", style = MaterialTheme.typography.bodySmall)
                    Gap(8.dp)
                    Column(Modifier.heightIn(max = 360.dp).verticalScroll(rememberScrollState())) {
                        c.models.forEach { id ->
                            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { choosing = null; post(buildJsonObject { put("name", p.name); put("model", id) }, "${p.name} now uses $id.") }, verticalAlignment = Alignment.CenterVertically) {
                                Text(id, style = MaterialTheme.typography.bodyMedium, color = if (id == c.model) MaterialTheme.extra.brassText else MaterialTheme.colorScheme.onSurface)
                            }
                        }
                    }
                }
            },
            confirmButton = { TextButton(onClick = { choosing = null }) { Text("Close") } },
        )
    }
    removing?.let { p ->
        AlertDialog(
            onDismissRequest = { removing = null }, title = { Text("Delete ${p.name}?") },
            text = { Text("The key is removed from the server's settings. Conversations that used it carry on with the default model.") },
            confirmButton = { TextButton(onClick = { removing = null; post(buildJsonObject { put("name", p.name); put("_delete", true) }, "Deleted") }) { Text("Delete", color = MaterialTheme.extra.bad) } },
            dismissButton = { TextButton(onClick = { removing = null }) { Text("Keep it") } },
        )
    }
}

@Composable
private fun ProfileCard(p: ModelProfile, repo: Repo, onTest: () -> Unit, onModels: () -> Unit, onRemove: () -> Unit, onNotes: (String) -> Unit) {
    var editing by remember(p.name, p.notes) { mutableStateOf(false) }
    var notes by remember(p.name, p.notes) { mutableStateOf(p.notes) }
    Panel(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text(p.name, style = MaterialTheme.typography.titleMedium)
                    Text(listOf(p.model, p.type.ifBlank { "openai" }).filter { it.isNotBlank() }.joinToString(" · "), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                when {
                    p.failing.isNotEmpty() -> Pill(if (p.failing_hard) "Not working" else "Resting", if (p.failing_hard) Tone.Bad else Tone.Brass)
                    !p.available -> Pill("No key", Tone.Neutral)
                    p.default -> Pill("Default", Tone.Brass)
                    else -> Pill("Ready", Tone.Ok)
                }
            }
            if (p.failing.isNotEmpty()) {
                Gap(6.dp)
                Text(p.failing + if (p.failing_hard) ". Work moves on to the next model until you fix it." else ". Work moves on to the next model meanwhile.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.extra.bad)
            } else if (!p.available && p.status.isNotBlank()) {
                Gap(6.dp)
                Text(p.status, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            if (p.best.isNotEmpty()) {
                Gap(6.dp)
                Text(p.best.joinToString(" · ") { strengthWords[it] ?: it }, style = MaterialTheme.typography.labelMedium, color = MaterialTheme.extra.brassText)
            }
            if (editing) {
                Gap(8.dp)
                OutlinedTextField(notes, { notes = it.take(200) }, Modifier.fillMaxWidth(), label = { Text("What it is for") }, placeholder = { Text("e.g. my free one: fine for summaries") }, minLines = 2, supportingText = { Text("The agent reads this when it picks a model for a task.") })
            } else if (p.notes.isNotBlank()) {
                Gap(6.dp)
                Text(p.notes, style = MaterialTheme.typography.bodyMedium)
            }
            Gap(8.dp)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                if (editing) {
                    SecondaryButton(onClick = { editing = false; onNotes(notes) }) { Text("Save") }
                    TextButton(onClick = { editing = false; notes = p.notes }) { Text("Cancel") }
                } else {
                    SecondaryButton(onClick = onTest, enabled = p.available) { Text("Test") }
                    if (p.available) TextButton(onClick = onModels) { Text("Models") }
                    TextButton(onClick = { editing = true }) { Text(if (p.notes.isBlank()) "What it is for" else "Edit note") }
                    TextButton(onClick = onRemove) { Text("Delete", color = MaterialTheme.extra.bad) }
                }
            }
        }
    }
}

@Composable
private fun AddModel(d: ModelsDto, onCancel: () -> Unit, onAdd: (kotlinx.serialization.json.JsonObject) -> Unit) {
    var preset by remember { mutableStateOf<ModelPreset?>(null) }
    var name by remember { mutableStateOf("") }
    var model by remember { mutableStateOf("") }
    var base by remember { mutableStateOf("") }
    var key by remember { mutableStateOf("") }
    var notes by remember { mutableStateOf("") }
    var best by remember { mutableStateOf(setOf<String>()) }
    LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp)) {
        item {
            Text("Pick a service to fill in its address and a starting model, then paste your key.", style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(4.dp))
            Gap(8.dp)
            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                d.presets.forEach { p ->
                    FilterChip(selected = preset?.label == p.label, onClick = {
                        preset = p; model = p.model; base = p.base_url
                        if (name.isBlank()) name = p.label.lowercase().replace(Regex("[^a-z0-9]+"), "-").trim('-')
                    }, label = { Text(p.label) })
                }
            }
            preset?.let { Gap(8.dp); Text(it.hint, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
            Gap(16.dp)
            OutlinedTextField(name, { name = it.lowercase().filter { c -> c.isLetterOrDigit() || c == '-' || c == '_' }.take(32) }, Modifier.fillMaxWidth(), label = { Text("Name") }, singleLine = true)
            Gap(10.dp)
            OutlinedTextField(model, { model = it.trim() }, Modifier.fillMaxWidth(), label = { Text("Model") }, singleLine = true)
            Gap(10.dp)
            OutlinedTextField(base, { base = it.trim() }, Modifier.fillMaxWidth(), label = { Text("Address") }, placeholder = { Text("https://api.example.com/v1") }, singleLine = true, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri))
            Gap(10.dp)
            OutlinedTextField(
                key, { key = it.trim() }, Modifier.fillMaxWidth(), label = { Text("API key") }, singleLine = true,
                visualTransformation = PasswordVisualTransformation(), keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                supportingText = { Text("Sent to your server and kept in its settings. Leave empty to use the server's ${preset?.key_env?.ifBlank { null } ?: "environment variable"}.") },
            )
            Gap(10.dp)
            Text("Good at", style = MaterialTheme.typography.labelLarge, modifier = Modifier.padding(start = 4.dp))
            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                d.strengths.forEach { s -> FilterChip(selected = s in best, onClick = { best = if (s in best) best - s else best + s }, label = { Text(strengthWords[s] ?: s) }) }
            }
            Gap(10.dp)
            OutlinedTextField(notes, { notes = it.take(200) }, Modifier.fillMaxWidth(), label = { Text("What it is for") }, placeholder = { Text("e.g. my free one: fine for summaries") }, minLines = 2)
            Gap(16.dp)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(onClick = {
                    onAdd(buildJsonObject {
                        put("name", name); put("type", preset?.type?.ifBlank { "openai" } ?: "openai")
                        if (model.isNotBlank()) put("model", model)
                        if (base.isNotBlank()) put("base_url", base)
                        if (key.isNotBlank()) put("api_key", key) else preset?.key_env?.takeIf { it.isNotBlank() }?.let { put("api_key_env", it) }
                        put("best_for", JsonArray(best.map { JsonPrimitive(it) }))
                        if (notes.isNotBlank()) put("notes", notes)
                    })
                }, enabled = name.isNotBlank() && model.isNotBlank(), modifier = Modifier.weight(1f), shape = MaterialTheme.shapes.large) { Text("Add") }
                SecondaryButton(onClick = onCancel) { Text("Cancel") }
            }
            Gap(32.dp)
        }
    }
}

@Composable
private fun Fallbacks(d: ModelsDto, onSave: (List<String>) -> Unit) {
    val names = d.profiles.map { it.name }
    val order = d.fallback_order.filter { it in names }
    val addable = names.filter { it !in order }
    SectionTitle("If a model stops working")
    Panel(Modifier.fillMaxWidth()) {
        Column {
            Text(
                "When a model is down, rate limited, out of credit or its key is refused, Talaria moves on to the next one here, for chats, background tasks and scheduled jobs. A model that just failed is left alone for a few minutes.",
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(16.dp),
            )
            order.forEachIndexed { i, n ->
                HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                val p = d.profiles.first { it.name == n }
                Row(Modifier.fillMaxWidth().heightIn(min = 56.dp).padding(start = 16.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text("${i + 1}. $n", style = MaterialTheme.typography.titleSmall)
                        Text(p.model + if (p.failing.isNotEmpty()) " · not working now: ${p.failing}" else "", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    IconButton(onClick = { onSave(order.toMutableList().also { java.util.Collections.swap(it, i, i - 1) }) }, enabled = i > 0) { Icon(Ti.ChevronUp, "Move up") }
                    IconButton(onClick = { onSave(order.toMutableList().also { java.util.Collections.swap(it, i, i + 1) }) }, enabled = i < order.lastIndex) { Icon(Ti.ChevronDown, "Move down") }
                    TextButton(onClick = { onSave(order - n) }) { Text("Take out") }
                }
            }
            if (order.isEmpty()) Text("No fallbacks yet: a model that stops working just fails.", style = MaterialTheme.typography.bodyMedium, modifier = Modifier.padding(start = 16.dp, end = 16.dp, bottom = 12.dp))
            if (addable.isNotEmpty()) {
                HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                FlowRow(Modifier.padding(16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    addable.forEach { n -> FilterChip(selected = false, onClick = { onSave(order + n) }, label = { Text("+ $n") }) }
                }
            }
        }
    }
}

/**
 * How the model for each message is chosen. The keyword rules always apply; smart routing adds a small model that
 * labels each message, and learning lets how replies were received (thumbs, "try a stronger model", corrections,
 * carrying on) move a model that has clearly done better into the lead for that kind of message.
 */
@Composable
private fun RoutingCard(repo: Repo) {
    val polled = rememberPolled(0, "routing" + repo.store.baseUrl) { repo.api.getJson<RoutingDto>("/api/routing") }
    val scope = rememberCoroutineScope()
    var confirmReset by remember { mutableStateOf(false) }
    val r = polled.value ?: return
    fun save(body: kotlinx.serialization.json.JsonObject) = scope.launch {
        try { repo.api.postJson("/api/routing", body); polled.refresh() } catch (e: Exception) { repo.toast(e.message ?: "That could not be saved.") }
    }
    SectionTitle("How Talaria chooses a model")
    Panel(Modifier.fillMaxWidth()) {
        Column {
            Text(
                "Each reply says which model wrote it and why. Thumbs and \"Try a stronger model\" under a reply teach it which model suits which kind of message.",
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(16.dp),
            )
            HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
            ToggleRow("Smart routing", "A small model labels each message when the keywords are not sure. It can add up to a couple of seconds before the first reply; the rules take over if it is slow.", r.smart) { save(buildJsonObject { put("smart", it) }) }
            if (r.smart) {
                Column(Modifier.padding(start = 16.dp, end = 16.dp, bottom = 12.dp)) {
                    Text("Model that labels messages", style = MaterialTheme.typography.labelLarge)
                    Gap(6.dp)
                    FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        FilterChip(selected = r.profile.isEmpty(), onClick = { save(buildJsonObject { put("profile", "") }) }, label = { Text("Automatic") })
                        r.profiles.forEach { n -> FilterChip(selected = r.profile == n, onClick = { save(buildJsonObject { put("profile", n) }) }, label = { Text(n) }) }
                    }
                }
            }
            HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
            ToggleRow("Learn from how replies are received", "Counts thumbs, retries, corrections and carrying on, and lets a model that has clearly done better on a kind of message take over for it.", r.learn) { save(buildJsonObject { put("learn", it) }) }
            if (r.rows.isEmpty()) {
                Text("Nothing learned yet.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 16.dp, end = 16.dp, bottom = 12.dp))
            } else {
                HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                Column(Modifier.padding(16.dp)) {
                    Text("What it has learned so far", style = MaterialTheme.typography.labelLarge)
                    r.rows.forEach { x ->
                        Gap(8.dp)
                        Text("${x.kind} → ${x.profile}", style = MaterialTheme.typography.bodyMedium)
                        Text(
                            listOfNotNull(x.Up.takeIf { it > 0 }?.let { "$it good" }, x.Down.takeIf { it > 0 }?.let { "$it poor" }, x.Retry.takeIf { it > 0 }?.let { "$it sent back" }, x.Correction.takeIf { it > 0 }?.let { "$it corrected" }, x.Continue.takeIf { it > 0 }?.let { "$it carried on" }).joinToString(" · ") + " · score ${"%.2f".format(x.score)}",
                            style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                    Gap(8.dp)
                    TextButton(onClick = { confirmReset = true }) { Text("Forget what it learned", color = MaterialTheme.extra.bad) }
                }
            }
        }
    }
    if (confirmReset) AlertDialog(
        onDismissRequest = { confirmReset = false }, title = { Text("Forget what it learned?") },
        text = { Text("The router goes back to its rules. Your settings stay.") },
        confirmButton = { TextButton(onClick = { confirmReset = false; save(buildJsonObject { put("reset", true) }) }) { Text("Forget", color = MaterialTheme.extra.bad) } },
        dismissButton = { TextButton(onClick = { confirmReset = false }) { Text("Keep it") } },
    )
}
