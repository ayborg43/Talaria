package app.talaria.ui.screens

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
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
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.data.FilesDto
import app.talaria.data.Repo
import app.talaria.ui.LocalOpenFile
import app.talaria.ui.MakeKind
import app.talaria.ui.ago
import app.talaria.ui.components.EmptyState
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.Panel
import app.talaria.ui.components.SecondaryButton
import app.talaria.ui.components.SkeletonCard
import app.talaria.ui.expiresIn
import app.talaria.ui.kindOf
import app.talaria.ui.madeFiles
import app.talaria.ui.rememberPolled
import app.talaria.ui.theme.Ti
import app.talaria.ui.theme.extra
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

fun kindIcon(k: MakeKind?): ImageVector = when (k) { MakeKind.Doc -> Ti.File; MakeKind.Page -> Ti.Globe; MakeKind.Slides -> Ti.Slides; MakeKind.Dashboard -> Ti.Chart; MakeKind.Pdf -> Ti.File; null -> Ti.File }

@Composable
fun kindTint(k: MakeKind?): Color = when (k) {
    MakeKind.Doc -> MaterialTheme.extra.info; MakeKind.Page -> MaterialTheme.extra.ok; MakeKind.Slides -> MaterialTheme.extra.brassText
    MakeKind.Dashboard -> MaterialTheme.extra.coral; MakeKind.Pdf -> MaterialTheme.colorScheme.onSurfaceVariant; null -> MaterialTheme.colorScheme.onSurfaceVariant
}

/** What the agent made for you: documents, pages, slides, dashboards and PDFs, with a way to ask for a new one. */
@Composable
fun MakeScreen(repo: Repo, modifier: Modifier, onStart: (String) -> Unit, onAllFiles: () -> Unit) {
    val files = rememberPolled(30_000, "make") { repo.api.getJson<FilesDto>("/api/files") }
    val open = LocalOpenFile.current
    var filter by rememberSaveable { mutableStateOf("all") }
    var refreshing by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val dto = files.value
    val made = remember(dto) { madeFiles(dto?.files.orEmpty()) }
    val kinds = remember(made) { made.mapNotNull { MakeKind.of(kindOf(it)) }.distinct().sortedBy { it.ordinal } }
    val shown = if (filter == "all") made else made.filter { kindOf(it) == filter }

    Column(modifier.fillMaxSize()) {
        TopAppBar(
            title = { Column { Text("Make", style = MaterialTheme.typography.headlineSmall); Text("What Talaria made for you", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } },
            actions = { IconButton(onClick = onAllFiles) { Icon(Ti.File, "All files") } },
            colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
        )
        PullToRefreshBox(refreshing, onRefresh = { scope.launch { refreshing = true; files.refresh(); delay(500); refreshing = false } }, modifier = Modifier.fillMaxSize()) {
            LazyVerticalGrid(GridCells.Adaptive(168.dp), Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 4.dp), horizontalArrangement = Arrangement.spacedBy(12.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                item(span = { GridItemSpan(maxLineSpan) }) {
                    Column {
                        Text("Start something", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 4.dp, top = 12.dp, bottom = 8.dp))
                        androidx.compose.foundation.layout.FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            MakeKind.startable.forEach { k ->
                                SecondaryButton(onClick = { onStart(k.ask) }, contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp)) {
                                    Icon(kindIcon(k), null, Modifier.size(18.dp), tint = kindTint(k)); Text(k.one, Modifier.padding(start = 8.dp))
                                }
                            }
                        }
                        files.error?.let { Gap(12.dp); ErrorNote(it) }
                    }
                }
                if (dto == null && files.error == null) item(span = { GridItemSpan(maxLineSpan) }) { SkeletonCard(lines = 2) }
                else if (made.isEmpty()) item(span = { GridItemSpan(maxLineSpan) }) { EmptyState("Nothing made yet", "Ask in Chat for a document, a web page, a slide deck or a dashboard, or tap one above.") }
                else {
                    item(span = { GridItemSpan(maxLineSpan) }) {
                        Row(Modifier.padding(top = 12.dp).horizontalScroll(androidx.compose.foundation.rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            FilterChip(selected = filter == "all", onClick = { filter = "all" }, label = { Text("All") })
                            kinds.forEach { k -> FilterChip(selected = filter == k.key, onClick = { filter = k.key }, label = { Text(k.label) }) }
                        }
                    }
                    items(shown, key = { it.id }) { f ->
                        val k = MakeKind.of(kindOf(f))
                        val tint = kindTint(k)
                        Panel(Modifier.fillMaxWidth().clip(MaterialTheme.shapes.large).clickable { open(f) }) {
                            Column(Modifier.padding(14.dp)) {
                                Box(Modifier.size(44.dp).clip(CircleShape).background(tint.copy(alpha = 0.14f)), contentAlignment = Alignment.Center) { Icon(kindIcon(k), null, Modifier.size(22.dp), tint = tint) }
                                Gap(12.dp)
                                Text(f.name.replace(Regex("\\.(html|pdf)$", RegexOption.IGNORE_CASE), ""), style = MaterialTheme.typography.titleSmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
                                Gap(2.dp)
                                Text("${k?.one.orEmpty()} · ${ago(f.created)}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1)
                                Text(expiresIn(f.created, dto?.retention_hours ?: 12), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1)
                            }
                        }
                    }
                    item(span = { GridItemSpan(maxLineSpan) }) {
                        Text("Made files are kept for ${dto?.retention_hours ?: 12} hours, then deleted. Open one and save it to keep it.", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 4.dp, top = 8.dp, bottom = 24.dp))
                    }
                }
            }
        }
    }
}
