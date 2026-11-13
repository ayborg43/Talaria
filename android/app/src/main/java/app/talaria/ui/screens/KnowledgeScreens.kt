package app.talaria.ui.screens

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
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
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

@Serializable
data class EvalCase(
    val id: String = "", val prompt: String = "", val expect: List<String> = emptyList(), val any: List<String> = emptyList(),
    val avoid: List<String> = emptyList(), val rubric: String = "",
)

@Serializable
data class EvalResult(
    val caseId: String = "", val prompt: String = "", val reply: String = "", val pass: Boolean = false, val unchecked: Boolean = false,
    val missed: List<String> = emptyList(), val ms: Long = 0, val error: String = "",
)

@Serializable
data class EvalRun(
    val id: String = "", val profile: String = "", val model: String = "", val at: String = "", val passed: Int = 0, val counted: Int = 0,
    val total: Int = 0, val avgMs: Long = 0, val results: List<EvalResult> = emptyList(),
)

@Serializable
data class EvalsDto(
    val cases: List<EvalCase> = emptyList(), val runs: List<EvalRun> = emptyList(), val profiles: List<String> = emptyList(),
    val running: String = "", val done: Int = 0, val total: Int = 0, val failed: String = "",
)

private fun split(s: String) = s.split(',', '\n').map { it.trim() }.filter { it.isNotEmpty() }

/**
 * The person's own test questions, and how each model did on them. Writing what a good answer must contain makes a run a
 * number (8 of 10) and a list of what was missed, so a new model can be tried before it is trusted with real work.
 */
@Composable
fun TestQuestionsCard(repo: Repo, offerFor: String? = null, onOfferHandled: () -> Unit = {}) {
    val running = remember { mutableStateOf(false) }
    val polled = rememberPolled(0, "evals" + repo.store.baseUrl) { repo.api.getJson<EvalsDto>("/api/evals") }
    val scope = rememberCoroutineScope()
    var adding by remember { mutableStateOf(false) }
    var prompt by remember { mutableStateOf("") }
    var expect by remember { mutableStateOf("") }
    var avoid by remember { mutableStateOf("") }
    var error by remember { mutableStateOf<String?>(null) }
    var details by remember { mutableStateOf<EvalRun?>(null) }
    val d = polled.value
    running.value = d?.running?.isNotEmpty() == true
    // While a run goes on, ask again every few seconds to show how far it got.
    androidx.compose.runtime.LaunchedEffect(running.value) { while (running.value) { kotlinx.coroutines.delay(2500); polled.refresh() } }

    fun post(body: JsonObject, done: (() -> Unit)? = null) = scope.launch {
        try { repo.api.postJson("/api/evals", body); error = null; polled.refresh(); done?.invoke() } catch (e: Exception) { error = e.message }
    }
    if (d == null) return
    SectionTitle("Your test questions")
    Panel(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp)) {
            Text(
                "Write a few questions you actually ask, and what a good answer must contain. Run them on any model to see how it does before you trust it with real work.",
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            error?.let { Gap(8.dp); ErrorNote(it) }
            d.cases.forEach { c ->
                Gap(10.dp)
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text(c.prompt, style = MaterialTheme.typography.bodyMedium)
                        val checks = listOfNotNull(c.expect.takeIf { it.isNotEmpty() }?.let { "must include " + it.joinToString(", ") }, c.avoid.takeIf { it.isNotEmpty() }?.let { "must not say " + it.joinToString(", ") }, c.rubric.takeIf { it.isNotBlank() }?.let { "judged: $it" })
                        Text(checks.joinToString(" · ").ifBlank { "nothing to check: shown, not scored" }, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    TextButton(onClick = { post(buildJsonObject { put("delete", c.id) }) }) { Text("Delete", color = MaterialTheme.extra.bad) }
                }
            }
            Gap(10.dp)
            if (adding) {
                OutlinedTextField(prompt, { prompt = it.take(2000) }, Modifier.fillMaxWidth(), label = { Text("A question you ask") }, minLines = 2)
                Gap(8.dp)
                OutlinedTextField(expect, { expect = it }, Modifier.fillMaxWidth(), label = { Text("The answer must include") }, supportingText = { Text("Separate several with commas. Leave empty to only look at the answer.") }, singleLine = true)
                Gap(8.dp)
                OutlinedTextField(avoid, { avoid = it }, Modifier.fillMaxWidth(), label = { Text("It must not say (optional)") }, singleLine = true)
                Gap(10.dp)
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Button(onClick = {
                        post(buildJsonObject {
                            put("add", buildJsonObject { put("prompt", prompt); put("expect", JsonArray(split(expect).map { JsonPrimitive(it) })); put("avoid", JsonArray(split(avoid).map { JsonPrimitive(it) })) })
                        }) { adding = false; prompt = ""; expect = ""; avoid = "" }
                    }, enabled = prompt.isNotBlank()) { Text("Add") }
                    SecondaryButton(onClick = { adding = false }) { Text("Cancel") }
                }
            } else if (d.cases.size < 40) {
                SecondaryButton(onClick = { adding = true }) { Text("Add a question") }
            }

            if (d.cases.isNotEmpty()) {
                Gap(16.dp)
                HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                Gap(12.dp)
                Text("How each model did", style = MaterialTheme.typography.labelLarge)
                if (d.running.isNotEmpty()) {
                    Gap(6.dp)
                    Text("Testing ${d.running}… ${d.done} of ${d.total}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.extra.brassText)
                }
                if (d.failed.isNotEmpty()) { Gap(6.dp); Text(d.failed, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.extra.bad) }
                d.profiles.forEach { name ->
                    val last = d.runs.firstOrNull { it.profile == name }
                    Row(Modifier.fillMaxWidth().padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                        Column(Modifier.weight(1f)) {
                            Text(name, style = MaterialTheme.typography.bodyMedium)
                            Text(
                                last?.let { if (it.counted == 0) "ran, nothing to score" else "${it.passed} of ${it.counted} · ${"%.1f".format(it.avgMs / 1000.0)}s on average" } ?: "not tested yet",
                                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                        if (last != null) TextButton(onClick = { details = last }) { Text("Details") }
                        SecondaryButton(onClick = { post(buildJsonObject { put("run", name) }) }, enabled = d.running.isEmpty()) { Text("Run") }
                    }
                }
            }
        }
    }
    offerFor?.let { name ->
        if (d.cases.isEmpty()) onOfferHandled()
        else AlertDialog(
            onDismissRequest = onOfferHandled, title = { Text("Try $name on your questions?") },
            text = { Text("It will answer your ${d.cases.size} test question${if (d.cases.size == 1) "" else "s"} once, and you can see how it did before you rely on it. It costs a few small requests.") },
            confirmButton = { TextButton(onClick = { onOfferHandled(); post(buildJsonObject { put("run", name) }) }) { Text("Run them") } },
            dismissButton = { TextButton(onClick = onOfferHandled) { Text("Not now") } },
        )
    }
    details?.let { run ->
        AlertDialog(
            onDismissRequest = { details = null }, title = { Text("${run.profile}: ${run.passed} of ${run.counted}") },
            text = {
                Column(Modifier.padding(0.dp)) {
                    run.results.forEach { r ->
                        Text((if (r.unchecked) "– " else if (r.pass) "✓ " else "✗ ") + r.prompt, style = MaterialTheme.typography.bodyMedium)
                        r.missed.forEach { Text("   " + it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.extra.bad) }
                        if (r.reply.isNotBlank()) Text("   " + r.reply.take(160), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        Gap(6.dp)
                    }
                }
            },
            confirmButton = { TextButton(onClick = { details = null }) { Text("Close") } },
        )
    }
}

@Serializable
data class DocsStatus(val files: Int = 0, val folders: Int = 0, val notes: Boolean = false, val at: String = "", val skipped: Int = 0, val error: String = "")

@Serializable
data class DocsDto(val enabled: Boolean = false, val folders: List<String> = emptyList(), val status: DocsStatus = DocsStatus())

/** What the agent can search: the files Talaria keeps, its notes, and the folders you add on the server. */
@Composable
fun FilesSearchScreen(repo: Repo, onBack: () -> Unit, modifier: Modifier = Modifier) {
    BackHandler(onBack = onBack)
    val polled = rememberPolled(0, "docs" + repo.store.baseUrl) { repo.api.getJson<DocsDto>("/api/docs") }
    val scope = rememberCoroutineScope()
    var path by remember { mutableStateOf("") }
    var error by remember { mutableStateOf<String?>(null) }
    val d = polled.value
    fun post(body: JsonObject, done: (() -> Unit)? = null) = scope.launch {
        try { repo.api.postJson("/api/docs", body); error = null; polled.refresh(); done?.invoke() } catch (e: Exception) { error = e.message }
    }
    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("Search my files", style = MaterialTheme.typography.titleLarge) },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp)) {
            item {
                Text(
                    "Talaria can search your own files and notes when you ask something about them: \"what did the lease say about renewing?\". It keeps a plain text index on your server, so it uses disk, not memory, and it only ever reads.",
                    style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(4.dp),
                )
                Gap(12.dp)
                error?.let { ErrorNote(it); Gap(8.dp) }
                if (d == null) {
                    polled.error?.let { ErrorNote(it) }
                } else if (!d.enabled) {
                    Text("Search is switched off on this server.", style = MaterialTheme.typography.bodyMedium)
                } else {
                    SectionTitle("What is searched")
                    Panel(Modifier.fillMaxWidth()) {
                        Column(Modifier.padding(16.dp)) {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Text("Files Talaria keeps", Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium)
                                Pill("${d.status.files}", Tone.Neutral)
                            }
                            Gap(6.dp)
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Text("Its notes about you", Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium)
                                Pill(if (d.status.notes) "Yes" else "None yet", Tone.Neutral)
                            }
                            Gap(6.dp)
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Text("Files in your folders", Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium)
                                Pill("${d.status.folders}", Tone.Neutral)
                            }
                            if (d.status.skipped > 0) Text("${d.status.skipped} left out (too large, or not text)", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 6.dp))
                            if (d.status.error.isNotBlank()) Text(d.status.error, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.extra.bad, modifier = Modifier.padding(top = 6.dp))
                        }
                    }
                    SectionTitle("Folders on your server")
                    Panel(Modifier.fillMaxWidth()) {
                        Column(Modifier.padding(16.dp)) {
                            if (d.folders.isEmpty()) Text("None yet. Add the folder where your notes or documents live, as a full path on the machine Talaria runs on.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            d.folders.forEach { f ->
                                Row(verticalAlignment = Alignment.CenterVertically) {
                                    Text(f, Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium)
                                    TextButton(onClick = { post(buildJsonObject { put("folders", JsonArray((d.folders - f).map { JsonPrimitive(it) })) }) }) { Text("Remove", color = MaterialTheme.extra.bad) }
                                }
                            }
                            Gap(10.dp)
                            OutlinedTextField(path, { path = it }, Modifier.fillMaxWidth(), label = { Text("Folder") }, placeholder = { Text("/home/you/Notes") }, singleLine = true,
                                supportingText = { Text("Text, Markdown, web pages and PDFs are read. Places that hold secrets are never indexed.") })
                            Gap(8.dp)
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Button(onClick = { post(buildJsonObject { put("folders", JsonArray((d.folders + path.trim()).map { JsonPrimitive(it) })) }) { path = "" } }, enabled = path.isNotBlank()) { Text("Add folder") }
                                SecondaryButton(onClick = { post(buildJsonObject { put("reindex", true) }) }) { Text("Look again now") }
                            }
                        }
                    }
                }
                Gap(32.dp)
            }
        }
    }
}
