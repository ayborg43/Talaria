package app.talaria.ui.screens

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
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
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.data.Repo
import app.talaria.data.UsageDay
import app.talaria.data.UsageDto
import app.talaria.data.UsageLine
import app.talaria.data.UsagePrice
import app.talaria.ui.Cost
import app.talaria.ui.components.Button
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.OutlinedTextField
import app.talaria.ui.components.Panel
import app.talaria.ui.components.SectionTitle
import app.talaria.ui.components.SkeletonCard
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.Ti
import app.talaria.ui.theme.extra
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/** What the models were asked and what that cost, over today, the week or the month, with the owner's own prices. */
@Composable
fun CostScreen(repo: Repo, onBack: () -> Unit, modifier: Modifier = Modifier) {
    BackHandler(onBack = onBack)
    var period by remember { mutableStateOf(Cost.Period.Week) }
    val base by repo.base.collectAsState()
    val usage = rememberPolled(0, "usage${period.days}$base") { repo.api.getJson<UsageDto>("/api/usage?days=${period.days}") }
    val u = usage.value

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Text("What the models cost", style = MaterialTheme.typography.titleLarge) },
            navigationIcon = { IconButton(onClick = onBack) { Icon(Ti.Back, "Back") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp)) {
            item {
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Cost.Period.entries.forEach { p -> FilterChip(selected = period == p, onClick = { period = p }, label = { Text(p.label) }) }
                }
                Gap(12.dp)
            }
            usage.error?.let { e -> item { ErrorNote(e); Gap(8.dp) } }
            when {
                u == null -> if (usage.error == null) item { SkeletonCard(lines = 3) }
                !u.available -> item { EmptyState("Not counted here", "This server does not keep a count of what the models were asked.") }
                u.report.total.totals.calls == 0L -> {
                    item { Panel(Modifier.fillMaxWidth()) { Text(u.summary.ifBlank { "Nothing was asked of a model in this time." }, Modifier.padding(16.dp), style = MaterialTheme.typography.bodyLarge) } }
                    item { Gap(4.dp); Text("Nothing has been counted yet. Once the models are asked something, it shows up here.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 4.dp)) }
                }
                else -> {
                    item { Panel(Modifier.fillMaxWidth()) { Text(u.summary, Modifier.padding(16.dp), style = MaterialTheme.typography.bodyLarge) } }
                    item { SectionTitle("Tokens per day"); DayChart(u.report.per_day) }
                    lines("By model", u.report.by_model)
                    lines("By kind of work", u.report.by_kind)
                    lines("By agent", u.report.by_agent)
                    lines("Who asked", u.report.by_source)
                }
            }
            if (u != null && u.available) {
                item { SectionTitle("Prices") }
                item {
                    Text(
                        "These are your own figures, in dollars for a million tokens. Talaria has no price list: a model with no price is counted in tokens only. Empty both boxes to remove a price.",
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 4.dp, vertical = 2.dp),
                    )
                    Gap(8.dp)
                }
                if (u.prices.isEmpty()) item { Panel(Modifier.fillMaxWidth()) { Text("There is no model to set a price for yet.", Modifier.padding(16.dp), style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } }
                else item {
                    Panel(Modifier.fillMaxWidth()) {
                        Column {
                            u.prices.forEachIndexed { i, p ->
                                if (i > 0) HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                                PriceRow(repo, p, onSaved = { usage.refresh() })
                            }
                        }
                    }
                }
            }
            item { Gap(32.dp) }
        }
    }
}

/** A heading and a card of rows, one per name; nothing at all when the report has none. */
private fun androidx.compose.foundation.lazy.LazyListScope.lines(title: String, rows: List<UsageLine>) {
    if (rows.isEmpty()) return
    item { SectionTitle(title) }
    item {
        Panel(Modifier.fillMaxWidth()) {
            Column {
                rows.forEachIndexed { i, l ->
                    if (i > 0) HorizontalDivider(Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
                    UsageRow(l)
                }
            }
        }
    }
}

@Composable
private fun UsageRow(l: UsageLine) {
    Row(Modifier.fillMaxWidth().heightIn(min = 60.dp).padding(horizontal = 16.dp, vertical = 10.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(l.name, style = MaterialTheme.typography.titleSmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
            Text(Cost.usage(l), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        Text(
            Cost.price(l), Modifier.padding(start = 12.dp), style = MaterialTheme.typography.titleSmall,
            color = if (l.priced) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }
}

/** Bars for each day, drawn here: no chart library. */
@Composable
private fun DayChart(days: List<UsageDay>) {
    val heights = remember(days) { Cost.barHeights(days) }
    val bar = MaterialTheme.colorScheme.primary
    val empty = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.6f)
    val peak = days.maxOfOrNull { it.tokens } ?: 0L
    val words = remember(days) { "Tokens per day. " + days.joinToString("; ") { Cost.dayWords(it) } }
    Panel(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp)) {
            Text("Busiest day: ${Cost.tokens(peak)} tokens", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Gap(8.dp)
            Canvas(Modifier.fillMaxWidth().height(96.dp).semantics { contentDescription = words }) {
                if (heights.isEmpty()) return@Canvas
                val gap = if (heights.size > 15) 2.dp.toPx() else 6.dp.toPx()
                val w = ((size.width - gap * (heights.size - 1)) / heights.size).coerceAtLeast(1f)
                heights.forEachIndexed { i, h ->
                    val x = i * (w + gap)
                    if (h <= 0f) drawRoundRect(empty, Offset(x, size.height - 2.dp.toPx()), Size(w, 2.dp.toPx()), CornerRadius(1.dp.toPx()))
                    else drawRoundRect(bar, Offset(x, size.height * (1 - h)), Size(w, size.height * h), CornerRadius(minOf(w / 2, 4.dp.toPx())))
                }
            }
            Gap(6.dp)
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                Text(days.firstOrNull()?.let { Cost.shortDate(it.date) }.orEmpty(), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                Text(days.lastOrNull()?.let { Cost.shortDate(it.date) }.orEmpty(), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }
}

/** One profile's price: two boxes and Save. Both empty removes it. */
@Composable
private fun PriceRow(repo: Repo, p: UsagePrice, onSaved: () -> Unit) {
    var priceIn by remember(p.profile, p.price_in) { mutableStateOf(Cost.priceText(p.price_in)) }
    var priceOut by remember(p.profile, p.price_out) { mutableStateOf(Cost.priceText(p.price_out)) }
    var note by remember(p.profile) { mutableStateOf<String?>(null) }
    var failed by remember(p.profile) { mutableStateOf(false) }
    var busy by remember(p.profile) { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val numbers = KeyboardOptions(keyboardType = KeyboardType.Decimal)

    Column(Modifier.fillMaxWidth().padding(16.dp)) {
        Text(p.profile, style = MaterialTheme.typography.titleSmall)
        if (p.model.isNotBlank()) Text(p.model, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
        Gap(10.dp)
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedTextField(priceIn, { priceIn = it; note = null }, label = { Text("In, $ per 1M") }, singleLine = true, keyboardOptions = numbers, modifier = Modifier.weight(1f), shape = MaterialTheme.shapes.medium)
            OutlinedTextField(priceOut, { priceOut = it; note = null }, label = { Text("Out, $ per 1M") }, singleLine = true, keyboardOptions = numbers, modifier = Modifier.weight(1f), shape = MaterialTheme.shapes.medium)
        }
        Gap(8.dp)
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Button(
                onClick = {
                    when (val e = Cost.parsePrice(priceIn, priceOut)) {
                        is Cost.PriceEdit.Bad -> { note = e.why; failed = true }
                        else -> {
                            busy = true; note = null
                            scope.launch {
                                try {
                                    repo.api.postJson("/api/usage", buildJsonObject {
                                        put("profile", p.profile)
                                        if (e is Cost.PriceEdit.Set) { put("price_in", e.priceIn); put("price_out", e.priceOut) }
                                        else { put("price_in", JsonNull); put("price_out", JsonNull) }
                                    })
                                    failed = false; note = if (e is Cost.PriceEdit.Set) "Saved." else "Price removed."
                                    onSaved()
                                } catch (ex: Exception) { failed = true; note = ex.message ?: "That could not be saved." }
                                busy = false
                            }
                        }
                    }
                },
                enabled = !busy, shape = MaterialTheme.shapes.medium,
            ) { Text("Save") }
            note?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = if (failed) MaterialTheme.extra.bad else MaterialTheme.extra.ok, modifier = Modifier.weight(1f)) }
        }
    }
}
