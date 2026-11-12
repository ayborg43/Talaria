package app.talaria.ui.screens

import app.talaria.ui.theme.Ti

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
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
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import app.talaria.ui.components.Button
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import app.talaria.ui.components.SecondaryButton
import androidx.compose.material3.PrimaryTabRow
import androidx.compose.material3.Surface
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import app.talaria.ui.components.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.data.AgentsDto
import app.talaria.data.JobInfo
import app.talaria.data.PersonaDto
import app.talaria.data.PersonasDto
import app.talaria.data.RecipeDto
import app.talaria.data.RecipesDto
import app.talaria.data.Repo
import app.talaria.data.RunSummary
import app.talaria.data.RunsDto
import app.talaria.data.SuggestionsDto
import app.talaria.data.TaskInfo
import app.talaria.data.Worker
import app.talaria.ui.ago
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.LiveDot
import app.talaria.ui.components.Panel
import app.talaria.ui.components.Pill
import app.talaria.ui.components.SectionTitle
import app.talaria.ui.components.Tone
import app.talaria.ui.elapsed
import app.talaria.ui.humanTool
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.extra
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

private val tabs = listOf("Active", "Up next", "History")

@androidx.compose.material3.ExperimentalMaterial3Api
@Composable
fun TasksHost(repo: Repo, modifier: Modifier, onOpenBrowser: () -> Unit, onChat: () -> Unit) {
    var run by remember { mutableStateOf<String?>(null) }
    var agent by remember { mutableStateOf<PersonaDto?>(null) }
    var creating by remember { mutableStateOf(false) }
    var tab by rememberSaveable { mutableIntStateOf(0) } // kept here so coming back from a run or a form lands on the same tab
    BackHandler(enabled = run != null) { run = null }
    BackHandler(enabled = agent != null || creating) { agent = null; creating = false }

    when {
        run != null -> RunDetailScreen(repo, run!!, onBack = { run = null }, modifier = modifier)
        agent != null || creating -> AgentFormScreen(repo, agent, onDone = { agent = null; creating = false }, modifier = modifier)
        else -> TasksScreen(repo, modifier, tab, { tab = it }, onRun = { run = it }, onEditAgent = { agent = it }, onNewAgent = { creating = true }, onOpenBrowser = onOpenBrowser, onChat = onChat)
    }
}

@androidx.compose.material3.ExperimentalMaterial3Api
@Composable
private fun TasksScreen(
    repo: Repo, modifier: Modifier, tab: Int, onTab: (Int) -> Unit, onRun: (String) -> Unit, onEditAgent: (PersonaDto) -> Unit,
    onNewAgent: () -> Unit, onOpenBrowser: () -> Unit, onChat: () -> Unit,
) {
    val workers by repo.workers.collectAsState()
    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("Tasks", style = MaterialTheme.typography.headlineSmall) },
            actions = { IconButton(onClick = onOpenBrowser) { Icon(Ti.Globe, "Open the agent's browser") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        PrimaryTabRow(selectedTabIndex = tab, containerColor = MaterialTheme.colorScheme.background, contentColor = MaterialTheme.colorScheme.onSurface) {
            tabs.forEachIndexed { i, t ->
                Tab(
                    selected = tab == i, onClick = { onTab(i) },
                    text = { Text(if (i == 0 && workers.any { it.kind != "main" }) "$t · ${workers.count { it.kind != "main" }}" else t, style = MaterialTheme.typography.labelLarge) },
                    selectedContentColor = MaterialTheme.colorScheme.onSurface, unselectedContentColor = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
        when (tab) {
            0 -> ActiveTab(repo, onRun)
            1 -> UpNextTab(repo, onEditAgent, onNewAgent, onOpenBrowser, onChat)
            else -> HistoryTab(repo, onRun)
        }
    }
}

// ---- Active ----

@androidx.compose.material3.ExperimentalMaterial3Api
@Composable
private fun ActiveTab(repo: Repo, onRun: (String) -> Unit) {
    val workers by repo.workers.collectAsState()
    val runs = app.talaria.ui.rememberPolled(4000, "runs") { repo.api.getJson<RunsDto>("/api/runs") }
    var refreshing by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    PullToRefreshBox(refreshing, onRefresh = { scope.launch { refreshing = true; runs.refresh(); kotlinx.coroutines.delay(500); refreshing = false } }, modifier = Modifier.fillMaxSize()) {
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp)) {
            val active = workers
            item {
                SectionTitle("Working now") {
                    if (active.isNotEmpty()) Pill("${active.size} running", Tone.Ok)
                }
            }
            if (active.isEmpty()) item {
                Panel(Modifier.fillMaxWidth()) { Text("Nothing is working right now. Ask for something in Chat, or start a task from Up next.", Modifier.padding(16.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) }
            } else item {
                Panel(Modifier.fillMaxWidth()) { Column { active.forEachIndexed { i, w -> if (i > 0) androidx.compose.material3.HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f)); WorkerRow(w) } } }
            }
            item { SectionTitle("What it did") }
            val list = runs.value?.runs.orEmpty()
            when {
                runs.loading && runs.value == null -> item { app.talaria.ui.components.SkeletonCard(lines = 2) }
                runs.value?.available == false -> item { EmptyState("Not recorded here", "This server does not keep a record of what its tasks did.") }
                list.isEmpty() && !runs.loading -> item { EmptyState("No runs yet", "When a background task, goal check-in or scheduled prompt runs, it shows up here with each step it took.") }
                else -> items(list, key = { it.id }) { r -> RunRow(r, onClick = { onRun(r.id) }); Gap(8.dp) }
            }
            runs.error?.let { item { ErrorNote(it) } }
        }
    }
}

@Composable
private fun WorkerRow(w: Worker) {
    val label = when (w.kind) { "main" -> "Talaria"; "sub" -> "Helper"; "task" -> "Task"; "goal" -> "Goal"; "watch" -> "Watch"; "job" -> "Scheduled" else -> w.kind }
    Row(Modifier.fillMaxWidth().heightIn(min = 60.dp).padding(horizontal = 16.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        LiveDot()
        Column(Modifier.weight(1f)) {
            Text(w.label.ifBlank { label }, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(if (w.doing.isNotBlank()) humanTool(w.doing) else "Thinking…", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        Column(horizontalAlignment = Alignment.End) {
            Pill(label, if (w.kind == "main") Tone.Brass else Tone.Neutral)
            Text(elapsed(w.ageMs) + if (w.model.isNotBlank()) " · ${w.model}" else "", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1)
        }
    }
}

@Composable
private fun RunRow(r: RunSummary, onClick: () -> Unit) {
    val (tone, word) = when (r.status) { "running" -> Tone.Ok to "Working"; "done" -> Tone.Neutral to "Done"; "failed" -> Tone.Bad to "Failed"; else -> Tone.Neutral to "Stopped" }
    Panel(Modifier.fillMaxWidth().clip(MaterialTheme.shapes.large).clickable(onClick = onClick)) {
        Column(Modifier.padding(horizontal = 16.dp, vertical = 12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(r.name.ifBlank { "Task" }, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f), maxLines = 2, overflow = TextOverflow.Ellipsis)
                Pill(word, tone)
            }
            Gap(4.dp)
            Text(
                if (r.status == "running" && r.doing.isNotBlank()) humanTool(r.doing)
                else "${if (r.ended.isNotBlank()) "Ended " + ago(r.ended) else "Started " + ago(r.started)} · ${r.steps} step${if (r.steps == 1) "" else "s"}${if (r.files > 0) " · ${r.files} file${if (r.files == 1) "" else "s"}" else ""}",
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis,
            )
        }
    }
}

// ---- Up next ----

@androidx.compose.material3.ExperimentalMaterial3Api
@Composable
private fun UpNextTab(repo: Repo, onEditAgent: (PersonaDto) -> Unit, onNewAgent: () -> Unit, onOpenBrowser: () -> Unit, onChat: () -> Unit) {
    val agents = rememberPolled(10_000, "agents") { repo.api.getJson<AgentsDto>("/api/agents") }
    val sugg = rememberPolled(0, "sugg") { repo.api.getJson<SuggestionsDto>("/api/suggestions") }
    val recipes = rememberPolled(0, "recipes") { repo.api.getJson<RecipesDto>("/api/recipes") }
    val personas = rememberPolled(0, "personas") { repo.api.getJson<PersonasDto>("/api/personas") }
    val scope = rememberCoroutineScope()
    var note by remember { mutableStateOf<String?>(null) }
    var failure by remember { mutableStateOf<String?>(null) }

    fun act(block: suspend () -> String?) = scope.launch {
        try { note = block(); failure = null; agents.refresh(); sugg.refresh(); personas.refresh() } catch (e: Exception) { failure = e.message }
    }

    LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp)) {
        note?.let { n -> item { Panel(Modifier.fillMaxWidth().padding(top = 8.dp), tint = MaterialTheme.extra.okTint, edge = MaterialTheme.extra.okLine) { Text(n, Modifier.padding(14.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.extra.ok) } } }
        failure?.let { f -> item { Gap(8.dp); ErrorNote(f) } }

        val jobs = agents.value?.jobs.orEmpty()
        item { SectionTitle("Running on its own") }
        if (jobs.isEmpty()) item { Panel(Modifier.fillMaxWidth()) { Text("Goals, watches and reminders you set up in Chat show here, with the next time each looks.", Modifier.padding(16.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } }
        else item {
            Panel(Modifier.fillMaxWidth()) { Column { jobs.forEachIndexed { i, j -> if (i > 0) androidx.compose.material3.HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f)); JobRow(j) { action -> act { repo.api.postJson("/api/jobs", buildJsonObject { put("kind", j.kind); put("id", j.id); put("action", action) }); null } } } } }
        }

        val ideas = sugg.value?.suggestions.orEmpty()
        if (ideas.isNotEmpty()) {
            item { SectionTitle("Suggested for you") }
            items(ideas, key = { it.id }) { s ->
                Panel(Modifier.fillMaxWidth().padding(bottom = 8.dp), tint = MaterialTheme.extra.brassSoft, edge = MaterialTheme.extra.brassLine) {
                    Column(Modifier.padding(14.dp)) {
                        Text(s.title, style = MaterialTheme.typography.titleSmall, color = MaterialTheme.extra.brassText)
                        if (s.body.isNotBlank()) { Gap(4.dp); Text(s.body, style = MaterialTheme.typography.bodySmall, maxLines = 4, overflow = TextOverflow.Ellipsis) }
                        Gap(8.dp)
                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            if (s.startable) Button(onClick = { act { repo.api.postJson("/api/suggestions", buildJsonObject { put("start", s.id) }); "Started. It will show under Active, and its result comes to your chat." } }, shape = MaterialTheme.shapes.medium) { Text("Start") }
                            TextButton(onClick = { act { repo.api.postJson("/api/suggestions", buildJsonObject { put("hide", s.id) }); null } }) { Text("Hide") }
                        }
                    }
                }
            }
        }

        item { SectionTitle("Your agents") }
        val list = personas.value?.agents.orEmpty()
        if (personas.value?.available == false) item { EmptyState("Not available here", "Named agents are not set up on this server.") }
        else {
            if (list.isEmpty()) item { Panel(Modifier.fillMaxWidth()) { Text("A named agent is a persona with its own instructions: an Inbox helper, a research partner. Make one, then chat with it.", Modifier.padding(16.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } }
            items(list, key = { it.id }) { a ->
                AgentCard(a, onChat = { act { repo.api.postJson("/api/personas", buildJsonObject { put("action", "chat"); put("id", a.id) }); repo.reload(); onChat(); null } }, onEdit = { onEditAgent(a) })
                Gap(8.dp)
            }
            if (list.size < (personas.value?.max ?: 8)) item { Gap(4.dp); SecondaryButton(onClick = onNewAgent, shape = MaterialTheme.shapes.large, modifier = Modifier.heightIn(min = 48.dp)) { Icon(Ti.Plus, null, Modifier.size(18.dp)); Text("New agent", Modifier.padding(start = 8.dp)) } }
        }

        item { SectionTitle("Routines") }
        item { RoutinesCard(repo, onOpenBrowser) }
        item { SectionTitle("Recipes") }
        val rs = recipes.value
        if (rs == null || !rs.available || rs.recipes.isEmpty()) item { EmptyState("No recipes", "Ready-made jobs show up here when the server has them.") }
        else rs.groups.forEach { g ->
            item { Text(g, style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(top = 8.dp, bottom = 6.dp)) }
            items(rs.recipes.filter { it.group == g }, key = { it.id }) { r ->
                RecipeCard(
                    r,
                    onAdd = { act { repo.api.postJson("/api/recipes", buildJsonObject { put("action", "add"); put("id", r.id) }).let(::messageOf) } },
                    onNow = { act { repo.api.postJson("/api/recipes", buildJsonObject { put("action", "now"); put("id", r.id) }).let(::messageOf) } },
                )
                Gap(8.dp)
            }
        }

        item { Gap(24.dp) }
    }
}

internal fun messageOf(raw: String): String? = runCatching { app.talaria.data.AppJson.parseToJsonElement(raw) as? kotlinx.serialization.json.JsonObject }.getOrNull()
    ?.let { o -> listOf("message", "read_back", "readback", "text").firstNotNullOfOrNull { (o[it] as? JsonPrimitive)?.takeIf { p -> p.isString }?.content } }

@Composable
private fun JobRow(j: JobInfo, onAct: (String) -> Unit) {
    var menu by remember { mutableStateOf(false) }
    val paused = j.state.contains("pause", ignoreCase = true)
    Row(Modifier.fillMaxWidth().heightIn(min = 64.dp).padding(start = 16.dp, end = 4.dp, top = 8.dp, bottom = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                Pill(when (j.kind) { "GOAL" -> "Goal"; "WATCH" -> "Watch"; else -> "Reminder" }, Tone.Info)
                if (paused) Pill("Paused")
            }
            Gap(4.dp)
            Text(j.title, style = MaterialTheme.typography.titleSmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
            if (j.sub.isNotBlank()) Text(j.sub, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        Box {
            IconButton(onClick = { menu = true }) { Icon(Ti.More, "More for ${j.title}") }
            DropdownMenu(menu, { menu = false }) {
                // A reminder is a single point in time: the server can only remove it, not pause it.
                if (j.kind != "TIME") DropdownMenuItem(text = { Text(if (paused) "Resume" else "Pause") }, leadingIcon = { Icon(if (paused) Ti.Play else Ti.Pause, null) }, onClick = { menu = false; onAct(if (paused) "resume" else "pause") })
                DropdownMenuItem(text = { Text("Remove") }, leadingIcon = { Icon(Ti.Trash, null) }, onClick = { menu = false; onAct("delete") })
            }
        }
    }
}

internal fun agentColor(name: String, extra: app.talaria.ui.theme.Extra, scheme: androidx.compose.material3.ColorScheme): Color = when (name) {
    "brass" -> scheme.primary; "info" -> extra.info; "ok" -> extra.ok; "coral" -> extra.coral
    "plum" -> Color(0xFF9A6BB5); "slate" -> Color(0xFF7B8CA0); else -> scheme.secondary
}

@Composable
private fun AgentCard(a: PersonaDto, onChat: () -> Unit, onEdit: () -> Unit) {
    val c = agentColor(a.color, MaterialTheme.extra, MaterialTheme.colorScheme)
    Panel(Modifier.fillMaxWidth().clip(MaterialTheme.shapes.large).clickable(onClick = onEdit)) {
        Row(Modifier.padding(14.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(14.dp)) {
            Box(Modifier.size(44.dp).clip(CircleShape).background(c.copy(alpha = 0.22f)), contentAlignment = Alignment.Center) {
                Text(a.icon.ifBlank { a.name.take(1).uppercase() }, style = MaterialTheme.typography.titleMedium, color = c)
            }
            Column(Modifier.weight(1f)) {
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(a.name, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                    if (a.read_only) Pill("Looks only", Tone.Info)
                }
                Text(a.instructions.take(120), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
            Button(onClick = onChat, shape = MaterialTheme.shapes.medium, contentPadding = PaddingValues(horizontal = 14.dp)) { Text("Chat") }
        }
    }
}


@Composable
private fun RecipeCard(r: RecipeDto, onAdd: () -> Unit, onNow: () -> Unit) {
    Panel(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Icon(Ti.Sparkles, null, Modifier.size(18.dp), tint = MaterialTheme.extra.brassText)
                Text(r.title, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                if (r.`when`.isNotBlank()) Pill("${r.`when`}${if (r.days.isNotBlank()) " · ${r.days}" else ""}")
            }
            Gap(4.dp)
            Text(r.summary, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Gap(8.dp)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                SecondaryButton(onClick = onAdd, shape = MaterialTheme.shapes.medium) { Text(if (r.kind == "schedule") "Add to schedule" else "Add") }
                TextButton(onClick = onNow) { Text("Run now") }
            }
        }
    }
}

@Composable
private fun RoutinesCard(repo: Repo, onOpenBrowser: () -> Unit) {
    val routines = rememberPolled(0, "routines") { repo.api.get("/api/routines") }
    val parsed = remember(routines.value) {
        runCatching {
            val o = app.talaria.data.AppJson.parseToJsonElement(routines.value ?: "{}") as kotlinx.serialization.json.JsonObject
            (o["routines"] as? kotlinx.serialization.json.JsonArray).orEmpty().mapNotNull { it as? kotlinx.serialization.json.JsonObject }
                .map { Triple((it["id"] as? JsonPrimitive)?.content.orEmpty(), (it["name"] as? JsonPrimitive)?.content.orEmpty(), (it["steps"] as? kotlinx.serialization.json.JsonArray)?.size ?: 0) }
        }.getOrDefault(emptyList())
    }
    val scope = rememberCoroutineScope()
    var note by remember { mutableStateOf<String?>(null) }
    Panel(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(14.dp)) {
            if (parsed.isEmpty()) {
                Text("Show it something once and it does it again. In the agent's browser, press Record, do it by hand, and save it as a routine.", style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Gap(10.dp)
                SecondaryButton(onClick = onOpenBrowser, shape = MaterialTheme.shapes.medium) { Text("Open the browser to record") }
            } else {
                parsed.forEach { (id, name, steps) ->
                    Row(Modifier.fillMaxWidth().heightIn(min = 52.dp), verticalAlignment = Alignment.CenterVertically) {
                        Column(Modifier.weight(1f)) { Text(name, style = MaterialTheme.typography.titleSmall); Text("$steps ${if (steps == 1) "step" else "steps"}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                        SecondaryButton(onClick = { scope.launch { note = try { messageOf(repo.api.postJson("/api/routines", buildJsonObject { put("action", "run"); put("id", id) })) ?: "Started." } catch (e: Exception) { e.message } } }, shape = MaterialTheme.shapes.medium) { Icon(Ti.Play, null, Modifier.size(18.dp)); Text("Run now", Modifier.padding(start = 4.dp)) }
                    }
                }
                note?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                Gap(8.dp)
                TextButton(onClick = onOpenBrowser) { Text("Record another") }
            }
        }
    }
}

// ---- History ----

@androidx.compose.material3.ExperimentalMaterial3Api
@Composable
private fun HistoryTab(repo: Repo, onRun: (String) -> Unit) {
    val agents = rememberPolled(10_000, "history") { repo.api.getJson<AgentsDto>("/api/agents") }
    val scope = rememberCoroutineScope()
    var failure by remember { mutableStateOf<String?>(null) }
    var refreshing by remember { mutableStateOf(false) }
    PullToRefreshBox(refreshing, onRefresh = { scope.launch { refreshing = true; agents.refresh(); kotlinx.coroutines.delay(500); refreshing = false } }, modifier = Modifier.fillMaxSize()) {
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp)) {
            failure?.let { f -> item { ErrorNote(f) } }
            val tasks = agents.value?.tasks.orEmpty()
            item { SectionTitle("Finished tasks") }
            if (tasks.isEmpty()) item { EmptyState("Nothing finished yet", "Tasks that have ended lately are kept here so you can read what they found.") }
            items(tasks, key = { it.id }) { t ->
                TaskRow(
                    t, onOpen = { onRun(t.id) },
                    onRetry = { scope.launch { try { repo.api.postJson("/api/tasks", buildJsonObject { put("retry", t.id) }); agents.refresh() } catch (e: Exception) { failure = e.message } } },
                    onReview = { scope.launch { try { repo.api.postJson("/api/tasks", buildJsonObject { put("review", t.id) }); agents.refresh() } catch (e: Exception) { failure = e.message } } },
                )
                Gap(8.dp)
            }
            val recent = agents.value?.recent.orEmpty()
            if (recent.isNotEmpty()) {
                item { SectionTitle("Helpers and checks that ran") }
                item {
                    Panel(Modifier.fillMaxWidth()) {
                        Column {
                            recent.forEachIndexed { i, f ->
                                if (i > 0) androidx.compose.material3.HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                                Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically) {
                                    Column(Modifier.weight(1f)) {
                                        Text(f.label, style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                        Text("${f.seconds} s${if (f.model.isNotBlank()) " · ${f.model}" else ""} · ${ago(f.at)}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                    }
                                    if (f.stopped) Pill("Stopped")
                                }
                            }
                        }
                    }
                }
            }
            agents.error?.let { item { Gap(8.dp); ErrorNote(it) } }
            item { Gap(24.dp) }
        }
    }
}

@Composable
private fun TaskRow(t: TaskInfo, onOpen: () -> Unit, onRetry: () -> Unit, onReview: () -> Unit) {
    Panel(Modifier.fillMaxWidth().clip(MaterialTheme.shapes.large).clickable(onClick = onOpen)) {
        Column(Modifier.padding(horizontal = 16.dp, vertical = 12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(t.name, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f), maxLines = 2, overflow = TextOverflow.Ellipsis)
                if (t.failed) Pill("Failed", Tone.Bad) else if (t.reviewed) Pill("Checked", Tone.Ok) else Pill("Done")
            }
            Gap(2.dp)
            Text((if (t.status.equals("ok", true) || t.status.isBlank()) "" else t.status + " · ") + ago(t.at), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis)
            if (!t.reviewed || t.failed) {
                Gap(6.dp)
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    if (!t.reviewed && !t.failed) SecondaryButton(onClick = onReview, shape = MaterialTheme.shapes.medium) { Icon(Ti.Check, null, Modifier.size(16.dp)); Text("Mark as checked", Modifier.padding(start = 6.dp)) }
                    if (t.failed) SecondaryButton(onClick = onRetry, shape = MaterialTheme.shapes.medium) { Icon(Ti.Refresh, null, Modifier.size(16.dp)); Text("Try again", Modifier.padding(start = 6.dp)) }
                }
            }
        }
    }
}
